// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package live

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/alert"
	"github.com/Sievehouse/gpu-lens/internal/config"
)

func smiEngine(t *testing.T) *Engine {
	path := fakeSMIPath(t)
	return newTestEngine(t, func(c *config.LiveConfig) {
		c.NvidiaSMIEnabled = true
		c.NvidiaSMIPath = path
		c.IntervalSeconds = 1
	})
}

func TestAlertsFollowLiveIncidents(t *testing.T) {
	var mu sync.Mutex
	var types []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var ev alert.Event
		_ = json.Unmarshal(b, &ev)
		mu.Lock()
		types = append(types, ev.Type+":"+ev.Incident.RuleID)
		mu.Unlock()
	}))
	defer hook.Close()
	n, err := alert.New(alert.Config{WebhookURL: hook.URL, MinSeverity: "warning"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	t.Setenv("FAKE_SMI_TEMP0", "95")
	e := smiEngine(t)
	e.SetAlerter(n)
	e.testStart()
	e.cycle()
	e.cycle()
	t.Setenv("FAKE_SMI_TEMP0", "54")
	e.cycle()
	e.cycle()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := strings.Join(types, ",")
		mu.Unlock()
		if strings.Contains(got, "incident.opened:") && strings.Contains(got, "incident.resolved:") {
			if strings.Count(got, "incident.opened:") != 1 {
				t.Fatalf("opened must be sent once, got %s", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("expected an opened and a resolved alert, got %v", types)
}

func TestLiveStatePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "live.json")
	t.Setenv("FAKE_SMI_TEMP0", "95")
	a := smiEngine(t)
	if err := a.SetStateFile(path); err != nil {
		t.Fatal(err)
	}
	a.testStart()
	a.cycle()
	if err := a.SetThresholds(map[string]float64{"temp_warning_celsius": 78}, false); err != nil {
		t.Fatal(err)
	}
	a.cycle()
	open := a.state().Overview.ActiveIncidents
	if len(open) == 0 {
		t.Fatal("expected a thermal incident at 95°C")
	}
	firstSeen := open[0].FirstSeenAt
	if err := a.SaveState(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode: %v %v", fi, err)
	}

	time.Sleep(20 * time.Millisecond)
	b := smiEngine(t)
	if err := b.SetStateFile(path); err != nil {
		t.Fatal(err)
	}
	st := b.state()
	if st.Thresholds["temp_warning_celsius"] != 78 || len(st.ThresholdOverrides) != 1 {
		t.Fatalf("threshold override not restored: %v", st.ThresholdOverrides)
	}
	if !st.Persistence.Enabled || st.Persistence.LoadedAt == nil {
		t.Fatalf("persistence status: %+v", st.Persistence)
	}
	b.testStart()
	b.cycle()
	reopened := b.state().Overview.ActiveIncidents
	if len(reopened) == 0 || !reopened[0].FirstSeenAt.Equal(firstSeen) {
		t.Fatalf("incident age not carried across restart: %v vs %v", reopened, firstSeen)
	}
	found := false
	for _, a := range b.state().Activity {
		if strings.HasPrefix(a.Title, "Incident still open") {
			found = true
		}
	}
	if !found {
		t.Fatal("a restored incident should be logged as still open, not as new")
	}

	// A corrupt file is reported and ignored.
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := smiEngine(t)
	if err := c.SetStateFile(path); err == nil || c.state().Persistence.LastError == "" {
		t.Fatal("a corrupt state file must be reported")
	}
}

func TestMetricsSnapshot(t *testing.T) {
	t.Setenv("FAKE_SMI_TEMP0", "95")
	e := smiEngine(t)
	e.testStart()
	e.cycle()
	m := e.MetricsSnapshot()
	if !m.Running || m.Tick == 0 || m.GPUsFresh == 0 || m.OpenIncidents["critical"]+m.OpenIncidents["warning"] == 0 || !m.ReadOnlyGuardOK {
		t.Fatalf("metrics snapshot: %+v", m)
	}
}

func TestAMDGPUsThroughTheEngine(t *testing.T) {
	path, err := filepath.Abs("../../tests/fixtures/fake-amd-smi")
	if err != nil {
		t.Fatal(err)
	}
	fakeSMIPath(t) // skips on Windows like the NVIDIA double
	t.Setenv("FAKE_AMD_TEMP0", "96")
	e := newTestEngine(t, func(c *config.LiveConfig) {
		c.NvidiaSMIEnabled = false
		c.AMDSMIEnabled = true
		c.AMDSMIPath = path
		c.IntervalSeconds = 1
	})
	e.testStart()
	e.cycle()
	st := e.state()
	if len(st.GPUs) != 2 || st.GPUs[0].ModelName != "AMD Instinct MI300X" {
		t.Fatalf("AMD GPUs: %+v", st.GPUs)
	}
	src := findSource(t, st, SourceAMDSMI)
	if src.State != StateConnected {
		t.Fatalf("amd source: %+v", src)
	}
	if _, ok := incidentRules(st)["GPU-THERM-006"]; !ok && len(st.Overview.ActiveIncidents) == 0 {
		t.Fatalf("a 96°C AMD GPU should raise a thermal incident; got %v", incidentRules(st))
	}
	found := false
	for _, g := range st.Preflight.Groups {
		if g.ID == "amd" {
			for _, c := range g.Checks {
				if c.ID == "amd.query" && c.Status == CheckPass {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("preflight should pass the AMD driver query")
	}
	for _, inc := range st.Overview.ActiveIncidents {
		for _, ev := range inc.SupportingEvidence {
			if ev.Source == "dcgm_exporter" {
				t.Fatalf("evidence for an AMD GPU must not claim DCGM provenance: %+v", ev)
			}
		}
	}
}
