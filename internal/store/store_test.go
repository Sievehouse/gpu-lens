// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package store

import (
	"testing"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/domain"
)

func TestIncidentDeduplicationOnRepeatedEvaluations(t *testing.T) {
	st := NewIncidentStore("")
	t0 := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Minute)

	inc1 := domain.Incident{
		ID:          "inc-health-12345678",
		Fingerprint: "1234567890abcdef",
		RuleID:      "GPU-HEALTH-002",
		Severity:    domain.SeverityCritical,
		Title:       "GPU XID 79 on gpu-node-01",
		FirstSeenAt: t0,
		LastSeenAt:  t0,
	}

	batch1 := st.UpsertBatch("scenario-3", []domain.Incident{inc1})
	if len(batch1) != 1 || batch1[0].OccurrenceCount != 1 {
		t.Fatalf("expected occurrence_count=1 on first evaluation, got %+v", batch1)
	}

	inc2 := inc1
	inc2.FirstSeenAt = t1
	inc2.LastSeenAt = t1
	batch2 := st.UpsertBatch("scenario-3", []domain.Incident{inc2})
	if len(batch2) != 1 {
		t.Fatalf("expected 1 deduplicated incident, got %d", len(batch2))
	}
	if batch2[0].OccurrenceCount != 2 {
		t.Fatalf("expected occurrence_count=2 on second evaluation, got %d", batch2[0].OccurrenceCount)
	}
	if !batch2[0].FirstSeenAt.Equal(t0) {
		t.Fatalf("expected FirstSeenAt to remain %v, got %v", t0, batch2[0].FirstSeenAt)
	}
	if !batch2[0].LastSeenAt.Equal(t1) {
		t.Fatalf("expected LastSeenAt to update to %v, got %v", t1, batch2[0].LastSeenAt)
	}
}
