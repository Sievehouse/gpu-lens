// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/config"
	"github.com/Sievehouse/gpu-lens/internal/domain"
)

// AMDSMISource reads AMD Instinct / Radeon GPU telemetry from the ROCm driver on the machine GPU Lens runs on, by
// invoking `amd-smi` with JSON output. It is the AMD counterpart of NvidiaSMISource and produces the same result
// type, so AMD GPUs flow through the same merge, rules and console.
//
// Safety boundary: exactly four fixed, read-only invocations are allowed (see validateAMDSMIArgs). No shell, no
// user-provided arguments, a bounded timeout and bounded output. Nothing that resets GPUs, changes clocks, power
// caps or partitions can be issued.
//
// Status: experimental. The parser accepts the JSON layouts of amd-smi from ROCm 6.0 through 6.4 (plain values,
// "value/unit" objects and the newer {"gpu_data": [...]} wrapper). It is verified against a test double built from
// AMD's documented output, not yet against real AMD hardware.
type AMDSMISource struct {
	explicitPath string
	timeout      time.Duration
	run          CommandRunner

	mu       sync.Mutex
	resolved string
	static   map[int]amdStatic
	listing  map[int]amdListing
	staticAt time.Time
}

type amdStatic struct {
	model, driver, bdf, serial string
	powerLimit                 float64
	maxWidth, maxGen           int
}

type amdListing struct {
	uuid, bdf string
}

// ErrAMDSMINotFound means no amd-smi binary exists on this machine.
var ErrAMDSMINotFound = errors.New("amd-smi not found on this machine")

// NewAMDSMISource constructs the local AMD driver adapter. explicitPath may be empty (auto-discover).
func NewAMDSMISource(explicitPath string, timeout time.Duration, runner CommandRunner) *AMDSMISource {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	if runner == nil {
		runner = execRunner
	}
	return &AMDSMISource{explicitPath: explicitPath, timeout: timeout, run: runner}
}

func (as *AMDSMISource) ID() string   { return "amd_smi" }
func (as *AMDSMISource) Name() string { return "Local AMD driver (amd-smi)" }

var amdAllowedInvocations = [][]string{
	{"list", "--json"},
	{"static", "--json"},
	{"metric", "--json"},
	{"process", "--json"},
}

// validateAMDSMIArgs is the allow-list for every amd-smi invocation made by GPU Lens.
func validateAMDSMIArgs(args []string) error {
	for _, allowed := range amdAllowedInvocations {
		if len(args) == len(allowed) {
			match := true
			for i := range args {
				if args[i] != allowed[i] {
					match = false
					break
				}
			}
			if match {
				return nil
			}
		}
	}
	return fmt.Errorf("amd-smi invocation rejected by allow-list: %q", args)
}

func amdCandidatePaths() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return []string{"/opt/rocm/bin/amd-smi", "/usr/bin/amd-smi", "/usr/local/bin/amd-smi"}
}

// ResolvePath finds the amd-smi binary.
func (as *AMDSMISource) ResolvePath() (string, error) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.resolved != "" {
		return as.resolved, nil
	}
	if as.explicitPath != "" {
		if fi, err := os.Stat(as.explicitPath); err != nil || fi.IsDir() {
			return "", fmt.Errorf("%w: %s", ErrAMDSMINotFound, as.explicitPath)
		}
		as.resolved = as.explicitPath
		return as.resolved, nil
	}
	if p, err := exec.LookPath("amd-smi"); err == nil {
		as.resolved = p
		return p, nil
	}
	for _, c := range amdCandidatePaths() {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			as.resolved = c
			return c, nil
		}
	}
	if dir := os.Getenv("ROCM_PATH"); dir != "" {
		c := filepath.Join(dir, "bin", "amd-smi")
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			as.resolved = c
			return c, nil
		}
	}
	return "", ErrAMDSMINotFound
}

// AMDSMIExitError is a non-zero exit from amd-smi.
type AMDSMIExitError struct {
	Code    int
	Message string
}

func (e *AMDSMIExitError) Error() string {
	return fmt.Sprintf("amd-smi exited with code %d: %s", e.Code, e.Message)
}

func (as *AMDSMISource) exec(ctx context.Context, path string, args ...string) ([]byte, error) {
	if err := validateAMDSMIArgs(args); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, as.timeout)
	defer cancel()
	stdout, stderr, code, err := as.run(ctx, path, args...)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("amd-smi %s timed out after %s", args[0], as.timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("could not run amd-smi: %v", err)
	}
	if code != 0 {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(stdout))
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, &AMDSMIExitError{Code: code, Message: msg}
	}
	return stdout, nil
}

// amdRecords decodes either a top-level list of per-GPU objects or the {"gpu_data": [...]} wrapper.
func amdRecords(raw []byte) ([]map[string]interface{}, error) {
	var list []map[string]interface{}
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("amd-smi returned output that is not JSON")
	}
	for _, key := range []string{"gpu_data", "gpus", "data"} {
		if inner, ok := wrapped[key]; ok {
			if err := json.Unmarshal(inner, &list); err == nil {
				return list, nil
			}
		}
	}
	return nil, fmt.Errorf("amd-smi JSON has no per-GPU list")
}

func amdGPUIndex(rec map[string]interface{}) (int, bool) {
	for _, k := range []string{"gpu", "gpu_id", "index"} {
		if v, ok := amdNumber(rec[k]); ok {
			return int(v), true
		}
	}
	return 0, false
}

func amdSection(rec map[string]interface{}, keys ...string) map[string]interface{} {
	for _, k := range keys {
		if m, ok := rec[k].(map[string]interface{}); ok {
			return m
		}
	}
	return nil
}

// amdNumber reads a number from 42, "42", "42 C", "N/A" or {"value": 42, "unit": "C"}.
func amdNumber(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		return smiNumber(x)
	case map[string]interface{}:
		return amdNumber(x["value"])
	}
	return 0, false
}

func amdUnit(v interface{}) string {
	if m, ok := v.(map[string]interface{}); ok {
		if u, ok := m["unit"].(string); ok {
			return strings.ToUpper(strings.TrimSpace(u))
		}
	}
	if s, ok := v.(string); ok {
		if i := strings.LastIndexByte(strings.TrimSpace(s), ' '); i > 0 {
			return strings.ToUpper(strings.TrimSpace(s)[i+1:])
		}
	}
	return ""
}

// amdMiB converts a memory value to MiB. amd-smi reports VRAM in "MB" meaning MiB, and process memory in bytes.
func amdMiB(v interface{}, defaultUnit string) (float64, bool) {
	n, ok := amdNumber(v)
	if !ok {
		return 0, false
	}
	unit := amdUnit(v)
	if unit == "" {
		unit = defaultUnit
	}
	switch unit {
	case "B":
		return n / (1024 * 1024), true
	case "KB", "KIB":
		return n / 1024, true
	case "GB", "GIB":
		return n * 1024, true
	}
	return n, true
}

func amdString(v interface{}) string {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if strings.EqualFold(s, "N/A") {
			return ""
		}
		return s
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case map[string]interface{}:
		return amdString(x["value"])
	}
	return ""
}

func pcieGenFromGTs(gts float64) int {
	switch {
	case gts >= 60:
		return 6
	case gts >= 30:
		return 5
	case gts >= 15:
		return 4
	case gts >= 7.5:
		return 3
	case gts >= 4.5:
		return 2
	case gts > 0:
		return 1
	}
	return 0
}

func (as *AMDSMISource) refreshStatic(ctx context.Context, path string) []string {
	as.mu.Lock()
	fresh := !as.staticAt.IsZero() && time.Since(as.staticAt) < 5*time.Minute
	as.mu.Unlock()
	if fresh {
		return nil
	}
	var notes []string
	listing := map[int]amdListing{}
	if out, err := as.exec(ctx, path, "list", "--json"); err == nil {
		if recs, err := amdRecords(out); err == nil {
			for _, r := range recs {
				if idx, ok := amdGPUIndex(r); ok {
					listing[idx] = amdListing{uuid: amdString(r["uuid"]), bdf: amdString(r["bdf"])}
				}
			}
		}
	} else {
		notes = append(notes, "GPU list unavailable: "+err.Error())
	}
	static := map[int]amdStatic{}
	if out, err := as.exec(ctx, path, "static", "--json"); err == nil {
		if recs, err := amdRecords(out); err == nil {
			for _, r := range recs {
				idx, ok := amdGPUIndex(r)
				if !ok {
					continue
				}
				s := amdStatic{}
				if asic := amdSection(r, "asic"); asic != nil {
					s.model = amdString(asic["market_name"])
					s.serial = amdString(asic["asic_serial"])
				}
				if drv := amdSection(r, "driver"); drv != nil {
					s.driver = amdString(drv["version"])
				}
				if bus := amdSection(r, "bus"); bus != nil {
					s.bdf = amdString(bus["bdf"])
					if w, ok := amdNumber(bus["max_pcie_width"]); ok {
						s.maxWidth = int(w)
					}
					if sp, ok := amdNumber(bus["max_pcie_speed"]); ok {
						s.maxGen = pcieGenFromGTs(sp)
					}
				}
				if lim := amdSection(r, "limit"); lim != nil {
					for _, k := range []string{"max_power", "power_cap", "socket_power"} {
						if p, ok := amdNumber(lim[k]); ok && p > 0 {
							s.powerLimit = p
							break
						}
					}
				}
				static[idx] = s
			}
		}
	} else {
		notes = append(notes, "Static GPU facts unavailable: "+err.Error())
	}
	as.mu.Lock()
	as.listing, as.static, as.staticAt = listing, static, time.Now()
	as.mu.Unlock()
	return notes
}

func amdNoDevices(err error) bool {
	var exitErr *AMDSMIExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	m := strings.ToLower(exitErr.Message)
	return strings.Contains(m, "no gpu") || strings.Contains(m, "no amd gpu") || strings.Contains(m, "no devices")
}

// Collect runs the read-only amd-smi queries and normalizes them into GPU devices.
func (as *AMDSMISource) Collect(ctx context.Context, mapping config.MetricMappingConfig) (*NvidiaSMIResult, error) {
	path, err := as.ResolvePath()
	if err != nil {
		return nil, err
	}
	res := &NvidiaSMIResult{
		BinaryPath: path, Hostname: LocalNodeName(), Extras: map[string]*GPUExtras{}, Processes: map[string][]GPUProcess{},
		Exported: map[string]bool{}, FieldsNote: "amd-smi JSON (experimental)",
	}
	out, err := as.exec(ctx, path, "metric", "--json")
	if err != nil {
		if amdNoDevices(err) {
			res.NoDevices = true
			res.Notes = append(res.Notes, "The AMD driver tools answered but report no GPUs.")
			return res, nil
		}
		return nil, err
	}
	recs, err := amdRecords(out)
	if err != nil {
		return nil, err
	}
	res.Notes = append(res.Notes, as.refreshStatic(ctx, path)...)
	as.mu.Lock()
	static, listing := as.static, as.listing
	as.mu.Unlock()

	now := time.Now().UTC()
	byIndex := map[int]string{}
	for _, r := range recs {
		idx, ok := amdGPUIndex(r)
		if !ok {
			res.Notes = append(res.Notes, "Skipped an amd-smi record without a GPU index.")
			continue
		}
		st, li := static[idx], listing[idx]
		bdf := li.bdf
		if bdf == "" {
			bdf = st.bdf
		}
		uuid := li.uuid
		switch {
		case uuid != "":
		case st.serial != "":
			uuid = "AMD-" + st.serial
		case bdf != "":
			uuid = "AMD-" + bdf
		default:
			uuid = fmt.Sprintf("AMD-%s-%d", res.Hostname, idx)
		}
		model := st.model
		if model == "" {
			model = "AMD GPU"
		}
		dev := domain.GPUDevice{
			UUID: uuid, DeviceIndex: idx, NodeName: res.Hostname, ModelName: model, PCIBusID: bdf,
			DriverVersion: st.driver, PowerLimitWatts: st.powerLimit, MappingStatus: domain.MappingUnallocated,
			LastSampleAt: now, Latest: domain.GPUMetricPoint{Timestamp: now},
		}
		if res.DriverVersion == "" {
			res.DriverVersion = st.driver
		}
		ex := &GPUExtras{Sources: []string{"amd_smi"}, ThrottleReasons: []string{}, Processes: []GPUProcess{}}

		if u := amdSection(r, "usage"); u != nil {
			if v, ok := amdNumber(u["gfx_activity"]); ok {
				dev.Latest.GPUUtilPercent = v
				res.Exported[mapping.GPUUtilMetric] = true
			}
			if v, ok := amdNumber(u["umc_activity"]); ok {
				dev.Latest.MemCopyUtilPercent = v
				res.Exported[mapping.MemCopyUtilMetric] = true
			}
		}
		if m := amdSection(r, "mem_usage", "memory_usage"); m != nil {
			total, okT := amdMiB(m["total_vram"], "MB")
			used, okU := amdMiB(m["used_vram"], "MB")
			free, okF := amdMiB(m["free_vram"], "MB")
			if okU {
				dev.Latest.FBUsedMiB = used
				res.Exported[mapping.FBUsedMetric] = true
			}
			if !okF && okT && okU {
				free, okF = total-used, true
			}
			if okF {
				dev.Latest.FBFreeMiB = free
				res.Exported[mapping.FBFreeMetric] = true
			}
			if okT {
				dev.TotalMemoryMiB = total
			} else if okU && okF {
				dev.TotalMemoryMiB = used + free
			}
		}
		if t := amdSection(r, "temperature"); t != nil {
			// Edge is the closest analogue of NVIDIA's core temperature; Instinct parts that do not report edge
			// expose the hotspot (junction) sensor instead.
			if v, ok := amdNumber(t["edge"]); ok {
				dev.Latest.GPUTempCelsius = v
				res.Exported[mapping.GPUTempMetric] = true
			} else if v, ok := amdNumber(t["hotspot"]); ok {
				dev.Latest.GPUTempCelsius = v
				res.Exported[mapping.GPUTempMetric] = true
			}
			if v, ok := amdNumber(t["mem"]); ok {
				dev.Latest.MemoryTempCelsius = v
				res.Exported[mapping.MemoryTempMetric] = true
			}
		}
		if p := amdSection(r, "power"); p != nil {
			for _, k := range []string{"socket_power", "average_socket_power", "current_socket_power"} {
				if v, ok := amdNumber(p[k]); ok {
					dev.Latest.PowerUsageWatts = v
					res.Exported[mapping.PowerUsageMetric] = true
					break
				}
			}
			if s := strings.ToUpper(amdString(p["throttle_status"])); s == "THROTTLED" {
				ex.ThrottleReasons = appendUnique(ex.ThrottleReasons, "amd_throttled")
			}
		}
		if e := amdSection(r, "ecc"); e != nil {
			if v, ok := amdNumber(e["total_uncorrectable_count"]); ok {
				n := int(v)
				dev.Latest.ECCDBEVolTotal = &n
				res.Exported[mapping.ECCDBEVolTotalMetric] = true
			}
			if v, ok := amdNumber(e["total_correctable_count"]); ok {
				n := int(v)
				dev.Latest.ECCSBEVolTotal = &n
				res.Exported[mapping.ECCSBEVolTotalMetric] = true
			}
		}
		if f := amdSection(r, "fan"); f != nil {
			if v, ok := amdNumber(f["usage"]); ok {
				fan := v
				ex.FanSpeedPercent = &fan
			}
		}
		if pc := amdSection(r, "pcie"); pc != nil {
			w, okW := amdNumber(pc["width"])
			sp, okS := amdNumber(pc["speed"])
			if okW && okS && st.maxWidth > 0 && st.maxGen > 0 {
				ex.PCIe = &PCIeLink{GenCurrent: pcieGenFromGTs(sp), GenMax: st.maxGen, WidthCurrent: int(w), WidthMax: st.maxWidth}
			}
		}
		res.GPUs = append(res.GPUs, dev)
		res.Extras[uuid] = ex
		byIndex[idx] = uuid
	}
	as.collectProcesses(ctx, path, res, byIndex)
	return res, nil
}

func (as *AMDSMISource) collectProcesses(ctx context.Context, path string, res *NvidiaSMIResult, byIndex map[int]string) {
	out, err := as.exec(ctx, path, "process", "--json")
	if err != nil {
		// Listing processes needs elevated rights on some systems; it is optional.
		res.Notes = append(res.Notes, "Compute process list unavailable (amd-smi process may need elevated permissions).")
		return
	}
	recs, err := amdRecords(out)
	if err != nil {
		return
	}
	for _, r := range recs {
		idx, ok := amdGPUIndex(r)
		if !ok {
			continue
		}
		uuid, ok := byIndex[idx]
		if !ok {
			continue
		}
		list, _ := r["process_list"].([]interface{})
		for _, item := range list {
			m, _ := item.(map[string]interface{})
			if m == nil {
				continue
			}
			info := amdSection(m, "process_info")
			if info == nil {
				info = m
			}
			pid, okPID := amdNumber(info["pid"])
			if !okPID {
				continue
			}
			proc := GPUProcess{PID: int(pid), Name: amdString(info["name"])}
			if mu := amdSection(info, "memory_usage"); mu != nil {
				if v, ok := amdMiB(mu["vram_mem"], "B"); ok {
					proc.UsedMemoryMiB = &v
				}
			}
			res.Processes[uuid] = append(res.Processes[uuid], proc)
			if ex := res.Extras[uuid]; ex != nil {
				ex.Processes = append(ex.Processes, proc)
			}
		}
	}
}
