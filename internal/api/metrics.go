// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package api

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/Sievehouse/gpu-lens/internal/live"
)

// statusRecorder captures the response status for request counting.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status, r.wroteHeader = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

type httpCounters struct {
	mu     sync.Mutex
	byCode map[int]int64
}

func (c *httpCounters) add(code int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byCode == nil {
		c.byCode = map[int]int64{}
	}
	c.byCode[code]++
}

func (c *httpCounters) snapshot() map[int]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int]int64, len(c.byCode))
	for k, v := range c.byCode {
		out[k] = v
	}
	return out
}

type promWriter struct{ b strings.Builder }

func (p *promWriter) family(name, typ, help string) {
	fmt.Fprintf(&p.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (p *promWriter) sample(name string, labels map[string]string, v float64) {
	p.b.WriteString(name)
	if len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf(`%s="%s"`, k, escapeLabel(labels[k])))
		}
		p.b.WriteString("{" + strings.Join(parts, ",") + "}")
	}
	fmt.Fprintf(&p.b, " %s\n", formatFloat(v))
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// handleMetrics exposes GPU Lens's own health in the Prometheus text format. It never re-exports GPU telemetry
// (that is DCGM exporter's job); it answers "is GPU Lens itself healthy and collecting?".
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	p := &promWriter{}
	p.family("gpulens_build_info", "gauge", "GPU Lens build information.")
	p.sample("gpulens_build_info", map[string]string{"version": live.Version, "runtime": "go", "goversion": runtime.Version()}, 1)
	p.family("gpulens_process_start_time_seconds", "gauge", "Start time of the GPU Lens process since the Unix epoch.")
	p.sample("gpulens_process_start_time_seconds", nil, float64(s.startedAt.Unix()))
	p.family("gpulens_auth_enabled", "gauge", "1 when HTTP basic authentication is required.")
	p.sample("gpulens_auth_enabled", nil, b2f(s.auth.Enabled()))

	p.family("gpulens_http_requests_total", "counter", "HTTP requests served, by status code.")
	codes := s.httpStats.snapshot()
	keys := make([]int, 0, len(codes))
	for k := range codes {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		p.sample("gpulens_http_requests_total", map[string]string{"code": fmt.Sprintf("%d", k)}, float64(codes[k]))
	}

	if s.live != nil {
		m := s.live.MetricsSnapshot()
		p.family("gpulens_live_enabled", "gauge", "1 when the live console is available.")
		p.sample("gpulens_live_enabled", nil, b2f(m.Enabled))
		p.family("gpulens_live_running", "gauge", "1 while a live collection session is running.")
		p.sample("gpulens_live_running", nil, b2f(m.Running))
		p.family("gpulens_live_cycles", "gauge", "Collection cycles completed in the current session.")
		p.sample("gpulens_live_cycles", nil, float64(m.Tick))
		p.family("gpulens_live_cycle_duration_seconds", "gauge", "Duration of the last collection cycle.")
		p.sample("gpulens_live_cycle_duration_seconds", nil, m.LastTickDuration.Seconds())
		p.family("gpulens_live_last_cycle_timestamp_seconds", "gauge", "Time of the last completed collection cycle (0 if none).")
		last := 0.0
		if !m.LastTickAt.IsZero() {
			last = float64(m.LastTickAt.Unix())
		}
		p.sample("gpulens_live_last_cycle_timestamp_seconds", nil, last)
		p.family("gpulens_live_interval_seconds", "gauge", "Configured live refresh interval.")
		p.sample("gpulens_live_interval_seconds", nil, m.IntervalSeconds)
		p.family("gpulens_live_source_enabled", "gauge", "1 when a telemetry source is enabled.")
		for _, src := range m.Sources {
			p.sample("gpulens_live_source_enabled", map[string]string{"source": src.ID}, b2f(src.Enabled))
		}
		p.family("gpulens_live_source_up", "gauge", "1 when a telemetry source answered on its last check (connected or degraded).")
		for _, src := range m.Sources {
			p.sample("gpulens_live_source_up", map[string]string{"source": src.ID, "state": src.State}, b2f(src.Up))
		}
		p.family("gpulens_live_source_latency_seconds", "gauge", "Latency of the last check of each source.")
		for _, src := range m.Sources {
			p.sample("gpulens_live_source_latency_seconds", map[string]string{"source": src.ID}, src.LatencyMs/1000)
		}
		p.family("gpulens_live_gpus", "gauge", "GPUs visible to the live console, by freshness.")
		p.sample("gpulens_live_gpus", map[string]string{"freshness": "fresh"}, float64(m.GPUsFresh))
		p.sample("gpulens_live_gpus", map[string]string{"freshness": "stale"}, float64(m.GPUsStale))
		p.family("gpulens_live_incidents_open", "gauge", "Open live incidents, by severity.")
		for _, sev := range []string{"critical", "warning", "info"} {
			p.sample("gpulens_live_incidents_open", map[string]string{"severity": sev}, float64(m.OpenIncidents[sev]))
		}
		p.family("gpulens_live_incidents_resolved", "gauge", "Incidents in the resolved log.")
		p.sample("gpulens_live_incidents_resolved", nil, float64(m.ResolvedIncidents))
		p.family("gpulens_live_threshold_overrides", "gauge", "Live rule thresholds that differ from the configured defaults.")
		p.sample("gpulens_live_threshold_overrides", nil, float64(m.ThresholdOverrides))
		p.family("gpulens_readonly_guard_ok", "gauge", "1 when the read-only transport self-test passed (mutating HTTP verbs are blocked).")
		p.sample("gpulens_readonly_guard_ok", nil, b2f(m.ReadOnlyGuardOK))
		p.family("gpulens_alerts_sent_total", "counter", "Alerts delivered, by destination.")
		p.family("gpulens_alerts_failed_total", "counter", "Alert deliveries that failed after retries, by destination.")
		p.family("gpulens_alerts_dropped_total", "counter", "Alerts dropped because the delivery queue was full, by destination.")
		dests := make([]string, 0, len(m.AlertCounters))
		for d := range m.AlertCounters {
			dests = append(dests, d)
		}
		sort.Strings(dests)
		for _, d := range dests {
			c := m.AlertCounters[d]
			p.sample("gpulens_alerts_sent_total", map[string]string{"destination": d}, float64(c[0]))
			p.sample("gpulens_alerts_failed_total", map[string]string{"destination": d}, float64(c[1]))
			p.sample("gpulens_alerts_dropped_total", map[string]string{"destination": d}, float64(c[2]))
		}
		if m.PersistenceEnabled {
			p.family("gpulens_live_state_saved_timestamp_seconds", "gauge", "Time the live state file was last written (0 if never).")
			saved := 0.0
			if !m.StateSavedAt.IsZero() {
				saved = float64(m.StateSavedAt.Unix())
			}
			p.sample("gpulens_live_state_saved_timestamp_seconds", nil, saved)
			p.family("gpulens_live_state_save_ok", "gauge", "1 when the last live state save succeeded.")
			p.sample("gpulens_live_state_save_ok", nil, b2f(m.StateLastError == ""))
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(p.b.String()))
	}
}
