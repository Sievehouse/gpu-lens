package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/live"
	"github.com/gpulens/gpu-lens/internal/rules"
	"github.com/gpulens/gpu-lens/internal/source"
	"github.com/gpulens/gpu-lens/internal/store"
)

func newLiveTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Source.FixturesDir = "../../fixtures"
	cfg.Live.Enabled = true
	cfg.Live.NvidiaSMIEnabled = false
	cfg.Live.HostVitals = false
	cfg.Live.DCGM.Enabled = false
	cfg.Live.Prometheus.Enabled = false
	cfg.Live.Kubernetes.Enabled = false
	if mutate != nil {
		mutate(&cfg)
	}
	fs, err := source.NewFixtureSource(cfg.Source.FixturesDir, cfg.Source.DefaultScenarioID)
	if err != nil {
		t.Skipf("fixtures unavailable: %v", err)
	}
	srv := NewServer(cfg, fs, rules.NewDefaultEngine(), store.NewIncidentStore(""), nil)
	eng := live.NewEngine(cfg, nil)
	t.Cleanup(func() { eng.Stop("test cleanup") })
	srv.SetLiveEngine(eng)
	return srv
}

type liveReq struct {
	method, path, body, remote, host string
	headers                          map[string]string
}

func (lr liveReq) do(srv *Server) *httptest.ResponseRecorder {
	var body *bytes.Buffer
	if lr.body != "" {
		body = bytes.NewBufferString(lr.body)
	} else {
		body = &bytes.Buffer{}
	}
	req := httptest.NewRequest(lr.method, lr.path, body)
	if lr.method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "127.0.0.1:50000"
	if lr.remote != "" {
		req.RemoteAddr = lr.remote
	}
	req.Host = "localhost:8080"
	if lr.host != "" {
		req.Host = lr.host
	}
	for k, v := range lr.headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func decodeState(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var m map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func TestLiveStateIsServedWithoutStarting(t *testing.T) {
	srv := newLiveTestServer(t, nil)
	rec := liveReq{method: http.MethodGet, path: "/api/v1/live/state"}.do(srv)
	m := decodeState(t, rec)
	for _, k := range []string{"status", "config", "overview", "preflight", "sources", "gpus", "history", "activity", "thresholds"} {
		if _, ok := m[k]; !ok {
			t.Errorf("state is missing %q", k)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("live state must not be cached")
	}
}

func TestLiveGuards(t *testing.T) {
	srv := newLiveTestServer(t, nil)
	cases := []struct {
		name string
		req  liveReq
		want int
	}{
		{"wrong method on state", liveReq{method: http.MethodPost, path: "/api/v1/live/state"}, http.StatusMethodNotAllowed},
		{"GET on a POST endpoint", liveReq{method: http.MethodGet, path: "/api/v1/live/recheck"}, http.StatusMethodNotAllowed},
		{"cross-site origin", liveReq{method: http.MethodPost, path: "/api/v1/live/recheck", headers: map[string]string{"Origin": "https://evil.example"}}, http.StatusForbidden},
		{"cross-site fetch metadata", liveReq{method: http.MethodGet, path: "/api/v1/live/state", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}}, http.StatusForbidden},
		{"DNS rebinding host", liveReq{method: http.MethodGet, path: "/api/v1/live/state", host: "attacker.example:8080"}, http.StatusForbidden},
		{"form content type", liveReq{method: http.MethodPost, path: "/api/v1/live/recheck", headers: map[string]string{"Content-Type": "text/plain"}}, http.StatusUnsupportedMediaType},
		{"same origin ok", liveReq{method: http.MethodGet, path: "/api/v1/live/state", headers: map[string]string{"Origin": "http://localhost:8080"}}, http.StatusOK},
		{"unknown field", liveReq{method: http.MethodPost, path: "/api/v1/live/thresholds", body: `{"nope":1}`}, http.StatusBadRequest},
		{"malformed json", liveReq{method: http.MethodPost, path: "/api/v1/live/config", body: `{`}, http.StatusBadRequest},
	}
	for _, c := range cases {
		if got := c.req.do(srv).Code; got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestLiveConfigIsLocalOnlyAndTokensAreNeverReturned(t *testing.T) {
	srv := newLiveTestServer(t, nil)
	patch := `{"prometheus":{"enabled":true,"endpoint":"http://127.0.0.1:9","bearer_token":"s3cr3t-token-value"}}`

	remote := liveReq{method: http.MethodPost, path: "/api/v1/live/config", body: patch, remote: "203.0.113.9:4444", host: "gpu.example.com"}.do(srv)
	if remote.Code != http.StatusForbidden {
		t.Fatalf("remote connection settings change must be refused, got %d: %s", remote.Code, remote.Body.String())
	}
	proxied := liveReq{method: http.MethodPost, path: "/api/v1/live/config", body: patch, headers: map[string]string{"X-Forwarded-For": "203.0.113.9"}}.do(srv)
	if proxied.Code != http.StatusForbidden {
		t.Fatalf("a proxied loopback request must be treated as remote, got %d", proxied.Code)
	}

	rec := liveReq{method: http.MethodPost, path: "/api/v1/live/config", body: patch}.do(srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("local config change failed: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "s3cr3t-token-value") {
		t.Fatalf("bearer token leaked in the config response")
	}
	again := liveReq{method: http.MethodGet, path: "/api/v1/live/state"}.do(srv)
	if strings.Contains(again.Body.String(), "s3cr3t-token-value") {
		t.Fatalf("bearer token leaked in the state response")
	}
	cfg := decodeState(t, again)["config"].(map[string]interface{})
	prom := cfg["prometheus"].(map[string]interface{})
	if prom["token_set"] != true || prom["endpoint"] != "http://127.0.0.1:9" {
		t.Fatalf("config view should report token_set without the token: %v", prom)
	}

	// Remote callers may still read state and tune thresholds (not connection settings).
	if got := (liveReq{method: http.MethodGet, path: "/api/v1/live/state", remote: "203.0.113.9:4444", host: "gpu.example.com"}).do(srv).Code; got != http.StatusOK {
		t.Fatalf("remote read should work, got %d", got)
	}
	st := decodeState(t, liveReq{method: http.MethodGet, path: "/api/v1/live/state", remote: "203.0.113.9:4444", host: "gpu.example.com"}.do(srv))
	if st["status"].(map[string]interface{})["config_mutable"] != false {
		t.Fatalf("remote callers must be told connection settings are read-only")
	}
}

func TestLiveRemoteConfigCanBeEnabledExplicitly(t *testing.T) {
	srv := newLiveTestServer(t, func(c *config.Config) { c.Live.AllowRemoteConfig = true })
	rec := liveReq{method: http.MethodPost, path: "/api/v1/live/config", body: `{"cluster_name":"remote-edit"}`, remote: "203.0.113.9:4444", host: "gpu.example.com"}.do(srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("explicitly allowed remote config should work: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLiveConfigRejectsUnsafeEndpoints(t *testing.T) {
	srv := newLiveTestServer(t, nil)
	for _, ep := range []string{"file:///etc/passwd", "ftp://example.com", "http://user:pw@example.com/"} {
		body := `{"dcgm_exporter":{"endpoint":"` + ep + `"}}`
		if got := (liveReq{method: http.MethodPost, path: "/api/v1/live/config", body: body}).do(srv).Code; got != http.StatusBadRequest {
			t.Errorf("endpoint %q should be rejected, got %d", ep, got)
		}
	}
}

func TestLiveStartStopRecheckAndThresholds(t *testing.T) {
	srv := newLiveTestServer(t, nil)
	m := decodeState(t, liveReq{method: http.MethodPost, path: "/api/v1/live/start", body: `{"interval_seconds":5}`}.do(srv))
	if m["status"].(map[string]interface{})["state"] != "running" {
		t.Fatalf("start should leave the engine running: %v", m["status"])
	}
	decodeState(t, liveReq{method: http.MethodPost, path: "/api/v1/live/recheck"}.do(srv))

	if got := (liveReq{method: http.MethodPost, path: "/api/v1/live/thresholds", body: `{"overrides":{"temp_warning_celsius":99}}`}).do(srv).Code; got != http.StatusBadRequest {
		t.Errorf("warning above critical must be rejected, got %d", got)
	}
	th := decodeState(t, liveReq{method: http.MethodPost, path: "/api/v1/live/thresholds", body: `{"overrides":{"temp_warning_celsius":70}}`}.do(srv))
	if th["thresholds"].(map[string]interface{})["temp_warning_celsius"].(float64) != 70 {
		t.Errorf("override not applied")
	}
	decodeState(t, liveReq{method: http.MethodPost, path: "/api/v1/live/thresholds", body: `{"reset":true}`}.do(srv))

	m = decodeState(t, liveReq{method: http.MethodPost, path: "/api/v1/live/stop"}.do(srv))
	if m["status"].(map[string]interface{})["state"] == "running" {
		t.Fatalf("stop should stop the engine")
	}
}

func TestLiveDisabledEngineRefusesToStart(t *testing.T) {
	srv := newLiveTestServer(t, func(c *config.Config) { c.Live.Enabled = false })
	if got := (liveReq{method: http.MethodPost, path: "/api/v1/live/start"}).do(srv).Code; got != http.StatusConflict {
		t.Fatalf("a disabled live console must answer 409, got %d", got)
	}
}

func TestDemoEndpointsAreUnaffectedByLive(t *testing.T) {
	srv := newLiveTestServer(t, nil)
	for _, p := range []string{"/api/v1/overview", "/api/v1/incidents", "/api/v1/capacity"} {
		if got := (liveReq{method: http.MethodGet, path: p}).do(srv).Code; got != http.StatusOK {
			t.Errorf("%s returned %d", p, got)
		}
	}
}
