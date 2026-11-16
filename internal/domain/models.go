package domain

import "time"

// Severity represents explainable incident severity levels.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// EvalStatus represents the deterministic outcome of evaluating a diagnostic rule.
type EvalStatus string

const (
	EvalStatusTriggered    EvalStatus = "triggered"
	EvalStatusInconclusive EvalStatus = "inconclusive"
	EvalStatusSuppressed   EvalStatus = "suppressed"
	EvalStatusHealthy      EvalStatus = "healthy"
)

// EvidenceStrength explicitly describes how strongly the available signals support a finding.
// Unlike arbitrary percentage confidence scores, EvidenceStrength follows a strict rubric:
// - Strong: Multi-layer corroboration (e.g., pod failure + GPU hardware error metric + node/kubelet event) with fresh data.
// - Moderate: Single-layer authoritative signal (e.g., FailedScheduling with node inventory, or node-level hardware fault) with fresh data.
// - Contextual: Threshold crossed with complete, fresh data, but workload intent (I/O, CPU stall, VRAM residency) requires operator verification.
// - Inconclusive: Missing, stale, or unmapped telemetry prevents complete verification.
type EvidenceStrength string

const (
	EvidenceStrong       EvidenceStrength = "strong"
	EvidenceModerate     EvidenceStrength = "moderate"
	EvidenceContextual   EvidenceStrength = "contextual"
	EvidenceInconclusive EvidenceStrength = "inconclusive"
)

// MappingStatus tracks whether a GPU device's telemetry carries Kubernetes workload attribution labels.
type MappingStatus string

const (
	MappingMapped           MappingStatus = "mapped"
	MappingUnallocated      MappingStatus = "unallocated"
	MappingMissingPodLabels MappingStatus = "missing_pod_labels"
)

// ClusterSnapshot is the normalized point-in-time representation of cluster state and GPU telemetry.
// The rules engine and capacity calculator consume only ClusterSnapshot, decoupling them from data sources.
type ClusterSnapshot struct {
	ScenarioID          string            `json:"scenario_id"`
	ScenarioName        string            `json:"scenario_name"`
	ScenarioDescription string            `json:"scenario_description"`
	ClusterName         string            `json:"cluster_name"`
	DataMode            string            `json:"data_mode"` // "simulated_fixture" or "live_telemetry"
	CollectedAt         time.Time         `json:"collected_at"`
	Sources             []SourceHealth    `json:"sources"`
	Nodes               []NodeState       `json:"nodes"`
	GPUs                []GPUDevice       `json:"gpus"`
	Pods                []WorkloadPod     `json:"pods"`
	Events              []K8sEvent        `json:"events"`
	ExportedMetrics     map[string]bool   `json:"exported_metrics"`
	MetricMappings      map[string]string `json:"metric_mappings"`
}

// SourceHealth represents the collection status and freshness of an infrastructure data source.
type SourceHealth struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Kind                  string    `json:"kind"` // "fixture", "kubernetes", "prometheus", "dcgm_exporter"
	Mode                  string    `json:"mode"` // "simulated" or "live"
	Endpoint              string    `json:"endpoint"`
	Available             bool      `json:"available"`
	ReadOnlyVerified      bool      `json:"read_only_verified"`
	LastSuccessAt         time.Time `json:"last_success_at"`
	LastError             string    `json:"last_error,omitempty"`
	StalenessSeconds      float64   `json:"staleness_seconds"`
	IsStale               bool      `json:"is_stale"`
	ExportedRequiredCount int       `json:"exported_required_count"`
	ExportedOptionalCount int       `json:"exported_optional_count"`
	MissingOptional       []string  `json:"missing_optional,omitempty"`
	MissingRequired       []string  `json:"missing_required,omitempty"`
	Notes                 string    `json:"notes,omitempty"`
}

// NodeCondition represents a Kubernetes Node status condition (including Node Problem Detector GPU conditions).
type NodeCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"` // "True", "False", "Unknown"
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

// NodeState represents normalized Kubernetes node state and GPU capacity vs. allocatable inventory.
type NodeState struct {
	Name           string            `json:"name"`
	Ready          bool              `json:"ready"`
	Schedulable    bool              `json:"schedulable"`
	GPUModel       string            `json:"gpu_model"`
	GPUCapacity    int               `json:"gpu_capacity"`
	GPUAllocatable int               `json:"gpu_allocatable"`
	GPUAllocated   int               `json:"gpu_allocated"`
	Labels         map[string]string `json:"labels,omitempty"`
	Taints         []string          `json:"taints,omitempty"`
	Conditions     []NodeCondition   `json:"conditions,omitempty"`
}

// GPUMetricPoint is a single time-series observation for a GPU device.
type GPUMetricPoint struct {
	Timestamp                 time.Time `json:"timestamp"`
	GPUUtilPercent            float64   `json:"gpu_util_percent"`                 // DCGM_FI_DEV_GPU_UTIL (%)
	MemCopyUtilPercent        float64   `json:"mem_copy_util_percent"`            // DCGM_FI_DEV_MEM_COPY_UTIL (%)
	FBUsedMiB                 float64   `json:"fb_used_mib"`                      // DCGM_FI_DEV_FB_USED (MiB)
	FBFreeMiB                 float64   `json:"fb_free_mib"`                      // DCGM_FI_DEV_FB_FREE (MiB)
	GPUTempCelsius            float64   `json:"gpu_temp_celsius"`                 // DCGM_FI_DEV_GPU_TEMP (C)
	MemoryTempCelsius         float64   `json:"memory_temp_celsius"`              // DCGM_FI_DEV_MEMORY_TEMP (C)
	PowerUsageWatts           float64   `json:"power_usage_watts"`                // DCGM_FI_DEV_POWER_USAGE (W)
	XIDErrorCode              int       `json:"xid_error_code"`                   // DCGM_FI_DEV_XID_ERRORS (last XID code)
	PCIeReplayCounter         int       `json:"pcie_replay_counter"`              // DCGM_FI_DEV_PCIE_REPLAY_COUNTER
	RowRemapFailure           int       `json:"row_remap_failure"`                // DCGM_FI_DEV_ROW_REMAP_FAILURE (0 or 1)
	UncorrectableRemappedRows int       `json:"uncorrectable_remapped_rows"`      // DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS
	ECCDBEVolTotal            *int      `json:"ecc_dbe_vol_total,omitempty"`      // Optional: DCGM_FI_DEV_ECC_DBE_VOL_TOTAL
	ECCSBEVolTotal            *int      `json:"ecc_sbe_vol_total,omitempty"`      // Optional: DCGM_FI_DEV_ECC_SBE_VOL_TOTAL
	NVLinkCRCFlitErrors       *int      `json:"nvlink_crc_flit_errors,omitempty"` // Optional: DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL
	NVLinkReplayErrors        *int      `json:"nvlink_replay_errors,omitempty"`   // Optional: DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL
	ThermalViolationNs        *int64    `json:"thermal_violation_ns,omitempty"`   // Optional: DCGM_FI_DEV_THERMAL_VIOLATION
	PowerViolationNs          *int64    `json:"power_violation_ns,omitempty"`     // Optional: DCGM_FI_DEV_POWER_VIOLATION
	HealthStatus              *int      `json:"health_status,omitempty"`          // Optional: DCGM_EXP_GPU_HEALTH_STATUS (0=PASS, 10=WARN, 20=FAIL)
	ClockThrottleReason       string    `json:"clock_throttle_reason,omitempty"`  // Optional: DCGM_EXP_CLOCK_EVENTS_COUNT clock_event label
}

// GPUDevice represents a physical GPU or MIG instance reported by telemetry.
type GPUDevice struct {
	UUID               string           `json:"uuid"`
	DeviceIndex        int              `json:"device_index"`
	NodeName           string           `json:"node_name"`
	ModelName          string           `json:"model_name"`
	PCIBusID           string           `json:"pci_bus_id"`
	DriverVersion      string           `json:"driver_version"`
	PowerLimitWatts    float64          `json:"power_limit_watts"`
	TotalMemoryMiB     float64          `json:"total_memory_mib"`
	AllocatedNamespace string           `json:"allocated_namespace,omitempty"`
	AllocatedPod       string           `json:"allocated_pod,omitempty"`
	AllocatedContainer string           `json:"allocated_container,omitempty"`
	MappingStatus      MappingStatus    `json:"mapping_status"`
	LastSampleAt       time.Time        `json:"last_sample_at"`
	StalenessSeconds   float64          `json:"staleness_seconds"`
	IsStale            bool             `json:"is_stale"`
	Latest             GPUMetricPoint   `json:"latest"`
	Samples            []GPUMetricPoint `json:"samples"`
}

// ContainerTermination captures the last termination state of a workload container.
type ContainerTermination struct {
	ExitCode   int       `json:"exit_code"`
	Reason     string    `json:"reason"`
	Message    string    `json:"message"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// WorkloadPod represents a normalized Kubernetes Pod requesting or using GPUs.
// Sensitive fields (env vars, command args, secrets) are stripped during normalization.
type WorkloadPod struct {
	Name             string                `json:"name"`
	Namespace        string                `json:"namespace"`
	UID              string                `json:"uid"`
	OwnerKind        string                `json:"owner_kind"`
	OwnerName        string                `json:"owner_name"`
	Phase            string                `json:"phase"` // "Pending", "Running", "Failed", "Succeeded"
	NodeName         string                `json:"node_name,omitempty"`
	RequestedGPUs    int                   `json:"requested_gpus"`
	AssignedGPUUUIDs []string              `json:"assigned_gpu_uuids,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	StartedAt        *time.Time            `json:"started_at,omitempty"`
	RestartCount     int                   `json:"restart_count"`
	LastTermination  *ContainerTermination `json:"last_termination,omitempty"`
	NodeSelector     map[string]string     `json:"node_selector,omitempty"`
	Tolerations      []string              `json:"tolerations,omitempty"`
	SchedulerReason  string                `json:"scheduler_reason,omitempty"`
	SchedulerMessage string                `json:"scheduler_message,omitempty"`
	// StatusDetail is a short kubectl-style status hint (for example "ContainerCreating", "CrashLoopBackOff")
	// or, for workloads inferred from telemetry, a note about where the attribution came from.
	StatusDetail string `json:"status_detail,omitempty"`
}

// K8sEvent represents a normalized Kubernetes Event associated with a Pod or Node.
type K8sEvent struct {
	ID                      string    `json:"id"`
	Type                    string    `json:"type"` // "Normal", "Warning"
	Reason                  string    `json:"reason"`
	Message                 string    `json:"message"`
	SourceComponent         string    `json:"source_component"`
	InvolvedObjectKind      string    `json:"involved_object_kind"` // "Pod", "Node"
	InvolvedObjectNamespace string    `json:"involved_object_namespace,omitempty"`
	InvolvedObjectName      string    `json:"involved_object_name"`
	NodeName                string    `json:"node_name,omitempty"`
	Count                   int       `json:"count"`
	FirstTimestamp          time.Time `json:"first_timestamp"`
	LastTimestamp           time.Time `json:"last_timestamp"`
}

// EvidenceItem represents a single verifiable piece of supporting or contradicting evidence.
type EvidenceItem struct {
	Kind       string    `json:"kind"`     // "metric", "k8s_event", "pod_state", "node_state", "source_state"
	Polarity   string    `json:"polarity"` // "supporting", "contradicting", "contextual"
	Source     string    `json:"source"`   // e.g. "dcgm_exporter", "kube-scheduler", "kubelet"
	MetricName string    `json:"metric_name,omitempty"`
	ObjectRef  string    `json:"object_ref"`
	Summary    string    `json:"summary"`
	Value      string    `json:"value"`
	Threshold  string    `json:"threshold,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// TimelineEntry orders correlated events, metric transitions, and pod state changes chronologically.
type TimelineEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Layer     string    `json:"layer"`    // "gpu_telemetry", "k8s_node", "k8s_scheduler", "k8s_pod"
	Severity  string    `json:"severity"` // "info", "warning", "critical"
	ObjectRef string    `json:"object_ref"`
	EventType string    `json:"event_type"`
	Summary   string    `json:"summary"`
}

// Incident is a deterministic, evidence-backed diagnostic finding produced by a rule.
type Incident struct {
	ID                    string           `json:"id"`
	Fingerprint           string           `json:"fingerprint"`
	RuleID                string           `json:"rule_id"`
	RuleName              string           `json:"rule_name"`
	Category              string           `json:"category"` // "scheduling", "hardware_health", "utilization", "failure_correlation", "data_freshness", "thermal_power_memory"
	Severity              Severity         `json:"severity"`
	EvalStatus            EvalStatus       `json:"eval_status"`
	EvidenceStrength      EvidenceStrength `json:"evidence_strength"`
	EvidenceStrengthBasis string           `json:"evidence_strength_basis"`
	Title                 string           `json:"title"`
	Summary               string           `json:"summary"`
	ObservedFacts         []string         `json:"observed_facts"`
	PossibleCauses        []string         `json:"possible_causes"`
	SupportingEvidence    []EvidenceItem   `json:"supporting_evidence"`
	ContradictingEvidence []EvidenceItem   `json:"contradicting_evidence"`
	Timeline              []TimelineEntry  `json:"timeline"`
	AffectedResources     []string         `json:"affected_resources"`
	SuggestedChecks       []string         `json:"suggested_checks"`
	InconclusiveReason    string           `json:"inconclusive_reason,omitempty"`
	DataMode              string           `json:"data_mode"`
	ScenarioID            string           `json:"scenario_id"`
	FirstSeenAt           time.Time        `json:"first_seen_at"`
	LastSeenAt            time.Time        `json:"last_seen_at"`
	OccurrenceCount       int              `json:"occurrence_count"`
}

// RuleThresholdView describes a configurable parameter exposed by a rule.
type RuleThresholdView struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
	Description string  `json:"description"`
}

// RuleDefinition documents a rule in the rule registry for operator inspection.
type RuleDefinition struct {
	ID                     string              `json:"id"`
	Name                   string              `json:"name"`
	Category               string              `json:"category"`
	DefaultSeverity        Severity            `json:"default_severity"`
	Description            string              `json:"description"`
	RequiredSignals        []string            `json:"required_signals"`
	OptionalSignals        []string            `json:"optional_signals"`
	Thresholds             []RuleThresholdView `json:"thresholds"`
	SuppressionConditions  []string            `json:"suppression_conditions"`
	InconclusiveConditions []string            `json:"inconclusive_conditions"`
	SuggestedChecks        []string            `json:"suggested_checks"`
}

// NodeCapacityBreakdown summarizes GPU capacity and utilization per node.
type NodeCapacityBreakdown struct {
	NodeName        string   `json:"node_name"`
	GPUModel        string   `json:"gpu_model"`
	Schedulable     bool     `json:"schedulable"`
	Ready           bool     `json:"ready"`
	CapacityGPUs    int      `json:"capacity_gpus"`
	AllocatableGPUs int      `json:"allocatable_gpus"`
	UnavailableGPUs int      `json:"unavailable_gpus"`
	AllocatedGPUs   int      `json:"allocated_gpus"`
	FreeAllocatable int      `json:"free_allocatable_gpus"`
	MeanGPUUtil     *float64 `json:"mean_gpu_util_percent,omitempty"`
	MeanFBUsedPct   *float64 `json:"mean_fb_used_percent,omitempty"`
	FreshGPUs       int      `json:"fresh_gpus"`
	StaleGPUs       int      `json:"stale_gpus"`
	UnmappedPodGPUs int      `json:"unmapped_pod_gpus"`
	StatusNote      string   `json:"status_note"`
}

// PendingWorkloadSummary summarizes a pending GPU pod and its observable scheduling reason.
type PendingWorkloadSummary struct {
	Namespace        string    `json:"namespace"`
	PodName          string    `json:"pod_name"`
	OwnerRef         string    `json:"owner_ref"`
	RequestedGPUs    int       `json:"requested_gpus"`
	PendingMinutes   float64   `json:"pending_minutes"`
	ObservedReason   string    `json:"observed_reason"`
	SchedulerMessage string    `json:"scheduler_message"`
	EvidenceComplete bool      `json:"evidence_complete"`
	CreatedAt        time.Time `json:"created_at"`
}

// LowUtilPodWindow documents a running workload whose allocated GPUs show persistently low compute utilization.
type LowUtilPodWindow struct {
	Namespace              string    `json:"namespace"`
	PodName                string    `json:"pod_name"`
	OwnerRef               string    `json:"owner_ref"`
	NodeName               string    `json:"node_name"`
	AllocatedGPUs          int       `json:"allocated_gpus"`
	AssignedGPUUUIDs       []string  `json:"assigned_gpu_uuids"`
	ObservationWindowMins  float64   `json:"observation_window_minutes"`
	SampleCount            int       `json:"sample_count"`
	MeanGPUUtilPercent     float64   `json:"mean_gpu_util_percent"`
	MinGPUUtilPercent      float64   `json:"min_gpu_util_percent"`
	MaxGPUUtilPercent      float64   `json:"max_gpu_util_percent"`
	MeanMemCopyUtilPercent float64   `json:"mean_mem_copy_util_percent"`
	MeanFBUsedMiB          float64   `json:"mean_fb_used_mib"`
	MeanFBUsedPercent      float64   `json:"mean_fb_used_percent"`
	TotalAllocatedGPUHours float64   `json:"total_allocated_gpu_hours"`
	LowUtilGPUHours        float64   `json:"low_util_gpu_hours"`
	OptionalSampleCostEst  *float64  `json:"optional_sample_cost_estimate_usd,omitempty"`
	LastDataUpdate         time.Time `json:"last_data_update"`
	WorkloadContextNote    string    `json:"workload_context_note"`
	FollowUpChecklist      []string  `json:"follow_up_checklist"`
}

// UtilizationHistoryPoint represents a cluster-wide historical utilization sample across fresh GPUs.
type UtilizationHistoryPoint struct {
	Timestamp             time.Time `json:"timestamp"`
	MeanGPUUtilPercent    float64   `json:"mean_gpu_util_percent"`
	MeanMemCopyPercent    float64   `json:"mean_mem_copy_util_percent"`
	MeanFBUsedPercent     float64   `json:"mean_fb_used_percent"`
	AllocatedGPUsObserved int       `json:"allocated_gpus_observed"`
}

// CapacityMethodology documents the exact formulas and caveats used in the capacity report.
type CapacityMethodology struct {
	UtilThresholdPercent        float64  `json:"util_threshold_percent"`
	MinWindowMinutes            float64  `json:"min_window_minutes"`
	MinSamplesRequired          int      `json:"min_samples_required"`
	FormulaSummary              string   `json:"formula_summary"`
	StepByStepCalculation       []string `json:"step_by_step_calculation"`
	NonWasteDisclaimer          string   `json:"non_waste_disclaimer"`
	MissingMappingPolicy        string   `json:"missing_mapping_policy"`
	OptionalSampleHourlyRateUSD *float64 `json:"optional_sample_hourly_rate_usd,omitempty"`
	OptionalCostEstimateLabel   string   `json:"optional_cost_estimate_label,omitempty"`
}

// CapacityReport is the complete GPU capacity and utilization intelligence response.
type CapacityReport struct {
	ClusterName                  string                    `json:"cluster_name"`
	ScenarioID                   string                    `json:"scenario_id"`
	DataMode                     string                    `json:"data_mode"`
	GeneratedAt                  time.Time                 `json:"generated_at"`
	TotalCapacityGPUs            int                       `json:"total_capacity_gpus"`
	TotalAllocatableGPUs         int                       `json:"total_allocatable_gpus"`
	UnavailableHardwareGPUs      int                       `json:"unavailable_hardware_gpus"`
	TotalAllocatedGPUs           int                       `json:"total_allocated_gpus"`
	TotalFreeAllocatableGPUs     int                       `json:"total_free_allocatable_gpus"`
	PendingRequestedGPUs         int                       `json:"pending_requested_gpus"`
	ClusterMeanGPUUtilPercent    *float64                  `json:"cluster_mean_gpu_util_percent,omitempty"`
	ClusterMeanFBUsedPercent     *float64                  `json:"cluster_mean_fb_used_percent,omitempty"`
	TotalObservedAllocatedGPUHrs float64                   `json:"total_observed_allocated_gpu_hours"`
	ObservedLowUtilGPUHours      float64                   `json:"observed_low_util_gpu_hours"`
	OptionalLowUtilSampleCostUSD *float64                  `json:"optional_low_util_sample_cost_usd,omitempty"`
	TelemetryFreshGPUs           int                       `json:"telemetry_fresh_gpus"`
	TelemetryStaleGPUs           int                       `json:"telemetry_stale_gpus"`
	MappedAllocatedGPUs          int                       `json:"mapped_allocated_gpus"`
	UnmappedAllocatedGPUs        int                       `json:"unmapped_allocated_gpus"`
	PerJobAttributionComplete    bool                      `json:"per_job_attribution_complete"`
	AttributionBlindSpotWarning  string                    `json:"attribution_blind_spot_warning,omitempty"`
	Nodes                        []NodeCapacityBreakdown   `json:"nodes"`
	PendingWorkloads             []PendingWorkloadSummary  `json:"pending_workloads"`
	LowUtilizationWorkloads      []LowUtilPodWindow        `json:"low_utilization_workloads"`
	HistoricalUtilization        []UtilizationHistoryPoint `json:"historical_utilization"`
	Methodology                  CapacityMethodology       `json:"methodology"`
}

// OverviewResponse aggregates key fleet reliability and capacity indicators for the Overview page.
type OverviewResponse struct {
	ClusterName          string         `json:"cluster_name"`
	ScenarioID           string         `json:"scenario_id"`
	ScenarioName         string         `json:"scenario_name"`
	ScenarioDescription  string         `json:"scenario_description"`
	DataMode             string         `json:"data_mode"`
	CollectedAt          time.Time      `json:"collected_at"`
	FleetHealthStatus    string         `json:"fleet_health_status"` // "healthy", "warning", "critical", "telemetry_blind_spot"
	FleetHealthHeadline  string         `json:"fleet_health_headline"`
	ActiveIncidentCounts map[string]int `json:"active_incident_counts"`
	CapacitySummary      CapacityReport `json:"capacity_summary"`
	ActiveIncidents      []Incident     `json:"active_incidents"`
	Sources              []SourceHealth `json:"sources"`
}
