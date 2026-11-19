// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:    "default config is valid without credentials",
			mutate:  func(c *Config) {},
			wantErr: false,
		},
		{
			name: "empty listen address fails",
			mutate: func(c *Config) {
				c.ListenAddr = ""
			},
			wantErr: true,
		},
		{
			name: "invalid source mode fails",
			mutate: func(c *Config) {
				c.Source.Mode = "unsupported"
			},
			wantErr: true,
		},
		{
			name: "excessive http timeout fails",
			mutate: func(c *Config) {
				c.Source.HTTPTimeout = 5 * time.Minute
			},
			wantErr: true,
		},
		{
			name: "invalid low util threshold fails",
			mutate: func(c *Config) {
				c.Thresholds.LowUtilThresholdPercent = 120.0
			},
			wantErr: true,
		},
		{
			name: "critical temp lower than warning temp fails",
			mutate: func(c *Config) {
				c.Thresholds.TempWarningCelsius = 85.0
				c.Thresholds.TempCriticalCelsius = 80.0
			},
			wantErr: true,
		},
		{
			name: "live mode with default local probes is valid",
			mutate: func(c *Config) {
				c.Source.Mode = "live"
			},
			wantErr: false,
		},
		{
			name: "live mode without any enabled source fails",
			mutate: func(c *Config) {
				c.Source.Mode = "live"
				c.Live.NvidiaSMIEnabled = false
				c.Live.AMDSMIEnabled = false
				c.Live.DCGM.Enabled = false
				c.Live.Prometheus.Enabled = false
				c.Live.Kubernetes.Enabled = false
			},
			wantErr: true,
		},
		{
			name: "live mode with the live console disabled fails",
			mutate: func(c *Config) {
				c.Source.Mode = "live"
				c.Live.Enabled = false
			},
			wantErr: true,
		},
		{
			name: "live endpoint with embedded credentials fails",
			mutate: func(c *Config) {
				c.Live.Prometheus.Endpoint = "http://user:secret@prometheus.example:9090"
			},
			wantErr: true,
		},
		{
			name: "live endpoint with a non-http scheme fails",
			mutate: func(c *Config) {
				c.Live.DCGM.Endpoint = "file:///etc/passwd"
			},
			wantErr: true,
		},
		{
			name: "live interval below one second fails",
			mutate: func(c *Config) {
				c.Live.IntervalSeconds = 0.2
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfigRedaction(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Source.PrometheusBearerToken = "secret-prom-token"
	cfg.Source.KubernetesBearerToken = "secret-k8s-token"

	redacted := cfg.RedactedCopy()
	if redacted.Source.PrometheusBearerToken != "[REDACTED]" {
		t.Fatalf("expected redacted prometheus token, got %q", redacted.Source.PrometheusBearerToken)
	}
	if redacted.Source.KubernetesBearerToken != "[REDACTED]" {
		t.Fatalf("expected redacted kubernetes token, got %q", redacted.Source.KubernetesBearerToken)
	}
}

func TestLiveConfigEnvAndRedaction(t *testing.T) {
	env := map[string]string{
		"GPULENS_PROMETHEUS_URL":           "http://prom.monitoring.svc:9090",
		"GPULENS_PROMETHEUS_TOKEN":         "super-secret-prom-token",
		"GPULENS_KUBERNETES":               "off",
		"GPULENS_NVIDIA_SMI":               "off",
		"GPULENS_LIVE_INTERVAL_SECONDS":    "5",
		"GPULENS_CLUSTER_NAME":             "lab-a",
		"GPULENS_DCGM_EXPORTER_URL":        "http://10.0.0.5:9400/metrics",
		"GPULENS_KUBERNETES_TOKEN_FILE":    "/tmp/k8s-token",
		"GPULENS_LIVE_ALLOW_REMOTE_CONFIG": "true",
	}
	c := DefaultLiveConfig()
	c.ApplyEnv(func(k string) string { return env[k] })

	if !c.Prometheus.Explicit || c.Prometheus.Endpoint != "http://prom.monitoring.svc:9090" || c.Prometheus.BearerToken != "super-secret-prom-token" {
		t.Fatalf("prometheus env not applied: %+v", c.Prometheus)
	}
	if c.Kubernetes.Enabled || c.NvidiaSMIEnabled {
		t.Fatalf("expected kubernetes and nvidia-smi to be disabled by env")
	}
	if c.IntervalSeconds != 5 || c.ClusterName != "lab-a" || !c.AllowRemoteConfig || !c.DCGM.Explicit {
		t.Fatalf("unexpected live config after env overlay: %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("env-configured live config should validate: %v", err)
	}

	raw, err := json.Marshal(c.View())
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if strings.Contains(string(raw), "super-secret-prom-token") {
		t.Fatalf("redacted view leaked the bearer token: %s", raw)
	}
	if !c.View().Prometheus.TokenSet {
		t.Fatalf("view should report that a token is set")
	}
	full, _ := json.Marshal(c)
	if strings.Contains(string(full), "super-secret-prom-token") {
		t.Fatalf("LiveConfig JSON must never serialize bearer tokens: %s", full)
	}
}

func TestDetectInCluster(t *testing.T) {
	env := map[string]string{"KUBERNETES_SERVICE_HOST": "10.96.0.1", "KUBERNETES_SERVICE_PORT": "443"}
	c := DefaultLiveConfig()
	c.DetectInCluster(func(k string) string { return env[k] }, func(string) bool { return true })
	if !c.InCluster || c.Kubernetes.Endpoint != "https://10.96.0.1:443" || !c.Kubernetes.Explicit || c.Kubernetes.TokenFile == "" || c.Kubernetes.CAFile == "" {
		t.Fatalf("in-cluster detection failed: %+v", c.Kubernetes)
	}

	// An explicitly configured API server always wins over in-cluster detection.
	c2 := DefaultLiveConfig()
	c2.Kubernetes.Endpoint, c2.Kubernetes.Explicit = "http://127.0.0.1:8001", true
	c2.DetectInCluster(func(k string) string { return env[k] }, func(string) bool { return true })
	if c2.InCluster || c2.Kubernetes.Endpoint != "http://127.0.0.1:8001" {
		t.Fatalf("explicit endpoint was overridden: %+v", c2.Kubernetes)
	}

	// Outside a cluster nothing changes.
	c3 := DefaultLiveConfig()
	c3.DetectInCluster(func(string) string { return "" }, func(string) bool { return false })
	if c3.InCluster || c3.Kubernetes.Endpoint != DefaultKubectlProxyEndpoint {
		t.Fatalf("unexpected change outside a cluster: %+v", c3.Kubernetes)
	}
}

func TestThresholdOverrides(t *testing.T) {
	base := DefaultRuleThresholds()
	m := ThresholdsToMap(base)
	if m["temp_warning_celsius"] != 80 || m["pending_grace_seconds"] != 60 || len(m) < 19 {
		t.Fatalf("unexpected threshold map: %v", m)
	}

	out, err := ApplyThresholdOverrides(base, map[string]float64{"temp_warning_celsius": 60, "low_util_window_minutes": 2, "min_utilization_samples": 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.TempWarningCelsius != 60 || out.LowUtilWindowMinutes != 2 || out.MinUtilizationSamples != 3 {
		t.Fatalf("overrides not applied: %+v", out)
	}
	if base.TempWarningCelsius != 80 {
		t.Fatalf("base thresholds were mutated")
	}

	bad := []map[string]float64{
		{"no_such_threshold": 1},
		{"temp_warning_celsius": 95},     // would exceed the critical threshold (87)
		{"min_utilization_samples": 2.5}, // must be whole
		{"max_staleness_seconds": -1},
		{"low_util_threshold_percent": 100},
		{"fb_memory_warning_percent": 0},
	}
	for _, ov := range bad {
		if _, err := ApplyThresholdOverrides(base, ov); err == nil {
			t.Fatalf("expected override %v to be rejected", ov)
		}
	}
}
