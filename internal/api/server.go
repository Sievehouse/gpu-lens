// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/audit"
	"github.com/Sievehouse/gpu-lens/internal/capacity"
	"github.com/Sievehouse/gpu-lens/internal/config"
	"github.com/Sievehouse/gpu-lens/internal/domain"
	"github.com/Sievehouse/gpu-lens/internal/fleet"
	"github.com/Sievehouse/gpu-lens/internal/live"
	"github.com/Sievehouse/gpu-lens/internal/rules"
	"github.com/Sievehouse/gpu-lens/internal/source"
	"github.com/Sievehouse/gpu-lens/internal/store"
)

// Server wires HTTP routes to the normalized collector, deterministic rule engine, capacity calculator, and store.
type Server struct {
	mu            sync.RWMutex
	cfg           config.Config
	fixtureSource *source.FixtureSource
	engine        *rules.Engine
	incidentStore *store.IncidentStore
	logger        *slog.Logger
	mux           *http.ServeMux
	// live is the optional live telemetry engine behind /api/v1/live/* (see live_handlers.go).
	live *live.Engine

	fleet     *fleet.Hub
	auth      AuthConfig
	audit     *audit.Log
	startedAt time.Time
	httpStats httpCounters
}

// NewServer constructs a configured HTTP API and static UI server.
func NewServer(cfg config.Config, fixtureSource *source.FixtureSource, engine *rules.Engine, incidentStore *store.IncidentStore, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	s := &Server{
		cfg:           cfg,
		fixtureSource: fixtureSource,
		engine:        engine,
		incidentStore: incidentStore,
		logger:        logger,
		mux:           http.NewServeMux(),
		startedAt:     time.Now(),
	}
	s.registerRoutes()
	return s
}

// SetAuth turns on basic authentication with admin and (optional) viewer accounts. Call before serving.
func (s *Server) SetAuth(a AuthConfig) { s.auth = a }

// SetAudit attaches the audit log. Without one, audited actions are not recorded.
func (s *Server) SetAudit(l *audit.Log) { s.audit = l }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() { s.httpStats.add(rec.status) }()
	w = rec
	// Security headers for every response: no framing (clickjacking), no MIME sniffing, no referrer leakage.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	var ok bool
	if r, ok = s.authenticate(w, r); !ok {
		return
	}
	s.serve(w, r)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	// The live API exposes real host and cluster data and accepts state-changing requests, so it is
	// same-origin only: no CORS headers are sent and cross-origin preflights are refused.
	if isLivePath(r.URL.Path) || r.URL.Path == "/api/v1/audit" || r.URL.Path == "/api/v1/fleet" || r.URL.Path == "/metrics" {
		if r.Method == http.MethodOptions {
			writeError(w, http.StatusForbidden, "live API refused: cross-origin requests are not allowed")
			return
		}
		s.mux.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/readyz", s.handleReadyz)
	s.mux.HandleFunc("/api/v1/overview", s.handleOverview)
	s.mux.HandleFunc("/api/v1/incidents", s.handleIncidents)
	s.mux.HandleFunc("/api/v1/incidents/", s.handleIncidentDetail)
	s.mux.HandleFunc("/api/v1/capacity", s.handleCapacity)
	s.mux.HandleFunc("/api/v1/sources", s.handleSources)
	s.mux.HandleFunc("/api/v1/rules", s.handleRules)
	s.mux.HandleFunc("/api/v1/scenarios", s.handleScenarios)
	s.mux.HandleFunc("/api/v1/scenarios/select", s.handleSelectScenario)
	s.mux.HandleFunc("/api/v1/audit", s.handleAudit)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
	s.mux.HandleFunc("/", s.handleStaticUI)
}

func (s *Server) evaluateActive(r *http.Request) (*domain.ClusterSnapshot, []domain.Incident, domain.CapacityReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if qScenario := strings.TrimSpace(r.URL.Query().Get("scenario")); qScenario != "" && s.fixtureSource != nil {
		before := s.activeScenarioID()
		if err := s.fixtureSource.SelectScenario(qScenario); err == nil && before != qScenario {
			s.record(r, "simulation.scenario.select", before+" → "+qScenario, audit.OutcomeOK)
		}
	}

	res, err := s.fixtureSource.Collect(r.Context())
	if err != nil {
		return nil, nil, domain.CapacityReport{}, err
	}
	snap := res.Snapshot
	evaluated := s.engine.Evaluate(snap, s.cfg.Thresholds)
	persisted := s.incidentStore.UpsertBatch(snap.ScenarioID, evaluated)
	capReport := capacity.BuildReport(snap, s.cfg.Thresholds)
	return snap, persisted, capReport, nil
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "ok",
		"service":   "gpu-lens",
		"timestamp": time.Now().UTC(),
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if s.fixtureSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "no telemetry source initialized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "ready",
		"mode":      s.cfg.Source.Mode,
		"timestamp": time.Now().UTC(),
	})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	snap, incidents, capReport, err := s.evaluateActive(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	counts := map[string]int{
		"critical":     0,
		"warning":      0,
		"info":         0,
		"inconclusive": 0,
		"total":        len(incidents),
	}
	hasBlindSpot := false
	for _, inc := range incidents {
		counts[string(inc.Severity)]++
		if inc.EvalStatus == domain.EvalStatusInconclusive {
			counts["inconclusive"]++
		}
		if inc.RuleID == "GPU-FRESH-005" {
			hasBlindSpot = true
		}
	}

	fleetStatus := "healthy"
	headline := "All monitored GPUs, nodes, and telemetry streams are operating within healthy envelopes."
	if hasBlindSpot {
		fleetStatus = "telemetry_blind_spot"
		headline = "Monitoring blind spot detected: stale, unavailable, or uncorrelated GPU telemetry. Missing data is never treated as healthy."
	} else if counts["critical"] > 0 {
		fleetStatus = "critical"
		headline = "Critical GPU hardware, scheduling, or workload failure findings require operator investigation."
	} else if counts["warning"] > 0 {
		fleetStatus = "warning"
		headline = "Active GPU scheduling, thermal, or utilization warnings detected."
	}

	resp := domain.OverviewResponse{
		ClusterName:          snap.ClusterName,
		ScenarioID:           snap.ScenarioID,
		ScenarioName:         snap.ScenarioName,
		ScenarioDescription:  snap.ScenarioDescription,
		DataMode:             snap.DataMode,
		CollectedAt:          snap.CollectedAt,
		FleetHealthStatus:    fleetStatus,
		FleetHealthHeadline:  headline,
		ActiveIncidentCounts: counts,
		CapacitySummary:      capReport,
		ActiveIncidents:      incidents,
		Sources:              snap.Sources,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	snap, _, _, err := s.evaluateActive(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sev := strings.TrimSpace(r.URL.Query().Get("severity"))
	cat := strings.TrimSpace(r.URL.Query().Get("category"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	items := s.incidentStore.List(snap.ScenarioID, sev, cat, q)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"scenario_id": snap.ScenarioID,
		"data_mode":   snap.DataMode,
		"count":       len(items),
		"incidents":   items,
	})
}

func (s *Server) handleIncidentDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/incidents/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing incident id")
		return
	}
	snap, _, _, err := s.evaluateActive(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	inc, ok := s.incidentStore.GetByID(snap.ScenarioID, id)
	if !ok {
		writeError(w, http.StatusNotFound, "incident not found in active scenario")
		return
	}
	writeJSON(w, http.StatusOK, inc)
}

func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_, _, capReport, err := s.evaluateActive(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, capReport)
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	snap, _, _, err := s.evaluateActive(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	redactedCfg := s.cfg.RedactedCopy()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data_mode":        snap.DataMode,
		"scenario_id":      snap.ScenarioID,
		"collected_at":     snap.CollectedAt,
		"sources":          snap.Sources,
		"exported_metrics": snap.ExportedMetrics,
		"metric_mappings":  redactedCfg.MetricMappings,
	})
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	defs := s.engine.Definitions(s.cfg.Thresholds)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count": len(defs),
		"rules": defs,
	})
}

func (s *Server) handleScenarios(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.mu.RLock()
	list := s.fixtureSource.ListScenarios()
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mode":      s.cfg.Source.Mode,
		"scenarios": list,
	})
}

func (s *Server) handleSelectScenario(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		ScenarioID string `json:"scenario_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	target := strings.TrimSpace(body.ScenarioID)
	s.mu.Lock()
	before := s.activeScenarioID()
	err := s.fixtureSource.SelectScenario(target)
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if before != target {
		s.record(r, "simulation.scenario.select", before+" → "+target, audit.OutcomeOK)
	}
	s.handleOverview(w, &http.Request{Method: http.MethodGet, URL: r.URL})
}

func (s *Server) handleStaticUI(w http.ResponseWriter, r *http.Request) {
	if isLivePath(r.URL.Path) {
		// An API path that no handler claims is an API error, never the single-page app.
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if s.cfg.StaticDir == "" {
		writeJSON(w, http.StatusOK, map[string]string{"service": "gpu-lens-api"})
		return
	}
	// Always revalidate so a rebuilt or upgraded bundle is picked up without a hard refresh.
	w.Header().Set("Cache-Control", "no-cache")
	cleanPath := filepath.Clean(r.URL.Path)
	if cleanPath == "/" || cleanPath == "." {
		http.ServeFile(w, r, filepath.Join(s.cfg.StaticDir, "index.html"))
		return
	}
	target := filepath.Join(s.cfg.StaticDir, cleanPath)
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		http.ServeFile(w, r, target)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.cfg.StaticDir, "index.html"))
}

// activeScenarioID returns the simulation scenario currently selected. Callers hold s.mu.
func (s *Server) activeScenarioID() string {
	if s.fixtureSource == nil {
		return ""
	}
	if snap, err := s.fixtureSource.ActiveScenario(); err == nil && snap != nil {
		return snap.ScenarioID
	}
	return ""
}

// handleAudit returns the audit log: who changed what, including refused attempts.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	entries := s.audit.List(since)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":      len(entries),
		"persistent": s.audit.Path() != "",
		"entries":    entries,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
