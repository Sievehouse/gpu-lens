package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/domain"
)

// ---------------------------------------------------------------------------------------------
// Read-only guard
// ---------------------------------------------------------------------------------------------

func TestGuardSelfTestBlocksEveryMutationWithoutNetworkIO(t *testing.T) {
	res := SelfTestReadOnlyGuard()
	if !res.OK() {
		t.Fatalf("guard self-test failed: %+v", res)
	}
	if len(res.BlockedMethods) != 4 || res.NetworkCalls != 0 || len(res.LeakedMethods) != 0 {
		t.Fatalf("expected 4 blocked methods and no network calls, got %+v", res)
	}
}

func TestGuardAuditTrailIsBoundedAndCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	guard := NewReadOnlyRoundTripper(http.DefaultTransport)
	client := &http.Client{Transport: guard}
	for i := 0; i < maxRecordedCalls+50; i++ {
		resp, err := client.Get(srv.URL + "/x")
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		resp.Body.Close()
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/nodes/n1", nil)
	if _, err := client.Do(req); !errors.Is(err, ErrMutatingMethodForbidden) {
		t.Fatalf("expected DELETE to be blocked, got %v", err)
	}
	if n := len(guard.Calls()); n != maxRecordedCalls {
		t.Fatalf("audit trail should be capped at %d entries, got %d", maxRecordedCalls, n)
	}
	allowed, blocked := guard.Stats()
	if allowed != int64(maxRecordedCalls+50) || blocked != 1 {
		t.Fatalf("unexpected guard stats: allowed=%d blocked=%d", allowed, blocked)
	}
}

// ---------------------------------------------------------------------------------------------
// nvidia-smi adapter
// ---------------------------------------------------------------------------------------------

const smiHelpOutput = `List of valid properties to query for the switch "--query-gpu=":

"timestamp"
The timestamp of when the query was made.

"driver_version"
"name" or "gpu_name"
"uuid" or "gpu_uuid"
"pci.bus_id" or "gpu_bus_id"
"index"
"fan.speed"
"pstate"
"memory.total"
"memory.used"
"memory.free"
"utilization.gpu"
"utilization.memory"
"temperature.gpu"
"temperature.memory"
"power.draw"
"power.limit"
"ecc.errors.uncorrected.volatile.total"
"pcie.link.gen.current"
"pcie.link.gen.max"
"pcie.link.width.current"
"pcie.link.width.max"
"clocks_event_reasons.hw_thermal_slowdown"
`

type fakeSMI struct {
	mu       sync.Mutex
	calls    [][]string
	respond  func(args []string) (stdout, stderr string, code int)
	sleepFor time.Duration
}

func (f *fakeSMI) run(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.mu.Unlock()
	if f.sleepFor > 0 {
		select {
		case <-time.After(f.sleepFor):
		case <-ctx.Done():
			return nil, nil, -1, nil
		}
	}
	out, errOut, code := f.respond(args)
	return []byte(out), []byte(errOut), code, nil
}

func newTestSMI(t *testing.T, f *fakeSMI) *NvidiaSMISource {
	t.Helper()
	// ResolvePath needs a real file; the fake runner never executes it.
	path := t.TempDir() + "/nvidia-smi"
	if err := writeFile(path, "#!/bin/sh\n"); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return NewNvidiaSMISource(path, 2*time.Second, f.run)
}

func TestNvidiaSMICollectParsesDevicesWithoutFabricatingValues(t *testing.T) {
	f := &fakeSMI{}
	f.respond = func(args []string) (string, string, int) {
		switch {
		case args[0] == "--help-query-gpu":
			return smiHelpOutput, "", 0
		case strings.HasPrefix(args[0], "--query-gpu="):
			fields := strings.Split(strings.TrimPrefix(args[0], "--query-gpu="), ",")
			row := func(vals map[string]string) string {
				var cells []string
				for _, fld := range fields {
					v, ok := vals[fld]
					if !ok {
						v = "[N/A]"
					}
					cells = append(cells, v)
				}
				return strings.Join(cells, ", ")
			}
			g0 := row(map[string]string{
				"index": "0", "uuid": "GPU-aaaa", "pci.bus_id": "00000000:01:00.0", "driver_version": "550.90.07",
				"temperature.gpu": "71", "utilization.gpu": "93", "utilization.memory": "40", "memory.total": "24564",
				"memory.used": "20000", "memory.free": "4302", "power.draw": "301.55", "power.limit": "450.00", "fan.speed": "62",
				"pstate": "P2", "ecc.errors.uncorrected.volatile.total": "0", "pcie.link.gen.current": "4", "pcie.link.gen.max": "4",
				"pcie.link.width.current": "16", "pcie.link.width.max": "16", "clocks_event_reasons.hw_thermal_slowdown": "Active",
				"name": "NVIDIA GeForce RTX 4090",
			})
			// Second GPU: passive data-center card with several unsupported fields and a comma in its name.
			g1 := row(map[string]string{
				"index": "1", "uuid": "GPU-bbbb", "pci.bus_id": "00000000:41:00.0", "driver_version": "550.90.07",
				"temperature.gpu": "38", "utilization.gpu": "0", "utilization.memory": "0", "memory.total": "81559",
				"memory.used": "4", "memory.free": "81555", "power.draw": "[N/A]", "power.limit": "[Not Supported]", "fan.speed": "[N/A]",
				"clocks_event_reasons.hw_thermal_slowdown": "Not Active", "name": "NVIDIA H100, 80GB",
			})
			return g0 + "\n" + g1 + "\n", "", 0
		case strings.HasPrefix(args[0], "--query-compute-apps="):
			return "GPU-aaaa, 4242, 18000, /usr/bin/python3\nGPU-aaaa, 77, [N/A], C:\\tools\\render.exe\n", "", 0
		case strings.HasPrefix(args[0], "--query-remapped-rows="):
			return "GPU-bbbb, 0, 2, No, Yes\n", "", 0
		}
		return "", "unexpected", 3
	}
	src := newTestSMI(t, f)
	mapping := config.DefaultMetricMapping()
	res, err := src.Collect(context.Background(), mapping)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(res.GPUs) != 2 || res.DriverVersion != "550.90.07" {
		t.Fatalf("unexpected result: %d GPUs, driver %q", len(res.GPUs), res.DriverVersion)
	}
	g0, g1 := res.GPUs[0], res.GPUs[1]
	if g0.UUID != "GPU-aaaa" || g0.ModelName != "NVIDIA GeForce RTX 4090" || g0.Latest.GPUUtilPercent != 93 || g0.Latest.GPUTempCelsius != 71 ||
		g0.TotalMemoryMiB != 24564 || g0.Latest.FBUsedMiB != 20000 || g0.PowerLimitWatts != 450 || g0.Latest.PowerUsageWatts != 301.55 {
		t.Fatalf("GPU 0 parsed incorrectly: %+v", g0)
	}
	if g0.Latest.ECCDBEVolTotal == nil || *g0.Latest.ECCDBEVolTotal != 0 {
		t.Fatalf("GPU 0 ECC counter should be an explicit 0, got %v", g0.Latest.ECCDBEVolTotal)
	}
	if g1.ModelName != "NVIDIA H100, 80GB" {
		t.Fatalf("a comma in the model name must not shift columns, got %q", g1.ModelName)
	}
	// Unsupported cells must stay unknown: no invented power limit, power draw, fan speed or ECC counter.
	if g1.PowerLimitWatts != 0 || g1.Latest.PowerUsageWatts != 0 || g1.Latest.ECCDBEVolTotal != nil {
		t.Fatalf("unsupported cells were turned into values: %+v", g1)
	}
	if res.Extras["GPU-bbbb"].FanSpeedPercent != nil {
		t.Fatalf("fan speed [N/A] must stay nil")
	}
	ex0 := res.Extras["GPU-aaaa"]
	if ex0.FanSpeedPercent == nil || *ex0.FanSpeedPercent != 62 || ex0.PState != "P2" || ex0.PCIe == nil || ex0.PCIe.WidthCurrent != 16 {
		t.Fatalf("GPU 0 extras parsed incorrectly: %+v", ex0)
	}
	if len(ex0.ThrottleReasons) != 1 || ex0.ThrottleReasons[0] != "hw_thermal_slowdown" {
		t.Fatalf("expected active thermal throttle reason, got %v", ex0.ThrottleReasons)
	}
	if len(ex0.Processes) != 2 || ex0.Processes[0].PID != 4242 || *ex0.Processes[0].UsedMemoryMiB != 18000 || ex0.Processes[1].UsedMemoryMiB != nil {
		t.Fatalf("compute processes parsed incorrectly: %+v", ex0.Processes)
	}
	if g1.Latest.RowRemapFailure != 1 || g1.Latest.UncorrectableRemappedRows != 2 {
		t.Fatalf("remapped-row query not applied: %+v", g1.Latest)
	}
	if !res.Exported[mapping.GPUTempMetric] || !res.Exported[mapping.RowRemapFailureMetric] {
		t.Fatalf("exported map should include metrics that were read: %v", res.Exported)
	}
	if res.Exported[mapping.XIDErrorsMetric] || res.Exported[mapping.PCIeReplayMetric] {
		t.Fatalf("XID and PCIe replay are not available from nvidia-smi and must not be marked exported")
	}
	for _, call := range f.calls {
		if err := validateSMIArgs(call[1:]); err != nil {
			t.Fatalf("adapter issued an invocation outside the allow-list: %v", call)
		}
	}
}

func TestNvidiaSMIAllowListRejectsEverythingElse(t *testing.T) {
	bad := [][]string{
		{"--gpu-reset"},
		{"-pl", "200"},
		{"--query-gpu=index", "--format=csv"},
		{"--query-gpu=index;reboot", "--format=csv,noheader,nounits"},
		{"--query-gpu=$(id)", "--format=csv,noheader,nounits"},
		{"--query-gpu=index", "--format=csv,noheader,nounits", "-i", "0"},
		{"--compute-mode=1"},
		{"-q", "-x"},
		{},
	}
	for _, args := range bad {
		if err := validateSMIArgs(args); err == nil {
			t.Fatalf("expected %q to be rejected", args)
		}
	}
	good := [][]string{
		{"--help-query-gpu"},
		{"--query-gpu=index,uuid,temperature.gpu", "--format=csv,noheader,nounits"},
		{"--query-compute-apps=gpu_uuid,pid", "--format=csv,noheader,nounits"},
		{"--query-remapped-rows=gpu_uuid,remapped_rows.failure", "--format=csv,noheader,nounits"},
	}
	for _, args := range good {
		if err := validateSMIArgs(args); err != nil {
			t.Fatalf("expected %q to be allowed: %v", args, err)
		}
	}
}

func TestNvidiaSMIFallsBackToCoreFieldsOnOldDrivers(t *testing.T) {
	f := &fakeSMI{}
	queries := 0
	f.respond = func(args []string) (string, string, int) {
		switch {
		case args[0] == "--help-query-gpu":
			// Advertises a field the driver then refuses.
			return "\"index\"\n\"uuid\"\n\"name\"\n\"temperature.gpu\"\n\"utilization.gpu\"\n\"pstate\"\n", "", 0
		case strings.HasPrefix(args[0], "--query-gpu="):
			queries++
			if strings.Contains(args[0], "pstate") {
				return "Field \"pstate\" is not a valid field to query.\n", "", 2
			}
			n := len(strings.Split(strings.TrimPrefix(args[0], "--query-gpu="), ","))
			cells := make([]string, n)
			for i := range cells {
				cells[i] = "1"
			}
			cells[1] = "GPU-old"
			cells[n-1] = "Tesla K80"
			return strings.Join(cells, ", ") + "\n", "", 0
		}
		return "", "", 2
	}
	src := newTestSMI(t, f)
	res, err := src.Collect(context.Background(), config.DefaultMetricMapping())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if queries != 2 || len(res.GPUs) != 1 || res.GPUs[0].UUID != "GPU-old" || res.GPUs[0].ModelName != "Tesla K80" {
		t.Fatalf("fallback did not work: queries=%d result=%+v", queries, res.GPUs)
	}
	if !strings.Contains(res.FieldsNote, "core fields only") {
		t.Fatalf("expected the fallback to be reported, got %q", res.FieldsNote)
	}
}

func TestNvidiaSMIFailureModes(t *testing.T) {
	mapping := config.DefaultMetricMapping()

	driverDown := &fakeSMI{respond: func(args []string) (string, string, int) {
		return "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver.\n", "", 9
	}}
	if _, err := newTestSMI(t, driverDown).Collect(context.Background(), mapping); err == nil || !strings.Contains(err.Error(), "couldn't communicate") {
		t.Fatalf("expected the driver failure to surface, got %v", err)
	}

	noDevices := &fakeSMI{respond: func(args []string) (string, string, int) {
		if args[0] == "--help-query-gpu" {
			return smiHelpOutput, "", 0
		}
		return "No devices were found\n", "", 6
	}}
	res, err := newTestSMI(t, noDevices).Collect(context.Background(), mapping)
	if err != nil || !res.NoDevices || len(res.GPUs) != 0 {
		t.Fatalf("\"No devices were found\" should be a clean empty result, got %+v / %v", res, err)
	}

	hang := &fakeSMI{sleepFor: 3 * time.Second, respond: func(args []string) (string, string, int) { return "", "", 0 }}
	src := newTestSMI(t, hang)
	src.timeout = 150 * time.Millisecond
	if _, err := src.Collect(context.Background(), mapping); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout error, got %v", err)
	}

	missing := NewNvidiaSMISource(t.TempDir()+"/does-not-exist", time.Second, nil)
	if _, err := missing.Collect(context.Background(), mapping); err == nil {
		t.Fatalf("expected an error for a configured path that does not exist")
	}
}

// ---------------------------------------------------------------------------------------------
// DCGM normalization + scrape
// ---------------------------------------------------------------------------------------------

func series(name string, value float64, labels map[string]string, ts time.Time) ParsedSeries {
	return ParsedSeries{MetricName: name, Labels: labels, Value: value, Timestamp: ts}
}

func TestNormalizeSeriesLiveNeverInventsHardwareDefaults(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	lbl := map[string]string{"gpu": "3", "UUID": "GPU-x", "Hostname": "node-a", "modelName": "NVIDIA A100"}
	res := NormalizeSeriesLive([]ParsedSeries{
		series("DCGM_FI_DEV_GPU_UTIL", 55, lbl, now),
		series("DCGM_FI_DEV_FB_USED", 30000, lbl, now),
		series("DCGM_FI_DEV_FB_FREE", 10000, lbl, now),
	}, LiveNormalizeOptions{Mapping: config.DefaultMetricMapping(), MaxStalenessSeconds: 180, Now: now, SourceTag: "dcgm_exporter"})
	if len(res.GPUs) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(res.GPUs))
	}
	g := res.GPUs[0]
	if g.PowerLimitWatts != 0 {
		t.Fatalf("power limit was not exported and must stay 0, got %v", g.PowerLimitWatts)
	}
	if g.TotalMemoryMiB != 40000 {
		t.Fatalf("total memory must be derived from used+free (40000), got %v", g.TotalMemoryMiB)
	}
	if g.DeviceIndex != 3 || g.NodeName != "node-a" || g.MappingStatus != domain.MappingUnallocated || g.IsStale {
		t.Fatalf("unexpected device: %+v", g)
	}
	if g.Latest.ECCDBEVolTotal != nil || g.Latest.ThermalViolationNs != nil {
		t.Fatalf("optional metrics that were not exported must stay nil")
	}
}

func TestNormalizeSeriesLiveWorkloadLabelConventions(t *testing.T) {
	now := time.Now().UTC()
	m := LiveMetricMapping(config.DefaultMetricMapping())
	opt := LiveNormalizeOptions{Mapping: m, MaxStalenessSeconds: 180, Now: now, SourceTag: "prometheus"}

	// honor_labels=false: the workload is in exported_*, the plain labels name the exporter pod.
	renamed := NormalizeSeriesLive([]ParsedSeries{
		series("DCGM_FI_DEV_GPU_UTIL", 80, map[string]string{"UUID": "GPU-1", "Hostname": "n1", "pod": "nvidia-dcgm-exporter-abc", "namespace": "gpu-operator",
			"container": "nvidia-dcgm-exporter", "exported_pod": "train-0", "exported_namespace": "ml", "exported_container": "trainer"}, now),
		series("DCGM_FI_DEV_GPU_UTIL", 0, map[string]string{"UUID": "GPU-2", "Hostname": "n1", "pod": "nvidia-dcgm-exporter-abc", "namespace": "gpu-operator",
			"container": "nvidia-dcgm-exporter"}, now),
	}, opt)
	if !renamed.HonorLabelsRenamed || renamed.MappedGPUs != 1 {
		t.Fatalf("expected exported_* labels to be used: %+v", renamed)
	}
	byUUID := map[string]domain.GPUDevice{}
	for _, g := range renamed.GPUs {
		byUUID[g.UUID] = g
	}
	if byUUID["GPU-1"].AllocatedPod != "train-0" || byUUID["GPU-1"].AllocatedNamespace != "ml" {
		t.Fatalf("GPU-1 should be attributed to ml/train-0, got %+v", byUUID["GPU-1"])
	}
	if byUUID["GPU-2"].AllocatedPod != "" || byUUID["GPU-2"].MappingStatus != domain.MappingUnallocated {
		t.Fatalf("a free GPU must not be attributed to the exporter pod: %+v", byUUID["GPU-2"])
	}

	// No exported_* labels anywhere, but the pod label points at the exporter itself: ignore it and say so.
	self := NormalizeSeriesLive([]ParsedSeries{
		series("DCGM_FI_DEV_GPU_UTIL", 0, map[string]string{"UUID": "GPU-3", "Hostname": "n1", "pod": "nvidia-dcgm-exporter-xyz", "namespace": "gpu-operator", "container": "nvidia-dcgm-exporter"}, now),
	}, opt)
	if self.IgnoredSelfLabels != 1 || self.GPUs[0].AllocatedPod != "" {
		t.Fatalf("exporter self-labels must be ignored and counted: %+v", self)
	}

	// honor_labels=true: plain pod labels are the workload.
	honored := NormalizeSeriesLive([]ParsedSeries{
		series("DCGM_FI_DEV_GPU_UTIL", 70, map[string]string{"UUID": "GPU-4", "Hostname": "n1", "pod": "serve-1", "namespace": "inference", "container": "triton"}, now),
	}, opt)
	if honored.GPUs[0].AllocatedPod != "serve-1" || honored.GPUs[0].MappingStatus != domain.MappingMapped {
		t.Fatalf("plain pod labels should attribute the workload: %+v", honored.GPUs[0])
	}
}

func TestNormalizeSeriesLiveNodeNamesMIGAndEmulatorMarker(t *testing.T) {
	now := time.Now().UTC()
	m := config.DefaultMetricMapping()
	res := NormalizeSeriesLive([]ParsedSeries{
		// Hostname is the exporter pod name; the `node` label is the real Kubernetes node.
		series("DCGM_FI_DEV_GPU_UTIL", 10, map[string]string{"UUID": "GPU-1", "Hostname": "dcgm-exporter-7xk2p", "node": "worker-7"}, now),
		series("DCGM_FI_DEV_GPU_UTIL", 20, map[string]string{"UUID": "GPU-M", "Hostname": "worker-7", "GPU_I_ID": "1", "GPU_I_PROFILE": "1g.10gb"}, now),
		series("DCGM_FI_DEV_GPU_UTIL", 30, map[string]string{"UUID": "GPU-M", "Hostname": "worker-7", "GPU_I_ID": "2", "GPU_I_PROFILE": "1g.10gb"}, now),
		series(LabEmulatorMarkerMetric, 1, map[string]string{"phase": "healthy"}, now),
		series("DCGM_FI_DEV_POWER_MGMT_LIMIT", 300, map[string]string{"UUID": "GPU-1", "Hostname": "dcgm-exporter-7xk2p", "node": "worker-7"}, now),
	}, LiveNormalizeOptions{Mapping: m, MaxStalenessSeconds: 180, Now: now, KnownNodeNames: map[string]bool{"worker-7": true}, SourceTag: "dcgm_exporter"})
	if len(res.GPUs) != 3 || res.MIGInstances != 2 || !res.Emulated {
		t.Fatalf("expected 3 devices (1 GPU + 2 MIG instances) from an emulated endpoint: %+v", res)
	}
	for _, g := range res.GPUs {
		if g.NodeName != "worker-7" {
			t.Fatalf("node label matching a known Kubernetes node must win over Hostname: %+v", g)
		}
		if g.UUID == "GPU-1" && g.PowerLimitWatts != 300 {
			t.Fatalf("exported power limit not applied: %+v", g)
		}
	}
	if res.Extras["GPU-M/mig-1"] == nil || res.Extras["GPU-M/mig-1"].MIGProfile != "1g.10gb" || !res.Extras["GPU-1"].Emulated {
		t.Fatalf("extras missing MIG profile or emulated flag: %+v", res.Extras)
	}
	if res.Exported[LabEmulatorMarkerMetric] != true {
		t.Fatalf("marker metric should be visible in the raw exported set")
	}
}

func TestDCGMExporterSourceScrapeIsLenientAndReadOnly(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("X-GPULens-Emulated", "live-lab")
		fmt.Fprint(w, "# HELP x y\nDCGM_FI_DEV_GPU_UTIL{gpu=\"0\",UUID=\"GPU-1\"} 42\nthis line is not exposition format {{{\nDCGM_FI_DEV_GPU_TEMP{gpu=\"0\",UUID=\"GPU-1\"} 61 1792324790000\n")
	}))
	defer srv.Close()
	ds := NewDCGMExporterSource(srv.URL+"/metrics", 2*time.Second, 1<<20, http.DefaultTransport)
	scrape, err := ds.Scrape(context.Background())
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if len(scrape.Series) != 2 || scrape.SkippedLines != 1 || !scrape.ExplicitTS || scrape.EmulatorHeader != "live-lab" {
		t.Fatalf("unexpected scrape: series=%d skipped=%d explicit=%v header=%q", len(scrape.Series), scrape.SkippedLines, scrape.ExplicitTS, scrape.EmulatorHeader)
	}
	if len(methods) != 1 || methods[0] != http.MethodGet {
		t.Fatalf("scrape must issue exactly one GET, saw %v", methods)
	}

	down := NewDCGMExporterSource("http://127.0.0.1:1/metrics", 500*time.Millisecond, 1<<20, http.DefaultTransport)
	if _, err := down.Scrape(context.Background()); err == nil {
		t.Fatalf("expected an error for an unreachable exporter")
	}
}

// ---------------------------------------------------------------------------------------------
// Prometheus live queries
// ---------------------------------------------------------------------------------------------

func promJSON(resultType string, result interface{}) string {
	raw, _ := json.Marshal(map[string]interface{}{"status": "success", "data": map[string]interface{}{"resultType": resultType, "result": result}})
	return string(raw)
}

func TestPrometheusCollectLiveAndBackfill(t *testing.T) {
	now := float64(time.Now().Unix())
	scraped := now - 12 // real scrape time, 12 s before the evaluation time
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Get("query"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer prom-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("query")
		lbl := map[string]string{"UUID": "GPU-1", "gpu": "0", "Hostname": "node-a", "modelName": "NVIDIA H100", "exported_pod": "train-0", "exported_namespace": "ml", "pod": "dcgm-exporter-x"}
		with := func(name string) map[string]string {
			out := map[string]string{"__name__": name}
			for k, v := range lbl {
				out[k] = v
			}
			return out
		}
		switch {
		case r.URL.Path == "/api/v1/status/buildinfo":
			fmt.Fprint(w, `{"status":"success","data":{"version":"2.53.1"}}`)
		case r.URL.Path == "/api/v1/query_range":
			fmt.Fprint(w, promJSON("matrix", []interface{}{
				map[string]interface{}{"metric": with("DCGM_FI_DEV_GPU_UTIL"), "values": [][]interface{}{{now - 120, "4"}, {now - 60, "6"}}},
				map[string]interface{}{"metric": with("DCGM_FI_DEV_FB_USED"), "values": [][]interface{}{{now - 120, "60000"}, {now - 60, "60100"}}},
			}))
		case q == "time()":
			fmt.Fprint(w, promJSON("scalar", []interface{}{now - 3, fmt.Sprintf("%f", now-3)}))
		case strings.HasPrefix(q, "timestamp("):
			fmt.Fprint(w, promJSON("vector", []interface{}{map[string]interface{}{"metric": lbl, "value": []interface{}{now, fmt.Sprintf("%f", scraped)}}}))
		case strings.HasPrefix(q, "up{"):
			fmt.Fprint(w, promJSON("vector", []interface{}{
				map[string]interface{}{"metric": map[string]string{"job": "nvidia-dcgm-exporter", "instance": "10.0.0.1:9400"}, "value": []interface{}{now, "1"}},
				map[string]interface{}{"metric": map[string]string{"job": "nvidia-dcgm-exporter", "instance": "10.0.0.2:9400"}, "value": []interface{}{now, "0"}},
			}))
		case strings.HasPrefix(q, "{__name__=~"):
			fmt.Fprint(w, promJSON("vector", []interface{}{
				map[string]interface{}{"metric": with("DCGM_FI_DEV_GPU_UTIL"), "value": []interface{}{now, "5"}},
				map[string]interface{}{"metric": with("DCGM_FI_DEV_FB_USED"), "value": []interface{}{now, "60200"}},
				map[string]interface{}{"metric": with("DCGM_FI_DEV_FB_FREE"), "value": []interface{}{now, "21359"}},
				map[string]interface{}{"metric": with("DCGM_FI_DEV_XID_ERRORS"), "value": []interface{}{now, "0"}},
			}))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	mapping := LiveMetricMapping(config.DefaultMetricMapping())
	ps := NewPrometheusSource(srv.URL, "prom-token", 2*time.Second, 1<<20, mapping, config.DefaultRuleThresholds(), http.DefaultTransport)
	res, err := ps.CollectLive(context.Background(), mapping, 180, nil, true)
	if err != nil {
		t.Fatalf("collect live: %v", err)
	}
	if len(res.Normalized.GPUs) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(res.Normalized.GPUs))
	}
	g := res.Normalized.GPUs[0]
	if g.AllocatedPod != "train-0" || g.Latest.GPUUtilPercent != 5 || g.TotalMemoryMiB != 81559 {
		t.Fatalf("unexpected device: %+v", g)
	}
	// Staleness must come from the real scrape timestamp (timestamp()), not the query evaluation time.
	if !res.TimestampsKnown || g.StalenessSeconds < 10 || g.StalenessSeconds > 20 {
		t.Fatalf("expected ~12s staleness from the scrape timestamp, got %.1fs (known=%v)", g.StalenessSeconds, res.TimestampsKnown)
	}
	if res.Version != "2.53.1" || res.SkewSeconds == nil || *res.SkewSeconds < 1 || *res.SkewSeconds > 6 {
		t.Fatalf("audit facts wrong: version=%q skew=%v", res.Version, res.SkewSeconds)
	}
	if !res.TargetsKnown || res.TargetsUp != 1 || res.TargetsDown != 1 || len(res.DownTargets) != 1 {
		t.Fatalf("target health wrong: %+v", res)
	}

	history, err := ps.Backfill(context.Background(), mapping, 30, 60)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	pts := history["GPU-1"]
	if len(pts) != 2 || pts[0].GPUUtilPercent != 4 || pts[1].FBUsedMiB != 60100 || !pts[0].Timestamp.Before(pts[1].Timestamp) {
		t.Fatalf("unexpected backfill: %+v", pts)
	}
	for _, call := range seen {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatalf("prometheus adapter issued a non-GET request: %s", call)
		}
	}

	// Without the audit queries only the metric and timestamp lookups are sent.
	mu.Lock()
	seen = nil
	mu.Unlock()
	if _, err := ps.CollectLive(context.Background(), mapping, 180, nil, false); err != nil {
		t.Fatalf("collect live (no audit): %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 queries without audit, saw %v", seen)
	}

	// A wrong token is an error, not an empty fleet.
	ps.SetBearerToken("wrong")
	if _, err := ps.CollectLive(context.Background(), mapping, 180, nil, false); err == nil {
		t.Fatalf("expected an error for HTTP 401")
	}
}

// ---------------------------------------------------------------------------------------------
// Kubernetes live collection
// ---------------------------------------------------------------------------------------------

func TestKubernetesCollectLivePaginatesNormalizesAndToleratesPartialAccess(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"gitVersion":"v1.30.2","platform":"linux/amd64"}`)
		case "/api/v1/nodes":
			fmt.Fprint(w, `{"metadata":{},"items":[
			 {"metadata":{"name":"gpu-a","labels":{"nvidia.com/gpu.product":"NVIDIA-H100"}},"spec":{},"status":{"capacity":{"nvidia.com/gpu":"4","nvidia.com/mig-1g.10gb":"7"},"allocatable":{"nvidia.com/gpu":"4"},"conditions":[{"type":"Ready","status":"True"}]}},
			 {"metadata":{"name":"cpu-b"},"spec":{"unschedulable":true},"status":{"capacity":{"cpu":"8"},"allocatable":{"cpu":"8"},"conditions":[{"type":"Ready","status":"False"}]}}]}`)
		case "/api/v1/pods":
			if r.URL.Query().Get("continue") == "" {
				fmt.Fprint(w, `{"metadata":{"continue":"page2"},"items":[
				 {"metadata":{"name":"train-0","namespace":"ml","uid":"u1","ownerReferences":[{"kind":"StatefulSet","name":"train"}]},
				  "spec":{"nodeName":"gpu-a","containers":[{"name":"t","resources":{"limits":{"nvidia.com/gpu":"2"}}}]},
				  "status":{"phase":"Running","startTime":"2026-11-15T10:00:00Z","containerStatuses":[{"name":"t","restartCount":3,"state":{"waiting":{"reason":"CrashLoopBackOff"}},
				    "lastState":{"terminated":{"exitCode":137,"reason":"OOMKilled","finishedAt":"2026-11-15T11:00:00Z"}}}]}},
				 {"metadata":{"name":"pulling","namespace":"ml","uid":"u2"},
				  "spec":{"nodeName":"gpu-a","containers":[{"name":"t","resources":{"requests":{"nvidia.com/gpu":"1"}}}]},
				  "status":{"phase":"Pending"}},
				 {"metadata":{"name":"web","namespace":"default","uid":"u3"},"spec":{"nodeName":"cpu-b","containers":[{"name":"w","resources":{}}]},"status":{"phase":"Running"}}]}`)
			} else {
				fmt.Fprint(w, `{"metadata":{},"items":[
				 {"metadata":{"name":"waiting","namespace":"ml","uid":"u4","creationTimestamp":"2026-11-15T11:30:00Z"},
				  "spec":{"containers":[{"name":"t","resources":{"limits":{"nvidia.com/gpu":"4"}}}],"initContainers":[{"name":"warm","resources":{"limits":{"nvidia.com/gpu":"8"}}}],
				          "tolerations":[{"key":"nvidia.com/gpu","operator":"Exists","effect":"NoSchedule"},{"key":"node.kubernetes.io/not-ready","operator":"Exists","effect":"NoExecute"}]},
				  "status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable","message":"0/2 nodes are available: 1 Insufficient nvidia.com/gpu."}]}},
				 {"metadata":{"name":"failed-job","namespace":"ml","uid":"u5"},
				  "spec":{"nodeName":"gpu-a","containers":[{"name":"t","resources":{"limits":{"nvidia.com/gpu":"1"}}}]},
				  "status":{"phase":"Failed","containerStatuses":[{"name":"t","restartCount":0,"state":{"terminated":{"exitCode":1,"reason":"Error","finishedAt":"2026-11-15T11:10:00Z"}},"lastState":{}}]}}]}`)
			}
		case "/api/v1/events":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"kind":"Status","status":"Failure","reason":"Forbidden","code":403}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ks := NewKubernetesSource(srv.URL, "tok", 2*time.Second, 1<<20, http.DefaultTransport)
	res, err := ks.CollectLive(context.Background())
	if err != nil {
		t.Fatalf("collect live: %v", err)
	}
	if res.Version != "v1.30.2" || res.Emulated || !res.Usable() {
		t.Fatalf("unexpected header facts: %+v", res)
	}
	if res.TotalNodes != 2 || res.GPUNodes != 1 || len(res.OtherResources) != 1 || res.OtherResources[0] != "nvidia.com/mig-1g.10gb" {
		t.Fatalf("node inventory wrong: nodes=%d gpuNodes=%d other=%v", res.TotalNodes, res.GPUNodes, res.OtherResources)
	}
	if res.TotalPodsSeen != 5 || len(res.Pods) != 4 {
		t.Fatalf("expected 5 pods scanned across 2 pages and 4 GPU pods kept, got %d / %d", res.TotalPodsSeen, len(res.Pods))
	}
	pods := map[string]domain.WorkloadPod{}
	for _, p := range res.Pods {
		pods[p.Name] = p
	}
	if p := pods["train-0"]; p.RestartCount != 3 || p.LastTermination == nil || p.LastTermination.Reason != "OOMKilled" || p.StatusDetail != "CrashLoopBackOff" || p.OwnerKind != "StatefulSet" {
		t.Fatalf("train-0 normalized incorrectly: %+v", p)
	}
	// Bound but not started: the scheduler already placed it, so it is not a scheduling problem.
	if p := pods["pulling"]; p.Phase != "ContainerCreating" || p.RequestedGPUs != 1 {
		t.Fatalf("bound pending pod should be ContainerCreating: %+v", p)
	}
	if p := pods["waiting"]; p.Phase != "Pending" || p.RequestedGPUs != 8 || p.SchedulerReason != "FailedScheduling" || !strings.Contains(p.SchedulerMessage, "Insufficient nvidia.com/gpu") || len(p.Tolerations) != 1 {
		t.Fatalf("unschedulable pod normalized incorrectly: %+v", p)
	}
	if p := pods["failed-job"]; p.LastTermination == nil || p.LastTermination.ExitCode != 1 {
		t.Fatalf("a failed pod's current terminated state should be captured: %+v", p)
	}
	// Allocation counts bound, unfinished pods: train-0 (2) + pulling (1); the Failed pod has released its GPU.
	for _, n := range res.Nodes {
		if n.Name == "gpu-a" && n.GPUAllocated != 3 {
			t.Fatalf("expected 3 GPUs allocated on gpu-a, got %d", n.GPUAllocated)
		}
	}
	var eventsAccess K8sResourceAccess
	for _, a := range res.Access {
		if a.Resource == "events" {
			eventsAccess = a
		}
	}
	if eventsAccess.OK || eventsAccess.Status != http.StatusForbidden {
		t.Fatalf("events 403 should be recorded as denied without failing the collection: %+v", eventsAccess)
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "GET ") {
			t.Fatalf("kubernetes adapter issued a non-GET request: %s", c)
		}
	}

	// Nothing listening at all is an error (unreachable), not an empty cluster.
	down := NewKubernetesSource("http://127.0.0.1:1", "", 500*time.Millisecond, 1<<20, http.DefaultTransport)
	if _, err := down.CollectLive(context.Background()); err == nil {
		t.Fatalf("expected an error for an unreachable API server")
	}
}

func TestKubernetesEventTimeFallbacks(t *testing.T) {
	eventTime := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	observed := eventTime.Add(5 * time.Minute)
	ev := normalizeEvent(k8sEventItem{
		Type: "Warning", Reason: "FailedScheduling", EventTime: &eventTime,
		Series: &struct {
			Count            int        `json:"count"`
			LastObservedTime *time.Time `json:"lastObservedTime"`
		}{Count: 7, LastObservedTime: &observed},
		ReportingComponent: "default-scheduler",
	})
	if !ev.LastTimestamp.Equal(observed) || !ev.FirstTimestamp.Equal(eventTime) || ev.Count != 7 || ev.SourceComponent != "default-scheduler" {
		t.Fatalf("events.k8s.io style event normalized incorrectly: %+v", ev)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o755)
}
