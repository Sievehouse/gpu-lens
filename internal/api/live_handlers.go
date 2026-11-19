// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Sievehouse/gpu-lens/internal/audit"
	"github.com/Sievehouse/gpu-lens/internal/fleet"
	"github.com/Sievehouse/gpu-lens/internal/live"
)

const livePathPrefix = "/api/v1/live/"

// SetLiveEngine attaches the live telemetry engine and registers its routes.
func (s *Server) SetLiveEngine(engine *live.Engine) {
	s.live = engine
	s.mux.HandleFunc("/api/v1/live/status", s.handleLiveStatus)
	s.mux.HandleFunc("/api/v1/live/state", s.handleLiveState)
	s.mux.HandleFunc("/api/v1/live/start", s.handleLiveStart)
	s.mux.HandleFunc("/api/v1/live/stop", s.handleLiveStop)
	s.mux.HandleFunc("/api/v1/live/recheck", s.handleLiveRecheck)
	s.mux.HandleFunc("/api/v1/live/config", s.handleLiveConfig)
	s.mux.HandleFunc("/api/v1/live/thresholds", s.handleLiveThresholds)
	s.mux.HandleFunc("/api/v1/live/alerts", s.handleLiveAlerts)
	s.mux.HandleFunc("/api/v1/live/alerts/test", s.handleLiveAlertsTest)
	s.mux.HandleFunc("/api/v1/live/summary", s.handleLiveSummary)
}

// SetFleet attaches the multi-cluster hub behind GET /api/v1/fleet.
func (s *Server) SetFleet(h *fleet.Hub) {
	s.fleet = h
	s.mux.HandleFunc("/api/v1/fleet", s.handleFleet)
}

func (s *Server) handleLiveSummary(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.live.Summary())
}

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "fleet API refused: cross-site requests are not allowed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.fleet.View())
}

func isLivePath(path string) bool {
	return strings.HasPrefix(path, livePathPrefix)
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

func remoteIsLoopback(r *http.Request) bool {
	ip := net.ParseIP(hostOnly(r.RemoteAddr))
	return ip != nil && ip.IsLoopback()
}

// forwardedByProxy reports whether the request passed through a reverse proxy, in which case the socket peer is
// the proxy and says nothing about where the caller really is.
func forwardedByProxy(r *http.Request) bool {
	return r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Real-IP") != ""
}

// hostHeaderIsLocal accepts only names that can legitimately address a server on this machine. A local browser
// that was tricked into reaching GPU Lens through an attacker-controlled DNS name (DNS rebinding) sends that name
// in the Host header and is refused.
func hostHeaderIsLocal(r *http.Request) bool {
	host := strings.ToLower(hostOnly(r.Host))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	if name, err := os.Hostname(); err == nil && name != "" && strings.EqualFold(host, name) {
		return true
	}
	return false
}

// sameOrigin rejects cross-site browser requests. Non-browser clients (curl, scripts) send neither header.
func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

// guardLive applies the checks shared by every live endpoint and reports whether the request may proceed.
func (s *Server) guardLive(w http.ResponseWriter, r *http.Request, method string) bool {
	if s.live == nil {
		writeError(w, http.StatusNotFound, "live console is not available")
		return false
	}
	if r.Method != method {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	if remoteIsLoopback(r) && !forwardedByProxy(r) && !hostHeaderIsLocal(r) {
		writeError(w, http.StatusForbidden, "live API refused: unexpected Host header for a local connection")
		return false
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "live API refused: cross-site requests are not allowed")
		return false
	}
	if method == http.MethodPost {
		ct := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
		if ct != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "live API requires Content-Type: application/json")
			return false
		}
	}
	return true
}

// liveAccess decides whether this caller may change connection settings (endpoints and tokens).
func (s *Server) liveAccess(r *http.Request) live.Access {
	p := principalOf(r)
	base := live.Access{Role: p.role, AuthEnabled: s.auth.Enabled(), CanOperate: p.role == RoleAdmin}
	if s.auth.Enabled() {
		base.User = p.user
	}
	if p.role != RoleAdmin {
		base.Reason = "You are signed in with the viewer role, which is read-only. Sign in as an admin to change connections, thresholds or collection."
		return base
	}
	if s.live.AllowRemoteConfig() || (remoteIsLoopback(r) && !forwardedByProxy(r) && hostHeaderIsLocal(r)) {
		base.ConfigMutable = true
		return base
	}
	base.Reason = "Connection settings can only be changed from the machine running GPU Lens (open it at http://localhost there), or with GPULENS_* environment variables at startup. Start with -live-allow-remote-config to lift this."
	return base
}

// liveDone records a successful live change in the audit log and the console activity log, and persists state.
func (s *Server) liveDone(r *http.Request, action, title, detail string) {
	e := s.record(r, action, detail, audit.OutcomeOK)
	who := e.Actor
	if e.Role != "" {
		who += " (" + e.Role + ")"
	}
	s.live.AddAudit(title, detail+" — by "+who+" from "+e.Remote+".")
	_ = s.live.SaveState()
}

func (s *Server) writeLiveState(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	activitySince, _ := strconv.Atoi(r.URL.Query().Get("activity_since"))
	if since < 0 {
		since = 0
	}
	if activitySince < 0 {
		activitySince = 0
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.live.State(since, activitySince, s.liveAccess(r)))
}

func decodeLiveBody(w http.ResponseWriter, r *http.Request, dest interface{}) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty body means "no options".
			return true
		}
		writeError(w, http.StatusBadRequest, "invalid JSON payload: "+sanitizeDecodeError(err))
		return false
	}
	return true
}

func sanitizeDecodeError(err error) string {
	msg := err.Error()
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return msg
}

func (s *Server) handleLiveStatus(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.live.Status(s.liveAccess(r)))
}

func (s *Server) handleLiveState(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodGet) {
		return
	}
	s.writeLiveState(w, r)
}

func (s *Server) handleLiveStart(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) {
		return
	}
	var body struct {
		IntervalSeconds float64 `json:"interval_seconds"`
	}
	if !decodeLiveBody(w, r, &body) {
		return
	}
	if body.IntervalSeconds > 0 && !s.requireAdmin(w, r, "live.start") {
		// Viewers may resume watching, but not change the refresh interval.
		return
	}
	wasRunning := s.live.Running()
	if err := s.live.Start(body.IntervalSeconds); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, live.ErrLiveDisabled) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	if !wasRunning {
		s.record(r, "live.start", "Live collection started.", audit.OutcomeOK)
	}
	s.writeLiveState(w, r)
}

func (s *Server) handleLiveStop(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) || !s.requireAdmin(w, r, "live.stop") {
		return
	}
	s.live.Stop("Stopped from the console.")
	s.liveDone(r, "live.stop", "Collection paused", "Live collection stopped from the console")
	s.writeLiveState(w, r)
}

func (s *Server) handleLiveRecheck(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) || !s.requireAdmin(w, r, "live.recheck") {
		return
	}
	s.live.Recheck()
	s.record(r, "live.recheck", "Immediate re-check of every source.", audit.OutcomeOK)
	s.writeLiveState(w, r)
}

func (s *Server) handleLiveConfig(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) || !s.requireAdmin(w, r, "live.config") {
		return
	}
	access := s.liveAccess(r)
	var patch live.ConfigPatch
	if !decodeLiveBody(w, r, &patch) {
		return
	}
	touchesConnections := patch.ClusterName != nil || patch.NvidiaSMIEnabled != nil || patch.HostVitals != nil ||
		patch.DCGM != nil || patch.Prometheus != nil || patch.Kubernetes != nil
	if touchesConnections && !access.ConfigMutable {
		s.record(r, "live.config", "refused: "+describePatch(patch), audit.OutcomeDenied)
		writeError(w, http.StatusForbidden, access.Reason)
		return
	}
	if err := s.live.UpdateConfig(patch); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, live.ErrLiveDisabled) {
			status = http.StatusConflict
		}
		s.record(r, "live.config", "rejected: "+describePatch(patch)+": "+err.Error(), audit.OutcomeError)
		writeError(w, status, err.Error())
		return
	}
	s.liveDone(r, "live.config", "Live configuration changed", describePatch(patch))
	s.writeLiveState(w, r)
}

// describePatch summarizes a configuration change for the audit log. Token values are never included.
func describePatch(p live.ConfigPatch) string {
	var parts []string
	if p.ClusterName != nil {
		parts = append(parts, fmt.Sprintf("cluster name=%q", *p.ClusterName))
	}
	if p.IntervalSeconds != nil {
		parts = append(parts, fmt.Sprintf("interval=%gs", *p.IntervalSeconds))
	}
	if p.NvidiaSMIEnabled != nil {
		parts = append(parts, fmt.Sprintf("nvidia-smi=%t", *p.NvidiaSMIEnabled))
	}
	if p.HostVitals != nil {
		parts = append(parts, fmt.Sprintf("host vitals=%t", *p.HostVitals))
	}
	for _, sp := range []struct {
		name string
		p    *live.SourcePatch
	}{{"DCGM", p.DCGM}, {"Prometheus", p.Prometheus}, {"Kubernetes", p.Kubernetes}} {
		if sp.p == nil {
			continue
		}
		var bits []string
		if sp.p.Enabled != nil {
			bits = append(bits, fmt.Sprintf("enabled=%t", *sp.p.Enabled))
		}
		if sp.p.Endpoint != nil {
			ep := strings.TrimSpace(*sp.p.Endpoint)
			if ep == "" {
				ep = "(default probe)"
			} else if u, err := url.Parse(ep); err == nil && u.User != nil {
				u.User = nil
				ep = u.String()
			}
			bits = append(bits, "endpoint="+ep)
		}
		if sp.p.BearerToken != nil && strings.TrimSpace(*sp.p.BearerToken) != "" {
			bits = append(bits, "token set")
		}
		if sp.p.ClearToken {
			bits = append(bits, "token cleared")
		}
		if len(bits) > 0 {
			parts = append(parts, sp.name+" ("+strings.Join(bits, ", ")+")")
		}
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, "; ")
}

func (s *Server) handleLiveThresholds(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) || !s.requireAdmin(w, r, "live.thresholds") {
		return
	}
	var body struct {
		Overrides map[string]float64 `json:"overrides"`
		Reset     bool               `json:"reset"`
	}
	if !decodeLiveBody(w, r, &body) {
		return
	}
	keys := make([]string, 0, len(body.Overrides))
	for k, v := range body.Overrides {
		keys = append(keys, fmt.Sprintf("%s=%g", k, v))
	}
	sort.Strings(keys)
	detail := "set " + strings.Join(keys, ", ")
	if body.Reset {
		detail = "reset all live thresholds to defaults"
		if len(keys) > 0 {
			detail += ", then set " + strings.Join(keys, ", ")
		}
	}
	if err := s.live.SetThresholds(body.Overrides, body.Reset); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, live.ErrLiveDisabled) {
			status = http.StatusConflict
		}
		s.record(r, "live.thresholds", "rejected: "+detail+": "+err.Error(), audit.OutcomeError)
		writeError(w, status, err.Error())
		return
	}
	s.liveDone(r, "live.thresholds", "Thresholds changed", detail)
	s.writeLiveState(w, r)
}

func (s *Server) handleLiveAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.live.AlertStatus())
}

func (s *Server) handleLiveAlertsTest(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) || !s.requireAdmin(w, r, "alerts.test") {
		return
	}
	res, err := s.live.TestAlerts()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	ok := 0
	for _, x := range res {
		if x.OK {
			ok++
		}
	}
	s.record(r, "alerts.test", fmt.Sprintf("test alert accepted by %d of %d destination(s)", ok, len(res)), audit.OutcomeOK)
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": res, "status": s.live.AlertStatus()})
}
