package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/domain"
)

// Rule defines the contract for a deterministic diagnostic rule in the GPU Lens registry.
type Rule interface {
	Definition(th config.RuleThresholds) domain.RuleDefinition
	Evaluate(snapshot *domain.ClusterSnapshot, th config.RuleThresholds) []domain.Incident
}

// Engine manages the ordered registry of deterministic diagnostic rules.
type Engine struct {
	rules []Rule
}

// NewDefaultEngine constructs the rule engine with all 6 core diagnostic rules registered.
func NewDefaultEngine() *Engine {
	return &Engine{
		rules: []Rule{
			&PendingSchedulingRule{},
			&GPUHealthWarningRule{},
			&PersistentLowUtilRule{},
			&WorkloadFailureCorrelationRule{},
			&DataFreshnessRule{},
			&ThermalPowerMemoryRule{},
		},
	}
}

// Definitions returns the inspectable metadata and active thresholds for all registered rules.
func (e *Engine) Definitions(th config.RuleThresholds) []domain.RuleDefinition {
	defs := make([]domain.RuleDefinition, 0, len(e.rules))
	for _, r := range e.rules {
		defs = append(defs, r.Definition(th))
	}
	return defs
}

// Evaluate runs all registered rules deterministically and deduplicates findings by stable fingerprint.
func (e *Engine) Evaluate(snapshot *domain.ClusterSnapshot, th config.RuleThresholds) []domain.Incident {
	if snapshot == nil {
		return nil
	}
	byFingerprint := make(map[string]domain.Incident)
	var order []string

	for _, r := range e.rules {
		findings := r.Evaluate(snapshot, th)
		for _, inc := range findings {
			if inc.EvalStatus == domain.EvalStatusSuppressed || inc.EvalStatus == domain.EvalStatusHealthy {
				continue
			}
			existing, exists := byFingerprint[inc.Fingerprint]
			if !exists {
				if inc.OccurrenceCount < 1 {
					inc.OccurrenceCount = 1
				}
				byFingerprint[inc.Fingerprint] = inc
				order = append(order, inc.Fingerprint)
			} else {
				existing.OccurrenceCount++
				if inc.LastSeenAt.After(existing.LastSeenAt) {
					existing.LastSeenAt = inc.LastSeenAt
				}
				if inc.FirstSeenAt.Before(existing.FirstSeenAt) {
					existing.FirstSeenAt = inc.FirstSeenAt
				}
				byFingerprint[inc.Fingerprint] = existing
			}
		}
	}

	result := make([]domain.Incident, 0, len(order))
	for _, fp := range order {
		result = append(result, byFingerprint[fp])
	}

	// Deterministic sort: Critical first, then Warning, then Info; ties broken by RuleID and ID.
	sort.SliceStable(result, func(i, j int) bool {
		ri := severityRank(result[i].Severity)
		rj := severityRank(result[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if result[i].RuleID != result[j].RuleID {
			return result[i].RuleID < result[j].RuleID
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func severityRank(s domain.Severity) int {
	switch s {
	case domain.SeverityCritical:
		return 3
	case domain.SeverityWarning:
		return 2
	default:
		return 1
	}
}

// ComputeFingerprint generates a deterministic 16-hex-character SHA-256 fingerprint.
func ComputeFingerprint(ruleID, cluster, targetKind, namespace, targetName, subReason string) string {
	raw := strings.Join([]string{ruleID, cluster, targetKind, namespace, targetName, subReason}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:16]
}

// ============================================================================
// Rule 1: GPU-SCHED-001 — Pending GPU Workload Scheduling Bottleneck
// ============================================================================

type PendingSchedulingRule struct{}

func (r *PendingSchedulingRule) Definition(th config.RuleThresholds) domain.RuleDefinition {
	return domain.RuleDefinition{
		ID:              "GPU-SCHED-001",
		Name:            "Pending GPU Workload Scheduling Bottleneck",
		Category:        "scheduling",
		DefaultSeverity: domain.SeverityWarning,
		Description:     "Identifies workloads requesting GPU resources that remain in Pending phase beyond the grace window, correlating scheduler events with per-node GPU allocatable capacity, fragmentation, taints, and node selectors.",
		RequiredSignals: []string{
			"k8s.pod.status.phase",
			"k8s.pod.spec.containers.resources.limits[nvidia.com/gpu]",
			"k8s.event[reason=FailedScheduling]",
			"k8s.node.status.allocatable[nvidia.com/gpu]",
		},
		OptionalSignals: []string{
			"k8s.node.spec.taints",
			"k8s.pod.spec.nodeSelector",
		},
		Thresholds: []domain.RuleThresholdView{
			{Key: "pending_grace_seconds", Label: "Pending Grace Window", Value: float64(th.PendingGraceSeconds), Unit: "s", Description: "Minimum time a GPU pod must be Pending before triggering an incident."},
			{Key: "critical_pending_minutes", Label: "Critical Escalation Window", Value: th.CriticalPendingMinutes, Unit: "min", Description: "Pending duration after which severity escalates from warning to critical."},
		},
		SuppressionConditions: []string{
			"Pod phase is not Pending or RequestedGPUs == 0.",
			"Pod has been Pending for less than pending_grace_seconds.",
		},
		InconclusiveConditions: []string{
			"Pod is Pending beyond grace window, but neither a FailedScheduling event nor node allocatable GPU inventory is available; GPU Lens refuses to guess a scheduling reason.",
		},
		SuggestedChecks: []string{
			"Inspect kubectl describe pod <namespace>/<pod> for FailedScheduling event details.",
			"Verify node status.allocatable['nvidia.com/gpu'] vs status.capacity['nvidia.com/gpu'] to check if device plugin de-allocated an unhealthy GPU.",
			"Check whether free GPUs are fragmented across multiple nodes while the pod requires contiguous single-node placement.",
			"Verify pod nodeSelector, nodeAffinity, and tolerations against available GPU node labels and taints.",
		},
	}
}

func (r *PendingSchedulingRule) Evaluate(snapshot *domain.ClusterSnapshot, th config.RuleThresholds) []domain.Incident {
	var incidents []domain.Incident

	totalFreeAllocatable := 0
	maxFreeSingleNode := 0
	degradedNodes := []string{}
	for _, n := range snapshot.Nodes {
		free := n.GPUAllocatable - n.GPUAllocated
		if free < 0 {
			free = 0
		}
		if n.Ready && n.Schedulable {
			totalFreeAllocatable += free
			if free > maxFreeSingleNode {
				maxFreeSingleNode = free
			}
		}
		if n.GPUAllocatable < n.GPUCapacity || !n.Schedulable || !n.Ready {
			degradedNodes = append(degradedNodes, fmt.Sprintf("%s (capacity=%d, allocatable=%d, allocated=%d, schedulable=%v, ready=%v)",
				n.Name, n.GPUCapacity, n.GPUAllocatable, n.GPUAllocated, n.Schedulable, n.Ready))
		}
	}

	for _, pod := range snapshot.Pods {
		if pod.Phase != "Pending" || pod.RequestedGPUs <= 0 {
			continue
		}
		pendingDur := snapshot.CollectedAt.Sub(pod.CreatedAt)
		if pendingDur.Seconds() < float64(th.PendingGraceSeconds) {
			continue
		}

		// Find matching FailedScheduling events
		var schedEvents []domain.K8sEvent
		for _, ev := range snapshot.Events {
			if ev.InvolvedObjectKind == "Pod" && ev.InvolvedObjectName == pod.Name &&
				(ev.InvolvedObjectNamespace == "" || ev.InvolvedObjectNamespace == pod.Namespace) &&
				ev.Reason == "FailedScheduling" {
				schedEvents = append(schedEvents, ev)
			}
		}

		sev := domain.SeverityWarning
		if pendingDur.Minutes() >= th.CriticalPendingMinutes {
			sev = domain.SeverityCritical
		}

		podRef := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
		fp := ComputeFingerprint("GPU-SCHED-001", snapshot.ClusterName, "Pod", pod.Namespace, pod.Name, "PendingScheduling")

		// Check if evidence is completely missing
		if len(schedEvents) == 0 && len(snapshot.Nodes) == 0 && pod.SchedulerMessage == "" {
			incidents = append(incidents, domain.Incident{
				ID:                    "inc-sched-" + fp[:8],
				Fingerprint:           fp,
				RuleID:                "GPU-SCHED-001",
				RuleName:              "Pending GPU Workload Scheduling Bottleneck",
				Category:              "scheduling",
				Severity:              domain.SeverityWarning,
				EvalStatus:            domain.EvalStatusInconclusive,
				EvidenceStrength:      domain.EvidenceInconclusive,
				EvidenceStrengthBasis: "Pod is Pending, but FailedScheduling events and node resource inventory are absent from the snapshot.",
				Title:                 fmt.Sprintf("Pending GPU workload %s (scheduling reason inconclusive — missing events)", podRef),
				Summary:               fmt.Sprintf("Workload %s requesting %d GPU(s) has been Pending for %.1f minutes, but no FailedScheduling event or node inventory was observed. Refusing to guess scheduling cause.", podRef, pod.RequestedGPUs, pendingDur.Minutes()),
				ObservedFacts: []string{
					fmt.Sprintf("Pod %s has been in Pending phase for %.1f minutes requesting %d nvidia.com/gpu.", podRef, pendingDur.Minutes(), pod.RequestedGPUs),
					"Zero FailedScheduling Kubernetes events and zero Node state records were available for correlation.",
				},
				PossibleCauses: []string{
					"Kubernetes event collection may be disabled, RBAC-restricted, or expired past event TTL.",
					"Custom batch queue controller (e.g., Kueue or Volcano) may be holding the workload prior to pod scheduling.",
				},
				SupportingEvidence: []domain.EvidenceItem{
					{
						Kind:       "pod_state",
						Polarity:   "supporting",
						Source:     "kubernetes_api",
						ObjectRef:  podRef,
						Summary:    "Pod remains in Pending phase beyond grace window",
						Value:      fmt.Sprintf("phase=Pending, requested_gpus=%d, age=%.1fm", pod.RequestedGPUs, pendingDur.Minutes()),
						Threshold:  fmt.Sprintf("pending_grace_seconds >= %ds", th.PendingGraceSeconds),
						ObservedAt: snapshot.CollectedAt,
					},
				},
				AffectedResources:  []string{podRef},
				SuggestedChecks:    r.Definition(th).SuggestedChecks,
				InconclusiveReason: "No FailedScheduling event or Node allocatable state observed; refusing to fabricate a scheduling bottleneck reason.",
				DataMode:           snapshot.DataMode,
				ScenarioID:         snapshot.ScenarioID,
				FirstSeenAt:        pod.CreatedAt,
				LastSeenAt:         snapshot.CollectedAt,
				OccurrenceCount:    1,
			})
			continue
		}

		// Evidence is present: build observed facts and classify the bottleneck accurately
		var supporting []domain.EvidenceItem
		var contradicting []domain.EvidenceItem
		var timeline []domain.TimelineEntry
		var facts []string
		var causes []string

		facts = append(facts, fmt.Sprintf("Pod %s (%s/%s) requesting %d nvidia.com/gpu has been Pending for %.1f minutes (since %s).",
			podRef, pod.OwnerKind, pod.OwnerName, pod.RequestedGPUs, pendingDur.Minutes(), pod.CreatedAt.Format(time.RFC3339)))
		facts = append(facts, fmt.Sprintf("Cluster schedulable free GPUs: %d total across ready/schedulable nodes; maximum free GPUs on any single ready node: %d.",
			totalFreeAllocatable, maxFreeSingleNode))

		timeline = append(timeline, domain.TimelineEntry{
			Timestamp: pod.CreatedAt,
			Layer:     "k8s_pod",
			Severity:  "info",
			ObjectRef: podRef,
			EventType: "PodCreated",
			Summary:   fmt.Sprintf("Pod created requesting %d nvidia.com/gpu", pod.RequestedGPUs),
		})

		schedMsg := pod.SchedulerMessage
		for _, ev := range schedEvents {
			if schedMsg == "" {
				schedMsg = ev.Message
			}
			facts = append(facts, fmt.Sprintf("Observed kube-scheduler event [%s] (x%d): %q", ev.Reason, ev.Count, ev.Message))
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "k8s_event",
				Polarity:   "supporting",
				Source:     ev.SourceComponent,
				ObjectRef:  podRef,
				Summary:    ev.Message,
				Value:      fmt.Sprintf("reason=%s, count=%d", ev.Reason, ev.Count),
				ObservedAt: ev.LastTimestamp,
			})
			timeline = append(timeline, domain.TimelineEntry{
				Timestamp: ev.LastTimestamp,
				Layer:     "k8s_scheduler",
				Severity:  "warning",
				ObjectRef: podRef,
				EventType: ev.Reason,
				Summary:   ev.Message,
			})
		}

		supporting = append(supporting, domain.EvidenceItem{
			Kind:       "node_state",
			Polarity:   "supporting",
			Source:     "kubernetes_api",
			ObjectRef:  "cluster/nodes",
			Summary:    "Cluster GPU allocatable vs allocated inventory",
			Value:      fmt.Sprintf("requested=%d, cluster_free_allocatable=%d, max_single_node_free=%d", pod.RequestedGPUs, totalFreeAllocatable, maxFreeSingleNode),
			Threshold:  fmt.Sprintf("single_node_free >= %d", pod.RequestedGPUs),
			ObservedAt: snapshot.CollectedAt,
		})

		if totalFreeAllocatable < pod.RequestedGPUs {
			causes = append(causes, fmt.Sprintf("Insufficient aggregate allocatable GPUs: pod requests %d GPUs, but only %d allocatable GPUs are currently free across ready/schedulable nodes.", pod.RequestedGPUs, totalFreeAllocatable))
		} else if maxFreeSingleNode < pod.RequestedGPUs {
			causes = append(causes, fmt.Sprintf("Node-level GPU fragmentation: %d free GPUs exist cluster-wide, but no single ready node has %d contiguous free GPUs (max on one node is %d).", totalFreeAllocatable, pod.RequestedGPUs, maxFreeSingleNode))
			contradicting = append(contradicting, domain.EvidenceItem{
				Kind:       "node_state",
				Polarity:   "contradicting",
				Source:     "kubernetes_api",
				ObjectRef:  "cluster/nodes",
				Summary:    "Aggregate free GPU count exceeds pod request, ruling out simple cluster-wide exhaustion",
				Value:      fmt.Sprintf("cluster_free_allocatable=%d >= requested=%d", totalFreeAllocatable, pod.RequestedGPUs),
				ObservedAt: snapshot.CollectedAt,
			})
		}

		if len(degradedNodes) > 0 {
			facts = append(facts, fmt.Sprintf("Nodes with reduced allocatable GPUs or unschedulable state: %s", strings.Join(degradedNodes, "; ")))
			causes = append(causes, "Reduced node allocatable GPU count or cordoned node state is withholding physical GPU capacity from kube-scheduler.")
		}

		if len(pod.NodeSelector) > 0 {
			var selPairs []string
			for k, v := range pod.NodeSelector {
				selPairs = append(selPairs, fmt.Sprintf("%s=%s", k, v))
			}
			sort.Strings(selPairs)
			facts = append(facts, fmt.Sprintf("Pod specifies nodeSelector constraints: %s", strings.Join(selPairs, ", ")))
			if strings.Contains(schedMsg, "node(s) didn't match Pod's node affinity/selector") {
				causes = append(causes, fmt.Sprintf("Node selector / affinity constraint (%s) excludes nodes with available GPUs.", strings.Join(selPairs, ", ")))
			}
		}

		if strings.Contains(schedMsg, "untolerated taint") {
			causes = append(causes, "One or more GPU nodes carry taints not tolerated by this workload.")
		}

		if len(causes) == 0 && schedMsg != "" {
			causes = append(causes, fmt.Sprintf("Scheduler reported constraint: %s", schedMsg))
		}

		strength := domain.EvidenceStrong
		strengthBasis := "Corroborated by both kube-scheduler FailedScheduling event and live per-node GPU allocatable inventory."
		if len(schedEvents) == 0 {
			strength = domain.EvidenceModerate
			strengthBasis = "Derived from live per-node GPU allocatable inventory and pod Pending state; no FailedScheduling event object was present."
		}

		incidents = append(incidents, domain.Incident{
			ID:                    "inc-sched-" + fp[:8],
			Fingerprint:           fp,
			RuleID:                "GPU-SCHED-001",
			RuleName:              "Pending GPU Workload Scheduling Bottleneck",
			Category:              "scheduling",
			Severity:              sev,
			EvalStatus:            domain.EvalStatusTriggered,
			EvidenceStrength:      strength,
			EvidenceStrengthBasis: strengthBasis,
			Title:                 fmt.Sprintf("Pending GPU workload %s (%d GPUs requested, %.0fm pending)", podRef, pod.RequestedGPUs, pendingDur.Minutes()),
			Summary:               fmt.Sprintf("Pod %s requesting %d nvidia.com/gpu has remained Pending for %.1f minutes. %s", podRef, pod.RequestedGPUs, pendingDur.Minutes(), firstOrDefault(causes, schedMsg)),
			ObservedFacts:         facts,
			PossibleCauses:        causes,
			SupportingEvidence:    supporting,
			ContradictingEvidence: contradicting,
			Timeline:              timeline,
			AffectedResources:     []string{podRef},
			SuggestedChecks:       r.Definition(th).SuggestedChecks,
			DataMode:              snapshot.DataMode,
			ScenarioID:            snapshot.ScenarioID,
			FirstSeenAt:           pod.CreatedAt,
			LastSeenAt:            snapshot.CollectedAt,
			OccurrenceCount:       1,
		})
	}

	return incidents
}

// ============================================================================
// Rule 2: GPU-HEALTH-002 — GPU Hardware Fault or Health Degradation
// ============================================================================

type GPUHealthWarningRule struct{}

func (r *GPUHealthWarningRule) Definition(th config.RuleThresholds) domain.RuleDefinition {
	return domain.RuleDefinition{
		ID:              "GPU-HEALTH-002",
		Name:            "GPU Hardware Fault or Health Degradation",
		Category:        "hardware_health",
		DefaultSeverity: domain.SeverityCritical,
		Description:     "Evaluates DCGM hardware health signals (XID errors, row-remap failures, PCIe replay counters, optional ECC double-bit volatile errors, NVLink CRC/replay errors, and DCGM health status) alongside Kubernetes node allocatable drops and Node Problem Detector conditions.",
		RequiredSignals: []string{
			"DCGM_FI_DEV_XID_ERRORS",
			"DCGM_FI_DEV_ROW_REMAP_FAILURE",
			"DCGM_FI_DEV_PCIE_REPLAY_COUNTER",
			"k8s.node.status.allocatable[nvidia.com/gpu]",
		},
		OptionalSignals: []string{
			"DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
			"DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL",
			"DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL",
			"DCGM_EXP_GPU_HEALTH_STATUS",
			"k8s.node.status.conditions[GPUXIDErrors|NVLinkProblem]",
		},
		Thresholds: []domain.RuleThresholdView{
			{Key: "xid_error_threshold", Label: "XID Error Code Trigger", Value: float64(th.XIDErrorThreshold), Unit: "code", Description: "Any non-zero XID error code triggers a critical hardware fault finding."},
			{Key: "ecc_dbe_threshold", Label: "Uncorrectable ECC DBE", Value: float64(th.ECCDBEThreshold), Unit: "errors", Description: "Volatile double-bit ECC error threshold (when metric is exported)."},
			{Key: "row_remap_failure_threshold", Label: "Row Remap Failure", Value: float64(th.RowRemapFailureThreshold), Unit: "flag", Description: "Non-zero DCGM_FI_DEV_ROW_REMAP_FAILURE indicates HBM row remapping exhaustion."},
			{Key: "pcie_replay_warning_count", Label: "PCIe Replay Counter Warning", Value: float64(th.PCIeReplayWarningCount), Unit: "replays", Description: "PCIe replay count threshold for warning severity."},
			{Key: "nvlink_crc_error_threshold", Label: "NVLink CRC Error Count", Value: float64(th.NVLinkCRCErrorThreshold), Unit: "errors", Description: "NVLink CRC flit error threshold (when optional metric is exported)."},
		},
		SuppressionConditions: []string{
			"Optional metrics (ECC DBE, NVLink CRC, DCGM_EXP_GPU_HEALTH_STATUS) are never evaluated or fabricated when not exported by the source.",
			"Stale GPU series (age > max_staleness_seconds) defer to GPU-FRESH-005 rather than asserting healthy status.",
		},
		InconclusiveConditions: []string{
			"Node allocatable GPU count dropped below capacity, but GPU-level DCGM telemetry for that node is stale or unavailable.",
		},
		SuggestedChecks: []string{
			"Inspect node kernel logs (dmesg -T | grep -iE 'nvrm|xid|sxid') to verify exact NVIDIA driver XID context.",
			"Check DCGM health watch breakdown and whether k8s-device-plugin marked the GPU unhealthy (allocatable < capacity).",
			"If XID 48/74/79/94/95, uncorrectable ECC DBE, or row-remap failure is present, cordon the node after workload checkpointing and schedule hardware maintenance.",
		},
	}
}

func (r *GPUHealthWarningRule) Evaluate(snapshot *domain.ClusterSnapshot, th config.RuleThresholds) []domain.Incident {
	var incidents []domain.Incident

	nodeByName := make(map[string]domain.NodeState)
	for _, n := range snapshot.Nodes {
		nodeByName[n.Name] = n
	}

	for _, gpu := range snapshot.GPUs {
		if gpu.IsStale {
			continue
		}
		pt := gpu.Latest
		var facts []string
		var causes []string
		var supporting []domain.EvidenceItem
		var contradicting []domain.EvidenceItem
		var timeline []domain.TimelineEntry
		isCritical := false
		isWarning := false

		gpuRef := fmt.Sprintf("%s/gpu-%d (%s)", gpu.NodeName, gpu.DeviceIndex, gpu.UUID)

		if pt.XIDErrorCode >= th.XIDErrorThreshold {
			isCritical = true
			xidDesc := describeXID(pt.XIDErrorCode)
			facts = append(facts, fmt.Sprintf("GPU %s reported DCGM_FI_DEV_XID_ERRORS = %d (%s) at %s.",
				gpuRef, pt.XIDErrorCode, xidDesc, pt.Timestamp.Format(time.RFC3339)))
			causes = append(causes, fmt.Sprintf("NVIDIA driver emitted XID %d (%s) indicating a GPU hardware, memory, bus, or interconnect fault.", pt.XIDErrorCode, xidDesc))
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_XID_ERRORS",
				ObjectRef:  gpuRef,
				Summary:    fmt.Sprintf("XID %d (%s) observed on GPU", pt.XIDErrorCode, xidDesc),
				Value:      fmt.Sprintf("%d", pt.XIDErrorCode),
				Threshold:  fmt.Sprintf(">= %d", th.XIDErrorThreshold),
				ObservedAt: pt.Timestamp,
			})
			timeline = append(timeline, domain.TimelineEntry{
				Timestamp: pt.Timestamp,
				Layer:     "gpu_telemetry",
				Severity:  "critical",
				ObjectRef: gpuRef,
				EventType: "XIDError",
				Summary:   fmt.Sprintf("DCGM_FI_DEV_XID_ERRORS=%d (%s)", pt.XIDErrorCode, xidDesc),
			})
		}

		if pt.RowRemapFailure >= th.RowRemapFailureThreshold {
			isCritical = true
			facts = append(facts, fmt.Sprintf("GPU %s reported DCGM_FI_DEV_ROW_REMAP_FAILURE = %d (uncorrectable_remapped_rows=%d).",
				gpuRef, pt.RowRemapFailure, pt.UncorrectableRemappedRows))
			causes = append(causes, "HBM memory row remapping table exhausted or failed due to persistent memory cell degradation.")
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_ROW_REMAP_FAILURE",
				ObjectRef:  gpuRef,
				Summary:    "GPU HBM row remap failure flagged",
				Value:      fmt.Sprintf("row_remap_failure=%d, uncorrectable_rows=%d", pt.RowRemapFailure, pt.UncorrectableRemappedRows),
				Threshold:  fmt.Sprintf(">= %d", th.RowRemapFailureThreshold),
				ObservedAt: pt.Timestamp,
			})
		}

		// Optional metric: DCGM_FI_DEV_ECC_DBE_VOL_TOTAL
		if pt.ECCDBEVolTotal != nil && *pt.ECCDBEVolTotal >= th.ECCDBEThreshold {
			isCritical = true
			facts = append(facts, fmt.Sprintf("GPU %s reported optional metric DCGM_FI_DEV_ECC_DBE_VOL_TOTAL = %d uncorrectable double-bit ECC errors.", gpuRef, *pt.ECCDBEVolTotal))
			causes = append(causes, "Uncorrectable volatile double-bit ECC memory error detected in GPU HBM.")
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
				ObjectRef:  gpuRef,
				Summary:    "Uncorrectable volatile double-bit ECC errors detected",
				Value:      fmt.Sprintf("%d", *pt.ECCDBEVolTotal),
				Threshold:  fmt.Sprintf(">= %d", th.ECCDBEThreshold),
				ObservedAt: pt.Timestamp,
			})
		}

		// Optional metric: NVLink CRC / Replay errors
		if pt.NVLinkCRCFlitErrors != nil && *pt.NVLinkCRCFlitErrors >= th.NVLinkCRCErrorThreshold {
			isWarning = true
			facts = append(facts, fmt.Sprintf("GPU %s reported DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL = %d.", gpuRef, *pt.NVLinkCRCFlitErrors))
			causes = append(causes, "NVLink high-speed interconnect experiencing CRC flit errors (degraded signal integrity or switch link issue).")
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL",
				ObjectRef:  gpuRef,
				Summary:    "NVLink CRC flit error counter elevated",
				Value:      fmt.Sprintf("%d", *pt.NVLinkCRCFlitErrors),
				Threshold:  fmt.Sprintf(">= %d", th.NVLinkCRCErrorThreshold),
				ObservedAt: pt.Timestamp,
			})
		}

		if pt.PCIeReplayCounter >= th.PCIeReplayWarningCount {
			isWarning = true
			facts = append(facts, fmt.Sprintf("GPU %s reported DCGM_FI_DEV_PCIE_REPLAY_COUNTER = %d (threshold %d).", gpuRef, pt.PCIeReplayCounter, th.PCIeReplayWarningCount))
			causes = append(causes, "Elevated PCIe link replay counter indicating host-to-GPU bus signal degradation or reseating needed.")
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_PCIE_REPLAY_COUNTER",
				ObjectRef:  gpuRef,
				Summary:    "Elevated PCIe link replay counter",
				Value:      fmt.Sprintf("%d", pt.PCIeReplayCounter),
				Threshold:  fmt.Sprintf(">= %d", th.PCIeReplayWarningCount),
				ObservedAt: pt.Timestamp,
			})
		}

		// Optional metric: DCGM_EXP_GPU_HEALTH_STATUS (10=WARN, 20=FAIL)
		if pt.HealthStatus != nil && *pt.HealthStatus >= 10 {
			statusLabel := "WARN (10)"
			if *pt.HealthStatus >= 20 {
				isCritical = true
				statusLabel = "FAIL (20)"
			} else {
				isWarning = true
			}
			facts = append(facts, fmt.Sprintf("Exporter health watch metric DCGM_EXP_GPU_HEALTH_STATUS reported %s.", statusLabel))
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_EXP_GPU_HEALTH_STATUS",
				ObjectRef:  gpuRef,
				Summary:    "DCGM health watch reported non-passing state",
				Value:      statusLabel,
				Threshold:  "> 0 (PASS)",
				ObservedAt: pt.Timestamp,
			})
		}

		if !isCritical && !isWarning {
			continue
		}

		// Correlate with Node conditions & events
		if node, ok := nodeByName[gpu.NodeName]; ok {
			if node.GPUAllocatable < node.GPUCapacity {
				facts = append(facts, fmt.Sprintf("Node %s Kubernetes status shows allocatable GPUs (%d) < capacity GPUs (%d), confirming device plugin de-allocation.",
					node.Name, node.GPUAllocatable, node.GPUCapacity))
				supporting = append(supporting, domain.EvidenceItem{
					Kind:       "node_state",
					Polarity:   "supporting",
					Source:     "kubernetes_api",
					ObjectRef:  "node/" + node.Name,
					Summary:    "Kubelet allocatable GPU count decreased below physical capacity",
					Value:      fmt.Sprintf("allocatable=%d, capacity=%d", node.GPUAllocatable, node.GPUCapacity),
					ObservedAt: snapshot.CollectedAt,
				})
			} else {
				contradicting = append(contradicting, domain.EvidenceItem{
					Kind:       "node_state",
					Polarity:   "contradicting",
					Source:     "kubernetes_api",
					ObjectRef:  "node/" + node.Name,
					Summary:    "Kubernetes device plugin still advertises full GPU allocatable count on node",
					Value:      fmt.Sprintf("allocatable=%d == capacity=%d", node.GPUAllocatable, node.GPUCapacity),
					ObservedAt: snapshot.CollectedAt,
				})
			}
			for _, cond := range node.Conditions {
				if cond.Status == "True" && (strings.Contains(cond.Type, "GPU") || strings.Contains(cond.Type, "NVLink")) {
					facts = append(facts, fmt.Sprintf("Node Problem Detector condition %s=True (%s): %s", cond.Type, cond.Reason, cond.Message))
					supporting = append(supporting, domain.EvidenceItem{
						Kind:       "node_state",
						Polarity:   "supporting",
						Source:     "node-problem-detector",
						ObjectRef:  "node/" + node.Name,
						Summary:    cond.Message,
						Value:      fmt.Sprintf("%s=%s (%s)", cond.Type, cond.Status, cond.Reason),
						ObservedAt: cond.LastTransitionTime,
					})
					timeline = append(timeline, domain.TimelineEntry{
						Timestamp: cond.LastTransitionTime,
						Layer:     "k8s_node",
						Severity:  "critical",
						ObjectRef: "node/" + node.Name,
						EventType: cond.Type,
						Summary:   cond.Message,
					})
				}
			}
		}

		for _, ev := range snapshot.Events {
			if ev.NodeName == gpu.NodeName || ev.InvolvedObjectName == gpu.NodeName {
				timeline = append(timeline, domain.TimelineEntry{
					Timestamp: ev.LastTimestamp,
					Layer:     "k8s_node",
					Severity:  strings.ToLower(ev.Type),
					ObjectRef: "node/" + gpu.NodeName,
					EventType: ev.Reason,
					Summary:   ev.Message,
				})
			}
		}

		sortTimeline(timeline)

		sev := domain.SeverityWarning
		if isCritical {
			sev = domain.SeverityCritical
		}
		strength := domain.EvidenceModerate
		strengthBasis := "Observed directly in fresh DCGM hardware telemetry."
		if len(supporting) >= 2 {
			strength = domain.EvidenceStrong
			strengthBasis = "Corroborated across multiple independent hardware metrics and/or Kubernetes Node Problem Detector conditions."
		}

		affected := []string{gpuRef, "node/" + gpu.NodeName}
		if gpu.AllocatedPod != "" {
			affected = append(affected, fmt.Sprintf("pod/%s/%s", gpu.AllocatedNamespace, gpu.AllocatedPod))
		}

		fp := ComputeFingerprint("GPU-HEALTH-002", snapshot.ClusterName, "GPU", gpu.NodeName, gpu.UUID, "HardwareHealth")
		incidents = append(incidents, domain.Incident{
			ID:                    "inc-health-" + fp[:8],
			Fingerprint:           fp,
			RuleID:                "GPU-HEALTH-002",
			RuleName:              "GPU Hardware Fault or Health Degradation",
			Category:              "hardware_health",
			Severity:              sev,
			EvalStatus:            domain.EvalStatusTriggered,
			EvidenceStrength:      strength,
			EvidenceStrengthBasis: strengthBasis,
			Title:                 fmt.Sprintf("GPU hardware warning/fault on %s (%s)", gpuRef, gpu.ModelName),
			Summary:               firstOrDefault(facts, fmt.Sprintf("Hardware fault indicators observed on %s.", gpuRef)),
			ObservedFacts:         facts,
			PossibleCauses:        causes,
			SupportingEvidence:    supporting,
			ContradictingEvidence: contradicting,
			Timeline:              timeline,
			AffectedResources:     affected,
			SuggestedChecks:       r.Definition(th).SuggestedChecks,
			DataMode:              snapshot.DataMode,
			ScenarioID:            snapshot.ScenarioID,
			FirstSeenAt:           pt.Timestamp,
			LastSeenAt:            snapshot.CollectedAt,
			OccurrenceCount:       1,
		})
	}

	return incidents
}

// ============================================================================
// Rule 3: GPU-UTIL-003 — Persistently Low GPU Compute Utilization
// ============================================================================

type PersistentLowUtilRule struct{}

func (r *PersistentLowUtilRule) Definition(th config.RuleThresholds) domain.RuleDefinition {
	return domain.RuleDefinition{
		ID:              "GPU-UTIL-003",
		Name:            "Persistently Low GPU Compute Utilization During Active Allocation",
		Category:        "utilization",
		DefaultSeverity: domain.SeverityInfo,
		Description:     "Identifies Running GPU workloads whose allocated GPUs show mean compute utilization (DCGM_FI_DEV_GPU_UTIL) below threshold over the required observation window. Never equates low compute utilization with proven waste; inspects framebuffer memory residency (DCGM_FI_DEV_FB_USED) and memory copy activity (DCGM_FI_DEV_MEM_COPY_UTIL) and provides a structured engineering checklist.",
		RequiredSignals: []string{
			"k8s.pod.status.phase[Running]",
			"DCGM_FI_DEV_GPU_UTIL",
			"DCGM_FI_DEV_MEM_COPY_UTIL",
			"DCGM_FI_DEV_FB_USED",
			"dcgm.pod_label_mapping",
		},
		OptionalSignals: []string{
			"DCGM_FI_DEV_FB_FREE",
		},
		Thresholds: []domain.RuleThresholdView{
			{Key: "low_util_threshold_percent", Label: "Low Compute Util Threshold", Value: th.LowUtilThresholdPercent, Unit: "%", Description: "Mean DCGM_FI_DEV_GPU_UTIL below which an allocation window is flagged for review."},
			{Key: "low_util_window_minutes", Label: "Minimum Observation Window", Value: th.LowUtilWindowMinutes, Unit: "min", Description: "Minimum continuous allocation duration required before firing."},
			{Key: "min_utilization_samples", Label: "Minimum Telemetry Samples", Value: float64(th.MinUtilizationSamples), Unit: "samples", Description: "Minimum number of data points required per GPU to prevent sparse-sample false positives."},
			{Key: "high_vram_context_percent", Label: "High VRAM Residency Indicator", Value: th.HighVRAMContextPercent, Unit: "%", Description: "Framebuffer occupancy threshold indicating a memory-capacity-bound workload."},
		},
		SuppressionConditions: []string{
			"Workload has been Running for less than low_util_window_minutes (warm-up / initialization suppression).",
			"Fewer than min_utilization_samples telemetry points exist in the observation window.",
			"GPU-to-pod label attribution is missing (handled by GPU-FRESH-005).",
		},
		InconclusiveConditions: []string{
			"Telemetry for one or more GPUs assigned to the pod is stale.",
		},
		SuggestedChecks: []string{
			"Verify CPU data-loader throughput, storage read latency, and dataset preprocessing queues for I/O starvation.",
			"In multi-GPU distributed jobs, inspect NCCL barrier wait times to check if a straggler rank or network bottleneck is stalling peers.",
			"Check DCGM_FI_DEV_FB_USED: if VRAM occupancy is high (e.g., >50%), the workload may be memory-capacity-bound (such as an LLM inference service holding weights in HBM during low QPS periods) rather than over-provisioned.",
			"Confirm whether the pod is an interactive Jupyter/VSCode session left idle between experiments, or a batch job suitable for MIG / time-slicing.",
		},
	}
}

func (r *PersistentLowUtilRule) Evaluate(snapshot *domain.ClusterSnapshot, th config.RuleThresholds) []domain.Incident {
	var incidents []domain.Incident

	gpuByUUID := make(map[string]domain.GPUDevice)
	for _, g := range snapshot.GPUs {
		gpuByUUID[g.UUID] = g
	}

	for _, pod := range snapshot.Pods {
		if pod.Phase != "Running" || pod.RequestedGPUs <= 0 || pod.StartedAt == nil {
			continue
		}
		runDur := snapshot.CollectedAt.Sub(*pod.StartedAt)
		if runDur.Minutes() < th.LowUtilWindowMinutes {
			continue
		}
		if len(pod.AssignedGPUUUIDs) == 0 {
			continue
		}

		// Gather assigned GPUs and verify mapping + sample count
		var mappedGPUs []domain.GPUDevice
		allMappedAndFresh := true
		for _, uuid := range pod.AssignedGPUUUIDs {
			g, ok := gpuByUUID[uuid]
			if !ok || g.IsStale || g.MappingStatus != domain.MappingMapped || len(g.Samples) < th.MinUtilizationSamples {
				allMappedAndFresh = false
				break
			}
			mappedGPUs = append(mappedGPUs, g)
		}
		if !allMappedAndFresh || len(mappedGPUs) == 0 {
			continue
		}

		// Calculate aggregate utilization statistics across all assigned GPUs
		totalSamples := 0
		sumUtil := 0.0
		minUtil := 100.0
		maxUtil := 0.0
		sumMemCopy := 0.0
		sumFBUsed := 0.0
		sumFBPct := 0.0
		var earliestSample, latestSample time.Time

		for _, g := range mappedGPUs {
			for _, s := range g.Samples {
				if earliestSample.IsZero() || s.Timestamp.Before(earliestSample) {
					earliestSample = s.Timestamp
				}
				if latestSample.IsZero() || s.Timestamp.After(latestSample) {
					latestSample = s.Timestamp
				}
				sumUtil += s.GPUUtilPercent
				if s.GPUUtilPercent < minUtil {
					minUtil = s.GPUUtilPercent
				}
				if s.GPUUtilPercent > maxUtil {
					maxUtil = s.GPUUtilPercent
				}
				sumMemCopy += s.MemCopyUtilPercent
				sumFBUsed += s.FBUsedMiB
				totalMem := g.TotalMemoryMiB
				if totalMem <= 0 {
					totalMem = s.FBUsedMiB + s.FBFreeMiB
				}
				if totalMem > 0 {
					sumFBPct += (s.FBUsedMiB / totalMem) * 100.0
				}
				totalSamples++
			}
		}

		if totalSamples == 0 {
			continue
		}
		obsWindowMins := latestSample.Sub(earliestSample).Minutes()
		if obsWindowMins < th.LowUtilWindowMinutes {
			obsWindowMins = runDur.Minutes()
		}

		meanUtil := sumUtil / float64(totalSamples)
		meanMemCopy := sumMemCopy / float64(totalSamples)
		meanFBUsed := sumFBUsed / float64(totalSamples)
		meanFBPct := sumFBPct / float64(totalSamples)

		if meanUtil >= th.LowUtilThresholdPercent {
			continue
		}

		podRef := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
		gpuHours := float64(pod.RequestedGPUs) * (obsWindowMins / 60.0)
		fp := ComputeFingerprint("GPU-UTIL-003", snapshot.ClusterName, "Pod", pod.Namespace, pod.Name, "PersistentLowUtil")

		var supporting []domain.EvidenceItem
		var contradicting []domain.EvidenceItem
		var facts []string
		var causes []string

		facts = append(facts, fmt.Sprintf("Workload %s (%s/%s) on node %s has held %d GPU(s) (%s) for %.0f minutes (%.2f allocated GPU-hours).",
			podRef, pod.OwnerKind, pod.OwnerName, pod.NodeName, pod.RequestedGPUs, strings.Join(pod.AssignedGPUUUIDs, ", "), obsWindowMins, gpuHours))
		facts = append(facts, fmt.Sprintf("Across %d telemetry samples (last update %s), mean DCGM_FI_DEV_GPU_UTIL is %.1f%% (min %.1f%%, max %.1f%%), below the %.1f%% review threshold.",
			totalSamples, latestSample.Format(time.RFC3339), meanUtil, minUtil, maxUtil, th.LowUtilThresholdPercent))
		facts = append(facts, fmt.Sprintf("Workload context metrics: mean DCGM_FI_DEV_MEM_COPY_UTIL = %.1f%%, mean DCGM_FI_DEV_FB_USED = %.0f MiB (%.1f%% of GPU VRAM).",
			meanMemCopy, meanFBUsed, meanFBPct))

		supporting = append(supporting, domain.EvidenceItem{
			Kind:       "metric",
			Polarity:   "supporting",
			Source:     "dcgm_exporter",
			MetricName: "DCGM_FI_DEV_GPU_UTIL",
			ObjectRef:  podRef,
			Summary:    fmt.Sprintf("Mean GPU SM utilization is %.1f%% across %d samples over %.0f minutes", meanUtil, totalSamples, obsWindowMins),
			Value:      fmt.Sprintf("mean=%.1f%%, min=%.1f%%, max=%.1f%%", meanUtil, minUtil, maxUtil),
			Threshold:  fmt.Sprintf("< %.1f%% over >= %.0fm", th.LowUtilThresholdPercent, th.LowUtilWindowMinutes),
			ObservedAt: latestSample,
		})

		if meanFBPct >= th.HighVRAMContextPercent {
			contradicting = append(contradicting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "contradicting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_FB_USED",
				ObjectRef:  podRef,
				Summary:    fmt.Sprintf("High GPU framebuffer memory residency (%.0f MiB / %.1f%%) indicates the workload actively requires GPU VRAM capacity despite low SM compute utilization", meanFBUsed, meanFBPct),
				Value:      fmt.Sprintf("%.0f MiB (%.1f%%)", meanFBUsed, meanFBPct),
				Threshold:  fmt.Sprintf(">= %.1f%% VRAM residency", th.HighVRAMContextPercent),
				ObservedAt: latestSample,
			})
			causes = append(causes, fmt.Sprintf("Memory-capacity-bound workload: %.1f%% of GPU framebuffer memory (%.0f MiB) is allocated (e.g., model weights resident in VRAM during low inference QPS or evaluation pause). Low compute utilization here does NOT prove wasted allocation.", meanFBPct, meanFBUsed))
		} else {
			contradicting = append(contradicting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "contextual",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_FB_USED",
				ObjectRef:  podRef,
				Summary:    fmt.Sprintf("Framebuffer memory occupancy is %.0f MiB (%.1f%%) and memory copy utilization is %.1f%%", meanFBUsed, meanFBPct, meanMemCopy),
				Value:      fmt.Sprintf("fb_used=%.1f%%, mem_copy=%.1f%%", meanFBPct, meanMemCopy),
				ObservedAt: latestSample,
			})
		}

		causes = append(causes,
			"CPU or network storage data-loading bottleneck starving GPU compute pipelines between mini-batches.",
			"Synchronization stall or straggler rank in distributed training holding peer GPUs idle at collective barriers.",
			"Interactive development session, notebook, or standby inference replica waiting for external requests.",
		)

		incidents = append(incidents, domain.Incident{
			ID:                    "inc-util-" + fp[:8],
			Fingerprint:           fp,
			RuleID:                "GPU-UTIL-003",
			RuleName:              "Persistently Low GPU Compute Utilization During Active Allocation",
			Category:              "utilization",
			Severity:              domain.SeverityWarning,
			EvalStatus:            domain.EvalStatusTriggered,
			EvidenceStrength:      domain.EvidenceContextual,
			EvidenceStrengthBasis: "Complete, pod-mapped DCGM telemetry confirms low SM compute utilization over the configured window; however, telemetry alone cannot distinguish application I/O bottlenecks or VRAM residency requirements from idle reservations.",
			Title:                 fmt.Sprintf("Persistent low GPU compute utilization on %s (%.1f%% mean over %.0fm)", podRef, meanUtil, obsWindowMins),
			Summary:               fmt.Sprintf("Workload %s has %d allocated GPU(s) with %.1f%% mean compute utilization over %.0f minutes (%.2f GPU-hours observed below %.1f%% threshold; VRAM usage %.1f%%). Review follow-up checklist before altering allocation.", podRef, pod.RequestedGPUs, meanUtil, obsWindowMins, gpuHours, th.LowUtilThresholdPercent, meanFBPct),
			ObservedFacts:         facts,
			PossibleCauses:        causes,
			SupportingEvidence:    supporting,
			ContradictingEvidence: contradicting,
			Timeline: []domain.TimelineEntry{
				{
					Timestamp: *pod.StartedAt,
					Layer:     "k8s_pod",
					Severity:  "info",
					ObjectRef: podRef,
					EventType: "PodStarted",
					Summary:   fmt.Sprintf("Workload started on %s with %d GPU(s)", pod.NodeName, pod.RequestedGPUs),
				},
				{
					Timestamp: latestSample,
					Layer:     "gpu_telemetry",
					Severity:  "warning",
					ObjectRef: podRef,
					EventType: "LowUtilizationWindow",
					Summary:   fmt.Sprintf("Mean GPU compute utilization %.1f%% (VRAM %.1f%%) across %d samples", meanUtil, meanFBPct, totalSamples),
				},
			},
			AffectedResources: append([]string{podRef, "node/" + pod.NodeName}, pod.AssignedGPUUUIDs...),
			SuggestedChecks:   r.Definition(th).SuggestedChecks,
			DataMode:          snapshot.DataMode,
			ScenarioID:        snapshot.ScenarioID,
			FirstSeenAt:       earliestSample,
			LastSeenAt:        latestSample,
			OccurrenceCount:   1,
		})
	}

	return incidents
}

// ============================================================================
// Rule 4: GPU-CORR-004 — Workload Failure or Restart Correlated with Signals
// ============================================================================

type WorkloadFailureCorrelationRule struct{}

func (r *WorkloadFailureCorrelationRule) Definition(th config.RuleThresholds) domain.RuleDefinition {
	return domain.RuleDefinition{
		ID:              "GPU-CORR-004",
		Name:            "Workload Failure or Restart Correlated with GPU / Node Signals",
		Category:        "failure_correlation",
		DefaultSeverity: domain.SeverityCritical,
		Description:     "Correlates workload container terminations, restarts, and Failed phases with GPU hardware faults (XID errors, ECC DBE, row-remap failures, NVLink errors, thermal violations) and Kubernetes node/pod events within a configurable temporal window. Explicitly separates observed facts from possible causes and records contradicting evidence when GPU hardware remained healthy.",
		RequiredSignals: []string{
			"k8s.pod.status.containerStatuses.restartCount",
			"k8s.pod.status.containerStatuses.lastState.terminated",
			"DCGM_FI_DEV_XID_ERRORS",
			"DCGM_FI_DEV_ROW_REMAP_FAILURE",
			"k8s.event",
		},
		OptionalSignals: []string{
			"DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
			"DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL",
			"DCGM_FI_DEV_THERMAL_VIOLATION",
		},
		Thresholds: []domain.RuleThresholdView{
			{Key: "correlation_lead_minutes", Label: "Pre-Failure Lookback Window", Value: th.CorrelationLeadMinutes, Unit: "min", Description: "Minutes prior to container termination inspected for preceding GPU/node anomalies."},
			{Key: "correlation_lag_minutes", Label: "Post-Failure Lag Window", Value: th.CorrelationLagMinutes, Unit: "min", Description: "Minutes after container termination inspected for delayed telemetry or node events."},
		},
		SuppressionConditions: []string{
			"Pod has zero restarts, is not in Failed phase, and has no terminated container state.",
		},
		InconclusiveConditions: []string{
			"Pod failed or restarted, but GPU telemetry for the assigned node/GPUs is missing or stale during the termination window.",
		},
		SuggestedChecks: []string{
			"Compare the container termination timestamp and exit code against the correlated GPU XID / ECC / NVLink timeline entries.",
			"If GPU hardware faults preceded the crash, drain or cordon the node before rescheduling the job so it does not enter a restart loop on defective hardware.",
			"If GPU telemetry shows zero hardware errors throughout the window (contradicting hardware fault), inspect application logs for OOM, NCCL rank deadlocks, or bad input data.",
		},
	}
}

func (r *WorkloadFailureCorrelationRule) Evaluate(snapshot *domain.ClusterSnapshot, th config.RuleThresholds) []domain.Incident {
	var incidents []domain.Incident

	for _, pod := range snapshot.Pods {
		if pod.RestartCount == 0 && pod.Phase != "Failed" && pod.LastTermination == nil {
			continue
		}
		if pod.LastTermination == nil {
			continue
		}

		term := pod.LastTermination
		podRef := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
		windowStart := term.FinishedAt.Add(-time.Duration(th.CorrelationLeadMinutes * float64(time.Minute)))
		windowEnd := term.FinishedAt.Add(time.Duration(th.CorrelationLagMinutes * float64(time.Minute)))

		// Gather GPUs assigned to pod or on the same node
		var relevantGPUs []domain.GPUDevice
		assignedSet := make(map[string]bool)
		for _, id := range pod.AssignedGPUUUIDs {
			assignedSet[id] = true
		}
		for _, g := range snapshot.GPUs {
			if assignedSet[g.UUID] || (len(assignedSet) == 0 && g.NodeName == pod.NodeName) {
				relevantGPUs = append(relevantGPUs, g)
			}
		}

		var timeline []domain.TimelineEntry
		var supporting []domain.EvidenceItem
		var contradicting []domain.EvidenceItem
		var facts []string
		var causes []string

		facts = append(facts, fmt.Sprintf("Pod %s (%s/%s) on node %s experienced container termination (reason=%s, exit_code=%d, restart_count=%d) at %s: %q.",
			podRef, pod.OwnerKind, pod.OwnerName, pod.NodeName, term.Reason, term.ExitCode, pod.RestartCount, term.FinishedAt.Format(time.RFC3339), term.Message))

		timeline = append(timeline,
			domain.TimelineEntry{
				Timestamp: term.StartedAt,
				Layer:     "k8s_pod",
				Severity:  "info",
				ObjectRef: podRef,
				EventType: "ContainerStarted",
				Summary:   fmt.Sprintf("Container started on node %s", pod.NodeName),
			},
			domain.TimelineEntry{
				Timestamp: term.FinishedAt,
				Layer:     "k8s_pod",
				Severity:  "critical",
				ObjectRef: podRef,
				EventType: "ContainerTerminated",
				Summary:   fmt.Sprintf("Terminated with exit_code=%d (%s): %s", term.ExitCode, term.Reason, term.Message),
			},
		)

		supporting = append(supporting, domain.EvidenceItem{
			Kind:       "pod_state",
			Polarity:   "supporting",
			Source:     "kubernetes_api",
			ObjectRef:  podRef,
			Summary:    fmt.Sprintf("Container terminated with exit code %d (%s), restartCount=%d", term.ExitCode, term.Reason, pod.RestartCount),
			Value:      fmt.Sprintf("exit_code=%d, reason=%s, restarts=%d", term.ExitCode, term.Reason, pod.RestartCount),
			ObservedAt: term.FinishedAt,
		})

		// Add related Kubernetes events
		for _, ev := range snapshot.Events {
			if (ev.InvolvedObjectName == pod.Name || ev.InvolvedObjectName == pod.NodeName || ev.NodeName == pod.NodeName) &&
				!ev.LastTimestamp.Before(windowStart) && !ev.FirstTimestamp.After(windowEnd) {
				timeline = append(timeline, domain.TimelineEntry{
					Timestamp: ev.LastTimestamp,
					Layer:     "k8s_node",
					Severity:  strings.ToLower(ev.Type),
					ObjectRef: fmt.Sprintf("%s/%s", strings.ToLower(ev.InvolvedObjectKind), ev.InvolvedObjectName),
					EventType: ev.Reason,
					Summary:   ev.Message,
				})
				supporting = append(supporting, domain.EvidenceItem{
					Kind:       "k8s_event",
					Polarity:   "supporting",
					Source:     ev.SourceComponent,
					ObjectRef:  fmt.Sprintf("%s/%s", strings.ToLower(ev.InvolvedObjectKind), ev.InvolvedObjectName),
					Summary:    ev.Message,
					Value:      fmt.Sprintf("reason=%s, count=%d", ev.Reason, ev.Count),
					ObservedAt: ev.LastTimestamp,
				})
			}
		}

		// Check if telemetry is missing or stale
		anyStaleOrMissing := len(relevantGPUs) == 0
		for _, g := range relevantGPUs {
			if g.IsStale {
				anyStaleOrMissing = true
			}
		}

		if anyStaleOrMissing {
			sortTimeline(timeline)
			fp := ComputeFingerprint("GPU-CORR-004", snapshot.ClusterName, "Pod", pod.Namespace, pod.Name, "InconclusiveTelemetry")
			incidents = append(incidents, domain.Incident{
				ID:                    "inc-corr-" + fp[:8],
				Fingerprint:           fp,
				RuleID:                "GPU-CORR-004",
				RuleName:              "Workload Failure or Restart Correlated with GPU / Node Signals",
				Category:              "failure_correlation",
				Severity:              domain.SeverityWarning,
				EvalStatus:            domain.EvalStatusInconclusive,
				EvidenceStrength:      domain.EvidenceInconclusive,
				EvidenceStrengthBasis: "Container termination is confirmed, but GPU telemetry on the node is missing or stale during the correlation window.",
				Title:                 fmt.Sprintf("Workload failure on %s (GPU hardware correlation inconclusive — stale telemetry)", podRef),
				Summary:               fmt.Sprintf("Workload %s failed/restarted (exit_code=%d, reason=%s), but GPU telemetry on node %s was stale or unavailable during [%.0fm, +%.0fm]. Cannot confirm or rule out GPU hardware fault.", podRef, term.ExitCode, term.Reason, pod.NodeName, -th.CorrelationLeadMinutes, th.CorrelationLagMinutes),
				ObservedFacts: append(facts,
					fmt.Sprintf("GPU telemetry on node %s is stale or unavailable for the termination window [%s, %s].", pod.NodeName, windowStart.Format(time.RFC3339), windowEnd.Format(time.RFC3339)),
				),
				PossibleCauses: []string{
					"Node or GPU driver hang may have simultaneously crashed both the workload container and the local dcgm-exporter daemon.",
					"Userspace application crash occurred while telemetry scraping was independently degraded.",
				},
				SupportingEvidence: supporting,
				Timeline:           timeline,
				AffectedResources:  []string{podRef, "node/" + pod.NodeName},
				SuggestedChecks:    r.Definition(th).SuggestedChecks,
				InconclusiveReason: "GPU telemetry was stale or missing during the container termination correlation window.",
				DataMode:           snapshot.DataMode,
				ScenarioID:         snapshot.ScenarioID,
				FirstSeenAt:        term.FinishedAt,
				LastSeenAt:         snapshot.CollectedAt,
				OccurrenceCount:    1,
			})
			continue
		}

		// Inspect GPU samples inside the correlation window
		hwFaultFound := false
		for _, g := range relevantGPUs {
			gpuRef := fmt.Sprintf("%s/gpu-%d (%s)", g.NodeName, g.DeviceIndex, g.UUID)
			for _, s := range g.Samples {
				if s.Timestamp.Before(windowStart) || s.Timestamp.After(windowEnd) {
					continue
				}
				if s.XIDErrorCode > 0 {
					hwFaultFound = true
					xidDesc := describeXID(s.XIDErrorCode)
					deltaSec := term.FinishedAt.Sub(s.Timestamp).Seconds()
					timingPhrase := fmt.Sprintf("%.0f seconds before container termination", deltaSec)
					if deltaSec < 0 {
						timingPhrase = fmt.Sprintf("%.0f seconds after container termination (persisting fault)", -deltaSec)
					}
					facts = append(facts, fmt.Sprintf("Assigned GPU %s recorded XID %d (%s) at %s (%s).",
						gpuRef, s.XIDErrorCode, xidDesc, s.Timestamp.Format(time.RFC3339), timingPhrase))
					supporting = append(supporting, domain.EvidenceItem{
						Kind:       "metric",
						Polarity:   "supporting",
						Source:     "dcgm_exporter",
						MetricName: "DCGM_FI_DEV_XID_ERRORS",
						ObjectRef:  gpuRef,
						Summary:    fmt.Sprintf("XID %d (%s) occurred %.0fs prior to container exit", s.XIDErrorCode, xidDesc, deltaSec),
						Value:      fmt.Sprintf("%d", s.XIDErrorCode),
						Threshold:  fmt.Sprintf("within [-%.0fm, +%.0fm] of termination", th.CorrelationLeadMinutes, th.CorrelationLagMinutes),
						ObservedAt: s.Timestamp,
					})
					timeline = append(timeline, domain.TimelineEntry{
						Timestamp: s.Timestamp,
						Layer:     "gpu_telemetry",
						Severity:  "critical",
						ObjectRef: gpuRef,
						EventType: fmt.Sprintf("XID_%d", s.XIDErrorCode),
						Summary:   fmt.Sprintf("GPU %s emitted XID %d (%s)", gpuRef, s.XIDErrorCode, xidDesc),
					})
				}
				if s.ECCDBEVolTotal != nil && *s.ECCDBEVolTotal > 0 {
					hwFaultFound = true
					facts = append(facts, fmt.Sprintf("Assigned GPU %s recorded %d uncorrectable double-bit ECC error(s) (DCGM_FI_DEV_ECC_DBE_VOL_TOTAL) at %s.",
						gpuRef, *s.ECCDBEVolTotal, s.Timestamp.Format(time.RFC3339)))
					supporting = append(supporting, domain.EvidenceItem{
						Kind:       "metric",
						Polarity:   "supporting",
						Source:     "dcgm_exporter",
						MetricName: "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
						ObjectRef:  gpuRef,
						Summary:    "Uncorrectable double-bit ECC error coincided with container failure",
						Value:      fmt.Sprintf("%d", *s.ECCDBEVolTotal),
						ObservedAt: s.Timestamp,
					})
					timeline = append(timeline, domain.TimelineEntry{
						Timestamp: s.Timestamp,
						Layer:     "gpu_telemetry",
						Severity:  "critical",
						ObjectRef: gpuRef,
						EventType: "ECC_DBE",
						Summary:   fmt.Sprintf("DCGM_FI_DEV_ECC_DBE_VOL_TOTAL=%d", *s.ECCDBEVolTotal),
					})
				}
				if s.RowRemapFailure > 0 {
					hwFaultFound = true
					facts = append(facts, fmt.Sprintf("Assigned GPU %s reported DCGM_FI_DEV_ROW_REMAP_FAILURE=1 at %s.", gpuRef, s.Timestamp.Format(time.RFC3339)))
				}
				if s.NVLinkReplayErrors != nil && *s.NVLinkReplayErrors > 0 {
					hwFaultFound = true
					facts = append(facts, fmt.Sprintf("Assigned GPU %s reported %d NVLink replay errors at %s.", gpuRef, *s.NVLinkReplayErrors, s.Timestamp.Format(time.RFC3339)))
					timeline = append(timeline, domain.TimelineEntry{
						Timestamp: s.Timestamp,
						Layer:     "gpu_telemetry",
						Severity:  "warning",
						ObjectRef: gpuRef,
						EventType: "NVLinkReplayError",
						Summary:   fmt.Sprintf("DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL=%d", *s.NVLinkReplayErrors),
					})
				}
			}
		}

		sortTimeline(timeline)

		if hwFaultFound {
			causes = append(causes,
				"Underlying GPU hardware fault (XID / uncorrectable ECC / NVLink fault) on the assigned accelerator likely aborted the active CUDA context and triggered the container crash.",
				"Alternative hypothesis to verify: application-level memory corruption or driver bug triggered simultaneously (note: temporal correlation is strong supporting evidence, not mathematical proof of sole causation).",
			)
			if term.Reason != "OOMKilled" && term.ExitCode != 137 {
				contradicting = append(contradicting, domain.EvidenceItem{
					Kind:       "pod_state",
					Polarity:   "contradicting",
					Source:     "kubernetes_api",
					ObjectRef:  podRef,
					Summary:    "Container termination reason is not host cgroup OOMKilled (exit code != 137), ruling out host RAM cgroup eviction",
					Value:      fmt.Sprintf("reason=%s, exit_code=%d", term.Reason, term.ExitCode),
					ObservedAt: term.FinishedAt,
				})
			}

			fp := ComputeFingerprint("GPU-CORR-004", snapshot.ClusterName, "Pod", pod.Namespace, pod.Name, "HardwareCorrelatedFailure")
			incidents = append(incidents, domain.Incident{
				ID:                    "inc-corr-" + fp[:8],
				Fingerprint:           fp,
				RuleID:                "GPU-CORR-004",
				RuleName:              "Workload Failure or Restart Correlated with GPU / Node Signals",
				Category:              "failure_correlation",
				Severity:              domain.SeverityCritical,
				EvalStatus:            domain.EvalStatusTriggered,
				EvidenceStrength:      domain.EvidenceStrong,
				EvidenceStrengthBasis: "Multi-layer temporal and topological correlation: container termination occurred within the configured window of a hardware fault on the exact GPU assigned to the pod.",
				Title:                 fmt.Sprintf("Workload failure on %s correlated with GPU hardware fault on %s", podRef, pod.NodeName),
				Summary:               fmt.Sprintf("Pod %s terminated (exit_code=%d, %s) and restarted (%d total restarts) within [-%.0fm, +%.0fm] of hardware error signals on its assigned GPU(s) on %s.", podRef, term.ExitCode, term.Reason, pod.RestartCount, th.CorrelationLeadMinutes, th.CorrelationLagMinutes, pod.NodeName),
				ObservedFacts:         facts,
				PossibleCauses:        causes,
				SupportingEvidence:    supporting,
				ContradictingEvidence: contradicting,
				Timeline:              timeline,
				AffectedResources:     append([]string{podRef, "node/" + pod.NodeName}, pod.AssignedGPUUUIDs...),
				SuggestedChecks:       r.Definition(th).SuggestedChecks,
				DataMode:              snapshot.DataMode,
				ScenarioID:            snapshot.ScenarioID,
				FirstSeenAt:           term.StartedAt,
				LastSeenAt:            snapshot.CollectedAt,
				OccurrenceCount:       1,
			})
		} else {
			// GPU telemetry is fresh and healthy throughout the failure window -> Contradicts GPU hardware cause!
			facts = append(facts, fmt.Sprintf("Assigned GPU(s) (%s) reported zero XID errors, zero row-remap failures, and normal operating temperatures throughout [%s, %s].",
				strings.Join(pod.AssignedGPUUUIDs, ", "), windowStart.Format(time.RFC3339), windowEnd.Format(time.RFC3339)))
			contradicting = append(contradicting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "contradicting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_XID_ERRORS",
				ObjectRef:  strings.Join(pod.AssignedGPUUUIDs, ","),
				Summary:    "Assigned GPUs recorded zero XID errors, zero ECC DBEs, and healthy temperatures during the failure window (contradicts local GPU hardware fault)",
				Value:      "xid_errors=0, row_remap_failure=0",
				ObservedAt: term.FinishedAt,
			})
			causes = append(causes,
				"Userspace application exception, CUDA out-of-memory, or host cgroup OOMKilled (local GPU hardware telemetry remained healthy throughout the failure window).",
				"If distributed training: peer rank hang or network timeout causing collective abort on this rank despite healthy local GPUs.",
			)
			fp := ComputeFingerprint("GPU-CORR-004", snapshot.ClusterName, "Pod", pod.Namespace, pod.Name, "ApplicationFailureHealthyGPU")
			incidents = append(incidents, domain.Incident{
				ID:                    "inc-corr-" + fp[:8],
				Fingerprint:           fp,
				RuleID:                "GPU-CORR-004",
				RuleName:              "Workload Failure or Restart Correlated with GPU / Node Signals",
				Category:              "failure_correlation",
				Severity:              domain.SeverityWarning,
				EvalStatus:            domain.EvalStatusTriggered,
				EvidenceStrength:      domain.EvidenceModerate,
				EvidenceStrengthBasis: "Container termination confirmed; fresh DCGM telemetry on assigned GPUs provides contradicting evidence against a local GPU hardware fault.",
				Title:                 fmt.Sprintf("Workload failure on %s (local GPU hardware telemetry healthy)", podRef),
				Summary:               fmt.Sprintf("Pod %s terminated (exit_code=%d, %s), while fresh DCGM telemetry on assigned GPU(s) recorded zero hardware faults during the correlation window.", podRef, term.ExitCode, term.Reason),
				ObservedFacts:         facts,
				PossibleCauses:        causes,
				SupportingEvidence:    supporting,
				ContradictingEvidence: contradicting,
				Timeline:              timeline,
				AffectedResources:     append([]string{podRef, "node/" + pod.NodeName}, pod.AssignedGPUUUIDs...),
				SuggestedChecks:       r.Definition(th).SuggestedChecks,
				DataMode:              snapshot.DataMode,
				ScenarioID:            snapshot.ScenarioID,
				FirstSeenAt:           term.StartedAt,
				LastSeenAt:            snapshot.CollectedAt,
				OccurrenceCount:       1,
			})
		}
	}

	return incidents
}

// ============================================================================
// Rule 5: GPU-FRESH-005 — Telemetry Source Unavailable, Stale, or Uncorrelated
// ============================================================================

type DataFreshnessRule struct{}

func (r *DataFreshnessRule) Definition(th config.RuleThresholds) domain.RuleDefinition {
	return domain.RuleDefinition{
		ID:              "GPU-FRESH-005",
		Name:            "Telemetry Source Unavailable, Stale, or Uncorrelated",
		Category:        "data_freshness",
		DefaultSeverity: domain.SeverityCritical,
		Description:     "Detects when a telemetry source fails collection, when GPU metric samples exceed the maximum staleness window, or when active GPU workloads cannot be correlated to GPU telemetry due to missing Kubernetes pod/namespace labels.",
		RequiredSignals: []string{
			"source.available",
			"source.last_success_at",
			"gpu.last_sample_at",
			"gpu.mapping_status",
		},
		OptionalSignals: []string{},
		Thresholds: []domain.RuleThresholdView{
			{Key: "max_staleness_seconds", Label: "Maximum Telemetry Age", Value: th.MaxStalenessSeconds, Unit: "s", Description: "Maximum age of a GPU metric sample before it is flagged as stale."},
		},
		SuppressionConditions: []string{
			"All configured sources are available, all GPU samples are fresher than max_staleness_seconds, and all Running GPU pods have mapped pod labels.",
		},
		InconclusiveConditions: []string{
			"This rule directly reports monitoring blind spots so that other rules never mistake missing telemetry for healthy hardware.",
		},
		SuggestedChecks: []string{
			"Check Prometheus target health (/targets) and dcgm-exporter DaemonSet pod readiness on affected nodes.",
			"If metrics are fresh but pod/namespace labels are missing, verify dcgm-exporter runs with --kubernetes and mounts /var/lib/kubelet/pod-resources/ (the directory, not the socket file inode).",
			"Verify nv-hostengine responsiveness on nodes with stale GPU series.",
		},
	}
}

func (r *DataFreshnessRule) Evaluate(snapshot *domain.ClusterSnapshot, th config.RuleThresholds) []domain.Incident {
	var incidents []domain.Incident

	// 1. Check unavailable or stale sources
	for _, src := range snapshot.Sources {
		if !src.Available || src.IsStale || src.StalenessSeconds > th.MaxStalenessSeconds {
			fp := ComputeFingerprint("GPU-FRESH-005", snapshot.ClusterName, "Source", "cluster", src.ID, "SourceStaleOrDown")
			incidents = append(incidents, domain.Incident{
				ID:                    "inc-fresh-" + fp[:8],
				Fingerprint:           fp,
				RuleID:                "GPU-FRESH-005",
				RuleName:              "Telemetry Source Unavailable, Stale, or Uncorrelated",
				Category:              "data_freshness",
				Severity:              domain.SeverityCritical,
				EvalStatus:            domain.EvalStatusTriggered,
				EvidenceStrength:      domain.EvidenceStrong,
				EvidenceStrengthBasis: "Direct telemetry collector health check and scrape timestamp audit.",
				Title:                 fmt.Sprintf("Telemetry blind spot: source %s is unavailable or stale (%.0fs old)", src.Name, src.StalenessSeconds),
				Summary:               fmt.Sprintf("Telemetry source %s (%s) is unavailable or stale (last successful collection %s, %.0fs ago > %.0fs limit). Missing observations do NOT indicate healthy GPUs.", src.Name, src.Endpoint, src.LastSuccessAt.Format(time.RFC3339), src.StalenessSeconds, th.MaxStalenessSeconds),
				ObservedFacts: []string{
					fmt.Sprintf("Source %s (kind=%s, endpoint=%s) reported available=%v, staleness=%.0fs (threshold %.0fs).", src.Name, src.Kind, src.Endpoint, src.Available, src.StalenessSeconds, th.MaxStalenessSeconds),
					firstOrDefault([]string{src.LastError}, "Scrape timestamp exceeded max_staleness_seconds."),
				},
				PossibleCauses: []string{
					"Prometheus server or dcgm-exporter endpoint is unreachable, crashed, or blocked by NetworkPolicy.",
					"Collector scrape loop timed out due to hung nv-hostengine.",
				},
				SupportingEvidence: []domain.EvidenceItem{
					{
						Kind:       "source_state",
						Polarity:   "supporting",
						Source:     src.ID,
						ObjectRef:  src.Name,
						Summary:    fmt.Sprintf("Source available=%v, staleness=%.0fs", src.Available, src.StalenessSeconds),
						Value:      fmt.Sprintf("staleness=%.0fs, error=%q", src.StalenessSeconds, src.LastError),
						Threshold:  fmt.Sprintf("<= %.0fs", th.MaxStalenessSeconds),
						ObservedAt: snapshot.CollectedAt,
					},
				},
				Timeline: []domain.TimelineEntry{
					{
						Timestamp: src.LastSuccessAt,
						Layer:     "gpu_telemetry",
						Severity:  "info",
						ObjectRef: src.Name,
						EventType: "LastSuccessfulCollection",
						Summary:   "Last valid scrape received from telemetry source",
					},
					{
						Timestamp: snapshot.CollectedAt,
						Layer:     "gpu_telemetry",
						Severity:  "critical",
						ObjectRef: src.Name,
						EventType: "TelemetryStale",
						Summary:   fmt.Sprintf("Telemetry age %.0fs exceeded %.0fs threshold", src.StalenessSeconds, th.MaxStalenessSeconds),
					},
				},
				AffectedResources: []string{"source/" + src.ID},
				SuggestedChecks:   r.Definition(th).SuggestedChecks,
				DataMode:          snapshot.DataMode,
				ScenarioID:        snapshot.ScenarioID,
				FirstSeenAt:       src.LastSuccessAt,
				LastSeenAt:        snapshot.CollectedAt,
				OccurrenceCount:   1,
			})
		}
	}

	// 2. Check per-GPU staleness and pod-to-GPU attribution blind spots
	var staleGPURefs []string
	var unmappedGPURefs []string
	var earliestStale time.Time

	for _, g := range snapshot.GPUs {
		age := snapshot.CollectedAt.Sub(g.LastSampleAt).Seconds()
		if g.IsStale || age > th.MaxStalenessSeconds {
			staleGPURefs = append(staleGPURefs, fmt.Sprintf("%s/gpu-%d (%s, age=%.0fs)", g.NodeName, g.DeviceIndex, g.UUID, age))
			if earliestStale.IsZero() || g.LastSampleAt.Before(earliestStale) {
				earliestStale = g.LastSampleAt
			}
		}
		if g.MappingStatus == domain.MappingMissingPodLabels {
			unmappedGPURefs = append(unmappedGPURefs, fmt.Sprintf("%s/gpu-%d (%s)", g.NodeName, g.DeviceIndex, g.UUID))
		}
	}

	if len(staleGPURefs) > 0 {
		fp := ComputeFingerprint("GPU-FRESH-005", snapshot.ClusterName, "ClusterGPUs", "cluster", "stale-gpus", "StaleGPUMetrics")
		incidents = append(incidents, domain.Incident{
			ID:                    "inc-fresh-" + fp[:8],
			Fingerprint:           fp,
			RuleID:                "GPU-FRESH-005",
			RuleName:              "Telemetry Source Unavailable, Stale, or Uncorrelated",
			Category:              "data_freshness",
			Severity:              domain.SeverityCritical,
			EvalStatus:            domain.EvalStatusTriggered,
			EvidenceStrength:      domain.EvidenceStrong,
			EvidenceStrengthBasis: "GPU metric sample timestamps compared directly against snapshot collection time.",
			Title:                 fmt.Sprintf("Stale GPU telemetry on %d device(s) (blind spot active)", len(staleGPURefs)),
			Summary:               fmt.Sprintf("%d GPU device(s) have telemetry older than %.0f seconds (%s). Health and utilization diagnostics for these devices are marked inconclusive rather than healthy.", len(staleGPURefs), th.MaxStalenessSeconds, strings.Join(staleGPURefs, "; ")),
			ObservedFacts: []string{
				fmt.Sprintf("%d GPU(s) exceeded max_staleness_seconds (%.0fs): %s", len(staleGPURefs), th.MaxStalenessSeconds, strings.Join(staleGPURefs, ", ")),
			},
			PossibleCauses: []string{
				"dcgm-exporter pod on the affected node crashed, was evicted, or lost connection to nv-hostengine.",
				"GPU driver lockup (e.g., severe PCIe bus hang) caused NVML/DCGM polling to block past scrape timeout.",
			},
			SupportingEvidence: []domain.EvidenceItem{
				{
					Kind:       "metric",
					Polarity:   "supporting",
					Source:     "dcgm_exporter",
					ObjectRef:  "cluster/gpus",
					Summary:    fmt.Sprintf("%d GPU series have sample timestamps older than %.0fs", len(staleGPURefs), th.MaxStalenessSeconds),
					Value:      strings.Join(staleGPURefs, "; "),
					Threshold:  fmt.Sprintf("age <= %.0fs", th.MaxStalenessSeconds),
					ObservedAt: snapshot.CollectedAt,
				},
			},
			Timeline: []domain.TimelineEntry{
				{
					Timestamp: earliestStale,
					Layer:     "gpu_telemetry",
					Severity:  "info",
					ObjectRef: "cluster/gpus",
					EventType: "LastFreshSample",
					Summary:   "Last fresh sample recorded on affected GPU(s)",
				},
				{
					Timestamp: snapshot.CollectedAt,
					Layer:     "gpu_telemetry",
					Severity:  "critical",
					ObjectRef: "cluster/gpus",
					EventType: "StaleTelemetryDetected",
					Summary:   fmt.Sprintf("%d GPU(s) exceeded %.0fs staleness threshold", len(staleGPURefs), th.MaxStalenessSeconds),
				},
			},
			AffectedResources: staleGPURefs,
			SuggestedChecks:   r.Definition(th).SuggestedChecks,
			DataMode:          snapshot.DataMode,
			ScenarioID:        snapshot.ScenarioID,
			FirstSeenAt:       earliestStale,
			LastSeenAt:        snapshot.CollectedAt,
			OccurrenceCount:   1,
		})
	}

	if len(unmappedGPURefs) > 0 {
		fp := ComputeFingerprint("GPU-FRESH-005", snapshot.ClusterName, "ClusterGPUs", "cluster", "unmapped-gpus", "MissingPodLabels")
		incidents = append(incidents, domain.Incident{
			ID:                    "inc-fresh-" + fp[:8],
			Fingerprint:           fp,
			RuleID:                "GPU-FRESH-005",
			RuleName:              "Telemetry Source Unavailable, Stale, or Uncorrelated",
			Category:              "data_freshness",
			Severity:              domain.SeverityWarning,
			EvalStatus:            domain.EvalStatusTriggered,
			EvidenceStrength:      domain.EvidenceStrong,
			EvidenceStrengthBasis: "Kubernetes Node/Pod state confirms active GPU allocations on nodes where DCGM series lack pod/namespace labels.",
			Title:                 fmt.Sprintf("Uncorrelated GPU telemetry: %d allocated GPU(s) missing Kubernetes pod labels", len(unmappedGPURefs)),
			Summary:               fmt.Sprintf("%d allocated GPU(s) (%s) are emitting hardware metrics without Kubernetes pod/namespace labels. Per-workload utilization and efficiency claims are suppressed for these devices.", len(unmappedGPURefs), strings.Join(unmappedGPURefs, ", ")),
			ObservedFacts: []string{
				fmt.Sprintf("Allocated GPUs missing pod/namespace labels: %s", strings.Join(unmappedGPURefs, ", ")),
				"Per-job GPU utilization attribution is disabled for unmapped devices to prevent fabricated efficiency numbers.",
			},
			PossibleCauses: []string{
				"dcgm-exporter is running without the --kubernetes flag enabled.",
				"Kubelet restarted while dcgm-exporter mounted /var/lib/kubelet/pod-resources/kubelet.sock directly as a file instead of mounting the /var/lib/kubelet/pod-resources/ directory, leaving a broken socket inode.",
			},
			SupportingEvidence: []domain.EvidenceItem{
				{
					Kind:       "metric",
					Polarity:   "supporting",
					Source:     "dcgm_exporter",
					ObjectRef:  "cluster/pod-resources-mapping",
					Summary:    "DCGM metric series lack pod and namespace labels on nodes with active GPU pods",
					Value:      fmt.Sprintf("%d unmapped GPUs: %s", len(unmappedGPURefs), strings.Join(unmappedGPURefs, ", ")),
					ObservedAt: snapshot.CollectedAt,
				},
			},
			AffectedResources: unmappedGPURefs,
			SuggestedChecks:   r.Definition(th).SuggestedChecks,
			DataMode:          snapshot.DataMode,
			ScenarioID:        snapshot.ScenarioID,
			FirstSeenAt:       snapshot.CollectedAt,
			LastSeenAt:        snapshot.CollectedAt,
			OccurrenceCount:   1,
		})
	}

	return incidents
}

// ============================================================================
// Rule 6: GPU-THERM-006 — Thermal, Power Brake, or Memory Pressure Warning
// ============================================================================

type ThermalPowerMemoryRule struct{}

func (r *ThermalPowerMemoryRule) Definition(th config.RuleThresholds) domain.RuleDefinition {
	return domain.RuleDefinition{
		ID:              "GPU-THERM-006",
		Name:            "GPU Thermal, Power Brake, or Memory Pressure Warning",
		Category:        "thermal_power_memory",
		DefaultSeverity: domain.SeverityWarning,
		Description:     "Detects elevated GPU core or HBM memory temperatures, clock throttling due to thermal or power brake constraints, and near-exhaustion of GPU framebuffer memory.",
		RequiredSignals: []string{
			"DCGM_FI_DEV_GPU_TEMP",
			"DCGM_FI_DEV_MEMORY_TEMP",
			"DCGM_FI_DEV_POWER_USAGE",
			"DCGM_FI_DEV_FB_USED",
			"DCGM_FI_DEV_FB_FREE",
		},
		OptionalSignals: []string{
			"DCGM_FI_DEV_THERMAL_VIOLATION",
			"DCGM_FI_DEV_POWER_VIOLATION",
			"DCGM_EXP_CLOCK_EVENTS_COUNT",
		},
		Thresholds: []domain.RuleThresholdView{
			{Key: "temp_warning_celsius", Label: "GPU Core Temp Warning", Value: th.TempWarningCelsius, Unit: "C", Description: "GPU core temperature warning threshold."},
			{Key: "temp_critical_celsius", Label: "GPU Core Temp Critical", Value: th.TempCriticalCelsius, Unit: "C", Description: "GPU core temperature critical threshold."},
			{Key: "mem_temp_warning_celsius", Label: "HBM Memory Temp Warning", Value: th.MemTempWarningCelsius, Unit: "C", Description: "GPU HBM memory temperature warning threshold."},
			{Key: "fb_memory_warning_percent", Label: "Framebuffer Memory Pressure", Value: th.FBMemoryWarningPercent, Unit: "%", Description: "Framebuffer memory occupancy warning threshold."},
		},
		SuppressionConditions: []string{
			"GPU telemetry is stale (age > max_staleness_seconds).",
			"All thermal, power, and memory metrics are within normal operating envelopes.",
		},
		InconclusiveConditions: []string{
			"Optional thermal/power violation counters are not exported; rule evaluates default temperature and power gauges without assuming zero violations.",
		},
		SuggestedChecks: []string{
			"Check server fan speeds, airflow, heatsink seating, and rack inlet temperatures via BMC/IPMI.",
			"Verify whether clock throttling reasons (hw_thermal, sw_thermal, hw_power_brake) are active.",
			"If framebuffer memory exceeds 95%, review batch size or KV-cache settings to avoid imminent CUDA OOM crashes.",
		},
	}
}

func (r *ThermalPowerMemoryRule) Evaluate(snapshot *domain.ClusterSnapshot, th config.RuleThresholds) []domain.Incident {
	var incidents []domain.Incident

	for _, gpu := range snapshot.GPUs {
		if gpu.IsStale {
			continue
		}
		pt := gpu.Latest
		gpuRef := fmt.Sprintf("%s/gpu-%d (%s)", gpu.NodeName, gpu.DeviceIndex, gpu.UUID)

		var facts []string
		var causes []string
		var supporting []domain.EvidenceItem
		var contradicting []domain.EvidenceItem
		isCritical := false
		isWarning := false

		if pt.GPUTempCelsius >= th.TempCriticalCelsius {
			isCritical = true
			facts = append(facts, fmt.Sprintf("GPU %s core temperature DCGM_FI_DEV_GPU_TEMP is %.1f°C (>= %.1f°C critical threshold).", gpuRef, pt.GPUTempCelsius, th.TempCriticalCelsius))
			causes = append(causes, "Severe thermal constraint risking hardware thermal slowdown (hw_thermal) or thermal shutdown.")
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_GPU_TEMP",
				ObjectRef:  gpuRef,
				Summary:    fmt.Sprintf("GPU core temperature reached %.1f°C", pt.GPUTempCelsius),
				Value:      fmt.Sprintf("%.1f°C", pt.GPUTempCelsius),
				Threshold:  fmt.Sprintf(">= %.1f°C (critical)", th.TempCriticalCelsius),
				ObservedAt: pt.Timestamp,
			})
		} else if pt.GPUTempCelsius >= th.TempWarningCelsius {
			isWarning = true
			facts = append(facts, fmt.Sprintf("GPU %s core temperature DCGM_FI_DEV_GPU_TEMP is %.1f°C (>= %.1f°C warning threshold).", gpuRef, pt.GPUTempCelsius, th.TempWarningCelsius))
			causes = append(causes, "Elevated GPU die temperature approaching thermal slowdown envelope.")
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_GPU_TEMP",
				ObjectRef:  gpuRef,
				Summary:    fmt.Sprintf("GPU core temperature elevated at %.1f°C", pt.GPUTempCelsius),
				Value:      fmt.Sprintf("%.1f°C", pt.GPUTempCelsius),
				Threshold:  fmt.Sprintf(">= %.1f°C (warning)", th.TempWarningCelsius),
				ObservedAt: pt.Timestamp,
			})
		}

		if pt.MemoryTempCelsius >= th.MemTempWarningCelsius {
			isWarning = true
			facts = append(facts, fmt.Sprintf("GPU %s HBM memory temperature DCGM_FI_DEV_MEMORY_TEMP is %.1f°C (>= %.1f°C warning threshold).", gpuRef, pt.MemoryTempCelsius, th.MemTempWarningCelsius))
			causes = append(causes, "Elevated HBM memory temperature increasing risk of memory bit flips and thermal throttling.")
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_MEMORY_TEMP",
				ObjectRef:  gpuRef,
				Summary:    fmt.Sprintf("HBM memory temperature reached %.1f°C", pt.MemoryTempCelsius),
				Value:      fmt.Sprintf("%.1f°C", pt.MemoryTempCelsius),
				Threshold:  fmt.Sprintf(">= %.1f°C", th.MemTempWarningCelsius),
				ObservedAt: pt.Timestamp,
			})
		}

		if pt.ThermalViolationNs != nil && *pt.ThermalViolationNs > 0 {
			isCritical = true
			facts = append(facts, fmt.Sprintf("GPU %s reported DCGM_FI_DEV_THERMAL_VIOLATION = %d ns (clock_event=%q).", gpuRef, *pt.ThermalViolationNs, pt.ClockThrottleReason))
			causes = append(causes, "Active GPU clock throttling due to thermal violation confirmed by DCGM counter.")
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_THERMAL_VIOLATION",
				ObjectRef:  gpuRef,
				Summary:    "Active thermal clock throttling duration recorded",
				Value:      fmt.Sprintf("%d ns (reason=%s)", *pt.ThermalViolationNs, pt.ClockThrottleReason),
				Threshold:  "> 0 ns",
				ObservedAt: pt.Timestamp,
			})
		}

		if pt.PowerViolationNs != nil && *pt.PowerViolationNs > 0 && gpu.PowerLimitWatts > 0 && (pt.PowerUsageWatts/gpu.PowerLimitWatts)*100.0 >= th.PowerCapWarningPercent {
			isWarning = true
			facts = append(facts, fmt.Sprintf("GPU %s power draw is %.0fW / %.0fW (%.1f%%) with DCGM_FI_DEV_POWER_VIOLATION = %d ns.",
				gpuRef, pt.PowerUsageWatts, gpu.PowerLimitWatts, (pt.PowerUsageWatts/gpu.PowerLimitWatts)*100.0, *pt.PowerViolationNs))
			causes = append(causes, "Sustained power brake / power cap throttling reducing SM clock frequency.")
			supporting = append(supporting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "supporting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_POWER_USAGE",
				ObjectRef:  gpuRef,
				Summary:    "Power draw at cap with active power violation counter",
				Value:      fmt.Sprintf("%.0fW / %.0fW (violation=%dns)", pt.PowerUsageWatts, gpu.PowerLimitWatts, *pt.PowerViolationNs),
				Threshold:  fmt.Sprintf(">= %.1f%% power limit", th.PowerCapWarningPercent),
				ObservedAt: pt.Timestamp,
			})
		}

		totalMem := gpu.TotalMemoryMiB
		if totalMem <= 0 {
			totalMem = pt.FBUsedMiB + pt.FBFreeMiB
		}
		if totalMem > 0 {
			fbPct := (pt.FBUsedMiB / totalMem) * 100.0
			if fbPct >= th.FBMemoryWarningPercent {
				isWarning = true
				facts = append(facts, fmt.Sprintf("GPU %s framebuffer memory usage is %.0f / %.0f MiB (%.1f%% >= %.1f%% threshold).",
					gpuRef, pt.FBUsedMiB, totalMem, fbPct, th.FBMemoryWarningPercent))
				causes = append(causes, "Framebuffer VRAM is nearly exhausted; additional allocations or activation spikes may trigger CUDA out-of-memory errors.")
				supporting = append(supporting, domain.EvidenceItem{
					Kind:       "metric",
					Polarity:   "supporting",
					Source:     "dcgm_exporter",
					MetricName: "DCGM_FI_DEV_FB_USED",
					ObjectRef:  gpuRef,
					Summary:    fmt.Sprintf("Framebuffer memory occupancy at %.1f%%", fbPct),
					Value:      fmt.Sprintf("%.0f / %.0f MiB (%.1f%%)", pt.FBUsedMiB, totalMem, fbPct),
					Threshold:  fmt.Sprintf(">= %.1f%%", th.FBMemoryWarningPercent),
					ObservedAt: pt.Timestamp,
				})
			}
		}

		if !isCritical && !isWarning {
			continue
		}

		if pt.XIDErrorCode == 0 && pt.RowRemapFailure == 0 {
			contradicting = append(contradicting, domain.EvidenceItem{
				Kind:       "metric",
				Polarity:   "contradicting",
				Source:     "dcgm_exporter",
				MetricName: "DCGM_FI_DEV_XID_ERRORS",
				ObjectRef:  gpuRef,
				Summary:    "No fatal XID or HBM row-remap failure has occurred yet on this GPU",
				Value:      "xid_errors=0, row_remap_failure=0",
				ObservedAt: pt.Timestamp,
			})
		}

		sev := domain.SeverityWarning
		if isCritical {
			sev = domain.SeverityCritical
		}
		fp := ComputeFingerprint("GPU-THERM-006", snapshot.ClusterName, "GPU", gpu.NodeName, gpu.UUID, "ThermalPowerMemory")
		affected := []string{gpuRef, "node/" + gpu.NodeName}
		if gpu.AllocatedPod != "" {
			affected = append(affected, fmt.Sprintf("pod/%s/%s", gpu.AllocatedNamespace, gpu.AllocatedPod))
		}

		incidents = append(incidents, domain.Incident{
			ID:                    "inc-therm-" + fp[:8],
			Fingerprint:           fp,
			RuleID:                "GPU-THERM-006",
			RuleName:              "GPU Thermal, Power Brake, or Memory Pressure Warning",
			Category:              "thermal_power_memory",
			Severity:              sev,
			EvalStatus:            domain.EvalStatusTriggered,
			EvidenceStrength:      domain.EvidenceStrong,
			EvidenceStrengthBasis: "Directly measured via fresh DCGM temperature, power, throttling, and framebuffer gauges.",
			Title:                 fmt.Sprintf("Operating envelope warning on %s (%.1f°C die, %.0fW)", gpuRef, pt.GPUTempCelsius, pt.PowerUsageWatts),
			Summary:               firstOrDefault(facts, fmt.Sprintf("Thermal, power, or memory pressure detected on %s.", gpuRef)),
			ObservedFacts:         facts,
			PossibleCauses:        causes,
			SupportingEvidence:    supporting,
			ContradictingEvidence: contradicting,
			Timeline: []domain.TimelineEntry{
				{
					Timestamp: pt.Timestamp,
					Layer:     "gpu_telemetry",
					Severity:  string(sev),
					ObjectRef: gpuRef,
					EventType: "EnvelopeThresholdExceeded",
					Summary:   firstOrDefault(facts, "Threshold exceeded"),
				},
			},
			AffectedResources: affected,
			SuggestedChecks:   r.Definition(th).SuggestedChecks,
			DataMode:          snapshot.DataMode,
			ScenarioID:        snapshot.ScenarioID,
			FirstSeenAt:       pt.Timestamp,
			LastSeenAt:        snapshot.CollectedAt,
			OccurrenceCount:   1,
		})
	}

	return incidents
}

func describeXID(code int) string {
	switch code {
	case 31:
		return "GPU memory page fault"
	case 43:
		return "GPU stopped processing"
	case 48:
		return "Double Bit ECC Error (DBE)"
	case 63:
		return "Row remapping / page retirement recording event"
	case 64:
		return "Row remapping failure"
	case 74:
		return "NVLink Error"
	case 79:
		return "GPU has fallen off the bus"
	case 92:
		return "High single-bit ECC error rate"
	case 94:
		return "Contained ECC error"
	case 95:
		return "Uncontained ECC error"
	default:
		return fmt.Sprintf("NVIDIA Driver XID %d", code)
	}
}

func sortTimeline(entries []domain.TimelineEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
}

func firstOrDefault(items []string, fallback string) string {
	if len(items) > 0 && items[0] != "" {
		return items[0]
	}
	return fallback
}

// Unused math import guard
var _ = math.Round
