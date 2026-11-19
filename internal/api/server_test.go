// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/config"
	"github.com/Sievehouse/gpu-lens/internal/domain"
	"github.com/Sievehouse/gpu-lens/internal/rules"
	"github.com/Sievehouse/gpu-lens/internal/source"
	"github.com/Sievehouse/gpu-lens/internal/store"
)

func TestAPIEndpointsEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	baseTime := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	snap := domain.ClusterSnapshot{
		ScenarioID:          "scenario-test",
		ScenarioName:        "Test Scenario",
		ScenarioDescription: "Integration test scenario",
		ClusterName:         "test-gpu-cluster",
		DataMode:            "simulated_fixture",
		CollectedAt:         baseTime,
		Sources: []domain.SourceHealth{
			{ID: "fixture", Name: "Fixture", Kind: "fixture", Mode: "simulated", Available: true, ReadOnlyVerified: true, LastSuccessAt: baseTime},
		},
		Nodes: []domain.NodeState{
			{Name: "gpu-node-01", Ready: true, Schedulable: true, GPUModel: "NVIDIA H100", GPUCapacity: 8, GPUAllocatable: 7, GPUAllocated: 7},
		},
		GPUs: []domain.GPUDevice{
			{
				UUID:         "GPU-TEST-01",
				DeviceIndex:  0,
				NodeName:     "gpu-node-01",
				ModelName:    "NVIDIA H100",
				LastSampleAt: baseTime,
				Latest:       domain.GPUMetricPoint{Timestamp: baseTime, XIDErrorCode: 79, GPUTempCelsius: 72},
			},
		},
	}

	raw, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "scenario_test.json"), raw, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	fs, err := source.NewFixtureSource(tmpDir, "scenario-test")
	if err != nil {
		t.Fatalf("new fixture source: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Source.FixturesDir = tmpDir
	cfg.Source.DefaultScenarioID = "scenario-test"

	srv := NewServer(cfg, fs, rules.NewDefaultEngine(), store.NewIncidentStore(""), nil)

	for _, endpoint := range []string{
		"/healthz",
		"/readyz",
		"/api/v1/overview",
		"/api/v1/incidents",
		"/api/v1/capacity",
		"/api/v1/sources",
		"/api/v1/rules",
		"/api/v1/scenarios",
	} {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s returned status %d: %s", endpoint, rec.Code, rec.Body.String())
		}
	}

	// Verify unknown scenario returns 400
	badReq := httptest.NewRequest(http.MethodPost, "/api/v1/scenarios/select", bytes.NewBufferString(`{"scenario_id":"nonexistent"}`))
	badRec := httptest.NewRecorder()
	srv.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on unknown scenario, got %d", badRec.Code)
	}
}
