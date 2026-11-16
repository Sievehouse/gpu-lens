package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/domain"
	"github.com/gpulens/gpu-lens/internal/rules"
	"github.com/gpulens/gpu-lens/internal/source"
)

const (
	maxActivityEntries   = 400
	maxResolvedIncidents = 100
	latencyHistoryLen    = 40
	notFoundRetry        = 30 * time.Second
	promAuditEvery       = 60 * time.Second
	deviceForgetAfter    = 30 * time.Minute
)

// ErrLiveDisabled is returned when the live console has been turned off by configuration.
var ErrLiveDisabled = errors.New("the live console is disabled on this GPU Lens instance (GPULENS_LIVE_ENABLED=false)")

// sourceState tracks one source across ticks.
type sourceState struct {
	status        SourceStatus
	everConnected bool
	inFlight      bool
	nextDue       time.Time
	lastAuditAt   time.Time
	buildError    string

	smi      *source.NvidiaSMIResult
	dcgm     *source.DCGMScrape
	dcgmNorm *source.LiveNormalizeResult
	prom     *source.PromLiveResult
	k8s      *source.K8sLiveResult
}

// outcome is the result of one collection attempt, handed from a collector goroutine to the tick loop.
type outcome struct {
	id         string
	at         time.Time
	latency    time.Duration
	err        error
	notFound   bool
	smi        *source.NvidiaSMIResult
	dcgm       *source.DCGMScrape
	prom       *source.PromLiveResult
	promAudit  bool
	k8s        *source.K8sLiveResult
	host       *source.HostVitals
	backfill   map[string][]domain.GPUMetricPoint
	backfillOK bool
	backfillEr string
}

// Engine is the live telemetry engine. All exported methods are safe for concurrent use.
type Engine struct {
	mu     sync.Mutex
	base   config.Config
	cfg    config.LiveConfig
	logger *slog.Logger
	rules  *rules.Engine

	thresholds config.RuleThresholds
	overrides  map[string]float64

	hostSrc *source.HostSource
	smiSrc  *source.NvidiaSMISource
	dcgmSrc *source.DCGMExporterSource
	promSrc *source.PrometheusSource
	k8sSrc  *source.KubernetesSource
	src     map[string]*sourceState
	guard   source.GuardSelfTestResult

	running     bool
	manualTicks bool
	generation  int
	loopCtx     context.Context
	cancel      context.CancelFunc
	wake        chan struct{}
	startedAt   time.Time
	stateReason string
	tick        int
	lastTickAt  time.Time
	lastTickDur time.Duration
	lastPoll    time.Time
	pending     []outcome
	hostname    string

	// session data
	devices      map[string]*deviceState
	k8sNodes     []domain.NodeState
	k8sPods      []domain.WorkloadPod
	k8sEvents    []domain.K8sEvent
	k8sNodesOK   bool
	k8sPodsOK    bool
	snapshot     *domain.ClusterSnapshot
	overview     *domain.OverviewResponse
	hostVitals   *source.HostVitals
	gpuViews     []GPUView
	processes    []ProcessView
	viewNodes    []domain.NodeState
	viewPods     []domain.WorkloadPod
	history      []HistoryPoint
	historyEpoch int
	seq          int
	activity     []ActivityEntry
	activitySeq  int
	open         map[string]domain.Incident
	resolved     []ResolvedIncident
	prev         diffState
	backfill     backfillState
	merge        mergeFacts
	exported     map[string]bool
	sessionNotes []string
}

type backfillState struct {
	attempted      bool
	ok             bool
	detail         string
	minutes        float64
	devices        int
	pendingHistory bool
}

// NewEngine builds the live engine from the application configuration. Nothing is collected until Start.
func NewEngine(base config.Config, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	e := &Engine{
		base:       base,
		cfg:        base.Live,
		logger:     logger,
		rules:      rules.NewDefaultEngine(),
		thresholds: base.Thresholds,
		overrides:  map[string]float64{},
		hostname:   source.LocalNodeName(),
		guard:      source.SelfTestReadOnlyGuard(),
	}
	e.rebuildSourcesLocked()
	e.resetSessionLocked()
	return e
}

func (e *Engine) httpTimeout() time.Duration {
	if e.base.Source.HTTPTimeout > 0 {
		return e.base.Source.HTTPTimeout
	}
	return 10 * time.Second
}

// rebuildSourcesLocked (re)creates the source adapters from the current configuration.
func (e *Engine) rebuildSourcesLocked() {
	e.src = map[string]*sourceState{}
	mk := func(id, name, kind, endpoint, readOnly string, enabled, explicit bool) *sourceState {
		st := &sourceState{status: SourceStatus{
			ID: id, Name: name, Kind: kind, Enabled: enabled, Explicit: explicit, Endpoint: endpoint,
			State: StatePending, ReadOnly: readOnly, Facts: []Fact{}, LatencyHistoryMs: []float64{},
		}}
		if !enabled {
			st.status.State = StateDisabled
			st.status.Detail = "Turned off in the live configuration."
		}
		e.src[id] = st
		return st
	}
	timeout := e.httpTimeout()
	maxBytes := e.base.Source.MaxResponseBytes

	e.hostSrc = nil
	mk(SourceHost, "Host vitals (machine running GPU Lens)", "host", "local /proc and /sys",
		"Reads local kernel statistics only; nothing is executed or written.", e.cfg.HostVitals, true)
	if e.cfg.HostVitals {
		e.hostSrc = source.NewHostSource()
	}

	e.smiSrc = nil
	smiEndpoint := "local nvidia-smi (auto-discovered)"
	if e.cfg.NvidiaSMIPath != "" {
		smiEndpoint = "local " + e.cfg.NvidiaSMIPath
	}
	mk(SourceNvidiaSMI, "Local NVIDIA driver (nvidia-smi)", "nvidia_smi", smiEndpoint,
		"Fixed allow-list of read-only query flags; no shell, no state-changing options.",
		e.cfg.NvidiaSMIEnabled, e.cfg.NvidiaSMIPath != "")
	if e.cfg.NvidiaSMIEnabled {
		e.smiSrc = source.NewNvidiaSMISource(e.cfg.NvidiaSMIPath, 8*time.Second, nil)
	}

	const httpReadOnly = "GET/HEAD only — mutating verbs are rejected at the transport before any network I/O."

	e.dcgmSrc = nil
	st := mk(SourceDCGM, "DCGM exporter (direct /metrics scrape)", "dcgm_exporter", e.cfg.DCGM.Endpoint, httpReadOnly, e.cfg.DCGM.Enabled, e.cfg.DCGM.Explicit)
	if e.cfg.DCGM.Enabled {
		if tr, err := source.NewBaseTransport(e.cfg.DCGM.CAFile); err != nil {
			st.buildError = err.Error()
		} else {
			e.dcgmSrc = source.NewDCGMExporterSource(e.cfg.DCGM.Endpoint, timeout, maxBytes, tr)
		}
	}

	e.promSrc = nil
	st = mk(SourcePrometheus, "Prometheus HTTP API", "prometheus", e.cfg.Prometheus.Endpoint, httpReadOnly, e.cfg.Prometheus.Enabled, e.cfg.Prometheus.Explicit)
	if e.cfg.Prometheus.Enabled {
		if tr, err := source.NewBaseTransport(e.cfg.Prometheus.CAFile); err != nil {
			st.buildError = err.Error()
		} else {
			e.promSrc = source.NewPrometheusSource(e.cfg.Prometheus.Endpoint, e.cfg.Prometheus.BearerToken, timeout, maxBytes, e.base.MetricMappings, e.thresholds, tr)
		}
	}

	e.k8sSrc = nil
	st = mk(SourceKubernetes, "Kubernetes API", "kubernetes", e.cfg.Kubernetes.Endpoint, httpReadOnly+" Lists nodes, pods and warning events.", e.cfg.Kubernetes.Enabled, e.cfg.Kubernetes.Explicit)
	if e.cfg.Kubernetes.Enabled {
		if tr, err := source.NewBaseTransport(e.cfg.Kubernetes.CAFile); err != nil {
			st.buildError = err.Error()
		} else {
			e.k8sSrc = source.NewKubernetesSource(e.cfg.Kubernetes.Endpoint, e.cfg.Kubernetes.BearerToken, timeout, maxBytes, tr)
		}
	}
}

// resetSessionLocked clears everything collected during a live session.
func (e *Engine) resetSessionLocked() {
	e.tick = 0
	e.lastTickAt = time.Time{}
	e.lastTickDur = 0
	e.pending = nil
	e.devices = map[string]*deviceState{}
	e.k8sNodes, e.k8sPods, e.k8sEvents = nil, nil, nil
	e.k8sNodesOK, e.k8sPodsOK = false, false
	e.snapshot, e.overview, e.hostVitals = nil, nil, nil
	e.gpuViews, e.processes, e.viewNodes, e.viewPods = []GPUView{}, []ProcessView{}, []domain.NodeState{}, []domain.WorkloadPod{}
	e.history = nil
	e.historyEpoch++
	e.seq = 0
	e.activity = nil
	e.open = map[string]domain.Incident{}
	e.resolved = nil
	e.prev = diffState{}
	e.backfill = backfillState{}
	e.merge = mergeFacts{}
	e.exported = map[string]bool{}
	e.sessionNotes = nil
}

func (e *Engine) effectiveInterval() time.Duration {
	secs := e.cfg.IntervalSeconds
	if secs < 1 {
		secs = 1
	}
	return time.Duration(secs * float64(time.Second))
}

func (e *Engine) refreshPeriodLocked(id string) time.Duration {
	base := e.effectiveInterval()
	floor := time.Duration(0)
	switch id {
	case SourceDCGM:
		floor = 5 * time.Second
	case SourcePrometheus, SourceKubernetes:
		floor = 15 * time.Second
	}
	if base < floor {
		return floor
	}
	return base
}

// Start begins a live session. It returns immediately; collection progress is visible through State.
func (e *Engine) Start(intervalSeconds float64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled {
		return ErrLiveDisabled
	}
	if intervalSeconds > 0 {
		cfg := e.cfg
		cfg.IntervalSeconds = intervalSeconds
		if err := cfg.Validate(); err != nil {
			return err
		}
		e.cfg = cfg
	}
	if e.running {
		e.lastPoll = time.Now()
		return nil
	}
	e.startLocked("Live collection started.", true)
	return nil
}

// startLocked begins a new session. spawnLoop is false only in tests, which drive runTick themselves.
func (e *Engine) startLocked(message string, spawnLoop bool) {
	e.rebuildSourcesLocked()
	e.resetSessionLocked()
	e.generation++
	gen := e.generation
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.wake = make(chan struct{}, 1)
	e.running = true
	e.startedAt = time.Now().UTC()
	e.lastPoll = time.Now()
	e.stateReason = ""
	e.guard = source.SelfTestReadOnlyGuard()
	e.addActivityLocked(e.startedAt, "success", "engine", message,
		fmt.Sprintf("Checking every source every %gs. All external calls are read-only.", e.cfg.IntervalSeconds))
	e.loopCtx = ctx
	if spawnLoop {
		go e.loop(ctx, gen, e.wake)
	}
}

// Stop ends the live session. The last collected state stays readable until the next Start.
func (e *Engine) Stop(reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopLocked(reason)
}

func (e *Engine) stopLocked(reason string) {
	if !e.running {
		return
	}
	e.running = false
	e.generation++
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	if reason == "" {
		reason = "Stopped by operator."
	}
	e.stateReason = reason
	for _, st := range e.src {
		st.inFlight = false
	}
	e.addActivityLocked(time.Now().UTC(), "info", "engine", "Live collection stopped", reason)
}

// Recheck makes every source due immediately and wakes the collector loop.
func (e *Engine) Recheck() {
	e.mu.Lock()
	for _, st := range e.src {
		st.nextDue = time.Time{}
		st.lastAuditAt = time.Time{}
	}
	wake := e.wake
	running := e.running
	e.mu.Unlock()
	if running && wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (e *Engine) loop(ctx context.Context, gen int, wake chan struct{}) {
	for {
		started := time.Now()
		e.runTick(ctx, gen)

		e.mu.Lock()
		if !e.running || gen != e.generation {
			e.mu.Unlock()
			return
		}
		interval := e.effectiveInterval()
		idle := e.cfg.IdleTimeoutSeconds
		if idle > 0 && time.Since(e.lastPoll) > time.Duration(idle*float64(time.Second)) {
			e.stopLocked(fmt.Sprintf("Auto-stopped after %.0f minutes without a viewer. Press Go Live to resume.", idle/60))
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()

		wait := interval - time.Since(started)
		if wait < 200*time.Millisecond {
			wait = 200 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// runTick launches every due source, waits briefly for the fast ones, then integrates whatever has arrived.
// Slow sources keep running in the background and are integrated by a later tick, so one unreachable endpoint
// can never stall local GPU sampling.
func (e *Engine) runTick(ctx context.Context, gen int) {
	tickStart := time.Now()

	e.mu.Lock()
	if !e.running || gen != e.generation {
		e.mu.Unlock()
		return
	}
	var wg sync.WaitGroup
	launched := 0
	k8sDone := make(chan struct{})
	k8sLaunched := false
	knownNodes := e.knownNodeNamesLocked()
	mapping := source.LiveMetricMapping(e.base.MetricMappings)
	maxStale := e.thresholds.MaxStalenessSeconds

	launch := func(id string, fn func() outcome) {
		st := e.src[id]
		if st == nil || !st.status.Enabled || st.inFlight || tickStart.Before(st.nextDue) {
			return
		}
		st.inFlight = true
		now := tickStart.UTC()
		st.status.LastAttemptAt = &now
		launched++
		wg.Add(1)
		go func() {
			defer wg.Done()
			begin := time.Now()
			out := fn()
			out.id = id
			out.at = time.Now().UTC()
			out.latency = time.Since(begin)
			e.deliver(gen, out)
		}()
	}

	if e.hostSrc != nil {
		hs := e.hostSrc
		launch(SourceHost, func() outcome { return outcome{host: hs.Collect()} })
	}
	if e.smiSrc != nil {
		smi := e.smiSrc
		launch(SourceNvidiaSMI, func() outcome {
			res, err := smi.Collect(ctx, mapping)
			return outcome{smi: res, err: err, notFound: errors.Is(err, source.ErrNvidiaSMINotFound)}
		})
	}
	if st := e.src[SourceDCGM]; st != nil && st.status.Enabled {
		if st.buildError != "" {
			e.failBuildLocked(st, tickStart)
		} else if e.dcgmSrc != nil {
			ds := e.dcgmSrc
			launch(SourceDCGM, func() outcome {
				scrape, err := ds.Scrape(ctx)
				return outcome{dcgm: scrape, err: err}
			})
		}
	}
	if st := e.src[SourceKubernetes]; st != nil && st.status.Enabled {
		if st.buildError != "" {
			e.failBuildLocked(st, tickStart)
		} else if e.k8sSrc != nil && !st.inFlight && !tickStart.Before(st.nextDue) {
			ks := e.k8sSrc
			tokenFile := e.cfg.Kubernetes.TokenFile
			k8sLaunched = true
			launch(SourceKubernetes, func() outcome {
				defer close(k8sDone)
				if tokenFile != "" {
					tok, err := readTokenFile(tokenFile)
					if err != nil {
						return outcome{err: err}
					}
					ks.SetBearerToken(tok)
				}
				res, err := ks.CollectLive(ctx)
				return outcome{k8s: res, err: err}
			})
		}
	}
	if !k8sLaunched {
		close(k8sDone)
	}
	if st := e.src[SourcePrometheus]; st != nil && st.status.Enabled {
		if st.buildError != "" {
			e.failBuildLocked(st, tickStart)
		} else if e.promSrc != nil {
			ps := e.promSrc
			tokenFile := e.cfg.Prometheus.TokenFile
			withAudit := st.lastAuditAt.IsZero() || tickStart.Sub(st.lastAuditAt) >= promAuditEvery
			needBackfill := !e.backfill.attempted && e.cfg.PrometheusBackfillMinutes > 0
			backfillMinutes := e.cfg.PrometheusBackfillMinutes
			baseMapping := mapping
			launch(SourcePrometheus, func() outcome {
				// Wait for this tick's Kubernetes inventory so node labels can be matched to real node names.
				select {
				case <-k8sDone:
				case <-time.After(3 * time.Second):
				case <-ctx.Done():
				}
				nodes := knownNodes
				if fresh := e.knownNodeNames(); len(fresh) > 0 {
					nodes = fresh
				}
				if tokenFile != "" {
					tok, err := readTokenFile(tokenFile)
					if err != nil {
						return outcome{err: err}
					}
					ps.SetBearerToken(tok)
				}
				res, err := ps.CollectLive(ctx, baseMapping, maxStale, nodes, withAudit)
				out := outcome{prom: res, err: err, promAudit: withAudit}
				if err == nil && needBackfill && len(res.Normalized.GPUs) > 0 {
					bf, bfErr := ps.Backfill(ctx, baseMapping, backfillMinutes, 60)
					if bfErr != nil {
						out.backfillEr = sanitizeError(bfErr)
					} else {
						out.backfill, out.backfillOK = bf, true
					}
				}
				return out
			})
		}
	}
	e.mu.Unlock()

	// Give this tick's collectors a short grace period; anything slower is integrated by a later tick.
	if launched > 0 {
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		grace := e.graceFor(tickStart)
		select {
		case <-done:
		case <-time.After(grace):
		case <-ctx.Done():
			return
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running || gen != e.generation {
		return
	}
	e.integrateLocked(tickStart)
}

func (e *Engine) graceFor(tickStart time.Time) time.Duration {
	e.mu.Lock()
	interval := e.effectiveInterval()
	first := e.tick == 0
	e.mu.Unlock()
	grace := time.Duration(float64(interval) * 0.75)
	if grace > 1500*time.Millisecond {
		grace = 1500 * time.Millisecond
	}
	if first && grace < 2500*time.Millisecond {
		// The first cycle is the "check everything" pass: wait a little longer so the first picture is complete.
		grace = 2500 * time.Millisecond
	}
	return grace
}

func (e *Engine) failBuildLocked(st *sourceState, now time.Time) {
	t := now.UTC()
	st.status.LastAttemptAt = &t
	st.status.State = StateError
	st.status.LastError = st.buildError
	st.status.Detail = st.buildError
	st.status.Hint = "Fix the configured CA bundle path and press Re-check."
}

// deliver hands a finished collection attempt to the engine. Results from a previous session are dropped.
func (e *Engine) deliver(gen int, out outcome) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if gen != e.generation || !e.running {
		return
	}
	e.recordOutcomeLocked(out)
	e.pending = append(e.pending, out)
}

func readTokenFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read bearer token file %q: %v", path, pathErrorReason(err))
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", fmt.Errorf("bearer token file %q is empty", path)
	}
	return tok, nil
}

func pathErrorReason(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// sanitizeError turns an adapter error into a short single-line message that is safe to show in the console.
// Adapter errors never contain response bodies or credentials; this only bounds length and strips newlines.
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

func (e *Engine) knownNodeNames() map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.knownNodeNamesLocked()
}

func (e *Engine) knownNodeNamesLocked() map[string]bool {
	if !e.k8sNodesOK {
		return nil
	}
	out := make(map[string]bool, len(e.k8sNodes))
	for _, n := range e.k8sNodes {
		out[n.Name] = true
	}
	return out
}

// addActivityLocked appends one entry to the session change log.
func (e *Engine) addActivityLocked(t time.Time, severity, kind, title, detail string) {
	e.activitySeq++
	e.activity = append(e.activity, ActivityEntry{Seq: e.activitySeq, T: t, Severity: severity, Kind: kind, Title: title, Detail: detail})
	if len(e.activity) > maxActivityEntries {
		e.activity = append([]ActivityEntry(nil), e.activity[len(e.activity)-maxActivityEntries:]...)
	}
}

// ---------------------------------------------------------------------------------------------
// Runtime configuration
// ---------------------------------------------------------------------------------------------

// SourcePatch changes one network source. Nil fields are left as they are.
type SourcePatch struct {
	Enabled     *bool   `json:"enabled"`
	Endpoint    *string `json:"endpoint"`
	BearerToken *string `json:"bearer_token"`
	ClearToken  bool    `json:"clear_token"`
}

// ConfigPatch is the body of POST /api/v1/live/config. File paths (nvidia-smi binary, token and CA files) are
// deliberately not settable over the API: they can only come from flags or environment at startup.
type ConfigPatch struct {
	ClusterName      *string      `json:"cluster_name"`
	IntervalSeconds  *float64     `json:"interval_seconds"`
	NvidiaSMIEnabled *bool        `json:"nvidia_smi_enabled"`
	HostVitals       *bool        `json:"host_vitals_enabled"`
	DCGM             *SourcePatch `json:"dcgm_exporter"`
	Prometheus       *SourcePatch `json:"prometheus"`
	Kubernetes       *SourcePatch `json:"kubernetes"`
}

func applySourcePatch(cur config.LiveSourceConfig, p *SourcePatch, defaultEndpoint string) config.LiveSourceConfig {
	if p == nil {
		return cur
	}
	if p.Enabled != nil {
		cur.Enabled = *p.Enabled
	}
	if p.Endpoint != nil {
		ep := strings.TrimSpace(*p.Endpoint)
		if ep == "" {
			// Back to probing the conventional local address.
			cur.Endpoint, cur.Explicit = defaultEndpoint, false
		} else if ep != cur.Endpoint || !cur.Explicit {
			cur.Endpoint, cur.Explicit = ep, true
			// A token entered for one endpoint must never be sent to a different one.
			cur.BearerToken = ""
			cur.TokenFile = ""
		}
	}
	if p.ClearToken {
		cur.BearerToken = ""
	}
	if p.BearerToken != nil && strings.TrimSpace(*p.BearerToken) != "" {
		cur.BearerToken = strings.TrimSpace(*p.BearerToken)
	}
	return cur
}

// UpdateConfig applies a configuration patch. A running session is restarted so the new sources are checked
// from scratch.
func (e *Engine) UpdateConfig(p ConfigPatch) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled {
		return ErrLiveDisabled
	}
	next := e.cfg
	if p.ClusterName != nil {
		next.ClusterName = strings.TrimSpace(*p.ClusterName)
	}
	if p.IntervalSeconds != nil {
		next.IntervalSeconds = *p.IntervalSeconds
	}
	if p.NvidiaSMIEnabled != nil {
		next.NvidiaSMIEnabled = *p.NvidiaSMIEnabled
	}
	if p.HostVitals != nil {
		next.HostVitals = *p.HostVitals
	}
	next.DCGM = applySourcePatch(next.DCGM, p.DCGM, config.DefaultDCGMExporterEndpoint)
	next.Prometheus = applySourcePatch(next.Prometheus, p.Prometheus, config.DefaultPrometheusEndpoint)
	next.Kubernetes = applySourcePatch(next.Kubernetes, p.Kubernetes, config.DefaultKubectlProxyEndpoint)
	if len(next.DCGM.BearerToken) > 8192 || len(next.Prometheus.BearerToken) > 8192 || len(next.Kubernetes.BearerToken) > 8192 {
		return errors.New("live: bearer token is too long")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	onlyInterval := next.IntervalSeconds != e.cfg.IntervalSeconds && sameSourcesConfig(next, e.cfg)
	e.cfg = next
	if onlyInterval {
		// Changing the pace does not invalidate the session.
		e.addActivityLocked(time.Now().UTC(), "info", "engine", "Refresh interval changed", fmt.Sprintf("Now collecting every %gs.", next.IntervalSeconds))
		for _, st := range e.src {
			st.nextDue = time.Time{}
		}
		return nil
	}
	if e.running {
		e.stopLocked("Live configuration changed.")
		e.startLocked("Live configuration changed — session restarted.", !e.manualTicks)
	} else {
		e.rebuildSourcesLocked()
		e.resetSessionLocked()
	}
	return nil
}

func sameSourcesConfig(a, b config.LiveConfig) bool {
	return a.ClusterName == b.ClusterName && a.NvidiaSMIEnabled == b.NvidiaSMIEnabled && a.HostVitals == b.HostVitals &&
		a.DCGM == b.DCGM && a.Prometheus == b.Prometheus && a.Kubernetes == b.Kubernetes
}

// SetThresholds replaces the live rule-threshold overrides. The fixture demo always uses the configured defaults.
func (e *Engine) SetThresholds(overrides map[string]float64, reset bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled {
		return ErrLiveDisabled
	}
	merged := map[string]float64{}
	if !reset {
		for k, v := range e.overrides {
			merged[k] = v
		}
	}
	defaults := config.ThresholdsToMap(e.base.Thresholds)
	for k, v := range overrides {
		merged[k] = v
	}
	// An override equal to the default is not an override.
	for k, v := range merged {
		if def, ok := defaults[k]; ok && def == v {
			delete(merged, k)
		}
	}
	th, err := config.ApplyThresholdOverrides(e.base.Thresholds, merged)
	if err != nil {
		return err
	}
	e.thresholds = th
	e.overrides = merged
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, fmt.Sprintf("%s=%g", k, merged[k]))
	}
	sort.Strings(keys)
	detail := "All live rules use the configured defaults again."
	if len(keys) > 0 {
		detail = "Live overrides: " + strings.Join(keys, ", ") + ". The simulated demo is unaffected."
	}
	e.addActivityLocked(time.Now().UTC(), "info", "threshold", "Live rule thresholds updated", detail)
	return nil
}

// Enabled reports whether the live console is available.
func (e *Engine) Enabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Enabled
}

// AllowRemoteConfig reports whether connection settings may be changed by non-loopback clients.
func (e *Engine) AllowRemoteConfig() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.AllowRemoteConfig
}
