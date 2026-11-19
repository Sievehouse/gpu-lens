// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package store

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/domain"
)

// IncidentStore persists and deduplicates incidents by stable fingerprint across evaluation cycles.
type IncidentStore struct {
	mu          sync.RWMutex
	storagePath string
	byScenario  map[string]map[string]domain.Incident
}

// NewIncidentStore initializes an IncidentStore backed by an optional disk persistence file.
func NewIncidentStore(storagePath string) *IncidentStore {
	s := &IncidentStore{
		storagePath: storagePath,
		byScenario:  make(map[string]map[string]domain.Incident),
	}
	if storagePath != "" {
		_ = s.loadFromDisk()
	}
	return s
}

// UpsertBatch merges a batch of evaluated incidents for a scenario, preserving FirstSeenAt and incrementing OccurrenceCount on repeated evaluations.
func (s *IncidentStore) UpsertBatch(scenarioID string, evaluated []domain.Incident) []domain.Incident {
	s.mu.Lock()
	defer s.mu.Unlock()

	scenMap := make(map[string]domain.Incident)
	prevMap := s.byScenario[scenarioID]

	out := make([]domain.Incident, 0, len(evaluated))
	for _, inc := range evaluated {
		if prev, ok := prevMap[inc.Fingerprint]; ok {
			if !prev.FirstSeenAt.IsZero() && prev.FirstSeenAt.Before(inc.FirstSeenAt) {
				inc.FirstSeenAt = prev.FirstSeenAt
			}
			inc.OccurrenceCount = prev.OccurrenceCount + 1
			if inc.LastSeenAt.Before(prev.LastSeenAt) {
				inc.LastSeenAt = prev.LastSeenAt
			}
		} else if inc.OccurrenceCount < 1 {
			inc.OccurrenceCount = 1
		}
		scenMap[inc.Fingerprint] = inc
		out = append(out, inc)
	}
	s.byScenario[scenarioID] = scenMap
	if s.storagePath != "" {
		_ = s.saveToDiskLocked()
	}
	return out
}

// List returns all active incidents for a scenario, optionally filtered by severity, category, or search query.
func (s *IncidentStore) List(scenarioID, severityFilter, categoryFilter, query string) []domain.Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()

	scenMap := s.byScenario[scenarioID]
	var out []domain.Incident
	for _, inc := range scenMap {
		if severityFilter != "" && severityFilter != "all" && string(inc.Severity) != severityFilter {
			continue
		}
		if categoryFilter != "" && categoryFilter != "all" && inc.Category != categoryFilter {
			continue
		}
		if query != "" && !matchesQuery(inc, query) {
			continue
		}
		out = append(out, inc)
	}
	return out
}

// GetByID finds a specific incident by ID across the active scenario.
func (s *IncidentStore) GetByID(scenarioID, id string) (*domain.Incident, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, inc := range s.byScenario[scenarioID] {
		if inc.ID == id || inc.Fingerprint == id {
			cp := inc
			return &cp, true
		}
	}
	return nil, false
}

func matchesQuery(inc domain.Incident, q string) bool {
	qLower := toLowerASCII(q)
	if containsCI(inc.Title, qLower) || containsCI(inc.Summary, qLower) || containsCI(inc.RuleID, qLower) || containsCI(inc.ID, qLower) {
		return true
	}
	for _, r := range inc.AffectedResources {
		if containsCI(r, qLower) {
			return true
		}
	}
	return false
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

func containsCI(haystack, needleLower string) bool {
	h := toLowerASCII(haystack)
	if len(needleLower) == 0 {
		return true
	}
	for i := 0; i+len(needleLower) <= len(h); i++ {
		if h[i:i+len(needleLower)] == needleLower {
			return true
		}
	}
	return false
}

type persistedState struct {
	UpdatedAt  time.Time                             `json:"updated_at"`
	ByScenario map[string]map[string]domain.Incident `json:"by_scenario"`
}

func (s *IncidentStore) saveToDiskLocked() error {
	st := persistedState{
		UpdatedAt:  time.Now().UTC(),
		ByScenario: s.byScenario,
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.storagePath, raw, 0o600)
}

func (s *IncidentStore) loadFromDisk() error {
	raw, err := os.ReadFile(s.storagePath)
	if err != nil {
		return err
	}
	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		return err
	}
	if st.ByScenario != nil {
		s.byScenario = st.ByScenario
	}
	return nil
}
