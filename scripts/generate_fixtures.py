#!/usr/bin/env python3
"""
Deterministic Offline Fixture Generator for GPU Lens.
Generates 6 realistic, reproducible cluster scenarios in fixtures/*.json with zero randomness.
"""
import json
import os
from datetime import datetime, timedelta, timezone

BASE_TIME = datetime(2026, 10, 18, 12, 0, 0, tzinfo=timezone.utc)


def iso(dt: datetime) -> str:
    return dt.strftime("%Y-%m-%dT%H:%M:%SZ")


def make_samples(
    count: int,
    step_mins: int,
    base_util: float,
    mem_copy: float,
    fb_used: float,
    fb_free: float,
    gpu_temp: float,
    mem_temp: float,
    power_w: float,
    xid_at_idx: dict = None,
    pcie_replay: int = 0,
    row_remap: int = 0,
    uncorr_rows: int = 0,
    ecc_dbe: int = None,
    nvlink_crc: int = None,
    thermal_violation_ns: int = None,
    health_status: int = None,
    clock_reason: str = "",
    end_time: datetime = None,
):
    if end_time is None:
        end_time = BASE_TIME - timedelta(seconds=15)
    xid_at_idx = xid_at_idx or {}
    samples = []
    for i in range(count):
        wave = ((i % 4) - 1.5) * 1.2
        u = max(0.0, min(100.0, round(base_util + wave, 1)))
        mc = max(0.0, min(100.0, round(mem_copy + (wave * 0.5), 1)))
        t_offset = (count - 1 - i) * step_mins
        ts = end_time - timedelta(minutes=t_offset)
        pt = {
            "timestamp": iso(ts),
            "gpu_util_percent": u,
            "mem_copy_util_percent": mc,
            "fb_used_mib": fb_used,
            "fb_free_mib": fb_free,
            "gpu_temp_celsius": round(gpu_temp + (wave * 0.4), 1),
            "memory_temp_celsius": round(mem_temp + (wave * 0.4), 1),
            "power_usage_watts": round(power_w + (wave * 3.0), 1),
            "xid_error_code": xid_at_idx.get(i, 0),
            "pcie_replay_counter": pcie_replay if i == count - 1 else max(0, pcie_replay // 2),
            "row_remap_failure": row_remap if i >= count - 2 else 0,
            "uncorrectable_remapped_rows": uncorr_rows if i >= count - 2 else 0,
        }
        if ecc_dbe is not None:
            pt["ecc_dbe_vol_total"] = ecc_dbe if i >= count - 2 else 0
        if nvlink_crc is not None:
            pt["nvlink_crc_flit_errors"] = nvlink_crc if i >= count - 2 else 0
        if thermal_violation_ns is not None:
            pt["thermal_violation_ns"] = thermal_violation_ns if i >= count - 2 else 0
        if health_status is not None:
            pt["health_status"] = health_status if i >= count - 2 else 0
        if clock_reason and i >= count - 2:
            pt["clock_throttle_reason"] = clock_reason
        samples.append(pt)
    return samples


def build_gpu(
    uuid: str,
    idx: int,
    node: str,
    model: str,
    pci: str,
    ns: str,
    pod: str,
    container: str,
    mapping_status: str,
    samples: list,
    power_limit: float = 700.0,
    total_mem: float = 81920.0,
    stale_seconds: float = 15.0,
    is_stale: bool = False,
):
    latest = dict(samples[-1])
    return {
        "uuid": uuid,
        "device_index": idx,
        "node_name": node,
        "model_name": model,
        "pci_bus_id": pci,
        "driver_version": "550.90.07",
        "power_limit_watts": power_limit,
        "total_memory_mib": total_mem,
        "allocated_namespace": ns,
        "allocated_pod": pod,
        "allocated_container": container,
        "mapping_status": mapping_status,
        "last_sample_at": latest["timestamp"],
        "staleness_seconds": stale_seconds,
        "is_stale": is_stale,
        "latest": latest,
        "samples": samples,
    }


def default_sources(stale: bool = False):
    if not stale:
        return [
            {
                "id": "dcgm-prometheus",
                "name": "Prometheus (DCGM Exporter Scrape Pool)",
                "kind": "prometheus",
                "mode": "simulated",
                "endpoint": "http://prometheus.monitoring.svc:9090 (Simulated Fixture)",
                "available": True,
                "read_only_verified": True,
                "last_success_at": iso(BASE_TIME - timedelta(seconds=15)),
                "staleness_seconds": 15.0,
                "is_stale": False,
                "exported_required_count": 11,
                "exported_optional_count": 6,
                "missing_optional": ["DCGM_FI_DEV_POWER_VIOLATION"],
                "notes": "Simulated offline fixture representing Prometheus scraping dcgm-exporter v3.3.8 across GPU nodes.",
            },
            {
                "id": "kubernetes-api-readonly",
                "name": "Kubernetes API Server (Read-Only)",
                "kind": "kubernetes",
                "mode": "simulated",
                "endpoint": "https://kubernetes.default.svc:443 (Simulated Fixture)",
                "available": True,
                "read_only_verified": True,
                "last_success_at": iso(BASE_TIME - timedelta(seconds=10)),
                "staleness_seconds": 10.0,
                "is_stale": False,
                "exported_required_count": 3,
                "exported_optional_count": 2,
                "notes": "Simulated offline fixture representing GET/LIST on v1.Nodes, v1.Pods, and v1.Events.",
            },
        ]
    else:
        return [
            {
                "id": "dcgm-prometheus",
                "name": "Prometheus (DCGM Exporter Scrape Pool)",
                "kind": "prometheus",
                "mode": "simulated",
                "endpoint": "http://prometheus.monitoring.svc:9090 (Simulated Fixture)",
                "available": False,
                "read_only_verified": True,
                "last_success_at": iso(BASE_TIME - timedelta(minutes=14)),
                "last_error": "Get \"http://10.42.2.19:9400/metrics\": context deadline exceeded (nv-hostengine scrape timeout on gpu-node-02)",
                "staleness_seconds": 840.0,
                "is_stale": True,
                "exported_required_count": 11,
                "exported_optional_count": 2,
                "missing_optional": [
                    "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
                    "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL",
                    "DCGM_EXP_GPU_HEALTH_STATUS",
                ],
                "notes": "Partial scrape failure: gpu-node-02 dcgm-exporter timed out 14m ago; gpu-node-01 lost kubelet pod-resources socket mapping.",
            },
            {
                "id": "kubernetes-api-readonly",
                "name": "Kubernetes API Server (Read-Only)",
                "kind": "kubernetes",
                "mode": "simulated",
                "endpoint": "https://kubernetes.default.svc:443 (Simulated Fixture)",
                "available": True,
                "read_only_verified": True,
                "last_success_at": iso(BASE_TIME - timedelta(seconds=12)),
                "staleness_seconds": 12.0,
                "is_stale": False,
                "exported_required_count": 3,
                "exported_optional_count": 2,
                "notes": "Kubernetes control plane healthy; pod-resources gRPC socket on gpu-node-01 requires directory mount.",
            },
        ]


def default_exported_metrics():
    return {
        "DCGM_FI_DEV_GPU_UTIL": True,
        "DCGM_FI_DEV_MEM_COPY_UTIL": True,
        "DCGM_FI_DEV_FB_USED": True,
        "DCGM_FI_DEV_FB_FREE": True,
        "DCGM_FI_DEV_GPU_TEMP": True,
        "DCGM_FI_DEV_MEMORY_TEMP": True,
        "DCGM_FI_DEV_POWER_USAGE": True,
        "DCGM_FI_DEV_XID_ERRORS": True,
        "DCGM_FI_DEV_PCIE_REPLAY_COUNTER": True,
        "DCGM_FI_DEV_ROW_REMAP_FAILURE": True,
        "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS": True,
        "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL": True,
        "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL": True,
        "DCGM_FI_DEV_THERMAL_VIOLATION": True,
        "DCGM_EXP_GPU_HEALTH_STATUS": True,
    }


def scenario_1_healthy():
    gpus = []
    for i in range(4):
        s = make_samples(10, 10, 88.0 + i, 52.0, 68000, 13920, 64.0 + i, 69.0 + i, 580.0 + i * 5, ecc_dbe=0, nvlink_crc=0, health_status=0)
        gpus.append(build_gpu(f"GPU-h100-n01-{i}", i, "gpu-node-01", "NVIDIA H100 80GB HBM3", f"00000000:1{i}:00.0", "ml-training", "llama3-pretrain-rank-0", "pytorch-trainer", "mapped", s))
    for i in range(4):
        if i < 2:
            s = make_samples(10, 10, 74.0 + i, 41.0, 54000, 27920, 61.0 + i, 66.0 + i, 510.0, ecc_dbe=0, nvlink_crc=0, health_status=0)
            gpus.append(build_gpu(f"GPU-h100-n02-{i}", i, "gpu-node-02", "NVIDIA H100 80GB HBM3", f"00000000:2{i}:00.0", "ml-inference", "vllm-mistral-serve-0", "vllm-engine", "mapped", s))
        else:
            s = make_samples(10, 10, 0.0, 0.0, 256, 81664, 36.0, 39.0, 68.0, ecc_dbe=0, nvlink_crc=0, health_status=0)
            gpus.append(build_gpu(f"GPU-h100-n02-{i}", i, "gpu-node-02", "NVIDIA H100 80GB HBM3", f"00000000:2{i}:00.0", "", "", "", "unallocated", s))

    return {
        "scenario_id": "scenario-1-healthy",
        "scenario_name": "1. Healthy Cluster (No Active Incidents)",
        "scenario_description": "Baseline 8-GPU H100 cluster with 6 allocated GPUs running distributed training and vLLM inference within normal thermal, error, and utilization envelopes.",
        "cluster_name": "prod-gpu-us-west-2",
        "data_mode": "simulated_fixture",
        "collected_at": iso(BASE_TIME),
        "sources": default_sources(False),
        "exported_metrics": default_exported_metrics(),
        "nodes": [
            {
                "name": "gpu-node-01",
                "ready": True,
                "schedulable": True,
                "gpu_model": "NVIDIA H100 80GB HBM3",
                "gpu_capacity": 4,
                "gpu_allocatable": 4,
                "gpu_allocated": 4,
                "labels": {"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3", "topology.kubernetes.io/rack": "rack-a1"},
                "conditions": [{"type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status", "last_transition_time": iso(BASE_TIME - timedelta(days=5))}],
            },
            {
                "name": "gpu-node-02",
                "ready": True,
                "schedulable": True,
                "gpu_model": "NVIDIA H100 80GB HBM3",
                "gpu_capacity": 4,
                "gpu_allocatable": 4,
                "gpu_allocated": 2,
                "labels": {"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3", "topology.kubernetes.io/rack": "rack-a2"},
                "conditions": [{"type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status", "last_transition_time": iso(BASE_TIME - timedelta(days=5))}],
            },
        ],
        "gpus": gpus,
        "pods": [
            {
                "name": "llama3-pretrain-rank-0",
                "namespace": "ml-training",
                "uid": "pod-uid-101",
                "owner_kind": "PyTorchJob",
                "owner_name": "llama3-pretrain",
                "phase": "Running",
                "node_name": "gpu-node-01",
                "requested_gpus": 4,
                "assigned_gpu_uuids": [f"GPU-h100-n01-{i}" for i in range(4)],
                "created_at": iso(BASE_TIME - timedelta(hours=6)),
                "started_at": iso(BASE_TIME - timedelta(hours=6)),
                "restart_count": 0,
            },
            {
                "name": "vllm-mistral-serve-0",
                "namespace": "ml-inference",
                "uid": "pod-uid-102",
                "owner_kind": "Deployment",
                "owner_name": "vllm-mistral-serve",
                "phase": "Running",
                "node_name": "gpu-node-02",
                "requested_gpus": 2,
                "assigned_gpu_uuids": ["GPU-h100-n02-0", "GPU-h100-n02-1"],
                "created_at": iso(BASE_TIME - timedelta(hours=4)),
                "started_at": iso(BASE_TIME - timedelta(hours=4)),
                "restart_count": 0,
            },
        ],
        "events": [],
    }


def scenario_2_pending_capacity():
    gpus = []
    for i in range(4):
        if i < 3:
            s = make_samples(10, 10, 85.0, 48.0, 62000, 19920, 65.0, 70.0, 560.0)
            gpus.append(build_gpu(f"GPU-h100-n01-{i}", i, "gpu-node-01", "NVIDIA H100 80GB HBM3", f"00000000:1{i}:00.0", "ml-training", f"embed-worker-{i}", "trainer", "mapped", s))
        else:
            s = make_samples(10, 10, 0.0, 0.0, 256, 81664, 37.0, 40.0, 65.0)
            gpus.append(build_gpu(f"GPU-h100-n01-{i}", i, "gpu-node-01", "NVIDIA H100 80GB HBM3", f"00000000:1{i}:00.0", "", "", "", "unallocated", s))
    for i in range(4):
        if i < 2:
            s = make_samples(10, 10, 79.0, 39.0, 58000, 23920, 63.0, 68.0, 530.0)
            gpus.append(build_gpu(f"GPU-h100-n02-{i}", i, "gpu-node-02", "NVIDIA H100 80GB HBM3", f"00000000:2{i}:00.0", "ml-inference", "triton-asr-0", "triton", "mapped", s))
        else:
            s = make_samples(10, 10, 0.0, 0.0, 256, 81664, 36.0, 39.0, 66.0)
            gpus.append(build_gpu(f"GPU-h100-n02-{i}", i, "gpu-node-02", "NVIDIA H100 80GB HBM3", f"00000000:2{i}:00.0", "", "", "", "unallocated", s))

    return {
        "scenario_id": "scenario-2-pending-capacity",
        "scenario_name": "2. Pending GPU Workloads (Capacity & Fragmentation)",
        "scenario_description": "Two pending GPU jobs: a 4-GPU distributed training pod blocked by insufficient aggregate/contiguous node capacity (3 free GPUs fragmented across 2 nodes) and a 2-GPU pod blocked by nodeSelector hardware SKU mismatch.",
        "cluster_name": "prod-gpu-us-west-2",
        "data_mode": "simulated_fixture",
        "collected_at": iso(BASE_TIME),
        "sources": default_sources(False),
        "exported_metrics": default_exported_metrics(),
        "nodes": [
            {
                "name": "gpu-node-01",
                "ready": True,
                "schedulable": True,
                "gpu_model": "NVIDIA H100 80GB HBM3",
                "gpu_capacity": 4,
                "gpu_allocatable": 4,
                "gpu_allocated": 3,
                "labels": {"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3"},
                "conditions": [{"type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status", "last_transition_time": iso(BASE_TIME - timedelta(days=5))}],
            },
            {
                "name": "gpu-node-02",
                "ready": True,
                "schedulable": True,
                "gpu_model": "NVIDIA H100 80GB HBM3",
                "gpu_capacity": 4,
                "gpu_allocatable": 4,
                "gpu_allocated": 2,
                "labels": {"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3"},
                "conditions": [{"type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status", "last_transition_time": iso(BASE_TIME - timedelta(days=5))}],
            },
        ],
        "gpus": gpus,
        "pods": [
            {
                "name": "llama3-70b-finetune-master-0",
                "namespace": "ml-training",
                "uid": "pod-uid-201",
                "owner_kind": "PyTorchJob",
                "owner_name": "llama3-70b-finetune",
                "phase": "Pending",
                "requested_gpus": 4,
                "created_at": iso(BASE_TIME - timedelta(minutes=42)),
                "scheduler_reason": "FailedScheduling",
                "scheduler_message": "0/2 nodes are available: 2 Insufficient nvidia.com/gpu. preemption: 0/2 nodes are available: 2 No preemption victims found for incoming pod.",
            },
            {
                "name": "blackwell-fp4-benchmark-0",
                "namespace": "ml-research",
                "uid": "pod-uid-202",
                "owner_kind": "Job",
                "owner_name": "blackwell-fp4-benchmark",
                "phase": "Pending",
                "requested_gpus": 2,
                "node_selector": {"nvidia.com/gpu.product": "NVIDIA-B200"},
                "created_at": iso(BASE_TIME - timedelta(minutes=18)),
                "scheduler_reason": "FailedScheduling",
                "scheduler_message": "0/2 nodes are available: 2 node(s) didn't match Pod's node affinity/selector. preemption: 0/2 nodes are available: 2 Preemption is not helpful for scheduling.",
            },
            {
                "name": "embed-worker-0",
                "namespace": "ml-training",
                "uid": "pod-uid-203",
                "owner_kind": "Job",
                "owner_name": "embed-batch",
                "phase": "Running",
                "node_name": "gpu-node-01",
                "requested_gpus": 3,
                "assigned_gpu_uuids": ["GPU-h100-n01-0", "GPU-h100-n01-1", "GPU-h100-n01-2"],
                "created_at": iso(BASE_TIME - timedelta(hours=3)),
                "started_at": iso(BASE_TIME - timedelta(hours=3)),
            },
            {
                "name": "triton-asr-0",
                "namespace": "ml-inference",
                "uid": "pod-uid-204",
                "owner_kind": "Deployment",
                "owner_name": "triton-asr",
                "phase": "Running",
                "node_name": "gpu-node-02",
                "requested_gpus": 2,
                "assigned_gpu_uuids": ["GPU-h100-n02-0", "GPU-h100-n02-1"],
                "created_at": iso(BASE_TIME - timedelta(hours=5)),
                "started_at": iso(BASE_TIME - timedelta(hours=5)),
            },
        ],
        "events": [
            {
                "id": "ev-sched-201",
                "type": "Warning",
                "reason": "FailedScheduling",
                "message": "0/2 nodes are available: 2 Insufficient nvidia.com/gpu. preemption: 0/2 nodes are available: 2 No preemption victims found for incoming pod.",
                "source_component": "default-scheduler",
                "involved_object_kind": "Pod",
                "involved_object_namespace": "ml-training",
                "involved_object_name": "llama3-70b-finetune-master-0",
                "count": 19,
                "first_timestamp": iso(BASE_TIME - timedelta(minutes=42)),
                "last_timestamp": iso(BASE_TIME - timedelta(minutes=1)),
            },
            {
                "id": "ev-sched-202",
                "type": "Warning",
                "reason": "FailedScheduling",
                "message": "0/2 nodes are available: 2 node(s) didn't match Pod's node affinity/selector (nvidia.com/gpu.product=NVIDIA-B200).",
                "source_component": "default-scheduler",
                "involved_object_kind": "Pod",
                "involved_object_namespace": "ml-research",
                "involved_object_name": "blackwell-fp4-benchmark-0",
                "count": 8,
                "first_timestamp": iso(BASE_TIME - timedelta(minutes=18)),
                "last_timestamp": iso(BASE_TIME - timedelta(minutes=1)),
            },
        ],
    }


def scenario_3_gpu_health_warning():
    gpus = []
    s0 = make_samples(
        10, 10, 72.0, 38.0, 61000, 20920, 89.2, 93.5, 688.0,
        xid_at_idx={9: 48},
        pcie_replay=24,
        row_remap=1,
        uncorr_rows=3,
        ecc_dbe=2,
        nvlink_crc=6,
        thermal_violation_ns=480000000,
        health_status=20,
        clock_reason="hw_thermal",
    )
    gpus.append(build_gpu("GPU-h100-n01-0", 0, "gpu-node-01", "NVIDIA H100 80GB HBM3", "00000000:10:00.0", "ml-training", "vision-vit-huge-0", "trainer", "mapped", s0))

    for i in range(1, 4):
        s = make_samples(10, 10, 82.0, 45.0, 60000, 21920, 66.0, 71.0, 575.0, ecc_dbe=0, nvlink_crc=0, health_status=0)
        gpus.append(build_gpu(f"GPU-h100-n01-{i}", i, "gpu-node-01", "NVIDIA H100 80GB HBM3", f"00000000:1{i}:00.0", "ml-training", "vision-vit-huge-0", "trainer", "mapped", s))

    return {
        "scenario_id": "scenario-3-gpu-health-warning",
        "scenario_name": "3. GPU Hardware Health & Thermal Warning (XID 48 + Row Remap + NPD Event)",
        "scenario_description": "GPU-h100-n01-0 exhibits uncorrectable HBM double-bit ECC errors (XID 48), row-remap failure, NVLink CRC flit errors, and 89.2C hardware thermal slowdown corroborated by Node Problem Detector conditions and reduced node allocatable GPUs.",
        "cluster_name": "prod-gpu-us-west-2",
        "data_mode": "simulated_fixture",
        "collected_at": iso(BASE_TIME),
        "sources": default_sources(False),
        "exported_metrics": default_exported_metrics(),
        "nodes": [
            {
                "name": "gpu-node-01",
                "ready": True,
                "schedulable": True,
                "gpu_model": "NVIDIA H100 80GB HBM3",
                "gpu_capacity": 4,
                "gpu_allocatable": 3,
                "gpu_allocated": 3,
                "labels": {"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3"},
                "conditions": [
                    {"type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status", "last_transition_time": iso(BASE_TIME - timedelta(days=5))},
                    {"type": "GPUXIDErrors", "status": "True", "reason": "XIDError48Detected", "message": "XID 48 (Double Bit ECC Error) and row remap failure detected on PCI 00000000:10:00.0 (GPU-h100-n01-0)", "last_transition_time": iso(BASE_TIME - timedelta(minutes=3))},
                    {"type": "GPUClockThrottling", "status": "True", "reason": "HWThermalSlowdown", "message": "GPU 0 clock throttled due to HW_THERMAL_SLOWDOWN (die temp 89.2C)", "last_transition_time": iso(BASE_TIME - timedelta(minutes=4))},
                ],
            }
        ],
        "gpus": gpus,
        "pods": [
            {
                "name": "vision-vit-huge-0",
                "namespace": "ml-training",
                "uid": "pod-uid-301",
                "owner_kind": "PyTorchJob",
                "owner_name": "vision-vit-huge",
                "phase": "Running",
                "node_name": "gpu-node-01",
                "requested_gpus": 4,
                "assigned_gpu_uuids": [f"GPU-h100-n01-{i}" for i in range(4)],
                "created_at": iso(BASE_TIME - timedelta(hours=4)),
                "started_at": iso(BASE_TIME - timedelta(hours=4)),
                "restart_count": 0,
            }
        ],
        "events": [
            {
                "id": "ev-npd-301",
                "type": "Warning",
                "reason": "XIDError48Detected",
                "message": "Node Problem Detector: NVRM: Xid (PCI:0000:10:00): 48, pid=41822, name=python3, Double Bit ECC Error; DCGM_FI_DEV_ROW_REMAP_FAILURE=1",
                "source_component": "node-problem-detector",
                "involved_object_kind": "Node",
                "involved_object_name": "gpu-node-01",
                "node_name": "gpu-node-01",
                "count": 2,
                "first_timestamp": iso(BASE_TIME - timedelta(minutes=4)),
                "last_timestamp": iso(BASE_TIME - timedelta(minutes=2)),
            }
        ],
    }


def scenario_4_persistent_low_utilization():
    gpus = []
    for i in range(2):
        s = make_samples(10, 10, 4.2, 1.8, 3800, 78120, 41.0, 44.0, 92.0)
        gpus.append(build_gpu(f"GPU-h100-n01-{i}", i, "gpu-node-01", "NVIDIA H100 80GB HBM3", f"00000000:1{i}:00.0", "ml-notebooks", "jupyter-research-dev-0", "notebook", "mapped", s))
    for i in range(2, 4):
        s = make_samples(10, 10, 7.8, 6.2, 75000, 6920, 52.0, 58.0, 185.0)
        gpus.append(build_gpu(f"GPU-h100-n01-{i}", i, "gpu-node-01", "NVIDIA H100 80GB HBM3", f"00000000:1{i}:00.0", "ml-inference", "llama3-70b-int8-realtime-0", "vllm", "mapped", s))

    return {
        "scenario_id": "scenario-4-low-utilization",
        "scenario_name": "4. Persistently Low GPU Utilization (VRAM-Bound vs. Idle / Data-Stalled)",
        "scenario_description": "Two allocated workloads with <15% compute utilization over 90m: (1) an interactive notebook using 4.2% SM compute and 4.6% VRAM, and (2) a 70B LLM inference service using 7.8% SM compute but 91.6% HBM VRAM residency.",
        "cluster_name": "prod-gpu-us-west-2",
        "data_mode": "simulated_fixture",
        "collected_at": iso(BASE_TIME),
        "sources": default_sources(False),
        "exported_metrics": default_exported_metrics(),
        "nodes": [
            {
                "name": "gpu-node-01",
                "ready": True,
                "schedulable": True,
                "gpu_model": "NVIDIA H100 80GB HBM3",
                "gpu_capacity": 4,
                "gpu_allocatable": 4,
                "gpu_allocated": 4,
                "labels": {"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3"},
                "conditions": [{"type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status", "last_transition_time": iso(BASE_TIME - timedelta(days=5))}],
            }
        ],
        "gpus": gpus,
        "pods": [
            {
                "name": "jupyter-research-dev-0",
                "namespace": "ml-notebooks",
                "uid": "pod-uid-401",
                "owner_kind": "StatefulSet",
                "owner_name": "jupyter-research-dev",
                "phase": "Running",
                "node_name": "gpu-node-01",
                "requested_gpus": 2,
                "assigned_gpu_uuids": ["GPU-h100-n01-0", "GPU-h100-n01-1"],
                "created_at": iso(BASE_TIME - timedelta(hours=3)),
                "started_at": iso(BASE_TIME - timedelta(hours=3)),
                "restart_count": 0,
            },
            {
                "name": "llama3-70b-int8-realtime-0",
                "namespace": "ml-inference",
                "uid": "pod-uid-402",
                "owner_kind": "Deployment",
                "owner_name": "llama3-70b-int8-realtime",
                "phase": "Running",
                "node_name": "gpu-node-01",
                "requested_gpus": 2,
                "assigned_gpu_uuids": ["GPU-h100-n01-2", "GPU-h100-n01-3"],
                "created_at": iso(BASE_TIME - timedelta(hours=4)),
                "started_at": iso(BASE_TIME - timedelta(hours=4)),
                "restart_count": 0,
            },
        ],
        "events": [],
    }


def scenario_5_correlated_workload_failure():
    gpus = []
    s0 = make_samples(
        10, 5, 86.0, 49.0, 69000, 12920, 78.0, 82.0, 610.0,
        xid_at_idx={8: 79, 9: 79},
        pcie_replay=42,
        row_remap=0,
        ecc_dbe=0,
        nvlink_crc=3,
        health_status=20,
    )
    gpus.append(build_gpu("GPU-h100-n01-0", 0, "gpu-node-01", "NVIDIA H100 80GB HBM3", "00000000:10:00.0", "ml-training", "multimodal-train-worker-0", "pytorch-worker", "mapped", s0))

    s1 = make_samples(10, 5, 84.0, 47.0, 69000, 12920, 67.0, 71.0, 580.0, ecc_dbe=0, nvlink_crc=0, health_status=0)
    gpus.append(build_gpu("GPU-h100-n01-1", 1, "gpu-node-01", "NVIDIA H100 80GB HBM3", "00000000:11:00.0", "ml-training", "multimodal-train-worker-0", "pytorch-worker", "mapped", s1))

    for i in range(2, 4):
        s = make_samples(10, 5, 79.0, 42.0, 52000, 29920, 62.0, 66.0, 520.0, ecc_dbe=0, nvlink_crc=0, health_status=0)
        gpus.append(build_gpu(f"GPU-h100-n01-{i}", i, "gpu-node-01", "NVIDIA H100 80GB HBM3", f"00000000:1{i}:00.0", "ml-research", "dataloader-oom-job-0", "trainer", "mapped", s))

    return {
        "scenario_id": "scenario-5-correlated-failure",
        "scenario_name": "5. Workload Failure Correlation (Hardware XID 79 vs. Userspace OOM)",
        "scenario_description": "Differential diagnosis of two crashing GPU pods: (1) multimodal-train-worker-0 crashed 90s after XID 79 (GPU fallen off bus) + PCIe replay spike on GPU-h100-n01-0, and (2) dataloader-oom-job-0 crashed with ExitCode 137 (OOMKilled) while its assigned GPUs remained completely healthy.",
        "cluster_name": "prod-gpu-us-west-2",
        "data_mode": "simulated_fixture",
        "collected_at": iso(BASE_TIME),
        "sources": default_sources(False),
        "exported_metrics": default_exported_metrics(),
        "nodes": [
            {
                "name": "gpu-node-01",
                "ready": True,
                "schedulable": True,
                "gpu_model": "NVIDIA H100 80GB HBM3",
                "gpu_capacity": 4,
                "gpu_allocatable": 3,
                "gpu_allocated": 3,
                "labels": {"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3"},
                "conditions": [
                    {"type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status", "last_transition_time": iso(BASE_TIME - timedelta(days=5))},
                    {"type": "GPUXIDErrors", "status": "True", "reason": "XID79FallenOffBus", "message": "GPU-h100-n01-0 (PCI 0000:10:00.0) reported XID 79: GPU has fallen off the bus", "last_transition_time": iso(BASE_TIME - timedelta(minutes=5))},
                ],
            }
        ],
        "gpus": gpus,
        "pods": [
            {
                "name": "multimodal-train-worker-0",
                "namespace": "ml-training",
                "uid": "pod-uid-501",
                "owner_kind": "PyTorchJob",
                "owner_name": "multimodal-train",
                "phase": "Running",
                "node_name": "gpu-node-01",
                "requested_gpus": 2,
                "assigned_gpu_uuids": ["GPU-h100-n01-0", "GPU-h100-n01-1"],
                "created_at": iso(BASE_TIME - timedelta(hours=3)),
                "started_at": iso(BASE_TIME - timedelta(hours=3)),
                "restart_count": 3,
                "last_termination": {
                    "exit_code": 1,
                    "reason": "Error",
                    "message": "RuntimeError: NCCL error / CUDA error: unknown error (device-side assert or bus disconnect on cuda:0)",
                    "started_at": iso(BASE_TIME - timedelta(hours=2)),
                    "finished_at": iso(BASE_TIME - timedelta(minutes=3, seconds=30)),
                },
            },
            {
                "name": "dataloader-oom-job-0",
                "namespace": "ml-research",
                "uid": "pod-uid-502",
                "owner_kind": "Job",
                "owner_name": "dataloader-oom-job",
                "phase": "Failed",
                "node_name": "gpu-node-01",
                "requested_gpus": 2,
                "assigned_gpu_uuids": ["GPU-h100-n01-2", "GPU-h100-n01-3"],
                "created_at": iso(BASE_TIME - timedelta(hours=2)),
                "started_at": iso(BASE_TIME - timedelta(hours=2)),
                "restart_count": 2,
                "last_termination": {
                    "exit_code": 137,
                    "reason": "OOMKilled",
                    "message": "Container exceeded host memory limit (64Gi) in DataLoader worker pool",
                    "started_at": iso(BASE_TIME - timedelta(minutes=50)),
                    "finished_at": iso(BASE_TIME - timedelta(minutes=6)),
                },
            },
        ],
        "events": [
            {
                "id": "ev-corr-501",
                "type": "Warning",
                "reason": "XIDError",
                "message": "NVRM: Xid (PCI:0000:10:00): 79, pid=19402, GPU has fallen off the bus.",
                "source_component": "node-problem-detector",
                "involved_object_kind": "Node",
                "involved_object_name": "gpu-node-01",
                "node_name": "gpu-node-01",
                "count": 1,
                "first_timestamp": iso(BASE_TIME - timedelta(minutes=5)),
                "last_timestamp": iso(BASE_TIME - timedelta(minutes=5)),
            },
            {
                "id": "ev-corr-502",
                "type": "Warning",
                "reason": "BackOff",
                "message": "Back-off restarting failed container pytorch-worker in pod multimodal-train-worker-0_ml-training",
                "source_component": "kubelet",
                "involved_object_kind": "Pod",
                "involved_object_namespace": "ml-training",
                "involved_object_name": "multimodal-train-worker-0",
                "node_name": "gpu-node-01",
                "count": 3,
                "first_timestamp": iso(BASE_TIME - timedelta(minutes=3)),
                "last_timestamp": iso(BASE_TIME - timedelta(minutes=1)),
            },
        ],
    }


def scenario_6_stale_and_unmapped_telemetry():
    gpus = []
    for i in range(4):
        s = make_samples(10, 10, 12.0, 5.0, 44000, 37920, 55.0, 59.0, 310.0)
        gpus.append(build_gpu(f"GPU-h100-n01-{i}", i, "gpu-node-01", "NVIDIA H100 80GB HBM3", f"00000000:1{i}:00.0", "", "", "", "missing_pod_labels", s, stale_seconds=12.0, is_stale=False))

    stale_end = BASE_TIME - timedelta(minutes=14)
    for i in range(4):
        s = make_samples(10, 10, 76.0, 38.0, 59000, 22920, 64.0, 68.0, 540.0, end_time=stale_end)
        gpus.append(build_gpu(f"GPU-h100-n02-{i}", i, "gpu-node-02", "NVIDIA H100 80GB HBM3", f"00000000:2{i}:00.0", "ml-training", "distributed-rlhf-worker-1", "trainer", "mapped", s, stale_seconds=840.0, is_stale=True))

    return {
        "scenario_id": "scenario-6-stale-telemetry",
        "scenario_name": "6. Stale & Uncorrelated Telemetry (Monitoring Blind Spots)",
        "scenario_description": "Demonstrates honest blind-spot handling: gpu-node-02 telemetry is 14m stale (nv-hostengine timeout) while a pod on gpu-node-02 restarted, and gpu-node-01 GPUs are missing Kubernetes pod labels due to a broken kubelet pod-resources socket.",
        "cluster_name": "prod-gpu-us-west-2",
        "data_mode": "simulated_fixture",
        "collected_at": iso(BASE_TIME),
        "sources": default_sources(True),
        "exported_metrics": default_exported_metrics(),
        "nodes": [
            {
                "name": "gpu-node-01",
                "ready": True,
                "schedulable": True,
                "gpu_model": "NVIDIA H100 80GB HBM3",
                "gpu_capacity": 4,
                "gpu_allocatable": 4,
                "gpu_allocated": 4,
                "labels": {"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3"},
            },
            {
                "name": "gpu-node-02",
                "ready": True,
                "schedulable": True,
                "gpu_model": "NVIDIA H100 80GB HBM3",
                "gpu_capacity": 4,
                "gpu_allocatable": 4,
                "gpu_allocated": 4,
                "labels": {"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3"},
            },
        ],
        "gpus": gpus,
        "pods": [
            {
                "name": "unmapped-inference-pod-0",
                "namespace": "ml-inference",
                "uid": "pod-uid-601",
                "owner_kind": "Deployment",
                "owner_name": "unmapped-inference",
                "phase": "Running",
                "node_name": "gpu-node-01",
                "requested_gpus": 4,
                "assigned_gpu_uuids": [f"GPU-h100-n01-{i}" for i in range(4)],
                "created_at": iso(BASE_TIME - timedelta(hours=5)),
                "started_at": iso(BASE_TIME - timedelta(hours=5)),
            },
            {
                "name": "distributed-rlhf-worker-1",
                "namespace": "ml-training",
                "uid": "pod-uid-602",
                "owner_kind": "PyTorchJob",
                "owner_name": "distributed-rlhf",
                "phase": "Running",
                "node_name": "gpu-node-02",
                "requested_gpus": 4,
                "assigned_gpu_uuids": [f"GPU-h100-n02-{i}" for i in range(4)],
                "created_at": iso(BASE_TIME - timedelta(hours=4)),
                "started_at": iso(BASE_TIME - timedelta(hours=4)),
                "restart_count": 1,
                "last_termination": {
                    "exit_code": 139,
                    "reason": "Error",
                    "message": "Segmentation fault / CUDA driver transport timeout during collective step",
                    "started_at": iso(BASE_TIME - timedelta(hours=3)),
                    "finished_at": iso(BASE_TIME - timedelta(minutes=8)),
                },
            },
        ],
        "events": [],
    }


def main():
    out_dir = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "fixtures")
    os.makedirs(out_dir, exist_ok=True)
    scenarios = [
        ("scenario_1_healthy.json", scenario_1_healthy()),
        ("scenario_2_pending_capacity.json", scenario_2_pending_capacity()),
        ("scenario_3_gpu_health_warning.json", scenario_3_gpu_health_warning()),
        ("scenario_4_low_utilization.json", scenario_4_persistent_low_utilization()),
        ("scenario_5_correlated_failure.json", scenario_5_correlated_workload_failure()),
        ("scenario_6_stale_telemetry.json", scenario_6_stale_and_unmapped_telemetry()),
    ]
    for fname, data in scenarios:
        path = os.path.join(out_dir, fname)
        with open(path, "w", encoding="utf-8") as f:
            json.dump(data, f, indent=2)
        print(f"Wrote deterministic fixture: {path}")


if __name__ == "__main__":
    main()
