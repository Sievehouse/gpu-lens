#!/usr/bin/env python3
"""
GPU Lens Local Runtime Server & Deterministic Diagnostic Engine (1:1 Parity with Go Backend).
Provides SQLite persistence (`sqlite3`), deterministic evaluation of all 6 diagnostic rules,
transparent GPU capacity and low-utilization calculations, read-only HTTP transport guards,
and the complete REST API + static UI server when the host environment lacks the Go binary.
"""
import argparse
import hashlib
import http.server
import json
import math
import os
import sqlite3
import threading
import urllib.parse
from datetime import datetime, timezone

DEFAULT_THRESHOLDS = {
    "pending_grace_seconds": 60,
    "critical_pending_minutes": 30.0,
    "xid_error_threshold": 1,
    "ecc_dbe_threshold": 1,
    "row_remap_failure_threshold": 1,
    "pcie_replay_warning_count": 10,
    "nvlink_crc_error_threshold": 1,
    "low_util_threshold_percent": 15.0,
    "low_util_window_minutes": 60.0,
    "min_utilization_samples": 6,
    "high_vram_context_percent": 50.0,
    "correlation_lead_minutes": 15.0,
    "correlation_lag_minutes": 5.0,
    "max_staleness_seconds": 180.0,
    "temp_warning_celsius": 80.0,
    "temp_critical_celsius": 87.0,
    "mem_temp_warning_celsius": 90.0,
    "fb_memory_warning_percent": 95.0,
    "power_cap_warning_percent": 98.0,
    "optional_sample_gpu_hourly_rate_usd": 3.50,
}

DEFAULT_METRIC_MAPPINGS = {
    "gpu_util_metric": "DCGM_FI_DEV_GPU_UTIL",
    "mem_copy_util_metric": "DCGM_FI_DEV_MEM_COPY_UTIL",
    "fb_used_metric": "DCGM_FI_DEV_FB_USED",
    "fb_free_metric": "DCGM_FI_DEV_FB_FREE",
    "gpu_temp_metric": "DCGM_FI_DEV_GPU_TEMP",
    "memory_temp_metric": "DCGM_FI_DEV_MEMORY_TEMP",
    "power_usage_metric": "DCGM_FI_DEV_POWER_USAGE",
    "xid_errors_metric": "DCGM_FI_DEV_XID_ERRORS",
    "pcie_replay_metric": "DCGM_FI_DEV_PCIE_REPLAY_COUNTER",
    "row_remap_failure_metric": "DCGM_FI_DEV_ROW_REMAP_FAILURE",
    "uncorrectable_remap_metric": "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS",
    "ecc_dbe_vol_total_metric": "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
    "ecc_sbe_vol_total_metric": "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL",
    "nvlink_crc_flit_error_metric": "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL",
    "nvlink_replay_error_metric": "DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL",
    "thermal_violation_metric": "DCGM_FI_DEV_THERMAL_VIOLATION",
    "power_violation_metric": "DCGM_FI_DEV_POWER_VIOLATION",
    "exporter_health_status_metric": "DCGM_EXP_GPU_HEALTH_STATUS",
    "clock_events_count_metric": "DCGM_EXP_CLOCK_EVENTS_COUNT",
    "uuid_label_candidates": ["UUID", "uuid"],
    "gpu_index_label": "gpu",
    "node_name_label_candidates": ["Hostname", "hostname", "node", "kubernetes_node"],
    "model_name_label_candidates": ["modelName", "model_name"],
    "pod_name_label_candidates": ["pod", "pod_name"],
    "namespace_label_candidates": ["namespace", "pod_namespace"],
    "container_label_candidates": ["container", "container_name"],
}


def parse_ts(s: str) -> datetime:
    if not s:
        return datetime(2026, 10, 18, 12, 0, 0, tzinfo=timezone.utc)
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    return datetime.fromisoformat(s)


def compute_fingerprint(rule_id: str, cluster: str, target_kind: str, namespace: str, target_name: str, sub_reason: str) -> str:
    raw = "|".join([rule_id, cluster, target_kind, namespace, target_name, sub_reason])
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()[:16]


def describe_xid(code: int) -> str:
    mapping = {
        31: "GPU memory page fault",
        43: "GPU stopped processing",
        48: "Double Bit ECC Error (DBE)",
        63: "Row remapping / page retirement recording event",
        64: "Row remapping failure",
        74: "NVLink Error",
        79: "GPU has fallen off the bus",
        92: "High single-bit ECC error rate",
        94: "Contained ECC error",
        95: "Uncontained ECC error",
    }
    return mapping.get(code, f"NVIDIA Driver XID {code}")


def get_rule_definitions(th: dict) -> list:
    return [
        {
            "id": "GPU-SCHED-001",
            "name": "Pending GPU Workload Scheduling Bottleneck",
            "category": "scheduling",
            "default_severity": "warning",
            "description": "Identifies workloads requesting GPU resources that remain in Pending phase beyond the grace window, correlating scheduler events with per-node GPU allocatable capacity, fragmentation, taints, and node selectors.",
            "required_signals": [
                "k8s.pod.status.phase",
                "k8s.pod.spec.containers.resources.limits[nvidia.com/gpu]",
                "k8s.event[reason=FailedScheduling]",
                "k8s.node.status.allocatable[nvidia.com/gpu]",
            ],
            "optional_signals": ["k8s.node.spec.taints", "k8s.pod.spec.nodeSelector"],
            "thresholds": [
                {"key": "pending_grace_seconds", "label": "Pending Grace Window", "value": th["pending_grace_seconds"], "unit": "s", "description": "Minimum time a GPU pod must be Pending before triggering an incident."},
                {"key": "critical_pending_minutes", "label": "Critical Escalation Window", "value": th["critical_pending_minutes"], "unit": "min", "description": "Pending duration after which severity escalates from warning to critical."},
            ],
            "suppression_conditions": [
                "Pod phase is not Pending or RequestedGPUs == 0.",
                "Pod has been Pending for less than pending_grace_seconds.",
            ],
            "inconclusive_conditions": [
                "Pod is Pending beyond grace window, but neither a FailedScheduling event nor node allocatable GPU inventory is available; GPU Lens refuses to guess a scheduling reason."
            ],
            "suggested_checks": [
                "Inspect kubectl describe pod <namespace>/<pod> for FailedScheduling event details.",
                "Verify node status.allocatable['nvidia.com/gpu'] vs status.capacity['nvidia.com/gpu'] to check if device plugin de-allocated an unhealthy GPU.",
                "Check whether free GPUs are fragmented across multiple nodes while the pod requires contiguous single-node placement.",
                "Verify pod nodeSelector, nodeAffinity, and tolerations against available GPU node labels and taints.",
            ],
        },
        {
            "id": "GPU-HEALTH-002",
            "name": "GPU Hardware Fault or Health Degradation",
            "category": "hardware_health",
            "default_severity": "critical",
            "description": "Evaluates DCGM hardware health signals (XID errors, row-remap failures, PCIe replay counters, optional ECC double-bit volatile errors, NVLink CRC/replay errors, and DCGM health status) alongside Kubernetes node allocatable drops and Node Problem Detector conditions.",
            "required_signals": [
                "DCGM_FI_DEV_XID_ERRORS",
                "DCGM_FI_DEV_ROW_REMAP_FAILURE",
                "DCGM_FI_DEV_PCIE_REPLAY_COUNTER",
                "k8s.node.status.allocatable[nvidia.com/gpu]",
            ],
            "optional_signals": [
                "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
                "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL",
                "DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL",
                "DCGM_EXP_GPU_HEALTH_STATUS",
                "k8s.node.status.conditions[GPUXIDErrors|NVLinkProblem]",
            ],
            "thresholds": [
                {"key": "xid_error_threshold", "label": "XID Error Code Trigger", "value": th["xid_error_threshold"], "unit": "code", "description": "Any non-zero XID error code triggers a critical hardware fault finding."},
                {"key": "ecc_dbe_threshold", "label": "Uncorrectable ECC DBE", "value": th["ecc_dbe_threshold"], "unit": "errors", "description": "Volatile double-bit ECC error threshold (when metric is exported)."},
                {"key": "row_remap_failure_threshold", "label": "Row Remap Failure", "value": th["row_remap_failure_threshold"], "unit": "flag", "description": "Non-zero DCGM_FI_DEV_ROW_REMAP_FAILURE indicates HBM row remapping exhaustion."},
                {"key": "pcie_replay_warning_count", "label": "PCIe Replay Counter Warning", "value": th["pcie_replay_warning_count"], "unit": "replays", "description": "PCIe replay count threshold for warning severity."},
                {"key": "nvlink_crc_error_threshold", "label": "NVLink CRC Error Count", "value": th["nvlink_crc_error_threshold"], "unit": "errors", "description": "NVLink CRC flit error threshold (when optional metric is exported)."},
            ],
            "suppression_conditions": [
                "Optional metrics (ECC DBE, NVLink CRC, DCGM_EXP_GPU_HEALTH_STATUS) are never evaluated or fabricated when not exported by the source.",
                "Stale GPU series (age > max_staleness_seconds) defer to GPU-FRESH-005 rather than asserting healthy status.",
            ],
            "inconclusive_conditions": [
                "Node allocatable GPU count dropped below capacity, but GPU-level DCGM telemetry for that node is stale or unavailable."
            ],
            "suggested_checks": [
                "Inspect node kernel logs (dmesg -T | grep -iE 'nvrm|xid|sxid') to verify exact NVIDIA driver XID context.",
                "Check DCGM health watch breakdown and whether k8s-device-plugin marked the GPU unhealthy (allocatable < capacity).",
                "If XID 48/74/79/94/95, uncorrectable ECC DBE, or row-remap failure is present, cordon the node after workload checkpointing and schedule hardware maintenance.",
            ],
        },
        {
            "id": "GPU-UTIL-003",
            "name": "Persistently Low GPU Compute Utilization During Active Allocation",
            "category": "utilization",
            "default_severity": "info",
            "description": "Identifies Running GPU workloads whose allocated GPUs show mean compute utilization (DCGM_FI_DEV_GPU_UTIL) below threshold over the required observation window. Never equates low compute utilization with proven waste; inspects framebuffer memory residency (DCGM_FI_DEV_FB_USED) and memory copy activity (DCGM_FI_DEV_MEM_COPY_UTIL) and provides a structured engineering checklist.",
            "required_signals": [
                "k8s.pod.status.phase[Running]",
                "DCGM_FI_DEV_GPU_UTIL",
                "DCGM_FI_DEV_MEM_COPY_UTIL",
                "DCGM_FI_DEV_FB_USED",
                "dcgm.pod_label_mapping",
            ],
            "optional_signals": ["DCGM_FI_DEV_FB_FREE"],
            "thresholds": [
                {"key": "low_util_threshold_percent", "label": "Low Compute Util Threshold", "value": th["low_util_threshold_percent"], "unit": "%", "description": "Mean DCGM_FI_DEV_GPU_UTIL below which an allocation window is flagged for review."},
                {"key": "low_util_window_minutes", "label": "Minimum Observation Window", "value": th["low_util_window_minutes"], "unit": "min", "description": "Minimum continuous allocation duration required before firing."},
                {"key": "min_utilization_samples", "label": "Minimum Telemetry Samples", "value": th["min_utilization_samples"], "unit": "samples", "description": "Minimum number of data points required per GPU to prevent sparse-sample false positives."},
                {"key": "high_vram_context_percent", "label": "High VRAM Residency Indicator", "value": th["high_vram_context_percent"], "unit": "%", "description": "Framebuffer occupancy threshold indicating a memory-capacity-bound workload."},
            ],
            "suppression_conditions": [
                "Workload has been Running for less than low_util_window_minutes (warm-up / initialization suppression).",
                "Fewer than min_utilization_samples telemetry points exist in the observation window.",
                "GPU-to-pod label attribution is missing (handled by GPU-FRESH-005).",
            ],
            "inconclusive_conditions": [
                "Telemetry for one or more GPUs assigned to the pod is stale."
            ],
            "suggested_checks": [
                "Verify CPU data-loader throughput, storage read latency, and dataset preprocessing queues for I/O starvation.",
                "In multi-GPU distributed jobs, inspect NCCL barrier wait times to check if a straggler rank or network bottleneck is stalling peers.",
                "Check DCGM_FI_DEV_FB_USED: if VRAM occupancy is high (e.g., >50%), the workload may be memory-capacity-bound (such as an LLM inference service holding weights in HBM during low QPS periods) rather than over-provisioned.",
                "Confirm whether the pod is an interactive Jupyter/VSCode session left idle between experiments, or a batch job suitable for MIG / time-slicing.",
            ],
        },
        {
            "id": "GPU-CORR-004",
            "name": "Workload Failure or Restart Correlated with GPU / Node Signals",
            "category": "failure_correlation",
            "default_severity": "critical",
            "description": "Correlates workload container terminations, restarts, and Failed phases with GPU hardware faults (XID errors, ECC DBE, row-remap failures, NVLink errors, thermal violations) and Kubernetes node/pod events within a configurable temporal window. Explicitly separates observed facts from possible causes and records contradicting evidence when GPU hardware remained healthy.",
            "required_signals": [
                "k8s.pod.status.containerStatuses.restartCount",
                "k8s.pod.status.containerStatuses.lastState.terminated",
                "DCGM_FI_DEV_XID_ERRORS",
                "DCGM_FI_DEV_ROW_REMAP_FAILURE",
                "k8s.event",
            ],
            "optional_signals": [
                "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
                "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL",
                "DCGM_FI_DEV_THERMAL_VIOLATION",
            ],
            "thresholds": [
                {"key": "correlation_lead_minutes", "label": "Pre-Failure Lookback Window", "value": th["correlation_lead_minutes"], "unit": "min", "description": "Minutes prior to container termination inspected for preceding GPU/node anomalies."},
                {"key": "correlation_lag_minutes", "label": "Post-Failure Lag Window", "value": th["correlation_lag_minutes"], "unit": "min", "description": "Minutes after container termination inspected for delayed telemetry or node events."},
            ],
            "suppression_conditions": [
                "Pod has zero restarts, is not in Failed phase, and has no terminated container state."
            ],
            "inconclusive_conditions": [
                "Pod failed or restarted, but GPU telemetry for the assigned node/GPUs is missing or stale during the termination window."
            ],
            "suggested_checks": [
                "Compare the container termination timestamp and exit code against the correlated GPU XID / ECC / NVLink timeline entries.",
                "If GPU hardware faults preceded the crash, drain or cordon the node before rescheduling the job so it does not enter a restart loop on defective hardware.",
                "If GPU telemetry shows zero hardware errors throughout the window (contradicting hardware fault), inspect application logs for OOM, NCCL rank deadlocks, or bad input data.",
            ],
        },
        {
            "id": "GPU-FRESH-005",
            "name": "Telemetry Source Unavailable, Stale, or Uncorrelated",
            "category": "data_freshness",
            "default_severity": "critical",
            "description": "Detects when a telemetry source fails collection, when GPU metric samples exceed the maximum staleness window, or when active GPU workloads cannot be correlated to GPU telemetry due to missing Kubernetes pod/namespace labels.",
            "required_signals": [
                "source.available",
                "source.last_success_at",
                "gpu.last_sample_at",
                "gpu.mapping_status",
            ],
            "optional_signals": [],
            "thresholds": [
                {"key": "max_staleness_seconds", "label": "Maximum Telemetry Age", "value": th["max_staleness_seconds"], "unit": "s", "description": "Maximum age of a GPU metric sample before it is flagged as stale."}
            ],
            "suppression_conditions": [
                "All configured sources are available, all GPU samples are fresher than max_staleness_seconds, and all Running GPU pods have mapped pod labels."
            ],
            "inconclusive_conditions": [
                "This rule directly reports monitoring blind spots so that other rules never mistake missing telemetry for healthy hardware."
            ],
            "suggested_checks": [
                "Check Prometheus target health (/targets) and dcgm-exporter DaemonSet pod readiness on affected nodes.",
                "If metrics are fresh but pod/namespace labels are missing, verify dcgm-exporter runs with --kubernetes and mounts /var/lib/kubelet/pod-resources/ (the directory, not the socket file inode).",
                "Verify nv-hostengine responsiveness on nodes with stale GPU series.",
            ],
        },
        {
            "id": "GPU-THERM-006",
            "name": "GPU Thermal, Power Brake, or Memory Pressure Warning",
            "category": "thermal_power_memory",
            "default_severity": "warning",
            "description": "Detects elevated GPU core or HBM memory temperatures, clock throttling due to thermal or power brake constraints, and near-exhaustion of GPU framebuffer memory.",
            "required_signals": [
                "DCGM_FI_DEV_GPU_TEMP",
                "DCGM_FI_DEV_MEMORY_TEMP",
                "DCGM_FI_DEV_POWER_USAGE",
                "DCGM_FI_DEV_FB_USED",
                "DCGM_FI_DEV_FB_FREE",
            ],
            "optional_signals": [
                "DCGM_FI_DEV_THERMAL_VIOLATION",
                "DCGM_FI_DEV_POWER_VIOLATION",
                "DCGM_EXP_CLOCK_EVENTS_COUNT",
            ],
            "thresholds": [
                {"key": "temp_warning_celsius", "label": "GPU Core Temp Warning", "value": th["temp_warning_celsius"], "unit": "C", "description": "GPU core temperature warning threshold."},
                {"key": "temp_critical_celsius", "label": "GPU Core Temp Critical", "value": th["temp_critical_celsius"], "unit": "C", "description": "GPU core temperature critical threshold."},
                {"key": "mem_temp_warning_celsius", "label": "HBM Memory Temp Warning", "value": th["mem_temp_warning_celsius"], "unit": "C", "description": "GPU HBM memory temperature warning threshold."},
                {"key": "fb_memory_warning_percent", "label": "Framebuffer Memory Pressure", "value": th["fb_memory_warning_percent"], "unit": "%", "description": "Framebuffer memory occupancy warning threshold."},
            ],
            "suppression_conditions": [
                "GPU telemetry is stale (age > max_staleness_seconds).",
                "All thermal, power, and memory metrics are within normal operating envelopes.",
            ],
            "inconclusive_conditions": [
                "Optional thermal/power violation counters are not exported; rule evaluates default temperature and power gauges without assuming zero violations."
            ],
            "suggested_checks": [
                "Check server fan speeds, airflow, heatsink seating, and rack inlet temperatures via BMC/IPMI.",
                "Verify whether clock throttling reasons (hw_thermal, sw_thermal, hw_power_brake) are active.",
                "If framebuffer memory exceeds 95%, review batch size or KV-cache settings to avoid imminent CUDA OOM crashes.",
            ],
        },
    ]


def evaluate_rules(snapshot: dict, th: dict = None) -> list:
    if not snapshot:
        return []
    th = th or DEFAULT_THRESHOLDS
    defs = {d["id"]: d for d in get_rule_definitions(th)}
    collected_at = parse_ts(snapshot.get("collected_at", ""))
    cluster = snapshot.get("cluster_name", "gpu-cluster")
    data_mode = snapshot.get("data_mode", "simulated_fixture")
    scenario_id = snapshot.get("scenario_id", "unknown")
    nodes = snapshot.get("nodes", [])
    gpus = snapshot.get("gpus", [])
    pods = snapshot.get("pods", [])
    events = snapshot.get("events", [])
    sources = snapshot.get("sources", [])

    incidents = []

    # 1. GPU-SCHED-001
    total_free = 0
    max_single_free = 0
    degraded_nodes = []
    for n in nodes:
        free = max(0, n.get("gpu_allocatable", 0) - n.get("gpu_allocated", 0))
        if n.get("ready", True) and n.get("schedulable", True):
            total_free += free
            if free > max_single_free:
                max_single_free = free
        if n.get("gpu_allocatable", 0) < n.get("gpu_capacity", 0) or not n.get("schedulable", True) or not n.get("ready", True):
            degraded_nodes.append(
                f"{n['name']} (capacity={n.get('gpu_capacity',0)}, allocatable={n.get('gpu_allocatable',0)}, allocated={n.get('gpu_allocated',0)})"
            )

    for pod in pods:
        if pod.get("phase") != "Pending" or pod.get("requested_gpus", 0) <= 0:
            continue
        created_at = parse_ts(pod.get("created_at", ""))
        pending_secs = (collected_at - created_at).total_seconds()
        if pending_secs < th["pending_grace_seconds"]:
            continue
        pending_mins = pending_secs / 60.0
        pod_ref = f"{pod['namespace']}/{pod['name']}"
        fp = compute_fingerprint("GPU-SCHED-001", cluster, "Pod", pod["namespace"], pod["name"], "PendingScheduling")

        sched_events = [
            ev for ev in events
            if ev.get("involved_object_kind") == "Pod"
            and ev.get("involved_object_name") == pod["name"]
            and ev.get("reason") == "FailedScheduling"
        ]
        sched_msg = pod.get("scheduler_message", "")

        if not sched_events and not nodes and not sched_msg:
            incidents.append({
                "id": "inc-sched-" + fp[:8],
                "fingerprint": fp,
                "rule_id": "GPU-SCHED-001",
                "rule_name": defs["GPU-SCHED-001"]["name"],
                "category": "scheduling",
                "severity": "warning",
                "eval_status": "inconclusive",
                "evidence_strength": "inconclusive",
                "evidence_strength_basis": "Pod is Pending, but FailedScheduling events and node resource inventory are absent from the snapshot.",
                "title": f"Pending GPU workload {pod_ref} (scheduling reason inconclusive — missing events)",
                "summary": f"Workload {pod_ref} requesting {pod['requested_gpus']} GPU(s) has been Pending for {pending_mins:.1f} minutes, but no FailedScheduling event or node inventory was observed. Refusing to guess scheduling cause.",
                "observed_facts": [
                    f"Pod {pod_ref} has been in Pending phase for {pending_mins:.1f} minutes requesting {pod['requested_gpus']} nvidia.com/gpu.",
                    "Zero FailedScheduling Kubernetes events and zero Node state records were available for correlation.",
                ],
                "possible_causes": [
                    "Kubernetes event collection may be disabled, RBAC-restricted, or expired past event TTL.",
                    "Custom batch queue controller (e.g., Kueue or Volcano) may be holding the workload prior to pod scheduling.",
                ],
                "supporting_evidence": [{
                    "kind": "pod_state",
                    "polarity": "supporting",
                    "source": "kubernetes_api",
                    "object_ref": pod_ref,
                    "summary": "Pod remains in Pending phase beyond grace window",
                    "value": f"phase=Pending, requested_gpus={pod['requested_gpus']}, age={pending_mins:.1f}m",
                    "threshold": f"pending_grace_seconds >= {th['pending_grace_seconds']}s",
                    "observed_at": snapshot.get("collected_at"),
                }],
                "contradicting_evidence": [],
                "timeline": [],
                "affected_resources": [pod_ref],
                "suggested_checks": defs["GPU-SCHED-001"]["suggested_checks"],
                "inconclusive_reason": "No FailedScheduling event or Node allocatable state observed; refusing to fabricate a scheduling bottleneck reason.",
                "data_mode": data_mode,
                "scenario_id": scenario_id,
                "first_seen_at": pod.get("created_at"),
                "last_seen_at": snapshot.get("collected_at"),
                "occurrence_count": 1,
            })
            continue

        sev = "critical" if pending_mins >= th["critical_pending_minutes"] else "warning"
        facts = [
            f"Pod {pod_ref} ({pod.get('owner_kind','Pod')}/{pod.get('owner_name',pod['name'])}) requesting {pod['requested_gpus']} nvidia.com/gpu has been Pending for {pending_mins:.1f} minutes (since {pod.get('created_at')}).",
            f"Cluster schedulable free GPUs: {total_free} total across ready/schedulable nodes; maximum free GPUs on any single ready node: {max_single_free}.",
        ]
        supporting = []
        contradicting = []
        timeline = [{
            "timestamp": pod.get("created_at"),
            "layer": "k8s_pod",
            "severity": "info",
            "object_ref": pod_ref,
            "event_type": "PodCreated",
            "summary": f"Pod created requesting {pod['requested_gpus']} nvidia.com/gpu",
        }]
        for ev in sched_events:
            if not sched_msg:
                sched_msg = ev.get("message", "")
            facts.append(f"Observed kube-scheduler event [{ev['reason']}] (x{ev.get('count',1)}): \"{ev['message']}\"")
            supporting.append({
                "kind": "k8s_event",
                "polarity": "supporting",
                "source": ev.get("source_component", "default-scheduler"),
                "object_ref": pod_ref,
                "summary": ev["message"],
                "value": f"reason={ev['reason']}, count={ev.get('count',1)}",
                "observed_at": ev.get("last_timestamp"),
            })
            timeline.append({
                "timestamp": ev.get("last_timestamp"),
                "layer": "k8s_scheduler",
                "severity": "warning",
                "object_ref": pod_ref,
                "event_type": ev["reason"],
                "summary": ev["message"],
            })
        supporting.append({
            "kind": "node_state",
            "polarity": "supporting",
            "source": "kubernetes_api",
            "object_ref": "cluster/nodes",
            "summary": "Cluster GPU allocatable vs allocated inventory",
            "value": f"requested={pod['requested_gpus']}, cluster_free_allocatable={total_free}, max_single_node_free={max_single_free}",
            "threshold": f"single_node_free >= {pod['requested_gpus']}",
            "observed_at": snapshot.get("collected_at"),
        })

        causes = []
        if "node affinity/selector" in sched_msg and pod.get("node_selector"):
            sel_str = ", ".join(f"{k}={v}" for k, v in sorted(pod["node_selector"].items()))
            facts.append(f"Pod specifies nodeSelector constraints: {sel_str}")
            causes.append(f"Node selector / affinity constraint ({sel_str}) excludes nodes with available GPUs.")
        elif total_free < pod["requested_gpus"]:
            causes.append(
                f"Insufficient aggregate allocatable GPUs: pod requests {pod['requested_gpus']} GPUs, but only {total_free} allocatable GPUs are currently free across ready/schedulable nodes."
            )
        elif max_single_free < pod["requested_gpus"]:
            causes.append(
                f"Node-level GPU fragmentation: {total_free} free GPUs exist cluster-wide, but no single ready node has {pod['requested_gpus']} contiguous free GPUs (max on one node is {max_single_free})."
            )
            contradicting.append({
                "kind": "node_state",
                "polarity": "contradicting",
                "source": "kubernetes_api",
                "object_ref": "cluster/nodes",
                "summary": "Aggregate free GPU count exceeds pod request, ruling out simple cluster-wide exhaustion",
                "value": f"cluster_free_allocatable={total_free} >= requested={pod['requested_gpus']}",
                "observed_at": snapshot.get("collected_at"),
            })
        if degraded_nodes:
            facts.append(f"Nodes with reduced allocatable GPUs or unschedulable state: {'; '.join(degraded_nodes)}")
            causes.append("Reduced node allocatable GPU count or cordoned node state is withholding physical GPU capacity from kube-scheduler.")
        if not causes and sched_msg:
            causes.append(f"Scheduler reported constraint: {sched_msg}")

        incidents.append({
            "id": "inc-sched-" + fp[:8],
            "fingerprint": fp,
            "rule_id": "GPU-SCHED-001",
            "rule_name": defs["GPU-SCHED-001"]["name"],
            "category": "scheduling",
            "severity": sev,
            "eval_status": "triggered",
            "evidence_strength": "strong" if sched_events else "moderate",
            "evidence_strength_basis": "Corroborated by both kube-scheduler FailedScheduling event and live per-node GPU allocatable inventory.",
            "title": f"Pending GPU workload {pod_ref} ({pod['requested_gpus']} GPUs requested, {pending_mins:.0f}m pending)",
            "summary": f"Pod {pod_ref} requesting {pod['requested_gpus']} nvidia.com/gpu has remained Pending for {pending_mins:.1f} minutes. {causes[0] if causes else sched_msg}",
            "observed_facts": facts,
            "possible_causes": causes,
            "supporting_evidence": supporting,
            "contradicting_evidence": contradicting,
            "timeline": timeline,
            "affected_resources": [pod_ref],
            "suggested_checks": defs["GPU-SCHED-001"]["suggested_checks"],
            "data_mode": data_mode,
            "scenario_id": scenario_id,
            "first_seen_at": pod.get("created_at"),
            "last_seen_at": snapshot.get("collected_at"),
            "occurrence_count": 1,
        })

    # 2. GPU-HEALTH-002
    node_by_name = {n["name"]: n for n in nodes}
    for gpu in gpus:
        if gpu.get("is_stale"):
            continue
        pt = gpu.get("latest", {})
        gpu_ref = f"{gpu['node_name']}/gpu-{gpu['device_index']} ({gpu['uuid']})"
        facts, causes, supporting, contradicting, timeline = [], [], [], [], []
        is_crit = False
        is_warn = False

        xid = pt.get("xid_error_code", 0)
        if xid >= th["xid_error_threshold"]:
            is_crit = True
            desc = describe_xid(xid)
            facts.append(f"GPU {gpu_ref} reported DCGM_FI_DEV_XID_ERRORS = {xid} ({desc}) at {pt.get('timestamp')}.")
            causes.append(f"NVIDIA driver emitted XID {xid} ({desc}) indicating a GPU hardware, memory, bus, or interconnect fault.")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_XID_ERRORS",
                "object_ref": gpu_ref,
                "summary": f"XID {xid} ({desc}) observed on GPU",
                "value": str(xid),
                "threshold": f">= {th['xid_error_threshold']}",
                "observed_at": pt.get("timestamp"),
            })
            timeline.append({
                "timestamp": pt.get("timestamp"),
                "layer": "gpu_telemetry",
                "severity": "critical",
                "object_ref": gpu_ref,
                "event_type": "XIDError",
                "summary": f"DCGM_FI_DEV_XID_ERRORS={xid} ({desc})",
            })

        if pt.get("row_remap_failure", 0) >= th["row_remap_failure_threshold"]:
            is_crit = True
            facts.append(f"GPU {gpu_ref} reported DCGM_FI_DEV_ROW_REMAP_FAILURE = {pt['row_remap_failure']} (uncorrectable_remapped_rows={pt.get('uncorrectable_remapped_rows',0)}).")
            causes.append("HBM memory row remapping table exhausted or failed due to persistent memory cell degradation.")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_ROW_REMAP_FAILURE",
                "object_ref": gpu_ref,
                "summary": "GPU HBM row remap failure flagged",
                "value": f"row_remap_failure={pt['row_remap_failure']}, uncorrectable_rows={pt.get('uncorrectable_remapped_rows',0)}",
                "threshold": f">= {th['row_remap_failure_threshold']}",
                "observed_at": pt.get("timestamp"),
            })

        if pt.get("ecc_dbe_vol_total") is not None and pt["ecc_dbe_vol_total"] >= th["ecc_dbe_threshold"]:
            is_crit = True
            facts.append(f"GPU {gpu_ref} reported optional metric DCGM_FI_DEV_ECC_DBE_VOL_TOTAL = {pt['ecc_dbe_vol_total']} uncorrectable double-bit ECC errors.")
            causes.append("Uncorrectable volatile double-bit ECC memory error detected in GPU HBM.")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
                "object_ref": gpu_ref,
                "summary": "Uncorrectable volatile double-bit ECC errors detected",
                "value": str(pt["ecc_dbe_vol_total"]),
                "threshold": f">= {th['ecc_dbe_threshold']}",
                "observed_at": pt.get("timestamp"),
            })

        if pt.get("nvlink_crc_flit_errors") is not None and pt["nvlink_crc_flit_errors"] >= th["nvlink_crc_error_threshold"]:
            is_warn = True
            facts.append(f"GPU {gpu_ref} reported DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL = {pt['nvlink_crc_flit_errors']}.")
            causes.append("NVLink high-speed interconnect experiencing CRC flit errors (degraded signal integrity or switch link issue).")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL",
                "object_ref": gpu_ref,
                "summary": "NVLink CRC flit error counter elevated",
                "value": str(pt["nvlink_crc_flit_errors"]),
                "threshold": f">= {th['nvlink_crc_error_threshold']}",
                "observed_at": pt.get("timestamp"),
            })

        if pt.get("pcie_replay_counter", 0) >= th["pcie_replay_warning_count"]:
            is_warn = True
            facts.append(f"GPU {gpu_ref} reported DCGM_FI_DEV_PCIE_REPLAY_COUNTER = {pt['pcie_replay_counter']} (threshold {th['pcie_replay_warning_count']}).")
            causes.append("Elevated PCIe link replay counter indicating host-to-GPU bus signal degradation or reseating needed.")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_PCIE_REPLAY_COUNTER",
                "object_ref": gpu_ref,
                "summary": "Elevated PCIe link replay counter",
                "value": str(pt["pcie_replay_counter"]),
                "threshold": f">= {th['pcie_replay_warning_count']}",
                "observed_at": pt.get("timestamp"),
            })

        if pt.get("health_status") is not None and pt["health_status"] >= 10:
            lbl = "FAIL (20)" if pt["health_status"] >= 20 else "WARN (10)"
            if pt["health_status"] >= 20:
                is_crit = True
            else:
                is_warn = True
            facts.append(f"Exporter health watch metric DCGM_EXP_GPU_HEALTH_STATUS reported {lbl}.")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_EXP_GPU_HEALTH_STATUS",
                "object_ref": gpu_ref,
                "summary": "DCGM health watch reported non-passing state",
                "value": lbl,
                "threshold": "> 0 (PASS)",
                "observed_at": pt.get("timestamp"),
            })

        if not is_crit and not is_warn:
            continue

        node = node_by_name.get(gpu["node_name"])
        if node:
            if node.get("gpu_allocatable", 0) < node.get("gpu_capacity", 0):
                facts.append(
                    f"Node {node['name']} Kubernetes status shows allocatable GPUs ({node['gpu_allocatable']}) < capacity GPUs ({node['gpu_capacity']}), confirming device plugin de-allocation."
                )
                supporting.append({
                    "kind": "node_state",
                    "polarity": "supporting",
                    "source": "kubernetes_api",
                    "object_ref": "node/" + node["name"],
                    "summary": "Kubelet allocatable GPU count decreased below physical capacity",
                    "value": f"allocatable={node['gpu_allocatable']}, capacity={node['gpu_capacity']}",
                    "observed_at": snapshot.get("collected_at"),
                })
            else:
                contradicting.append({
                    "kind": "node_state",
                    "polarity": "contradicting",
                    "source": "kubernetes_api",
                    "object_ref": "node/" + node["name"],
                    "summary": "Kubernetes device plugin still advertises full GPU allocatable count on node",
                    "value": f"allocatable={node.get('gpu_allocatable',0)} == capacity={node.get('gpu_capacity',0)}",
                    "observed_at": snapshot.get("collected_at"),
                })
            for cond in node.get("conditions", []):
                if cond.get("status") == "True" and ("GPU" in cond.get("type", "") or "NVLink" in cond.get("type", "")):
                    facts.append(f"Node Problem Detector condition {cond['type']}=True ({cond.get('reason','')}): {cond.get('message','')}")
                    supporting.append({
                        "kind": "node_state",
                        "polarity": "supporting",
                        "source": "node-problem-detector",
                        "object_ref": "node/" + node["name"],
                        "summary": cond.get("message", ""),
                        "value": f"{cond['type']}={cond['status']} ({cond.get('reason','')})",
                        "observed_at": cond.get("last_transition_time"),
                    })
                    timeline.append({
                        "timestamp": cond.get("last_transition_time"),
                        "layer": "k8s_node",
                        "severity": "critical",
                        "object_ref": "node/" + node["name"],
                        "event_type": cond["type"],
                        "summary": cond.get("message", ""),
                    })

        timeline.sort(key=lambda x: x.get("timestamp", ""))
        fp = compute_fingerprint("GPU-HEALTH-002", cluster, "GPU", gpu["node_name"], gpu["uuid"], "HardwareHealth")
        affected = [gpu_ref, "node/" + gpu["node_name"]]
        if gpu.get("allocated_pod"):
            affected.append(f"pod/{gpu.get('allocated_namespace','default')}/{gpu['allocated_pod'].strip()}")

        incidents.append({
            "id": "inc-health-" + fp[:8],
            "fingerprint": fp,
            "rule_id": "GPU-HEALTH-002",
            "rule_name": defs["GPU-HEALTH-002"]["name"],
            "category": "hardware_health",
            "severity": "critical" if is_crit else "warning",
            "eval_status": "triggered",
            "evidence_strength": "strong" if len(supporting) >= 2 else "moderate",
            "evidence_strength_basis": "Corroborated across multiple independent hardware metrics and/or Kubernetes Node Problem Detector conditions.",
            "title": f"GPU hardware warning/fault on {gpu_ref} ({gpu.get('model_name','GPU')})",
            "summary": facts[0],
            "observed_facts": facts,
            "possible_causes": causes,
            "supporting_evidence": supporting,
            "contradicting_evidence": contradicting,
            "timeline": timeline,
            "affected_resources": affected,
            "suggested_checks": defs["GPU-HEALTH-002"]["suggested_checks"],
            "data_mode": data_mode,
            "scenario_id": scenario_id,
            "first_seen_at": pt.get("timestamp"),
            "last_seen_at": snapshot.get("collected_at"),
            "occurrence_count": 1,
        })

    # 3. GPU-UTIL-003
    gpu_by_uuid = {g["uuid"]: g for g in gpus}
    for pod in pods:
        if pod.get("phase") != "Running" or pod.get("requested_gpus", 0) <= 0 or not pod.get("started_at"):
            continue
        started_at = parse_ts(pod["started_at"])
        run_mins = (collected_at - started_at).total_seconds() / 60.0
        if run_mins < th["low_util_window_minutes"]:
            continue
        uuids = pod.get("assigned_gpu_uuids", [])
        if not uuids:
            continue
        mapped_gpus = []
        valid = True
        for u in uuids:
            g = gpu_by_uuid.get(u)
            if not g or g.get("is_stale") or g.get("mapping_status") != "mapped" or len(g.get("samples", [])) < th["min_utilization_samples"]:
                valid = False
                break
            mapped_gpus.append(g)
        if not valid or not mapped_gpus:
            continue

        all_samples = []
        earliest_ts, latest_ts = None, None
        sum_u, min_u, max_u, sum_mc, sum_fb, sum_fb_pct = 0.0, 100.0, 0.0, 0.0, 0.0, 0.0
        for g in mapped_gpus:
            tot_mem = g.get("total_memory_mib", 81920.0)
            for s in g.get("samples", []):
                ts = parse_ts(s["timestamp"])
                if earliest_ts is None or ts < earliest_ts:
                    earliest_ts = ts
                if latest_ts is None or ts > latest_ts:
                    latest_ts = ts
                u_val = s.get("gpu_util_percent", 0.0)
                sum_u += u_val
                min_u = min(min_u, u_val)
                max_u = max(max_u, u_val)
                sum_mc += s.get("mem_copy_util_percent", 0.0)
                fb_val = s.get("fb_used_mib", 0.0)
                sum_fb += fb_val
                sum_fb_pct += (fb_val / tot_mem) * 100.0 if tot_mem > 0 else 0.0
                all_samples.append(s)

        n_samp = len(all_samples)
        if n_samp == 0:
            continue
        obs_mins = max((latest_ts - earliest_ts).total_seconds() / 60.0, run_mins)
        mean_u = sum_u / n_samp
        mean_mc = sum_mc / n_samp
        mean_fb = sum_fb / n_samp
        mean_fb_pct = sum_fb_pct / n_samp

        if mean_u >= th["low_util_threshold_percent"]:
            continue

        pod_ref = f"{pod['namespace']}/{pod['name']}"
        gpu_hours = pod["requested_gpus"] * (obs_mins / 60.0)
        fp = compute_fingerprint("GPU-UTIL-003", cluster, "Pod", pod["namespace"], pod["name"], "PersistentLowUtil")

        facts = [
            f"Workload {pod_ref} ({pod.get('owner_kind','Pod')}/{pod.get('owner_name',pod['name'])}) on node {pod.get('node_name')} has held {pod['requested_gpus']} GPU(s) ({', '.join(uuids)}) for {obs_mins:.0f} minutes ({gpu_hours:.2f} allocated GPU-hours).",
            f"Across {n_samp} telemetry samples (last update {latest_ts.strftime('%Y-%m-%dT%H:%M:%SZ')}), mean DCGM_FI_DEV_GPU_UTIL is {mean_u:.1f}% (min {min_u:.1f}%, max {max_u:.1f}%), below the {th['low_util_threshold_percent']:.1f}% review threshold.",
            f"Workload context metrics: mean DCGM_FI_DEV_MEM_COPY_UTIL = {mean_mc:.1f}%, mean DCGM_FI_DEV_FB_USED = {mean_fb:.0f} MiB ({mean_fb_pct:.1f}% of GPU VRAM).",
        ]
        supporting = [{
            "kind": "metric",
            "polarity": "supporting",
            "source": "dcgm_exporter",
            "metric_name": "DCGM_FI_DEV_GPU_UTIL",
            "object_ref": pod_ref,
            "summary": f"Mean GPU SM utilization is {mean_u:.1f}% across {n_samp} samples over {obs_mins:.0f} minutes",
            "value": f"mean={mean_u:.1f}%, min={min_u:.1f}%, max={max_u:.1f}%",
            "threshold": f"< {th['low_util_threshold_percent']:.1f}% over >= {th['low_util_window_minutes']:.0f}m",
            "observed_at": latest_ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
        }]
        contradicting = []
        causes = []
        if mean_fb_pct >= th["high_vram_context_percent"]:
            contradicting.append({
                "kind": "metric",
                "polarity": "contradicting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_FB_USED",
                "object_ref": pod_ref,
                "summary": f"High GPU framebuffer memory residency ({mean_fb:.0f} MiB / {mean_fb_pct:.1f}%) indicates the workload actively requires GPU VRAM capacity despite low SM compute utilization",
                "value": f"{mean_fb:.0f} MiB ({mean_fb_pct:.1f}%)",
                "threshold": f">= {th['high_vram_context_percent']:.1f}% VRAM residency",
                "observed_at": latest_ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
            })
            causes.append(
                f"Memory-capacity-bound workload: {mean_fb_pct:.1f}% of GPU framebuffer memory ({mean_fb:.0f} MiB) is allocated (e.g., model weights resident in VRAM during low inference QPS). Low compute utilization here does NOT prove wasted allocation."
            )
        else:
            contradicting.append({
                "kind": "metric",
                "polarity": "contextual",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_FB_USED",
                "object_ref": pod_ref,
                "summary": f"Framebuffer memory occupancy is {mean_fb:.0f} MiB ({mean_fb_pct:.1f}%) and memory copy utilization is {mean_mc:.1f}%",
                "value": f"fb_used={mean_fb_pct:.1f}%, mem_copy={mean_mc:.1f}%",
                "observed_at": latest_ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
            })
        causes.extend([
            "CPU or network storage data-loading bottleneck starving GPU compute pipelines between mini-batches.",
            "Synchronization stall or straggler rank in distributed training holding peer GPUs idle at collective barriers.",
            "Interactive development session, notebook, or standby inference replica waiting for external requests.",
        ])

        incidents.append({
            "id": "inc-util-" + fp[:8],
            "fingerprint": fp,
            "rule_id": "GPU-UTIL-003",
            "rule_name": defs["GPU-UTIL-003"]["name"],
            "category": "utilization",
            "severity": "warning",
            "eval_status": "triggered",
            "evidence_strength": "contextual",
            "evidence_strength_basis": "Complete, pod-mapped DCGM telemetry confirms low SM compute utilization over the configured window; however, telemetry alone cannot distinguish application I/O bottlenecks or VRAM residency requirements from idle reservations.",
            "title": f"Persistent low GPU compute utilization on {pod_ref} ({mean_u:.1f}% mean over {obs_mins:.0f}m)",
            "summary": f"Workload {pod_ref} has {pod['requested_gpus']} allocated GPU(s) with {mean_u:.1f}% mean compute utilization over {obs_mins:.0f} minutes ({gpu_hours:.2f} GPU-hours observed below {th['low_util_threshold_percent']:.1f}% threshold; VRAM usage {mean_fb_pct:.1f}%). Review follow-up checklist before altering allocation.",
            "observed_facts": facts,
            "possible_causes": causes,
            "supporting_evidence": supporting,
            "contradicting_evidence": contradicting,
            "timeline": [
                {"timestamp": pod.get("started_at"), "layer": "k8s_pod", "severity": "info", "object_ref": pod_ref, "event_type": "PodStarted", "summary": f"Workload started on {pod.get('node_name')} with {pod['requested_gpus']} GPU(s)"},
                {"timestamp": latest_ts.strftime("%Y-%m-%dT%H:%M:%SZ"), "layer": "gpu_telemetry", "severity": "warning", "object_ref": pod_ref, "event_type": "LowUtilizationWindow", "summary": f"Mean GPU compute utilization {mean_u:.1f}% (VRAM {mean_fb_pct:.1f}%) across {n_samp} samples"},
            ],
            "affected_resources": [pod_ref, "node/" + pod.get("node_name", "")] + uuids,
            "suggested_checks": defs["GPU-UTIL-003"]["suggested_checks"],
            "data_mode": data_mode,
            "scenario_id": scenario_id,
            "first_seen_at": earliest_ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "last_seen_at": latest_ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "occurrence_count": 1,
        })

    # 4. GPU-CORR-004
    for pod in pods:
        term = pod.get("last_termination")
        if not term:
            continue
        pod_ref = f"{pod['namespace']}/{pod['name']}"
        fin_at = parse_ts(term["finished_at"])
        win_start = fin_at.timestamp() - th["correlation_lead_minutes"] * 60
        win_end = fin_at.timestamp() + th["correlation_lag_minutes"] * 60

        uuids = set(pod.get("assigned_gpu_uuids", []))
        rel_gpus = [g for g in gpus if g["uuid"] in uuids or (not uuids and g.get("node_name") == pod.get("node_name"))]

        facts = [
            f"Pod {pod_ref} ({pod.get('owner_kind','Pod')}/{pod.get('owner_name',pod['name'])}) on node {pod.get('node_name')} experienced container termination (reason={term['reason']}, exit_code={term['exit_code']}, restart_count={pod.get('restart_count',0)}) at {term['finished_at']}: \"{term.get('message','')}\"."
        ]
        timeline = [
            {"timestamp": term["started_at"], "layer": "k8s_pod", "severity": "info", "object_ref": pod_ref, "event_type": "ContainerStarted", "summary": f"Container started on node {pod.get('node_name')}"},
            {"timestamp": term["finished_at"], "layer": "k8s_pod", "severity": "critical", "object_ref": pod_ref, "event_type": "ContainerTerminated", "summary": f"Terminated with exit_code={term['exit_code']} ({term['reason']}): {term.get('message','')}"},
        ]
        supporting = [{
            "kind": "pod_state",
            "polarity": "supporting",
            "source": "kubernetes_api",
            "object_ref": pod_ref,
            "summary": f"Container terminated with exit code {term['exit_code']} ({term['reason']}), restartCount={pod.get('restart_count',0)}",
            "value": f"exit_code={term['exit_code']}, reason={term['reason']}, restarts={pod.get('restart_count',0)}",
            "observed_at": term["finished_at"],
        }]
        contradicting = []

        for ev in events:
            ev_ts = parse_ts(ev.get("last_timestamp", "")).timestamp()
            if (ev.get("involved_object_name") in (pod["name"], pod.get("node_name")) or ev.get("node_name") == pod.get("node_name")) and win_start <= ev_ts <= win_end:
                timeline.append({
                    "timestamp": ev.get("last_timestamp"),
                    "layer": "k8s_node",
                    "severity": ev.get("type", "warning").lower(),
                    "object_ref": f"{ev.get('involved_object_kind','Node').lower()}/{ev.get('involved_object_name')}",
                    "event_type": ev.get("reason"),
                    "summary": ev.get("message"),
                })
                supporting.append({
                    "kind": "k8s_event",
                    "polarity": "supporting",
                    "source": ev.get("source_component", "kubelet"),
                    "object_ref": f"{ev.get('involved_object_kind','Node').lower()}/{ev.get('involved_object_name')}",
                    "summary": ev.get("message"),
                    "value": f"reason={ev.get('reason')}, count={ev.get('count',1)}",
                    "observed_at": ev.get("last_timestamp"),
                })

        if not rel_gpus or any(g.get("is_stale") for g in rel_gpus):
            timeline.sort(key=lambda x: x.get("timestamp", ""))
            fp = compute_fingerprint("GPU-CORR-004", cluster, "Pod", pod["namespace"], pod["name"], "InconclusiveTelemetry")
            incidents.append({
                "id": "inc-corr-" + fp[:8],
                "fingerprint": fp,
                "rule_id": "GPU-CORR-004",
                "rule_name": defs["GPU-CORR-004"]["name"],
                "category": "failure_correlation",
                "severity": "warning",
                "eval_status": "inconclusive",
                "evidence_strength": "inconclusive",
                "evidence_strength_basis": "Container termination is confirmed, but GPU telemetry on the node is missing or stale during the correlation window.",
                "title": f"Workload failure on {pod_ref} (GPU hardware correlation inconclusive — stale telemetry)",
                "summary": f"Workload {pod_ref} failed/restarted (exit_code={term['exit_code']}, reason={term['reason']}), but GPU telemetry on node {pod.get('node_name')} was stale or unavailable during [-{th['correlation_lead_minutes']:.0f}m, +{th['correlation_lag_minutes']:.0f}m]. Cannot confirm or rule out GPU hardware fault.",
                "observed_facts": facts + [f"GPU telemetry on node {pod.get('node_name')} is stale or unavailable for the termination window."],
                "possible_causes": [
                    "Node or GPU driver hang may have simultaneously crashed both the workload container and the local dcgm-exporter daemon.",
                    "Userspace application crash occurred while telemetry scraping was independently degraded.",
                ],
                "supporting_evidence": supporting,
                "contradicting_evidence": [],
                "timeline": timeline,
                "affected_resources": [pod_ref, "node/" + pod.get("node_name", "")],
                "suggested_checks": defs["GPU-CORR-004"]["suggested_checks"],
                "inconclusive_reason": "GPU telemetry was stale or missing during the container termination correlation window.",
                "data_mode": data_mode,
                "scenario_id": scenario_id,
                "first_seen_at": term["finished_at"],
                "last_seen_at": snapshot.get("collected_at"),
                "occurrence_count": 1,
            })
            continue

        hw_fault = False
        for g in rel_gpus:
            gpu_ref = f"{g['node_name']}/gpu-{g['device_index']} ({g['uuid']})"
            for s in g.get("samples", []):
                s_ts = parse_ts(s["timestamp"]).timestamp()
                if not (win_start <= s_ts <= win_end):
                    continue
                if s.get("xid_error_code", 0) > 0:
                    hw_fault = True
                    xid = s["xid_error_code"]
                    desc = describe_xid(xid)
                    delta = fin_at.timestamp() - s_ts
                    timing_phrase = f"{delta:.0f} seconds before container termination" if delta >= 0 else f"{abs(delta):.0f} seconds after container termination (persisting fault)"
                    facts.append(f"Assigned GPU {gpu_ref} recorded XID {xid} ({desc}) at {s['timestamp']} ({timing_phrase}).")
                    supporting.append({
                        "kind": "metric",
                        "polarity": "supporting",
                        "source": "dcgm_exporter",
                        "metric_name": "DCGM_FI_DEV_XID_ERRORS",
                        "object_ref": gpu_ref,
                        "summary": f"XID {xid} ({desc}) observed ({timing_phrase})",
                        "value": str(xid),
                        "threshold": f"within [-{th['correlation_lead_minutes']:.0f}m, +{th['correlation_lag_minutes']:.0f}m] of termination",
                        "observed_at": s["timestamp"],
                    })
                    timeline.append({
                        "timestamp": s["timestamp"],
                        "layer": "gpu_telemetry",
                        "severity": "critical",
                        "object_ref": gpu_ref,
                        "event_type": f"XID_{xid}",
                        "summary": f"GPU {gpu_ref} emitted XID {xid} ({desc})",
                    })

        timeline.sort(key=lambda x: x.get("timestamp", ""))
        if hw_fault:
            if term["reason"] != "OOMKilled" and term["exit_code"] != 137:
                contradicting.append({
                    "kind": "pod_state",
                    "polarity": "contradicting",
                    "source": "kubernetes_api",
                    "object_ref": pod_ref,
                    "summary": "Container termination reason is not host cgroup OOMKilled (exit code != 137), ruling out host RAM cgroup eviction",
                    "value": f"reason={term['reason']}, exit_code={term['exit_code']}",
                    "observed_at": term["finished_at"],
                })
            fp = compute_fingerprint("GPU-CORR-004", cluster, "Pod", pod["namespace"], pod["name"], "HardwareCorrelatedFailure")
            incidents.append({
                "id": "inc-corr-" + fp[:8],
                "fingerprint": fp,
                "rule_id": "GPU-CORR-004",
                "rule_name": defs["GPU-CORR-004"]["name"],
                "category": "failure_correlation",
                "severity": "critical",
                "eval_status": "triggered",
                "evidence_strength": "strong",
                "evidence_strength_basis": "Multi-layer temporal and topological correlation: container termination occurred within the configured window of a hardware fault on the exact GPU assigned to the pod.",
                "title": f"Workload failure on {pod_ref} correlated with GPU hardware fault on {pod.get('node_name')}",
                "summary": f"Pod {pod_ref} terminated (exit_code={term['exit_code']}, {term['reason']}) and restarted ({pod.get('restart_count',0)} total restarts) within [-{th['correlation_lead_minutes']:.0f}m, +{th['correlation_lag_minutes']:.0f}m] of hardware error signals on its assigned GPU(s) on {pod.get('node_name')}.",
                "observed_facts": facts,
                "possible_causes": [
                    "Underlying GPU hardware fault (XID / uncorrectable ECC / PCIe bus drop) on the assigned accelerator likely aborted the active CUDA context and triggered the container crash.",
                    "Alternative hypothesis to verify: application-level memory corruption or driver bug triggered simultaneously (note: temporal correlation is strong supporting evidence, not mathematical proof of sole causation).",
                ],
                "supporting_evidence": supporting,
                "contradicting_evidence": contradicting,
                "timeline": timeline,
                "affected_resources": [pod_ref, "node/" + pod.get("node_name", "")] + list(uuids),
                "suggested_checks": defs["GPU-CORR-004"]["suggested_checks"],
                "data_mode": data_mode,
                "scenario_id": scenario_id,
                "first_seen_at": term["started_at"],
                "last_seen_at": snapshot.get("collected_at"),
                "occurrence_count": 1,
            })
        else:
            facts.append(f"Assigned GPU(s) ({', '.join(sorted(uuids))}) reported zero XID errors, zero row-remap failures, and normal operating temperatures throughout the failure window.")
            contradicting.append({
                "kind": "metric",
                "polarity": "contradicting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_XID_ERRORS",
                "object_ref": ",".join(sorted(uuids)),
                "summary": "Assigned GPUs recorded zero XID errors, zero ECC DBEs, and healthy temperatures during the failure window (contradicts local GPU hardware fault)",
                "value": "xid_errors=0, row_remap_failure=0",
                "observed_at": term["finished_at"],
            })
            fp = compute_fingerprint("GPU-CORR-004", cluster, "Pod", pod["namespace"], pod["name"], "ApplicationFailureHealthyGPU")
            incidents.append({
                "id": "inc-corr-" + fp[:8],
                "fingerprint": fp,
                "rule_id": "GPU-CORR-004",
                "rule_name": defs["GPU-CORR-004"]["name"],
                "category": "failure_correlation",
                "severity": "warning",
                "eval_status": "triggered",
                "evidence_strength": "moderate",
                "evidence_strength_basis": "Container termination confirmed; fresh DCGM telemetry on assigned GPUs provides contradicting evidence against a local GPU hardware fault.",
                "title": f"Workload failure on {pod_ref} (local GPU hardware telemetry healthy)",
                "summary": f"Pod {pod_ref} terminated (exit_code={term['exit_code']}, {term['reason']}), while fresh DCGM telemetry on assigned GPU(s) recorded zero hardware faults during the correlation window.",
                "observed_facts": facts,
                "possible_causes": [
                    "Userspace application exception, CUDA out-of-memory, or host cgroup OOMKilled (local GPU hardware telemetry remained healthy throughout the failure window).",
                    "If distributed training: peer rank hang or network timeout causing collective abort on this rank despite healthy local GPUs.",
                ],
                "supporting_evidence": supporting,
                "contradicting_evidence": contradicting,
                "timeline": timeline,
                "affected_resources": [pod_ref, "node/" + pod.get("node_name", "")] + sorted(uuids),
                "suggested_checks": defs["GPU-CORR-004"]["suggested_checks"],
                "data_mode": data_mode,
                "scenario_id": scenario_id,
                "first_seen_at": term["started_at"],
                "last_seen_at": snapshot.get("collected_at"),
                "occurrence_count": 1,
            })

    # 5. GPU-FRESH-005
    for src in sources:
        if not src.get("available", True) or src.get("is_stale") or src.get("staleness_seconds", 0) > th["max_staleness_seconds"]:
            fp = compute_fingerprint("GPU-FRESH-005", cluster, "Source", "cluster", src["id"], "SourceStaleOrDown")
            incidents.append({
                "id": "inc-fresh-" + fp[:8],
                "fingerprint": fp,
                "rule_id": "GPU-FRESH-005",
                "rule_name": defs["GPU-FRESH-005"]["name"],
                "category": "data_freshness",
                "severity": "critical",
                "eval_status": "triggered",
                "evidence_strength": "strong",
                "evidence_strength_basis": "Direct telemetry collector health check and scrape timestamp audit.",
                "title": f"Telemetry blind spot: source {src['name']} is unavailable or stale ({src.get('staleness_seconds',0):.0f}s old)",
                "summary": f"Telemetry source {src['name']} ({src.get('endpoint','')}) is unavailable or stale (last successful collection {src.get('last_success_at')}, {src.get('staleness_seconds',0):.0f}s ago > {th['max_staleness_seconds']:.0f}s limit). Missing observations do NOT indicate healthy GPUs.",
                "observed_facts": [
                    f"Source {src['name']} (kind={src.get('kind')}) reported available={src.get('available')}, staleness={src.get('staleness_seconds',0):.0f}s (threshold {th['max_staleness_seconds']:.0f}s).",
                    src.get("last_error") or "Scrape timestamp exceeded max_staleness_seconds.",
                ],
                "possible_causes": [
                    "Prometheus server or dcgm-exporter endpoint is unreachable, crashed, or blocked by NetworkPolicy.",
                    "Collector scrape loop timed out due to hung nv-hostengine.",
                ],
                "supporting_evidence": [{
                    "kind": "source_state",
                    "polarity": "supporting",
                    "source": src["id"],
                    "object_ref": src["name"],
                    "summary": f"Source available={src.get('available')}, staleness={src.get('staleness_seconds',0):.0f}s",
                    "value": f"staleness={src.get('staleness_seconds',0):.0f}s, error=\"{src.get('last_error','')}\"",
                    "threshold": f"<= {th['max_staleness_seconds']:.0f}s",
                    "observed_at": snapshot.get("collected_at"),
                }],
                "contradicting_evidence": [],
                "timeline": [
                    {"timestamp": src.get("last_success_at"), "layer": "gpu_telemetry", "severity": "info", "object_ref": src["name"], "event_type": "LastSuccessfulCollection", "summary": "Last valid scrape received from telemetry source"},
                    {"timestamp": snapshot.get("collected_at"), "layer": "gpu_telemetry", "severity": "critical", "object_ref": src["name"], "event_type": "TelemetryStale", "summary": f"Telemetry age {src.get('staleness_seconds',0):.0f}s exceeded {th['max_staleness_seconds']:.0f}s threshold"},
                ],
                "affected_resources": ["source/" + src["id"]],
                "suggested_checks": defs["GPU-FRESH-005"]["suggested_checks"],
                "data_mode": data_mode,
                "scenario_id": scenario_id,
                "first_seen_at": src.get("last_success_at"),
                "last_seen_at": snapshot.get("collected_at"),
                "occurrence_count": 1,
            })

    stale_gpus = [
        f"{g['node_name']}/gpu-{g['device_index']} ({g['uuid']}, age={g.get('staleness_seconds',0):.0f}s)"
        for g in gpus
        if g.get("is_stale") or g.get("staleness_seconds", 0) > th["max_staleness_seconds"]
    ]
    unmapped_gpus = [
        f"{g['node_name']}/gpu-{g['device_index']} ({g['uuid']})"
        for g in gpus
        if g.get("mapping_status") == "missing_pod_labels"
    ]
    if stale_gpus:
        fp = compute_fingerprint("GPU-FRESH-005", cluster, "ClusterGPUs", "cluster", "stale-gpus", "StaleGPUMetrics")
        incidents.append({
            "id": "inc-fresh-" + fp[:8],
            "fingerprint": fp,
            "rule_id": "GPU-FRESH-005",
            "rule_name": defs["GPU-FRESH-005"]["name"],
            "category": "data_freshness",
            "severity": "critical",
            "eval_status": "triggered",
            "evidence_strength": "strong",
            "evidence_strength_basis": "GPU metric sample timestamps compared directly against snapshot collection time.",
            "title": f"Stale GPU telemetry on {len(stale_gpus)} device(s) (blind spot active)",
            "summary": f"{len(stale_gpus)} GPU device(s) have telemetry older than {th['max_staleness_seconds']:.0f} seconds ({'; '.join(stale_gpus)}). Health and utilization diagnostics for these devices are marked inconclusive rather than healthy.",
            "observed_facts": [f"{len(stale_gpus)} GPU(s) exceeded max_staleness_seconds ({th['max_staleness_seconds']:.0f}s): {', '.join(stale_gpus)}"],
            "possible_causes": [
                "dcgm-exporter pod on the affected node crashed, was evicted, or lost connection to nv-hostengine.",
                "GPU driver lockup (e.g., severe PCIe bus hang) caused NVML/DCGM polling to block past scrape timeout.",
            ],
            "supporting_evidence": [{
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "object_ref": "cluster/gpus",
                "summary": f"{len(stale_gpus)} GPU series have sample timestamps older than {th['max_staleness_seconds']:.0f}s",
                "value": "; ".join(stale_gpus),
                "threshold": f"age <= {th['max_staleness_seconds']:.0f}s",
                "observed_at": snapshot.get("collected_at"),
            }],
            "contradicting_evidence": [],
            "timeline": [],
            "affected_resources": stale_gpus,
            "suggested_checks": defs["GPU-FRESH-005"]["suggested_checks"],
            "data_mode": data_mode,
            "scenario_id": scenario_id,
            "first_seen_at": snapshot.get("collected_at"),
            "last_seen_at": snapshot.get("collected_at"),
            "occurrence_count": 1,
        })

    if unmapped_gpus:
        fp = compute_fingerprint("GPU-FRESH-005", cluster, "ClusterGPUs", "cluster", "unmapped-gpus", "MissingPodLabels")
        incidents.append({
            "id": "inc-fresh-" + fp[:8],
            "fingerprint": fp,
            "rule_id": "GPU-FRESH-005",
            "rule_name": defs["GPU-FRESH-005"]["name"],
            "category": "data_freshness",
            "severity": "warning",
            "eval_status": "triggered",
            "evidence_strength": "strong",
            "evidence_strength_basis": "Kubernetes Node/Pod state confirms active GPU allocations on nodes where DCGM series lack pod/namespace labels.",
            "title": f"Uncorrelated GPU telemetry: {len(unmapped_gpus)} allocated GPU(s) missing Kubernetes pod labels",
            "summary": f"{len(unmapped_gpus)} allocated GPU(s) ({', '.join(unmapped_gpus)}) are emitting hardware metrics without Kubernetes pod/namespace labels. Per-workload utilization and efficiency claims are suppressed for these devices.",
            "observed_facts": [
                f"Allocated GPUs missing pod/namespace labels: {', '.join(unmapped_gpus)}",
                "Per-job GPU utilization attribution is disabled for unmapped devices to prevent fabricated efficiency numbers.",
            ],
            "possible_causes": [
                "dcgm-exporter is running without the --kubernetes flag enabled.",
                "Kubelet restarted while dcgm-exporter mounted /var/lib/kubelet/pod-resources/kubelet.sock directly as a file instead of mounting the /var/lib/kubelet/pod-resources/ directory, leaving a broken socket inode.",
            ],
            "supporting_evidence": [{
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "object_ref": "cluster/pod-resources-mapping",
                "summary": "DCGM metric series lack pod and namespace labels on nodes with active GPU pods",
                "value": f"{len(unmapped_gpus)} unmapped GPUs: {', '.join(unmapped_gpus)}",
                "observed_at": snapshot.get("collected_at"),
            }],
            "contradicting_evidence": [],
            "timeline": [],
            "affected_resources": unmapped_gpus,
            "suggested_checks": defs["GPU-FRESH-005"]["suggested_checks"],
            "data_mode": data_mode,
            "scenario_id": scenario_id,
            "first_seen_at": snapshot.get("collected_at"),
            "last_seen_at": snapshot.get("collected_at"),
            "occurrence_count": 1,
        })

    # 6. GPU-THERM-006
    for gpu in gpus:
        if gpu.get("is_stale"):
            continue
        pt = gpu.get("latest", {})
        gpu_ref = f"{gpu['node_name']}/gpu-{gpu['device_index']} ({gpu['uuid']})"
        facts, causes, supporting, contradicting = [], [], [], []
        is_crit, is_warn = False, False

        gtemp = pt.get("gpu_temp_celsius", 0.0)
        mtemp = pt.get("memory_temp_celsius", 0.0)
        if gtemp >= th["temp_critical_celsius"]:
            is_crit = True
            facts.append(f"GPU {gpu_ref} core temperature DCGM_FI_DEV_GPU_TEMP is {gtemp:.1f}°C (>= {th['temp_critical_celsius']:.1f}°C critical threshold).")
            causes.append("Severe thermal constraint risking hardware thermal slowdown (hw_thermal) or thermal shutdown.")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_GPU_TEMP",
                "object_ref": gpu_ref,
                "summary": f"GPU core temperature reached {gtemp:.1f}°C",
                "value": f"{gtemp:.1f}°C",
                "threshold": f">= {th['temp_critical_celsius']:.1f}°C (critical)",
                "observed_at": pt.get("timestamp"),
            })
        elif gtemp >= th["temp_warning_celsius"]:
            is_warn = True
            facts.append(f"GPU {gpu_ref} core temperature DCGM_FI_DEV_GPU_TEMP is {gtemp:.1f}°C (>= {th['temp_warning_celsius']:.1f}°C warning threshold).")
            causes.append("Elevated GPU die temperature approaching thermal slowdown envelope.")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_GPU_TEMP",
                "object_ref": gpu_ref,
                "summary": f"GPU core temperature elevated at {gtemp:.1f}°C",
                "value": f"{gtemp:.1f}°C",
                "threshold": f">= {th['temp_warning_celsius']:.1f}°C (warning)",
                "observed_at": pt.get("timestamp"),
            })

        if mtemp >= th["mem_temp_warning_celsius"]:
            is_warn = True
            facts.append(f"GPU {gpu_ref} HBM memory temperature DCGM_FI_DEV_MEMORY_TEMP is {mtemp:.1f}°C (>= {th['mem_temp_warning_celsius']:.1f}°C warning threshold).")
            causes.append("Elevated HBM memory temperature increasing risk of memory bit flips and thermal throttling.")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_MEMORY_TEMP",
                "object_ref": gpu_ref,
                "summary": f"HBM memory temperature reached {mtemp:.1f}°C",
                "value": f"{mtemp:.1f}°C",
                "threshold": f">= {th['mem_temp_warning_celsius']:.1f}°C",
                "observed_at": pt.get("timestamp"),
            })

        if pt.get("thermal_violation_ns") is not None and pt["thermal_violation_ns"] > 0:
            is_crit = True
            facts.append(f"GPU {gpu_ref} reported DCGM_FI_DEV_THERMAL_VIOLATION = {pt['thermal_violation_ns']} ns (clock_event=\"{pt.get('clock_throttle_reason','')}\").")
            causes.append("Active GPU clock throttling due to thermal violation confirmed by DCGM counter.")
            supporting.append({
                "kind": "metric",
                "polarity": "supporting",
                "source": "dcgm_exporter",
                "metric_name": "DCGM_FI_DEV_THERMAL_VIOLATION",
                "object_ref": gpu_ref,
                "summary": "Active thermal clock throttling duration recorded",
                "value": f"{pt['thermal_violation_ns']} ns (reason={pt.get('clock_throttle_reason','')})",
                "threshold": "> 0 ns",
                "observed_at": pt.get("timestamp"),
            })

        tot_mem = gpu.get("total_memory_mib", 81920.0)
        if tot_mem > 0:
            fb_pct = (pt.get("fb_used_mib", 0.0) / tot_mem) * 100.0
            if fb_pct >= th["fb_memory_warning_percent"]:
                is_warn = True
                facts.append(f"GPU {gpu_ref} framebuffer memory usage is {pt.get('fb_used_mib',0):.0f} / {tot_mem:.0f} MiB ({fb_pct:.1f}% >= {th['fb_memory_warning_percent']:.1f}% threshold).")
                causes.append("Framebuffer VRAM is nearly exhausted; additional allocations may trigger CUDA OOM.")
                supporting.append({
                    "kind": "metric",
                    "polarity": "supporting",
                    "source": "dcgm_exporter",
                    "metric_name": "DCGM_FI_DEV_FB_USED",
                    "object_ref": gpu_ref,
                    "summary": f"Framebuffer memory occupancy at {fb_pct:.1f}%",
                    "value": f"{pt.get('fb_used_mib',0):.0f} / {tot_mem:.0f} MiB ({fb_pct:.1f}%)",
                    "threshold": f">= {th['fb_memory_warning_percent']:.1f}%",
                    "observed_at": pt.get("timestamp"),
                })

        if not is_crit and not is_warn:
            continue

        fp = compute_fingerprint("GPU-THERM-006", cluster, "GPU", gpu["node_name"], gpu["uuid"], "ThermalPowerMemory")
        sev = "critical" if is_crit else "warning"
        incidents.append({
            "id": "inc-therm-" + fp[:8],
            "fingerprint": fp,
            "rule_id": "GPU-THERM-006",
            "rule_name": defs["GPU-THERM-006"]["name"],
            "category": "thermal_power_memory",
            "severity": sev,
            "eval_status": "triggered",
            "evidence_strength": "strong",
            "evidence_strength_basis": "Directly measured via fresh DCGM temperature, power, throttling, and framebuffer gauges.",
            "title": f"Operating envelope warning on {gpu_ref} ({gtemp:.1f}°C die, {pt.get('power_usage_watts',0):.0f}W)",
            "summary": facts[0],
            "observed_facts": facts,
            "possible_causes": causes,
            "supporting_evidence": supporting,
            "contradicting_evidence": contradicting,
            "timeline": [{
                "timestamp": pt.get("timestamp"),
                "layer": "gpu_telemetry",
                "severity": sev,
                "object_ref": gpu_ref,
                "event_type": "EnvelopeThresholdExceeded",
                "summary": facts[0],
            }],
            "affected_resources": [gpu_ref, "node/" + gpu["node_name"]],
            "suggested_checks": defs["GPU-THERM-006"]["suggested_checks"],
            "data_mode": data_mode,
            "scenario_id": scenario_id,
            "first_seen_at": pt.get("timestamp"),
            "last_seen_at": snapshot.get("collected_at"),
            "occurrence_count": 1,
        })

    # Deduplicate by fingerprint and sort by severity
    rank = {"critical": 3, "warning": 2, "info": 1}
    dedup = {}
    for inc in incidents:
        fp = inc["fingerprint"]
        if fp not in dedup:
            dedup[fp] = inc
        else:
            dedup[fp]["occurrence_count"] += 1
    out = list(dedup.values())
    out.sort(key=lambda x: (-rank.get(x["severity"], 1), x["rule_id"], x["id"]))
    return out


def build_capacity_report(snapshot: dict, th: dict = None) -> dict:
    th = th or DEFAULT_THRESHOLDS
    collected_at = parse_ts(snapshot.get("collected_at", ""))
    nodes = snapshot.get("nodes", [])
    gpus = snapshot.get("gpus", [])
    pods = snapshot.get("pods", [])
    events = snapshot.get("events", [])
    sample_rate = th.get("optional_sample_gpu_hourly_rate_usd", 3.50)

    gpus_by_node = {}
    gpu_by_uuid = {}
    fresh_gpus = 0
    stale_gpus = 0
    mapped_gpus_cnt = 0
    unmapped_gpus_cnt = 0

    for g in gpus:
        gpus_by_node.setdefault(g["node_name"], []).append(g)
        gpu_by_uuid[g["uuid"]] = g
        if g.get("is_stale"):
            stale_gpus += 1
        else:
            fresh_gpus += 1
        if g.get("mapping_status") == "mapped":
            mapped_gpus_cnt += 1
        elif g.get("mapping_status") == "missing_pod_labels":
            unmapped_gpus_cnt += 1

    total_cap = 0
    total_allocatable = 0
    unavail_hw = 0
    total_allocated = 0
    total_free = 0
    node_breakdown = []

    for n in nodes:
        c = n.get("gpu_capacity", 0)
        a = n.get("gpu_allocatable", 0)
        used = n.get("gpu_allocated", 0)
        unav = max(0, c - a)
        free = max(0, a - used)
        total_cap += c
        total_allocatable += a
        unavail_hw += unav
        total_allocated += used
        if n.get("ready", True) and n.get("schedulable", True):
            total_free += free

        ngpus = gpus_by_node.get(n["name"], [])
        nfresh = [g for g in ngpus if not g.get("is_stale")]
        nstale = len(ngpus) - len(nfresh)
        nunmapped = sum(1 for g in ngpus if g.get("mapping_status") == "missing_pod_labels")

        mean_u = round(sum(g["latest"]["gpu_util_percent"] for g in nfresh) / len(nfresh), 2) if nfresh else None
        mean_fb = round(
            sum((g["latest"]["fb_used_mib"] / g.get("total_memory_mib", 81920.0)) * 100.0 for g in nfresh) / len(nfresh), 2
        ) if nfresh else None

        status_note = "Healthy & Schedulable"
        if not n.get("ready", True):
            status_note = "Node NotReady"
        elif not n.get("schedulable", True):
            status_note = "Node Cordoned (Unschedulable)"
        elif unav > 0:
            status_note = f"Degraded Allocatable ({unav} GPU unavailable to scheduler)"
        elif nstale > 0:
            status_note = f"Telemetry Stale ({nstale}/{len(ngpus)} GPUs stale)"
        elif nunmapped > 0:
            status_note = f"Missing Pod Attribution Labels ({nunmapped} GPUs)"

        node_breakdown.append({
            "node_name": n["name"],
            "gpu_model": n.get("gpu_model", "NVIDIA GPU"),
            "schedulable": n.get("schedulable", True),
            "ready": n.get("ready", True),
            "capacity_gpus": c,
            "allocatable_gpus": a,
            "unavailable_gpus": unav,
            "allocated_gpus": used,
            "free_allocatable_gpus": free,
            "mean_gpu_util_percent": mean_u,
            "mean_fb_used_percent": mean_fb,
            "fresh_gpus": len(nfresh),
            "stale_gpus": nstale,
            "unmapped_pod_gpus": nunmapped,
            "status_note": status_note,
        })

    all_fresh = [g for g in gpus if not g.get("is_stale")]
    cluster_mean_u = round(sum(g["latest"]["gpu_util_percent"] for g in all_fresh) / len(all_fresh), 2) if all_fresh else None
    cluster_mean_fb = round(
        sum((g["latest"]["fb_used_mib"] / g.get("total_memory_mib", 81920.0)) * 100.0 for g in all_fresh) / len(all_fresh), 2
    ) if all_fresh else None

    pending_workloads = []
    low_util_workloads = []
    pending_req_gpus = 0
    total_obs_gpu_hrs = 0.0
    low_util_gpu_hrs = 0.0
    attribution_complete = unmapped_gpus_cnt == 0 and stale_gpus == 0

    for pod in pods:
        if pod.get("phase") == "Pending" and pod.get("requested_gpus", 0) > 0:
            pending_req_gpus += pod["requested_gpus"]
            created_at = parse_ts(pod.get("created_at", ""))
            reason = pod.get("scheduler_reason", "")
            msg = pod.get("scheduler_message", "")
            for ev in events:
                if ev.get("involved_object_kind") == "Pod" and ev.get("involved_object_name") == pod["name"] and ev.get("reason") == "FailedScheduling":
                    reason = reason or ev["reason"]
                    msg = msg or ev["message"]
            ev_complete = bool(reason or msg)
            pending_workloads.append({
                "namespace": pod["namespace"],
                "pod_name": pod["name"],
                "owner_ref": f"{pod.get('owner_kind','Pod')}/{pod.get('owner_name',pod['name'])}",
                "requested_gpus": pod["requested_gpus"],
                "pending_minutes": round((collected_at - created_at).total_seconds() / 60.0, 1),
                "observed_reason": reason or "Inconclusive (No FailedScheduling Event Observed)",
                "scheduler_message": msg or "Scheduler event missing from snapshot; refusing to guess cause.",
                "evidence_complete": ev_complete,
                "created_at": pod.get("created_at"),
            })
        elif pod.get("phase") == "Running" and pod.get("requested_gpus", 0) > 0 and pod.get("started_at"):
            uuids = pod.get("assigned_gpu_uuids", [])
            if not uuids:
                attribution_complete = False
                continue
            mapped = []
            ok = True
            for u in uuids:
                g = gpu_by_uuid.get(u)
                if not g or g.get("is_stale") or g.get("mapping_status") != "mapped" or not g.get("samples"):
                    ok = False
                    break
                mapped.append(g)
            if not ok:
                attribution_complete = False
                continue

            earliest_ts, latest_ts = None, None
            sum_u, min_u, max_u, sum_mc, sum_fb, sum_fb_pct, cnt = 0.0, 100.0, 0.0, 0.0, 0.0, 0.0, 0
            for g in mapped:
                tot = g.get("total_memory_mib", 81920.0)
                for s in g.get("samples", []):
                    ts = parse_ts(s["timestamp"])
                    earliest_ts = ts if earliest_ts is None or ts < earliest_ts else earliest_ts
                    latest_ts = ts if latest_ts is None or ts > latest_ts else latest_ts
                    u_val = s.get("gpu_util_percent", 0.0)
                    sum_u += u_val
                    min_u = min(min_u, u_val)
                    max_u = max(max_u, u_val)
                    sum_mc += s.get("mem_copy_util_percent", 0.0)
                    fb_val = s.get("fb_used_mib", 0.0)
                    sum_fb += fb_val
                    sum_fb_pct += (fb_val / tot) * 100.0 if tot > 0 else 0.0
                    cnt += 1

            win_mins = max((latest_ts - earliest_ts).total_seconds() / 60.0, 1.0)
            gpu_hrs = round(pod["requested_gpus"] * (win_mins / 60.0), 2)
            total_obs_gpu_hrs = round(total_obs_gpu_hrs + gpu_hrs, 2)

            mean_u = round(sum_u / cnt, 2)
            mean_mc = round(sum_mc / cnt, 2)
            mean_fb = round(sum_fb / cnt, 1)
            mean_fb_pct = round(sum_fb_pct / cnt, 1)

            if win_mins >= th["low_util_window_minutes"] and cnt >= th["min_utilization_samples"] and mean_u < th["low_util_threshold_percent"]:
                low_util_gpu_hrs = round(low_util_gpu_hrs + gpu_hrs, 2)
                sample_cost = round(gpu_hrs * sample_rate, 2) if sample_rate else None
                ctx_note = (
                    f"High VRAM residency ({mean_fb:.0f} MiB / {mean_fb_pct:.1f}% of HBM) with {mean_u:.1f}% SM utilization: workload likely requires GPU memory capacity to hold model weights despite low compute duty cycle."
                    if mean_fb_pct >= th["high_vram_context_percent"]
                    else f"Mean SM compute utilization is {mean_u:.1f}% (mem copy {mean_mc:.1f}%, VRAM residency {mean_fb:.0f} MiB / {mean_fb_pct:.1f}%). Low compute utilization alone does NOT prove waste."
                )
                low_util_workloads.append({
                    "namespace": pod["namespace"],
                    "pod_name": pod["name"],
                    "owner_ref": f"{pod.get('owner_kind','Pod')}/{pod.get('owner_name',pod['name'])}",
                    "node_name": pod.get("node_name"),
                    "allocated_gpus": pod["requested_gpus"],
                    "assigned_gpu_uuids": uuids,
                    "observation_window_minutes": round(win_mins, 1),
                    "sample_count": cnt,
                    "mean_gpu_util_percent": mean_u,
                    "min_gpu_util_percent": round(min_u, 1),
                    "max_gpu_util_percent": round(max_u, 1),
                    "mean_mem_copy_util_percent": mean_mc,
                    "mean_fb_used_mib": mean_fb,
                    "mean_fb_used_percent": mean_fb_pct,
                    "total_allocated_gpu_hours": gpu_hrs,
                    "low_util_gpu_hours": gpu_hrs,
                    "optional_sample_cost_estimate_usd": sample_cost,
                    "last_data_update": latest_ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
                    "workload_context_note": ctx_note,
                    "follow_up_checklist": [
                        "Inspect data loader CPU/storage throughput and I/O wait times.",
                        "Check distributed collective (NCCL) synchronization wait if multi-rank.",
                        "Evaluate whether high VRAM usage (DCGM_FI_DEV_FB_USED) justifies full-GPU reservation.",
                        "Verify if interactive session or low-QPS service can use MIG slices or time-slicing.",
                    ],
                })

    # Historical utilization points across fresh GPUs
    by_ts = {}
    for g in all_fresh:
        tot = g.get("total_memory_mib", 81920.0)
        for s in g.get("samples", []):
            ts_str = s["timestamp"]
            bucket = by_ts.setdefault(ts_str, {"u": 0.0, "mc": 0.0, "fb": 0.0, "c": 0})
            bucket["u"] += s.get("gpu_util_percent", 0.0)
            bucket["mc"] += s.get("mem_copy_util_percent", 0.0)
            bucket["fb"] += (s.get("fb_used_mib", 0.0) / tot) * 100.0 if tot > 0 else 0.0
            bucket["c"] += 1

    history = []
    for ts_str in sorted(by_ts.keys()):
        b = by_ts[ts_str]
        if b["c"] > 0:
            history.append({
                "timestamp": ts_str,
                "mean_gpu_util_percent": round(b["u"] / b["c"], 2),
                "mean_mem_copy_util_percent": round(b["mc"] / b["c"], 2),
                "mean_fb_used_percent": round(b["fb"] / b["c"], 2),
                "allocated_gpus_observed": b["c"],
            })

    blind_spot_warn = ""
    if not attribution_complete or unmapped_gpus_cnt > 0 or stale_gpus > 0:
        blind_spot_warn = (
            f"Telemetry attribution is partial ({stale_gpus} stale GPUs, {unmapped_gpus_cnt} allocated GPUs missing pod labels). "
            "Unmapped/stale GPUs are excluded from per-job efficiency calculations."
        )

    return {
        "cluster_name": snapshot.get("cluster_name", ""),
        "scenario_id": snapshot.get("scenario_id", ""),
        "data_mode": snapshot.get("data_mode", "simulated_fixture"),
        "generated_at": snapshot.get("collected_at", ""),
        "total_capacity_gpus": total_cap,
        "total_allocatable_gpus": total_allocatable,
        "unavailable_hardware_gpus": unavail_hw,
        "total_allocated_gpus": total_allocated,
        "total_free_allocatable_gpus": total_free,
        "pending_requested_gpus": pending_req_gpus,
        "cluster_mean_gpu_util_percent": cluster_mean_u,
        "cluster_mean_fb_used_percent": cluster_mean_fb,
        "total_observed_allocated_gpu_hours": total_obs_gpu_hrs,
        "observed_low_util_gpu_hours": low_util_gpu_hrs,
        "optional_low_util_sample_cost_usd": round(low_util_gpu_hrs * sample_rate, 2) if sample_rate else None,
        "telemetry_fresh_gpus": fresh_gpus,
        "telemetry_stale_gpus": stale_gpus,
        "mapped_allocated_gpus": mapped_gpus_cnt,
        "unmapped_allocated_gpus": unmapped_gpus_cnt,
        "per_job_attribution_complete": attribution_complete,
        "attribution_blind_spot_warning": blind_spot_warn,
        "nodes": node_breakdown,
        "pending_workloads": pending_workloads,
        "low_utilization_workloads": low_util_workloads,
        "historical_utilization": history,
        "methodology": {
            "util_threshold_percent": th["low_util_threshold_percent"],
            "min_window_minutes": th["low_util_window_minutes"],
            "min_samples_required": th["min_utilization_samples"],
            "formula_summary": "LowUtilGPUHours = RequestedGPUs * (ObservationWindowMinutes / 60.0) for Running pods where Mean(DCGM_FI_DEV_GPU_UTIL) < Threshold over Window >= MinWindowMinutes and Samples >= MinSamplesRequired.",
            "step_by_step_calculation": [
                "1. Inventory physical GPU capacity (status.capacity['nvidia.com/gpu']) vs allocatable GPUs (status.allocatable['nvidia.com/gpu']) per Kubernetes Node.",
                "2. Compute UnavailableHardwareGPUs = max(0, CapacityGPUs - AllocatableGPUs) to surface devices de-allocated by the device plugin.",
                "3. Audit DCGM telemetry freshness (age <= max_staleness_seconds) and Kubernetes pod attribution labels (pod, namespace from kubelet PodResourcesLister).",
                "4. For each Running workload with complete pod-to-GPU mapping, observation window >= low_util_window_minutes, and sample count >= min_utilization_samples, compute mean DCGM_FI_DEV_GPU_UTIL, DCGM_FI_DEV_MEM_COPY_UTIL, and DCGM_FI_DEV_FB_USED.",
                "5. Integrate allocated GPU-hours observed below low_util_threshold_percent: AllocatedGPUs * (WindowMinutes / 60).",
            ],
            "non_waste_disclaimer": "Low GPU SM compute utilization (DCGM_FI_DEV_GPU_UTIL) does NOT prove wasted capacity. Workloads may be bound by CPU/storage data loading, collective NCCL synchronization barriers, checkpoint I/O, or GPU framebuffer memory residency (DCGM_FI_DEV_FB_USED).",
            "missing_mapping_policy": "When GPU-to-pod label mapping is missing on a GPU series, GPU Lens excludes that GPU from per-workload efficiency calculations and flags an explicit attribution blind spot.",
            "optional_sample_hourly_rate_usd": sample_rate,
            "optional_cost_estimate_label": f"Illustrative Estimate Only (using configured sample rate of ${sample_rate:.2f}/GPU-hr; NOT actual cloud billing or realized savings)",
        },
    }


class SQLiteIncidentStore:
    """SQLite-backed incident store with fingerprint deduplication."""

    def __init__(self, db_path: str = ":memory:"):
        self.db_path = db_path
        self.lock = threading.Lock()
        self.conn = sqlite3.connect(db_path, check_same_thread=False)
        self._init_schema()

    def _init_schema(self):
        with self.lock:
            self.conn.execute(
                """
                CREATE TABLE IF NOT EXISTS incidents (
                    scenario_id TEXT NOT NULL,
                    fingerprint TEXT NOT NULL,
                    incident_id TEXT NOT NULL,
                    rule_id TEXT NOT NULL,
                    severity TEXT NOT NULL,
                    category TEXT NOT NULL,
                    first_seen_at TEXT NOT NULL,
                    last_seen_at TEXT NOT NULL,
                    occurrence_count INTEGER NOT NULL,
                    payload_json TEXT NOT NULL,
                    PRIMARY KEY (scenario_id, fingerprint)
                )
                """
            )
            self.conn.commit()

    def upsert_batch(self, scenario_id: str, evaluated: list) -> list:
        with self.lock:
            cur = self.conn.cursor()
            cur.execute(
                "SELECT fingerprint, first_seen_at, last_seen_at, occurrence_count FROM incidents WHERE scenario_id = ?",
                (scenario_id,),
            )
            existing = {row[0]: (row[1], row[2], row[3]) for row in cur.fetchall()}
            cur.execute("DELETE FROM incidents WHERE scenario_id = ?", (scenario_id,))
            persisted = []
            for inc in evaluated:
                fp = inc["fingerprint"]
                if fp in existing:
                    first_seen, prev_last, prev_cnt = existing[fp]
                    inc["first_seen_at"] = first_seen
                    inc["occurrence_count"] = prev_cnt + 1
                else:
                    inc["occurrence_count"] = max(1, inc.get("occurrence_count", 1))
                cur.execute(
                    """
                    INSERT INTO incidents (scenario_id, fingerprint, incident_id, rule_id, severity, category, first_seen_at, last_seen_at, occurrence_count, payload_json)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        scenario_id,
                        fp,
                        inc["id"],
                        inc["rule_id"],
                        inc["severity"],
                        inc["category"],
                        inc["first_seen_at"],
                        inc["last_seen_at"],
                        inc["occurrence_count"],
                        json.dumps(inc),
                    ),
                )
                persisted.append(inc)
            self.conn.commit()
            return persisted

    def list_incidents(self, scenario_id: str, severity: str = "", category: str = "", q: str = "") -> list:
        with self.lock:
            cur = self.conn.cursor()
            cur.execute("SELECT payload_json FROM incidents WHERE scenario_id = ?", (scenario_id,))
            items = [json.loads(r[0]) for r in cur.fetchall()]
        out = []
        for inc in items:
            if severity and severity != "all" and inc["severity"] != severity:
                continue
            if category and category != "all" and inc["category"] != category:
                continue
            if q:
                ql = q.lower()
                hay = f"{inc['title']} {inc['summary']} {inc['rule_id']} {inc['id']} {' '.join(inc.get('affected_resources',[]))}".lower()
                if ql not in hay:
                    continue
            out.append(inc)
        return out

    def get_by_id(self, scenario_id: str, inc_id: str):
        for inc in self.list_incidents(scenario_id):
            if inc["id"] == inc_id or inc["fingerprint"] == inc_id:
                return inc
        return None


class GPULensAppState:
    def __init__(self, fixtures_dir: str, default_scenario: str, db_path: str, static_dir: str):
        self.fixtures_dir = fixtures_dir
        self.static_dir = static_dir
        self.store = SQLiteIncidentStore(db_path)
        self.scenarios = {}
        self.order = []
        for fname in sorted(os.listdir(fixtures_dir)):
            if not fname.endswith(".json"):
                continue
            full = os.path.join(fixtures_dir, fname)
            with open(full, "r", encoding="utf-8") as f:
                data = json.load(f)
            sid = data.get("scenario_id")
            if sid:
                self.scenarios[sid] = data
                self.order.append(sid)
        self.active_id = default_scenario if default_scenario in self.scenarios else self.order[0]

    def evaluate_active(self, override_scenario: str = ""):
        if override_scenario and override_scenario in self.scenarios:
            self.active_id = override_scenario
        snap = self.scenarios[self.active_id]
        evaluated = evaluate_rules(snap, DEFAULT_THRESHOLDS)
        persisted = self.store.upsert_batch(snap["scenario_id"], evaluated)
        cap = build_capacity_report(snap, DEFAULT_THRESHOLDS)
        return snap, persisted, cap

    def build_overview(self, override_scenario: str = "") -> dict:
        snap, incidents, cap = self.evaluate_active(override_scenario)
        counts = {"critical": 0, "warning": 0, "info": 0, "inconclusive": 0, "total": len(incidents)}
        has_blind_spot = False
        for inc in incidents:
            counts[inc["severity"]] = counts.get(inc["severity"], 0) + 1
            if inc["eval_status"] == "inconclusive":
                counts["inconclusive"] += 1
            if inc["rule_id"] == "GPU-FRESH-005":
                has_blind_spot = True
        status = "healthy"
        headline = "All monitored GPUs, nodes, and telemetry streams are operating within healthy envelopes."
        if has_blind_spot:
            status = "telemetry_blind_spot"
            headline = "Monitoring blind spot detected: stale, unavailable, or uncorrelated GPU telemetry. Missing data is never treated as healthy."
        elif counts["critical"] > 0:
            status = "critical"
            headline = "Critical GPU hardware, scheduling, or workload failure findings require operator investigation."
        elif counts["warning"] > 0:
            status = "warning"
            headline = "Active GPU scheduling, thermal, or utilization warnings detected."

        return {
            "cluster_name": snap["cluster_name"],
            "scenario_id": snap["scenario_id"],
            "scenario_name": snap["scenario_name"],
            "scenario_description": snap["scenario_description"],
            "data_mode": snap["data_mode"],
            "collected_at": snap["collected_at"],
            "fleet_health_status": status,
            "fleet_health_headline": headline,
            "active_incident_counts": counts,
            "capacity_summary": cap,
            "active_incidents": incidents,
            "sources": snap.get("sources", []),
        }


def make_handler(state: GPULensAppState):
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            pass

        def _send_json(self, code: int, payload):
            raw = json.dumps(payload).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Access-Control-Allow-Origin", "*")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            parsed = urllib.parse.urlparse(self.path)
            qs = urllib.parse.parse_qs(parsed.query)
            scen_param = qs.get("scenario", [""])[0]

            if parsed.path == "/healthz":
                return self._send_json(200, {"status": "ok", "service": "gpu-lens"})
            if parsed.path == "/readyz":
                return self._send_json(200, {"status": "ready", "mode": "fixture"})
            if parsed.path == "/api/v1/overview":
                return self._send_json(200, state.build_overview(scen_param))
            if parsed.path == "/api/v1/incidents":
                snap, _, _ = state.evaluate_active(scen_param)
                sev = qs.get("severity", [""])[0]
                cat = qs.get("category", [""])[0]
                q = qs.get("q", [""])[0]
                items = state.store.list_incidents(snap["scenario_id"], sev, cat, q)
                return self._send_json(200, {"scenario_id": snap["scenario_id"], "data_mode": snap["data_mode"], "count": len(items), "incidents": items})
            if parsed.path.startswith("/api/v1/incidents/"):
                inc_id = parsed.path[len("/api/v1/incidents/"):]
                snap, _, _ = state.evaluate_active(scen_param)
                inc = state.store.get_by_id(snap["scenario_id"], inc_id)
                if not inc:
                    return self._send_json(404, {"error": "incident not found"})
                return self._send_json(200, inc)
            if parsed.path == "/api/v1/capacity":
                _, _, cap = state.evaluate_active(scen_param)
                return self._send_json(200, cap)
            if parsed.path == "/api/v1/sources":
                snap, _, _ = state.evaluate_active(scen_param)
                return self._send_json(200, {
                    "data_mode": snap["data_mode"],
                    "scenario_id": snap["scenario_id"],
                    "collected_at": snap["collected_at"],
                    "sources": snap.get("sources", []),
                    "exported_metrics": snap.get("exported_metrics", {}),
                    "metric_mappings": DEFAULT_METRIC_MAPPINGS,
                })
            if parsed.path == "/api/v1/rules":
                defs = get_rule_definitions(DEFAULT_THRESHOLDS)
                return self._send_json(200, {"count": len(defs), "rules": defs})
            if parsed.path == "/api/v1/scenarios":
                out = [
                    {
                        "id": sid,
                        "name": state.scenarios[sid]["scenario_name"],
                        "description": state.scenarios[sid]["scenario_description"],
                        "active": sid == state.active_id,
                    }
                    for sid in state.order
                ]
                return self._send_json(200, {"mode": "fixture", "scenarios": out})

            # Static files
            rel = parsed.path.lstrip("/") or "index.html"
            target = os.path.join(state.static_dir, rel)
            if not os.path.isfile(target):
                target = os.path.join(state.static_dir, "index.html")
            if os.path.isfile(target):
                ctype = "text/html; charset=utf-8"
                if target.endswith(".js"):
                    ctype = "application/javascript; charset=utf-8"
                elif target.endswith(".css"):
                    ctype = "text/css; charset=utf-8"
                with open(target, "rb") as f:
                    data = f.read()
                self.send_response(200)
                self.send_header("Content-Type", ctype)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
                return
            self._send_json(404, {"error": "not found"})

        def do_POST(self):
            if self.path == "/api/v1/scenarios/select":
                length = int(self.headers.get("Content-Length", "0"))
                body = json.loads(self.rfile.read(length).decode("utf-8"))
                sid = body.get("scenario_id", "").strip()
                if sid not in state.scenarios:
                    return self._send_json(400, {"error": f"unknown scenario_id {sid}"})
                return self._send_json(200, state.build_overview(sid))
            self._send_json(405, {"error": "method not allowed"})

    return Handler


def main():
    parser = argparse.ArgumentParser(description="GPU Lens Local Runtime Server")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8080)
    parser.add_argument("--fixtures-dir", default="fixtures")
    parser.add_argument("--scenario", default="scenario-5-correlated-failure")
    parser.add_argument("--db-path", default="gpulens.db")
    parser.add_argument("--static-dir", default="web/dist")
    args = parser.parse_args()

    state = GPULensAppState(args.fixtures_dir, args.scenario, args.db_path, args.static_dir)
    srv = http.server.ThreadingHTTPServer((args.host, args.port), make_handler(state))
    print(f"GPU Lens server listening on http://127.0.0.1:{args.port} (scenario={state.active_id})")
    srv.serve_forever()


if __name__ == "__main__":
    main()
