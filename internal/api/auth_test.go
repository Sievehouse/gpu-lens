// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Sievehouse/gpu-lens/internal/alert"
	"github.com/Sievehouse/gpu-lens/internal/audit"
	"github.com/Sievehouse/gpu-lens/internal/fleet"
)

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func newAuthServer(t *testing.T) (*Server, *audit.Log) {
	t.Helper()
	srv := newLiveTestServer(t, nil)
	srv.SetAuth(AuthConfig{AdminUser: "admin", AdminPass: "admin-pass-1", ViewerUser: "viewer", ViewerPass: "viewer-pass-1"})
	log, err := audit.New(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	srv.SetAudit(log)
	return srv, log
}

func TestAuthRequiredExceptProbes(t *testing.T) {
	srv, _ := newAuthServer(t)
	for _, p := range []string{"/", "/api/v1/overview", "/api/v1/live/state", "/metrics", "/api/v1/audit"} {
		rec := liveReq{method: http.MethodGet, path: p}.do(srv)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Basic") {
			t.Errorf("%s without credentials: got %d", p, rec.Code)
		}
	}
	rec := liveReq{method: http.MethodGet, path: "/api/v1/overview", headers: map[string]string{"Authorization": basic("admin", "wrong")}}.do(srv)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password accepted: %d", rec.Code)
	}
	for _, p := range []string{"/healthz", "/readyz"} {
		if rec := (liveReq{method: http.MethodGet, path: p}).do(srv); rec.Code != http.StatusOK {
			t.Errorf("%s must stay public, got %d", p, rec.Code)
		}
	}
	if rec := (liveReq{method: http.MethodGet, path: "/healthz"}).do(srv); rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("security headers missing")
	}
}

func TestViewerIsReadOnlyAndAudited(t *testing.T) {
	srv, log := newAuthServer(t)
	viewer := map[string]string{"Authorization": basic("viewer", "viewer-pass-1")}
	admin := map[string]string{"Authorization": basic("admin", "admin-pass-1")}

	rec := liveReq{method: http.MethodGet, path: "/api/v1/live/status", headers: viewer}.do(srv)
	var st map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != 200 || st["role"] != "viewer" || st["can_operate"] != false || st["config_mutable"] != false || st["user"] != "viewer" {
		t.Fatalf("viewer status: %d %v", rec.Code, st)
	}
	for _, c := range []struct{ path, body string }{
		{"/api/v1/live/thresholds", `{"overrides":{"temp_warning_celsius":75}}`},
		{"/api/v1/live/stop", ``},
		{"/api/v1/live/recheck", ``},
		{"/api/v1/live/config", `{"interval_seconds":5}`},
		{"/api/v1/live/alerts/test", ``},
		{"/api/v1/live/start", `{"interval_seconds":5}`},
	} {
		rec := liveReq{method: http.MethodPost, path: c.path, body: c.body, headers: viewer}.do(srv)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "viewer") {
			t.Errorf("viewer POST %s: got %d %s", c.path, rec.Code, rec.Body.String())
		}
	}
	// A viewer may resume watching.
	if rec := (liveReq{method: http.MethodPost, path: "/api/v1/live/start", body: `{}`, headers: viewer}).do(srv); rec.Code != 200 {
		t.Fatalf("viewer start: %d %s", rec.Code, rec.Body.String())
	}
	rec = liveReq{method: http.MethodPost, path: "/api/v1/live/thresholds", body: `{"overrides":{"temp_warning_celsius":75}}`, headers: admin}.do(srv)
	if rec.Code != 200 {
		t.Fatalf("admin thresholds: %d %s", rec.Code, rec.Body.String())
	}
	// Viewers can still switch the simulation scenario; it is audited.
	rec = liveReq{method: http.MethodPost, path: "/api/v1/scenarios/select", body: `{"scenario_id":"scenario-3-gpu-health-warning"}`, headers: viewer}.do(srv)
	if rec.Code != 200 {
		t.Fatalf("scenario select: %d %s", rec.Code, rec.Body.String())
	}

	entries := log.List(0)
	var denied, thresholds, scenario int
	for _, e := range entries {
		switch {
		case e.Outcome == audit.OutcomeDenied && e.Actor == "viewer":
			denied++
		case e.Action == "live.thresholds" && e.Actor == "admin" && strings.Contains(e.Detail, "temp_warning_celsius=75"):
			thresholds++
		case e.Action == "simulation.scenario.select" && strings.Contains(e.Detail, "scenario-3-gpu-health-warning"):
			scenario++
		}
	}
	if denied != 6 || thresholds != 1 || scenario != 1 {
		t.Fatalf("audit log: denied=%d thresholds=%d scenario=%d entries=%+v", denied, thresholds, scenario, entries)
	}
	// The audit endpoint and file agree.
	rec = liveReq{method: http.MethodGet, path: "/api/v1/audit", headers: viewer}.do(srv)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"persistent":true`) {
		t.Fatalf("audit endpoint: %d %s", rec.Code, rec.Body.String())
	}
	b, _ := os.ReadFile(log.Path())
	if strings.Count(string(b), "\n") != len(entries) {
		t.Fatalf("audit file has %d lines, memory has %d", strings.Count(string(b), "\n"), len(entries))
	}
	// The change also appears in the console activity log.
	rec = liveReq{method: http.MethodGet, path: "/api/v1/live/state", headers: admin}.do(srv)
	if !strings.Contains(rec.Body.String(), `"kind":"audit"`) {
		t.Fatal("audited change missing from the activity log")
	}
}

func TestConfigPatchAuditNeverLogsTokens(t *testing.T) {
	srv, log := newAuthServer(t)
	admin := map[string]string{"Authorization": basic("admin", "admin-pass-1")}
	rec := liveReq{method: http.MethodPost, path: "/api/v1/live/config", headers: admin,
		body: `{"prometheus":{"enabled":true,"endpoint":"http://prom:9090","bearer_token":"SUPERSECRET"}}`}.do(srv)
	if rec.Code != 200 {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}
	for _, e := range log.List(0) {
		if strings.Contains(e.Detail, "SUPERSECRET") {
			t.Fatalf("secret leaked into audit: %+v", e)
		}
	}
	b, _ := os.ReadFile(log.Path())
	if strings.Contains(string(b), "SUPERSECRET") || !strings.Contains(string(b), "token set") {
		t.Fatalf("audit file content wrong: %s", b)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	srv := newLiveTestServer(t, nil)
	_ = liveReq{method: http.MethodGet, path: "/api/v1/overview"}.do(srv)
	rec := liveReq{method: http.MethodGet, path: "/metrics"}.do(srv)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{"gpulens_build_info{", `gpulens_http_requests_total{code="200"}`, "gpulens_live_running 0",
		`gpulens_live_incidents_open{severity="critical"} 0`, "gpulens_readonly_guard_ok 1", "# TYPE gpulens_live_source_up gauge"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("metrics must not be readable cross-origin")
	}
}

func TestAlertEndpoints(t *testing.T) {
	var mu sync.Mutex
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
	}))
	defer hook.Close()
	srv := newLiveTestServer(t, nil)
	if rec := (liveReq{method: http.MethodPost, path: "/api/v1/live/alerts/test", body: `{}`}).do(srv); rec.Code != http.StatusConflict {
		t.Fatalf("test without destinations: %d", rec.Code)
	}
	n, err := alert.New(alert.Config{WebhookURL: hook.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	srv.live.SetAlerter(n)
	rec := liveReq{method: http.MethodPost, path: "/api/v1/live/alerts/test", body: `{}`}.do(srv)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("alert test: %d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !strings.Contains(got[0], `"type":"test"`) {
		t.Fatalf("webhook got %v", got)
	}
	rec = liveReq{method: http.MethodGet, path: "/api/v1/live/alerts"}.do(srv)
	if !strings.Contains(rec.Body.String(), `"sent":1`) {
		t.Fatalf("alert status: %s", rec.Body.String())
	}
}

func TestSummaryAndFleetEndpoints(t *testing.T) {
	srv := newLiveTestServer(t, nil)
	rec := liveReq{method: http.MethodGet, path: "/api/v1/live/summary", remote: "10.1.2.3:4000", host: "gpulens.cluster-a:8080"}.do(srv)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"schema":"gpulens.summary/v1"`) {
		t.Fatalf("summary from a remote hub: %d %s", rec.Code, rec.Body.String())
	}
	hub, err := fleet.NewHub(srv.live, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetFleet(hub)
	rec = liveReq{method: http.MethodGet, path: "/api/v1/fleet"}.do(srv)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"local"`) || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("fleet: %d %s", rec.Code, rec.Body.String())
	}
	authSrv, _ := newAuthServer(t)
	if rec := (liveReq{method: http.MethodGet, path: "/api/v1/live/summary"}).do(authSrv); rec.Code != http.StatusUnauthorized {
		t.Fatalf("summary must require credentials when auth is on: %d", rec.Code)
	}
}
