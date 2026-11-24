// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

// Package live implements the live telemetry console ("Go Live"): it collects from the real sources available to
// this GPU Lens instance, merges them into one cluster snapshot, runs the same deterministic rules as the fixture
// demo, and keeps a rolling session history. Every external call is read-only.
package live

import (
	"time"

	"github.com/Sievehouse/gpu-lens/internal/alert"
	"github.com/Sievehouse/gpu-lens/internal/config"
	"github.com/Sievehouse/gpu-lens/internal/domain"
	"github.com/Sievehouse/gpu-lens/internal/source"
)

// Version is the GPU Lens release reported by the live API, /metrics and -version. Release builds set it from the
// git tag: go build -ldflags "-X github.com/Sievehouse/gpu-lens/internal/live.Version=v0.1.0".
var Version = "0.1.0"

// Source identifiers used across the live API.
const (
	SourceHost       = "host"
	SourceNvidiaSMI  = "nvidia_smi"
	SourceAMDSMI     = "amd_smi"
	SourceDCGM       = "dcgm_exporter"
	SourcePrometheus = "prometheus"
	SourceKubernetes = "kubernetes"
)

// Source connection states.
const (
	StatePending     = "pending"
	StateConnected   = "connected"
	StateDegraded    = "degraded"
	StateUnreachable = "unreachable"
	StateNotFound    = "not_found"
	StateDisabled    = "disabled"
	StateError       = "error"
)

// Preflight check outcomes.
const (
	CheckPass    = "pass"
	CheckWarn    = "warn"
	CheckFail    = "fail"
	CheckSkip    = "skip"
	CheckInfo    = "info"
	CheckPending = "pending"
)

// Status describes the live engine itself.
type Status struct {
	Runtime             string     `json:"runtime"`
	Version             string     `json:"version"`
	Supported           bool       `json:"supported"`
	UnsupportedReason   string     `json:"unsupported_reason"`
	State               string     `json:"state"`
	StateReason         string     `json:"state_reason"`
	StartedAt           *time.Time `json:"started_at"`
	Tick                int        `json:"tick"`
	IntervalSeconds     float64    `json:"interval_seconds"`
	LastTickAt          *time.Time `json:"last_tick_at"`
	LastTickDurationMs  float64    `json:"last_tick_duration_ms"`
	ServerTime          time.Time  `json:"server_time"`
	ConfigMutable       bool       `json:"config_mutable"`
	ConfigMutableReason string     `json:"config_mutable_reason"`
	AutoStart           bool       `json:"auto_start"`
	Hostname            string     `json:"hostname"`
	Emulated            bool       `json:"emulated"`
	Role                string     `json:"role"`
	AuthEnabled         bool       `json:"auth_enabled"`
	User                string     `json:"user"`
	CanOperate          bool       `json:"can_operate"`
}

// Fact is a labelled detail shown on a source card.
type Fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// SourceStatus is the connection state of one live source.
type SourceStatus struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Kind                string     `json:"kind"`
	Enabled             bool       `json:"enabled"`
	Explicit            bool       `json:"explicit"`
	State               string     `json:"state"`
	Endpoint            string     `json:"endpoint"`
	Detail              string     `json:"detail"`
	Hint                string     `json:"hint"`
	LastAttemptAt       *time.Time `json:"last_attempt_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastError           string     `json:"last_error"`
	LastLatencyMs       *float64   `json:"last_latency_ms"`
	LatencyHistoryMs    []float64  `json:"latency_history_ms"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	SuccessCount        int        `json:"success_count"`
	FailureCount        int        `json:"failure_count"`
	RefreshSeconds      float64    `json:"refresh_seconds"`
	Emulated            bool       `json:"emulated"`
	ReadOnly            string     `json:"read_only"`
	Facts               []Fact     `json:"facts"`
}

// Check is one line of the live preflight checklist.
type Check struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Detail    string   `json:"detail"`
	Hint      string   `json:"hint"`
	LatencyMs *float64 `json:"latency_ms"`
}

// CheckGroup groups related checks.
type CheckGroup struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Checks []Check `json:"checks"`
}

// PreflightReport is the "check everything" result, recomputed from current collector state on every read.
type PreflightReport struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Verdict     string         `json:"verdict"`
	VerdictText string         `json:"verdict_text"`
	Summary     map[string]int `json:"summary"`
	Groups      []CheckGroup   `json:"groups"`
}

// GPUView is one GPU as shown by the live console.
type GPUView struct {
	UUID               string                `json:"uuid"`
	DeviceIndex        int                   `json:"device_index"`
	NodeName           string                `json:"node_name"`
	ModelName          string                `json:"model_name"`
	PCIBusID           string                `json:"pci_bus_id"`
	DriverVersion      string                `json:"driver_version"`
	PowerLimitWatts    float64               `json:"power_limit_watts"`
	TotalMemoryMiB     float64               `json:"total_memory_mib"`
	AllocatedNamespace string                `json:"allocated_namespace"`
	AllocatedPod       string                `json:"allocated_pod"`
	AllocatedContainer string                `json:"allocated_container"`
	MappingStatus      string                `json:"mapping_status"`
	LastSampleAt       time.Time             `json:"last_sample_at"`
	StalenessSeconds   float64               `json:"staleness_seconds"`
	IsStale            bool                  `json:"is_stale"`
	Latest             domain.GPUMetricPoint `json:"latest"`
	FBUsedPercent      *float64              `json:"fb_used_percent"`
	Sources            []string              `json:"sources"`
	FanSpeedPercent    *float64              `json:"fan_speed_percent"`
	PState             string                `json:"pstate"`
	ThrottleReasons    []string              `json:"throttle_reasons"`
	PCIe               *source.PCIeLink      `json:"pcie"`
	Processes          []source.GPUProcess   `json:"processes"`
	Emulated           bool                  `json:"emulated"`
	MIGProfile         string                `json:"mig_profile"`
	SampleCount        int                   `json:"sample_count"`
	FirstSeenAt        time.Time             `json:"first_seen_at"`
	IncidentIDs        []string              `json:"incident_ids"`
	WorstSeverity      string                `json:"worst_severity"`
	// ExportedMetrics lists the canonical metrics at least one of this GPU's sources publishes, so the console can
	// show "not exported" instead of a misleading zero.
	ExportedMetrics []string `json:"exported_metrics"`
}

// ProcessView is one GPU compute process row.
type ProcessView struct {
	GPUUUID       string   `json:"gpu_uuid"`
	GPUIndex      int      `json:"gpu_index"`
	NodeName      string   `json:"node_name"`
	PID           int      `json:"pid"`
	Name          string   `json:"name"`
	UsedMemoryMiB *float64 `json:"used_memory_mib"`
}

// HistoryPoint is one fleet-level sample of the live session. G holds per-GPU [util %, VRAM %, temp °C, power W].
type HistoryPoint struct {
	Seq        int                  `json:"seq"`
	T          time.Time            `json:"t"`
	GPUs       int                  `json:"gpus"`
	FreshGPUs  int                  `json:"fresh_gpus"`
	Util       *float64             `json:"util"`
	FB         *float64             `json:"fb"`
	TempMax    *float64             `json:"temp_max"`
	Power      *float64             `json:"power"`
	Critical   int                  `json:"crit"`
	Warning    int                  `json:"warn"`
	HostCPU    *float64             `json:"cpu"`
	HostMem    *float64             `json:"mem"`
	Backfilled bool                 `json:"backfilled,omitempty"`
	G          map[string][]float64 `json:"g"`
}

// HistoryView is the slice of history returned for one poll.
type HistoryView struct {
	// Epoch changes whenever the series was rebuilt (new session, backfill); clients then reload it from 0.
	Epoch    int            `json:"epoch"`
	LastSeq  int            `json:"last_seq"`
	FirstSeq int            `json:"first_seq"`
	Capacity int            `json:"capacity"`
	Points   []HistoryPoint `json:"points"`
}

// ActivityEntry is one line of the live change log.
type ActivityEntry struct {
	Seq      int       `json:"seq"`
	T        time.Time `json:"t"`
	Severity string    `json:"severity"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail"`
}

// ResolvedIncident is an incident that stopped firing during this session.
type ResolvedIncident struct {
	Incident        domain.Incident `json:"incident"`
	ResolvedAt      time.Time       `json:"resolved_at"`
	DurationSeconds float64         `json:"duration_seconds"`
}

// SourcesDetail mirrors the fixture /api/v1/sources payload so the console can reuse the Data Sources view.
type SourcesDetail struct {
	DataMode        string                     `json:"data_mode"`
	ScenarioID      string                     `json:"scenario_id"`
	CollectedAt     time.Time                  `json:"collected_at"`
	Sources         []domain.SourceHealth      `json:"sources"`
	ExportedMetrics map[string]bool            `json:"exported_metrics"`
	MetricMappings  config.MetricMappingConfig `json:"metric_mappings"`
}

// State is the full payload of GET /api/v1/live/state.
type State struct {
	Status             Status                   `json:"status"`
	Config             config.LiveConfigView    `json:"config"`
	Overview           *domain.OverviewResponse `json:"overview"`
	Preflight          PreflightReport          `json:"preflight"`
	Sources            []SourceStatus           `json:"sources"`
	Host               *source.HostVitals       `json:"host"`
	GPUs               []GPUView                `json:"gpus"`
	Processes          []ProcessView            `json:"processes"`
	Nodes              []domain.NodeState       `json:"nodes"`
	Pods               []domain.WorkloadPod     `json:"pods"`
	Events             []domain.K8sEvent        `json:"events"`
	History            HistoryView              `json:"history"`
	Activity           []ActivityEntry          `json:"activity"`
	ActivityLastSeq    int                      `json:"activity_last_seq"`
	ResolvedIncidents  []ResolvedIncident       `json:"resolved_incidents"`
	Thresholds         map[string]float64       `json:"thresholds"`
	ThresholdDefaults  map[string]float64       `json:"threshold_defaults"`
	ThresholdOverrides []string                 `json:"threshold_overrides"`
	Rules              []domain.RuleDefinition  `json:"rules"`
	SourcesDetail      *SourcesDetail           `json:"sources_detail"`
	Notes              []string                 `json:"notes"`
	Alerts             alert.Status             `json:"alerts"`
	Persistence        PersistenceStatus        `json:"persistence"`
}
