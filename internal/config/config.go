package config

import (
	"errors"
	"fmt"
	"time"
)

// MetricMappingConfig maps canonical GPU Lens signal names to DCGM Exporter / Prometheus metric names and label keys.
// This allows operators to adapt to custom dcgm-exporter CSVs or --use-old-namespace flags without code changes.
type MetricMappingConfig struct {
	// Default DCGM Exporter metrics (enabled in shipped default-counters.csv / dcp-metrics-included.csv)
	GPUUtilMetric            string `json:"gpu_util_metric"`
	MemCopyUtilMetric        string `json:"mem_copy_util_metric"`
	FBUsedMetric             string `json:"fb_used_metric"`
	FBFreeMetric             string `json:"fb_free_metric"`
	GPUTempMetric            string `json:"gpu_temp_metric"`
	MemoryTempMetric         string `json:"memory_temp_metric"`
	PowerUsageMetric         string `json:"power_usage_metric"`
	XIDErrorsMetric          string `json:"xid_errors_metric"`
	PCIeReplayMetric         string `json:"pcie_replay_metric"`
	RowRemapFailureMetric    string `json:"row_remap_failure_metric"`
	UncorrectableRemapMetric string `json:"uncorrectable_remap_metric"`

	// Optional DCGM Exporter metrics (commented by default in dcgm-exporter CSV; evaluated only when present)
	ECCDBEVolTotalMetric       string `json:"ecc_dbe_vol_total_metric"`
	ECCSBEVolTotalMetric       string `json:"ecc_sbe_vol_total_metric"`
	NVLinkCRCFlitErrorMetric   string `json:"nvlink_crc_flit_error_metric"`
	NVLinkReplayErrorMetric    string `json:"nvlink_replay_error_metric"`
	ThermalViolationMetric     string `json:"thermal_violation_metric"`
	PowerViolationMetric       string `json:"power_violation_metric"`
	ExporterHealthStatusMetric string `json:"exporter_health_status_metric"`
	ClockEventsCountMetric     string `json:"clock_events_count_metric"`

	// Configurable label keys (supporting both default and --use-old-namespace conventions)
	UUIDLabelCandidates      []string `json:"uuid_label_candidates"`
	GPUIndexLabel            string   `json:"gpu_index_label"`
	NodeNameLabelCandidates  []string `json:"node_name_label_candidates"`
	ModelNameLabelCandidates []string `json:"model_name_label_candidates"`
	PodNameLabelCandidates   []string `json:"pod_name_label_candidates"`
	NamespaceLabelCandidates []string `json:"namespace_label_candidates"`
	ContainerLabelCandidates []string `json:"container_label_candidates"`
}

// RuleThresholds holds inspectable, testable thresholds and observation windows for all 6 diagnostic rules.
type RuleThresholds struct {
	// Rule 1: GPU-SCHED-001 (Pending GPU Workload Scheduling Bottleneck)
	PendingGraceSeconds    int     `json:"pending_grace_seconds"`
	CriticalPendingMinutes float64 `json:"critical_pending_minutes"`

	// Rule 2: GPU-HEALTH-002 (GPU Hardware Fault or Health Degradation)
	XIDErrorThreshold        int `json:"xid_error_threshold"`
	ECCDBEThreshold          int `json:"ecc_dbe_threshold"`
	RowRemapFailureThreshold int `json:"row_remap_failure_threshold"`
	PCIeReplayWarningCount   int `json:"pcie_replay_warning_count"`
	NVLinkCRCErrorThreshold  int `json:"nvlink_crc_error_threshold"`

	// Rule 3: GPU-UTIL-003 (Persistently Low Utilization)
	LowUtilThresholdPercent float64 `json:"low_util_threshold_percent"`
	LowUtilWindowMinutes    float64 `json:"low_util_window_minutes"`
	MinUtilizationSamples   int     `json:"min_utilization_samples"`
	HighVRAMContextPercent  float64 `json:"high_vram_context_percent"`

	// Rule 4: GPU-CORR-004 (Workload Failure Correlation)
	CorrelationLeadMinutes float64 `json:"correlation_lead_minutes"`
	CorrelationLagMinutes  float64 `json:"correlation_lag_minutes"`

	// Rule 5: GPU-FRESH-005 (Monitoring Data Freshness)
	MaxStalenessSeconds float64 `json:"max_staleness_seconds"`

	// Rule 6: GPU-THERM-006 (Thermal, Power, and Memory Pressure)
	TempWarningCelsius     float64 `json:"temp_warning_celsius"`
	TempCriticalCelsius    float64 `json:"temp_critical_celsius"`
	MemTempWarningCelsius  float64 `json:"mem_temp_warning_celsius"`
	FBMemoryWarningPercent float64 `json:"fb_memory_warning_percent"`
	PowerCapWarningPercent float64 `json:"power_cap_warning_percent"`

	// Optional Capacity Cost Estimation (0 or nil means disabled; never presented as realized savings)
	OptionalSampleGPUHourlyRateUSD *float64 `json:"optional_sample_gpu_hourly_rate_usd,omitempty"`
}

// SourceConfig configures offline fixture mode and optional read-only infrastructure adapters.
type SourceConfig struct {
	Mode                  string        `json:"mode"` // "fixture" or "live"
	DefaultScenarioID     string        `json:"default_scenario_id"`
	FixturesDir           string        `json:"fixtures_dir"`
	HTTPTimeout           time.Duration `json:"http_timeout"`
	MaxResponseBytes      int64         `json:"max_response_bytes"`
	PrometheusEnabled     bool          `json:"prometheus_enabled"`
	PrometheusEndpoint    string        `json:"prometheus_endpoint"`
	PrometheusBearerToken string        `json:"prometheus_bearer_token,omitempty"`
	DCGMExporterEnabled   bool          `json:"dcgm_exporter_enabled"`
	DCGMExporterEndpoint  string        `json:"dcgm_exporter_endpoint"`
	KubernetesEnabled     bool          `json:"kubernetes_enabled"`
	KubernetesAPIServer   string        `json:"kubernetes_api_server"`
	KubernetesBearerToken string        `json:"kubernetes_bearer_token,omitempty"`
}

// Config is the top-level application configuration.
type Config struct {
	ListenAddr     string              `json:"listen_addr"`
	SQLitePath     string              `json:"sqlite_path"`
	RetentionDays  int                 `json:"retention_days"`
	StaticDir      string              `json:"static_dir"`
	Source         SourceConfig        `json:"source"`
	Thresholds     RuleThresholds      `json:"thresholds"`
	MetricMappings MetricMappingConfig `json:"metric_mappings"`
	// Live configures the live telemetry console ("Go Live"), which runs next to the fixture demo.
	Live LiveConfig `json:"live"`
}

// DefaultMetricMapping returns the official NVIDIA DCGM Exporter metric names and label conventions.
func DefaultMetricMapping() MetricMappingConfig {
	return MetricMappingConfig{
		GPUUtilMetric:              "DCGM_FI_DEV_GPU_UTIL",
		MemCopyUtilMetric:          "DCGM_FI_DEV_MEM_COPY_UTIL",
		FBUsedMetric:               "DCGM_FI_DEV_FB_USED",
		FBFreeMetric:               "DCGM_FI_DEV_FB_FREE",
		GPUTempMetric:              "DCGM_FI_DEV_GPU_TEMP",
		MemoryTempMetric:           "DCGM_FI_DEV_MEMORY_TEMP",
		PowerUsageMetric:           "DCGM_FI_DEV_POWER_USAGE",
		XIDErrorsMetric:            "DCGM_FI_DEV_XID_ERRORS",
		PCIeReplayMetric:           "DCGM_FI_DEV_PCIE_REPLAY_COUNTER",
		RowRemapFailureMetric:      "DCGM_FI_DEV_ROW_REMAP_FAILURE",
		UncorrectableRemapMetric:   "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS",
		ECCDBEVolTotalMetric:       "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
		ECCSBEVolTotalMetric:       "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL",
		NVLinkCRCFlitErrorMetric:   "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL",
		NVLinkReplayErrorMetric:    "DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL",
		ThermalViolationMetric:     "DCGM_FI_DEV_THERMAL_VIOLATION",
		PowerViolationMetric:       "DCGM_FI_DEV_POWER_VIOLATION",
		ExporterHealthStatusMetric: "DCGM_EXP_GPU_HEALTH_STATUS",
		ClockEventsCountMetric:     "DCGM_EXP_CLOCK_EVENTS_COUNT",
		UUIDLabelCandidates:        []string{"UUID", "uuid"},
		GPUIndexLabel:              "gpu",
		NodeNameLabelCandidates:    []string{"Hostname", "hostname", "node", "kubernetes_node"},
		ModelNameLabelCandidates:   []string{"modelName", "model_name"},
		PodNameLabelCandidates:     []string{"pod", "pod_name"},
		NamespaceLabelCandidates:   []string{"namespace", "pod_namespace"},
		ContainerLabelCandidates:   []string{"container", "container_name"},
	}
}

// DefaultRuleThresholds returns conservative, production-grounded default rule thresholds.
func DefaultRuleThresholds() RuleThresholds {
	sampleRate := 3.50 // Explicitly labeled optional sample rate ($3.50/GPU-hr) for illustrative estimates
	return RuleThresholds{
		PendingGraceSeconds:            60,
		CriticalPendingMinutes:         30.0,
		XIDErrorThreshold:              1,
		ECCDBEThreshold:                1,
		RowRemapFailureThreshold:       1,
		PCIeReplayWarningCount:         10,
		NVLinkCRCErrorThreshold:        1,
		LowUtilThresholdPercent:        15.0,
		LowUtilWindowMinutes:           60.0,
		MinUtilizationSamples:          6,
		HighVRAMContextPercent:         50.0,
		CorrelationLeadMinutes:         15.0,
		CorrelationLagMinutes:          5.0,
		MaxStalenessSeconds:            180.0,
		TempWarningCelsius:             80.0,
		TempCriticalCelsius:            87.0,
		MemTempWarningCelsius:          90.0,
		FBMemoryWarningPercent:         95.0,
		PowerCapWarningPercent:         98.0,
		OptionalSampleGPUHourlyRateUSD: &sampleRate,
	}
}

// DefaultConfig returns a complete default configuration suitable for offline demo mode.
func DefaultConfig() Config {
	return Config{
		ListenAddr:    ":8080",
		SQLitePath:    "gpulens.db",
		RetentionDays: 14,
		StaticDir:     "web/dist",
		Source: SourceConfig{
			Mode:              "fixture",
			DefaultScenarioID: "scenario-5-correlated-failure",
			FixturesDir:       "fixtures",
			HTTPTimeout:       10 * time.Second,
			MaxResponseBytes:  16 * 1024 * 1024, // 16 MiB
		},
		Thresholds:     DefaultRuleThresholds(),
		MetricMappings: DefaultMetricMapping(),
		Live:           DefaultLiveConfig(),
	}
}

// Validate checks configuration invariants at startup and returns descriptive errors.
func (c Config) Validate() error {
	if c.ListenAddr == "" {
		return errors.New("config: listen_addr must not be empty")
	}
	if c.Source.Mode != "fixture" && c.Source.Mode != "live" {
		return fmt.Errorf("config: source.mode must be 'fixture' or 'live', got %q", c.Source.Mode)
	}
	if c.Source.HTTPTimeout <= 0 || c.Source.HTTPTimeout > 120*time.Second {
		return fmt.Errorf("config: source.http_timeout must be between 1ms and 120s, got %v", c.Source.HTTPTimeout)
	}
	if c.Thresholds.LowUtilThresholdPercent <= 0 || c.Thresholds.LowUtilThresholdPercent >= 100 {
		return fmt.Errorf("config: thresholds.low_util_threshold_percent must be in (0, 100), got %.2f", c.Thresholds.LowUtilThresholdPercent)
	}
	if c.Thresholds.LowUtilWindowMinutes <= 0 {
		return errors.New("config: thresholds.low_util_window_minutes must be > 0")
	}
	if c.Thresholds.MinUtilizationSamples < 1 {
		return errors.New("config: thresholds.min_utilization_samples must be >= 1")
	}
	if c.Thresholds.MaxStalenessSeconds <= 0 {
		return errors.New("config: thresholds.max_staleness_seconds must be > 0")
	}
	if c.Thresholds.TempWarningCelsius <= 0 || c.Thresholds.TempCriticalCelsius <= c.Thresholds.TempWarningCelsius {
		return errors.New("config: thresholds.temp_critical_celsius must be greater than temp_warning_celsius")
	}
	if err := ValidateThresholds(c.Thresholds); err != nil {
		return fmt.Errorf("config: thresholds.%w", err)
	}
	if err := c.Live.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if c.Source.Mode == "live" {
		if !c.Live.Enabled {
			return errors.New("config: source.mode 'live' requires the live console to be enabled")
		}
		if !c.Live.NvidiaSMIEnabled && !c.Live.DCGM.Enabled && !c.Live.Prometheus.Enabled && !c.Live.Kubernetes.Enabled {
			return errors.New("config: live mode requires at least one telemetry source (nvidia-smi, DCGM exporter, Prometheus, or Kubernetes) to be enabled")
		}
	}
	return nil
}

// ApplyLegacySourceFields maps the older source.*_enabled settings onto the live console configuration so that
// existing programmatic configuration keeps working.
func (c *Config) ApplyLegacySourceFields() {
	if c.Source.PrometheusEnabled && !c.Live.Prometheus.Explicit && c.Source.PrometheusEndpoint != "" {
		c.Live.Prometheus = LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: c.Source.PrometheusEndpoint, BearerToken: c.Source.PrometheusBearerToken}
	}
	if c.Source.DCGMExporterEnabled && !c.Live.DCGM.Explicit && c.Source.DCGMExporterEndpoint != "" {
		c.Live.DCGM = LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: c.Source.DCGMExporterEndpoint}
	}
	if c.Source.KubernetesEnabled && !c.Live.Kubernetes.Explicit && c.Source.KubernetesAPIServer != "" {
		c.Live.Kubernetes = LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: c.Source.KubernetesAPIServer, BearerToken: c.Source.KubernetesBearerToken}
	}
}

// RedactedCopy returns a copy of Config with sensitive tokens masked for safe logging and API inspection.
func (c Config) RedactedCopy() Config {
	cp := c
	if cp.Source.PrometheusBearerToken != "" {
		cp.Source.PrometheusBearerToken = "[REDACTED]"
	}
	if cp.Source.KubernetesBearerToken != "" {
		cp.Source.KubernetesBearerToken = "[REDACTED]"
	}
	return cp
}
