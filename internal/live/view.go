package live

import (
	"sort"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/domain"
	"github.com/gpulens/gpu-lens/internal/source"
)

// Access describes what the caller of the live API may change.
type Access struct {
	ConfigMutable bool
	Reason        string
}

func (e *Engine) statusLocked(now time.Time, access Access) Status {
	st := Status{
		Runtime:             "go",
		Version:             Version,
		Supported:           e.cfg.Enabled,
		State:               "idle",
		StateReason:         e.stateReason,
		Tick:                e.tick,
		IntervalSeconds:     e.cfg.IntervalSeconds,
		LastTickDurationMs:  float64(e.lastTickDur.Microseconds()) / 1000.0,
		ServerTime:          now,
		ConfigMutable:       access.ConfigMutable && e.cfg.Enabled,
		ConfigMutableReason: access.Reason,
		AutoStart:           e.cfg.AutoStart,
		Hostname:            e.hostname,
	}
	if !e.cfg.Enabled {
		st.UnsupportedReason = ErrLiveDisabled.Error()
	}
	if e.running {
		st.State = "running"
	}
	if !e.startedAt.IsZero() {
		t := e.startedAt
		st.StartedAt = &t
	}
	if !e.lastTickAt.IsZero() {
		t := e.lastTickAt
		st.LastTickAt = &t
	}
	for _, s := range e.src {
		if s.status.Emulated && (s.status.State == StateConnected || s.status.State == StateDegraded) {
			st.Emulated = true
		}
	}
	return st
}

// Status returns the engine status without touching the viewer idle timer.
func (e *Engine) Status(access Access) Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statusLocked(time.Now().UTC(), access)
}

// State returns the full live payload. `historySince` and `activitySince` are the last sequence numbers the caller
// already holds; only newer history points and activity entries are returned.
func (e *Engine) State(historySince, activitySince int, access Access) State {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now().UTC()
	e.lastPoll = time.Now()

	out := State{
		Status:            e.statusLocked(now, access),
		Config:            e.cfg.View(),
		Overview:          e.overview,
		Preflight:         e.preflightLocked(now),
		Sources:           []SourceStatus{},
		Host:              e.hostVitals,
		GPUs:              e.gpuViews,
		Processes:         e.processes,
		Nodes:             e.viewNodes,
		Pods:              e.viewPods,
		Events:            []domain.K8sEvent{},
		Activity:          []ActivityEntry{},
		ActivityLastSeq:   e.activitySeq,
		ResolvedIncidents: []ResolvedIncident{},
		Thresholds:        config.ThresholdsToMap(e.thresholds),
		ThresholdDefaults: config.ThresholdsToMap(e.base.Thresholds),
		Rules:             e.rules.Definitions(e.thresholds),
		Notes:             []string{},
	}
	if out.GPUs == nil {
		out.GPUs = []GPUView{}
	}
	if out.Processes == nil {
		out.Processes = []ProcessView{}
	}
	if out.Nodes == nil {
		out.Nodes = []domain.NodeState{}
	}
	if out.Pods == nil {
		out.Pods = []domain.WorkloadPod{}
	}
	for _, id := range []string{SourceNvidiaSMI, SourceDCGM, SourcePrometheus, SourceKubernetes, SourceHost} {
		if st := e.src[id]; st != nil {
			s := st.status
			s.Facts = append([]Fact{}, st.status.Facts...)
			s.LatencyHistoryMs = append([]float64{}, st.status.LatencyHistoryMs...)
			out.Sources = append(out.Sources, s)
		}
	}
	// Newest warning events first, capped for the console.
	if n := len(e.k8sEvents); n > 0 {
		start := 0
		if n > 200 {
			start = n - 200
		}
		for i := n - 1; i >= start; i-- {
			out.Events = append(out.Events, e.k8sEvents[i])
		}
	}

	out.History = HistoryView{Epoch: e.historyEpoch, LastSeq: e.seq, Capacity: e.cfg.HistoryPoints, Points: []HistoryPoint{}}
	if len(e.history) > 0 {
		out.History.FirstSeq = e.history[0].Seq
		idx := sort.Search(len(e.history), func(i int) bool { return e.history[i].Seq > historySince })
		out.History.Points = append(out.History.Points, e.history[idx:]...)
	}
	if len(e.activity) > 0 {
		idx := sort.Search(len(e.activity), func(i int) bool { return e.activity[i].Seq > activitySince })
		out.Activity = append(out.Activity, e.activity[idx:]...)
	}
	for i := len(e.resolved) - 1; i >= 0; i-- {
		out.ResolvedIncidents = append(out.ResolvedIncidents, e.resolved[i])
	}
	out.ThresholdOverrides = sortedKeys(e.overrides)

	if e.snapshot != nil {
		out.SourcesDetail = &SourcesDetail{
			DataMode:        e.snapshot.DataMode,
			ScenarioID:      e.snapshot.ScenarioID,
			CollectedAt:     e.snapshot.CollectedAt,
			Sources:         e.snapshot.Sources,
			ExportedMetrics: e.snapshot.ExportedMetrics,
			MetricMappings:  source.LiveMetricMapping(e.base.MetricMappings),
		}
		if out.SourcesDetail.Sources == nil {
			out.SourcesDetail.Sources = []domain.SourceHealth{}
		}
	}

	if out.Status.Emulated {
		out.Notes = append(out.Notes, "At least one connected source is the GPU Lens Live Lab emulator. Its GPUs, pods and events are synthetic; the collection, merge and rule evaluation you see are real.")
	}
	if e.merge.hostWorkloads > 0 {
		out.Notes = append(out.Notes, "No Kubernetes pods are involved on this host: each busy GPU is attributed to the compute process holding most of its memory.")
	}
	if len(e.overrides) > 0 {
		out.Notes = append(out.Notes, "Live rule thresholds differ from the configured defaults. The simulated demo is not affected.")
	}
	return out
}
