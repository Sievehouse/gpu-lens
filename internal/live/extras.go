// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package live

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/alert"
	"github.com/Sievehouse/gpu-lens/internal/config"
)

// ---------------------------------------------------------------------------------------------
// Alerting
// ---------------------------------------------------------------------------------------------

// SetAlerter attaches the incident notifier. Call before Start. A nil notifier disables alerting.
func (e *Engine) SetAlerter(n *alert.Notifier) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.alerts = n
}

// AlertStatus reports the alert destinations and their delivery record.
func (e *Engine) AlertStatus() alert.Status {
	e.mu.Lock()
	n := e.alerts
	e.mu.Unlock()
	return n.Status()
}

// ErrAlertsDisabled means no alert destination is configured.
var ErrAlertsDisabled = errors.New("no alert destination is configured: start GPU Lens with -alert-slack-webhook and/or -alert-webhook-url")

// TestAlerts sends a test alert to every configured destination and reports the outcome.
func (e *Engine) TestAlerts() ([]alert.TestResult, error) {
	e.mu.Lock()
	n := e.alerts
	meta := alert.Meta{Cluster: e.cfg.ClusterName, DataMode: "live", Version: Version}
	if e.snapshot != nil {
		meta.Cluster, meta.DataMode = e.snapshot.ClusterName, e.snapshot.DataMode
	}
	if meta.Cluster == "" {
		meta.Cluster = "live@" + e.hostname
	}
	e.mu.Unlock()
	if !n.Enabled() {
		return nil, ErrAlertsDisabled
	}
	res := n.SendTest(meta)
	ok := 0
	for _, r := range res {
		if r.OK {
			ok++
		}
	}
	e.mu.Lock()
	sev := "success"
	if ok < len(res) {
		sev = "warning"
	}
	e.addActivityLocked(time.Now().UTC(), sev, "alert", "Test alert sent", fmt.Sprintf("%d of %d destination(s) accepted it.", ok, len(res)))
	e.mu.Unlock()
	return res, nil
}

// AddAudit writes an audited operator action into the session activity log so it shows in the console.
func (e *Engine) AddAudit(title, detail string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addActivityLocked(time.Now().UTC(), "info", "audit", title, detail)
}

// Running reports whether a collection session is active.
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// ---------------------------------------------------------------------------------------------
// Self-observability (/metrics)
// ---------------------------------------------------------------------------------------------

// SourceMetric is the health of one source, for /metrics.
type SourceMetric struct {
	ID        string
	Enabled   bool
	Up        bool
	State     string
	LatencyMs float64
}

// Metrics is a point-in-time view of the engine's own health. It deliberately carries no GPU telemetry: GPU
// metrics belong to DCGM exporter and Prometheus, not to GPU Lens.
type Metrics struct {
	Enabled             bool
	Running             bool
	Tick                int
	LastTickAt          time.Time
	LastTickDuration    time.Duration
	IntervalSeconds     float64
	Sources             []SourceMetric
	GPUsFresh           int
	GPUsStale           int
	OpenIncidents       map[string]int
	ResolvedIncidents   int
	ThresholdOverrides  int
	ReadOnlyGuardOK     bool
	AlertCounters       map[string][3]int
	StateSavedAt        time.Time
	StateLastError      string
	PersistenceEnabled  bool
	AuditEntriesInState int
}

// MetricsSnapshot returns the engine's own health for the /metrics endpoint.
func (e *Engine) MetricsSnapshot() Metrics {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := Metrics{
		Enabled: e.cfg.Enabled, Running: e.running, Tick: e.tick, LastTickAt: e.lastTickAt, LastTickDuration: e.lastTickDur,
		IntervalSeconds: e.cfg.IntervalSeconds, OpenIncidents: map[string]int{"critical": 0, "warning": 0, "info": 0},
		ResolvedIncidents: len(e.resolved), ThresholdOverrides: len(e.overrides), ReadOnlyGuardOK: e.guard.OK(),
		AlertCounters: e.alerts.Counters(), StateSavedAt: e.stateSavedAt, StateLastError: e.stateErr,
		PersistenceEnabled: e.statePath != "",
	}
	ids := make([]string, 0, len(e.src))
	for id := range e.src {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		st := e.src[id]
		sm := SourceMetric{ID: id, Enabled: st.status.Enabled, State: st.status.State,
			Up: st.status.State == StateConnected || st.status.State == StateDegraded}
		if st.status.LastLatencyMs != nil {
			sm.LatencyMs = *st.status.LastLatencyMs
		}
		m.Sources = append(m.Sources, sm)
	}
	for _, g := range e.gpuViews {
		if g.IsStale {
			m.GPUsStale++
		} else {
			m.GPUsFresh++
		}
	}
	for _, inc := range e.open {
		m.OpenIncidents[string(inc.Severity)]++
	}
	return m
}

// ---------------------------------------------------------------------------------------------
// Durable live state
// ---------------------------------------------------------------------------------------------

// PersistenceStatus describes the live state file.
type PersistenceStatus struct {
	Enabled   bool       `json:"enabled"`
	Path      string     `json:"path"`
	LoadedAt  *time.Time `json:"loaded_at"`
	SavedAt   *time.Time `json:"saved_at"`
	LastError string     `json:"last_error"`
	Note      string     `json:"note"`
}

func (e *Engine) persistenceStatusLocked() PersistenceStatus {
	ps := PersistenceStatus{Enabled: e.statePath != "", Path: e.statePath, LastError: e.stateErr}
	if !e.stateLoadedAt.IsZero() {
		t := e.stateLoadedAt
		ps.LoadedAt = &t
	}
	if !e.stateSavedAt.IsZero() {
		t := e.stateSavedAt
		ps.SavedAt = &t
	}
	if ps.Enabled {
		ps.Note = "Threshold overrides, the resolved-incident log, open-incident ages and the alert record survive restarts. Single-node file; not shared between replicas."
	} else {
		ps.Note = "Live state is kept in memory only. Start with -live-state-file to keep thresholds, resolved incidents and alert history across restarts."
	}
	return ps
}

// persistedState is the on-disk format of the live state file.
type persistedState struct {
	Format             string                             `json:"format"`
	SavedAt            time.Time                          `json:"saved_at"`
	GPULensVersion     string                             `json:"gpulens_version"`
	ThresholdOverrides map[string]float64                 `json:"threshold_overrides"`
	Resolved           []ResolvedIncident                 `json:"resolved_incidents"`
	OpenFirstSeen      map[string]time.Time               `json:"open_first_seen"`
	Alerts             map[string]alert.PersistedIncident `json:"alerts"`
}

const stateFormat = "gpulens.live-state/v1"

// SetStateFile enables durable live state and loads it if the file exists. Call before Start.
func (e *Engine) SetStateFile(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.statePath = path
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		e.stateErr = err.Error()
		return err
	}
	var ps persistedState
	if err := json.Unmarshal(b, &ps); err != nil || ps.Format != stateFormat {
		e.stateErr = "the live state file is not in a format this version understands; starting fresh"
		return fmt.Errorf("live: %s: %s", path, e.stateErr)
	}
	if len(ps.ThresholdOverrides) > 0 {
		if th, err := config.ApplyThresholdOverrides(e.base.Thresholds, ps.ThresholdOverrides); err == nil {
			e.thresholds, e.overrides = th, ps.ThresholdOverrides
		}
	}
	if len(ps.Resolved) > maxResolvedIncidents {
		ps.Resolved = ps.Resolved[len(ps.Resolved)-maxResolvedIncidents:]
	}
	e.restoredResolved = ps.Resolved
	e.resolved = append([]ResolvedIncident(nil), ps.Resolved...)
	e.restoredFirstSeen = ps.OpenFirstSeen
	e.alerts.Restore(ps.Alerts)
	e.stateLoadedAt = time.Now().UTC()
	return nil
}

// SaveState writes the durable live state atomically (temp file + rename, mode 0600).
func (e *Engine) SaveState() error {
	e.mu.Lock()
	path := e.statePath
	if path == "" {
		e.mu.Unlock()
		return nil
	}
	ps := persistedState{
		Format: stateFormat, SavedAt: time.Now().UTC(), GPULensVersion: Version,
		ThresholdOverrides: map[string]float64{}, Resolved: append([]ResolvedIncident(nil), e.resolved...),
		OpenFirstSeen: map[string]time.Time{},
	}
	for k, v := range e.overrides {
		ps.ThresholdOverrides[k] = v
	}
	for fp, inc := range e.open {
		ps.OpenFirstSeen[fp] = inc.FirstSeenAt
	}
	// Incidents restored at boot that have not been re-evaluated yet are kept, not forgotten.
	for fp, t := range e.restoredFirstSeen {
		if _, ok := ps.OpenFirstSeen[fp]; !ok {
			ps.OpenFirstSeen[fp] = t
		}
	}
	n := e.alerts
	e.mu.Unlock()
	ps.Alerts = n.Snapshot()

	err := writeFileAtomic(path, ps)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.stateErr = err.Error()
		return err
	}
	e.stateErr = ""
	e.stateSavedAt = ps.SavedAt
	return nil
}

func writeFileAtomic(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gpulens-live-state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// RunStateSaver saves the live state every interval until stop is closed, then once more.
func (e *Engine) RunStateSaver(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = e.SaveState()
		case <-stop:
			_ = e.SaveState()
			return
		}
	}
}

// ---------------------------------------------------------------------------------------------
// Fleet summary (multi-cluster)
// ---------------------------------------------------------------------------------------------

// Summary is the compact, read-only health of this instance that a fleet hub polls from
// GET /api/v1/live/summary. It contains counts and verdicts only: no hostnames of nodes, pods or tokens.
type Summary struct {
	Schema           string         `json:"schema"`
	Cluster          string         `json:"cluster"`
	Hostname         string         `json:"hostname"`
	Version          string         `json:"version"`
	Runtime          string         `json:"runtime"`
	State            string         `json:"state"`
	Verdict          string         `json:"verdict"`
	VerdictText      string         `json:"verdict_text"`
	FleetStatus      string         `json:"fleet_status"`
	DataMode         string         `json:"data_mode"`
	Emulated         bool           `json:"emulated"`
	GPUsTotal        int            `json:"gpus_total"`
	GPUsFresh        int            `json:"gpus_fresh"`
	Nodes            int            `json:"nodes"`
	Incidents        map[string]int `json:"incidents"`
	TopIncidents     []string       `json:"top_incidents"`
	SourcesConnected int            `json:"sources_connected"`
	SourcesEnabled   int            `json:"sources_enabled"`
	LastCycleAt      *time.Time     `json:"last_cycle_at"`
	GeneratedAt      time.Time      `json:"generated_at"`
}

// SummarySchema identifies the summary payload format.
const SummarySchema = "gpulens.summary/v1"

// Summary returns the compact health of this instance.
func (e *Engine) Summary() Summary {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now().UTC()
	pf := e.preflightLocked(now)
	st := e.statusLocked(now, Access{})
	s := Summary{
		Schema: SummarySchema, Cluster: e.cfg.ClusterName, Hostname: e.hostname, Version: Version, Runtime: "go",
		State: st.State, Verdict: pf.Verdict, VerdictText: pf.VerdictText, Emulated: st.Emulated, DataMode: "live",
		Incidents: map[string]int{"critical": 0, "warning": 0, "info": 0}, TopIncidents: []string{},
		LastCycleAt: st.LastTickAt, GeneratedAt: now, Nodes: len(e.viewNodes),
	}
	if s.Cluster == "" {
		s.Cluster = "live@" + e.hostname
	}
	if e.snapshot != nil {
		s.DataMode = e.snapshot.DataMode
		if e.snapshot.ClusterName != "" {
			s.Cluster = e.snapshot.ClusterName
		}
	}
	if e.overview != nil {
		s.FleetStatus = e.overview.FleetHealthStatus
		for _, inc := range e.overview.ActiveIncidents {
			s.Incidents[string(inc.Severity)]++
			if len(s.TopIncidents) < 3 {
				s.TopIncidents = append(s.TopIncidents, fmt.Sprintf("[%s] %s", inc.Severity, inc.Title))
			}
		}
	}
	for _, g := range e.gpuViews {
		s.GPUsTotal++
		if !g.IsStale {
			s.GPUsFresh++
		}
	}
	for _, src := range e.src {
		if src.status.Kind == "host" || !src.status.Enabled {
			continue
		}
		if !(src.everConnected || (src.status.Explicit && !isLocalDriverSource(src.status.ID)) || src.status.State == StateError) {
			// A default address that never answered is not a source of this instance.
			continue
		}
		s.SourcesEnabled++
		if src.status.State == StateConnected || src.status.State == StateDegraded {
			s.SourcesConnected++
		}
	}
	return s
}
