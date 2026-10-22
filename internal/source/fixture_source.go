package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/gpulens/gpu-lens/internal/domain"
)

// ClusterSnapshotAlias aliases domain.ClusterSnapshot for package convenience.
type ClusterSnapshotAlias = domain.ClusterSnapshot

// ScenarioSummary describes an available deterministic offline demo scenario.
type ScenarioSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
}

// FixtureSource loads deterministic, version-controlled cluster scenarios from local JSON fixtures.
// It requires zero external API keys, zero network access, and zero GPU hardware.
type FixtureSource struct {
	fixturesDir string
	scenarios   map[string]*domain.ClusterSnapshot
	order       []string
	activeID    string
}

// NewFixtureSource loads all scenario JSON files from fixturesDir.
func NewFixtureSource(fixturesDir, defaultScenarioID string) (*FixtureSource, error) {
	fs := &FixtureSource{
		fixturesDir: fixturesDir,
		scenarios:   make(map[string]*domain.ClusterSnapshot),
	}
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		return nil, fmt.Errorf("fixture source: read dir %q: %w", fixturesDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || entry.Name() == "scenarios_index.json" {
			continue
		}
		fullPath := filepath.Join(fixturesDir, entry.Name())
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("fixture source: read %q: %w", fullPath, err)
		}
		var snap domain.ClusterSnapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return nil, fmt.Errorf("fixture source: unmarshal %q: %w", fullPath, err)
		}
		if snap.ScenarioID == "" {
			continue
		}
		fs.scenarios[snap.ScenarioID] = &snap
		fs.order = append(fs.order, snap.ScenarioID)
	}
	sort.Strings(fs.order)
	if len(fs.order) == 0 {
		return nil, fmt.Errorf("fixture source: no scenario JSON files found in %q", fixturesDir)
	}
	if _, ok := fs.scenarios[defaultScenarioID]; ok {
		fs.activeID = defaultScenarioID
	} else {
		fs.activeID = fs.order[0]
	}
	return fs, nil
}

func (fs *FixtureSource) ID() string   { return "offline-fixture-source" }
func (fs *FixtureSource) Name() string { return "Bundled Deterministic Offline Fixtures" }

// ListScenarios returns metadata for all loaded deterministic scenarios.
func (fs *FixtureSource) ListScenarios() []ScenarioSummary {
	out := make([]ScenarioSummary, 0, len(fs.order))
	for _, id := range fs.order {
		s := fs.scenarios[id]
		out = append(out, ScenarioSummary{
			ID:          s.ScenarioID,
			Name:        s.ScenarioName,
			Description: s.ScenarioDescription,
			Active:      id == fs.activeID,
		})
	}
	return out
}

// SelectScenario switches the active deterministic fixture scenario.
func (fs *FixtureSource) SelectScenario(scenarioID string) error {
	if _, ok := fs.scenarios[scenarioID]; !ok {
		return fmt.Errorf("unknown scenario_id %q", scenarioID)
	}
	fs.activeID = scenarioID
	return nil
}

// ActiveScenario wraps the current ClusterSnapshot.
func (fs *FixtureSource) ActiveScenario() (*domain.ClusterSnapshot, error) {
	snap, ok := fs.scenarios[fs.activeID]
	if !ok {
		return nil, fmt.Errorf("active scenario %q not found", fs.activeID)
	}
	return snap, nil
}

// Collect implements TelemetryCollector for offline fixture mode.
func (fs *FixtureSource) Collect(_ context.Context) (*CollectorResult, error) {
	snap, err := fs.ActiveScenario()
	if err != nil {
		return nil, err
	}
	return &CollectorResult{Snapshot: snap}, nil
}
