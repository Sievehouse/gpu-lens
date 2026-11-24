// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

// Package fleet aggregates several GPU Lens instances into one view. A hub instance polls the read-only
// GET /api/v1/live/summary endpoint of each peer (one GPU Lens per cluster or GPU host) and reports their health
// side by side. Peers keep doing their own collection, rules and alerting; the hub only reads their summaries.
package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/live"
	"github.com/Sievehouse/gpu-lens/internal/source"
)

// Peer is one remote GPU Lens instance.
type Peer struct {
	Name string
	URL  string // base URL, e.g. https://gpulens.cluster-a.example.com (may embed user:pass for basic auth)
}

var peerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// ParsePeers parses "name=url" items (comma-separated or repeated).
func ParsePeers(items []string) ([]Peer, error) {
	var out []Peer
	seen := map[string]bool{}
	for _, raw := range items {
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			name, rawURL, ok := strings.Cut(item, "=")
			name, rawURL = strings.TrimSpace(name), strings.TrimSpace(rawURL)
			if !ok || !peerName.MatchString(name) {
				return nil, fmt.Errorf("fleet: peer %q must look like name=https://host:port", item)
			}
			u, err := url.Parse(rawURL)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return nil, fmt.Errorf("fleet: peer %s has an invalid URL", name)
			}
			if seen[name] {
				return nil, fmt.Errorf("fleet: duplicate peer name %s", name)
			}
			seen[name] = true
			u.Path = strings.TrimRight(u.Path, "/")
			out = append(out, Peer{Name: name, URL: u.String()})
		}
	}
	return out, nil
}

// Redact hides credentials embedded in a peer URL.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return u.String()
}

// Member is one entry of the fleet view.
type Member struct {
	Name      string        `json:"name"`
	Kind      string        `json:"kind"` // local | peer
	URL       string        `json:"url"`
	Reachable bool          `json:"reachable"`
	Error     string        `json:"error"`
	LastOKAt  *time.Time    `json:"last_ok_at"`
	LatencyMs float64       `json:"latency_ms"`
	Summary   *live.Summary `json:"summary"`
}

// Totals add up the reachable members.
type Totals struct {
	Members          int `json:"members"`
	Reachable        int `json:"reachable"`
	GPUsTotal        int `json:"gpus_total"`
	GPUsFresh        int `json:"gpus_fresh"`
	Critical         int `json:"critical"`
	Warning          int `json:"warning"`
	Info             int `json:"info"`
	Unhealthy        int `json:"unhealthy"`
	SourcesConnected int `json:"sources_connected"`
}

// View is the payload of GET /api/v1/fleet.
type View struct {
	Enabled         bool      `json:"enabled"`
	IntervalSeconds float64   `json:"interval_seconds"`
	Members         []Member  `json:"members"`
	Totals          Totals    `json:"totals"`
	GeneratedAt     time.Time `json:"generated_at"`
	Note            string    `json:"note"`
}

// Hub polls peers in the background.
type Hub struct {
	local    *live.Engine
	peers    []Peer
	interval time.Duration
	client   *http.Client

	mu    sync.Mutex
	state map[string]*Member
}

// NewHub builds a hub. With no peers it still reports the local instance, and Enabled is false.
func NewHub(local *live.Engine, peers []Peer, interval time.Duration, timeout time.Duration) (*Hub, error) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	base, err := source.NewBaseTransport("")
	if err != nil {
		return nil, err
	}
	h := &Hub{
		local: local, peers: peers, interval: interval, state: map[string]*Member{},
		client: &http.Client{
			Timeout:   timeout,
			Transport: source.NewReadOnlyRoundTripper(base), // GET only, like every other source
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	for _, p := range peers {
		h.state[p.Name] = &Member{Name: p.Name, Kind: "peer", URL: Redact(p.URL), Error: "not polled yet"}
	}
	return h, nil
}

// Enabled reports whether any peer is configured.
func (h *Hub) Enabled() bool { return h != nil && len(h.peers) > 0 }

// Run polls every peer until ctx is done.
func (h *Hub) Run(ctx context.Context) {
	if !h.Enabled() {
		return
	}
	h.PollOnce(ctx)
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.PollOnce(ctx)
		}
	}
}

// PollOnce polls all peers concurrently.
func (h *Hub) PollOnce(ctx context.Context) {
	var wg sync.WaitGroup
	for _, p := range h.peers {
		wg.Add(1)
		go func(p Peer) {
			defer wg.Done()
			h.poll(ctx, p)
		}(p)
	}
	wg.Wait()
}

func (h *Hub) poll(ctx context.Context, p Peer) {
	start := time.Now()
	sum, err := h.fetch(ctx, p)
	lat := float64(time.Since(start).Microseconds()) / 1000
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.state[p.Name]
	m.LatencyMs = lat
	if err != nil {
		m.Reachable, m.Error = false, err.Error()
		return
	}
	now := time.Now().UTC()
	m.Reachable, m.Error, m.LastOKAt, m.Summary = true, "", &now, sum
}

func (h *Hub) fetch(ctx context.Context, p Peer) (*live.Summary, error) {
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	user := u.User
	u.User = nil
	u.Path += "/api/v1/live/summary"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	if user != nil {
		pass, _ := user.Password()
		req.SetBasicAuth(user.Username(), pass)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gpu-lens-fleet/"+live.Version)
	resp, err := h.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			if ue.Timeout() {
				return nil, errors.New("timed out")
			}
			return nil, fmt.Errorf("unreachable: %v", ue.Err)
		}
		return nil, errors.New("unreachable")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, errors.New("could not read the response")
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("HTTP 401: the peer requires credentials (put user:password in the peer URL)")
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("HTTP 404: the peer is an older GPU Lens without the fleet API, or its live console is disabled")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var s live.Summary
	if err := json.Unmarshal(body, &s); err != nil || s.Schema != live.SummarySchema {
		return nil, errors.New("the response is not a GPU Lens summary")
	}
	return &s, nil
}

// View returns the local instance followed by every peer, with totals.
func (h *Hub) View() View {
	v := View{Enabled: h.Enabled(), IntervalSeconds: h.interval.Seconds(), GeneratedAt: time.Now().UTC(), Members: []Member{}}
	if h.local != nil {
		s := h.local.Summary()
		now := time.Now().UTC()
		v.Members = append(v.Members, Member{Name: s.Cluster, Kind: "local", URL: "(this instance)", Reachable: true, LastOKAt: &now, Summary: &s})
	}
	h.mu.Lock()
	names := make([]string, 0, len(h.state))
	for n := range h.state {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m := *h.state[n]
		v.Members = append(v.Members, m)
	}
	h.mu.Unlock()
	for _, m := range v.Members {
		v.Totals.Members++
		if !m.Reachable || m.Summary == nil {
			v.Totals.Unhealthy++
			continue
		}
		s := m.Summary
		v.Totals.Reachable++
		v.Totals.GPUsTotal += s.GPUsTotal
		v.Totals.GPUsFresh += s.GPUsFresh
		v.Totals.Critical += s.Incidents["critical"]
		v.Totals.Warning += s.Incidents["warning"]
		v.Totals.Info += s.Incidents["info"]
		v.Totals.SourcesConnected += s.SourcesConnected
		if s.Incidents["critical"] > 0 || s.Verdict == "blocked" {
			v.Totals.Unhealthy++
		}
	}
	if v.Enabled {
		v.Note = "Each peer is a separate GPU Lens instance that runs its own collection, rules and alerts. The hub reads their summaries every " + fmt.Sprintf("%gs", v.IntervalSeconds) + " (read-only GET)."
	} else {
		v.Note = "Add other GPU Lens instances with -peer name=https://host:8080 (repeatable) or GPULENS_PEERS to see every cluster here."
	}
	return v
}
