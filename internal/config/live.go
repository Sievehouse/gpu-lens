// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Default local endpoints probed when a live source has not been configured explicitly.
const (
	DefaultDCGMExporterEndpoint = "http://127.0.0.1:9400/metrics"
	DefaultPrometheusEndpoint   = "http://127.0.0.1:9090"
	DefaultKubectlProxyEndpoint = "http://127.0.0.1:8001"

	inClusterTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	inClusterCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// LiveSourceConfig configures one network telemetry source of the live console.
type LiveSourceConfig struct {
	Enabled bool `json:"enabled"`
	// Explicit is true when the endpoint was configured by the operator (flag, environment or console) rather
	// than being the default local address GPU Lens probes on its own. Only explicit sources are reported as
	// failures when they cannot be reached.
	Explicit    bool   `json:"explicit"`
	Endpoint    string `json:"endpoint"`
	BearerToken string `json:"-"`
	TokenFile   string `json:"token_file"`
	CAFile      string `json:"ca_file"`
}

// LiveConfig configures the live telemetry console ("Go Live").
type LiveConfig struct {
	Enabled            bool    `json:"enabled"`
	AutoStart          bool    `json:"auto_start"`
	AllowRemoteConfig  bool    `json:"allow_remote_config"`
	IntervalSeconds    float64 `json:"interval_seconds"`
	IdleTimeoutSeconds float64 `json:"idle_timeout_seconds"`
	HistoryPoints      int     `json:"history_points"`
	ClusterName        string  `json:"cluster_name"`

	NvidiaSMIEnabled bool   `json:"nvidia_smi_enabled"`
	NvidiaSMIPath    string `json:"nvidia_smi_path"`
	AMDSMIEnabled    bool   `json:"amd_smi_enabled"`
	AMDSMIPath       string `json:"amd_smi_path"`
	HostVitals       bool   `json:"host_vitals_enabled"`

	DCGM       LiveSourceConfig `json:"dcgm_exporter"`
	Prometheus LiveSourceConfig `json:"prometheus"`
	Kubernetes LiveSourceConfig `json:"kubernetes"`

	PrometheusBackfillMinutes float64 `json:"prometheus_backfill_minutes"`
	InCluster                 bool    `json:"in_cluster"`
}

// DefaultLiveConfig returns the live console defaults: local probes on, network sources auto-probed on their
// conventional local addresses.
func DefaultLiveConfig() LiveConfig {
	return LiveConfig{
		Enabled:                   true,
		IntervalSeconds:           2,
		IdleTimeoutSeconds:        900,
		HistoryPoints:             900,
		NvidiaSMIEnabled:          true,
		AMDSMIEnabled:             true,
		HostVitals:                true,
		DCGM:                      LiveSourceConfig{Enabled: true, Endpoint: DefaultDCGMExporterEndpoint},
		Prometheus:                LiveSourceConfig{Enabled: true, Endpoint: DefaultPrometheusEndpoint},
		Kubernetes:                LiveSourceConfig{Enabled: true, Endpoint: DefaultKubectlProxyEndpoint},
		PrometheusBackfillMinutes: 60,
	}
}

func envBool(v string) (value bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "enabled":
		return true, true
	case "0", "false", "no", "off", "disabled":
		return false, true
	}
	return false, false
}

// ApplyEnv overlays GPULENS_* environment variables onto the live configuration.
func (c *LiveConfig) ApplyEnv(getenv func(string) string) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if v, ok := envBool(getenv("GPULENS_LIVE_ENABLED")); ok {
		c.Enabled = v
	}
	if v, ok := envBool(getenv("GPULENS_LIVE_ALLOW_REMOTE_CONFIG")); ok {
		c.AllowRemoteConfig = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(getenv("GPULENS_LIVE_INTERVAL_SECONDS")), 64); err == nil {
		c.IntervalSeconds = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(getenv("GPULENS_LIVE_IDLE_TIMEOUT_SECONDS")), 64); err == nil {
		c.IdleTimeoutSeconds = v
	}
	if v := strings.TrimSpace(getenv("GPULENS_CLUSTER_NAME")); v != "" {
		c.ClusterName = v
	}
	if v, ok := envBool(getenv("GPULENS_NVIDIA_SMI")); ok {
		c.NvidiaSMIEnabled = v
	}
	if v := strings.TrimSpace(getenv("GPULENS_NVIDIA_SMI_PATH")); v != "" {
		c.NvidiaSMIPath = v
	}
	if v, ok := envBool(getenv("GPULENS_AMD_SMI")); ok {
		c.AMDSMIEnabled = v
	}
	if v := strings.TrimSpace(getenv("GPULENS_AMD_SMI_PATH")); v != "" {
		c.AMDSMIPath = v
	}
	if v, ok := envBool(getenv("GPULENS_HOST_VITALS")); ok {
		c.HostVitals = v
	}
	applySourceEnv(&c.DCGM, getenv, "GPULENS_DCGM_EXPORTER")
	applySourceEnv(&c.Prometheus, getenv, "GPULENS_PROMETHEUS")
	applySourceEnv(&c.Kubernetes, getenv, "GPULENS_KUBERNETES")
	if v, err := strconv.ParseFloat(strings.TrimSpace(getenv("GPULENS_PROMETHEUS_BACKFILL_MINUTES")), 64); err == nil {
		c.PrometheusBackfillMinutes = v
	}
}

func applySourceEnv(s *LiveSourceConfig, getenv func(string) string, prefix string) {
	if v, ok := envBool(getenv(prefix)); ok {
		s.Enabled = v
	}
	if v := strings.TrimSpace(getenv(prefix + "_URL")); v != "" {
		s.Endpoint = v
		s.Explicit = true
		s.Enabled = true
	}
	if v := strings.TrimSpace(getenv(prefix + "_TOKEN")); v != "" {
		s.BearerToken = v
	}
	if v := strings.TrimSpace(getenv(prefix + "_TOKEN_FILE")); v != "" {
		s.TokenFile = v
	}
	if v := strings.TrimSpace(getenv(prefix + "_CA_FILE")); v != "" {
		s.CAFile = v
	}
}

// DetectInCluster points the Kubernetes source at the in-cluster API server when GPU Lens runs in a pod with a
// mounted service-account token and no API server was configured explicitly.
func (c *LiveConfig) DetectInCluster(getenv func(string) string, fileExists func(string) bool) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if fileExists == nil {
		fileExists = func(p string) bool {
			info, err := os.Stat(p)
			return err == nil && !info.IsDir()
		}
	}
	if c.Kubernetes.Explicit || !c.Kubernetes.Enabled {
		return
	}
	host := strings.TrimSpace(getenv("KUBERNETES_SERVICE_HOST"))
	if host == "" || !fileExists(inClusterTokenFile) {
		return
	}
	port := strings.TrimSpace(getenv("KUBERNETES_SERVICE_PORT"))
	if port == "" {
		port = "443"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	c.Kubernetes.Endpoint = "https://" + host + ":" + port
	c.Kubernetes.Explicit = true
	c.Kubernetes.TokenFile = inClusterTokenFile
	if fileExists(inClusterCAFile) {
		c.Kubernetes.CAFile = inClusterCAFile
	}
	c.InCluster = true
}

// ValidateEndpoint accepts only plain http(s) URLs with a host and without embedded credentials.
func ValidateEndpoint(name, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%s endpoint is not a valid URL", name)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s endpoint must start with http:// or https://", name)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%s endpoint is missing a host", name)
	}
	if u.User != nil {
		return fmt.Errorf("%s endpoint must not contain credentials; use the token setting instead", name)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%s endpoint must not contain a #fragment", name)
	}
	return nil
}

// Validate checks live configuration invariants.
func (c LiveConfig) Validate() error {
	if c.IntervalSeconds < 1 || c.IntervalSeconds > 300 {
		return fmt.Errorf("live: interval_seconds must be between 1 and 300, got %g", c.IntervalSeconds)
	}
	if c.IdleTimeoutSeconds != 0 && c.IdleTimeoutSeconds < 30 {
		return fmt.Errorf("live: idle_timeout_seconds must be 0 (never) or at least 30, got %g", c.IdleTimeoutSeconds)
	}
	if c.HistoryPoints < 60 || c.HistoryPoints > 20000 {
		return fmt.Errorf("live: history_points must be between 60 and 20000, got %d", c.HistoryPoints)
	}
	if c.PrometheusBackfillMinutes < 0 || c.PrometheusBackfillMinutes > 720 {
		return fmt.Errorf("live: prometheus_backfill_minutes must be between 0 and 720, got %g", c.PrometheusBackfillMinutes)
	}
	if len(c.ClusterName) > 120 {
		return errors.New("live: cluster_name is too long (max 120 characters)")
	}
	for _, s := range []struct {
		name string
		cfg  LiveSourceConfig
	}{{"DCGM exporter", c.DCGM}, {"Prometheus", c.Prometheus}, {"Kubernetes", c.Kubernetes}} {
		if !s.cfg.Enabled && s.cfg.Endpoint == "" {
			continue
		}
		if err := ValidateEndpoint(s.name, s.cfg.Endpoint); err != nil {
			return fmt.Errorf("live: %w", err)
		}
	}
	return nil
}

// LiveSourceView is the redacted, API-safe representation of a live source configuration.
type LiveSourceView struct {
	Enabled   bool   `json:"enabled"`
	Explicit  bool   `json:"explicit"`
	Endpoint  string `json:"endpoint"`
	TokenSet  bool   `json:"token_set"`
	TokenFile string `json:"token_file"`
	CAFile    string `json:"ca_file"`
}

// LiveConfigView is the redacted, API-safe representation of LiveConfig. Tokens are never included.
type LiveConfigView struct {
	Enabled                   bool           `json:"enabled"`
	AutoStart                 bool           `json:"auto_start"`
	AllowRemoteConfig         bool           `json:"allow_remote_config"`
	IntervalSeconds           float64        `json:"interval_seconds"`
	IdleTimeoutSeconds        float64        `json:"idle_timeout_seconds"`
	HistoryPoints             int            `json:"history_points"`
	ClusterName               string         `json:"cluster_name"`
	NvidiaSMIEnabled          bool           `json:"nvidia_smi_enabled"`
	NvidiaSMIPath             string         `json:"nvidia_smi_path"`
	AMDSMIEnabled             bool           `json:"amd_smi_enabled"`
	AMDSMIPath                string         `json:"amd_smi_path"`
	HostVitals                bool           `json:"host_vitals_enabled"`
	DCGM                      LiveSourceView `json:"dcgm_exporter"`
	Prometheus                LiveSourceView `json:"prometheus"`
	Kubernetes                LiveSourceView `json:"kubernetes"`
	PrometheusBackfillMinutes float64        `json:"prometheus_backfill_minutes"`
	InCluster                 bool           `json:"in_cluster"`
}

func (s LiveSourceConfig) view() LiveSourceView {
	return LiveSourceView{
		Enabled:   s.Enabled,
		Explicit:  s.Explicit,
		Endpoint:  s.Endpoint,
		TokenSet:  s.BearerToken != "" || s.TokenFile != "",
		TokenFile: s.TokenFile,
		CAFile:    s.CAFile,
	}
}

// View returns the redacted representation used by the API.
func (c LiveConfig) View() LiveConfigView {
	return LiveConfigView{
		Enabled:                   c.Enabled,
		AutoStart:                 c.AutoStart,
		AllowRemoteConfig:         c.AllowRemoteConfig,
		IntervalSeconds:           c.IntervalSeconds,
		IdleTimeoutSeconds:        c.IdleTimeoutSeconds,
		HistoryPoints:             c.HistoryPoints,
		ClusterName:               c.ClusterName,
		NvidiaSMIEnabled:          c.NvidiaSMIEnabled,
		NvidiaSMIPath:             c.NvidiaSMIPath,
		AMDSMIEnabled:             c.AMDSMIEnabled,
		AMDSMIPath:                c.AMDSMIPath,
		HostVitals:                c.HostVitals,
		DCGM:                      c.DCGM.view(),
		Prometheus:                c.Prometheus.view(),
		Kubernetes:                c.Kubernetes.view(),
		PrometheusBackfillMinutes: c.PrometheusBackfillMinutes,
		InCluster:                 c.InCluster,
	}
}

// ---------------------------------------------------------------------------------------------
// Rule threshold inspection + live overrides
// ---------------------------------------------------------------------------------------------

// integerThresholdKeys lists the threshold keys that must hold whole numbers.
var integerThresholdKeys = map[string]bool{
	"pending_grace_seconds":       true,
	"xid_error_threshold":         true,
	"ecc_dbe_threshold":           true,
	"row_remap_failure_threshold": true,
	"pcie_replay_warning_count":   true,
	"nvlink_crc_error_threshold":  true,
	"min_utilization_samples":     true,
}

// ThresholdsToMap flattens the numeric rule thresholds into key -> value using their JSON names.
func ThresholdsToMap(th RuleThresholds) map[string]float64 {
	raw, _ := json.Marshal(th)
	var generic map[string]interface{}
	_ = json.Unmarshal(raw, &generic)
	out := make(map[string]float64, len(generic))
	for k, v := range generic {
		if f, ok := v.(float64); ok {
			out[k] = f
		}
	}
	return out
}

// ThresholdKeys returns the sorted names of all tunable thresholds.
func ThresholdKeys() []string {
	m := ThresholdsToMap(DefaultRuleThresholds())
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ApplyThresholdOverrides returns `base` with the given overrides applied, after validating every value.
func ApplyThresholdOverrides(base RuleThresholds, overrides map[string]float64) (RuleThresholds, error) {
	if len(overrides) == 0 {
		return base, nil
	}
	raw, _ := json.Marshal(base)
	var generic map[string]interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return base, err
	}
	known := ThresholdsToMap(DefaultRuleThresholds())
	for k, v := range overrides {
		if _, ok := known[k]; !ok {
			return base, fmt.Errorf("unknown threshold %q", k)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return base, fmt.Errorf("threshold %q must be a finite number", k)
		}
		if integerThresholdKeys[k] {
			if v != math.Trunc(v) {
				return base, fmt.Errorf("threshold %q must be a whole number", k)
			}
			generic[k] = int64(v)
		} else {
			generic[k] = v
		}
	}
	merged, _ := json.Marshal(generic)
	out := base
	if err := json.Unmarshal(merged, &out); err != nil {
		return base, fmt.Errorf("invalid threshold values: %w", err)
	}
	if err := ValidateThresholds(out); err != nil {
		return base, err
	}
	return out, nil
}

// ValidateThresholds checks that rule thresholds are internally consistent and inside sane physical bounds.
func ValidateThresholds(t RuleThresholds) error {
	if t.PendingGraceSeconds < 0 || t.PendingGraceSeconds > 86400 {
		return errors.New("pending_grace_seconds must be between 0 and 86400")
	}
	if t.CriticalPendingMinutes <= 0 || t.CriticalPendingMinutes > 10080 {
		return errors.New("critical_pending_minutes must be greater than 0 (max 10080)")
	}
	for name, v := range map[string]int{
		"xid_error_threshold":         t.XIDErrorThreshold,
		"ecc_dbe_threshold":           t.ECCDBEThreshold,
		"row_remap_failure_threshold": t.RowRemapFailureThreshold,
		"pcie_replay_warning_count":   t.PCIeReplayWarningCount,
		"nvlink_crc_error_threshold":  t.NVLinkCRCErrorThreshold,
	} {
		if v < 1 || v > 1_000_000_000 {
			return fmt.Errorf("%s must be at least 1", name)
		}
	}
	if t.LowUtilThresholdPercent <= 0 || t.LowUtilThresholdPercent >= 100 {
		return fmt.Errorf("low_util_threshold_percent must be in (0, 100), got %.2f", t.LowUtilThresholdPercent)
	}
	if t.LowUtilWindowMinutes <= 0 || t.LowUtilWindowMinutes > 10080 {
		return errors.New("low_util_window_minutes must be greater than 0 (max 10080)")
	}
	if t.MinUtilizationSamples < 1 || t.MinUtilizationSamples > 100000 {
		return errors.New("min_utilization_samples must be at least 1")
	}
	if t.HighVRAMContextPercent <= 0 || t.HighVRAMContextPercent > 100 {
		return errors.New("high_vram_context_percent must be in (0, 100]")
	}
	if t.CorrelationLeadMinutes < 0 || t.CorrelationLeadMinutes > 1440 || t.CorrelationLagMinutes < 0 || t.CorrelationLagMinutes > 1440 {
		return errors.New("correlation_lead_minutes and correlation_lag_minutes must be between 0 and 1440")
	}
	if t.MaxStalenessSeconds <= 0 || t.MaxStalenessSeconds > 86400 {
		return errors.New("max_staleness_seconds must be greater than 0 (max 86400)")
	}
	if t.TempWarningCelsius <= 0 || t.TempWarningCelsius > 150 || t.TempCriticalCelsius > 150 {
		return errors.New("temperature thresholds must be between 0 and 150 °C")
	}
	if t.TempCriticalCelsius <= t.TempWarningCelsius {
		return errors.New("temp_critical_celsius must be greater than temp_warning_celsius")
	}
	if t.MemTempWarningCelsius <= 0 || t.MemTempWarningCelsius > 150 {
		return errors.New("mem_temp_warning_celsius must be between 0 and 150 °C")
	}
	if t.FBMemoryWarningPercent <= 0 || t.FBMemoryWarningPercent > 100 {
		return errors.New("fb_memory_warning_percent must be in (0, 100]")
	}
	if t.PowerCapWarningPercent <= 0 || t.PowerCapWarningPercent > 200 {
		return errors.New("power_cap_warning_percent must be in (0, 200]")
	}
	if t.OptionalSampleGPUHourlyRateUSD != nil && (*t.OptionalSampleGPUHourlyRateUSD < 0 || *t.OptionalSampleGPUHourlyRateUSD > 10000) {
		return errors.New("optional_sample_gpu_hourly_rate_usd must be between 0 and 10000")
	}
	return nil
}
