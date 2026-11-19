// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package api

import (
	"os"
	"strings"
	"testing"
)

// TestOpenAPICoversEveryRoute keeps docs/openapi.yaml in step with the routes the server registers.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	spec, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	routes := []string{
		"/healthz", "/readyz", "/metrics", "/api/v1/overview", "/api/v1/incidents", "/api/v1/incidents/{id}", "/api/v1/capacity",
		"/api/v1/sources", "/api/v1/rules", "/api/v1/scenarios", "/api/v1/scenarios/select", "/api/v1/audit", "/api/v1/fleet",
		"/api/v1/live/status", "/api/v1/live/state", "/api/v1/live/summary", "/api/v1/live/start", "/api/v1/live/stop",
		"/api/v1/live/recheck", "/api/v1/live/config", "/api/v1/live/thresholds", "/api/v1/live/alerts", "/api/v1/live/alerts/test",
	}
	for _, r := range routes {
		if !strings.Contains(string(spec), "\n  "+r+":\n") {
			t.Errorf("docs/openapi.yaml does not document %s", r)
		}
	}
}
