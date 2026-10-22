package config

import (
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
			name: "live mode without any enabled source fails",
			mutate: func(c *Config) {
				c.Source.Mode = "live"
				c.Source.PrometheusEnabled = false
				c.Source.DCGMExporterEnabled = false
				c.Source.KubernetesEnabled = false
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
