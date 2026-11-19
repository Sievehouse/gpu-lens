// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/config"
	"github.com/Sievehouse/gpu-lens/internal/domain"
)

// ---------------------------------------------------------------------------------------------
// Test doubles: a dcgm-exporter and a Kubernetes API whose state the test can change between cycles
// ---------------------------------------------------------------------------------------------

type fakeGPU struct {
	UUID, Node          string
	Index               int
	Util, Used, Free    float64
	Temp, Power         float64
	XID                 int
	Pod, Namespace      string
	FrozenAt            time.Time // non-zero: emit this explicit (old) sample timestamp
	OmitRequiredMetrics bool
}

type fakeCluster struct {
	mu          sync.Mutex
	gpus        []fakeGPU
	nodes       []map[string]interface{}
	pods        []map[string]interface{}
	events      []map[string]interface{}
	podsStatus  int
	dcgmDown    bool
	k8sRequests []string
	emulated    bool
}

func (c *fakeCluster) dcgmHandler(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dcgmDown {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var b strings.Builder
	if c.emulated {
		b.WriteString("gpulens_lab_emulator_info{phase=\"test\"} 1\n")
	}
	for _, g := range c.gpus {
		labels := fmt.Sprintf(`gpu="%d",UUID="%s",Hostname="%s",modelName="NVIDIA H100 80GB HBM3"`, g.Index, g.UUID, g.Node)
		if g.Pod != "" {
			labels += fmt.Sprintf(`,pod="%s",namespace="%s",container="main"`, g.Pod, g.Namespace)
		}
		suffix := ""
		if !g.FrozenAt.IsZero() {
			suffix = fmt.Sprintf(" %d", g.FrozenAt.UnixMilli())
		}
		emit := func(name string, v float64) { fmt.Fprintf(&b, "%s{%s} %g%s\n", name, labels, v, suffix) }
		emit("DCGM_FI_DEV_GPU_UTIL", g.Util)
		emit("DCGM_FI_DEV_FB_USED", g.Used)
		emit("DCGM_FI_DEV_FB_FREE", g.Free)
		emit("DCGM_FI_DEV_GPU_TEMP", g.Temp)
		emit("DCGM_FI_DEV_POWER_USAGE", g.Power)
		if !g.OmitRequiredMetrics {
			emit("DCGM_FI_DEV_MEM_COPY_UTIL", g.Util/2)
			emit("DCGM_FI_DEV_MEMORY_TEMP", g.Temp+8)
			emit("DCGM_FI_DEV_XID_ERRORS", float64(g.XID))
			emit("DCGM_FI_DEV_PCIE_REPLAY_COUNTER", 0)
			emit("DCGM_FI_DEV_ROW_REMAP_FAILURE", 0)
		}
	}
	fmt.Fprint(w, b.String())
}

func (c *fakeCluster) k8sHandler(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.k8sRequests = append(c.k8sRequests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	write := func(items interface{}) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"metadata": map[string]string{}, "items": items})
	}
	switch r.URL.Path {
	case "/version":
		ver := "v1.30.2"
		if c.emulated {
			ver = "v1.30.4-gpulens-lab"
		}
		fmt.Fprintf(w, `{"gitVersion":%q,"platform":"linux/amd64"}`, ver)
	case "/api/v1/nodes":
		write(c.nodes)
	case "/api/v1/pods":
		if c.podsStatus != 0 {
			w.WriteHeader(c.podsStatus)
			return
		}
		write(c.pods)
	case "/api/v1/events":
		write(c.events)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func k8sNode(name string, capacity, allocatable int) map[string]interface{} {
	return map[string]interface{}{
		"metadata": map[string]interface{}{"name": name, "labels": map[string]string{"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3"}},
		"spec":     map[string]interface{}{},
		"status": map[string]interface{}{
			"capacity":    map[string]string{"nvidia.com/gpu": fmt.Sprint(capacity)},
			"allocatable": map[string]string{"nvidia.com/gpu": fmt.Sprint(allocatable)},
			"conditions":  []map[string]string{{"type": "Ready", "status": "True"}},
		},
	}
}

func k8sPod(ns, name, node, phase string, gpus int, started time.Time) map[string]interface{} {
	status := map[string]interface{}{"phase": phase}
	if phase == "Running" {
		status["startTime"] = started.UTC().Format(time.RFC3339)
		status["containerStatuses"] = []map[string]interface{}{{"name": "main", "restartCount": 0, "state": map[string]interface{}{"running": map[string]string{}}, "lastState": map[string]interface{}{}}}
	}
	if phase == "Pending" {
		status["conditions"] = []map[string]string{{"type": "PodScheduled", "status": "False", "reason": "Unschedulable", "message": "0/1 nodes are available: 1 Insufficient nvidia.com/gpu."}}
	}
	spec := map[string]interface{}{"containers": []map[string]interface{}{{"name": "main", "resources": map[string]interface{}{"limits": map[string]string{"nvidia.com/gpu": fmt.Sprint(gpus)}}}}}
	if node != "" {
		spec["nodeName"] = node
	}
	return map[string]interface{}{
		"metadata": map[string]interface{}{"name": name, "namespace": ns, "uid": "uid-" + name, "creationTimestamp": started.Add(-10 * time.Minute).UTC().Format(time.RFC3339)},
		"spec":     spec,
		"status":   status,
	}
}

// newTestEngine builds an engine whose only enabled sources are the ones the test turns on. The engine is driven
// manually (one collection cycle per cycle() call) so tests are fast and deterministic.
func newTestEngine(t *testing.T, mutate func(*config.LiveConfig)) *Engine {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Source.HTTPTimeout = 2 * time.Second
	cfg.Live.NvidiaSMIEnabled = false
	cfg.Live.HostVitals = false
	cfg.Live.DCGM.Enabled = false
	cfg.Live.Prometheus.Enabled = false
	cfg.Live.Kubernetes.Enabled = false
	if mutate != nil {
		mutate(&cfg.Live)
	}
	e := NewEngine(cfg, nil)
	e.manualTicks = true
	t.Cleanup(func() { e.Stop("test finished") })
	return e
}

func (e *Engine) testStart() {
	e.mu.Lock()
	e.startLocked("test session", false)
	e.mu.Unlock()
}

// tick forces every source to be due and runs exactly one collection cycle.
func (e *Engine) cycle() {
	e.mu.Lock()
	for _, st := range e.src {
		st.nextDue = time.Time{}
		st.lastAuditAt = time.Time{}
	}
	gen, ctx := e.generation, e.loopCtx
	e.mu.Unlock()
	e.runTick(ctx, gen)
	// Let any collector that outlived the grace period deliver before the next assertion.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		busy := false
		for _, st := range e.src {
			busy = busy || st.inFlight
		}
		e.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *Engine) state() State {
	return e.State(0, 0, Access{ConfigMutable: true})
}

func findCheck(t *testing.T, st State, id string) Check {
	t.Helper()
	for _, g := range st.Preflight.Groups {
		for _, c := range g.Checks {
			if c.ID == id {
				return c
			}
		}
	}
	t.Fatalf("preflight check %q not found", id)
	return Check{}
}

func findSource(t *testing.T, st State, id string) SourceStatus {
	t.Helper()
	for _, s := range st.Sources {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("source %q not found", id)
	return SourceStatus{}
}

func incidentRules(st State) map[string]domain.Incident {
	out := map[string]domain.Incident{}
	if st.Overview == nil {
		return out
	}
	for _, inc := range st.Overview.ActiveIncidents {
		out[inc.RuleID] = inc
	}
	return out
}

func fakeSMIPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the nvidia-smi test double is a POSIX shell script")
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "tests", "fixtures", "fake-nvidia-smi"))
	if err != nil {
		t.Fatalf("resolve fake nvidia-smi: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fake nvidia-smi fixture missing: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------------------------

func TestNoSourcesIsNeverReportedAsHealthy(t *testing.T) {
	e := newTestEngine(t, func(c *config.LiveConfig) {
		// Enabled but pointing at addresses nobody configured and nothing listens on.
		c.DCGM = config.LiveSourceConfig{Enabled: true, Endpoint: "http://127.0.0.1:1/metrics"}
		c.Kubernetes = config.LiveSourceConfig{Enabled: true, Endpoint: "http://127.0.0.1:1"}
	})
	if st := e.state(); st.Status.State != "idle" || st.Preflight.Verdict != "idle" || st.Overview != nil {
		t.Fatalf("before Go Live the engine must be idle with no verdict: %+v", st.Status)
	}
	e.testStart()
	e.cycle()
	st := e.state()
	if st.Overview == nil || st.Overview.FleetHealthStatus != "no_telemetry" {
		t.Fatalf("with nothing connected the fleet must be 'no_telemetry', got %+v", st.Overview)
	}
	if st.Preflight.Verdict != "blocked" {
		t.Fatalf("expected verdict 'blocked', got %q", st.Preflight.Verdict)
	}
	if len(st.Overview.ActiveIncidents) != 0 {
		t.Fatalf("unconfigured default addresses must not raise incidents: %+v", st.Overview.ActiveIncidents)
	}
	if s := findSource(t, st, SourceDCGM); s.State != StateNotFound || s.FailureCount != 0 {
		t.Fatalf("an auto-probed address that never answered is 'not_found', not a failure: %+v", s)
	}
	if c := findCheck(t, st, "corr.telemetry"); c.Status != CheckFail || !strings.Contains(c.Hint, "live_lab.py") {
		t.Fatalf("telemetry coverage must fail with a hint: %+v", c)
	}
	if c := findCheck(t, st, "core.readonly_guard"); c.Status != CheckPass {
		t.Fatalf("read-only guard self-test should pass: %+v", c)
	}
}

func TestExplicitSourceDownRaisesBlindSpot(t *testing.T) {
	e := newTestEngine(t, func(c *config.LiveConfig) {
		c.DCGM = config.LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: "http://127.0.0.1:1/metrics"}
	})
	e.testStart()
	e.cycle()
	st := e.state()
	if s := findSource(t, st, SourceDCGM); s.State != StateUnreachable || s.LastError == "" || s.ConsecutiveFailures != 1 {
		t.Fatalf("a configured source that is down is 'unreachable': %+v", s)
	}
	inc, ok := incidentRules(st)["GPU-FRESH-005"]
	if !ok || inc.Severity != domain.SeverityCritical {
		t.Fatalf("expected a critical data-freshness incident, got %+v", st.Overview.ActiveIncidents)
	}
	if st.Overview.FleetHealthStatus != "telemetry_blind_spot" {
		t.Fatalf("expected telemetry_blind_spot, got %q", st.Overview.FleetHealthStatus)
	}
	if c := findCheck(t, st, "dcgm.reachable"); c.Status != CheckFail {
		t.Fatalf("expected the reachability check to fail: %+v", c)
	}
}

func TestKubernetesAndDCGMAreJoined(t *testing.T) {
	now := time.Now()
	cl := &fakeCluster{
		gpus: []fakeGPU{
			{UUID: "GPU-n1-0", Node: "node-1", Index: 0, Util: 92, Used: 70000, Free: 11559, Temp: 70, Power: 600, Pod: "train-0", Namespace: "ml"},
			{UUID: "GPU-n1-1", Node: "node-1", Index: 1, Util: 91, Used: 70000, Free: 11559, Temp: 71, Power: 600, Pod: "train-0", Namespace: "ml"},
			{UUID: "GPU-n1-2", Node: "node-1", Index: 2, Util: 0, Used: 300, Free: 81259, Temp: 45, Power: 80, XID: 79, Pod: "crash-0", Namespace: "ml"},
			{UUID: "GPU-n1-3", Node: "node-1", Index: 3, Util: 0, Used: 4, Free: 81555, Temp: 33, Power: 70},
		},
		nodes: []map[string]interface{}{k8sNode("node-1", 4, 3)},
		pods: []map[string]interface{}{
			k8sPod("ml", "train-0", "node-1", "Running", 2, now.Add(-3*time.Hour)),
			k8sPod("ml", "crash-0", "node-1", "Running", 1, now.Add(-3*time.Hour)),
			k8sPod("ml", "big-job", "", "Pending", 4, now.Add(-20*time.Minute)),
			k8sPod("kube-system", "coredns", "node-1", "Running", 0, now.Add(-30*time.Hour)),
		},
		events: []map[string]interface{}{{
			"metadata": map[string]string{"uid": "ev-1"}, "type": "Warning", "reason": "FailedScheduling", "count": 12,
			"message":        "0/1 nodes are available: 1 Insufficient nvidia.com/gpu.",
			"firstTimestamp": now.Add(-19 * time.Minute).UTC().Format(time.RFC3339), "lastTimestamp": now.Add(-10 * time.Second).UTC().Format(time.RFC3339),
			"source": map[string]string{"component": "default-scheduler"}, "involvedObject": map[string]string{"kind": "Pod", "namespace": "ml", "name": "big-job"},
		}},
	}
	// crash-0 restarted 30 s ago
	crash := cl.pods[1]["status"].(map[string]interface{})
	crash["containerStatuses"] = []map[string]interface{}{{
		"name": "main", "restartCount": 1, "state": map[string]interface{}{"waiting": map[string]string{"reason": "CrashLoopBackOff"}},
		"lastState": map[string]interface{}{"terminated": map[string]interface{}{"exitCode": 139, "reason": "Error", "finishedAt": now.Add(-30 * time.Second).UTC().Format(time.RFC3339)}},
	}}
	dcgm := httptest.NewServer(http.HandlerFunc(cl.dcgmHandler))
	defer dcgm.Close()
	k8s := httptest.NewServer(http.HandlerFunc(cl.k8sHandler))
	defer k8s.Close()

	e := newTestEngine(t, func(c *config.LiveConfig) {
		c.DCGM = config.LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: dcgm.URL + "/metrics"}
		c.Kubernetes = config.LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: k8s.URL, BearerToken: "secret-k8s-token"}
		c.ClusterName = "unit-cluster"
	})
	e.testStart()
	e.cycle()
	e.cycle()
	st := e.state()

	if st.Overview.ClusterName != "unit-cluster" || st.Overview.DataMode != "live_telemetry" || st.Status.Emulated {
		t.Fatalf("unexpected overview header: %+v", st.Overview)
	}
	if len(st.GPUs) != 4 || len(st.Nodes) != 1 {
		t.Fatalf("expected 4 GPUs on 1 Kubernetes node (no inferred node), got %d GPUs / %d nodes", len(st.GPUs), len(st.Nodes))
	}
	rules := incidentRules(st)
	for _, id := range []string{"GPU-SCHED-001", "GPU-HEALTH-002", "GPU-CORR-004"} {
		if _, ok := rules[id]; !ok {
			t.Fatalf("expected rule %s to fire; active: %v", id, keys(rules))
		}
	}
	if rules["GPU-CORR-004"].EvidenceStrength != domain.EvidenceStrong {
		t.Fatalf("crash + Xid in the correlation window should be strong evidence: %+v", rules["GPU-CORR-004"].EvidenceStrength)
	}
	if _, ok := rules["GPU-UTIL-003"]; ok {
		t.Fatalf("low-utilization must not fire after seconds of observation, even though the pod has run for hours")
	}
	// Attribution: Kubernetes pods get their GPU UUIDs from the DCGM pod labels.
	var train domain.WorkloadPod
	for _, p := range st.Pods {
		if p.Name == "train-0" {
			train = p
		}
	}
	if len(train.AssignedGPUUUIDs) != 2 {
		t.Fatalf("train-0 should be joined to 2 GPUs, got %v", train.AssignedGPUUUIDs)
	}
	capSum := st.Overview.CapacitySummary
	if capSum.TotalCapacityGPUs != 4 || capSum.TotalAllocatableGPUs != 3 || capSum.TotalAllocatedGPUs != 3 || capSum.PendingRequestedGPUs != 4 {
		t.Fatalf("capacity math wrong: %+v", capSum)
	}
	if c := findCheck(t, st, "corr.attribution"); c.Status != CheckPass {
		t.Fatalf("attribution check should pass: %+v", c)
	}
	if c := findCheck(t, st, "k8s.readonly"); c.Status != CheckPass || !strings.Contains(c.Detail, "0 mutating") {
		t.Fatalf("read-only access check wrong: %+v", c)
	}
	// The GPU with the Xid is linked to its incidents.
	for _, g := range st.GPUs {
		if g.UUID == "GPU-n1-2" && (g.WorstSeverity != "critical" || len(g.IncidentIDs) < 2) {
			t.Fatalf("GPU-n1-2 should carry its critical incidents: %+v", g)
		}
	}
	// Only read-only requests ever reached Kubernetes, and the token never appears in the API payload.
	cl.mu.Lock()
	for _, r := range cl.k8sRequests {
		if !strings.HasPrefix(r, "GET ") {
			t.Fatalf("non-GET request sent to Kubernetes: %s", r)
		}
	}
	cl.mu.Unlock()
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), "secret-k8s-token") {
		t.Fatalf("the bearer token leaked into the live state payload")
	}
	if !st.Config.Kubernetes.TokenSet {
		t.Fatalf("config view should report that a token is set")
	}

	// The pending pod gets scheduled and the Xid clears: incidents resolve and are kept as session history.
	cl.mu.Lock()
	cl.pods = cl.pods[:2]
	cl.events = nil
	cl.gpus[2].XID = 0
	cl.nodes = []map[string]interface{}{k8sNode("node-1", 4, 4)}
	delete(cl.pods[1]["status"].(map[string]interface{}), "containerStatuses")
	cl.mu.Unlock()
	e.cycle()
	st = e.state()
	if len(st.Overview.ActiveIncidents) != 0 || st.Overview.FleetHealthStatus != "healthy" {
		t.Fatalf("expected a healthy fleet after recovery, got %s / %v", st.Overview.FleetHealthStatus, keys(incidentRules(st)))
	}
	if len(st.ResolvedIncidents) != 3 {
		t.Fatalf("expected 3 resolved incidents in the session history, got %d", len(st.ResolvedIncidents))
	}
	var sawResolved, sawOpened bool
	for _, a := range st.Activity {
		sawResolved = sawResolved || strings.HasPrefix(a.Title, "Incident resolved")
		sawOpened = sawOpened || strings.HasPrefix(a.Title, "Incident opened")
	}
	if !sawOpened || !sawResolved {
		t.Fatalf("activity log should record incidents opening and resolving")
	}
}

func keys(m map[string]domain.Incident) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestMissingPodLabelsAndPartialRBAC(t *testing.T) {
	now := time.Now()
	cl := &fakeCluster{
		gpus: []fakeGPU{
			{UUID: "GPU-a", Node: "node-1", Index: 0, Util: 90, Used: 60000, Free: 21559, Temp: 60, Power: 500},
			{UUID: "GPU-b", Node: "node-1", Index: 1, Util: 88, Used: 60000, Free: 21559, Temp: 61, Power: 500},
		},
		nodes: []map[string]interface{}{k8sNode("node-1", 2, 2)},
		pods:  []map[string]interface{}{k8sPod("ml", "train-0", "node-1", "Running", 2, now.Add(-time.Hour))},
	}
	dcgm := httptest.NewServer(http.HandlerFunc(cl.dcgmHandler))
	defer dcgm.Close()
	k8s := httptest.NewServer(http.HandlerFunc(cl.k8sHandler))
	defer k8s.Close()
	e := newTestEngine(t, func(c *config.LiveConfig) {
		c.DCGM = config.LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: dcgm.URL + "/metrics"}
		c.Kubernetes = config.LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: k8s.URL}
	})
	e.testStart()
	e.cycle()
	e.cycle()
	st := e.state()
	// Kubernetes says both GPUs are allocated; neither series carries pod labels -> both are certainly unlabelled.
	for _, g := range st.GPUs {
		if g.MappingStatus != string(domain.MappingMissingPodLabels) {
			t.Fatalf("expected missing_pod_labels, got %+v", g)
		}
	}
	if _, ok := incidentRules(st)["GPU-FRESH-005"]; !ok || st.Overview.FleetHealthStatus != "telemetry_blind_spot" {
		t.Fatalf("unlabelled allocated GPUs are a telemetry blind spot: %v / %s", keys(incidentRules(st)), st.Overview.FleetHealthStatus)
	}

	// Lose pod-list permission: the source degrades instead of disappearing, and the check explains why.
	cl.mu.Lock()
	cl.podsStatus = http.StatusForbidden
	cl.mu.Unlock()
	e.cycle()
	st = e.state()
	if s := findSource(t, st, SourceKubernetes); s.State != StateDegraded || !strings.Contains(s.Detail, "pods denied") {
		t.Fatalf("expected a degraded Kubernetes source: %+v", s)
	}
	if c := findCheck(t, st, "k8s.pods"); c.Status != CheckFail || !strings.Contains(c.Hint, "rbac-readonly.yaml") {
		t.Fatalf("expected the pod-list check to fail with an RBAC hint: %+v", c)
	}
}

func TestStaleTelemetryAndSourceRecovery(t *testing.T) {
	cl := &fakeCluster{gpus: []fakeGPU{
		{UUID: "GPU-live", Node: "node-1", Index: 0, Util: 50, Used: 1000, Free: 80559, Temp: 50, Power: 200},
		{UUID: "GPU-frozen", Node: "node-2", Index: 0, Util: 50, Used: 1000, Free: 80559, Temp: 50, Power: 200, FrozenAt: time.Now().Add(-10 * time.Minute)},
	}}
	dcgm := httptest.NewServer(http.HandlerFunc(cl.dcgmHandler))
	defer dcgm.Close()
	e := newTestEngine(t, func(c *config.LiveConfig) {
		c.DCGM = config.LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: dcgm.URL + "/metrics"}
	})
	e.testStart()
	e.cycle()
	st := e.state()
	stale := map[string]bool{}
	for _, g := range st.GPUs {
		stale[g.UUID] = g.IsStale
	}
	if stale["GPU-live"] || !stale["GPU-frozen"] {
		t.Fatalf("explicit sample timestamps must drive staleness: %v", stale)
	}
	if _, ok := incidentRules(st)["GPU-FRESH-005"]; !ok {
		t.Fatalf("stale GPU telemetry must raise the data-freshness rule")
	}
	if c := findCheck(t, st, "corr.telemetry"); c.Status != CheckWarn {
		t.Fatalf("coverage check should warn about stale GPUs: %+v", c)
	}
	if c := findCheck(t, st, "dcgm.metrics"); c.Status != CheckPass {
		t.Fatalf("all default metrics are exported: %+v", c)
	}

	// The exporter goes down after having been connected: unreachable, and the last known GPUs are kept.
	cl.mu.Lock()
	cl.dcgmDown = true
	cl.mu.Unlock()
	e.cycle()
	st = e.state()
	if s := findSource(t, st, SourceDCGM); s.State != StateUnreachable {
		t.Fatalf("expected unreachable after the exporter went down: %+v", s)
	}
	if len(st.GPUs) != 2 {
		t.Fatalf("GPUs must not silently vanish when their source fails, got %d", len(st.GPUs))
	}
	// ... and comes back.
	cl.mu.Lock()
	cl.dcgmDown = false
	cl.gpus[1].FrozenAt = time.Time{}
	cl.mu.Unlock()
	e.cycle()
	st = e.state()
	if s := findSource(t, st, SourceDCGM); s.State != StateConnected {
		t.Fatalf("expected the source to recover: %+v", s)
	}
	if len(incidentRules(st)) != 0 {
		t.Fatalf("expected no incidents after recovery, got %v", keys(incidentRules(st)))
	}
	var recovered bool
	for _, a := range st.Activity {
		recovered = recovered || strings.HasPrefix(a.Title, "Source recovered")
	}
	if !recovered {
		t.Fatalf("activity log should record the recovery")
	}
}

func TestMissingRequiredMetricsDegradeTheSource(t *testing.T) {
	cl := &fakeCluster{gpus: []fakeGPU{{UUID: "GPU-1", Node: "n", Util: 10, Used: 100, Free: 900, Temp: 40, Power: 90, OmitRequiredMetrics: true}}, emulated: true}
	dcgm := httptest.NewServer(http.HandlerFunc(cl.dcgmHandler))
	defer dcgm.Close()
	e := newTestEngine(t, func(c *config.LiveConfig) {
		c.DCGM = config.LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: dcgm.URL + "/metrics"}
	})
	e.testStart()
	e.cycle()
	st := e.state()
	s := findSource(t, st, SourceDCGM)
	if s.State != StateDegraded || !strings.Contains(s.Detail, "DCGM_FI_DEV_XID_ERRORS") || !s.Emulated {
		t.Fatalf("expected a degraded, emulated source naming the missing metric: %+v", s)
	}
	if c := findCheck(t, st, "dcgm.metrics"); c.Status != CheckWarn {
		t.Fatalf("expected a warning for missing default metrics: %+v", c)
	}
	if !st.Status.Emulated || st.Overview.DataMode != "live_emulated" || len(st.Notes) == 0 {
		t.Fatalf("an emulated endpoint must be labelled everywhere: status=%v mode=%s notes=%v", st.Status.Emulated, st.Overview.DataMode, st.Notes)
	}
	if st.SourcesDetail.ExportedMetrics["DCGM_FI_DEV_XID_ERRORS"] {
		t.Fatalf("a metric that is not exported must not be listed as exported")
	}
}

func TestStandaloneHostThroughNvidiaSMI(t *testing.T) {
	path := fakeSMIPath(t)
	t.Setenv("FAKE_SMI_TEMP0", "84")
	e := newTestEngine(t, func(c *config.LiveConfig) {
		c.NvidiaSMIEnabled = true
		c.NvidiaSMIPath = path
	})
	e.testStart()
	e.cycle()
	st := e.state()
	if len(st.GPUs) != 2 {
		t.Fatalf("expected 2 GPUs from the driver, got %d", len(st.GPUs))
	}
	if st.GPUs[1].ModelName != "NVIDIA RTX A5000, Rev. B" {
		t.Fatalf("model name with a comma was mangled: %q", st.GPUs[1].ModelName)
	}
	// One inferred node, flagged as telemetry-only rather than "schedulable".
	if len(st.Nodes) != 1 || st.Nodes[0].Labels[inferredNodeLabel] == "" {
		t.Fatalf("expected one inferred host node: %+v", st.Nodes)
	}
	if note := st.Overview.CapacitySummary.Nodes[0].StatusNote; !strings.HasPrefix(note, "Telemetry-only host") {
		t.Fatalf("inferred nodes must not claim Kubernetes scheduling state: %q", note)
	}
	// GPUs are attributed to compute processes, clearly marked as not being pods.
	if len(st.Pods) != 2 {
		t.Fatalf("expected 2 host-process workloads, got %d", len(st.Pods))
	}
	for _, p := range st.Pods {
		if p.Namespace != HostProcessNamespace || p.OwnerKind != "HostProcess" || !strings.Contains(p.StatusDetail, "not a Kubernetes pod") {
			t.Fatalf("host workload not labelled correctly: %+v", p)
		}
	}
	if st.GPUs[0].AllocatedPod != "python3[4242]" {
		t.Fatalf("GPU 0 should be attributed to the process holding most memory, got %q", st.GPUs[0].AllocatedPod)
	}
	inc, ok := incidentRules(st)["GPU-THERM-006"]
	if !ok || inc.Severity != domain.SeverityWarning {
		t.Fatalf("84°C should raise a thermal warning: %v", keys(incidentRules(st)))
	}
	// Evidence provenance names the real source, and the incident says so.
	if inc.SupportingEvidence[0].Source != "nvidia_smi" {
		t.Fatalf("evidence should be attributed to nvidia_smi, got %q", inc.SupportingEvidence[0].Source)
	}
	if last := inc.ObservedFacts[len(inc.ObservedFacts)-1]; !strings.Contains(last, "nvidia-smi") {
		t.Fatalf("incident should disclose the nvidia-smi provenance: %q", last)
	}
	if c := findCheck(t, st, "nvsmi.coverage"); c.Status != CheckWarn {
		t.Fatalf("nvidia-smi-only GPUs lack fault counters and the check must say so: %+v", c)
	}
	if st.SourcesDetail.ExportedMetrics["DCGM_FI_DEV_XID_ERRORS"] {
		t.Fatalf("Xid is not available from nvidia-smi and must not be listed as exported")
	}

	// Driver failure: an installed nvidia-smi that cannot talk to the driver is a failing source.
	t.Setenv("FAKE_SMI_MODE", "driver-fail")
	e.cycle()
	st = e.state()
	s := findSource(t, st, SourceNvidiaSMI)
	if s.State != StateError || !strings.Contains(s.Hint, "driver") {
		t.Fatalf("expected an error state with a driver hint: %+v", s)
	}
	if _, ok := incidentRules(st)["GPU-FRESH-005"]; !ok {
		t.Fatalf("a failing driver query must raise the data-freshness rule")
	}
}

func TestLiveThresholdOverridesAndLowUtilWindow(t *testing.T) {
	path := fakeSMIPath(t)
	t.Setenv("FAKE_SMI_TEMP0", "70")
	e := newTestEngine(t, func(c *config.LiveConfig) {
		c.NvidiaSMIEnabled = true
		c.NvidiaSMIPath = path
		c.IntervalSeconds = 1
	})
	e.testStart()
	e.cycle()
	if n := len(e.state().Overview.ActiveIncidents); n != 0 {
		t.Fatalf("expected a quiet fleet at 70°C with default thresholds, got %d incidents", n)
	}

	// Invalid overrides are rejected and change nothing.
	if err := e.SetThresholds(map[string]float64{"temp_warning_celsius": 95}, false); err == nil {
		t.Fatalf("a warning threshold above the critical one must be rejected")
	}
	if err := e.SetThresholds(map[string]float64{"not_a_threshold": 1}, false); err == nil {
		t.Fatalf("unknown threshold keys must be rejected")
	}

	if err := e.SetThresholds(map[string]float64{"temp_warning_celsius": 65}, false); err != nil {
		t.Fatalf("set thresholds: %v", err)
	}
	e.cycle()
	st := e.state()
	if _, ok := incidentRules(st)["GPU-THERM-006"]; !ok {
		t.Fatalf("lowering the warning threshold to 65°C should raise a thermal warning at 70°C")
	}
	if len(st.ThresholdOverrides) != 1 || st.Thresholds["temp_warning_celsius"] != 65 || st.ThresholdDefaults["temp_warning_celsius"] != 80 {
		t.Fatalf("override bookkeeping wrong: %v / %v", st.ThresholdOverrides, st.Thresholds["temp_warning_celsius"])
	}
	if c := findCheck(t, st, "core.rules"); c.Status != CheckInfo {
		t.Fatalf("active overrides should be visible in the preflight: %+v", c)
	}

	// Low utilization (GPU 1 idles at 2% while a process holds 74% of its memory) needs an observed window.
	if err := e.SetThresholds(map[string]float64{"low_util_window_minutes": 0.05, "min_utilization_samples": 2}, true); err != nil {
		t.Fatalf("set thresholds: %v", err)
	}
	e.cycle()
	if _, ok := incidentRules(e.state())["GPU-UTIL-003"]; ok {
		t.Fatalf("low utilization must not fire before the window (3 s) has been observed")
	}
	deadline := time.Now().Add(8 * time.Second)
	fired := false
	for time.Now().Before(deadline) && !fired {
		time.Sleep(1100 * time.Millisecond)
		e.cycle()
		_, fired = incidentRules(e.state())["GPU-UTIL-003"]
	}
	if !fired {
		t.Fatalf("low utilization should fire once the process has been observed for the whole window")
	}
	st = e.state()
	low := st.Overview.CapacitySummary.LowUtilizationWorkloads
	if len(low) != 1 || low[0].Namespace != HostProcessNamespace || low[0].MeanGPUUtilPercent > 5 {
		t.Fatalf("capacity report should list the idle host process: %+v", low)
	}

	// Reset returns to defaults and resolves the incident.
	if err := e.SetThresholds(nil, true); err != nil {
		t.Fatalf("reset thresholds: %v", err)
	}
	e.cycle()
	st = e.state()
	if len(st.ThresholdOverrides) != 0 || len(incidentRules(st)) != 0 {
		t.Fatalf("after reset there should be no overrides and no incidents: %v / %v", st.ThresholdOverrides, keys(incidentRules(st)))
	}
}

func TestHistoryAndActivityAreIncremental(t *testing.T) {
	cl := &fakeCluster{gpus: []fakeGPU{{UUID: "GPU-1", Node: "n", Util: 40, Used: 500, Free: 500, Temp: 55, Power: 150}}}
	dcgm := httptest.NewServer(http.HandlerFunc(cl.dcgmHandler))
	defer dcgm.Close()
	e := newTestEngine(t, func(c *config.LiveConfig) {
		c.DCGM = config.LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: dcgm.URL + "/metrics"}
		c.HistoryPoints = 60
	})
	e.testStart()
	for i := 0; i < 3; i++ {
		e.cycle()
	}
	full := e.state()
	if len(full.History.Points) != 3 || full.History.LastSeq != 3 || full.History.FirstSeq != 1 {
		t.Fatalf("expected 3 history points, got %+v", full.History)
	}
	pt := full.History.Points[2]
	if pt.Util == nil || *pt.Util != 40 || pt.FB == nil || *pt.FB != 50 || len(pt.G["GPU-1"]) != 4 {
		t.Fatalf("history point wrong: %+v", pt)
	}
	e.cycle()
	delta := e.State(full.History.LastSeq, full.ActivityLastSeq, Access{})
	if len(delta.History.Points) != 1 || delta.History.Points[0].Seq != 4 {
		t.Fatalf("expected only the new history point, got %+v", delta.History.Points)
	}
	if len(delta.Activity) != 0 {
		t.Fatalf("no new activity expected, got %+v", delta.Activity)
	}
	// The ring is bounded.
	for i := 0; i < 70; i++ {
		e.cycle()
	}
	if st := e.state(); len(st.History.Points) != 60 || st.History.FirstSeq != st.History.LastSeq-59 {
		t.Fatalf("history ring should hold exactly 60 points: len=%d first=%d last=%d", len(st.History.Points), st.History.FirstSeq, st.History.LastSeq)
	}
}

func TestConfigUpdateRestartsSessionAndValidates(t *testing.T) {
	cl := &fakeCluster{gpus: []fakeGPU{{UUID: "GPU-1", Node: "n", Util: 40, Used: 500, Free: 500, Temp: 55, Power: 150}}}
	dcgm := httptest.NewServer(http.HandlerFunc(cl.dcgmHandler))
	defer dcgm.Close()
	e := newTestEngine(t, nil)
	e.testStart()
	e.cycle()
	if n := len(e.state().GPUs); n != 0 {
		t.Fatalf("no source enabled yet, expected 0 GPUs, got %d", n)
	}

	bad := "ftp://example.com/metrics"
	on := true
	if err := e.UpdateConfig(ConfigPatch{DCGM: &SourcePatch{Enabled: &on, Endpoint: &bad}}); err == nil {
		t.Fatalf("a non-http endpoint must be rejected")
	}
	creds := "http://user:pw@127.0.0.1:9400/metrics"
	if err := e.UpdateConfig(ConfigPatch{DCGM: &SourcePatch{Enabled: &on, Endpoint: &creds}}); err == nil {
		t.Fatalf("an endpoint with embedded credentials must be rejected")
	}

	good := dcgm.URL + "/metrics"
	token := "prom-secret"
	if err := e.UpdateConfig(ConfigPatch{DCGM: &SourcePatch{Enabled: &on, Endpoint: &good}, Prometheus: &SourcePatch{BearerToken: &token}}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	e.cycle()
	st := e.state()
	if len(st.GPUs) != 1 || !st.Config.DCGM.Explicit || st.Config.DCGM.Endpoint != good {
		t.Fatalf("the new endpoint should be live after the update: gpus=%d cfg=%+v", len(st.GPUs), st.Config.DCGM)
	}
	if st.Status.Tick != 1 {
		t.Fatalf("a connection change restarts the session (tick counter resets), got tick %d", st.Status.Tick)
	}

	// Moving a source to another endpoint drops the token that was entered for the old one.
	other := "http://127.0.0.1:1"
	if err := e.UpdateConfig(ConfigPatch{Prometheus: &SourcePatch{Enabled: &on, Endpoint: &other}}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if e.state().Config.Prometheus.TokenSet {
		t.Fatalf("a bearer token must not follow the source to a different endpoint")
	}

	// Changing only the pace keeps the session.
	tickBefore := e.state().Status.Tick
	interval := 5.0
	if err := e.UpdateConfig(ConfigPatch{IntervalSeconds: &interval}); err != nil {
		t.Fatalf("update interval: %v", err)
	}
	if st := e.state(); st.Status.IntervalSeconds != 5 || st.Status.Tick != tickBefore {
		t.Fatalf("interval change should not restart the session: %+v", st.Status)
	}
	tooFast := 0.1
	if err := e.UpdateConfig(ConfigPatch{IntervalSeconds: &tooFast}); err == nil {
		t.Fatalf("an interval below one second must be rejected")
	}
}

func TestStartStopLoopAndIdleAutoStop(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Live.NvidiaSMIEnabled = false
	cfg.Live.DCGM.Enabled = false
	cfg.Live.Prometheus.Enabled = false
	cfg.Live.Kubernetes.Enabled = false
	cfg.Live.IntervalSeconds = 1
	e := NewEngine(cfg, nil)
	defer e.Stop("")
	if err := e.Start(0); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && e.Status(Access{}).Tick < 2 {
		time.Sleep(50 * time.Millisecond)
	}
	st := e.Status(Access{})
	if st.State != "running" || st.Tick < 2 {
		t.Fatalf("the collector loop should be ticking: %+v", st)
	}
	e.Stop("Stopped from the console.")
	if st := e.Status(Access{}); st.State != "idle" || st.StateReason != "Stopped from the console." {
		t.Fatalf("expected idle after stop: %+v", st)
	}
	tickAfterStop := e.Status(Access{}).Tick
	time.Sleep(1300 * time.Millisecond)
	if got := e.Status(Access{}).Tick; got != tickAfterStop {
		t.Fatalf("no cycle may run after Stop (tick %d -> %d)", tickAfterStop, got)
	}

	// Idle auto-stop: nobody polls State, so the loop stops on its own.
	e.mu.Lock()
	e.cfg.IdleTimeoutSeconds = 1
	e.mu.Unlock()
	if err := e.Start(0); err != nil {
		t.Fatalf("restart: %v", err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && e.Status(Access{}).State == "running" {
		time.Sleep(100 * time.Millisecond)
	}
	if st := e.Status(Access{}); st.State != "idle" || !strings.Contains(st.StateReason, "Auto-stopped") {
		t.Fatalf("expected an idle auto-stop, got %+v", st)
	}

	// A disabled live console refuses to start.
	off := config.DefaultConfig()
	off.Live.Enabled = false
	disabled := NewEngine(off, nil)
	if err := disabled.Start(0); err != ErrLiveDisabled {
		t.Fatalf("expected ErrLiveDisabled, got %v", err)
	}
	if st := disabled.Status(Access{ConfigMutable: true}); st.Supported || st.ConfigMutable {
		t.Fatalf("a disabled console must report unsupported: %+v", st)
	}
}

func TestSampleThinningAndResampling(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var samples []domain.GPUMetricPoint
	// 3 hours of 10-second samples, oldest first; one old sample carries an Xid.
	for ts := now.Add(-3 * time.Hour); !ts.After(now); ts = ts.Add(10 * time.Second) {
		p := domain.GPUMetricPoint{Timestamp: ts, GPUUtilPercent: 50}
		if ts.Equal(now.Add(-2 * time.Hour).Add(10 * time.Second)) {
			p.XIDErrorCode = 79
		}
		samples = append(samples, p)
	}
	thinned := thinSamples(samples, now)
	recent, old, xid := 0, 0, 0
	for _, s := range thinned {
		if s.Timestamp.Before(now.Add(-rawSampleWindow)) {
			old++
		} else {
			recent++
		}
		if s.XIDErrorCode == 79 {
			xid++
		}
	}
	if recent != 91 {
		t.Fatalf("the last 15 minutes must keep full resolution (91 samples), got %d", recent)
	}
	if old < 165 || old > 167 {
		t.Fatalf("older samples should be thinned to about one per minute (165 + the Xid sample), got %d", old)
	}
	if xid != 1 {
		t.Fatalf("a sample carrying an Xid must survive thinning")
	}

	// Resampling weights every part of the window equally.
	var skewed []domain.GPUMetricPoint
	base := now.Add(-10 * time.Minute)
	for i := 0; i < 5; i++ { // first 5 minutes: one sample per minute at 0%
		skewed = append(skewed, domain.GPUMetricPoint{Timestamp: base.Add(time.Duration(i) * time.Minute), GPUUtilPercent: 0})
	}
	for i := 0; i < 300; i++ { // last 5 minutes: one sample per second at 100%
		skewed = append(skewed, domain.GPUMetricPoint{Timestamp: base.Add(5*time.Minute + time.Duration(i)*time.Second), GPUUtilPercent: 100})
	}
	buckets := resampleWindow(skewed, base, time.Minute)
	if len(buckets) != 10 {
		t.Fatalf("expected 10 one-minute buckets, got %d", len(buckets))
	}
	sum := 0.0
	for _, b := range buckets {
		sum += b.GPUUtilPercent
	}
	if mean := sum / float64(len(buckets)); mean != 50 {
		t.Fatalf("time-weighted mean should be 50%%, got %.1f%%", mean)
	}
}
