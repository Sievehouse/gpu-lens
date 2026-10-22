package capacity

import (
	"testing"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/domain"
)

func TestCapacityReportAndMissingMappingSafeguard(t *testing.T) {
	baseTime := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	started := baseTime.Add(-2 * time.Hour)
	th := config.DefaultRuleThresholds()

	t.Run("excludes unmapped GPUs from per-job efficiency claims and flags blind spot", func(t *testing.T) {
		snap := &domain.ClusterSnapshot{
			ScenarioID:  "unmapped-test",
			ClusterName: "prod-gpu",
			DataMode:    "simulated_fixture",
			CollectedAt: baseTime,
			Nodes: []domain.NodeState{
				{Name: "node-1", Ready: true, Schedulable: true, GPUCapacity: 4, GPUAllocatable: 3, GPUAllocated: 2},
			},
			GPUs: []domain.GPUDevice{
				{
					UUID:          "GPU-UNMAPPED-1",
					DeviceIndex:   0,
					NodeName:      "node-1",
					MappingStatus: domain.MappingMissingPodLabels,
					LastSampleAt:  baseTime.Add(-10 * time.Second),
					Latest:        domain.GPUMetricPoint{Timestamp: baseTime.Add(-10 * time.Second), GPUUtilPercent: 3.0},
					Samples: []domain.GPUMetricPoint{
						{Timestamp: baseTime.Add(-90 * time.Minute), GPUUtilPercent: 3.0},
						{Timestamp: baseTime.Add(-10 * time.Second), GPUUtilPercent: 3.0},
					},
				},
			},
			Pods: []domain.WorkloadPod{
				{
					Name:             "unmapped-pod",
					Namespace:        "ml",
					Phase:            "Running",
					NodeName:         "node-1",
					RequestedGPUs:    1,
					AssignedGPUUUIDs: []string{"GPU-UNMAPPED-1"},
					StartedAt:        &started,
				},
			},
		}

		rep := BuildReport(snap, th)
		if rep.UnavailableHardwareGPUs != 1 {
			t.Fatalf("expected 1 unavailable hardware GPU (4 capacity - 3 allocatable), got %d", rep.UnavailableHardwareGPUs)
		}
		if rep.PerJobAttributionComplete {
			t.Fatalf("expected PerJobAttributionComplete=false when pod labels are missing")
		}
		if len(rep.LowUtilizationWorkloads) != 0 {
			t.Fatalf("expected 0 low-util workload claims when GPU mapping is missing, got %d", len(rep.LowUtilizationWorkloads))
		}
		if rep.AttributionBlindSpotWarning == "" {
			t.Fatalf("expected explicit AttributionBlindSpotWarning")
		}
	})
}
