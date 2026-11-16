package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gpulens/gpu-lens/internal/live"
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
	if s.live.AllowRemoteConfig() {
		return live.Access{ConfigMutable: true}
	}
	if remoteIsLoopback(r) && !forwardedByProxy(r) && hostHeaderIsLocal(r) {
		return live.Access{ConfigMutable: true}
	}
	return live.Access{
		ConfigMutable: false,
		Reason:        "Connection settings can only be changed from the machine running GPU Lens (open it at http://localhost there), or with GPULENS_* environment variables at startup. Start with -live-allow-remote-config to lift this.",
	}
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
	if err := s.live.Start(body.IntervalSeconds); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, live.ErrLiveDisabled) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	s.writeLiveState(w, r)
}

func (s *Server) handleLiveStop(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) {
		return
	}
	s.live.Stop("Stopped from the console.")
	s.writeLiveState(w, r)
}

func (s *Server) handleLiveRecheck(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) {
		return
	}
	s.live.Recheck()
	s.writeLiveState(w, r)
}

func (s *Server) handleLiveConfig(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) {
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
		writeError(w, http.StatusForbidden, access.Reason)
		return
	}
	if err := s.live.UpdateConfig(patch); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, live.ErrLiveDisabled) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	s.writeLiveState(w, r)
}

func (s *Server) handleLiveThresholds(w http.ResponseWriter, r *http.Request) {
	if !s.guardLive(w, r, http.MethodPost) {
		return
	}
	var body struct {
		Overrides map[string]float64 `json:"overrides"`
		Reset     bool               `json:"reset"`
	}
	if !decodeLiveBody(w, r, &body) {
		return
	}
	if err := s.live.SetThresholds(body.Overrides, body.Reset); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, live.ErrLiveDisabled) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	s.writeLiveState(w, r)
}
