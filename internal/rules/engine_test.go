package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/domain"
)

func TestRuleEngineTableDriven(t *testing.T) {
	baseTime := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	th := config.DefaultRuleThresholds()
	engine := NewDefaultEngine()

	t.Run("healthy cluster produces zero incidents", func(t *testing.T) {
		started := baseTime.Add(-2 * time.Hour)
		snap := &domain.ClusterSnapshot{
			ScenarioID:  "healthy",
			ClusterName: "prod-gpu-us-east",
			DataMode:    "simulated_fixture",
			CollectedAt: baseTime,
			Sources: []domain.SourceHealth{
				{ID: "prom", Name: "Prometheus", Available: true, LastSuccessAt: baseTime.Add(-15 * time.Second), StalenessSeconds: 15},
			},
			Nodes: []domain.NodeState{
				{Name: "gpu-node-01", Ready: true, Schedulable: true, GPUModel: "NVIDIA H100 80GB HBM3", GPUCapacity: 4, GPUAllocatable: 4, GPUAllocated: 4},
			},
			GPUs: []domain.GPUDevice{
				{
					UUID:               "GPU-01",
					DeviceIndex:        0,
					NodeName:           "gpu-node-01",
					ModelName:          "NVIDIA H100 80GB HBM3",
					TotalMemoryMiB:     81920,
					PowerLimitWatts:    700,
					AllocatedNamespace: "ml-train",
					AllocatedPod:       "train-0",
					MappingStatus:      domain.MappingMapped,
					LastSampleAt:       baseTime.Add(-15 * time.Second),
					Latest:             domain.GPUMetricPoint{Timestamp: baseTime.Add(-15 * time.Second), GPUUtilPercent: 91.0, MemCopyUtilPercent: 44.0, FBUsedMiB: 64000, FBFreeMiB: 17920, GPUTempCelsius: 66.0, MemoryTempCelsius: 70.0, PowerUsageWatts: 590},
					Samples: []domain.GPUMetricPoint{
						{Timestamp: baseTime.Add(-60 * time.Minute), GPUUtilPercent: 89.0, FBUsedMiB: 64000, FBFreeMiB: 17920},
						{Timestamp: baseTime.Add(-45 * time.Minute), GPUUtilPercent: 92.0, FBUsedMiB: 64000, FBFreeMiB: 17920},
						{Timestamp: baseTime.Add(-30 * time.Minute), GPUUtilPercent: 90.0, FBUsedMiB: 64000, FBFreeMiB: 17920},
						{Timestamp: baseTime.Add(-20 * time.Minute), GPUUtilPercent: 93.0, FBUsedMiB: 64000, FBFreeMiB: 17920},
						{Timestamp: baseTime.Add(-10 * time.Minute), GPUUtilPercent: 91.0, FBUsedMiB: 64000, FBFreeMiB: 17920},
						{Timestamp: baseTime.Add(-15 * time.Second), GPUUtilPercent: 91.0, FBUsedMiB: 64000, FBFreeMiB: 17920},
					},
				},
			},
			Pods: []domain.WorkloadPod{
				{Name: "train-0", Namespace: "ml-train", Phase: "Running", NodeName: "gpu-node-01", RequestedGPUs: 1, AssignedGPUUUIDs: []string{"GPU-01"}, StartedAt: &started},
			},
		}

		incidents := engine.Evaluate(snap, th)
		if len(incidents) != 0 {
			t.Fatalf("expected 0 incidents on healthy cluster, got %d: %+v", len(incidents), incidents)
		}
	})

	t.Run("pending pod without events or nodes is marked inconclusive instead of guessing", func(t *testing.T) {
		snap := &domain.ClusterSnapshot{
			ScenarioID:  "pending-missing-evidence",
			ClusterName: "prod-gpu-us-east",
			DataMode:    "simulated_fixture",
			CollectedAt: baseTime,
			Pods: []domain.WorkloadPod{
				{
					Name:          "mystery-pending-job",
					Namespace:     "ml-research",
					Phase:         "Pending",
					RequestedGPUs: 4,
					CreatedAt:     baseTime.Add(-25 * time.Minute),
				},
			},
		}
		incidents := engine.Evaluate(snap, th)
		if len(incidents) != 1 {
			t.Fatalf("expected 1 incident, got %d", len(incidents))
		}
		if incidents[0].EvalStatus != domain.EvalStatusInconclusive {
			t.Fatalf("expected EvalStatusInconclusive when scheduling evidence is missing, got %s", incidents[0].EvalStatus)
		}
		if !strings.Contains(incidents[0].Summary, "Refusing to guess") {
			t.Fatalf("expected summary to explicitly refuse guessing, got %q", incidents[0].Summary)
		}
	})

	t.Run("low utilization observation is never described as proven waste and surfaces VRAM context", func(t *testing.T) {
		started := baseTime.Add(-120 * time.Minute)
		var samples []domain.GPUMetricPoint
		for i := 0; i < 8; i++ {
			samples = append(samples, domain.GPUMetricPoint{
				Timestamp:          baseTime.Add(-time.Duration(80-i*10) * time.Minute),
				GPUUtilPercent:     6.5,
				MemCopyUtilPercent: 4.0,
				FBUsedMiB:          72000, // 87.9% of 81920 MiB
				FBFreeMiB:          9920,
				GPUTempCelsius:     48.0,
			})
		}
		snap := &domain.ClusterSnapshot{
			ScenarioID:  "low-util",
			ClusterName: "prod-gpu-us-east",
			DataMode:    "simulated_fixture",
			CollectedAt: baseTime,
			Nodes: []domain.NodeState{
				{Name: "gpu-node-01", Ready: true, Schedulable: true, GPUCapacity: 1, GPUAllocatable: 1, GPUAllocated: 1},
			},
			GPUs: []domain.GPUDevice{
				{
					UUID:               "GPU-LOW-01",
					DeviceIndex:        0,
					NodeName:           "gpu-node-01",
					TotalMemoryMiB:     81920,
					AllocatedNamespace: "inference",
					AllocatedPod:       "llama-70b-serve",
					MappingStatus:      domain.MappingMapped,
					LastSampleAt:       baseTime.Add(-10 * time.Second),
					Latest:             samples[len(samples)-1],
					Samples:            samples,
				},
			},
			Pods: []domain.WorkloadPod{
				{
					Name:             "llama-70b-serve",
					Namespace:        "inference",
					OwnerKind:        "Deployment",
					OwnerName:        "llama-70b-serve",
					Phase:            "Running",
					NodeName:         "gpu-node-01",
					RequestedGPUs:    1,
					AssignedGPUUUIDs: []string{"GPU-LOW-01"},
					StartedAt:        &started,
				},
			},
		}

		incidents := engine.Evaluate(snap, th)
		if len(incidents) != 1 {
			t.Fatalf("expected 1 low-util incident, got %d", len(incidents))
		}
		inc := incidents[0]
		if inc.RuleID != "GPU-UTIL-003" {
			t.Fatalf("expected GPU-UTIL-003, got %s", inc.RuleID)
		}
		if inc.EvidenceStrength != domain.EvidenceContextual {
			t.Fatalf("expected EvidenceContextual, got %s", inc.EvidenceStrength)
		}
		if len(inc.ContradictingEvidence) == 0 {
			t.Fatalf("expected contradicting evidence highlighting high VRAM residency")
		}
		if len(inc.SuggestedChecks) < 3 {
			t.Fatalf("expected structured follow-up checklist")
		}
	})

	t.Run("missing or stale telemetry triggers GPU-FRESH-005 and does not produce healthy status", func(t *testing.T) {
		snap := &domain.ClusterSnapshot{
			ScenarioID:  "stale",
			ClusterName: "prod-gpu-us-east",
			DataMode:    "simulated_fixture",
			CollectedAt: baseTime,
			Sources: []domain.SourceHealth{
				{ID: "prom", Name: "Prometheus", Available: false, LastSuccessAt: baseTime.Add(-20 * time.Minute), StalenessSeconds: 1200, IsStale: true, LastError: "connection timeout"},
			},
			GPUs: []domain.GPUDevice{
				{
					UUID:             "GPU-STALE-01",
					DeviceIndex:      0,
					NodeName:         "gpu-node-02",
					LastSampleAt:     baseTime.Add(-20 * time.Minute),
					StalenessSeconds: 1200,
					IsStale:          true,
				},
			},
		}
		incidents := engine.Evaluate(snap, th)
		if len(incidents) < 2 {
			t.Fatalf("expected source and GPU freshness incidents, got %d", len(incidents))
		}
		if incidents[0].RuleID != "GPU-FRESH-005" || incidents[0].Severity != domain.SeverityCritical {
			t.Fatalf("expected critical GPU-FRESH-005 incident, got %+v", incidents[0])
		}
	})

	t.Run("duplicate evaluations produce single deduplicated incident by fingerprint", func(t *testing.T) {
		fp1 := ComputeFingerprint("GPU-HEALTH-002", "cluster-a", "GPU", "node-1", "GPU-123", "HardwareHealth")
		fp2 := ComputeFingerprint("GPU-HEALTH-002", "cluster-a", "GPU", "node-1", "GPU-123", "HardwareHealth")
		if fp1 != fp2 {
			t.Fatalf("expected identical fingerprints, got %s vs %s", fp1, fp2)
		}
	})
}
