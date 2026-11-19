// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/live"
)

func summaryServer(t *testing.T, s live.Summary, wantUser, wantPass string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/live/summary" {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		if wantUser != "" {
			if u, p, ok := r.BasicAuth(); !ok || u != wantUser || p != wantPass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(s)
	}))
}

func TestParsePeers(t *testing.T) {
	peers, err := ParsePeers([]string{"a=http://h1:8080/", "b=https://u:p@h2"})
	if err != nil || len(peers) != 2 || peers[0].URL != "http://h1:8080" {
		t.Fatalf("%v %v", peers, err)
	}
	for _, bad := range []string{"noequals", "a=ftp://x", "bad name=http://x", "a=http://x,a=http://y"} {
		if _, err := ParsePeers([]string{bad}); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if r := Redact("https://u:secret@h2"); strings.Contains(r, "secret") {
		t.Fatalf("redaction leaked: %s", r)
	}
}

func TestHubAggregatesPeers(t *testing.T) {
	a := summaryServer(t, live.Summary{Schema: live.SummarySchema, Cluster: "prod-a", Verdict: "ready", GPUsTotal: 8, GPUsFresh: 8,
		Incidents: map[string]int{"critical": 1, "warning": 2}, SourcesConnected: 3}, "", "")
	defer a.Close()
	b := summaryServer(t, live.Summary{Schema: live.SummarySchema, Cluster: "prod-b", Verdict: "ready", GPUsTotal: 4, GPUsFresh: 3,
		Incidents: map[string]int{}}, "hub", "pw-123456")
	defer b.Close()
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"hello":1}`)) }))
	defer wrong.Close()

	bURL := strings.Replace(b.URL, "http://", "http://hub:pw-123456@", 1)
	peers, err := ParsePeers([]string{"a=" + a.URL, "b=" + bURL, "c=" + wrong.URL, "d=http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHub(nil, peers, time.Hour, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h.PollOnce(context.Background())
	v := h.View()
	if !v.Enabled || v.Totals.Members != 4 || v.Totals.Reachable != 2 || v.Totals.GPUsTotal != 12 || v.Totals.Critical != 1 {
		t.Fatalf("totals: %+v", v.Totals)
	}
	byName := map[string]Member{}
	for _, m := range v.Members {
		byName[m.Name] = m
	}
	if !byName["b"].Reachable || strings.Contains(byName["b"].URL, "pw-123456") {
		t.Fatalf("peer b: %+v", byName["b"])
	}
	if byName["c"].Reachable || !strings.Contains(byName["c"].Error, "not a GPU Lens summary") {
		t.Fatalf("peer c: %+v", byName["c"])
	}
	if byName["d"].Reachable || byName["d"].Error == "" {
		t.Fatalf("peer d: %+v", byName["d"])
	}
	// Unhealthy = unreachable peers + peers with critical incidents.
	if v.Totals.Unhealthy != 3 {
		t.Fatalf("unhealthy: %d", v.Totals.Unhealthy)
	}
}

func TestHubWithoutPeers(t *testing.T) {
	h, err := NewHub(nil, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if h.Enabled() || len(h.View().Members) != 0 || h.View().Note == "" {
		t.Fatal("a hub without peers is disabled and explains how to add them")
	}
}
