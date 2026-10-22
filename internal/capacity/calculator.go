package capacity

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/domain"
)

// BuildReport computes a transparent, reproducible GPU capacity and utilization report from a ClusterSnapshot.
// It explicitly separates node-level telemetry from pod-mapped telemetry and refuses to fabricate per-job efficiency
// when pod-to-GPU label attribution is missing.
func BuildReport(snapshot *domain.ClusterSnapshot, th config.RuleThresholds) domain.CapacityReport {
	now := time.Now().UTC()
	if snapshot != nil && !snapshot.CollectedAt.IsZero() {
		now = snapshot.CollectedAt
	}

	report := domain.CapacityReport{
		GeneratedAt:               now,
		PerJobAttributionComplete: true,
		Methodology: domain.CapacityMethodology{
			UtilThresholdPercent: th.LowUtilThresholdPercent,
			MinWindowMinutes:     th.LowUtilWindowMinutes,
			MinSamplesRequired:   th.MinUtilizationSamples,
			FormulaSummary:       "LowUtilGPUHours = RequestedGPUs * (ObservationWindowMinutes / 60.0) for Running pods where Mean(DCGM_FI_DEV_GPU_UTIL) < Threshold over Window >= MinWindowMinutes and Samples >= MinSamplesRequired.",
			StepByStepCalculation: []string{
				"1. Inventory physical GPU capacity (status.capacity['nvidia.com/gpu']) vs allocatable GPUs (status.allocatable['nvidia.com/gpu']) per Kubernetes Node.",
				"2. Compute UnavailableHardwareGPUs = max(0, CapacityGPUs - AllocatableGPUs) to surface devices de-allocated by the device plugin.",
				"3. Audit DCGM telemetry freshness (age <= max_staleness_seconds) and Kubernetes pod attribution labels (pod, namespace from kubelet PodResourcesLister).",
				"4. For each Running workload with complete pod-to-GPU mapping, observation window >= low_util_window_minutes, and sample count >= min_utilization_samples, compute mean DCGM_FI_DEV_GPU_UTIL, DCGM_FI_DEV_MEM_COPY_UTIL, and DCGM_FI_DEV_FB_USED.",
				"5. Integrate allocated GPU-hours observed below low_util_threshold_percent: AllocatedGPUs * (WindowMinutes / 60).",
			},
			NonWasteDisclaimer:   "Low GPU SM compute utilization (DCGM_FI_DEV_GPU_UTIL) does NOT prove wasted capacity. Workloads may be bound by CPU/storage data loading, collective NCCL synchronization barriers, checkpoint I/O, or GPU framebuffer memory residency (DCGM_FI_DEV_FB_USED).",
			MissingMappingPolicy: "When GPU-to-pod label mapping is missing on a GPU series, GPU Lens excludes that GPU from per-workload efficiency calculations and flags an explicit attribution blind spot.",
		},
	}

	if th.OptionalSampleGPUHourlyRateUSD != nil && *th.OptionalSampleGPUHourlyRateUSD > 0 {
		rate := *th.OptionalSampleGPUHourlyRateUSD
		report.Methodology.OptionalSampleHourlyRateUSD = &rate
		report.Methodology.OptionalCostEstimateLabel = fmt.Sprintf("Illustrative Estimate Only (using configured sample rate of $%.2f/GPU-hr; NOT actual cloud billing or realized savings)", rate)
	}

	if snapshot == nil {
		return report
	}

	report.ClusterName = snapshot.ClusterName
	report.ScenarioID = snapshot.ScenarioID
	report.DataMode = snapshot.DataMode

	gpusByNode := make(map[string][]domain.GPUDevice)
	gpuByUUID := make(map[string]domain.GPUDevice)
	for _, g := range snapshot.GPUs {
		gpusByNode[g.NodeName] = append(gpusByNode[g.NodeName], g)
		gpuByUUID[g.UUID] = g
		if g.IsStale {
			report.TelemetryStaleGPUs++
		} else {
			report.TelemetryFreshGPUs++
		}
		if g.MappingStatus == domain.MappingMapped {
			report.MappedAllocatedGPUs++
		} else if g.MappingStatus == domain.MappingMissingPodLabels {
			report.UnmappedAllocatedGPUs++
			report.PerJobAttributionComplete = false
		}
	}

	// Node breakdown
	for _, n := range snapshot.Nodes {
		unavail := n.GPUCapacity - n.GPUAllocatable
		if unavail < 0 {
			unavail = 0
		}
		free := n.GPUAllocatable - n.GPUAllocated
		if free < 0 {
			free = 0
		}
		report.TotalCapacityGPUs += n.GPUCapacity
		report.TotalAllocatableGPUs += n.GPUAllocatable
		report.UnavailableHardwareGPUs += unavail
		report.TotalAllocatedGPUs += n.GPUAllocated
		if n.Ready && n.Schedulable {
			report.TotalFreeAllocatableGPUs += free
		}

		nodeGPUs := gpusByNode[n.Name]
		freshCount := 0
		staleCount := 0
		unmappedCount := 0
		sumUtil := 0.0
		sumFBPct := 0.0
		for _, g := range nodeGPUs {
			if g.IsStale {
				staleCount++
				continue
			}
			freshCount++
			sumUtil += g.Latest.GPUUtilPercent
			totalMem := g.TotalMemoryMiB
			if totalMem <= 0 {
				totalMem = g.Latest.FBUsedMiB + g.Latest.FBFreeMiB
			}
			if totalMem > 0 {
				sumFBPct += (g.Latest.FBUsedMiB / totalMem) * 100.0
			}
			if g.MappingStatus == domain.MappingMissingPodLabels {
				unmappedCount++
			}
		}

		var meanUtilPtr, meanFBPtr *float64
		if freshCount > 0 {
			u := round2(sumUtil / float64(freshCount))
			f := round2(sumFBPct / float64(freshCount))
			meanUtilPtr = &u
			meanFBPtr = &f
		}

		statusNote := "Healthy & Schedulable"
		if !n.Ready {
			statusNote = "Node NotReady"
		} else if !n.Schedulable {
			statusNote = "Node Cordoned (Unschedulable)"
		} else if unavail > 0 {
			statusNote = fmt.Sprintf("Degraded Allocatable (%d GPU unavailable to scheduler)", unavail)
		} else if staleCount > 0 {
			statusNote = fmt.Sprintf("Telemetry Stale (%d/%d GPUs stale)", staleCount, len(nodeGPUs))
		} else if unmappedCount > 0 {
			statusNote = fmt.Sprintf("Missing Pod Attribution Labels (%d GPUs)", unmappedCount)
		}

		report.Nodes = append(report.Nodes, domain.NodeCapacityBreakdown{
			NodeName:        n.Name,
			GPUModel:        n.GPUModel,
			Schedulable:     n.Schedulable,
			Ready:           n.Ready,
			CapacityGPUs:    n.GPUCapacity,
			AllocatableGPUs: n.GPUAllocatable,
			UnavailableGPUs: unavail,
			AllocatedGPUs:   n.GPUAllocated,
			FreeAllocatable: free,
			MeanGPUUtil:     meanUtilPtr,
			MeanFBUsedPct:   meanFBPtr,
			FreshGPUs:       freshCount,
			StaleGPUs:       staleCount,
			UnmappedPodGPUs: unmappedCount,
			StatusNote:      statusNote,
		})
	}

	// Cluster-wide mean utilization across fresh GPUs
	if report.TelemetryFreshGPUs > 0 {
		sumU := 0.0
		sumFB := 0.0
		for _, g := range snapshot.GPUs {
			if g.IsStale {
				continue
			}
			sumU += g.Latest.GPUUtilPercent
			tot := g.TotalMemoryMiB
			if tot <= 0 {
				tot = g.Latest.FBUsedMiB + g.Latest.FBFreeMiB
			}
			if tot > 0 {
				sumFB += (g.Latest.FBUsedMiB / tot) * 100.0
			}
		}
		u := round2(sumU / float64(report.TelemetryFreshGPUs))
		fb := round2(sumFB / float64(report.TelemetryFreshGPUs))
		report.ClusterMeanGPUUtilPercent = &u
		report.ClusterMeanFBUsedPercent = &fb
	}

	// Pending workloads & Running workload utilization windows
	for _, pod := range snapshot.Pods {
		if pod.Phase == "Pending" && pod.RequestedGPUs > 0 {
			report.PendingRequestedGPUs += pod.RequestedGPUs
			reason := pod.SchedulerReason
			msg := pod.SchedulerMessage
			for _, ev := range snapshot.Events {
				if ev.InvolvedObjectKind == "Pod" && ev.InvolvedObjectName == pod.Name && ev.Reason == "FailedScheduling" {
					if reason == "" {
						reason = ev.Reason
					}
					if msg == "" {
						msg = ev.Message
					}
				}
			}
			evidenceComplete := reason != "" || msg != ""
			if !evidenceComplete {
				reason = "Inconclusive (No FailedScheduling Event Observed)"
				msg = "Scheduler event missing from snapshot; refusing to guess cause."
			}
			report.PendingWorkloads = append(report.PendingWorkloads, domain.PendingWorkloadSummary{
				Namespace:        pod.Namespace,
				PodName:          pod.Name,
				OwnerRef:         fmt.Sprintf("%s/%s", pod.OwnerKind, pod.OwnerName),
				RequestedGPUs:    pod.RequestedGPUs,
				PendingMinutes:   round2(now.Sub(pod.CreatedAt).Minutes()),
				ObservedReason:   reason,
				SchedulerMessage: msg,
				EvidenceComplete: evidenceComplete,
				CreatedAt:        pod.CreatedAt,
			})
			continue
		}

		if pod.Phase == "Running" && pod.RequestedGPUs > 0 && pod.StartedAt != nil {
			if len(pod.AssignedGPUUUIDs) == 0 {
				report.PerJobAttributionComplete = false
				continue
			}
			var mappedGPUs []domain.GPUDevice
			validMapping := true
			for _, uuid := range pod.AssignedGPUUUIDs {
				g, ok := gpuByUUID[uuid]
				if !ok || g.IsStale || g.MappingStatus != domain.MappingMapped || len(g.Samples) == 0 {
					validMapping = false
					break
				}
				mappedGPUs = append(mappedGPUs, g)
			}
			if !validMapping {
				report.PerJobAttributionComplete = false
				continue
			}

			var earliest, latest time.Time
			totalSamples := 0
			sumU := 0.0
			minU := 100.0
			maxU := 0.0
			sumMC := 0.0
			sumFB := 0.0
			sumFBPct := 0.0
			for _, g := range mappedGPUs {
				for _, s := range g.Samples {
					if earliest.IsZero() || s.Timestamp.Before(earliest) {
						earliest = s.Timestamp
					}
					if latest.IsZero() || s.Timestamp.After(latest) {
						latest = s.Timestamp
					}
					sumU += s.GPUUtilPercent
					if s.GPUUtilPercent < minU {
						minU = s.GPUUtilPercent
					}
					if s.GPUUtilPercent > maxU {
						maxU = s.GPUUtilPercent
					}
					sumMC += s.MemCopyUtilPercent
					sumFB += s.FBUsedMiB
					tot := g.TotalMemoryMiB
					if tot <= 0 {
						tot = s.FBUsedMiB + s.FBFreeMiB
					}
					if tot > 0 {
						sumFBPct += (s.FBUsedMiB / tot) * 100.0
					}
					totalSamples++
				}
			}

			windowMins := latest.Sub(earliest).Minutes()
			if windowMins <= 0 {
				windowMins = now.Sub(*pod.StartedAt).Minutes()
			}
			allocGPUHrs := round2(float64(pod.RequestedGPUs) * (windowMins / 60.0))
			report.TotalObservedAllocatedGPUHrs = round2(report.TotalObservedAllocatedGPUHrs + allocGPUHrs)

			meanU := round2(sumU / float64(totalSamples))
			meanMC := round2(sumMC / float64(totalSamples))
			meanFB := round2(sumFB / float64(totalSamples))
			meanFBPct := round2(sumFBPct / float64(totalSamples))

			if windowMins >= th.LowUtilWindowMinutes && totalSamples >= th.MinUtilizationSamples && meanU < th.LowUtilThresholdPercent {
				report.ObservedLowUtilGPUHours = round2(report.ObservedLowUtilGPUHours + allocGPUHrs)
				var sampleCostPtr *float64
				if th.OptionalSampleGPUHourlyRateUSD != nil && *th.OptionalSampleGPUHourlyRateUSD > 0 {
					c := round2(allocGPUHrs * (*th.OptionalSampleGPUHourlyRateUSD))
					sampleCostPtr = &c
				}

				contextNote := fmt.Sprintf("Mean SM compute utilization is %.1f%% (mem copy %.1f%%, VRAM residency %.0f MiB / %.1f%%). Low compute utilization alone does NOT prove waste.", meanU, meanMC, meanFB, meanFBPct)
				if meanFBPct >= th.HighVRAMContextPercent {
					contextNote = fmt.Sprintf("High VRAM residency (%.0f MiB / %.1f%% of HBM) with %.1f%% SM utilization: workload likely requires GPU memory capacity to hold model weights despite low compute duty cycle.", meanFB, meanFBPct, meanU)
				}

				report.LowUtilizationWorkloads = append(report.LowUtilizationWorkloads, domain.LowUtilPodWindow{
					Namespace:              pod.Namespace,
					PodName:                pod.Name,
					OwnerRef:               fmt.Sprintf("%s/%s", pod.OwnerKind, pod.OwnerName),
					NodeName:               pod.NodeName,
					AllocatedGPUs:          pod.RequestedGPUs,
					AssignedGPUUUIDs:       pod.AssignedGPUUUIDs,
					ObservationWindowMins:  round2(windowMins),
					SampleCount:            totalSamples,
					MeanGPUUtilPercent:     meanU,
					MinGPUUtilPercent:      round2(minU),
					MaxGPUUtilPercent:      round2(maxU),
					MeanMemCopyUtilPercent: meanMC,
					MeanFBUsedMiB:          meanFB,
					MeanFBUsedPercent:      meanFBPct,
					TotalAllocatedGPUHours: allocGPUHrs,
					LowUtilGPUHours:        allocGPUHrs,
					OptionalSampleCostEst:  sampleCostPtr,
					LastDataUpdate:         latest,
					WorkloadContextNote:    contextNote,
					FollowUpChecklist: []string{
						"Inspect data loader CPU/storage throughput and I/O wait times.",
						"Check distributed collective (NCCL) synchronization wait if multi-rank.",
						"Evaluate whether high VRAM usage (DCGM_FI_DEV_FB_USED) justifies full-GPU reservation.",
						"Verify if interactive session or low-QPS service can use MIG slices or time-slicing.",
					},
				})
			}
		}
	}

	if th.OptionalSampleGPUHourlyRateUSD != nil && *th.OptionalSampleGPUHourlyRateUSD > 0 {
		est := round2(report.ObservedLowUtilGPUHours * (*th.OptionalSampleGPUHourlyRateUSD))
		report.OptionalLowUtilSampleCostUSD = &est
	}

	if !report.PerJobAttributionComplete || report.UnmappedAllocatedGPUs > 0 || report.TelemetryStaleGPUs > 0 {
		report.AttributionBlindSpotWarning = fmt.Sprintf("Telemetry attribution is partial (%d stale GPUs, %d allocated GPUs missing pod labels). Unmapped/stale GPUs are excluded from per-job efficiency calculations.",
			report.TelemetryStaleGPUs, report.UnmappedAllocatedGPUs)
	}

	// Build historical utilization series across timestamps where fresh GPU samples exist
	type aggPoint struct {
		sumU  float64
		sumMC float64
		sumFB float64
		count int
	}
	byTS := make(map[time.Time]*aggPoint)
	for _, g := range snapshot.GPUs {
		if g.IsStale {
			continue
		}
		tot := g.TotalMemoryMiB
		if tot <= 0 {
			tot = 81920.0
		}
		for _, s := range g.Samples {
			p, ok := byTS[s.Timestamp]
			if !ok {
				p = &aggPoint{}
				byTS[s.Timestamp] = p
			}
			p.sumU += s.GPUUtilPercent
			p.sumMC += s.MemCopyUtilPercent
			p.sumFB += (s.FBUsedMiB / tot) * 100.0
			p.count++
		}
	}
	var timestamps []time.Time
	for ts := range byTS {
		timestamps = append(timestamps, ts)
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i].Before(timestamps[j]) })
	for _, ts := range timestamps {
		p := byTS[ts]
		if p.count == 0 {
			continue
		}
		report.HistoricalUtilization = append(report.HistoricalUtilization, domain.UtilizationHistoryPoint{
			Timestamp:             ts,
			MeanGPUUtilPercent:    round2(p.sumU / float64(p.count)),
			MeanMemCopyPercent:    round2(p.sumMC / float64(p.count)),
			MeanFBUsedPercent:     round2(p.sumFB / float64(p.count)),
			AllocatedGPUsObserved: p.count,
		})
	}

	return report
}

func round2(v float64) float64 {
	return math.Round(v*100.0) / 100.0
}
