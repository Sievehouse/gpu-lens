// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/config"
	"github.com/Sievehouse/gpu-lens/internal/domain"
)

// NvidiaSMISource reads GPU telemetry from the NVIDIA driver on the machine GPU Lens itself runs on, by invoking
// the `nvidia-smi` query interface. It is the live source for standalone GPU hosts and workstations that have no
// dcgm-exporter / Prometheus / Kubernetes stack.
//
// Safety boundary: only a fixed allow-list of read-only query invocations is ever executed (see
// validateSMIArgs). No shell is involved, arguments are never built from user input, every call has a bounded
// timeout, and nothing that changes GPU or driver state (power limits, compute mode, resets, diagnostics) can
// be issued through this adapter.
type NvidiaSMISource struct {
	explicitPath string
	timeout      time.Duration
	run          CommandRunner

	mu         sync.Mutex
	resolved   string
	fieldsFor  string
	fields     []string
	fieldsNote string
}

// CommandRunner executes a local command and returns its output. It exists so tests can substitute a fake.
type CommandRunner func(ctx context.Context, name string, args ...string) (stdout []byte, stderr []byte, exitCode int, err error)

// NewNvidiaSMISource constructs the local NVIDIA driver adapter. explicitPath may be empty (auto-discover).
func NewNvidiaSMISource(explicitPath string, timeout time.Duration, runner CommandRunner) *NvidiaSMISource {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	if runner == nil {
		runner = execRunner
	}
	return &NvidiaSMISource{explicitPath: explicitPath, timeout: timeout, run: runner}
}

func (ns *NvidiaSMISource) ID() string   { return "nvidia_smi" }
func (ns *NvidiaSMISource) Name() string { return "Local NVIDIA driver (nvidia-smi)" }

const maxCommandOutput = 4 * 1024 * 1024

type cappedBuffer struct {
	buf bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := maxCommandOutput - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func execRunner(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr cappedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
		err = nil
	}
	return stdout.buf.Bytes(), stderr.buf.Bytes(), code, err
}

var smiFieldPattern = regexp.MustCompile(`^[a-z0-9_.]+$`)

// validateSMIArgs is the allow-list for every nvidia-smi invocation made by GPU Lens.
func validateSMIArgs(args []string) error {
	if len(args) == 1 && args[0] == "--help-query-gpu" {
		return nil
	}
	if len(args) != 2 || args[1] != "--format=csv,noheader,nounits" {
		return fmt.Errorf("nvidia-smi invocation rejected by allow-list: %q", args)
	}
	for _, prefix := range []string{"--query-gpu=", "--query-compute-apps=", "--query-remapped-rows="} {
		if strings.HasPrefix(args[0], prefix) {
			for _, f := range strings.Split(strings.TrimPrefix(args[0], prefix), ",") {
				if !smiFieldPattern.MatchString(f) {
					return fmt.Errorf("nvidia-smi query field rejected by allow-list: %q", f)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("nvidia-smi invocation rejected by allow-list: %q", args)
}

// ErrNvidiaSMINotFound means no nvidia-smi binary exists on this machine.
var ErrNvidiaSMINotFound = errors.New("nvidia-smi not found on this machine")

func smiCandidatePaths() []string {
	if runtime.GOOS == "windows" {
		var out []string
		if root := os.Getenv("SystemRoot"); root != "" {
			out = append(out, filepath.Join(root, "System32", "nvidia-smi.exe"))
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			out = append(out, filepath.Join(pf, "NVIDIA Corporation", "NVSMI", "nvidia-smi.exe"))
		}
		return out
	}
	return []string{
		"/usr/bin/nvidia-smi",
		"/usr/local/bin/nvidia-smi",
		"/usr/local/nvidia/bin/nvidia-smi",
		"/run/nvidia/driver/usr/bin/nvidia-smi",
		"/usr/lib/wsl/lib/nvidia-smi",
	}
}

// ResolvePath locates the nvidia-smi binary: the explicit path if configured, else PATH, else well-known locations.
func (ns *NvidiaSMISource) ResolvePath() (string, error) {
	if ns.explicitPath != "" {
		if info, err := os.Stat(ns.explicitPath); err == nil && !info.IsDir() {
			return ns.explicitPath, nil
		}
		return "", fmt.Errorf("configured nvidia-smi path %q does not exist", ns.explicitPath)
	}
	if p, err := exec.LookPath("nvidia-smi"); err == nil {
		return p, nil
	}
	for _, cand := range smiCandidatePaths() {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand, nil
		}
	}
	return "", ErrNvidiaSMINotFound
}

func (ns *NvidiaSMISource) exec(ctx context.Context, path string, args ...string) (string, error) {
	if err := validateSMIArgs(args); err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, ns.timeout)
	defer cancel()
	stdout, stderr, code, err := ns.run(cctx, path, args...)
	if cctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("nvidia-smi timed out after %s (possible driver hang or GPU fallen off the bus)", ns.timeout)
	}
	if err != nil {
		return "", fmt.Errorf("nvidia-smi could not be started: %v", err)
	}
	if code != 0 {
		return string(stdout), &SMIExitError{Code: code, Message: firstMeaningfulLine(string(stderr), string(stdout))}
	}
	return string(stdout), nil
}

// SMIExitError is a non-zero nvidia-smi exit together with the first line it printed.
type SMIExitError struct {
	Code    int
	Message string
}

func (e *SMIExitError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("nvidia-smi exited with status %d", e.Code)
	}
	return fmt.Sprintf("nvidia-smi exited with status %d: %s", e.Code, e.Message)
}

func firstMeaningfulLine(candidates ...string) string {
	for _, text := range candidates {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				if len(line) > 240 {
					line = line[:240] + "…"
				}
				return line
			}
		}
	}
	return ""
}

// Core fields exist in every nvidia-smi release of the last decade; extended fields are used only when the
// installed driver lists them in `--help-query-gpu`. `name` is always requested last so a model name that
// contains a comma cannot shift the numeric columns.
var smiCoreFields = []string{
	"index", "uuid", "pci.bus_id", "driver_version", "temperature.gpu", "utilization.gpu", "utilization.memory",
	"memory.total", "memory.used", "memory.free", "power.draw", "power.limit", "fan.speed",
}

var smiExtendedFields = []string{
	"temperature.memory", "memory.reserved", "pstate",
	"ecc.errors.uncorrected.volatile.total", "ecc.errors.corrected.volatile.total",
	"pcie.link.gen.current", "pcie.link.gen.max", "pcie.link.width.current", "pcie.link.width.max",
	"clocks_throttle_reasons.hw_slowdown", "clocks_throttle_reasons.hw_thermal_slowdown",
	"clocks_throttle_reasons.hw_power_brake_slowdown", "clocks_throttle_reasons.sw_thermal_slowdown",
	"clocks_throttle_reasons.sw_power_cap",
	"clocks_event_reasons.hw_slowdown", "clocks_event_reasons.hw_thermal_slowdown",
	"clocks_event_reasons.hw_power_brake_slowdown", "clocks_event_reasons.sw_thermal_slowdown",
	"clocks_event_reasons.sw_power_cap",
}

var smiHelpFieldPattern = regexp.MustCompile(`"([a-z0-9_.]+)"`)

// parseSMIHelpFields extracts the valid --query-gpu property names from `nvidia-smi --help-query-gpu` output.
func parseSMIHelpFields(help string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(help, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `"`) {
			continue
		}
		for _, m := range smiHelpFieldPattern.FindAllStringSubmatch(line, -1) {
			out[m[1]] = true
		}
	}
	return out
}

func (ns *NvidiaSMISource) queryFields(ctx context.Context, path string) ([]string, string) {
	ns.mu.Lock()
	if ns.fieldsFor == path && ns.fields != nil {
		f, note := ns.fields, ns.fieldsNote
		ns.mu.Unlock()
		return f, note
	}
	ns.mu.Unlock()

	fields := append([]string(nil), smiCoreFields...)
	note := "core fields only (driver field list unavailable)"
	if help, err := ns.exec(ctx, path, "--help-query-gpu"); err == nil {
		supported := parseSMIHelpFields(help)
		if len(supported) > 0 {
			fields = fields[:0]
			for _, f := range smiCoreFields {
				if supported[f] {
					fields = append(fields, f)
				}
			}
			extended := 0
			for _, f := range smiExtendedFields {
				if supported[f] {
					fields = append(fields, f)
					extended++
				}
			}
			note = fmt.Sprintf("%d core + %d extended fields supported by this driver", len(fields)-extended, extended)
		}
	}
	fields = append(fields, "name")

	ns.mu.Lock()
	ns.fieldsFor, ns.fields, ns.fieldsNote = path, fields, note
	ns.mu.Unlock()
	return fields, note
}

func (ns *NvidiaSMISource) fallbackToCoreFields(path string) []string {
	fields := append(append([]string(nil), smiCoreFields...), "name")
	ns.mu.Lock()
	ns.fieldsFor, ns.fields, ns.fieldsNote = path, fields, "core fields only (extended query was rejected by this driver)"
	ns.mu.Unlock()
	return fields
}

// smiNumber parses a numeric nvidia-smi CSV cell. Cells such as "[N/A]", "[Not Supported]" or "[Unknown Error]"
// mean the value is unavailable; they are reported as (0, false) and never turned into a number.
func smiNumber(cell string) (float64, bool) {
	cell = strings.TrimSpace(cell)
	if cell == "" || strings.HasPrefix(cell, "[") || strings.EqualFold(cell, "N/A") {
		return 0, false
	}
	if i := strings.IndexByte(cell, ' '); i > 0 {
		cell = cell[:i]
	}
	v, err := strconv.ParseFloat(cell, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func smiFlagActive(cell string) bool {
	return strings.EqualFold(strings.TrimSpace(cell), "Active")
}

func smiYes(cell string) (bool, bool) {
	c := strings.ToLower(strings.TrimSpace(cell))
	switch c {
	case "yes", "true", "1":
		return true, true
	case "no", "false", "0":
		return false, true
	}
	return false, false
}

// NvidiaSMIResult is the outcome of one local driver query.
type NvidiaSMIResult struct {
	BinaryPath    string
	DriverVersion string
	Hostname      string
	GPUs          []domain.GPUDevice
	Extras        map[string]*GPUExtras
	Processes     map[string][]GPUProcess
	Exported      map[string]bool
	FieldsNote    string
	Notes         []string
	NoDevices     bool
}

// LocalNodeName returns the name used for GPUs on this machine: NODE_NAME when set (Kubernetes downward API),
// otherwise the OS hostname.
func LocalNodeName() string {
	if n := strings.TrimSpace(os.Getenv("NODE_NAME")); n != "" {
		return n
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "localhost"
}

// Collect runs the read-only driver queries and normalizes them into GPU devices.
func (ns *NvidiaSMISource) Collect(ctx context.Context, mapping config.MetricMappingConfig) (*NvidiaSMIResult, error) {
	path, err := ns.ResolvePath()
	if err != nil {
		return nil, err
	}
	fields, note := ns.queryFields(ctx, path)
	out, err := ns.exec(ctx, path, "--query-gpu="+strings.Join(fields, ","), "--format=csv,noheader,nounits")
	if err != nil && strings.Contains(strings.ToLower(err.Error()+out), "not a valid field") {
		fields = ns.fallbackToCoreFields(path)
		note = "core fields only (extended query was rejected by this driver)"
		out, err = ns.exec(ctx, path, "--query-gpu="+strings.Join(fields, ","), "--format=csv,noheader,nounits")
	}
	res := &NvidiaSMIResult{
		BinaryPath: path,
		Hostname:   LocalNodeName(),
		Extras:     map[string]*GPUExtras{},
		Processes:  map[string][]GPUProcess{},
		Exported:   map[string]bool{},
		FieldsNote: note,
	}
	if err != nil {
		var exitErr *SMIExitError
		if errors.As(err, &exitErr) && strings.Contains(strings.ToLower(exitErr.Message), "no devices were found") {
			res.NoDevices = true
			res.Notes = append(res.Notes, "The NVIDIA driver is loaded but reports no devices.")
			return res, nil
		}
		return nil, err
	}

	now := time.Now().UTC()
	col := map[string]int{}
	for i, f := range fields {
		col[f] = i
	}
	cell := func(cells []string, field string) string {
		if i, ok := col[field]; ok && i < len(cells) {
			return cells[i]
		}
		return ""
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cells := strings.SplitN(line, ",", len(fields))
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(cells) < len(fields) {
			res.Notes = append(res.Notes, "Skipped an nvidia-smi row with an unexpected number of columns.")
			continue
		}
		uuid := cell(cells, "uuid")
		if uuid == "" || strings.HasPrefix(uuid, "[") {
			res.Notes = append(res.Notes, "Skipped an nvidia-smi row without a readable GPU UUID.")
			continue
		}
		idx := 0
		if v, ok := smiNumber(cell(cells, "index")); ok {
			idx = int(v)
		}
		dev := domain.GPUDevice{
			UUID:          uuid,
			DeviceIndex:   idx,
			NodeName:      res.Hostname,
			ModelName:     cell(cells, "name"),
			PCIBusID:      cell(cells, "pci.bus_id"),
			DriverVersion: cell(cells, "driver_version"),
			MappingStatus: domain.MappingUnallocated,
			LastSampleAt:  now,
			Latest:        domain.GPUMetricPoint{Timestamp: now},
		}
		if res.DriverVersion == "" && !strings.HasPrefix(dev.DriverVersion, "[") {
			res.DriverVersion = dev.DriverVersion
		}
		ex := &GPUExtras{Sources: []string{"nvidia_smi"}, ThrottleReasons: []string{}, Processes: []GPUProcess{}}

		if v, ok := smiNumber(cell(cells, "utilization.gpu")); ok {
			dev.Latest.GPUUtilPercent = v
			res.Exported[mapping.GPUUtilMetric] = true
		}
		if v, ok := smiNumber(cell(cells, "utilization.memory")); ok {
			dev.Latest.MemCopyUtilPercent = v
			res.Exported[mapping.MemCopyUtilMetric] = true
		}
		if v, ok := smiNumber(cell(cells, "memory.used")); ok {
			dev.Latest.FBUsedMiB = v
			res.Exported[mapping.FBUsedMetric] = true
		}
		if v, ok := smiNumber(cell(cells, "memory.free")); ok {
			dev.Latest.FBFreeMiB = v
			res.Exported[mapping.FBFreeMetric] = true
		}
		if v, ok := smiNumber(cell(cells, "memory.total")); ok {
			dev.TotalMemoryMiB = v
		} else if dev.Latest.FBUsedMiB+dev.Latest.FBFreeMiB > 0 {
			dev.TotalMemoryMiB = dev.Latest.FBUsedMiB + dev.Latest.FBFreeMiB
		}
		if v, ok := smiNumber(cell(cells, "temperature.gpu")); ok {
			dev.Latest.GPUTempCelsius = v
			res.Exported[mapping.GPUTempMetric] = true
		}
		if v, ok := smiNumber(cell(cells, "temperature.memory")); ok {
			dev.Latest.MemoryTempCelsius = v
			res.Exported[mapping.MemoryTempMetric] = true
		}
		if v, ok := smiNumber(cell(cells, "power.draw")); ok {
			dev.Latest.PowerUsageWatts = v
			res.Exported[mapping.PowerUsageMetric] = true
		}
		if v, ok := smiNumber(cell(cells, "power.limit")); ok {
			dev.PowerLimitWatts = v
		}
		if v, ok := smiNumber(cell(cells, "fan.speed")); ok {
			fan := v
			ex.FanSpeedPercent = &fan
		}
		if v, ok := smiNumber(cell(cells, "ecc.errors.uncorrected.volatile.total")); ok {
			n := int(v)
			dev.Latest.ECCDBEVolTotal = &n
			res.Exported[mapping.ECCDBEVolTotalMetric] = true
		}
		if v, ok := smiNumber(cell(cells, "ecc.errors.corrected.volatile.total")); ok {
			n := int(v)
			dev.Latest.ECCSBEVolTotal = &n
			res.Exported[mapping.ECCSBEVolTotalMetric] = true
		}
		if p := cell(cells, "pstate"); p != "" && !strings.HasPrefix(p, "[") {
			ex.PState = p
		}
		genCur, okGC := smiNumber(cell(cells, "pcie.link.gen.current"))
		genMax, okGM := smiNumber(cell(cells, "pcie.link.gen.max"))
		wCur, okWC := smiNumber(cell(cells, "pcie.link.width.current"))
		wMax, okWM := smiNumber(cell(cells, "pcie.link.width.max"))
		if okGC && okGM && okWC && okWM {
			ex.PCIe = &PCIeLink{GenCurrent: int(genCur), GenMax: int(genMax), WidthCurrent: int(wCur), WidthMax: int(wMax)}
		}
		for _, family := range []string{"clocks_event_reasons", "clocks_throttle_reasons"} {
			for _, reason := range []string{"hw_slowdown", "hw_thermal_slowdown", "hw_power_brake_slowdown", "sw_thermal_slowdown", "sw_power_cap"} {
				if smiFlagActive(cell(cells, family+"."+reason)) {
					ex.ThrottleReasons = appendUnique(ex.ThrottleReasons, reason)
				}
			}
		}
		for _, c := range cells {
			if c == "[GPU is lost]" {
				res.Notes = append(res.Notes, fmt.Sprintf("GPU %d (%s) reports \"GPU is lost\" for at least one field.", idx, uuid))
				break
			}
		}
		res.GPUs = append(res.GPUs, dev)
		res.Extras[uuid] = ex
	}

	ns.collectProcesses(ctx, path, res)
	ns.collectRemappedRows(ctx, path, mapping, res)
	return res, nil
}

func (ns *NvidiaSMISource) collectProcesses(ctx context.Context, path string, res *NvidiaSMIResult) {
	out, err := ns.exec(ctx, path, "--query-compute-apps=gpu_uuid,pid,used_memory,process_name", "--format=csv,noheader,nounits")
	if err != nil {
		res.Notes = append(res.Notes, "Compute process list unavailable: "+err.Error())
		return
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cells := strings.SplitN(line, ",", 4)
		if len(cells) < 4 {
			continue
		}
		uuid := strings.TrimSpace(cells[0])
		pid, okPID := smiNumber(cells[1])
		if uuid == "" || !okPID {
			continue
		}
		proc := GPUProcess{PID: int(pid), Name: strings.TrimSpace(cells[3])}
		if mem, ok := smiNumber(cells[2]); ok {
			m := mem
			proc.UsedMemoryMiB = &m
		}
		res.Processes[uuid] = append(res.Processes[uuid], proc)
		if ex, ok := res.Extras[uuid]; ok {
			ex.Processes = append(ex.Processes, proc)
		}
	}
}

func (ns *NvidiaSMISource) collectRemappedRows(ctx context.Context, path string, mapping config.MetricMappingConfig, res *NvidiaSMIResult) {
	out, err := ns.exec(ctx, path,
		"--query-remapped-rows=gpu_uuid,remapped_rows.correctable,remapped_rows.uncorrectable,remapped_rows.pending,remapped_rows.failure",
		"--format=csv,noheader,nounits")
	if err != nil {
		// Row remapping only exists on recent data-center and RTX 30+ GPUs; absence is not a fault.
		return
	}
	byUUID := map[string]int{}
	for i := range res.GPUs {
		byUUID[res.GPUs[i].UUID] = i
	}
	for _, line := range strings.Split(out, "\n") {
		cells := strings.Split(strings.TrimSpace(line), ",")
		if len(cells) < 5 {
			continue
		}
		i, ok := byUUID[strings.TrimSpace(cells[0])]
		if !ok {
			continue
		}
		if v, ok := smiNumber(cells[2]); ok {
			res.GPUs[i].Latest.UncorrectableRemappedRows = int(v)
			res.Exported[mapping.UncorrectableRemapMetric] = true
		}
		if failed, ok := smiYes(cells[4]); ok {
			if failed {
				res.GPUs[i].Latest.RowRemapFailure = 1
			}
			res.Exported[mapping.RowRemapFailureMetric] = true
		}
	}
}
