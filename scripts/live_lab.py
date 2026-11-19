#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""
GPU Lens Live Lab — emulated telemetry endpoints for trying "Go Live" without a GPU cluster.

It serves, on this machine only (127.0.0.1 by default):

  * a dcgm-exporter compatible  /metrics  endpoint            (default port 9400)
  * a tiny read-only Kubernetes API: /version, /api/v1/nodes,
    /api/v1/pods, /api/v1/events                              (default port 8001, where `kubectl proxy` listens)

Those are exactly the default addresses GPU Lens probes when you press Go Live, so no configuration is needed:

    python3 scripts/live_lab.py          # terminal 1
    go run ./cmd/gpulens                 # terminal 2  (or: python3 scripts/local_runtime_server.py)
    open http://localhost:8080  ->  Part 2  ->  Go Live

EVERYTHING SERVED HERE IS SYNTHETIC. The emulator marks itself in three ways (a `gpulens_lab_emulator_info`
metric, an `X-GPULens-Emulated` response header and a `-gpulens-lab` Kubernetes version suffix), and GPU Lens
shows an "emulated endpoint" banner whenever it is connected to it. What is real is the pipeline you are
exercising: HTTP collection, parsing, merging, rule evaluation and the incident lifecycle.

The emulated cluster (2 nodes x 4 GPUs) replays a repeating storyline so each rule can be seen firing and
clearing (default cycle: 12 minutes):

    0%   healthy fleet; one idle notebook is holding a GPU at ~3% utilization
    8%   GPU 1 on node-01 heats up: warning at 80 C, critical at 87 C, then cools
   17%   a 4-GPU job goes Pending (no single node has 4 free GPUs)
   42%   Xid 79 on node-02 GPU 2; 15 s later its training pod crashes and restarts
   67%   the exporter on node-02 stalls (samples stop advancing) -> stale-telemetry blind spot
  100%   everything resets and the story repeats

Standard library only. Read-only: every non-GET request is rejected.
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import math
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Optional, Tuple

EMULATOR_VERSION = "v1.30.4-gpulens-lab"
MODEL = "NVIDIA H100 80GB HBM3"
DRIVER = "550.90.07"
TOTAL_MIB = 81559.0
POWER_LIMIT_W = 700.0
NODES = ["lab-gpu-node-01", "lab-gpu-node-02"]
GPUS_PER_NODE = 4

# (namespace, pod, container, owner kind, owner name, node index, gpu indices, profile)
WORKLOADS = [
    ("ml-train", "llama-finetune-0", "trainer", "StatefulSet", "llama-finetune", 0, [0, 1], "train"),
    ("ml-train", "vit-pretrain-7c9d4", "trainer", "Job", "vit-pretrain", 0, [2], "stall"),
    ("research", "notebook-alice-6d5f8b7c9-x2kqp", "jupyter", "ReplicaSet", "notebook-alice-6d5f8b7c9", 0, [3], "idle"),
    ("inference", "triton-llm-5f7b9c6d4-r8wzn", "triton", "ReplicaSet", "triton-llm-5f7b9c6d4", 1, [0, 1], "serve"),
    ("ml-train", "diffusion-train-0", "trainer", "StatefulSet", "diffusion-train", 1, [2], "train"),
]
PENDING_POD = ("ml-train", "mega-train-4gpu-b6k2p", "trainer", "Job", "mega-train-4gpu", 4)
CPU_ONLY_PODS = [
    ("kube-system", "coredns-7db6d8ff4d-9xkzl"),
    ("kube-system", "kube-proxy-lab01"),
    ("gpu-operator", "nvidia-dcgm-exporter-lab01"),
    ("gpu-operator", "nvidia-dcgm-exporter-lab02"),
    ("gpu-operator", "nvidia-device-plugin-daemonset-lab01"),
    ("monitoring", "prometheus-k8s-0"),
]


def iso(ts: float) -> str:
    return dt.datetime.fromtimestamp(ts, dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def jitter(key: str, bucket: int) -> float:
    """Deterministic pseudo-noise in [-1, 1] that changes once per `bucket`."""
    digest = hashlib.sha256(f"{key}:{bucket}".encode()).digest()
    return (int.from_bytes(digest[:4], "big") / 0xFFFFFFFF) * 2.0 - 1.0


def gpu_uuid(node_idx: int, gpu_idx: int) -> str:
    return f"GPU-1ab0{node_idx + 1:04d}-e0e0-4000-8000-0000000000{node_idx + 1}{gpu_idx}"


def clamp(v: float, lo: float, hi: float) -> float:
    return max(lo, min(hi, v))


class Lab:
    """Deterministic world model: state is a pure function of (cycle offset, wall clock)."""

    def __init__(self, cycle_seconds: float, freeze_at: Optional[float]) -> None:
        self.cycle = max(60.0, cycle_seconds)
        self.freeze_at = freeze_at
        self.started = time.time()
        # Workloads look long-running to Kubernetes; GPU Lens still only trusts what it has observed itself.
        self.pod_started = self.started - 3 * 3600.0

    # --- storyline -----------------------------------------------------------------------------------
    def offset(self, now: float) -> float:
        if self.freeze_at is not None:
            return self.freeze_at % self.cycle
        return (now - self.started) % self.cycle

    def cycle_start(self, now: float) -> float:
        return now - self.offset(now)

    def phase(self, now: float) -> Dict[str, Any]:
        t = self.offset(now)
        c = self.cycle
        f = t / c
        thermal = 0.0
        if 0.08 <= f < 0.33:
            # ramp up for the first half of the window, back down for the second
            x = (f - 0.08) / 0.25
            thermal = math.sin(math.pi * clamp(x, 0.0, 1.0))
        xid_at = 0.42 * c
        crash_at = xid_at + 15.0
        stall_at = 0.67 * c
        return {
            "t": t,
            "thermal": thermal,
            "pending": 0.17 <= f < 0.58,
            "pending_since": self.cycle_start(now) + 0.17 * c,
            "xid": 0.42 <= f < 0.67,
            "xid_time": self.cycle_start(now) + xid_at,
            "crashed": t >= crash_at,
            "crash_time": self.cycle_start(now) + crash_at,
            "stalled": f >= 0.67,
            "stall_time": self.cycle_start(now) + stall_at,
        }

    def phase_name(self, now: float) -> str:
        p = self.phase(now)
        names = []
        if p["thermal"] > 0.05:
            names.append("thermal-ramp")
        if p["pending"]:
            names.append("pending-job")
        if p["xid"]:
            names.append("xid-fault")
        if p["stalled"]:
            names.append("exporter-stall")
        return "+".join(names) or "healthy"

    # --- GPU telemetry -------------------------------------------------------------------------------
    def gpu_samples(self, now: float) -> List[Dict[str, Any]]:
        p = self.phase(now)
        out: List[Dict[str, Any]] = []
        for node_idx, node in enumerate(NODES):
            stalled = p["stalled"] and node_idx == 1
            sample_time = p["stall_time"] if stalled else now
            bucket = int(sample_time // 5)
            for gpu_idx in range(GPUS_PER_NODE):
                key = f"{node_idx}-{gpu_idx}"
                wl = next((w for w in WORKLOADS if w[5] == node_idx and gpu_idx in w[6]), None)
                profile = wl[7] if wl else "free"
                n1, n2, n3 = jitter(key + "u", bucket), jitter(key + "t", bucket), jitter(key + "p", bucket)
                wave = math.sin(sample_time / 23.0 + node_idx * 2.1 + gpu_idx * 0.9)
                if profile == "train":
                    util = 91 + 6 * wave + 3 * n1
                    used = 70200 + 900 * n2
                    temp = 69 + 3 * wave + 1.5 * n2
                elif profile == "stall":
                    # periodic data-loader stalls: utilization collapses for part of each minute
                    stall = (sample_time % 60) < 18
                    util = (12 + 6 * n1) if stall else (88 + 6 * n1)
                    used = 48800 + 600 * n2
                    temp = (61 + 2 * n2) if stall else (68 + 2 * n2)
                elif profile == "idle":
                    util = 3 + 2.5 * n1
                    used = 62100 + 150 * n2
                    temp = 41 + 1.5 * n2
                elif profile == "serve":
                    util = 48 + 22 * wave + 6 * n1
                    used = 38400 + 1200 * n2
                    temp = 58 + 4 * wave + 1.5 * n2
                else:
                    util, used, temp = 0.0, 4.0, 33 + 1.0 * n2
                xid = 0
                thermal_violation_ns = 0
                throttle = ""
                if node_idx == 0 and gpu_idx == 1 and p["thermal"] > 0:
                    temp = temp + 21.0 * p["thermal"]
                    if temp >= 87.0:
                        thermal_violation_ns = int(2.0e8 * (temp - 86.0))
                        throttle = "thermal"
                        util *= 0.82
                if node_idx == 1 and gpu_idx == 2 and p["xid"]:
                    xid = 79
                    # after the crash the pod is restarting: the GPU is idle and nearly empty
                    if now >= p["crash_time"]:
                        util, used, temp = 0.0, 310.0, 44 + 1.0 * n2
                util = clamp(util, 0.0, 100.0)
                used = clamp(used, 0.0, TOTAL_MIB)
                power = 78.0 + 5.6 * util + 6.0 * n3
                out.append({
                    "node": node, "node_idx": node_idx, "gpu": gpu_idx, "uuid": gpu_uuid(node_idx, gpu_idx),
                    "bus": f"00000000:{0x17 + gpu_idx * 0x1A:02X}:00.0", "workload": wl, "stalled": stalled,
                    "sample_time": sample_time,
                    "util": round(util, 1), "memcopy": round(clamp(util * 0.42 + 2 * n2, 0, 100), 1),
                    "used": round(used), "free": round(TOTAL_MIB - used), "temp": round(temp, 1),
                    "memtemp": round(temp + 9.0, 1), "power": round(clamp(power, 60.0, POWER_LIMIT_W), 1),
                    "xid": xid, "thermal_violation_ns": thermal_violation_ns, "throttle": throttle,
                })
        return out

    def render_metrics(self, now: float) -> str:
        samples = self.gpu_samples(now)
        lines: List[str] = [
            "# GPU Lens Live Lab emulator: every value below is synthetic.",
            "# HELP gpulens_lab_emulator_info Marks this endpoint as the GPU Lens Live Lab emulator (synthetic data).",
            "# TYPE gpulens_lab_emulator_info gauge",
            f'gpulens_lab_emulator_info{{phase="{self.phase_name(now)}"}} 1',
        ]
        families: List[Tuple[str, str, str, str]] = [
            ("DCGM_FI_DEV_GPU_UTIL", "gauge", "GPU utilization (in %).", "util"),
            ("DCGM_FI_DEV_MEM_COPY_UTIL", "gauge", "Memory utilization (in %).", "memcopy"),
            ("DCGM_FI_DEV_FB_USED", "gauge", "Framebuffer memory used (in MiB).", "used"),
            ("DCGM_FI_DEV_FB_FREE", "gauge", "Framebuffer memory free (in MiB).", "free"),
            ("DCGM_FI_DEV_GPU_TEMP", "gauge", "GPU temperature (in C).", "temp"),
            ("DCGM_FI_DEV_MEMORY_TEMP", "gauge", "Memory temperature (in C).", "memtemp"),
            ("DCGM_FI_DEV_POWER_USAGE", "gauge", "Power draw (in W).", "power"),
            ("DCGM_FI_DEV_XID_ERRORS", "gauge", "Value of the last XID error encountered.", "xid"),
            ("DCGM_FI_DEV_PCIE_REPLAY_COUNTER", "counter", "Total number of PCIe retries.", "_zero"),
            ("DCGM_FI_DEV_ROW_REMAP_FAILURE", "gauge", "Whether remapping of rows has failed.", "_zero"),
            ("DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", "counter", "Number of remapped rows for uncorrectable errors.", "_zero"),
            ("DCGM_FI_DEV_ECC_DBE_VOL_TOTAL", "counter", "Total number of double-bit volatile ECC errors.", "_zero"),
            ("DCGM_FI_DEV_ECC_SBE_VOL_TOTAL", "counter", "Total number of single-bit volatile ECC errors.", "_zero"),
            ("DCGM_FI_DEV_THERMAL_VIOLATION", "counter", "Throttling duration due to thermal constraints (in ns).", "thermal_violation_ns"),
            ("DCGM_FI_DEV_POWER_VIOLATION", "counter", "Throttling duration due to power constraints (in ns).", "_zero"),
            ("DCGM_FI_DEV_POWER_MGMT_LIMIT", "gauge", "Power management limit (in W).", "_limit"),
        ]
        for name, kind, help_text, field in families:
            lines.append(f"# HELP {name} {help_text}")
            lines.append(f"# TYPE {name} {kind}")
            for s in samples:
                value = 0 if field == "_zero" else POWER_LIMIT_W if field == "_limit" else s[field]
                lines.append(self._series(name, s, value))
        throttled = [s for s in samples if s["throttle"]]
        if throttled:
            lines.append("# HELP DCGM_EXP_CLOCK_EVENTS_COUNT Count of clock events within the user-configured time window.")
            lines.append("# TYPE DCGM_EXP_CLOCK_EVENTS_COUNT gauge")
            for s in throttled:
                lines.append(self._series("DCGM_EXP_CLOCK_EVENTS_COUNT", s, 1, extra=f',clock_event="{s["throttle"]}"'))
        return "\n".join(lines) + "\n"

    @staticmethod
    def _series(name: str, s: Dict[str, Any], value: Any, extra: str = "") -> str:
        labels = (
            f'gpu="{s["gpu"]}",UUID="{s["uuid"]}",pci_bus_id="{s["bus"]}",device="nvidia{s["gpu"]}",'
            f'modelName="{MODEL}",Hostname="{s["node"]}",DCGM_FI_DRIVER_VERSION="{DRIVER}"'
        )
        wl = s["workload"]
        if wl:
            labels += f',container="{wl[2]}",namespace="{wl[0]}",pod="{wl[1]}"'
        line = f"{name}{{{labels}{extra}}} {value}"
        if s["stalled"]:
            # A hung exporter keeps serving its last collection: the sample time stops advancing.
            line += f" {int(s['sample_time'] * 1000)}"
        return line

    # --- Kubernetes objects --------------------------------------------------------------------------
    def nodes(self, now: float) -> Dict[str, Any]:
        p = self.phase(now)
        items = []
        for idx, name in enumerate(NODES):
            allocatable = GPUS_PER_NODE
            conditions = [{
                "type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status",
                "lastTransitionTime": iso(self.pod_started - 86400),
            }]
            if idx == 1 and p["xid"]:
                allocatable = GPUS_PER_NODE - 1
                conditions.append({
                    "type": "GpuXidError", "status": "True", "reason": "XidCriticalError",
                    "message": "NVRM: Xid (PCI:0000:4b:00): 79, GPU has fallen off the bus.",
                    "lastTransitionTime": iso(p["xid_time"]),
                })
            items.append({
                "metadata": {
                    "name": name, "uid": f"lab-node-{idx + 1}",
                    "labels": {
                        "kubernetes.io/hostname": name, "nvidia.com/gpu.present": "true",
                        "nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3", "nvidia.com/gpu.count": str(GPUS_PER_NODE),
                        "gpulens.io/live-lab": "emulated",
                    },
                    "creationTimestamp": iso(self.pod_started - 30 * 86400),
                },
                "spec": {},
                "status": {
                    "capacity": {"cpu": "64", "memory": "528280Mi", "nvidia.com/gpu": str(GPUS_PER_NODE)},
                    "allocatable": {"cpu": "63", "memory": "520000Mi", "nvidia.com/gpu": str(allocatable)},
                    "conditions": conditions,
                    "nodeInfo": {"kubeletVersion": "v1.30.4", "osImage": "Ubuntu 22.04.4 LTS"},
                },
            })
        return {"kind": "NodeList", "apiVersion": "v1", "metadata": {"resourceVersion": str(int(now))}, "items": items}

    def pods(self, now: float) -> Dict[str, Any]:
        p = self.phase(now)
        items: List[Dict[str, Any]] = []
        for ns, name, container, owner_kind, owner_name, node_idx, gpu_list, _profile in WORKLOADS:
            restart_count = 0
            container_status: Dict[str, Any] = {
                "name": container, "ready": True, "restartCount": 0,
                "state": {"running": {"startedAt": iso(self.pod_started)}}, "lastState": {},
            }
            if name == "diffusion-train-0" and p["crashed"]:
                # Kubernetes keeps the restart count and last termination after the container comes back.
                restart_count = 1
                state: Dict[str, Any] = {"running": {"startedAt": iso(p["crash_time"] + 40)}}
                if p["xid"]:
                    state = {"waiting": {"reason": "CrashLoopBackOff", "message": "back-off restarting failed container"}}
                container_status = {
                    "name": container, "ready": not p["xid"], "restartCount": restart_count,
                    "state": state,
                    "lastState": {"terminated": {
                        "exitCode": 139, "reason": "Error", "message": "CUDA error: unspecified launch failure",
                        "startedAt": iso(self.pod_started), "finishedAt": iso(p["crash_time"]),
                    }},
                }
            items.append({
                "metadata": {
                    "name": name, "namespace": ns, "uid": f"lab-pod-{name}",
                    "creationTimestamp": iso(self.pod_started - 20),
                    "ownerReferences": [{"apiVersion": "apps/v1", "kind": owner_kind, "name": owner_name}],
                    "labels": {"gpulens.io/live-lab": "emulated"},
                },
                "spec": {
                    "nodeName": NODES[node_idx],
                    "containers": [{"name": container, "resources": {
                        "limits": {"nvidia.com/gpu": str(len(gpu_list))}, "requests": {"nvidia.com/gpu": str(len(gpu_list))}}}],
                },
                "status": {
                    "phase": "Running", "startTime": iso(self.pod_started),
                    "conditions": [{"type": "PodScheduled", "status": "True"}],
                    "containerStatuses": [container_status],
                },
            })
        if p["pending"]:
            ns, name, container, owner_kind, owner_name, gpus = PENDING_POD
            items.append({
                "metadata": {
                    "name": name, "namespace": ns, "uid": f"lab-pod-{name}", "creationTimestamp": iso(p["pending_since"]),
                    "ownerReferences": [{"apiVersion": "batch/v1", "kind": owner_kind, "name": owner_name}],
                    "labels": {"gpulens.io/live-lab": "emulated"},
                },
                "spec": {"containers": [{"name": container, "resources": {
                    "limits": {"nvidia.com/gpu": str(gpus)}, "requests": {"nvidia.com/gpu": str(gpus)}}}]},
                "status": {
                    "phase": "Pending",
                    "conditions": [{
                        "type": "PodScheduled", "status": "False", "reason": "Unschedulable",
                        "message": "0/2 nodes are available: 2 Insufficient nvidia.com/gpu. preemption: 0/2 nodes are available: 2 No preemption victims found for incoming pod.",
                    }],
                },
            })
        for ns, name in CPU_ONLY_PODS:
            items.append({
                "metadata": {"name": name, "namespace": ns, "uid": f"lab-pod-{name}", "creationTimestamp": iso(self.pod_started - 86400)},
                "spec": {"nodeName": NODES[0], "containers": [{"name": "main", "resources": {"requests": {"cpu": "100m"}}}]},
                "status": {"phase": "Running", "startTime": iso(self.pod_started - 86400),
                           "containerStatuses": [{"name": "main", "ready": True, "restartCount": 0, "state": {"running": {}}, "lastState": {}}]},
            })
        return {"kind": "PodList", "apiVersion": "v1", "metadata": {"resourceVersion": str(int(now))}, "items": items}

    def events(self, now: float, only_warning: bool) -> Dict[str, Any]:
        p = self.phase(now)
        items: List[Dict[str, Any]] = []

        def event(uid: str, etype: str, reason: str, message: str, kind: str, ns: str, name: str, component: str,
                  host: str, first: float, last: float, count: int) -> None:
            items.append({
                "metadata": {"name": f"{name}.{uid}", "namespace": ns or "default", "uid": f"lab-event-{uid}", "creationTimestamp": iso(first)},
                "type": etype, "reason": reason, "message": message, "count": count,
                "firstTimestamp": iso(first), "lastTimestamp": iso(last),
                "source": {"component": component, "host": host},
                "involvedObject": {"kind": kind, "namespace": ns, "name": name},
            })

        if p["pending"]:
            ns, name = PENDING_POD[0], PENDING_POD[1]
            waited = max(1, int((now - p["pending_since"]) // 30) + 1)
            event(f"sched-{int(p['pending_since'])}", "Warning", "FailedScheduling",
                  "0/2 nodes are available: 2 Insufficient nvidia.com/gpu. preemption: 0/2 nodes are available: 2 No preemption victims found for incoming pod.",
                  "Pod", ns, name, "default-scheduler", "", p["pending_since"], now, waited)
        if p["xid"]:
            event(f"xid-{int(p['xid_time'])}", "Warning", "XidCriticalError",
                  "NVRM: Xid (PCI:0000:4b:00): 79, pid='<unknown>', name=<unknown>, GPU has fallen off the bus.",
                  "Node", "", NODES[1], "node-problem-detector", NODES[1], p["xid_time"], p["xid_time"], 1)
            if p["crashed"]:
                backoffs = max(1, int((now - p["crash_time"]) // 20) + 1)
                event(f"backoff-{int(p['crash_time'])}", "Warning", "BackOff",
                      "Back-off restarting failed container trainer in pod diffusion-train-0_ml-train",
                      "Pod", "ml-train", "diffusion-train-0", "kubelet", NODES[1], p["crash_time"], now, backoffs)
        if not only_warning:
            event("pulled-llama", "Normal", "Pulled", "Container image already present on machine",
                  "Pod", "ml-train", "llama-finetune-0", "kubelet", NODES[0], self.pod_started, self.pod_started, 1)
        return {"kind": "EventList", "apiVersion": "v1", "metadata": {"resourceVersion": str(int(now))}, "items": items}


class _Handler(BaseHTTPRequestHandler):
    server_version = "GPULensLiveLab/1.0"
    lab: Lab
    role = ""
    quiet = True

    def log_message(self, fmt: str, *args: Any) -> None:  # noqa: A003
        if not self.quiet:
            sys.stderr.write("[%s] %s\n" % (self.role, fmt % args))

    def _send(self, status: int, body: bytes, content_type: str) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("X-GPULens-Emulated", "live-lab")
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def _json(self, status: int, payload: Any) -> None:
        self._send(status, json.dumps(payload).encode("utf-8"), "application/json")

    def _reject(self) -> None:
        self._json(405, {"kind": "Status", "status": "Failure", "reason": "MethodNotAllowed", "code": 405,
                         "message": "the GPU Lens Live Lab emulator is read-only"})

    do_POST = do_PUT = do_PATCH = do_DELETE = _reject  # type: ignore[assignment]

    def do_HEAD(self) -> None:  # noqa: N802
        self.do_GET()


class DCGMHandler(_Handler):
    role = "dcgm"

    def do_GET(self) -> None:  # noqa: N802
        path = urllib.parse.urlparse(self.path).path
        if path == "/metrics":
            self._send(200, self.lab.render_metrics(time.time()).encode("utf-8"), "text/plain; version=0.0.4; charset=utf-8")
        elif path in ("/health", "/healthz"):
            self._send(200, b"OK", "text/plain; charset=utf-8")
        else:
            self._send(404, b"not found\n", "text/plain; charset=utf-8")


class K8sHandler(_Handler):
    role = "k8s"

    def do_GET(self) -> None:  # noqa: N802
        parsed = urllib.parse.urlparse(self.path)
        query = urllib.parse.parse_qs(parsed.query)
        now = time.time()
        if parsed.path == "/version":
            self._json(200, {"major": "1", "minor": "30", "gitVersion": EMULATOR_VERSION, "platform": "linux/amd64"})
        elif parsed.path == "/api/v1/nodes":
            self._json(200, self.lab.nodes(now))
        elif parsed.path == "/api/v1/pods":
            self._json(200, self.lab.pods(now))
        elif parsed.path == "/api/v1/events":
            selector = ",".join(query.get("fieldSelector", []))
            self._json(200, self.lab.events(now, only_warning="type=Warning" in selector))
        elif parsed.path in ("/healthz", "/readyz", "/livez"):
            self._send(200, b"ok", "text/plain; charset=utf-8")
        else:
            self._json(404, {"kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404,
                             "message": f"the Live Lab emulator does not serve {parsed.path}"})


def _serve(handler_cls: type, lab: Lab, host: str, port: int, quiet: bool) -> ThreadingHTTPServer:
    bound = type(handler_cls.__name__ + "Bound", (handler_cls,), {"lab": lab, "quiet": quiet})
    srv = ThreadingHTTPServer((host, port), bound)
    srv.daemon_threads = True
    threading.Thread(target=srv.serve_forever, name=f"lab-{handler_cls.role}", daemon=True).start()
    return srv


def main(argv: Optional[List[str]] = None) -> int:
    ap = argparse.ArgumentParser(description="GPU Lens Live Lab: emulated dcgm-exporter and Kubernetes API endpoints (synthetic data).")
    ap.add_argument("--host", default="127.0.0.1", help="Bind address (default 127.0.0.1: reachable from this machine only)")
    ap.add_argument("--dcgm-port", type=int, default=9400, help="Port of the emulated dcgm-exporter (0 disables it)")
    ap.add_argument("--k8s-port", type=int, default=8001, help="Port of the emulated Kubernetes API (0 disables it)")
    ap.add_argument("--cycle-seconds", type=float, default=720.0, help="Length of one storyline cycle (default 720)")
    ap.add_argument("--freeze-at", type=float, default=None, help="Freeze the storyline at this offset in seconds (for tests and demos)")
    ap.add_argument("--verbose", action="store_true", help="Log every request")
    args = ap.parse_args(argv)

    lab = Lab(args.cycle_seconds, args.freeze_at)
    servers = []
    try:
        if args.dcgm_port:
            servers.append(_serve(DCGMHandler, lab, args.host, args.dcgm_port, not args.verbose))
        if args.k8s_port:
            servers.append(_serve(K8sHandler, lab, args.host, args.k8s_port, not args.verbose))
    except OSError as exc:
        print(f"Could not start the Live Lab: {exc}. Is another exporter, `kubectl proxy` or lab already using that port?", file=sys.stderr)
        return 1

    print("GPU Lens Live Lab — EMULATED endpoints (all values are synthetic)")
    if args.dcgm_port:
        print(f"  dcgm-exporter   http://{args.host}:{args.dcgm_port}/metrics")
    if args.k8s_port:
        print(f"  Kubernetes API  http://{args.host}:{args.k8s_port}   (read-only: nodes, pods, events)")
    print(f"  storyline cycle {lab.cycle:.0f}s" + (f", frozen at {args.freeze_at:.0f}s" if args.freeze_at is not None else ""))
    print("Now open GPU Lens and press Go Live — these are the addresses it probes by default. Ctrl+C to stop.")
    sys.stdout.flush()
    try:
        last = ""
        while True:
            time.sleep(1.0)
            name = lab.phase_name(time.time())
            if name != last and args.freeze_at is None:
                print(f"  [{dt.datetime.now().strftime('%H:%M:%S')}] storyline: {name}")
                sys.stdout.flush()
                last = name
    except KeyboardInterrupt:
        print("\nLive Lab stopped.")
    finally:
        for srv in servers:
            srv.shutdown()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
