// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package alert

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/domain"
)

type receiver struct {
	mu     sync.Mutex
	bodies [][]byte
	heads  []http.Header
	status int
}

func (r *receiver) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, b)
		r.heads = append(r.heads, req.Header.Clone())
		st := r.status
		r.mu.Unlock()
		if st == 0 {
			st = 200
		}
		w.WriteHeader(st)
	}
}

func (r *receiver) events(t *testing.T) []Event {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, b := range r.bodies {
		var ev Event
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatalf("bad body %s: %v", b, err)
		}
		out = append(out, ev)
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func inc(fp string, sev domain.Severity) domain.Incident {
	return domain.Incident{ID: "inc-" + fp, Fingerprint: fp, RuleID: "GPU-HEALTH-002", Severity: sev, Title: "GPU fault " + fp,
		Summary: "XID 79 <fell off the bus>", FirstSeenAt: time.Now().UTC().Add(-2 * time.Minute)}
}

func TestDisabledWithoutDestinations(t *testing.T) {
	n, err := New(Config{}, nil)
	if err != nil || n != nil {
		t.Fatalf("want nil notifier, got %v %v", n, err)
	}
	if n.Enabled() || n.Status().Enabled {
		t.Fatal("nil notifier must report disabled")
	}
	n.Sync([]domain.Incident{inc("a", domain.SeverityCritical)}, Meta{}, true) // must not panic
	n.Close()
}

func TestValidation(t *testing.T) {
	for _, c := range []Config{
		{SlackWebhookURL: "http://hooks.slack.com/services/x"},
		{WebhookURL: "ftp://example.com"},
		{WebhookURL: "https://user:pw@example.com/hook"},
		{WebhookURL: "https://example.com", MinSeverity: "loud"},
	} {
		if _, err := New(c, nil); err == nil {
			t.Errorf("config %+v should be rejected", c)
		}
	}
}

func TestLifecycleSignatureAndDedupe(t *testing.T) {
	rec := &receiver{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	n, err := New(Config{WebhookURL: srv.URL + "/hook", WebhookSecret: "s3cret", PublicURL: "https://gpulens.example.com/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	meta := Meta{Cluster: "prod-a", DataMode: "live", Version: "0.1.0"}

	n.Sync([]domain.Incident{inc("a", domain.SeverityWarning), inc("low", domain.SeverityInfo)}, meta, true)
	waitFor(t, func() bool { return len(rec.events(t)) == 1 })
	ev := rec.events(t)[0]
	if ev.Type != EventOpened || ev.Incident.Fingerprint != "a" || ev.Cluster != "prod-a" || ev.Schema != Schema {
		t.Fatalf("unexpected opened event: %+v", ev)
	}
	if ev.ConsoleURL != "https://gpulens.example.com/?live_tab=incidents" {
		t.Fatalf("console URL %q", ev.ConsoleURL)
	}
	rec.mu.Lock()
	sig := rec.heads[0].Get("X-GPULens-Signature-256")
	body := rec.bodies[0]
	rec.mu.Unlock()
	if sig != "sha256="+Sign("s3cret", body) {
		t.Fatalf("bad signature %q", sig)
	}

	// Same incidents again: nothing new. Then a severity change, and info→critical counts as an opening.
	n.Sync([]domain.Incident{inc("a", domain.SeverityWarning), inc("low", domain.SeverityInfo)}, meta, true)
	n.Sync([]domain.Incident{inc("a", domain.SeverityCritical), inc("low", domain.SeverityCritical)}, meta, true)
	waitFor(t, func() bool { return len(rec.events(t)) == 3 })
	evs := rec.events(t)
	if evs[1].Type != EventSeverityChanged || evs[1].PreviousSeverity != "warning" {
		t.Fatalf("want severity change, got %+v", evs[1])
	}
	if evs[2].Type != EventOpened || evs[2].Incident.Fingerprint != "low" {
		t.Fatalf("want opened for escalated incident, got %+v", evs[2])
	}

	// Warm-up after a restart: missing incidents are not resolved, and still-open ones are not re-announced.
	n.Sync(nil, meta, false)
	n.Sync([]domain.Incident{inc("a", domain.SeverityCritical)}, meta, false)
	time.Sleep(100 * time.Millisecond)
	if got := len(rec.events(t)); got != 3 {
		t.Fatalf("warm-up must not send anything, got %d events", got)
	}
	n.Sync([]domain.Incident{inc("a", domain.SeverityCritical)}, meta, true)
	waitFor(t, func() bool { return len(rec.events(t)) == 4 })
	if ev := rec.events(t)[3]; ev.Type != EventResolved || ev.Incident.Fingerprint != "low" || ev.DurationSeconds <= 0 {
		t.Fatalf("want resolved for low, got %+v", ev)
	}
	st := n.Status()
	if st.Destinations[0].Sent != 4 || !st.Destinations[0].Signed || strings.Contains(st.Destinations[0].Target, "hook") {
		t.Fatalf("status %+v", st.Destinations[0])
	}
}

func TestSlackFormatRedactionAndFailures(t *testing.T) {
	rec := &receiver{status: 500}
	srv := httptest.NewTLSServer(rec.handler())
	defer srv.Close()
	n, err := New(Config{SlackWebhookURL: srv.URL + "/services/T000/B000/SECRETPATH"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.client = srv.Client()
	n.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer n.Close()
	res := n.SendTest(Meta{Cluster: "lab", DataMode: "live_emulated"})
	if len(res) != 1 || res[0].OK || res[0].Error != "HTTP 500" {
		t.Fatalf("test result %+v", res)
	}
	if strings.Contains(res[0].Target, "SECRETPATH") || strings.Contains(n.Status().Destinations[0].Target, "SECRETPATH") {
		t.Fatal("Slack webhook path leaked")
	}
	rec.mu.Lock()
	var msg map[string]interface{}
	_ = json.Unmarshal(rec.bodies[0], &msg)
	rec.mu.Unlock()
	if !strings.Contains(msg["text"].(string), "[EMULATED]") || msg["blocks"] == nil {
		t.Fatalf("slack message %v", msg)
	}
	m := SlackMessage(Event{Type: EventOpened, Cluster: "c", Incident: &IncidentRef{Severity: "critical", Title: "a<b>", RuleID: "R"}})
	if !strings.Contains(m["text"].(string), "🔴") || strings.Contains(m["blocks"].([]interface{})[0].(map[string]interface{})["text"].(map[string]string)["text"], "<b>") {
		t.Fatalf("slack escaping/emoji wrong: %v", m)
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	target := &receiver{}
	ts := httptest.NewServer(target.handler())
	defer ts.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ts.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	n, _ := New(Config{WebhookURL: redir.URL}, nil)
	defer n.Close()
	res := n.SendTest(Meta{})
	if res[0].OK || len(target.events(t)) != 0 {
		t.Fatalf("redirect must not be followed: %+v", res)
	}
}

func TestSnapshotRestore(t *testing.T) {
	rec := &receiver{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	a, _ := New(Config{WebhookURL: srv.URL}, nil)
	a.Sync([]domain.Incident{inc("x", domain.SeverityCritical)}, Meta{}, true)
	waitFor(t, func() bool { return len(rec.events(t)) == 1 })
	snap := a.Snapshot()
	a.Close()
	b, _ := New(Config{WebhookURL: srv.URL}, nil)
	defer b.Close()
	b.Restore(snap)
	b.Sync([]domain.Incident{inc("x", domain.SeverityCritical)}, Meta{}, true) // already announced before restart
	b.Sync(nil, Meta{}, true)                                                  // now it resolves
	waitFor(t, func() bool { return len(rec.events(t)) == 2 })
	if ev := rec.events(t)[1]; ev.Type != EventResolved {
		t.Fatalf("want resolved after restore, got %+v", ev)
	}
}
