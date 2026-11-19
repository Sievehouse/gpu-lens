#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""
GPU Lens live telemetry engine for the Python runtime ("Go Live", Python edition).

This is the standard-library mirror of the Go engine in internal/live: same /api/v1/live/* API, same JSON shapes,
same read-only guarantees, so the web console works identically against either runtime. Differences are listed in
docs/ARCHITECTURE.md ("Python runtime parity"): Prometheus is read through one instant query (no history backfill,
no scrape-target audit) and the activity log is a little less detailed.

Every external call is read-only: HTTP GET/HEAD only (mutating verbs are refused before any socket is opened) and a
fixed allow-list of nvidia-smi query flags, executed without a shell.
"""
import copy
import json
import math
import os
import re
import shutil
import socket
import ssl
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

VERSION = "0.3.0-dev"
RUNTIME = "python"

SOURCE_HOST, SOURCE_SMI, SOURCE_DCGM, SOURCE_PROM, SOURCE_K8S = "host", "nvidia_smi", "dcgm_exporter", "prometheus", "kubernetes"
SOURCE_AMD = "amd_smi"
LOCAL_DRIVER_SOURCES = (SOURCE_SMI, SOURCE_AMD)
HOST_PROCESS_NS = "host-processes"
LAB_MARKER = "gpulens_lab_emulator_info"
GPU_RESOURCE = "nvidia.com/gpu"
LOW_UTIL_RULE = "GPU-UTIL-003"
NOT_FOUND_RETRY = 30.0
DEVICE_FORGET_AFTER = 30 * 60.0
MAX_ACTIVITY = 400
MAX_RESOLVED = 100
LATENCY_HISTORY = 40

DEFAULT_ENDPOINTS = {
    SOURCE_DCGM: "http://127.0.0.1:9400/metrics",
    SOURCE_PROM: "http://127.0.0.1:9090",
    SOURCE_K8S: "http://127.0.0.1:8001",
}

INTEGER_THRESHOLDS = {
    "pending_grace_seconds", "xid_error_threshold", "ecc_dbe_threshold", "row_remap_failure_threshold",
    "pcie_replay_warning_count", "nvlink_crc_error_threshold", "min_utilization_samples",
}


class LiveError(Exception):
    """A request the live API refuses; `status` is the HTTP status to answer with."""

    def __init__(self, message, status=400):
        super().__init__(message)
        self.status = status


# ---------------------------------------------------------------------------------------------
# small helpers
# ---------------------------------------------------------------------------------------------
def utcnow():
    return datetime.now(timezone.utc)


def iso(dt):
    if dt is None:
        return None
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z" if dt.microsecond else dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_iso(s):
    if not s:
        return None
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    s = re.sub(r"(\.\d{6})\d+", r"\1", s)
    try:
        return datetime.fromisoformat(s)
    except ValueError:
        return None


def round1(v):
    return round(v * 10.0) / 10.0


def plural(n, one, many):
    return f"{n} {one if n == 1 else many}"


def or_dash(s):
    return s if (s or "").strip() else "—"


def sanitize_error(err):
    msg = " ".join(str(err).split())
    return msg[:300] + "…" if len(msg) > 300 else msg


def short_uuid(u):
    return u[:16] + "…" if len(u) > 16 else u


def first_label(labels, candidates):
    for k in candidates:
        v = labels.get(k)
        if v:
            return v
    return ""


def validate_endpoint(name, raw):
    try:
        u = urllib.parse.urlparse((raw or "").strip())
    except ValueError:
        raise LiveError(f"{name} endpoint is not a valid URL")
    if u.scheme not in ("http", "https"):
        raise LiveError(f"{name} endpoint must start with http:// or https://")
    if not u.hostname:
        raise LiveError(f"{name} endpoint is missing a host")
    if u.username or u.password:
        raise LiveError(f"{name} endpoint must not contain credentials; use the token setting instead")
    if u.fragment:
        raise LiveError(f"{name} endpoint must not contain a #fragment")


def validate_thresholds(t):
    def bad(msg):
        raise LiveError(msg)

    if not 0 <= t["pending_grace_seconds"] <= 86400:
        bad("pending_grace_seconds must be between 0 and 86400")
    if not 0 < t["critical_pending_minutes"] <= 10080:
        bad("critical_pending_minutes must be greater than 0 (max 10080)")
    for k in ("xid_error_threshold", "ecc_dbe_threshold", "row_remap_failure_threshold", "pcie_replay_warning_count", "nvlink_crc_error_threshold"):
        if not 1 <= t[k] <= 1_000_000_000:
            bad(f"{k} must be at least 1")
    if not 0 < t["low_util_threshold_percent"] < 100:
        bad(f"low_util_threshold_percent must be in (0, 100), got {t['low_util_threshold_percent']:.2f}")
    if not 0 < t["low_util_window_minutes"] <= 10080:
        bad("low_util_window_minutes must be greater than 0 (max 10080)")
    if not 1 <= t["min_utilization_samples"] <= 100000:
        bad("min_utilization_samples must be at least 1")
    if not 0 < t["high_vram_context_percent"] <= 100:
        bad("high_vram_context_percent must be in (0, 100]")
    if not (0 <= t["correlation_lead_minutes"] <= 1440 and 0 <= t["correlation_lag_minutes"] <= 1440):
        bad("correlation_lead_minutes and correlation_lag_minutes must be between 0 and 1440")
    if not 0 < t["max_staleness_seconds"] <= 86400:
        bad("max_staleness_seconds must be greater than 0 (max 86400)")
    if t["temp_warning_celsius"] <= 0 or t["temp_warning_celsius"] > 150 or t["temp_critical_celsius"] > 150:
        bad("temperature thresholds must be between 0 and 150 °C")
    if t["temp_critical_celsius"] <= t["temp_warning_celsius"]:
        bad("temp_critical_celsius must be greater than temp_warning_celsius")
    if not 0 < t["mem_temp_warning_celsius"] <= 150:
        bad("mem_temp_warning_celsius must be between 0 and 150 °C")
    if not 0 < t["fb_memory_warning_percent"] <= 100:
        bad("fb_memory_warning_percent must be in (0, 100]")
    if not 0 < t["power_cap_warning_percent"] <= 200:
        bad("power_cap_warning_percent must be in (0, 200]")
    rate = t.get("optional_sample_gpu_hourly_rate_usd")
    if rate is not None and not 0 <= rate <= 10000:
        bad("optional_sample_gpu_hourly_rate_usd must be between 0 and 10000")


# ---------------------------------------------------------------------------------------------
# read-only HTTP
# ---------------------------------------------------------------------------------------------
class ReadOnlyHTTP:
    """HTTP client that can only GET/HEAD. Other verbs raise before any socket is opened."""

    ALLOWED = ("GET", "HEAD")

    def __init__(self, timeout=10.0, max_bytes=16 * 1024 * 1024, ca_file=""):
        self.timeout = timeout
        self.max_bytes = max_bytes
        self.allowed = 0
        self.blocked = 0
        self._lock = threading.Lock()
        self._ctx = None
        if ca_file:
            self._ctx = ssl.create_default_context(cafile=ca_file)

    def request(self, method, url, token="", headers=None):
        method = method.upper()
        if method not in self.ALLOWED:
            with self._lock:
                self.blocked += 1
            raise PermissionError(f"read-only transport refused {method} {url}")
        with self._lock:
            self.allowed += 1
        req = urllib.request.Request(url, method=method)
        req.add_header("Accept", "application/json, text/plain")
        req.add_header("User-Agent", f"gpu-lens/{VERSION}")
        if token:
            req.add_header("Authorization", "Bearer " + token)
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        try:
            resp = urllib.request.urlopen(req, timeout=self.timeout, context=self._ctx)
        except urllib.error.HTTPError as e:
            e.read(0)
            raise HTTPStatus(e.code, f"{method} {url} returned HTTP {e.code}")
        with resp:
            raw = resp.read(self.max_bytes + 1)
            hdrs = {k.lower(): v for k, v in resp.headers.items()}
            return resp.status, hdrs, raw[: self.max_bytes]

    def get_json(self, url, token=""):
        status, _, raw = self.request("GET", url, token)
        return json.loads(raw.decode("utf-8", "replace"))


class HTTPStatus(Exception):
    def __init__(self, status, message):
        super().__init__(message)
        self.status = status


def selftest_readonly_guard():
    """Proves the transport refuses mutating verbs without touching the network."""
    g = ReadOnlyHTTP()
    leaked = []
    for m in ("POST", "PUT", "PATCH", "DELETE"):
        try:
            g.request(m, "http://127.0.0.1:9/never-called")
            leaked.append(m)
        except PermissionError:
            pass
        except Exception:
            leaked.append(m)
    return {"ok": not leaked, "blocked_methods": ["POST", "PUT", "PATCH", "DELETE"], "leaked_methods": leaked, "network_calls": g.allowed}


def read_token_file(path):
    try:
        with open(path, "r", encoding="utf-8") as f:
            tok = f.read().strip()
    except OSError as e:
        raise RuntimeError(f"read bearer token file {path!r}: {e.strerror or e}")
    if not tok:
        raise RuntimeError(f"bearer token file {path!r} is empty")
    return tok


# ---------------------------------------------------------------------------------------------
# host vitals (Linux /proc, /sys)
# ---------------------------------------------------------------------------------------------
def _read(path, limit=1 << 20):
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            return f.read(limit)
    except OSError:
        return None


class HostSource:
    def __init__(self):
        self.started = time.time()
        self._prev = None
        self._static = None

    def _static_facts(self):
        if self._static is None:
            platform_name = ""
            osr = _read("/etc/os-release") or ""
            m = re.search(r'^PRETTY_NAME="?([^"\n]+)"?', osr, re.M)
            if m:
                platform_name = m.group(1)
            cpu = ""
            ci = _read("/proc/cpuinfo") or ""
            m = re.search(r"^model name\s*:\s*(.+)$", ci, re.M)
            if m:
                cpu = m.group(1).strip()
            in_container = os.path.exists("/.dockerenv") or "kubepods" in (_read("/proc/1/cgroup") or "")
            self._static = {
                "platform": platform_name,
                "kernel": (_read("/proc/sys/kernel/osrelease") or "").strip(),
                "cpu_model": cpu,
                "in_container": in_container,
            }
        return self._static

    def collect(self):
        st = self._static_facts()
        import platform as plat

        hv = {
            "supported": os.path.exists("/proc/stat"),
            "collected_at": iso(utcnow()),
            "hostname": socket.gethostname(),
            "os": plat.system().lower(),
            "arch": {"x86_64": "amd64", "aarch64": "arm64"}.get(plat.machine(), plat.machine()),
            "platform": st["platform"], "kernel": st["kernel"], "cpu_model": st["cpu_model"],
            "logical_cpus": os.cpu_count() or 0, "in_container": st["in_container"],
            "uptime_seconds": None, "cpu_percent": None, "cpu_iowait_percent": None,
            "load1": None, "load5": None, "load15": None,
            "mem_total_mib": None, "mem_available_mib": None, "mem_used_percent": None,
            "swap_total_mib": None, "swap_used_mib": None,
            "disk_path": "/", "disk_total_gib": None, "disk_used_percent": None,
            "net_rx_bytes_per_sec": None, "net_tx_bytes_per_sec": None,
            "pressure": None,
            "process": {"pid": os.getpid(), "uptime_seconds": round(time.time() - self.started, 3), "memory_mib": 0.0,
                        "workers": threading.active_count(), "runtime": "python " + plat.python_version()},
            "pci_gpus": [], "pci_scan_supported": False, "nvidia_driver_version": "", "notes": [],
        }
        if not hv["supported"]:
            return hv
        up = _read("/proc/uptime")
        if up:
            hv["uptime_seconds"] = float(up.split()[0])
        la = _read("/proc/loadavg")
        if la:
            p = la.split()
            hv["load1"], hv["load5"], hv["load15"] = float(p[0]), float(p[1]), float(p[2])
        mi = {}
        for line in (_read("/proc/meminfo") or "").splitlines():
            k, _, rest = line.partition(":")
            try:
                mi[k] = float(rest.split()[0]) / 1024.0
            except (IndexError, ValueError):
                pass
        if "MemTotal" in mi and "MemAvailable" in mi and mi["MemTotal"] > 0:
            hv["mem_total_mib"], hv["mem_available_mib"] = mi["MemTotal"], mi["MemAvailable"]
            hv["mem_used_percent"] = (1 - mi["MemAvailable"] / mi["MemTotal"]) * 100.0
        if "SwapTotal" in mi:
            hv["swap_total_mib"] = mi["SwapTotal"]
            hv["swap_used_mib"] = mi["SwapTotal"] - mi.get("SwapFree", 0.0)
        try:
            du = shutil.disk_usage("/")
            hv["disk_total_gib"] = du.total / 2 ** 30
            hv["disk_used_percent"] = du.used / du.total * 100.0 if du.total else None
        except OSError:
            pass
        statm = _read("/proc/self/statm")
        if statm:
            try:
                hv["process"]["memory_mib"] = int(statm.split()[1]) * os.sysconf("SC_PAGE_SIZE") / 2 ** 20
            except (IndexError, ValueError, OSError):
                pass
        now = time.time()
        cur = {"at": now, "busy": 0, "io": 0, "all": 0, "rx": None, "tx": None}
        for line in (_read("/proc/stat") or "").splitlines():
            if line.startswith("cpu "):
                f = [int(x) for x in line.split()[1:9]]
                idle = f[3] + f[4]
                cur["all"] = sum(f)
                cur["busy"] = cur["all"] - idle
                cur["io"] = f[4]
                break
        rx = tx = 0
        net_ok = False
        for line in (_read("/proc/net/dev") or "").splitlines()[2:]:
            name, _, rest = line.partition(":")
            if name.strip() == "lo" or not rest:
                continue
            f = rest.split()
            rx += int(f[0])
            tx += int(f[8])
            net_ok = True
        if net_ok:
            cur["rx"], cur["tx"] = rx, tx
        if self._prev:
            dall = cur["all"] - self._prev["all"]
            if dall > 0:
                hv["cpu_percent"] = (cur["busy"] - self._prev["busy"]) / dall * 100.0
                hv["cpu_iowait_percent"] = (cur["io"] - self._prev["io"]) / dall * 100.0
            dt = cur["at"] - self._prev["at"]
            if dt > 0 and cur["rx"] is not None and self._prev["rx"] is not None:
                hv["net_rx_bytes_per_sec"] = max(0.0, (cur["rx"] - self._prev["rx"]) / dt)
                hv["net_tx_bytes_per_sec"] = max(0.0, (cur["tx"] - self._prev["tx"]) / dt)
        self._prev = cur
        pressure = {}
        for res, key in (("cpu", "cpu_some_avg10"), ("memory", "memory_some_avg10"), ("io", "io_some_avg10")):
            txt = _read("/proc/pressure/" + res)
            m = re.search(r"^some .*avg10=([0-9.]+)", txt or "", re.M)
            pressure[key] = float(m.group(1)) if m else None
        if any(v is not None for v in pressure.values()):
            hv["pressure"] = pressure
        # PCI display-class devices
        pci_root = "/sys/bus/pci/devices"
        if os.path.isdir(pci_root):
            hv["pci_scan_supported"] = True
            vendors = {"10de": "NVIDIA", "1002": "AMD", "8086": "Intel"}
            for addr in sorted(os.listdir(pci_root)):
                cls = (_read(f"{pci_root}/{addr}/class") or "").strip()
                if not cls.startswith("0x03"):
                    continue
                vid = (_read(f"{pci_root}/{addr}/vendor") or "").strip()[2:]
                did = (_read(f"{pci_root}/{addr}/device") or "").strip()[2:]
                drv = ""
                try:
                    drv = os.path.basename(os.readlink(f"{pci_root}/{addr}/driver"))
                except OSError:
                    pass
                hv["pci_gpus"].append({"address": addr, "vendor": vendors.get(vid, vid), "vendor_id": vid, "device_id": did,
                                       "class": "VGA/3D controller" if cls.startswith("0x0302") else "Display controller", "driver": drv})
        ver = _read("/proc/driver/nvidia/version")
        m = re.search(r"Kernel Module\s+([0-9.]+)", ver or "")
        if m:
            hv["nvidia_driver_version"] = m.group(1)
        return hv


# ---------------------------------------------------------------------------------------------
# nvidia-smi (fixed, read-only query flags; no shell)
# ---------------------------------------------------------------------------------------------
SMI_WELL_KNOWN = ["/usr/bin/nvidia-smi", "/usr/local/bin/nvidia-smi", "/usr/local/nvidia/bin/nvidia-smi", "/opt/nvidia/bin/nvidia-smi",
                  "/run/current-system/sw/bin/nvidia-smi", "C:\\Windows\\System32\\nvidia-smi.exe",
                  "C:\\Program Files\\NVIDIA Corporation\\NVSMI\\nvidia-smi.exe"]
SMI_WANT = ["index", "uuid", "pci.bus_id", "driver_version", "temperature.gpu", "temperature.memory", "utilization.gpu",
            "utilization.memory", "memory.total", "memory.used", "memory.free", "memory.reserved", "power.draw", "power.limit",
            "fan.speed", "pstate", "ecc.errors.uncorrected.volatile.total", "ecc.errors.corrected.volatile.total",
            "pcie.link.gen.current", "pcie.link.gen.max", "pcie.link.width.current", "pcie.link.width.max",
            "clocks_throttle_reasons.hw_slowdown", "clocks_throttle_reasons.hw_thermal_slowdown",
            "clocks_throttle_reasons.hw_power_brake_slowdown", "clocks_throttle_reasons.sw_thermal_slowdown",
            "clocks_throttle_reasons.sw_power_cap"]
SMI_BASE = ["index", "uuid", "pci.bus_id", "driver_version", "temperature.gpu", "utilization.gpu", "utilization.memory",
            "memory.total", "memory.used", "memory.free", "power.draw", "power.limit", "fan.speed"]


class SMINotFound(Exception):
    pass


def _num(s):
    s = (s or "").strip()
    if not s or s.startswith("[") or s.lower() in ("n/a", "not supported", "[n/a]"):
        return None
    try:
        return float(s)
    except ValueError:
        return None


class NvidiaSMISource:
    def __init__(self, path="", timeout=8.0):
        self.path = path
        self.timeout = timeout
        self._fields = None

    def _find(self):
        if self.path:
            if os.path.isfile(self.path) and os.access(self.path, os.X_OK):
                return self.path
            raise SMINotFound(f"nvidia-smi was not found at the configured path {self.path}")
        found = shutil.which("nvidia-smi")
        if found:
            return found
        for p in SMI_WELL_KNOWN:
            if os.path.isfile(p) and os.access(p, os.X_OK):
                return p
        raise SMINotFound("nvidia-smi was not found on PATH or in well-known locations")

    def _exec(self, binary, *args):
        # args are drawn from the fixed allow-list below; the binary is run directly (no shell).
        allowed = args == ("--help-query-gpu",) or (len(args) == 2 and args[1] == "--format=csv,noheader,nounits" and
                                                   args[0].startswith(("--query-gpu=", "--query-compute-apps=")))
        if not allowed:
            raise PermissionError("nvidia-smi invocation is not on the read-only allow-list")
        try:
            p = subprocess.run([binary, *args], capture_output=True, text=True, timeout=self.timeout, stdin=subprocess.DEVNULL, shell=False)
        except subprocess.TimeoutExpired:
            raise RuntimeError("nvidia-smi timed out")
        if p.returncode != 0:
            raise RuntimeError((p.stdout + p.stderr).strip().splitlines()[0] if (p.stdout + p.stderr).strip() else f"nvidia-smi exited {p.returncode}")
        return p.stdout

    def _query_fields(self, binary):
        if self._fields:
            return self._fields
        valid = None
        try:
            help_txt = self._exec(binary, "--help-query-gpu")
            valid = set(re.findall(r'^"([a-z0-9_.]+)"', help_txt, re.M))
            for m in re.finditer(r'^"([a-z0-9_.]+)" or "([a-z0-9_.]+)"', help_txt, re.M):
                valid.update(m.groups())
        except Exception:
            valid = None
        if valid:
            fields = [f for f in SMI_WANT if f in valid]
            for alt in ("clocks_event_reasons.sw_thermal_slowdown",):
                if alt in valid and "clocks_throttle_reasons.sw_thermal_slowdown" not in fields:
                    fields.append(alt)
            note = f"{len(fields)} of {len(SMI_WANT)} optional fields supported by this driver"
        else:
            fields = list(SMI_BASE)
            note = "basic field set (this nvidia-smi does not list its query fields)"
        for f in SMI_BASE:
            if f not in fields:
                fields.insert(0, f)
        self._fields = (fields, note)
        return self._fields

    def collect(self, hostname):
        binary = self._find()
        fields, note = self._query_fields(binary)
        qfields = fields + ["name"]  # the model name may contain commas, so it goes last
        out = self._exec(binary, "--query-gpu=" + ",".join(qfields), "--format=csv,noheader,nounits")
        gpus, extras, procs_by_uuid = [], {}, {}
        driver = ""
        now = iso(utcnow())
        exported = {}
        for line in out.strip().splitlines():
            parts = [p.strip() for p in line.split(",", len(qfields) - 1)]
            if len(parts) < len(qfields):
                continue
            row = dict(zip(qfields, parts))
            uuid = row.get("uuid", "")
            if not uuid:
                continue
            driver = driver or row.get("driver_version", "")
            total = _num(row.get("memory.total")) or 0.0
            used, free = _num(row.get("memory.used")), _num(row.get("memory.free"))
            if free is None and used is not None:
                free = max(0.0, total - used)
            latest = {
                "timestamp": now,
                "gpu_util_percent": _num(row.get("utilization.gpu")) or 0.0,
                "mem_copy_util_percent": _num(row.get("utilization.memory")) or 0.0,
                "fb_used_mib": used or 0.0, "fb_free_mib": free or 0.0,
                "gpu_temp_celsius": _num(row.get("temperature.gpu")) or 0.0,
                "memory_temp_celsius": _num(row.get("temperature.memory")) or 0.0,
                "power_usage_watts": _num(row.get("power.draw")) or 0.0,
                "xid_error_code": 0, "pcie_replay_counter": 0, "row_remap_failure": 0, "uncorrectable_remapped_rows": 0,
            }
            ecc = _num(row.get("ecc.errors.uncorrected.volatile.total"))
            if ecc is not None:
                latest["ecc_dbe_vol_total"] = int(ecc)
            sbe = _num(row.get("ecc.errors.corrected.volatile.total"))
            if sbe is not None:
                latest["ecc_sbe_vol_total"] = int(sbe)
            throttles = []
            for f in qfields:
                if f.startswith(("clocks_throttle_reasons.", "clocks_event_reasons.")) and row.get(f, "").lower() == "active":
                    throttles.append(f.split(".", 1)[1])
            if throttles:
                latest["clock_throttle_reason"] = throttles[0]
            pcie = None
            if _num(row.get("pcie.link.gen.current")) is not None:
                pcie = {"gen_current": int(_num(row["pcie.link.gen.current"])), "gen_max": int(_num(row.get("pcie.link.gen.max")) or 0),
                        "width_current": int(_num(row.get("pcie.link.width.current")) or 0), "width_max": int(_num(row.get("pcie.link.width.max")) or 0)}
            fan = _num(row.get("fan.speed"))
            dev = {"uuid": uuid, "device_index": int(_num(row.get("index")) or 0), "node_name": hostname, "model_name": row.get("name", ""),
                   "pci_bus_id": row.get("pci.bus_id", ""), "driver_version": row.get("driver_version", ""),
                   "power_limit_watts": _num(row.get("power.limit")) or 0.0, "total_memory_mib": total,
                   "allocated_namespace": "", "allocated_pod": "", "allocated_container": "", "mapping_status": "unallocated",
                   "last_sample_at": now, "latest": latest}
            gpus.append(dev)
            extras[uuid] = {"sources": [SOURCE_SMI], "fan_speed_percent": fan, "pstate": row.get("pstate", "") if row.get("pstate", "").startswith("P") else "",
                            "throttle_reasons": throttles, "pcie": pcie, "processes": [], "emulated": False, "mig_profile": ""}
        for f in qfields:
            exported[f] = True
        notes = []
        try:
            apps = self._exec(binary, "--query-compute-apps=gpu_uuid,pid,used_memory,process_name", "--format=csv,noheader,nounits")
            for line in apps.strip().splitlines():
                p = [x.strip() for x in line.split(",", 3)]
                if len(p) < 4 or not p[0].startswith("GPU-"):
                    continue
                procs_by_uuid.setdefault(p[0], []).append({"pid": int(p[1]) if p[1].isdigit() else 0, "name": p[3], "used_memory_mib": _num(p[2])})
        except Exception as e:
            notes.append("compute-process query failed: " + sanitize_error(e))
        for uuid, procs in procs_by_uuid.items():
            if uuid in extras:
                extras[uuid]["processes"] = procs
        m = {"DCGM_FI_DEV_GPU_UTIL": "utilization.gpu", "DCGM_FI_DEV_MEM_COPY_UTIL": "utilization.memory", "DCGM_FI_DEV_FB_USED": "memory.used",
             "DCGM_FI_DEV_FB_FREE": "memory.free", "DCGM_FI_DEV_GPU_TEMP": "temperature.gpu", "DCGM_FI_DEV_MEMORY_TEMP": "temperature.memory",
             "DCGM_FI_DEV_POWER_USAGE": "power.draw"}
        exported_dcgm = {k: v in qfields for k, v in m.items()}
        # Fields nvidia-smi cannot supply stay un-exported, so the rules treat them as "not exported", never as zero.
        if gpus and all(g["latest"]["memory_temp_celsius"] == 0.0 for g in gpus):
            exported_dcgm["DCGM_FI_DEV_MEMORY_TEMP"] = False
        return {"binary": binary, "driver": driver, "gpus": gpus, "extras": extras, "processes": procs_by_uuid, "exported": exported_dcgm,
                "fields_note": note, "no_devices": not gpus, "notes": notes}


class AMDNotFound(SMINotFound):
    pass


AMD_ALLOWED = (("list", "--json"), ("static", "--json"), ("metric", "--json"), ("process", "--json"))
AMD_WELL_KNOWN = ("/opt/rocm/bin/amd-smi", "/usr/bin/amd-smi", "/usr/local/bin/amd-smi")


def _amd_records(raw):
    doc = json.loads(raw)
    if isinstance(doc, list):
        return doc
    if isinstance(doc, dict):
        for key in ("gpu_data", "gpus", "data"):
            if isinstance(doc.get(key), list):
                return doc[key]
    raise ValueError("amd-smi JSON has no per-GPU list")


def _amd_num(v):
    if isinstance(v, bool):
        return None
    if isinstance(v, (int, float)):
        return float(v)
    if isinstance(v, str):
        return _num(v.split(" ")[0])
    if isinstance(v, dict):
        return _amd_num(v.get("value"))
    return None


def _amd_unit(v):
    if isinstance(v, dict) and isinstance(v.get("unit"), str):
        return v["unit"].strip().upper()
    if isinstance(v, str) and " " in v.strip():
        return v.strip().rsplit(" ", 1)[1].upper()
    return ""


def _amd_mib(v, default_unit):
    n = _amd_num(v)
    if n is None:
        return None
    unit = _amd_unit(v) or default_unit
    return {"B": n / 1048576.0, "KB": n / 1024.0, "KIB": n / 1024.0, "GB": n * 1024.0, "GIB": n * 1024.0}.get(unit, n)


def _amd_str(v):
    if isinstance(v, dict):
        return _amd_str(v.get("value"))
    if isinstance(v, (int, float)) and not isinstance(v, bool):
        return f"{v:g}"
    if isinstance(v, str):
        return "" if v.strip().upper() == "N/A" else v.strip()
    return ""


def _amd_index(rec):
    for k in ("gpu", "gpu_id", "index"):
        n = _amd_num(rec.get(k))
        if n is not None:
            return int(n)
    return None


def _pcie_gen(gts):
    for floor, gen in ((60, 6), (30, 5), (15, 4), (7.5, 3), (4.5, 2)):
        if gts >= floor:
            return gen
    return 1 if gts > 0 else 0


class AMDSMISource:
    """Mirror of internal/source/amd_smi_source.go (experimental): four fixed read-only amd-smi JSON queries."""

    def __init__(self, path="", timeout=8.0):
        self.path = path
        self.timeout = timeout
        self._static = None
        self._static_at = 0.0

    def _find(self):
        if self.path:
            if os.path.isfile(self.path) and os.access(self.path, os.X_OK):
                return self.path
            raise AMDNotFound(f"amd-smi was not found at the configured path {self.path}")
        found = shutil.which("amd-smi")
        if found:
            return found
        for p in AMD_WELL_KNOWN + ((os.path.join(os.environ["ROCM_PATH"], "bin", "amd-smi"),) if os.environ.get("ROCM_PATH") else ()):
            if os.path.isfile(p) and os.access(p, os.X_OK):
                return p
        raise AMDNotFound("amd-smi was not found on PATH or in /opt/rocm/bin")

    def _exec(self, binary, *args):
        if tuple(args) not in AMD_ALLOWED:
            raise PermissionError("amd-smi invocation is not on the read-only allow-list")
        try:
            p = subprocess.run([binary, *args], capture_output=True, text=True, timeout=self.timeout, stdin=subprocess.DEVNULL, shell=False)
        except subprocess.TimeoutExpired:
            raise RuntimeError(f"amd-smi {args[0]} timed out")
        if p.returncode != 0:
            msg = (p.stderr or p.stdout).strip()
            raise RuntimeError(msg.splitlines()[0][:300] if msg else f"amd-smi exited {p.returncode}")
        return p.stdout

    def _refresh_static(self, binary, notes):
        if self._static is not None and time.time() - self._static_at < 300:
            return self._static
        listing, static = {}, {}
        try:
            for r in _amd_records(self._exec(binary, "list", "--json")):
                i = _amd_index(r)
                if i is not None:
                    listing[i] = {"uuid": _amd_str(r.get("uuid")), "bdf": _amd_str(r.get("bdf"))}
        except Exception as e:
            notes.append("GPU list unavailable: " + sanitize_error(e))
        try:
            for r in _amd_records(self._exec(binary, "static", "--json")):
                i = _amd_index(r)
                if i is None:
                    continue
                asic, drv, bus, lim = (r.get(k) if isinstance(r.get(k), dict) else {} for k in ("asic", "driver", "bus", "limit"))
                power = 0.0
                for k in ("max_power", "power_cap", "socket_power"):
                    v = _amd_num(lim.get(k))
                    if v:
                        power = v
                        break
                static[i] = {"model": _amd_str(asic.get("market_name")), "serial": _amd_str(asic.get("asic_serial")), "driver": _amd_str(drv.get("version")),
                             "bdf": _amd_str(bus.get("bdf")), "max_width": int(_amd_num(bus.get("max_pcie_width")) or 0),
                             "max_gen": _pcie_gen(_amd_num(bus.get("max_pcie_speed")) or 0), "power_limit": power}
        except Exception as e:
            notes.append("Static GPU facts unavailable: " + sanitize_error(e))
        self._static, self._static_at = (listing, static), time.time()
        return self._static

    def collect(self, hostname):
        binary = self._find()
        notes = []
        try:
            raw = self._exec(binary, "metric", "--json")
        except RuntimeError as e:
            m = str(e).lower()
            if "no gpu" in m or "no amd gpu" in m or "no devices" in m:
                return {"binary": binary, "driver": "", "gpus": [], "extras": {}, "processes": {}, "exported": {}, "fields_note": "amd-smi JSON (experimental)",
                        "no_devices": True, "notes": ["The AMD driver tools answered but report no GPUs."]}
            raise
        recs = _amd_records(raw)
        listing, static = self._refresh_static(binary, notes)
        now = iso(utcnow())
        gpus, extras, by_index, exported, driver = [], {}, {}, {}, ""
        for r in recs:
            i = _amd_index(r)
            if i is None:
                continue
            st, li = static.get(i, {}), listing.get(i, {})
            bdf = li.get("bdf") or st.get("bdf", "")
            uuid = li.get("uuid") or (("AMD-" + st["serial"]) if st.get("serial") else (("AMD-" + bdf) if bdf else f"AMD-{hostname}-{i}"))
            sec = lambda *keys: next((r[k] for k in keys if isinstance(r.get(k), dict)), {})
            usage, mem, temp, power, ecc, fan, pcie = sec("usage"), sec("mem_usage", "memory_usage"), sec("temperature"), sec("power"), sec("ecc"), sec("fan"), sec("pcie")
            latest = {"timestamp": now, "gpu_util_percent": 0.0, "mem_copy_util_percent": 0.0, "fb_used_mib": 0.0, "fb_free_mib": 0.0,
                      "gpu_temp_celsius": 0.0, "memory_temp_celsius": 0.0, "power_usage_watts": 0.0,
                      "xid_error_code": 0, "pcie_replay_counter": 0, "row_remap_failure": 0, "uncorrectable_remapped_rows": 0}
            v = _amd_num(usage.get("gfx_activity"))
            if v is not None:
                latest["gpu_util_percent"], exported[Mapping.gpu_util] = v, True
            v = _amd_num(usage.get("umc_activity"))
            if v is not None:
                latest["mem_copy_util_percent"], exported[Mapping.mem_copy] = v, True
            total, used, free = _amd_mib(mem.get("total_vram"), "MB"), _amd_mib(mem.get("used_vram"), "MB"), _amd_mib(mem.get("free_vram"), "MB")
            if free is None and total is not None and used is not None:
                free = total - used
            if used is not None:
                latest["fb_used_mib"], exported[Mapping.fb_used] = used, True
            if free is not None:
                latest["fb_free_mib"], exported[Mapping.fb_free] = free, True
            t = _amd_num(temp.get("edge"))
            if t is None:
                t = _amd_num(temp.get("hotspot"))
            if t is not None:
                latest["gpu_temp_celsius"], exported[Mapping.gpu_temp] = t, True
            t = _amd_num(temp.get("mem"))
            if t is not None:
                latest["memory_temp_celsius"], exported[Mapping.mem_temp] = t, True
            for k in ("socket_power", "average_socket_power", "current_socket_power"):
                v = _amd_num(power.get(k))
                if v is not None:
                    latest["power_usage_watts"], exported[Mapping.power] = v, True
                    break
            throttles = ["amd_throttled"] if _amd_str(power.get("throttle_status")).upper() == "THROTTLED" else []
            v = _amd_num(ecc.get("total_uncorrectable_count"))
            if v is not None:
                latest["ecc_dbe_vol_total"], exported[Mapping.ecc_dbe] = int(v), True
            v = _amd_num(ecc.get("total_correctable_count"))
            if v is not None:
                latest["ecc_sbe_vol_total"], exported[Mapping.ecc_sbe] = int(v), True
            link = None
            w, sp = _amd_num(pcie.get("width")), _amd_num(pcie.get("speed"))
            if w is not None and sp is not None and st.get("max_width") and st.get("max_gen"):
                link = {"gen_current": _pcie_gen(sp), "gen_max": st["max_gen"], "width_current": int(w), "width_max": st["max_width"]}
            driver = driver or st.get("driver", "")
            if total is None and used is not None and free is not None:
                total = used + free
            dev = {"uuid": uuid, "device_index": i, "node_name": hostname, "model_name": st.get("model") or "AMD GPU", "pci_bus_id": bdf,
                   "driver_version": st.get("driver", ""), "power_limit_watts": st.get("power_limit", 0.0), "total_memory_mib": total or 0.0,
                   "allocated_namespace": "", "allocated_pod": "", "allocated_container": "", "mapping_status": "unallocated",
                   "last_sample_at": now, "latest": latest}
            gpus.append(dev)
            extras[uuid] = {"sources": [SOURCE_AMD], "fan_speed_percent": _amd_num(fan.get("usage")), "pstate": "", "throttle_reasons": throttles,
                            "pcie": link, "processes": [], "emulated": False, "mig_profile": ""}
            by_index[i] = uuid
        procs_by_uuid = {}
        try:
            for r in _amd_records(self._exec(binary, "process", "--json")):
                uuid = by_index.get(_amd_index(r))
                if not uuid:
                    continue
                for item in r.get("process_list") or []:
                    info = item.get("process_info") if isinstance(item.get("process_info"), dict) else item
                    pid = _amd_num(info.get("pid"))
                    if pid is None:
                        continue
                    mu = info.get("memory_usage") if isinstance(info.get("memory_usage"), dict) else {}
                    procs_by_uuid.setdefault(uuid, []).append({"pid": int(pid), "name": _amd_str(info.get("name")), "used_memory_mib": _amd_mib(mu.get("vram_mem"), "B")})
        except Exception:
            notes.append("Compute process list unavailable (amd-smi process may need elevated permissions).")
        for uuid, procs in procs_by_uuid.items():
            extras[uuid]["processes"] = procs
        return {"binary": binary, "driver": driver, "gpus": gpus, "extras": extras, "processes": procs_by_uuid, "exported": exported,
                "fields_note": "amd-smi JSON (experimental)", "no_devices": not gpus, "notes": notes}


# ---------------------------------------------------------------------------------------------
# DCGM exposition parsing + normalisation
# ---------------------------------------------------------------------------------------------
_LABEL_RE = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)="((?:[^"\\]|\\.)*)"')
_LINE_RE = re.compile(r"^([A-Za-z_:][A-Za-z0-9_:]*)(\{.*\})?\s+(\S+)(?:\s+(\d+))?$")


def parse_exposition(body, fallback_time):
    series, skipped = [], 0
    for line in body.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        m = _LINE_RE.match(line)
        if not m:
            skipped += 1
            continue
        try:
            value = float(m.group(3))
        except ValueError:
            skipped += 1
            continue
        if math.isnan(value) or math.isinf(value):
            skipped += 1
            continue
        labels = {k: v.replace('\\"', '"').replace("\\\\", "\\") for k, v in _LABEL_RE.findall(m.group(2) or "")}
        ts = fallback_time
        if m.group(4):
            ts = datetime.fromtimestamp(int(m.group(4)) / (1000 if len(m.group(4)) > 11 else 1), tz=timezone.utc)
        series.append({"name": m.group(1), "labels": labels, "value": value, "ts": ts})
    return series, skipped


class Mapping:
    gpu_util = "DCGM_FI_DEV_GPU_UTIL"
    mem_copy = "DCGM_FI_DEV_MEM_COPY_UTIL"
    fb_used = "DCGM_FI_DEV_FB_USED"
    fb_free = "DCGM_FI_DEV_FB_FREE"
    gpu_temp = "DCGM_FI_DEV_GPU_TEMP"
    mem_temp = "DCGM_FI_DEV_MEMORY_TEMP"
    power = "DCGM_FI_DEV_POWER_USAGE"
    xid = "DCGM_FI_DEV_XID_ERRORS"
    pcie_replay = "DCGM_FI_DEV_PCIE_REPLAY_COUNTER"
    row_remap = "DCGM_FI_DEV_ROW_REMAP_FAILURE"
    unc_remap = "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS"
    ecc_dbe = "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL"
    ecc_sbe = "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL"
    nvl_crc = "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL"
    nvl_replay = "DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL"
    thermal_viol = "DCGM_FI_DEV_THERMAL_VIOLATION"
    power_viol = "DCGM_FI_DEV_POWER_VIOLATION"
    health = "DCGM_EXP_GPU_HEALTH_STATUS"
    clock_events = "DCGM_EXP_CLOCK_EVENTS_COUNT"
    uuid_labels = ["UUID", "uuid"]
    index_label = "gpu"
    node_labels = ["Hostname", "hostname", "node", "kubernetes_node"]
    model_labels = ["modelName", "model_name"]
    pod_labels = ["exported_pod", "pod", "pod_name"]
    ns_labels = ["exported_namespace", "namespace", "pod_namespace"]
    cont_labels = ["exported_container", "container", "container_name"]

    @classmethod
    def required(cls):
        return [cls.gpu_util, cls.mem_copy, cls.fb_used, cls.fb_free, cls.gpu_temp, cls.mem_temp, cls.power, cls.xid, cls.pcie_replay, cls.row_remap]

    @classmethod
    def optional(cls):
        return [cls.unc_remap, cls.ecc_dbe, cls.ecc_sbe, cls.nvl_crc, cls.nvl_replay, cls.thermal_viol, cls.power_viol, cls.health, cls.clock_events]


def missing_metrics(wanted, exported):
    present = sum(1 for n in wanted if exported.get(n))
    return present, [n for n in wanted if not exported.get(n)]


def normalize_series(series, now, max_stale, known_nodes, default_node, source_tag):
    M = Mapping
    res = {"gpus": [], "extras": {}, "exported": {}, "series_count": len(series), "ignored_self": 0, "honor_renamed": False,
           "mig": 0, "emulated": False, "mapped": 0}
    for s in series:
        lb = s["labels"]
        if lb.get("exported_pod") or lb.get("exported_namespace") or lb.get("exported_container"):
            res["honor_renamed"] = True
            break
    pod_c, ns_c, cont_c = (["exported_pod"], ["exported_namespace"], ["exported_container"]) if res["honor_renamed"] else (M.pod_labels, M.ns_labels, M.cont_labels)
    by_key, order = {}, []
    for s in series:
        res["exported"][s["name"]] = True
        if s["name"] == LAB_MARKER:
            res["emulated"] = True
            continue
        lb = s["labels"]
        uuid = first_label(lb, M.uuid_labels)
        if not uuid:
            continue
        key = uuid
        mig_id = lb.get("GPU_I_ID", "")
        if mig_id:
            key = f"{uuid}/mig-{mig_id}"
        st = by_key.get(key)
        ts = s["ts"]
        if st is None:
            node = ""
            if known_nodes:
                for k in M.node_labels:
                    if lb.get(k) and lb[k] in known_nodes:
                        node = lb[k]
                        break
            node = node or first_label(lb, M.node_labels) or default_node
            try:
                idx = int(lb.get(M.index_label, "0"))
            except ValueError:
                idx = 0
            dev = {"uuid": key, "device_index": idx, "node_name": node, "model_name": first_label(lb, M.model_labels), "pci_bus_id": lb.get("pci_bus_id", ""),
                   "driver_version": lb.get("DCGM_FI_DRIVER_VERSION", ""), "power_limit_watts": 0.0, "total_memory_mib": 0.0,
                   "allocated_namespace": "", "allocated_pod": "", "allocated_container": "", "mapping_status": "unallocated",
                   "_last": ts, "latest": {"timestamp": ts, "gpu_util_percent": 0.0, "mem_copy_util_percent": 0.0, "fb_used_mib": 0.0, "fb_free_mib": 0.0,
                                          "gpu_temp_celsius": 0.0, "memory_temp_celsius": 0.0, "power_usage_watts": 0.0, "xid_error_code": 0,
                                          "pcie_replay_counter": 0, "row_remap_failure": 0, "uncorrectable_remapped_rows": 0}}
            ex = {"sources": [source_tag], "fan_speed_percent": None, "pstate": "", "throttle_reasons": [], "pcie": None, "processes": [], "emulated": False,
                  "mig_profile": ""}
            if mig_id:
                ex["mig_profile"] = lb.get("GPU_I_PROFILE", "")
                res["mig"] += 1
            st = {"dev": dev, "fb_total": 0.0, "fb_reserved": 0.0, "self": False}
            by_key[key] = st
            order.append(key)
            res["extras"][key] = ex
        dev, lat = st["dev"], st["dev"]["latest"]
        if not dev["allocated_pod"]:
            pod, ns, cont = first_label(lb, pod_c), first_label(lb, ns_c), first_label(lb, cont_c)
            if pod and ns:
                if not res["honor_renamed"] and ("dcgm-exporter" in pod.lower() or "dcgm-exporter" in cont.lower()):
                    st["self"] = True
                else:
                    dev["allocated_pod"], dev["allocated_namespace"], dev["allocated_container"] = pod, ns, cont
                    dev["mapping_status"] = "mapped"
        if not dev["model_name"]:
            dev["model_name"] = first_label(lb, M.model_labels)
        if ts > dev["_last"]:
            dev["_last"] = ts
            lat["timestamp"] = ts
        n, v = s["name"], s["value"]
        if n == M.gpu_util: lat["gpu_util_percent"] = v
        elif n == M.mem_copy: lat["mem_copy_util_percent"] = v
        elif n == M.fb_used: lat["fb_used_mib"] = v
        elif n == M.fb_free: lat["fb_free_mib"] = v
        elif n == M.gpu_temp: lat["gpu_temp_celsius"] = v
        elif n == M.mem_temp: lat["memory_temp_celsius"] = v
        elif n == M.power: lat["power_usage_watts"] = v
        elif n == M.xid: lat["xid_error_code"] = int(v)
        elif n == M.pcie_replay: lat["pcie_replay_counter"] = int(v)
        elif n == M.row_remap: lat["row_remap_failure"] = int(v)
        elif n == M.unc_remap: lat["uncorrectable_remapped_rows"] = int(v)
        elif n == M.ecc_dbe: lat["ecc_dbe_vol_total"] = int(v)
        elif n == M.ecc_sbe: lat["ecc_sbe_vol_total"] = int(v)
        elif n == M.nvl_crc: lat["nvlink_crc_flit_errors"] = int(v)
        elif n == M.nvl_replay: lat["nvlink_replay_errors"] = int(v)
        elif n == M.thermal_viol: lat["thermal_violation_ns"] = int(v)
        elif n == M.power_viol: lat["power_violation_ns"] = int(v)
        elif n == M.health: lat["health_status"] = int(v)
        elif n == M.clock_events:
            if v > 0 and lb.get("clock_event"):
                lat["clock_throttle_reason"] = lb["clock_event"]
                if lb["clock_event"] not in res["extras"][key]["throttle_reasons"]:
                    res["extras"][key]["throttle_reasons"].append(lb["clock_event"])
        elif n == "DCGM_FI_DEV_POWER_MGMT_LIMIT": dev["power_limit_watts"] = v
        elif n == "DCGM_FI_DEV_FAN_SPEED": res["extras"][key]["fan_speed_percent"] = v
        elif n == "DCGM_FI_DEV_FB_TOTAL": st["fb_total"] = v
        elif n == "DCGM_FI_DEV_FB_RESERVED": st["fb_reserved"] = v
    for key in order:
        st = by_key[key]
        d = st["dev"]
        if st["self"] and not d["allocated_pod"]:
            res["ignored_self"] += 1
        lat = d["latest"]
        if st["fb_total"] > 0:
            d["total_memory_mib"] = st["fb_total"]
        elif lat["fb_used_mib"] + lat["fb_free_mib"] > 0:
            d["total_memory_mib"] = lat["fb_used_mib"] + lat["fb_free_mib"] + st["fb_reserved"]
        d["last_sample_at"] = iso(d["_last"])
        lat["timestamp"] = iso(lat["timestamp"]) if not isinstance(lat["timestamp"], str) else lat["timestamp"]
        d["_last_dt"] = d.pop("_last")
        stale_s = max(0.0, (now - d["_last_dt"]).total_seconds())
        d["staleness_seconds"], d["is_stale"] = stale_s, stale_s > max_stale
        if d["mapping_status"] == "mapped":
            res["mapped"] += 1
        res["extras"][key]["emulated"] = res["emulated"]
        res["gpus"].append(d)
    res["gpus"].sort(key=lambda g: (g["node_name"], g["device_index"], g["uuid"]))
    return res


# ---------------------------------------------------------------------------------------------
# Kubernetes (read-only list calls)
# ---------------------------------------------------------------------------------------------
def parse_quantity_int(v):
    try:
        return int(str(v).strip())
    except (TypeError, ValueError):
        return 0


def _container_gpus(c):
    res = c.get("resources") or {}
    for key in ("limits", "requests"):
        if GPU_RESOURCE in (res.get(key) or {}):
            return parse_quantity_int(res[key][GPU_RESOURCE])
    return 0


def normalize_node(item):
    meta, spec, status = item.get("metadata", {}), item.get("spec", {}), item.get("status", {})
    labels = meta.get("labels") or {}
    ready, conds = False, []
    for c in status.get("conditions") or []:
        if c.get("type") == "Ready" and c.get("status") == "True":
            ready = True
        conds.append({"type": c.get("type", ""), "status": c.get("status", ""), "reason": c.get("reason", ""), "message": c.get("message", ""),
                      "last_transition_time": c.get("lastTransitionTime", "")})
    taints = [f"{t.get('key','')}={t.get('value','')}:{t.get('effect','')}" for t in spec.get("taints") or []]
    return {"name": meta.get("name", ""), "ready": ready, "schedulable": not spec.get("unschedulable", False),
            "gpu_model": labels.get("nvidia.com/gpu.product") or "NVIDIA Datacenter GPU",
            "gpu_capacity": parse_quantity_int((status.get("capacity") or {}).get(GPU_RESOURCE)),
            "gpu_allocatable": parse_quantity_int((status.get("allocatable") or {}).get(GPU_RESOURCE)), "gpu_allocated": 0,
            "labels": labels, "taints": taints, "conditions": conds}


def normalize_pod(item):
    meta, spec, status = item.get("metadata", {}), item.get("spec", {}), item.get("status", {})
    req = sum(_container_gpus(c) for c in spec.get("containers") or [])
    for c in spec.get("initContainers") or []:
        req = max(req, _container_gpus(c))
    if req <= 0:
        return None
    owner_kind, owner_name = "Pod", meta.get("name", "")
    if meta.get("ownerReferences"):
        owner_kind, owner_name = meta["ownerReferences"][0].get("kind", "Pod"), meta["ownerReferences"][0].get("name", "")
    restarts, last_term, detail = 0, None, ""
    for cs in status.get("containerStatuses") or []:
        restarts += cs.get("restartCount", 0)
        term = (cs.get("lastState") or {}).get("terminated")
        cur = (cs.get("state") or {}).get("terminated")
        if term is None and cur and cur.get("exitCode", 0) != 0:
            term = cur
        if term and (last_term is None or (term.get("finishedAt") or "") > (last_term.get("finished_at") or "")):
            last_term = {"exit_code": term.get("exitCode", 0), "reason": term.get("reason", ""), "message": term.get("message", ""),
                         "started_at": term.get("startedAt", ""), "finished_at": term.get("finishedAt", "")}
        waiting = (cs.get("state") or {}).get("waiting")
        if waiting and waiting.get("reason") and not detail:
            detail = waiting["reason"]
    phase, sched_reason, sched_msg = status.get("phase", ""), "", ""
    if phase == "Pending":
        if spec.get("nodeName"):
            phase = "ContainerCreating"
            detail = detail or "ContainerCreating"
        else:
            for c in status.get("conditions") or []:
                if c.get("type") == "PodScheduled" and c.get("status") == "False":
                    sched_reason, sched_msg = c.get("reason", ""), c.get("message", "")
                    if sched_reason == "Unschedulable":
                        sched_reason = "FailedScheduling"
    if not detail and status.get("reason"):
        detail = status["reason"]
    tolerations = []
    for t in spec.get("tolerations") or []:
        if (t.get("key") or "").startswith("node.kubernetes.io/"):
            continue
        tolerations.append(f"{t.get('key','')} {t.get('operator') or 'Equal'} {t.get('value','')}:{t.get('effect','')}".rstrip(":"))
    pod = {"name": meta.get("name", ""), "namespace": meta.get("namespace", ""), "uid": meta.get("uid", ""), "owner_kind": owner_kind,
           "owner_name": owner_name, "phase": phase, "node_name": spec.get("nodeName", ""), "requested_gpus": req,
           "created_at": meta.get("creationTimestamp", ""), "started_at": status.get("startTime"), "restart_count": restarts}
    if last_term:
        pod["last_termination"] = last_term
    if spec.get("nodeSelector"):
        pod["node_selector"] = spec["nodeSelector"]
    if tolerations:
        pod["tolerations"] = tolerations
    if sched_reason:
        pod["scheduler_reason"] = sched_reason
    if sched_msg:
        pod["scheduler_message"] = sched_msg
    if detail:
        pod["status_detail"] = detail
    return pod


def normalize_event(ev):
    count = ev.get("count") or 0
    series = ev.get("series") or {}
    count = max(count, series.get("count") or 0) or 1
    last = ev.get("lastTimestamp") or series.get("lastObservedTime") or ev.get("eventTime") or ev.get("firstTimestamp") or (ev.get("metadata") or {}).get("creationTimestamp") or ""
    first = ev.get("firstTimestamp") or ev.get("eventTime") or last
    src, inv = ev.get("source") or {}, ev.get("involvedObject") or {}
    node = src.get("host") or (inv.get("name", "") if inv.get("kind") == "Node" else "")
    out = {"id": (ev.get("metadata") or {}).get("uid", ""), "type": ev.get("type", ""), "reason": ev.get("reason", ""), "message": ev.get("message", ""),
           "source_component": src.get("component") or ev.get("reportingComponent", ""), "involved_object_kind": inv.get("kind", ""),
           "involved_object_name": inv.get("name", ""), "count": count, "first_timestamp": first, "last_timestamp": last}
    if inv.get("namespace"):
        out["involved_object_namespace"] = inv["namespace"]
    if node:
        out["node_name"] = node
    return out


class KubernetesSource:
    MAX_PAGES = 20

    def __init__(self, endpoint, token, http):
        self.endpoint = endpoint.rstrip("/")
        self.token = token
        self.http = http

    def _list(self, resource, path, normalize, query=None):
        items, total, cont, truncated, access = [], 0, "", False, {"resource": resource, "ok": False, "status": 0, "error": "", "items": 0, "truncated": False}
        try:
            for _ in range(self.MAX_PAGES):
                q = dict(query or {})
                q["limit"] = "500"
                if cont:
                    q["continue"] = cont
                page = self.http.get_json(f"{self.endpoint}{path}?{urllib.parse.urlencode(q)}", self.token)
                for it in page.get("items") or []:
                    total += 1
                    n = normalize(it)
                    if n is not None:
                        items.append(n)
                cont = (page.get("metadata") or {}).get("continue", "")
                if not cont:
                    break
            else:
                truncated = True
            access.update(ok=True, status=200, items=total, truncated=truncated)
            return items, access, None
        except HTTPStatus as e:
            access.update(status=e.status, error=str(e))
            return None, access, e
        except Exception as e:
            access.update(error=sanitize_error(e))
            return None, access, e

    def collect(self):
        res = {"version": "", "platform": "", "emulated": False, "nodes": [], "pods": [], "events": [], "access": [], "total_nodes": 0,
               "gpu_nodes": 0, "total_pods_seen": 0, "other_resources": []}
        ver_err = None
        try:
            ver = self.http.get_json(self.endpoint + "/version", self.token)
            res["version"], res["platform"] = ver.get("gitVersion", ""), ver.get("platform", "")
            res["emulated"] = "gpulens-lab" in res["version"]
        except Exception as e:
            ver_err = e
        other = set()

        def norm_node(it):
            for r in ((it.get("status") or {}).get("capacity") or {}):
                if r.startswith("nvidia.com/") and r != GPU_RESOURCE:
                    other.add(r)
            return normalize_node(it)

        nodes, acc, node_err = self._list("nodes", "/api/v1/nodes", norm_node)
        res["access"].append(acc)
        if node_err is None:
            res["nodes"], res["total_nodes"], res["other_resources"] = nodes, acc["items"], sorted(other)
            res["gpu_nodes"] = sum(1 for n in nodes if n["gpu_capacity"] > 0)
        pods, acc, pod_err = self._list("pods", "/api/v1/pods", normalize_pod)
        res["access"].append(acc)
        if pod_err is None:
            res["pods"], res["total_pods_seen"] = pods, acc["items"]
        events, acc, ev_err = self._list("events", "/api/v1/events", normalize_event, {"fieldSelector": "type=Warning"})
        res["access"].append(acc)
        if ev_err is None:
            res["events"] = sorted(events, key=lambda e: e["last_timestamp"])
        if node_err is None and pod_err is None:
            alloc = {}
            for p in res["pods"]:
                if p["node_name"] and p["requested_gpus"] > 0 and p["phase"] not in ("Succeeded", "Failed"):
                    alloc[p["node_name"]] = alloc.get(p["node_name"], 0) + p["requested_gpus"]
            for n in res["nodes"]:
                n["gpu_allocated"] = alloc.get(n["name"], 0)
        if ver_err and node_err and pod_err and ev_err:
            if any(isinstance(e, HTTPStatus) for e in (node_err, ver_err)):
                return res
            raise node_err
        return res

    @staticmethod
    def usable(res):
        return any(a["ok"] and a["resource"] in ("nodes", "pods") for a in res["access"])


class PrometheusSource:
    """Reads DCGM series with one instant query. (History backfill and the scrape-target audit are Go-runtime features.)"""

    def __init__(self, endpoint, token, http):
        self.endpoint = endpoint.rstrip("/")
        self.token = token
        self.http = http

    def collect(self, now, max_stale, known_nodes):
        version = ""
        try:
            version = (self.http.get_json(self.endpoint + "/api/v1/status/buildinfo", self.token).get("data") or {}).get("version", "")
        except HTTPStatus:
            pass
        q = urllib.parse.urlencode({"query": '{__name__=~"DCGM_.*|gpulens_lab_emulator_info"}'})
        doc = self.http.get_json(f"{self.endpoint}/api/v1/query?{q}", self.token)
        if doc.get("status") != "success":
            raise RuntimeError("Prometheus query failed: " + str(doc.get("error", "unknown error")))
        series = []
        for r in (doc.get("data") or {}).get("result") or []:
            lb = dict(r.get("metric") or {})
            name = lb.pop("__name__", "")
            ts, val = r.get("value", [now.timestamp(), "NaN"])
            try:
                v = float(val)
            except ValueError:
                continue
            if math.isnan(v) or math.isinf(v) or not name:
                continue
            series.append({"name": name, "labels": lb, "value": v, "ts": now})
        norm = normalize_series(series, now, max_stale, known_nodes, "", SOURCE_PROM)
        return {"version": version, "norm": norm}


# ---------------------------------------------------------------------------------------------
# configuration
# ---------------------------------------------------------------------------------------------
def default_live_config():
    def src(key):
        return {"enabled": True, "explicit": False, "endpoint": DEFAULT_ENDPOINTS[key], "bearer_token": "", "token_file": "", "ca_file": ""}

    return {"enabled": True, "auto_start": False, "allow_remote_config": False, "interval_seconds": 2.0, "idle_timeout_seconds": 900.0,
            "history_points": 900, "cluster_name": "", "nvidia_smi_enabled": True, "nvidia_smi_path": "", "amd_smi_enabled": True, "amd_smi_path": "", "host_vitals_enabled": True,
            "dcgm_exporter": src(SOURCE_DCGM), "prometheus": src(SOURCE_PROM), "kubernetes": src(SOURCE_K8S),
            "prometheus_backfill_minutes": 60.0, "in_cluster": False}


def _env_bool(v):
    v = (v or "").strip().lower()
    if v in ("1", "true", "yes", "on", "enabled"):
        return True
    if v in ("0", "false", "no", "off", "disabled"):
        return False
    return None


def apply_env(cfg, env=None):
    env = os.environ if env is None else env
    b = _env_bool(env.get("GPULENS_LIVE_ENABLED"))
    if b is not None:
        cfg["enabled"] = b
    b = _env_bool(env.get("GPULENS_LIVE_ALLOW_REMOTE_CONFIG"))
    if b is not None:
        cfg["allow_remote_config"] = b
    for key, var in (("interval_seconds", "GPULENS_LIVE_INTERVAL_SECONDS"), ("idle_timeout_seconds", "GPULENS_LIVE_IDLE_TIMEOUT_SECONDS"),
                     ("prometheus_backfill_minutes", "GPULENS_PROMETHEUS_BACKFILL_MINUTES")):
        try:
            cfg[key] = float(env.get(var, "").strip())
        except ValueError:
            pass
    if env.get("GPULENS_CLUSTER_NAME", "").strip():
        cfg["cluster_name"] = env["GPULENS_CLUSTER_NAME"].strip()
    b = _env_bool(env.get("GPULENS_NVIDIA_SMI"))
    if b is not None:
        cfg["nvidia_smi_enabled"] = b
    if env.get("GPULENS_NVIDIA_SMI_PATH", "").strip():
        cfg["nvidia_smi_path"] = env["GPULENS_NVIDIA_SMI_PATH"].strip()
    b = _env_bool(env.get("GPULENS_AMD_SMI", ""))
    if b is not None:
        cfg["amd_smi_enabled"] = b
    if env.get("GPULENS_AMD_SMI_PATH", "").strip():
        cfg["amd_smi_path"] = env["GPULENS_AMD_SMI_PATH"].strip()
    b = _env_bool(env.get("GPULENS_HOST_VITALS"))
    if b is not None:
        cfg["host_vitals_enabled"] = b
    for key, prefix in (("dcgm_exporter", "GPULENS_DCGM_EXPORTER"), ("prometheus", "GPULENS_PROMETHEUS"), ("kubernetes", "GPULENS_KUBERNETES")):
        s = cfg[key]
        b = _env_bool(env.get(prefix))
        if b is not None:
            s["enabled"] = b
        if env.get(prefix + "_URL", "").strip():
            s["endpoint"], s["explicit"], s["enabled"] = env[prefix + "_URL"].strip(), True, True
        if env.get(prefix + "_TOKEN", "").strip():
            s["bearer_token"] = env[prefix + "_TOKEN"].strip()
        if env.get(prefix + "_TOKEN_FILE", "").strip():
            s["token_file"] = env[prefix + "_TOKEN_FILE"].strip()
        if env.get(prefix + "_CA_FILE", "").strip():
            s["ca_file"] = env[prefix + "_CA_FILE"].strip()
    return cfg


def detect_in_cluster(cfg, env=None):
    env = os.environ if env is None else env
    k = cfg["kubernetes"]
    token = "/var/run/secrets/kubernetes.io/serviceaccount/token"
    ca = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
    host = env.get("KUBERNETES_SERVICE_HOST", "").strip()
    if k["explicit"] or not k["enabled"] or not host or not os.path.isfile(token):
        return
    port = env.get("KUBERNETES_SERVICE_PORT", "").strip() or "443"
    if ":" in host:
        host = f"[{host}]"
    k["endpoint"], k["explicit"], k["token_file"] = f"https://{host}:{port}", True, token
    if os.path.isfile(ca):
        k["ca_file"] = ca
    cfg["in_cluster"] = True


def validate_live_config(c):
    if not 1 <= c["interval_seconds"] <= 300:
        raise LiveError(f"live: interval_seconds must be between 1 and 300, got {c['interval_seconds']:g}")
    if c["idle_timeout_seconds"] != 0 and c["idle_timeout_seconds"] < 30:
        raise LiveError(f"live: idle_timeout_seconds must be 0 (never) or at least 30, got {c['idle_timeout_seconds']:g}")
    if not 60 <= c["history_points"] <= 20000:
        raise LiveError(f"live: history_points must be between 60 and 20000, got {c['history_points']}")
    if not 0 <= c["prometheus_backfill_minutes"] <= 720:
        raise LiveError(f"live: prometheus_backfill_minutes must be between 0 and 720, got {c['prometheus_backfill_minutes']:g}")
    if len(c["cluster_name"]) > 120:
        raise LiveError("live: cluster_name is too long (max 120 characters)")
    for key, name in (("dcgm_exporter", "DCGM exporter"), ("prometheus", "Prometheus"), ("kubernetes", "Kubernetes")):
        s = c[key]
        if s["enabled"] or s["endpoint"]:
            try:
                validate_endpoint(name, s["endpoint"])
            except LiveError as e:
                raise LiveError("live: " + str(e))


def config_view(c):
    def sv(s):
        return {"enabled": s["enabled"], "explicit": s["explicit"], "endpoint": s["endpoint"], "token_set": bool(s["bearer_token"] or s["token_file"]),
                "token_file": s["token_file"], "ca_file": s["ca_file"]}

    return {"enabled": c["enabled"], "auto_start": c["auto_start"], "allow_remote_config": c["allow_remote_config"], "interval_seconds": c["interval_seconds"],
            "idle_timeout_seconds": c["idle_timeout_seconds"], "history_points": c["history_points"], "cluster_name": c["cluster_name"],
            "nvidia_smi_enabled": c["nvidia_smi_enabled"], "nvidia_smi_path": c["nvidia_smi_path"],
            "amd_smi_enabled": c["amd_smi_enabled"], "amd_smi_path": c["amd_smi_path"], "host_vitals_enabled": c["host_vitals_enabled"],
            "dcgm_exporter": sv(c["dcgm_exporter"]), "prometheus": sv(c["prometheus"]), "kubernetes": sv(c["kubernetes"]),
            "prometheus_backfill_minutes": c["prometheus_backfill_minutes"], "in_cluster": c["in_cluster"]}


# ---------------------------------------------------------------------------------------------
# sample helpers
# ---------------------------------------------------------------------------------------------
NUMERIC_AVG = ("gpu_util_percent", "mem_copy_util_percent", "fb_used_mib", "fb_free_mib", "gpu_temp_celsius", "memory_temp_celsius", "power_usage_watts")


def resample_window(samples, from_dt, bucket):
    """Average samples into equal buckets inside the window, so a mean weights every part of it equally."""
    out, cur, cur_bucket, n = [], None, None, 0

    def flush():
        if n == 0:
            return
        pt = dict(cur["last"])
        for k in NUMERIC_AVG:
            pt[k] = cur["sum"][k] / n
        pt["xid_error_code"], pt["pcie_replay_counter"] = cur["xid"], cur["pcie"]
        pt["row_remap_failure"], pt["uncorrectable_remapped_rows"] = cur["rr"], cur["ur"]
        out.append((datetime.fromtimestamp(cur_bucket, tz=timezone.utc), pt))

    for ts, s in samples:
        if ts < from_dt:
            continue
        b = math.floor(ts.timestamp() / bucket) * bucket
        if n == 0 or b != cur_bucket:
            flush()
            cur_bucket, n = b, 0
            cur = {"sum": {k: 0.0 for k in NUMERIC_AVG}, "xid": 0, "pcie": 0, "rr": 0, "ur": 0, "last": s}
        for k in NUMERIC_AVG:
            cur["sum"][k] += s.get(k) or 0.0
        if s.get("xid_error_code"):
            cur["xid"] = s["xid_error_code"]
        cur["pcie"] = max(cur["pcie"], s.get("pcie_replay_counter") or 0)
        cur["rr"] = max(cur["rr"], s.get("row_remap_failure") or 0)
        cur["ur"] = max(cur["ur"], s.get("uncorrectable_remapped_rows") or 0)
        cur["last"] = s
        n += 1
    flush()
    return out


def anchor_window(raw, out, from_dt, now, bucket):
    if len(out) < 2 or not raw:
        return out
    if raw[0][0] > from_dt + timedelta(seconds=bucket):
        return out
    out[0] = (from_dt, out[0][1])
    out[-1] = (now, out[-1][1])
    return out


def thin_samples(samples, now):
    raw_from, oldest = now - timedelta(minutes=15), now - timedelta(hours=6)
    out, last_bucket = [], None
    for i, (ts, s) in enumerate(samples):
        if ts < oldest:
            continue
        if ts >= raw_from:
            out.extend(samples[i:])
            break
        b = ts.replace(second=0, microsecond=0)
        if s.get("xid_error_code") or b != last_bucket:
            out.append((ts, s))
            last_bucket = b
    return out


def severity_rank(s):
    return {"critical": 3, "warning": 2}.get(s, 1)


def amd_error_hint(err):
    m = str(err).lower()
    if "not initialized" in m or "driver not loaded" in m:
        return "The amdgpu kernel driver is not loaded. Check `dmesg | grep -i amdgpu` and the ROCm installation."
    if "permission" in m:
        return "The user running GPU Lens may not read the GPU. Add it to the `render` and `video` groups."
    if "timed out" in m:
        return "The driver did not answer in time. Check `dmesg` for GPU resets or hangs."
    return "Run `amd-smi metric` in a terminal on this machine to see the full driver error."


def smi_error_hint(err):
    msg = str(err).lower()
    if "couldn't communicate with the nvidia driver" in msg:
        return "The NVIDIA kernel driver is not loaded. Check `dmesg | grep -i nvrm`, reinstall the driver, or reboot."
    if "version mismatch" in msg:
        return "The driver and its user-space library differ in version — reboot after a driver update."
    if "insufficient permissions" in msg:
        return "The user running GPU Lens may not query NVML. Run it as a user with access to /dev/nvidia*."
    if "timed out" in msg:
        return "The driver did not answer in time. A GPU may have fallen off the bus; check `dmesg` for Xid 79."
    if "gpu is lost" in msg:
        return "A GPU has fallen off the bus. A reboot is normally required to recover it."
    return "Run `nvidia-smi` in a terminal on this machine to see the full driver error."


def fmt_pct(v):
    return "n/a" if v is None else f"{v:.0f}%"


# ---------------------------------------------------------------------------------------------
# the engine
# ---------------------------------------------------------------------------------------------
class Engine:
    def __init__(self, rt, cfg, hostname=None):
        """`rt` supplies the shared rule machinery: evaluate_rules, build_capacity_report, get_rule_definitions,
        DEFAULT_THRESHOLDS (the local runtime server module)."""
        self.rt = rt
        self.lock = threading.RLock()
        self.cfg = cfg
        self.hostname = hostname or socket.gethostname()
        self.base_thresholds = dict(rt.DEFAULT_THRESHOLDS)
        self.thresholds = dict(self.base_thresholds)
        self.overrides = {}
        self.guard = selftest_readonly_guard()
        self.running = False
        self.manual = False
        self.generation = 0
        self.wake = threading.Event()
        self.started_at = None
        self.state_reason = ""
        self.last_poll = time.time()
        self.src = {}
        self.history_epoch = 0
        # alerting and durable state (gpulens_extras.py)
        self.alerts = None
        self.state_path = ""
        self.state_loaded_at = self.state_saved_at = None
        self.state_err = ""
        self.restored_resolved, self.restored_first_seen = [], {}
        self._rebuild_sources()
        self._reset_session()

    # ----- setup -----
    def _timeout(self):
        return 10.0

    def _rebuild_sources(self):
        c = self.cfg
        self.src = {}
        ro = "GET/HEAD only — mutating verbs are rejected at the transport before any network I/O."

        def mk(sid, name, kind, endpoint, read_only, enabled, explicit):
            st = {"status": {"id": sid, "name": name, "kind": kind, "enabled": enabled, "explicit": explicit, "state": "pending" if enabled else "disabled",
                             "endpoint": endpoint, "detail": "" if enabled else "Turned off in the live configuration.", "hint": "", "last_attempt_at": None,
                             "last_success_at": None, "last_error": "", "last_latency_ms": None, "latency_history_ms": [], "consecutive_failures": 0,
                             "success_count": 0, "failure_count": 0, "refresh_seconds": 0, "emulated": False, "read_only": read_only, "facts": []},
                  "ever": False, "inflight": False, "next_due": 0.0, "smi": None, "amd": None, "dcgm": None, "dcgm_norm": None, "prom": None, "k8s": None}
            self.src[sid] = st
            return st

        mk(SOURCE_HOST, "Host vitals (machine running GPU Lens)", "host", "local /proc and /sys",
           "Reads local kernel statistics only; nothing is executed or written.", c["host_vitals_enabled"], True)
        self.host_src = HostSource() if c["host_vitals_enabled"] else None
        smi_ep = "local nvidia-smi (auto-discovered)" if not c["nvidia_smi_path"] else "local " + c["nvidia_smi_path"]
        mk(SOURCE_SMI, "Local NVIDIA driver (nvidia-smi)", "nvidia_smi", smi_ep,
           "Fixed allow-list of read-only query flags; no shell, no state-changing options.", c["nvidia_smi_enabled"], bool(c["nvidia_smi_path"]))
        self.smi_src = NvidiaSMISource(c["nvidia_smi_path"]) if c["nvidia_smi_enabled"] else None
        amd_ep = "local amd-smi (auto-discovered)" if not c["amd_smi_path"] else "local " + c["amd_smi_path"]
        mk(SOURCE_AMD, "Local AMD driver (amd-smi, experimental)", "amd_smi", amd_ep,
           "Four fixed read-only JSON queries (list, static, metric, process); no shell, no state-changing options.", c["amd_smi_enabled"], bool(c["amd_smi_path"]))
        self.amd_src = AMDSMISource(c["amd_smi_path"]) if c["amd_smi_enabled"] else None
        self.http = {}
        for sid, key, name, kind, extra in ((SOURCE_DCGM, "dcgm_exporter", "DCGM exporter (direct /metrics scrape)", "dcgm_exporter", ""),
                                            (SOURCE_PROM, "prometheus", "Prometheus HTTP API", "prometheus", ""),
                                            (SOURCE_K8S, "kubernetes", "Kubernetes API", "kubernetes", " Lists nodes, pods and warning events.")):
            s = c[key]
            st = mk(sid, name, kind, s["endpoint"], ro + extra, s["enabled"], s["explicit"])
            if s["enabled"]:
                try:
                    self.http[sid] = ReadOnlyHTTP(self._timeout(), ca_file=s["ca_file"])
                except Exception as e:
                    st["build_error"] = sanitize_error(e)

    def _reset_session(self):
        self.tick = 0
        self.last_tick_at = None
        self.last_tick_dur = 0.0
        self.pending = []
        self.devices = {}
        self.k8s_nodes, self.k8s_pods, self.k8s_events = [], [], []
        self.k8s_nodes_ok = self.k8s_pods_ok = False
        self.snapshot = self.overview = self.host_vitals = None
        self.gpu_views, self.processes, self.view_nodes, self.view_pods = [], [], [], []
        self.history, self.seq = [], 0
        self.history_epoch += 1
        self.activity, self.activity_seq = [], 0
        self.open, self.resolved = {}, list(self.restored_resolved)
        self.restored_resolved = []
        self.prev = {"initialized": False}
        self.merge = self._empty_facts()
        self.exported = {}

    @staticmethod
    def _empty_facts():
        return {"total": 0, "fresh": 0, "mapped": 0, "smi_only": 0, "amd": 0, "emulated": 0, "unmatched_nodes": [], "gaps": [], "unknown_pod_gpus": 0,
                "inferred_nodes": [], "k8s_allocated": 0, "k8s_capacity": 0, "host_workloads": 0, "inferred_pods": 0}

    def interval(self):
        return max(1.0, float(self.cfg["interval_seconds"]))

    def _period(self, sid):
        floor = {SOURCE_DCGM: 5.0, SOURCE_PROM: 15.0, SOURCE_K8S: 15.0}.get(sid, 0.0)
        return max(self.interval(), floor)

    # ----- lifecycle -----
    def enabled(self):
        return self.cfg["enabled"]

    def allow_remote_config(self):
        return self.cfg["allow_remote_config"]

    def start(self, interval_seconds=0):
        with self.lock:
            if not self.cfg["enabled"]:
                raise LiveError("the live console is disabled on this GPU Lens instance (GPULENS_LIVE_ENABLED=false)", 409)
            if interval_seconds and interval_seconds > 0:
                nxt = copy.deepcopy(self.cfg)
                nxt["interval_seconds"] = float(interval_seconds)
                validate_live_config(nxt)
                self.cfg = nxt
            if self.running:
                self.last_poll = time.time()
                return
            self._start_locked("Live collection started.", not self.manual)

    def _start_locked(self, message, spawn):
        self._rebuild_sources()
        self._reset_session()
        self.generation += 1
        gen = self.generation
        self.wake = threading.Event()
        self.running = True
        self.started_at = utcnow()
        self.last_poll = time.time()
        self.state_reason = ""
        self.guard = selftest_readonly_guard()
        self._add_activity(self.started_at, "success", "engine", message, f"Checking every source every {self.cfg['interval_seconds']:g}s. All external calls are read-only.")
        if spawn:
            threading.Thread(target=self._loop, args=(gen, self.wake), daemon=True, name="gpulens-live").start()

    def stop(self, reason="Stopped by operator."):
        with self.lock:
            self._stop_locked(reason)

    def _stop_locked(self, reason):
        if not self.running:
            return
        self.running = False
        self.generation += 1
        self.wake.set()
        self.state_reason = reason or "Stopped by operator."
        for st in self.src.values():
            st["inflight"] = False
        self._add_activity(utcnow(), "info", "engine", "Live collection stopped", self.state_reason)

    def recheck(self):
        with self.lock:
            for st in self.src.values():
                st["next_due"] = 0.0
            wake, running = self.wake, self.running
        if running:
            wake.set()

    def _loop(self, gen, wake):
        while True:
            started = time.time()
            try:
                self._run_tick(gen, wait_all=False)
            except Exception as e:  # never let one bad cycle kill the collector
                with self.lock:
                    self._add_activity(utcnow(), "warning", "engine", "Collection cycle failed", sanitize_error(e))
            with self.lock:
                if not self.running or gen != self.generation:
                    return
                idle = self.cfg["idle_timeout_seconds"]
                if idle > 0 and time.time() - self.last_poll > idle:
                    self._stop_locked(f"Auto-stopped after {idle / 60:.0f} minutes without a viewer. Press Go Live to resume.")
                    return
                interval = self.interval()
            wait = max(0.2, interval - (time.time() - started))
            wake.wait(wait)
            wake.clear()

    def cycle(self):
        """One full collection cycle, waiting for every source (used by tests and by manual driving)."""
        self._run_tick(self.generation, wait_all=True)

    # ----- collection -----
    def _known_nodes(self):
        return {n["name"] for n in self.k8s_nodes} if self.k8s_nodes_ok else set()

    def _run_tick(self, gen, wait_all):
        tick_start = time.time()
        threads = []
        with self.lock:
            if not self.running or gen != self.generation:
                return
            known = self._known_nodes()
            max_stale = self.thresholds["max_staleness_seconds"]

            def launch(sid, fn):
                st = self.src.get(sid)
                if st is None or not st["status"]["enabled"] or st["inflight"] or tick_start < st["next_due"]:
                    return
                st["inflight"] = True
                st["status"]["last_attempt_at"] = iso(utcnow())

                def run():
                    begin = time.time()
                    try:
                        out = fn()
                        out["err"] = None
                    except SMINotFound as e:
                        out = {"err": e, "not_found": True}
                    except Exception as e:
                        out = {"err": e}
                    out.update(id=sid, at=utcnow(), latency=time.time() - begin)
                    self._deliver(gen, out)

                t = threading.Thread(target=run, daemon=True)
                threads.append(t)

            if self.host_src is not None:
                launch(SOURCE_HOST, lambda: {"host": self.host_src.collect()})
            if self.smi_src is not None:
                launch(SOURCE_SMI, lambda: {"smi": self.smi_src.collect(self.hostname)})
            if self.amd_src is not None:
                launch(SOURCE_AMD, lambda: {"amd": self.amd_src.collect(self.hostname)})
            c = self.cfg
            for sid, key in ((SOURCE_DCGM, "dcgm_exporter"), (SOURCE_K8S, "kubernetes"), (SOURCE_PROM, "prometheus")):
                st = self.src[sid]
                if not st["status"]["enabled"]:
                    continue
                if st.get("build_error"):
                    self._fail_build(st)
                    continue
                sc = c[key]
                http = self.http.get(sid)
                if http is None:
                    continue

                def token_for(sc=sc):
                    return read_token_file(sc["token_file"]) if sc["token_file"] else sc["bearer_token"]

                if sid == SOURCE_DCGM:
                    def fn(http=http, sc=sc):
                        status, hdrs, raw = http.request("GET", sc["endpoint"], token_for(), {"Accept": "text/plain;version=0.0.4"})
                        now = utcnow()
                        series, skipped = parse_exposition(raw.decode("utf-8", "replace"), now)
                        return {"dcgm": {"series": series, "skipped": skipped, "bytes": len(raw), "emulated_header": hdrs.get("x-gpulens-emulated", "")}}
                elif sid == SOURCE_K8S:
                    def fn(http=http, sc=sc, token_for=token_for):
                        return {"k8s": KubernetesSource(sc["endpoint"], token_for(), http).collect()}
                else:
                    def fn(http=http, sc=sc, token_for=token_for, known=known):
                        k = self._known_nodes() or known
                        return {"prom": PrometheusSource(sc["endpoint"], token_for(), http).collect(utcnow(), max_stale, k)}
                launch(sid, fn)
            first = self.tick == 0
        for t in threads:
            t.start()
        if threads:
            grace = None if wait_all else min(1.5, self.interval() * 0.75)
            if not wait_all and first:
                grace = 2.5
            deadline = None if grace is None else time.time() + grace
            for t in threads:
                t.join(None if deadline is None else max(0.0, deadline - time.time()))
        with self.lock:
            if not self.running or gen != self.generation:
                return
            self._integrate(tick_start)

    def _fail_build(self, st):
        st["status"].update(last_attempt_at=iso(utcnow()), state="error", last_error=st["build_error"], detail=st["build_error"],
                            hint="Fix the configured CA bundle path and press Re-check.")

    def _deliver(self, gen, out):
        with self.lock:
            if gen != self.generation or not self.running:
                return
            self._record_outcome(out)
            self.pending.append(out)

    # ----- per-source bookkeeping (mirrors collect.go) -----
    def _record_outcome(self, out):
        sid = out["id"]
        st = self.src.get(sid)
        if st is None:
            return
        s = st["status"]
        st["inflight"] = False
        period = self._period(sid)
        started = out["at"].timestamp() - out["latency"]
        st["next_due"] = started + period
        ms = out["latency"] * 1000.0
        s["last_latency_ms"] = round(ms, 3)
        s["latency_history_ms"] = (s["latency_history_ms"] + [round(ms, 3)])[-LATENCY_HISTORY:]
        s["refresh_seconds"] = period
        err = out.get("err")

        def fail(state, detail, hint, e):
            s.update(state=state, detail=detail, hint=hint, last_error=sanitize_error(e) if e else "")
            s["failure_count"] += 1
            s["consecutive_failures"] += 1

        def neutral_not_found(detail, hint, e):
            st["next_due"] = started + max(period, NOT_FOUND_RETRY)
            fail("not_found", detail, hint, e)
            s["failure_count"] -= 1
            s["consecutive_failures"] = 0
            s["last_error"] = ""

        def succeed():
            s["last_success_at"] = iso(out["at"])
            s["last_error"], s["hint"] = "", ""
            s["success_count"] += 1
            s["consecutive_failures"] = 0
            st["ever"] = True

        def net_fail(auto_d, auto_h, exp_d, exp_h):
            if not s["explicit"] and not st["ever"]:
                neutral_not_found(auto_d, auto_h, err)
                return
            detail = exp_d if s["explicit"] else "This source was connected earlier in the session and has stopped answering."
            fail("unreachable", detail, exp_h, err)

        if sid == SOURCE_HOST:
            succeed()
            self.host_vitals = out["host"]
            self._describe_host(st, out["host"])
        elif sid == SOURCE_SMI:
            if out.get("not_found"):
                neutral_not_found("nvidia-smi is not installed on this machine.",
                                  "Install the NVIDIA driver (it ships nvidia-smi), or connect a DCGM exporter or Prometheus instead.", err)
            elif err:
                fail("error", "nvidia-smi is installed but the driver query failed.", smi_error_hint(err), err)
            else:
                succeed()
                st["smi"] = out["smi"]
                self._describe_smi(st, out["smi"])
        elif sid == SOURCE_AMD:
            if out.get("not_found"):
                neutral_not_found("amd-smi is not installed on this machine (only needed for AMD GPUs).",
                                  "For AMD GPUs, install ROCm (it ships amd-smi). Machines with NVIDIA GPUs do not need it.", err)
            elif err:
                fail("error", "amd-smi is installed but the driver query failed.", amd_error_hint(err), err)
            else:
                succeed()
                st["amd"] = out["amd"]
                self._describe_amd(st, out["amd"])
        elif sid == SOURCE_DCGM:
            if err:
                net_fail("No DCGM exporter answered on the default local address.",
                         "Run dcgm-exporter on this host, or set the exporter URL under Connections (GPULENS_DCGM_EXPORTER_URL).",
                         "The configured DCGM exporter could not be scraped.",
                         "Check that the URL ends in /metrics and that the exporter pod or service is reachable from this machine.")
                return
            succeed()
            st["dcgm"] = out["dcgm"]
            s["state"] = "connected"
            s["detail"] = f"Scrape OK — {len(out['dcgm']['series'])} series."
        elif sid == SOURCE_PROM:
            if err:
                net_fail("No Prometheus answered on the default local address.",
                         "Set the Prometheus URL under Connections (GPULENS_PROMETHEUS_URL) if your cluster has one.",
                         "The configured Prometheus could not be queried.",
                         "Check the URL, network path and bearer token. GPU Lens needs GET access to /api/v1/query.")
                return
            succeed()
            st["prom"] = out["prom"]
            self._describe_prom(st)
        elif sid == SOURCE_K8S:
            if err:
                net_fail("No Kubernetes API answered on the default local address (kubectl proxy).",
                         "Run `kubectl proxy --port=8001` next to GPU Lens, or set the API server URL and token under Connections.",
                         "The configured Kubernetes API server could not be reached.",
                         "Check the API server URL, the CA bundle (GPULENS_KUBERNETES_CA_FILE) and the bearer token.")
                return
            st["k8s"] = out["k8s"]
            self._describe_k8s(st, out["k8s"], succeed, fail, started, period)

    def _describe_host(self, st, v):
        s = st["status"]
        s["facts"] = [{"label": "Hostname", "value": v["hostname"]}, {"label": "OS", "value": f"{v['os']}/{v['arch']}"}]
        for label, key in (("Platform", "platform"), ("Kernel", "kernel"), ("CPU", "cpu_model")):
            if v[key]:
                s["facts"].append({"label": label, "value": v[key]})
        s["facts"].append({"label": "Logical CPUs", "value": str(v["logical_cpus"])})
        if not v["supported"]:
            s.update(state="degraded", detail=f"Basic host facts only on {v['os']} ({v['logical_cpus']} logical CPUs).",
                     hint="Detailed CPU, memory, disk and network vitals are collected on Linux hosts.")
            return
        s["state"] = "connected"
        s["detail"] = f"{v['logical_cpus']} logical CPUs · CPU {fmt_pct(v['cpu_percent'])} · memory {fmt_pct(v['mem_used_percent'])} used"

    def _describe_smi(self, st, res):
        s = st["status"]
        s["endpoint"] = "local " + res["binary"]
        procs = sum(len(x) for x in res["processes"].values())
        s["facts"] = [{"label": "Binary", "value": res["binary"]}, {"label": "Driver version", "value": or_dash(res["driver"])},
                      {"label": "GPUs", "value": str(len(res["gpus"]))}, {"label": "Compute processes", "value": str(procs)},
                      {"label": "Query fields", "value": res["fields_note"]}]
        if res["no_devices"]:
            s.update(state="degraded", detail="The NVIDIA driver answered but reports no GPU devices.",
                     hint="Check that the GPU is seated and visible in `lspci`, and that the driver matches the hardware.")
        elif res["notes"]:
            s.update(state="degraded", detail=f"{len(res['gpus'])} GPU(s) · driver {or_dash(res['driver'])} — {res['notes'][0]}")
        else:
            s.update(state="connected", detail=f"{len(res['gpus'])} GPU(s) · driver {or_dash(res['driver'])} · {procs} compute process(es)")

    def _describe_amd(self, st, res):
        s = st["status"]
        s["endpoint"] = "local " + res["binary"]
        procs = sum(len(x) for x in res["processes"].values())
        s["facts"] = [{"label": "Binary", "value": res["binary"]}, {"label": "Driver version", "value": or_dash(res["driver"])},
                      {"label": "GPUs", "value": str(len(res["gpus"]))}, {"label": "Compute processes", "value": str(procs)},
                      {"label": "Support", "value": "Experimental: verified against AMD's documented output, not yet on real hardware"}]
        if res["no_devices"]:
            s.update(state="degraded", detail="The AMD driver tools answered but report no GPU devices.",
                     hint="Check that the GPU is visible in `lspci` and that the amdgpu driver is loaded.")
        else:
            s.update(state="connected", detail=f"{len(res['gpus'])} GPU(s) · driver {or_dash(res['driver'])} · {procs} compute process(es)")

    def _describe_dcgm(self, st):
        n, scrape, s = st["dcgm_norm"], st["dcgm"], st["status"]
        if not n or not scrape:
            return
        s["emulated"] = n["emulated"] or bool(scrape["emulated_header"])
        nodes = {g["node_name"] for g in n["gpus"]}
        req, miss_req = missing_metrics(Mapping.required(), n["exported"])
        opt, miss_opt = missing_metrics(Mapping.optional(), n["exported"])
        s["facts"] = [{"label": "Series", "value": f"{n['series_count']} ({scrape['bytes']} bytes)"}, {"label": "GPUs", "value": f"{len(n['gpus'])} on {len(nodes)} node(s)"},
                      {"label": "Default metrics", "value": f"{req} of {req + len(miss_req)}"},
                      {"label": "Optional fault metrics", "value": f"{opt} of {opt + len(miss_opt)}"},
                      {"label": "GPUs with pod labels", "value": str(n["mapped"])}]
        if scrape["skipped"]:
            s["facts"].append({"label": "Unparseable lines skipped", "value": str(scrape["skipped"])})
        if not n["gpus"]:
            s.update(state="degraded", detail="The endpoint answered but exposes no DCGM GPU series.",
                     hint="Confirm this URL is a dcgm-exporter /metrics endpoint and that the exporter can see GPUs.")
        elif miss_req:
            s.update(state="degraded", detail=f"{len(n['gpus'])} GPU(s); {len(miss_req)} default metric(s) missing: {', '.join(miss_req)}",
                     hint="Enable the missing fields in the dcgm-exporter collectors CSV.")
        else:
            s.update(state="connected", detail=f"{len(n['gpus'])} GPU(s) on {len(nodes)} node(s) · {n['series_count']} series")

    def _describe_prom(self, st):
        res, s = st["prom"], st["status"]
        n = res["norm"]
        s["emulated"] = n["emulated"]
        nodes = {g["node_name"] for g in n["gpus"]}
        req, miss_req = missing_metrics(Mapping.required(), n["exported"])
        s["facts"] = [{"label": "Version", "value": or_dash(res["version"])}, {"label": "GPUs via DCGM series", "value": f"{len(n['gpus'])} on {len(nodes)} node(s)"},
                      {"label": "Default metrics", "value": f"{req} of {req + len(miss_req)}"},
                      {"label": "Python runtime", "value": "instant query only (no history backfill, no target audit)"}]
        if not n["gpus"]:
            s.update(state="degraded", detail="Connected, but no DCGM GPU series were found.",
                     hint="Make sure Prometheus scrapes dcgm-exporter (a ServiceMonitor or scrape job for port 9400).")
        elif miss_req:
            s.update(state="degraded", detail=f"{len(n['gpus'])} GPU(s) visible; {len(miss_req)} default metric(s) missing: {', '.join(miss_req)}",
                     hint="Enable the missing fields in the dcgm-exporter collectors CSV.")
        else:
            s.update(state="connected", detail=f"{len(n['gpus'])} GPU(s) on {len(nodes)} node(s) via DCGM series")

    def _describe_k8s(self, st, res, succeed, fail, started, period):
        s = st["status"]
        s["emulated"] = res["emulated"]
        denied, facts = [], [{"label": "Version", "value": or_dash(res["version"])}]
        for a in res["access"]:
            val = f"{a['items']} listed"
            if not a["ok"]:
                val = f"denied (HTTP {a['status']})" if a["status"] else "failed"
                denied.append(a["resource"])
            elif a["truncated"]:
                val += " (truncated)"
            facts.append({"label": "GET " + a["resource"], "value": val})
        if not KubernetesSource.usable(res):
            s["facts"] = facts
            if not s["explicit"] and not st["ever"]:
                st["next_due"] = time.time() + NOT_FOUND_RETRY
                s.update(state="not_found", detail="The default local address answered, but not as a usable Kubernetes API.",
                         hint="Run `kubectl proxy --port=8001` next to GPU Lens, or set the API server URL and token under Connections.", last_error="")
                return
            fail("error", "The API server answered, but nodes and pods could not be listed.",
                 "Apply deploy/kubernetes/rbac-readonly.yaml and use that service account's token (HTTP 401 = bad token, 403 = missing RBAC).",
                 RuntimeError("listing nodes and pods was denied"))
            return
        succeed()
        gpu_cap = sum(n["gpu_capacity"] for n in res["nodes"])
        facts += [{"label": "GPU nodes", "value": f"{res['gpu_nodes']} of {res['total_nodes']} ({gpu_cap} GPUs)"},
                  {"label": "GPU pods", "value": f"{len(res['pods'])} of {res['total_pods_seen']} scanned"},
                  {"label": "Warning events", "value": str(len(res["events"]))}]
        if res["other_resources"]:
            facts.append({"label": "Not modeled", "value": ", ".join(res["other_resources"])})
        s["facts"] = facts
        if denied:
            s.update(state="degraded", detail=f"Connected ({or_dash(res['version'])}), but access is partial: {', '.join(sorted(denied))} denied.",
                     hint="Grant get/list on nodes, pods and events (deploy/kubernetes/rbac-readonly.yaml).")
            return
        s["state"] = "connected"
        s["detail"] = f"{or_dash(res['version'])} · {res['total_nodes']} node(s), {res['gpu_nodes']} with GPUs · {len(res['pods'])} GPU pod(s)"

    # ----- activity -----
    def _add_activity(self, t, severity, kind, title, detail=""):
        self.activity_seq += 1
        self.activity.append({"seq": self.activity_seq, "t": iso(t), "severity": severity, "kind": kind, "title": title, "detail": detail})
        if len(self.activity) > MAX_ACTIVITY:
            self.activity = self.activity[-MAX_ACTIVITY:]

    # ----- integration -----
    SOURCE_PRIORITY = {SOURCE_SMI: 0, SOURCE_AMD: 0, SOURCE_DCGM: 1, SOURCE_PROM: 2}

    def _observe(self, sid, dev, extras, exported, at):
        uuid = dev["uuid"]
        ds = self.devices.get(uuid)
        if ds is None:
            ds = {"obs": {}, "dev": None, "extras": None, "samples": [], "first_seen": at, "last_seen": at, "last_sample": None,
                  "proc_pid": 0, "proc_name": "", "proc_since": None, "host_proc": False}
            self.devices[uuid] = ds
        dev = copy.deepcopy(dev)
        if "_last_dt" not in dev:
            dev["_last_dt"] = parse_iso(dev.get("last_sample_at")) or at
        ex = copy.deepcopy(extras) if extras else {"sources": [sid], "fan_speed_percent": None, "pstate": "", "throttle_reasons": [], "pcie": None,
                                                   "processes": [], "emulated": False, "mig_profile": ""}
        ds["obs"][sid] = {"dev": dev, "extras": ex, "at": at, "exported": exported or {}}
        ds["last_seen"] = at

    def _merge_device(self, ds, known):
        if not ds["obs"]:
            return
        ids = sorted(ds["obs"], key=lambda i: (-ds["obs"][i]["dev"]["_last_dt"].timestamp(), self.SOURCE_PRIORITY.get(i, 9)))
        base = ds["obs"][ids[0]]
        dev = copy.deepcopy(base["dev"])
        ex = copy.deepcopy(base["extras"])
        ex["sources"] = [ids[0]]
        for sid in ids[1:]:
            o = ds["obs"][sid]
            od, oex = o["dev"], o["extras"]
            ex["sources"].append(sid)
            if not dev["allocated_pod"] and od["allocated_pod"]:
                dev["allocated_pod"], dev["allocated_namespace"], dev["allocated_container"] = od["allocated_pod"], od["allocated_namespace"], od["allocated_container"]
                dev["mapping_status"] = "mapped"
            for k in ("model_name", "pci_bus_id", "driver_version"):
                if not dev[k]:
                    dev[k] = od[k]
            for k in ("power_limit_watts", "total_memory_mib"):
                if dev[k] <= 0:
                    dev[k] = od[k]
            if known and dev["node_name"] not in known and od["node_name"] in known:
                dev["node_name"] = od["node_name"]
            lat, olat = dev["latest"], od["latest"]
            for metric, key in ((Mapping.xid, "xid_error_code"), (Mapping.pcie_replay, "pcie_replay_counter"), (Mapping.row_remap, "row_remap_failure"),
                                (Mapping.unc_remap, "uncorrectable_remapped_rows"), (Mapping.mem_temp, "memory_temp_celsius")):
                if not base["exported"].get(metric) and o["exported"].get(metric):
                    lat[key] = olat.get(key, 0)
            for key in ("ecc_dbe_vol_total", "ecc_sbe_vol_total", "nvlink_crc_flit_errors", "nvlink_replay_errors", "thermal_violation_ns",
                        "power_violation_ns", "health_status", "clock_throttle_reason"):
                if key not in lat and key in olat:
                    lat[key] = olat[key]
            if ex["fan_speed_percent"] is None:
                ex["fan_speed_percent"] = oex["fan_speed_percent"]
            ex["pstate"] = ex["pstate"] or oex["pstate"]
            ex["pcie"] = ex["pcie"] or oex["pcie"]
            ex["mig_profile"] = ex["mig_profile"] or oex["mig_profile"]
            for r in oex["throttle_reasons"]:
                if r not in ex["throttle_reasons"]:
                    ex["throttle_reasons"].append(r)
            if not ex["processes"] and oex["processes"]:
                ex["processes"] = list(oex["processes"])
            ex["emulated"] = ex["emulated"] or oex["emulated"]
        ds["dev"], ds["extras"] = dev, ex

    def _integrate(self, tick_start):
        now = utcnow()
        tick_time = now.replace(microsecond=0)
        outs, self.pending = self.pending, []
        for out in outs:
            if out["id"] != SOURCE_K8S or out.get("err") or not out.get("k8s"):
                continue
            for a in out["k8s"]["access"]:
                if a["resource"] == "nodes" and a["ok"]:
                    self.k8s_nodes, self.k8s_nodes_ok = out["k8s"]["nodes"], True
                elif a["resource"] == "pods" and a["ok"]:
                    self.k8s_pods, self.k8s_pods_ok = out["k8s"]["pods"], True
                elif a["resource"] == "events" and a["ok"]:
                    self.k8s_events = out["k8s"]["events"]
        known = self._known_nodes()
        max_stale = self.thresholds["max_staleness_seconds"]
        for out in outs:
            if out.get("err"):
                continue
            sid = out["id"]
            if sid == SOURCE_SMI and out.get("smi"):
                res = out["smi"]
                for g in res["gpus"]:
                    self._observe(SOURCE_SMI, g, res["extras"].get(g["uuid"]), res["exported"], out["at"])
            elif sid == SOURCE_AMD and out.get("amd"):
                res = out["amd"]
                for g in res["gpus"]:
                    self._observe(SOURCE_AMD, g, res["extras"].get(g["uuid"]), res["exported"], out["at"])
            elif sid == SOURCE_DCGM and out.get("dcgm"):
                norm = normalize_series(out["dcgm"]["series"], out["at"], max_stale, known, self.hostname, SOURCE_DCGM)
                if out["dcgm"]["emulated_header"]:
                    norm["emulated"] = True
                    for ex in norm["extras"].values():
                        ex["emulated"] = True
                st = self.src[SOURCE_DCGM]
                st["dcgm_norm"] = norm
                self._describe_dcgm(st)
                for g in norm["gpus"]:
                    self._observe(SOURCE_DCGM, g, norm["extras"].get(g["uuid"]), norm["exported"], out["at"])
            elif sid == SOURCE_PROM and out.get("prom"):
                n = out["prom"]["norm"]
                for g in n["gpus"]:
                    self._observe(SOURCE_PROM, g, n["extras"].get(g["uuid"]), n["exported"], out["at"])
        for uuid in list(self.devices):
            ds = self.devices[uuid]
            for sid in list(ds["obs"]):
                st = self.src.get(sid)
                if st is None or not st["status"]["enabled"]:
                    del ds["obs"][sid]
                    continue
                last_ok = parse_iso(st["status"]["last_success_at"])
                if last_ok and last_ok > ds["obs"][sid]["at"] + timedelta(seconds=1):
                    del ds["obs"][sid]
            self._merge_device(ds, known)
            d = ds["dev"]
            d["staleness_seconds"] = max(0.0, (now - d["_last_dt"]).total_seconds())
            d["is_stale"] = d["staleness_seconds"] > max_stale
            if ds["last_sample"] is None or d["_last_dt"] > ds["last_sample"]:
                pt = dict(d["latest"])
                pt["timestamp"] = iso(tick_time)
                if ds["samples"] and ds["samples"][-1][0] == tick_time:
                    ds["samples"][-1] = (tick_time, pt)
                else:
                    ds["samples"].append((tick_time, pt))
                ds["last_sample"] = d["_last_dt"]
            if not ds["obs"] and (now - ds["last_seen"]).total_seconds() > DEVICE_FORGET_AFTER:
                self._add_activity(now, "warning", "gpu", f"GPU {short_uuid(uuid)} removed from the live inventory",
                                   f"No source has reported this GPU for {DEVICE_FORGET_AFTER / 60:.0f} minutes.")
                del self.devices[uuid]
        if (self.tick + 1) % 30 == 0:
            for ds in self.devices.values():
                ds["samples"] = thin_samples(ds["samples"], now)
        self.tick += 1
        self._assemble(now)
        self.last_tick_at = now
        self.last_tick_dur = time.time() - tick_start

    # ----- snapshot assembly + rule evaluation -----
    def _assemble(self, now):
        facts = self._empty_facts()
        known = self._known_nodes()
        uuids = sorted(self.devices, key=lambda u: (self.devices[u]["dev"]["node_name"], self.devices[u]["dev"]["device_index"], u))
        nodes = [dict(n) for n in self.k8s_nodes]
        by_node = {}
        for u in uuids:
            by_node.setdefault(self.devices[u]["dev"]["node_name"], []).append(u)
        inferred = set()
        for name in sorted(by_node):
            if name in known:
                continue
            lst = by_node[name]
            model = next((self.devices[u]["dev"]["model_name"] for u in lst if self.devices[u]["dev"]["model_name"]), "")
            nodes.append({"name": name, "ready": True, "schedulable": True, "gpu_model": model, "gpu_capacity": len(lst), "gpu_allocatable": len(lst),
                          "gpu_allocated": 0, "labels": {"gpulens.io/inferred": "telemetry"}})
            inferred.add(name)
            facts["inferred_nodes"].append(name)
            if self.k8s_nodes_ok:
                for u in lst:
                    if any(s not in LOCAL_DRIVER_SOURCES for s in self.devices[u]["extras"]["sources"]):
                        if name not in facts["unmatched_nodes"]:
                            facts["unmatched_nodes"].append(name)
        for n in nodes:
            if not n["gpu_model"] or n["gpu_model"] == "NVIDIA Datacenter GPU":
                for u in by_node.get(n["name"], []):
                    if self.devices[u]["dev"]["model_name"]:
                        n["gpu_model"] = self.devices[u]["dev"]["model_name"]
                        break
            if n["name"] not in inferred:
                facts["k8s_allocated"] += n.get("gpu_allocated", 0)
                facts["k8s_capacity"] += n.get("gpu_capacity", 0)

        pods = []
        for p in self.k8s_pods:
            p = dict(p)
            p["assigned_gpu_uuids"] = []
            pods.append(p)
        pod_index = {f"{p['namespace']}/{p['name']}": i for i, p in enumerate(pods)}
        observed_since = {}

        def first_sample_time(ds):
            return ds["samples"][0][0] if ds["samples"] else ds["first_seen"]

        def add_host_workload(ds, uuid):
            d = ds["dev"]
            base = (ds["proc_name"].strip().replace("\\", "/").split("/")[-1] or "process")[:40]
            name = f"{base}[{ds['proc_pid']}]"
            key = f"{HOST_PROCESS_NS}/{name}"
            d["allocated_namespace"], d["allocated_pod"], d["allocated_container"], d["mapping_status"] = HOST_PROCESS_NS, name, "", "mapped"
            ds["host_proc"] = True
            since = ds["proc_since"] or now
            if key in pod_index:
                p = pods[pod_index[key]]
                p["assigned_gpu_uuids"].append(uuid)
                p["requested_gpus"] = len(p["assigned_gpu_uuids"])
                if since < parse_iso(p["started_at"]):
                    p["started_at"] = p["created_at"] = iso(since)
                return
            pods.append({"name": name, "namespace": HOST_PROCESS_NS, "uid": f"host-process:{d['node_name']}:{ds['proc_pid']}", "owner_kind": "HostProcess",
                         "owner_name": ds["proc_name"], "phase": "Running", "node_name": d["node_name"], "requested_gpus": 1, "assigned_gpu_uuids": [uuid],
                         "created_at": iso(since), "started_at": iso(since), "restart_count": 0,
                         "status_detail": "Host compute process reported by the local GPU driver (nvidia-smi / amd-smi), not a Kubernetes pod."})
            pod_index[key] = len(pods) - 1
            facts["host_workloads"] += 1

        for u in uuids:
            ds = self.devices[u]
            d = ds["dev"]
            if not ds["obs"] and ds["host_proc"]:
                add_host_workload(ds, u)
                continue
            ds["host_proc"] = False
            if d["allocated_pod"]:
                key = f"{d['allocated_namespace']}/{d['allocated_pod']}"
                if key in pod_index:
                    p = pods[pod_index[key]]
                    p["assigned_gpu_uuids"].append(u)
                    if p["uid"].startswith("inferred:"):
                        p["requested_gpus"] = len(p["assigned_gpu_uuids"])
                elif not self.k8s_pods_ok:
                    started = first_sample_time(ds)
                    pods.append({"name": d["allocated_pod"], "namespace": d["allocated_namespace"], "uid": "inferred:" + key, "owner_kind": "Pod",
                                 "owner_name": d["allocated_pod"], "phase": "Running", "node_name": d["node_name"], "requested_gpus": 1,
                                 "assigned_gpu_uuids": [u], "created_at": iso(started), "started_at": iso(started), "restart_count": 0,
                                 "status_detail": "Inferred from DCGM pod labels (Kubernetes API not connected)."})
                    pod_index[key] = len(pods) - 1
                    facts["inferred_pods"] += 1
                else:
                    facts["unknown_pod_gpus"] += 1
                continue
            if d["node_name"] not in inferred:
                continue
            procs = ds["extras"]["processes"]
            if not procs:
                ds["proc_pid"] = 0
                continue
            best = procs[0]
            for p in procs[1:]:
                pm = p["used_memory_mib"] if p["used_memory_mib"] is not None else -1
                bm = best["used_memory_mib"] if best["used_memory_mib"] is not None else -1
                if pm > bm or (pm == bm and p["pid"] < best["pid"]):
                    best = p
            if ds["proc_pid"] != best["pid"]:
                ds["proc_pid"], ds["proc_since"] = best["pid"], now
            ds["proc_name"] = best["name"]
            add_host_workload(ds, u)

        for n in nodes:
            if n["name"] in inferred:
                n["gpu_allocated"] = sum(1 for u in by_node.get(n["name"], []) if self.devices[u]["dev"]["allocated_pod"])
        for n in nodes:
            if n["name"] in inferred or n.get("gpu_allocated", 0) <= 0:
                continue
            lst = by_node.get(n["name"], [])
            if not lst:
                continue
            mapped = sum(1 for u in lst if self.devices[u]["dev"]["mapping_status"] == "mapped")
            unlabelled = [u for u in lst if self.devices[u]["dev"]["mapping_status"] != "mapped" and not self.devices[u]["dev"]["is_stale"]]
            missing = n["gpu_allocated"] - mapped
            if missing <= 0:
                continue
            if len(unlabelled) == missing:
                for u in unlabelled:
                    self.devices[u]["dev"]["mapping_status"] = "missing_pod_labels"
            elif len(unlabelled) > missing:
                facts["gaps"].append(f"{n['name']}: Kubernetes reports {n['gpu_allocated']} allocated GPU(s), {mapped} series carry pod labels, {len(unlabelled)} unlabelled")

        gpus = []
        for u in uuids:
            ds = self.devices[u]
            d = {k: v for k, v in ds["dev"].items() if not k.startswith("_")}
            d["samples"] = [dict(s) for _, s in ds["samples"]]
            d["last_sample_at"] = iso(ds["dev"]["_last_dt"])
            d["staleness_seconds"] = round(ds["dev"]["staleness_seconds"], 1)
            gpus.append(d)
            facts["total"] += 1
            if not d["is_stale"]:
                facts["fresh"] += 1
            if d["mapping_status"] == "mapped":
                facts["mapped"] += 1
            if ds["extras"]["sources"] == [SOURCE_SMI]:
                facts["smi_only"] += 1
            if ds["extras"]["sources"] == [SOURCE_AMD]:
                facts["amd"] += 1
            if ds["extras"]["emulated"]:
                facts["emulated"] += 1
            if d["allocated_pod"]:
                k = f"{d['allocated_namespace']}/{d['allocated_pod']}"
                t = first_sample_time(ds)
                if k not in observed_since or t > observed_since[k]:
                    observed_since[k] = t

        self.exported = {}
        for sid in (SOURCE_SMI, SOURCE_AMD, SOURCE_DCGM, SOURCE_PROM):
            st = self.src.get(sid)
            if not st:
                continue
            ex = None
            if sid == SOURCE_SMI and st["smi"] and st["status"]["state"] != "disabled":
                ex = st["smi"]["exported"]
            elif sid == SOURCE_AMD and st["amd"] and st["status"]["state"] != "disabled":
                ex = st["amd"]["exported"]
            elif sid == SOURCE_DCGM and st["dcgm_norm"]:
                ex = st["dcgm_norm"]["exported"]
            elif sid == SOURCE_PROM and st["prom"]:
                ex = st["prom"]["norm"]["exported"]
            for k, v in (ex or {}).items():
                self.exported[k] = self.exported.get(k, False) or v
        self.exported.pop(LAB_MARKER, None)
        sources = self._source_health(now)
        emulated = any(st["status"]["emulated"] and st["status"]["state"] in ("connected", "degraded") for st in self.src.values())
        data_mode = "live_emulated" if emulated else "live_telemetry"
        cluster = self.cfg["cluster_name"] or f"live@{self.hostname}"
        snap = {"scenario_id": "live", "scenario_name": "Live Telemetry", "scenario_description": self._describe_session(facts, emulated),
                "cluster_name": cluster, "data_mode": data_mode, "collected_at": iso(now), "sources": sources, "nodes": nodes, "gpus": gpus, "pods": pods,
                "events": list(self.k8s_events), "exported_metrics": dict(self.exported)}

        th = self.thresholds
        window_from = now - timedelta(minutes=th["low_util_window_minutes"])
        bucket = max(1, int(max(min(th["low_util_window_minutes"] * 60 / 60, 60), self.interval())))
        windowed = dict(snap)
        wg = []
        for u, g in zip(uuids, gpus):
            ds = self.devices[u]
            rs = resample_window(ds["samples"], window_from, bucket)
            rs = anchor_window(ds["samples"], rs, window_from, now, bucket)
            g2 = dict(g)
            g2["samples"] = [dict(pt, timestamp=iso(ts)) for ts, pt in rs]
            wg.append(g2)
        windowed["gpus"] = wg
        wp = []
        for p in pods:
            p = dict(p)
            if p.get("started_at"):
                seen = observed_since.get(f"{p['namespace']}/{p['name']}")
                started = parse_iso(p["started_at"])
                if seen is not None and started is not None and seen > started:
                    p["started_at"] = iso(seen)
                elif seen is None and not p["assigned_gpu_uuids"]:
                    p["started_at"] = iso(now)
            wp.append(p)
        windowed["pods"] = wp

        incidents = [i for i in self.rt.evaluate_rules(snap, th) if i["rule_id"] != LOW_UTIL_RULE]
        incidents += [i for i in self.rt.evaluate_rules(windowed, th) if i["rule_id"] == LOW_UTIL_RULE]
        incidents.sort(key=lambda i: (-severity_rank(i["severity"]), i["rule_id"], i["id"]))
        self._annotate_provenance(incidents)
        incidents = self._track_incidents(incidents, now)
        if self.alerts is not None:
            # Resolutions are only announced once the session has settled (see internal/live/merge.go).
            self.alerts.sync(incidents, {"cluster": snap["cluster_name"], "data_mode": snap["data_mode"], "version": VERSION}, self.tick >= 3)
        cap = self.rt.build_capacity_report(windowed, th)
        for n in cap.get("nodes", []):
            if n["node_name"] in inferred and str(n.get("status_note", "")).startswith("Healthy"):
                n["status_note"] = "Telemetry-only host (no Kubernetes scheduler state)"
        self.merge = facts
        self.snapshot = snap
        self.view_nodes, self.view_pods = nodes, pods
        self.overview = self._build_overview(snap, incidents, cap, facts)
        self._build_views(uuids, incidents)
        self._record_history(now, incidents)
        self._diff_activity(now, incidents)

    def _describe_session(self, f, emulated):
        parts = [self.src[i]["status"]["name"] for i in (SOURCE_SMI, SOURCE_AMD, SOURCE_DCGM, SOURCE_PROM, SOURCE_K8S)
                 if self.src[i]["status"]["state"] in ("connected", "degraded")]
        if not parts:
            return "No GPU telemetry source is connected. Nothing is being monitored."
        text = f"Live read-only collection from {', '.join(parts)} — {f['total']} GPU(s) observed."
        if emulated:
            text += " At least one source is the GPU Lens Live Lab emulator: its values are synthetic."
        return text

    def _source_health(self, now):
        out = []
        for sid in (SOURCE_SMI, SOURCE_AMD, SOURCE_DCGM, SOURCE_PROM, SOURCE_K8S):
            st = self.src.get(sid)
            if st is None or not st["status"]["enabled"]:
                continue
            s = st["status"]
            if not st["ever"] and not (s["explicit"] and sid not in LOCAL_DRIVER_SOURCES) and s["state"] != "error":
                continue
            if s["state"] == "pending":
                continue
            ref = parse_iso(s["last_success_at"]) or self.started_at
            stale_s = max(0.0, (now - ref).total_seconds())
            h = {"id": sid, "name": s["name"], "kind": s["kind"], "mode": "live (emulated endpoint)" if s["emulated"] else "live", "endpoint": s["endpoint"],
                 "available": s["state"] in ("connected", "degraded"), "read_only_verified": self.guard["ok"], "last_success_at": s["last_success_at"] or "",
                 "last_error": s["last_error"], "notes": s["detail"], "staleness_seconds": stale_s, "is_stale": stale_s > self.thresholds["max_staleness_seconds"],
                 "exported_required_count": 0, "exported_optional_count": 0}
            ex = None
            if sid == SOURCE_SMI and st["smi"]:
                ex = st["smi"]["exported"]
            elif sid == SOURCE_AMD and st["amd"]:
                ex = st["amd"]["exported"]
            elif sid == SOURCE_DCGM and st["dcgm_norm"]:
                ex = st["dcgm_norm"]["exported"]
            elif sid == SOURCE_PROM and st["prom"]:
                ex = st["prom"]["norm"]["exported"]
            if ex is not None:
                h["exported_required_count"], h["missing_required"] = missing_metrics(Mapping.required(), ex)
                h["exported_optional_count"], h["missing_optional"] = missing_metrics(Mapping.optional(), ex)
            out.append(h)
        return out

    def _build_overview(self, snap, incidents, cap, f):
        counts = {"critical": 0, "warning": 0, "info": 0, "inconclusive": 0, "total": len(incidents)}
        blind = False
        for inc in incidents:
            counts[inc["severity"]] = counts.get(inc["severity"], 0) + 1
            if inc["eval_status"] == "inconclusive":
                counts["inconclusive"] += 1
            if inc["rule_id"] == "GPU-FRESH-005":
                blind = True
        status, headline = "healthy", "All monitored GPUs, nodes, and telemetry streams are operating within healthy envelopes."
        if blind:
            status, headline = "telemetry_blind_spot", "Monitoring blind spot detected: stale, unavailable, or uncorrelated GPU telemetry. Missing data is never treated as healthy."
        elif f["total"] == 0 and not self.k8s_nodes_ok:
            status, headline = "no_telemetry", "No GPU telemetry source is connected, so nothing is being monitored. This is not a healthy verdict — connect a source under Connections."
        elif f["total"] == 0 and f["k8s_capacity"] == 0:
            status, headline = "no_telemetry", "Kubernetes is connected but advertises no nvidia.com/gpu capacity, and no GPU telemetry source is connected. Nothing is being monitored."
        elif f["total"] == 0:
            status, headline = "telemetry_blind_spot", "Kubernetes inventory is live, but no GPU hardware telemetry is connected: hardware-health, thermal and utilization rules are blind."
        elif counts["critical"] > 0:
            status, headline = "critical", "Critical GPU hardware, scheduling, or workload failure findings require operator investigation."
        elif counts["warning"] > 0:
            status, headline = "warning", "Active GPU scheduling, thermal, or utilization warnings detected."
        return {"cluster_name": snap["cluster_name"], "scenario_id": snap["scenario_id"], "scenario_name": snap["scenario_name"],
                "scenario_description": snap["scenario_description"], "data_mode": snap["data_mode"], "collected_at": snap["collected_at"],
                "fleet_health_status": status, "fleet_health_headline": headline, "active_incident_counts": counts, "capacity_summary": cap,
                "active_incidents": incidents, "sources": snap["sources"]}

    def _annotate_provenance(self, incidents):
        local_only = {u: ds["extras"]["sources"][0] for u, ds in self.devices.items()
                      if len(ds["extras"]["sources"]) == 1 and ds["extras"]["sources"][0] in LOCAL_DRIVER_SOURCES}
        if not local_only:
            return
        notes = {SOURCE_SMI: "Telemetry for the affected GPU was read from the local NVIDIA driver via nvidia-smi; DCGM field names denote the equivalent canonical signals.",
                 SOURCE_AMD: "Telemetry for the affected GPU was read from the local AMD driver via amd-smi; DCGM field names denote the equivalent canonical signals."}

        def touches(ref):
            return next((src for u, src in local_only.items() if u in ref), "")

        for inc in incidents:
            via = ""
            for lst in (inc.get("supporting_evidence") or [], inc.get("contradicting_evidence") or []):
                for ev in lst:
                    src = touches(ev.get("object_ref", ""))
                    if ev.get("source") == "dcgm_exporter" and src:
                        ev["source"] = src
                        via = src
            if not via:
                via = next((touches(r) for r in inc.get("affected_resources") or [] if touches(r)), "")
            if via:
                inc.setdefault("observed_facts", []).append(notes[via])

    def _track_incidents(self, incidents, now):
        nxt = {}
        for inc in incidents:
            fp = inc["fingerprint"]
            prev = self.open.get(fp)
            if prev:
                if prev.get("first_seen_at") and (not inc.get("first_seen_at") or prev["first_seen_at"] < inc["first_seen_at"]):
                    inc["first_seen_at"] = prev["first_seen_at"]
                inc["occurrence_count"] = prev.get("occurrence_count", 1) + 1
                if prev["severity"] != inc["severity"]:
                    self._add_activity(now, "critical" if inc["severity"] == "critical" else "warning", "incident",
                                       f"Severity changed: {prev['severity']} → {inc['severity']}", inc["title"])
            else:
                inc["occurrence_count"] = 1
                if not inc.get("first_seen_at"):
                    inc["first_seen_at"] = iso(now)
                sev = "inconclusive" if inc["eval_status"] == "inconclusive" else inc["severity"]
                since = self.restored_first_seen.pop(fp, None)
                if since:
                    # Still firing after a GPU Lens restart: keep its original age instead of reporting a new incident.
                    if (parse_iso(since) or now) < (parse_iso(inc["first_seen_at"]) or now):
                        inc["first_seen_at"] = since
                    self._add_activity(now, sev, "incident", f"Incident still open [{inc['rule_id']}]", inc["title"] + " — open since before GPU Lens restarted.")
                else:
                    self._add_activity(now, sev, "incident", f"Incident opened [{inc['rule_id']}]", inc["title"])
            if (parse_iso(inc.get("last_seen_at")) or now) < now:
                inc["last_seen_at"] = iso(now)
            nxt[fp] = copy.deepcopy(inc)
        for fp, prev in sorted(self.open.items(), key=lambda kv: kv[1]["id"]):
            if fp not in nxt:
                first = parse_iso(prev.get("first_seen_at")) or now
                dur = max(0.0, (now - first).total_seconds())
                self.resolved.append({"incident": prev, "resolved_at": iso(now), "duration_seconds": dur})
                human = f"{dur:.0f}s" if dur < 90 else (f"{dur / 60:.0f} min" if dur < 5400 else f"{dur / 3600:.1f} h")
                self._add_activity(now, "success", "incident", f"Incident resolved [{prev['rule_id']}]", f"{prev['title']} — no longer firing after {human}.")
        self.resolved = self.resolved[-MAX_RESOLVED:]
        self.open = nxt
        if self.tick >= 3:
            self.restored_first_seen = {}
        return incidents

    def _build_views(self, uuids, incidents):
        views, procs = [], []
        for u in uuids:
            ds = self.devices[u]
            d, ex = ds["dev"], ds["extras"]
            lat = d["latest"]
            total = d["total_memory_mib"]
            if total <= 0 and lat["fb_used_mib"] + lat["fb_free_mib"] > 0:
                total = lat["fb_used_mib"] + lat["fb_free_mib"]
            fb_total = d["total_memory_mib"] or (lat["fb_used_mib"] + lat["fb_free_mib"])
            v = {"uuid": d["uuid"], "device_index": d["device_index"], "node_name": d["node_name"], "model_name": d["model_name"], "pci_bus_id": d["pci_bus_id"],
                 "driver_version": d["driver_version"], "power_limit_watts": d["power_limit_watts"], "total_memory_mib": total,
                 "allocated_namespace": d["allocated_namespace"], "allocated_pod": d["allocated_pod"], "allocated_container": d["allocated_container"],
                 "mapping_status": d["mapping_status"], "last_sample_at": iso(d["_last_dt"]), "staleness_seconds": round1(d["staleness_seconds"]),
                 "is_stale": d["is_stale"], "latest": lat, "fb_used_percent": round1(lat["fb_used_mib"] / fb_total * 100.0) if fb_total > 0 else None,
                 "sources": list(ex["sources"]), "fan_speed_percent": ex["fan_speed_percent"], "pstate": ex["pstate"], "throttle_reasons": list(ex["throttle_reasons"]),
                 "pcie": ex["pcie"], "processes": list(ex["processes"]), "emulated": ex["emulated"], "mig_profile": ex["mig_profile"],
                 "sample_count": len(ds["samples"]), "first_seen_at": iso(ds["first_seen"]), "incident_ids": [], "worst_severity": "",
                 "exported_metrics": sorted({k for o in ds["obs"].values() for k, ok in (o.get("exported") or {}).items() if ok and k != LAB_MARKER})}
            worst = 0
            for inc in incidents:
                if any(u in r for r in inc.get("affected_resources") or []):
                    v["incident_ids"].append(inc["id"])
                    if severity_rank(inc["severity"]) > worst:
                        worst, v["worst_severity"] = severity_rank(inc["severity"]), inc["severity"]
            views.append(v)
            for p in ex["processes"]:
                procs.append({"gpu_uuid": u, "gpu_index": d["device_index"], "node_name": d["node_name"], "pid": p["pid"], "name": p["name"],
                              "used_memory_mib": p["used_memory_mib"]})
        self.gpu_views, self.processes = views, procs

    def _record_history(self, now, incidents):
        pt = {"seq": 0, "t": iso(now), "gpus": 0, "fresh_gpus": 0, "util": None, "fb": None, "temp_max": None, "power": None, "crit": 0, "warn": 0,
              "cpu": None, "mem": None, "g": {}}
        su = sf = mt = sp = 0.0
        fbn = 0
        for u, ds in self.devices.items():
            d = ds["dev"]
            pt["gpus"] += 1
            if d["is_stale"]:
                continue
            pt["fresh_gpus"] += 1
            lat = d["latest"]
            su += lat["gpu_util_percent"]
            total = d["total_memory_mib"] or (lat["fb_used_mib"] + lat["fb_free_mib"])
            fb = lat["fb_used_mib"] / total * 100.0 if total > 0 else 0.0
            if total > 0:
                sf += fb
                fbn += 1
            mt = max(mt, lat["gpu_temp_celsius"])
            sp += lat["power_usage_watts"]
            pt["g"][u] = [round1(lat["gpu_util_percent"]), round1(fb), round1(lat["gpu_temp_celsius"]), round1(lat["power_usage_watts"])]
        if pt["fresh_gpus"]:
            pt["util"], pt["temp_max"], pt["power"] = round1(su / pt["fresh_gpus"]), round1(mt), round1(sp)
            if fbn:
                pt["fb"] = round1(sf / fbn)
        for inc in incidents:
            if inc["severity"] == "critical":
                pt["crit"] += 1
            elif inc["severity"] == "warning":
                pt["warn"] += 1
        hv = self.host_vitals
        if hv:
            if hv.get("cpu_percent") is not None:
                pt["cpu"] = round1(hv["cpu_percent"])
            if hv.get("mem_used_percent") is not None:
                pt["mem"] = round1(hv["mem_used_percent"])
        self.seq += 1
        pt["seq"] = self.seq
        self.history.append(pt)
        lim = self.cfg["history_points"]
        if lim > 0 and len(self.history) > lim:
            self.history = self.history[-lim:]

    def _diff_activity(self, now, incidents):
        cur = {"initialized": True, "sources": {i: st["status"]["state"] for i, st in self.src.items()}, "gpus": {}, "nodes": {}, "pods": {},
               "events": {e["id"]: e["count"] for e in self.k8s_events}, "fleet": self.overview["fleet_health_status"] if self.overview else ""}
        th = self.thresholds
        for u, ds in self.devices.items():
            d = ds["dev"]
            cur["gpus"][u] = {"stale": d["is_stale"], "hot": (not d["is_stale"]) and d["latest"]["gpu_temp_celsius"] >= th["temp_warning_celsius"],
                              "xid": d["latest"]["xid_error_code"], "throttle": ", ".join(ds["extras"]["throttle_reasons"]), "node": d["node_name"], "index": d["device_index"]}
        for n in self.view_nodes:
            cur["nodes"][n["name"]] = {"ready": n["ready"], "schedulable": n["schedulable"], "alloc": n["gpu_allocatable"]}
        for p in self.view_pods:
            cur["pods"][f"{p['namespace']}/{p['name']}"] = {"phase": p["phase"], "restarts": p["restart_count"]}
        prev = self.prev
        budget = [24]
        suppressed = [0]

        def log(sev, kind, title, detail=""):
            if budget[0] <= 0:
                suppressed[0] += 1
                return
            budget[0] -= 1
            self._add_activity(now, sev, kind, title, detail)

        for sid in (SOURCE_HOST, SOURCE_SMI, SOURCE_AMD, SOURCE_DCGM, SOURCE_PROM, SOURCE_K8S):
            st = self.src.get(sid)
            if not st:
                continue
            was, is_ = prev.get("sources", {}).get(sid), cur["sources"][sid]
            if was is not None and was == is_:
                continue
            if is_ in ("pending", "disabled"):
                continue
            s = st["status"]
            if is_ == "connected":
                log("success", "source", ("Source recovered: " if was in ("unreachable", "error") else "Source connected: ") + s["name"], s["detail"])
            elif is_ == "degraded":
                log("warning", "source", "Source degraded: " + s["name"], s["detail"])
            elif is_ in ("unreachable", "error"):
                log("critical", "source", "Source unavailable: " + s["name"], (s["detail"] + " " + s["last_error"]).strip())
            elif is_ == "not_found" and (was is None or was == "pending"):
                log("info", "source", "Not found: " + s["name"], s["detail"])
        pg = prev.get("gpus", {})
        if not pg:
            if cur["gpus"]:
                log("success", "gpu", f"{len(cur['gpus'])} GPU(s) discovered", self.snapshot["scenario_description"])
        else:
            for u in sorted(cur["gpus"]):
                if u not in pg:
                    g = cur["gpus"][u]
                    log("info", "gpu", f"GPU {g['index']} on {g['node']} appeared", u)
        for u in sorted(cur["gpus"]):
            g, p = cur["gpus"][u], pg.get(u)
            if p is None:
                continue
            label, d = f"GPU {g['index']} on {g['node']}", self.devices[u]["dev"]
            if g["stale"] and not p["stale"]:
                log("critical", "gpu", label + " telemetry went stale", f"Last sample {d['staleness_seconds']:.0f}s ago (limit {th['max_staleness_seconds']:.0f}s).")
            elif not g["stale"] and p["stale"]:
                log("success", "gpu", label + " telemetry is fresh again", u)
            if g["hot"] and not p["hot"]:
                log("warning", "gpu", f"{label} crossed {th['temp_warning_celsius']:.0f}°C", f"Core temperature is now {d['latest']['gpu_temp_celsius']:.0f}°C.")
            elif not g["hot"] and p["hot"] and not g["stale"]:
                log("success", "gpu", f"{label} cooled below {th['temp_warning_celsius']:.0f}°C", f"Core temperature is now {d['latest']['gpu_temp_celsius']:.0f}°C.")
            if g["xid"] != 0 and g["xid"] != p["xid"]:
                log("critical", "gpu", f"{label} reported Xid {g['xid']}", "A new NVIDIA driver error code was observed on this GPU.")
            if g["throttle"] != p["throttle"]:
                if g["throttle"]:
                    log("warning", "gpu", label + " is being throttled", "Active clock event reasons: " + g["throttle"])
                else:
                    log("success", "gpu", label + " is no longer throttled")
        if prev.get("initialized"):
            for name in sorted(cur["nodes"]):
                n, p = cur["nodes"][name], prev["nodes"].get(name)
                if p is None:
                    continue
                if n["ready"] != p["ready"]:
                    log("success" if n["ready"] else "critical", "node", f"Node {name} is Ready again" if n["ready"] else f"Node {name} became NotReady")
                if n["schedulable"] != p["schedulable"]:
                    log("success" if n["schedulable"] else "warning", "node", f"Node {name} was uncordoned" if n["schedulable"] else f"Node {name} was cordoned")
                if n["alloc"] != p["alloc"]:
                    log("success" if n["alloc"] > p["alloc"] else "warning", "node", f"Node {name} allocatable GPUs: {p['alloc']} → {n['alloc']}")
            for key in sorted(cur["pods"]):
                pod, p = cur["pods"][key], prev["pods"].get(key)
                if p is None:
                    if pod["phase"] == "Pending":
                        log("warning", "workload", "New pending GPU workload: " + key)
                    else:
                        log("info", "workload", "GPU workload appeared: " + key, "Phase " + pod["phase"])
                    continue
                if pod["phase"] != p["phase"]:
                    sev = "critical" if pod["phase"] == "Failed" else ("success" if pod["phase"] == "Running" else "info")
                    log(sev, "workload", f"{key}: {p['phase']} → {pod['phase']}")
                if pod["restarts"] > p["restarts"]:
                    note = ""
                    for vp in self.view_pods:
                        if f"{vp['namespace']}/{vp['name']}" == key and vp.get("last_termination"):
                            note = f"Last termination: {or_dash(vp['last_termination'].get('reason', ''))} (exit code {vp['last_termination'].get('exit_code', 0)})."
                    log("critical", "workload", f"{key} restarted ({p['restarts']} → {pod['restarts']})", note)
            for key in sorted(prev["pods"]):
                if key not in cur["pods"]:
                    log("info", "workload", "GPU workload gone: " + key)
            for ev in self.k8s_events:
                if ev["id"] in prev["events"] and prev["events"][ev["id"]] >= ev["count"]:
                    continue
                target = ev["involved_object_name"]
                if ev.get("involved_object_namespace"):
                    target = ev["involved_object_namespace"] + "/" + target
                msg = " ".join(ev["message"].split())
                log("warning", "k8s_event", f"{ev['reason']} on {ev['involved_object_kind'].lower()} {target}", msg[:220] + ("…" if len(msg) > 220 else ""))
            if cur["fleet"] != prev.get("fleet") and prev.get("fleet"):
                sev = {"critical": "critical", "telemetry_blind_spot": "critical", "no_telemetry": "critical", "warning": "warning", "healthy": "success"}.get(cur["fleet"], "info")
                log(sev, "fleet", f"Fleet status: {prev['fleet']} → {cur['fleet']}", self.overview["fleet_health_headline"])
        if suppressed[0]:
            self._add_activity(now, "info", "engine", f"{suppressed[0]} more change(s) in this cycle", "The change log is capped per cycle to stay readable.")
        self.prev = cur

    # ----- configuration / thresholds -----
    @staticmethod
    def _apply_source_patch(cur, p, default_endpoint):
        cur = dict(cur)
        if not isinstance(p, dict):
            return cur
        if isinstance(p.get("enabled"), bool):
            cur["enabled"] = p["enabled"]
        if isinstance(p.get("endpoint"), str):
            ep = p["endpoint"].strip()
            if not ep:
                cur["endpoint"], cur["explicit"] = default_endpoint, False
            elif ep != cur["endpoint"] or not cur["explicit"]:
                cur["endpoint"], cur["explicit"] = ep, True
                cur["bearer_token"] = ""  # a token entered for one endpoint must never be sent to another
                cur["token_file"] = ""
        if p.get("clear_token") is True:
            cur["bearer_token"] = ""
        tok = p.get("bearer_token")
        if isinstance(tok, str) and tok.strip():
            cur["bearer_token"] = tok.strip()
        return cur

    CONFIG_PATCH_KEYS = {"cluster_name", "interval_seconds", "nvidia_smi_enabled", "amd_smi_enabled", "host_vitals_enabled", "dcgm_exporter", "prometheus", "kubernetes"}
    CONNECTION_KEYS = {"cluster_name", "nvidia_smi_enabled", "amd_smi_enabled", "host_vitals_enabled", "dcgm_exporter", "prometheus", "kubernetes"}

    def update_config(self, patch):
        unknown = sorted(set(patch) - self.CONFIG_PATCH_KEYS)
        if unknown:
            raise LiveError(f"invalid JSON payload: json: unknown field \"{unknown[0]}\"")
        with self.lock:
            if not self.cfg["enabled"]:
                raise LiveError("the live console is disabled on this GPU Lens instance (GPULENS_LIVE_ENABLED=false)", 409)
            nxt = copy.deepcopy(self.cfg)
            if patch.get("cluster_name") is not None:
                nxt["cluster_name"] = str(patch["cluster_name"]).strip()
            if patch.get("interval_seconds") is not None:
                try:
                    nxt["interval_seconds"] = float(patch["interval_seconds"])
                except (TypeError, ValueError):
                    raise LiveError("invalid JSON payload: interval_seconds must be a number")
            for k in ("nvidia_smi_enabled", "amd_smi_enabled", "host_vitals_enabled"):
                if patch.get(k) is not None:
                    if not isinstance(patch[k], bool):
                        raise LiveError(f"invalid JSON payload: {k} must be a boolean")
                    nxt[k] = patch[k]
            for key, sid in (("dcgm_exporter", SOURCE_DCGM), ("prometheus", SOURCE_PROM), ("kubernetes", SOURCE_K8S)):
                nxt[key] = self._apply_source_patch(nxt[key], patch.get(key), DEFAULT_ENDPOINTS[sid])
                if len(nxt[key]["bearer_token"]) > 8192:
                    raise LiveError("live: bearer token is too long")
            validate_live_config(nxt)
            only_interval = nxt["interval_seconds"] != self.cfg["interval_seconds"] and all(
                nxt[k] == self.cfg[k] for k in self.CONNECTION_KEYS)
            self.cfg = nxt
            if only_interval:
                self._add_activity(utcnow(), "info", "engine", "Refresh interval changed", f"Now collecting every {nxt['interval_seconds']:g}s.")
                for st in self.src.values():
                    st["next_due"] = 0.0
                return
            if self.running:
                self._stop_locked("Live configuration changed.")
                self._start_locked("Live configuration changed — session restarted.", not self.manual)
            else:
                self._rebuild_sources()
                self._reset_session()

    def set_thresholds(self, overrides, reset=False):
        with self.lock:
            if not self.cfg["enabled"]:
                raise LiveError("the live console is disabled on this GPU Lens instance (GPULENS_LIVE_ENABLED=false)", 409)
            merged = {} if reset else dict(self.overrides)
            for k, v in (overrides or {}).items():
                if isinstance(v, bool) or not isinstance(v, (int, float)):
                    raise LiveError(f"invalid JSON payload: threshold {k!r} must be a number")
                merged[k] = float(v)
            for k, v in list(merged.items()):
                if k not in self.base_thresholds:
                    raise LiveError(f'unknown threshold "{k}"')
                if math.isnan(v) or math.isinf(v):
                    raise LiveError(f'threshold "{k}" must be a finite number')
                if k in INTEGER_THRESHOLDS and v != math.trunc(v):
                    raise LiveError(f'threshold "{k}" must be a whole number')
                if self.base_thresholds[k] == v:
                    del merged[k]  # an override equal to the default is not an override
            th = dict(self.base_thresholds)
            for k, v in merged.items():
                th[k] = int(v) if k in INTEGER_THRESHOLDS else v
            validate_thresholds(th)
            self.thresholds, self.overrides = th, merged
            parts = sorted(f"{k}={v:g}" for k, v in merged.items())
            detail = "All live rules use the configured defaults again." if not parts else "Live overrides: " + ", ".join(parts) + ". The simulated demo is unaffected."
            self._add_activity(utcnow(), "info", "threshold", "Live rule thresholds updated", detail)

    # ----- preflight -----
    class _Checks:
        def __init__(self):
            self.groups = []

        def group(self, gid, title):
            self.groups.append({"id": gid, "title": title, "checks": []})

        def add(self, cid, title, status, detail, hint="", latency=None):
            self.groups[-1]["checks"].append({"id": cid, "title": title, "status": status, "detail": detail, "hint": hint, "latency_ms": latency})

        def gate(self, st, cid, title):
            s = st["status"]
            state = s["state"]
            if state == "disabled":
                self.add(cid, title, "skip", "Turned off in the live configuration.", "Enable it under Connections.")
                return False
            if state == "pending":
                self.add(cid, title, "pending", "Checking…")
                return False
            if state == "not_found":
                self.add(cid, title, "skip", s["detail"], s["hint"], s["last_latency_ms"])
                return False
            if state in ("unreachable", "error"):
                detail = s["detail"]
                if s["last_error"]:
                    detail = (detail + " " + s["last_error"]).strip()
                self.add(cid, title, "fail", detail, s["hint"], s["last_latency_ms"])
                return False
            return True

    @staticmethod
    def _plural_n(n, one, many):
        return f"{n} {one if n == 1 else many}"

    @staticmethod
    def _emu(flag):
        return " This endpoint is the GPU Lens Live Lab emulator: values are synthetic." if flag else ""

    def _metric_checks(self, b, prefix, n):
        P = self._plural_n
        req, miss_req = missing_metrics(Mapping.required(), n["exported"])
        if not miss_req:
            b.add(prefix + ".metrics", "Default DCGM metrics", "pass", f"All {req} default metrics are exported.")
        else:
            b.add(prefix + ".metrics", "Default DCGM metrics", "warn", f"{req} of {req + len(miss_req)} exported; missing: {', '.join(miss_req)}.",
                  "Rules that need a missing metric treat it as not exported — never as zero. Enable it in the dcgm-exporter collectors CSV.")
        opt, miss_opt = missing_metrics(Mapping.optional(), n["exported"])
        if not miss_opt:
            b.add(prefix + ".optional", "Optional fault metrics", "pass", f"All {opt} optional fault metrics are exported.")
        else:
            b.add(prefix + ".optional", "Optional fault metrics", "info", f"{opt} of {opt + len(miss_opt)} exported; not exported: {', '.join(miss_opt)}.",
                  "Optional: uncomment these in the dcgm-exporter CSV for ECC, NVLink and violation coverage.")

    def _attribution_check(self, b, cid, n):
        if not n["gpus"]:
            return
        if n["ignored_self"] > 0:
            b.add(cid, "Workload labels", "warn", f"{self._plural_n(n['ignored_self'], 'GPU', 'GPUs')} carried pod labels that point at the dcgm-exporter pod itself and were ignored.",
                  "Prometheus is overwriting workload labels with scrape-target labels. Set honorLabels: true on the ServiceMonitor, or keep honor_labels=false and GPU Lens will read exported_pod.")
        elif n["mapped"] == 0:
            b.add(cid, "Workload labels", "info", "No GPU series carries pod labels.", "Either no workload holds a GPU, or the exporter runs without Kubernetes pod mapping.")
        else:
            detail = f"{n['mapped']} of {len(n['gpus'])} GPUs carry pod, namespace and container labels."
            if n["honor_renamed"]:
                detail += " Read from exported_pod / exported_namespace (honor_labels=false)."
            b.add(cid, "Workload labels", "pass", detail)

    def _pci_check(self, b, v):
        if not v["pci_scan_supported"]:
            b.add("host.pci", "GPU hardware on the PCI bus", "skip", "The PCI bus is not readable here (/sys/bus/pci).")
            return
        if not v["pci_gpus"]:
            b.add("host.pci", "GPU hardware on the PCI bus", "info", "No display-class PCI device is present on this machine.",
                  "This is normal for a VM or a control-plane host; point GPU Lens at a DCGM exporter or Prometheus instead.")
            return
        by_vendor, unbound = {}, []
        for d in v["pci_gpus"]:
            by_vendor[d["vendor"]] = by_vendor.get(d["vendor"], 0) + 1
            if d["vendor_id"] == "10de" and d["driver"] != "nvidia":
                unbound.append(f"{d['address']} ({d['driver'] or 'no driver'})")
        detail = ", ".join(f"{by_vendor[k]} × {k}" for k in sorted(by_vendor)) + "."
        if v["nvidia_driver_version"]:
            detail += f" NVIDIA kernel driver {v['nvidia_driver_version']} is loaded."
        if unbound:
            b.add("host.pci", "GPU hardware on the PCI bus", "warn", detail + " NVIDIA device not bound to the nvidia driver: " + ", ".join(unbound) + ".",
                  "Install or load the proprietary NVIDIA driver; without it there is no telemetry for these GPUs.")
        elif not by_vendor.get("NVIDIA"):
            b.add("host.pci", "GPU hardware on the PCI bus", "info", detail + " None is an NVIDIA device, so no NVML telemetry is available locally.")
        else:
            b.add("host.pci", "GPU hardware on the PCI bus", "pass", detail)

    def _preflight(self, now):
        b = self._Checks()
        P = self._plural_n
        f = self.merge
        n_rules = len(self.rt.get_rule_definitions(self.thresholds))
        b.group("core", "GPU Lens core")
        b.add("core.api", "API server", "pass", f"Answering — python runtime, version {VERSION}.")
        if not self.overrides:
            b.add("core.rules", "Rule engine", "pass", f"{n_rules} deterministic rules registered with default thresholds.")
        else:
            keys = sorted(self.overrides)
            b.add("core.rules", "Rule engine", "info",
                  f"{n_rules} deterministic rules registered; {P(len(keys), 'live threshold override', 'live threshold overrides')} active: {', '.join(keys)}.",
                  "Reset them under Rules & Thresholds to return to the defaults.")
        if self.guard["ok"]:
            b.add("core.readonly_guard", "Read-only transport guard", "pass",
                  f"{', '.join(self.guard['blocked_methods'])} rejected before any network I/O ({self.guard['network_calls']} network calls during the self-test).")
        else:
            b.add("core.readonly_guard", "Read-only transport guard", "fail", f"Self-test failed: {', '.join(self.guard['leaked_methods'])} were not blocked.",
                  "Do not connect this build to production systems; the read-only guarantee is broken.")
        if not self.running:
            b.add("core.collector", "Collector loop", "skip", "Live collection is not running.", "Press Go Live.")
        elif self.tick == 0:
            b.add("core.collector", "Collector loop", "pending", "First collection cycle in progress…")
        else:
            ms = self.last_tick_dur * 1000.0
            status, hint = "pass", ""
            if self.last_tick_dur > self.interval() + 0.5:
                status, hint = "warn", "Collection takes longer than the refresh interval; choose a longer interval."
            b.add("core.collector", "Collector loop", status, f"Cycle {self.tick} completed in {ms:.0f} ms; refreshing every {self.cfg['interval_seconds']:g}s.", hint, ms)
        b.add("core.clock", "Clock agreement with Prometheus", "skip", "Clock comparison with Prometheus is a Go-runtime check." if self.src[SOURCE_PROM]["status"]["state"] in ("connected", "degraded") else "Needs a connected Prometheus to compare clocks.")

        # host
        b.group("host", "Host running GPU Lens")
        host_st, v = self.src[SOURCE_HOST], self.host_vitals
        if b.gate(host_st, "host.vitals", "Host vitals"):
            lat = host_st["status"]["last_latency_ms"]
            if v is None:
                b.add("host.vitals", "Host vitals", "pending", "Checking…")
            elif not v["supported"]:
                b.add("host.vitals", "Host vitals", "info", f"{v['hostname']} · {v['os']}/{v['arch']} · {P(v['logical_cpus'], 'logical CPU', 'logical CPUs')}.",
                      "Detailed CPU, memory, disk and network vitals are collected on Linux hosts.", lat)
                b.add("host.pressure", "Host resource pressure", "skip", "Not measured on this operating system.")
                b.add("host.pci", "GPU hardware on the PCI bus", "skip", "The PCI scan is implemented for Linux (/sys).")
            else:
                b.add("host.vitals", "Host vitals", "pass",
                      f"{v['hostname']} · {P(v['logical_cpus'], 'logical CPU', 'logical CPUs')} · CPU {fmt_pct(v['cpu_percent'])} · memory {fmt_pct(v['mem_used_percent'])} used.", "", lat)
                pressure = []
                if v["cpu_percent"] is not None and v["cpu_percent"] >= 90:
                    pressure.append(f"CPU at {v['cpu_percent']:.0f}%")
                if v["cpu_iowait_percent"] is not None and v["cpu_iowait_percent"] >= 25:
                    pressure.append(f"I/O wait at {v['cpu_iowait_percent']:.0f}%")
                if v["mem_used_percent"] is not None and v["mem_used_percent"] >= 92:
                    pressure.append(f"memory {v['mem_used_percent']:.0f}% used")
                if v["pressure"]:
                    p = v["pressure"].get("io_some_avg10")
                    if p is not None and p >= 20:
                        pressure.append(f"I/O pressure {p:.0f}%")
                    p = v["pressure"].get("memory_some_avg10")
                    if p is not None and p >= 20:
                        pressure.append(f"memory pressure {p:.0f}%")
                if pressure:
                    b.add("host.pressure", "Host resource pressure", "warn", "High: " + ", ".join(pressure) + ".",
                          "CPU, memory or storage starvation on the host slows data loaders and depresses GPU utilization.")
                else:
                    b.add("host.pressure", "Host resource pressure", "pass", "CPU, memory and I/O are not saturated.")
                self._pci_check(b, v)
        else:
            b.add("host.pressure", "Host resource pressure", "skip", "Host vitals are not being collected.")
            b.add("host.pci", "GPU hardware on the PCI bus", "skip", "Host vitals are not being collected.")

        # nvidia-smi
        b.group("nvidia", "Local NVIDIA driver (nvidia-smi)")
        smi_st = self.src[SOURCE_SMI]
        if b.gate(smi_st, "nvsmi.query", "Driver query"):
            res = smi_st["smi"]
            if res is None:
                b.add("nvsmi.query", "Driver query", "pending", "Checking…")
            else:
                s = smi_st["status"]
                b.add("nvsmi.query", "Driver query", "warn" if s["state"] == "degraded" else "pass",
                      f"{s['detail']} — {res['binary']} ({res['fields_note']}).", s["hint"], s["last_latency_ms"])
                procs = sum(len(x) for x in res["processes"].values())
                busy = sum(1 for x in res["processes"].values() if x)
                if procs == 0:
                    b.add("nvsmi.processes", "Compute processes", "info", "No compute process currently holds a GPU context.")
                else:
                    b.add("nvsmi.processes", "Compute processes", "pass", f"{P(procs, 'compute process', 'compute processes')} on {P(busy, 'GPU', 'GPUs')}.")
                if f["smi_only"] > 0:
                    b.add("nvsmi.coverage", "Hardware-fault signal coverage", "warn",
                          f"{P(f['smi_only'], 'GPU is', 'GPUs are')} read only through nvidia-smi: Xid codes, PCIe replay, NVLink and thermal/power violation counters are not available from it.",
                          "Connect a DCGM exporter for full hardware-health and failure-correlation coverage.")
                elif res["gpus"]:
                    b.add("nvsmi.coverage", "Hardware-fault signal coverage", "pass", "Local GPUs are also covered by DCGM telemetry, so fault counters are available.")

        # amd-smi (experimental)
        b.group("amd", "Local AMD driver (amd-smi, experimental)")
        amd_st = self.src.get(SOURCE_AMD)
        if amd_st is not None and b.gate(amd_st, "amd.query", "Driver query"):
            res = amd_st["amd"]
            if res is None:
                b.add("amd.query", "Driver query", "pending", "Checking…")
            else:
                s = amd_st["status"]
                b.add("amd.query", "Driver query", "warn" if s["state"] == "degraded" else "pass", f"{s['detail']} — {res['binary']}.", s["hint"], s["last_latency_ms"])
                if f["amd"] > 0:
                    b.add("amd.coverage", "Hardware-fault signal coverage", "warn",
                          f"{P(f['amd'], 'AMD GPU is', 'AMD GPUs are')} read through amd-smi: utilization, VRAM, temperature, power and ECC counts are evaluated; NVIDIA-specific signals (Xid, NVLink, row remapping) have no AMD equivalent in GPU Lens yet.",
                          "AMD support is experimental. Please report what you see on real hardware.")

        # dcgm
        b.group("dcgm", "DCGM exporter")
        dst = self.src[SOURCE_DCGM]
        if b.gate(dst, "dcgm.reachable", "Endpoint reachable"):
            n = dst["dcgm_norm"]
            if n is None or dst["dcgm"] is None:
                b.add("dcgm.reachable", "Endpoint reachable", "pending", "Scrape answered; reading series…", "", dst["status"]["last_latency_ms"])
            else:
                b.add("dcgm.reachable", "Endpoint reachable", "pass",
                      f"{dst['status']['endpoint']} answered with {n['series_count']} series.{self._emu(dst['status']['emulated'])}", "", dst["status"]["last_latency_ms"])
                self._metric_checks(b, "dcgm", n)
                nodes = {g["node_name"] for g in n["gpus"]}
                if not n["gpus"]:
                    b.add("dcgm.gpus", "GPU devices", "warn", "No series carries a GPU UUID label.", "Confirm this is a dcgm-exporter endpoint and that the exporter can see GPUs.")
                else:
                    detail = f"{P(len(n['gpus']), 'GPU', 'GPUs')} on {P(len(nodes), 'node', 'nodes')}."
                    if n["mig"] > 0:
                        detail += f" {P(n['mig'], 'MIG instance', 'MIG instances')} shown as separate devices."
                    b.add("dcgm.gpus", "GPU devices", "pass", detail)
                self._attribution_check(b, "dcgm.attribution", n)

        # prometheus
        b.group("prometheus", "Prometheus")
        pst = self.src[SOURCE_PROM]
        if b.gate(pst, "prom.reachable", "API reachable"):
            res = pst["prom"]
            if res is None:
                b.add("prom.reachable", "API reachable", "pending", "Checking…")
            else:
                n = res["norm"]
                ver = "version " + res["version"] if res["version"] else "version not reported"
                b.add("prom.reachable", "API reachable", "pass", f"{pst['status']['endpoint']} answered ({ver}).{self._emu(pst['status']['emulated'])}", "", pst["status"]["last_latency_ms"])
                if not n["gpus"]:
                    b.add("prom.series", "DCGM series", "warn", "Connected, but no DCGM GPU series were found.",
                          "Make sure Prometheus scrapes dcgm-exporter (ServiceMonitor or scrape job on port 9400).")
                else:
                    nodes = {g["node_name"] for g in n["gpus"]}
                    b.add("prom.series", "DCGM series", "pass", f"{P(len(n['gpus']), 'GPU', 'GPUs')} on {P(len(nodes), 'node', 'nodes')} visible.")
                    self._metric_checks(b, "prom", n)
                    self._attribution_check(b, "prom.attribution", n)
                b.add("prom.targets", "GPU scrape targets", "skip", "The scrape-target audit is a Go-runtime check.")
                if n["gpus"]:
                    oldest = max((now - parse_iso(g["last_sample_at"])).total_seconds() for g in n["gpus"])
                    status, hint = "pass", ""
                    if oldest > self.thresholds["max_staleness_seconds"]:
                        status, hint = "fail", "Samples are older than the staleness limit; check the exporter and the scrape job."
                    elif oldest > 90:
                        status, hint = "warn", "Samples are getting old; check the scrape interval."
                    b.add("prom.freshness", "Sample freshness", status, f"Oldest GPU sample is {oldest:.0f}s old, measured from evaluation time (sample timestamps unavailable).", hint)
                b.add("prom.backfill", "History backfill", "info", "History backfill is a Go-runtime feature; the window builds from live samples instead.")

        # kubernetes
        b.group("kubernetes", "Kubernetes API")
        kst = self.src[SOURCE_K8S]
        if b.gate(kst, "k8s.reachable", "API reachable"):
            res = kst["k8s"]
            if res is None:
                b.add("k8s.reachable", "API reachable", "pending", "Checking…")
            else:
                b.add("k8s.reachable", "API reachable", "pass", f"{kst['status']['endpoint']} answered ({or_dash(res['version'])}).{self._emu(res['emulated'])}", "", kst["status"]["last_latency_ms"])
                for a in res["access"]:
                    cid, title = "k8s." + a["resource"], "List " + a["resource"]
                    if not a["ok"]:
                        status = "fail"
                        hint = f"Grant get/list on {a['resource']} (deploy/kubernetes/rbac-readonly.yaml)."
                        if a["resource"] == "events":
                            status = "warn"
                            hint += " Without events the scheduling rule loses FailedScheduling detail."
                        detail = f"Denied with HTTP {a['status']}." if a["status"] else "Request failed: " + (a.get("error") or "unknown")
                        b.add(cid, title, status, detail, hint)
                        continue
                    detail = ""
                    if a["resource"] == "nodes":
                        gpus = sum(nn["gpu_capacity"] for nn in res["nodes"])
                        detail = f"{P(res['total_nodes'], 'node', 'nodes')}, {res['gpu_nodes']} advertising nvidia.com/gpu ({P(gpus, 'GPU', 'GPUs')})."
                    elif a["resource"] == "pods":
                        detail = f"{P(res['total_pods_seen'], 'pod', 'pods')} scanned, {len(res['pods'])} requesting GPUs."
                    elif a["resource"] == "events":
                        detail = f"{P(len(res['events']), 'warning event', 'warning events')} in the event window."
                    if a["truncated"]:
                        detail += " List was truncated at the page limit."
                    b.add(cid, title, "pass", detail)
                http = self.http.get(SOURCE_K8S)
                allowed, blocked = (http.allowed, http.blocked) if http is not None else (0, 0)
                b.add("k8s.readonly", "Read-only access", "pass",
                      f"{allowed} GET requests sent, {blocked} mutating requests attempted. GPU Lens never creates, patches or deletes cluster objects.")
                if any(a["resource"] == "nodes" and a["ok"] for a in res["access"]):
                    if res["gpu_nodes"] == 0:
                        b.add("k8s.device_plugin", "NVIDIA device plugin", "warn", "No node advertises nvidia.com/gpu capacity.",
                              "Install the NVIDIA device plugin or GPU Operator, or check that GPU nodes have joined.")
                    elif res["other_resources"]:
                        b.add("k8s.device_plugin", "NVIDIA device plugin", "info",
                              f"{res['gpu_nodes']} GPU node(s). Also present but not modeled: {', '.join(res['other_resources'])}.",
                              "MIG slices and shared-GPU resources are outside this release's capacity math.")
                    else:
                        b.add("k8s.device_plugin", "NVIDIA device plugin", "pass", f"{P(res['gpu_nodes'], 'node is', 'nodes are')} advertising nvidia.com/gpu.")

        # correlation
        b.group("correlation", "Cross-source correlation")
        if self.tick == 0:
            b.add("corr.telemetry", "GPU telemetry coverage", "pending", "Waiting for the first collection cycle.")
        elif f["total"] == 0:
            b.add("corr.telemetry", "GPU telemetry coverage", "fail", "No GPU is reported by any connected source.",
                  "Install the NVIDIA driver on this machine, start a DCGM exporter, or connect Prometheus under Connections. To try the pipeline without GPU hardware, run `python3 scripts/live_lab.py`.")
        else:
            detail = f"{P(f['total'], 'GPU', 'GPUs')} observed, {f['fresh']} fresh."
            status, hint = "pass", ""
            if f["fresh"] < f["total"]:
                status, hint = "warn", "Stale GPUs are excluded from health verdicts and raise a data-freshness incident."
            if f["emulated"] > 0:
                detail += f" {f['emulated']} come from the Live Lab emulator (synthetic)."
            b.add("corr.telemetry", "GPU telemetry coverage", status, detail, hint)
        if self.k8s_nodes_ok and f["total"] > 0:
            if f["unmatched_nodes"]:
                names = sorted(f["unmatched_nodes"])
                b.add("corr.nodes", "Telemetry ↔ Kubernetes node names", "warn",
                      f"GPU series name {P(len(names), 'a node', 'nodes')} unknown to Kubernetes: {', '.join(names)}.",
                      "Set NODE_NAME on the dcgm-exporter pods or relabel a `node` label so telemetry and node inventory can be joined.")
            else:
                b.add("corr.nodes", "Telemetry ↔ Kubernetes node names", "pass", "Every cluster GPU series maps to a known Kubernetes node.")
        else:
            b.add("corr.nodes", "Telemetry ↔ Kubernetes node names", "skip", "Needs both a Kubernetes connection and cluster GPU telemetry.")
        if self.k8s_pods_ok and f["total"] > 0:
            problems = list(f["gaps"])
            if f["unknown_pod_gpus"] > 0:
                problems.append(f"{P(f['unknown_pod_gpus'], 'GPU', 'GPUs')} carry pod labels for pods the API does not list")
            if problems:
                b.add("corr.attribution", "Pod ↔ GPU attribution", "warn", "; ".join(problems) + ".",
                      "Run dcgm-exporter with Kubernetes pod-resources mapping enabled so each allocated GPU carries pod labels.")
            else:
                b.add("corr.attribution", "Pod ↔ GPU attribution", "pass",
                      f"{f['mapped']} of {f['total']} GPUs are attributed to workloads; Kubernetes reports {f['k8s_allocated']} GPUs allocated.")
        elif f["total"] > 0 and f["host_workloads"] > 0:
            b.add("corr.attribution", "Pod ↔ GPU attribution", "info",
                  f"No Kubernetes on this host: {P(f['mapped'], 'GPU is', 'GPUs are')} attributed to compute processes instead of pods.")
        elif f["total"] > 0 and f["inferred_pods"] > 0:
            b.add("corr.attribution", "Pod ↔ GPU attribution", "info",
                  f"{P(f['inferred_pods'], 'workload', 'workloads')} inferred from DCGM pod labels; pod phase, restarts and scheduling state need the Kubernetes API.",
                  "Connect the Kubernetes API for scheduling and failure-correlation rules.")
        else:
            b.add("corr.attribution", "Pod ↔ GPU attribution", "skip", "Needs GPU telemetry with workload labels or a Kubernetes connection.")

        summary = {k: 0 for k in ("pass", "warn", "fail", "skip", "info", "pending")}
        for g in b.groups:
            for c in g["checks"]:
                summary[c["status"]] += 1
        if not self.running and self.tick == 0:
            verdict, text = "idle", "Press Go Live to check every source."
        elif self.tick == 0 or summary["pending"] > 0:
            verdict, text = "checking", "Checking every source…"
        elif f["total"] == 0 and not self.k8s_nodes_ok:
            verdict, text = "blocked", "No GPU telemetry source is reachable from this machine. The hints below show how to connect one."
        elif summary["fail"] > 0 or summary["warn"] > 0:
            verdict, text = "degraded", f"Live with caveats: {summary['fail']} failing and {summary['warn']} warning check(s)."
        else:
            verdict, text = "ready", f"All checks passed — {P(f['total'], 'GPU', 'GPUs')} monitored live."
        return {"generated_at": iso(now), "verdict": verdict, "verdict_text": text, "summary": summary, "groups": b.groups}

    # ----- API payloads -----
    def _status(self, now, access):
        enabled = self.cfg["enabled"]
        st = {"runtime": RUNTIME, "version": VERSION, "supported": enabled, "unsupported_reason": "" if enabled else
              "the live console is disabled on this GPU Lens instance (GPULENS_LIVE_ENABLED=false)",
              "state": "running" if self.running else "idle", "state_reason": self.state_reason,
              "started_at": iso(self.started_at) if self.started_at else None, "tick": self.tick, "interval_seconds": self.cfg["interval_seconds"],
              "last_tick_at": iso(self.last_tick_at) if self.last_tick_at else None, "last_tick_duration_ms": round(self.last_tick_dur * 1000.0, 3),
              "server_time": iso(now), "config_mutable": bool(access.get("config_mutable")) and enabled,
              "config_mutable_reason": access.get("reason", ""), "auto_start": self.cfg["auto_start"], "hostname": self.hostname,
              "role": access.get("role") or "admin", "auth_enabled": bool(access.get("auth_enabled")), "user": access.get("user", ""),
              "can_operate": bool(access.get("can_operate", True)),
              "emulated": any(s["status"]["emulated"] and s["status"]["state"] in ("connected", "degraded") for s in self.src.values())}
        return st

    def status(self, access):
        with self.lock:
            return self._status(utcnow(), access)

    def state(self, history_since=0, activity_since=0, access=None):
        access = access or {}
        with self.lock:
            now = utcnow()
            self.last_poll = time.time()
            out = {"status": self._status(now, access), "config": config_view(self.cfg), "overview": self.overview, "preflight": self._preflight(now),
                   "sources": [], "host": self.host_vitals, "gpus": self.gpu_views, "processes": self.processes, "nodes": self.view_nodes,
                   "pods": self.view_pods, "events": [], "activity_last_seq": self.activity_seq, "resolved_incidents": [],
                   "thresholds": dict(self.thresholds), "threshold_defaults": dict(self.base_thresholds), "rules": self.rt.get_rule_definitions(self.thresholds),
                   "notes": [], "alerts": alert_status(self.alerts), "persistence": self._persistence_status()}
            for sid in (SOURCE_SMI, SOURCE_AMD, SOURCE_DCGM, SOURCE_PROM, SOURCE_K8S, SOURCE_HOST):
                st = self.src.get(sid)
                if st:
                    s = dict(st["status"])
                    s["facts"], s["latency_history_ms"] = list(s["facts"]), list(s["latency_history_ms"])
                    out["sources"].append(s)
            out["events"] = list(reversed(self.k8s_events[-200:]))
            pts = [p for p in self.history if p["seq"] > history_since]
            out["history"] = {"epoch": self.history_epoch, "last_seq": self.seq, "first_seq": self.history[0]["seq"] if self.history else 0,
                              "capacity": self.cfg["history_points"], "points": pts}
            out["activity"] = [a for a in self.activity if a["seq"] > activity_since]
            out["resolved_incidents"] = list(reversed(self.resolved))
            out["threshold_overrides"] = sorted(self.overrides)
            snap = self.snapshot
            if snap is not None:
                out["sources_detail"] = {"data_mode": snap["data_mode"], "scenario_id": snap["scenario_id"], "collected_at": snap["collected_at"],
                                         "sources": snap["sources"], "exported_metrics": snap["exported_metrics"],
                                         "metric_mappings": dict(getattr(self.rt, "DEFAULT_METRIC_MAPPINGS", {}))}
            else:
                out["sources_detail"] = None
            if out["status"]["emulated"]:
                out["notes"].append("At least one connected source is the GPU Lens Live Lab emulator. Its GPUs, pods and events are synthetic; the collection, merge and rule evaluation you see are real.")
            if self.merge["host_workloads"] > 0:
                out["notes"].append("No Kubernetes pods are involved on this host: each busy GPU is attributed to the compute process holding most of its memory.")
            if self.overrides:
                out["notes"].append("Live rule thresholds differ from the configured defaults. The simulated demo is not affected.")
            return json.loads(json.dumps(out, default=str))


    # ----- alerting, audit, durable state, fleet summary, self-metrics (mirrors internal/live/extras.go) -----
    def set_alerter(self, notifier):
        with self.lock:
            self.alerts = notifier

    def alert_status(self):
        return alert_status(self.alerts)

    def test_alerts(self):
        with self.lock:
            n = self.alerts
            meta = {"cluster": self.cfg["cluster_name"] or ("live@" + self.hostname), "data_mode": "live", "version": VERSION}
            if self.snapshot:
                meta["cluster"], meta["data_mode"] = self.snapshot["cluster_name"], self.snapshot["data_mode"]
        if n is None:
            raise LiveError("no alert destination is configured: start GPU Lens with --alert-slack-webhook and/or --alert-webhook-url", 409)
        res = n.send_test(meta)
        ok = sum(1 for r in res if r["ok"])
        with self.lock:
            self._add_activity(utcnow(), "success" if ok == len(res) else "warning", "alert", "Test alert sent", f"{ok} of {len(res)} destination(s) accepted it.")
        return res

    def add_audit(self, title, detail):
        with self.lock:
            self._add_activity(utcnow(), "info", "audit", title, detail)

    def is_running(self):
        with self.lock:
            return self.running

    def summary(self):
        with self.lock:
            now = utcnow()
            pf = self._preflight(now)
            st = self._status(now, {})
            out = {"schema": "gpulens.summary/v1", "cluster": self.cfg["cluster_name"] or ("live@" + self.hostname), "hostname": self.hostname,
                   "version": VERSION, "runtime": RUNTIME, "state": st["state"], "verdict": pf["verdict"], "verdict_text": pf["verdict_text"],
                   "fleet_status": "", "data_mode": "live", "emulated": st["emulated"], "gpus_total": 0, "gpus_fresh": 0, "nodes": len(self.view_nodes),
                   "incidents": {"critical": 0, "warning": 0, "info": 0}, "top_incidents": [], "sources_connected": 0, "sources_enabled": 0,
                   "last_cycle_at": st["last_tick_at"], "generated_at": iso(now)}
            if self.snapshot:
                out["data_mode"] = self.snapshot["data_mode"]
                out["cluster"] = self.snapshot["cluster_name"] or out["cluster"]
            if self.overview:
                out["fleet_status"] = self.overview["fleet_health_status"]
                for inc in self.overview["active_incidents"]:
                    out["incidents"][inc["severity"]] = out["incidents"].get(inc["severity"], 0) + 1
                    if len(out["top_incidents"]) < 3:
                        out["top_incidents"].append(f"[{inc['severity']}] {inc['title']}")
            for g in self.gpu_views:
                out["gpus_total"] += 1
                out["gpus_fresh"] += 0 if g.get("is_stale") else 1
            for sid, src in self.src.items():
                s = src["status"]
                if sid == SOURCE_HOST or not s["enabled"]:
                    continue
                if not (src["ever"] or (s["explicit"] and sid not in LOCAL_DRIVER_SOURCES) or s["state"] == "error"):
                    continue
                out["sources_enabled"] += 1
                out["sources_connected"] += 1 if s["state"] in ("connected", "degraded") else 0
            return json.loads(json.dumps(out, default=str))

    def metrics_snapshot(self):
        with self.lock:
            open_by = {"critical": 0, "warning": 0, "info": 0}
            for inc in self.open.values():
                open_by[inc["severity"]] = open_by.get(inc["severity"], 0) + 1
            return {"enabled": self.cfg["enabled"], "running": self.running, "tick": self.tick, "last_tick_at": self.last_tick_at,
                    "last_tick_dur": self.last_tick_dur, "interval": self.cfg["interval_seconds"],
                    "sources": [{"id": sid, "enabled": st["status"]["enabled"], "state": st["status"]["state"],
                                 "up": st["status"]["state"] in ("connected", "degraded"), "latency_ms": st["status"]["last_latency_ms"] or 0.0}
                                for sid, st in sorted(self.src.items())],
                    "gpus_fresh": sum(1 for g in self.gpu_views if not g.get("is_stale")), "gpus_stale": sum(1 for g in self.gpu_views if g.get("is_stale")),
                    "open": open_by, "resolved": len(self.resolved), "overrides": len(self.overrides), "guard_ok": bool(self.guard["ok"]),
                    "alert_counters": self.alerts.counters() if self.alerts else {}, "persistence": bool(self.state_path),
                    "state_saved_at": self.state_saved_at, "state_err": self.state_err}

    def _persistence_status(self):
        enabled = bool(self.state_path)
        return {"enabled": enabled, "path": self.state_path, "loaded_at": iso(self.state_loaded_at) if self.state_loaded_at else None,
                "saved_at": iso(self.state_saved_at) if self.state_saved_at else None, "last_error": self.state_err,
                "note": ("Threshold overrides, the resolved-incident log, open-incident ages and the alert record survive restarts. Single-node file; not shared between replicas."
                         if enabled else "Live state is kept in memory only. Start with --live-state-file to keep thresholds, resolved incidents and alert history across restarts.")}

    def set_state_file(self, path):
        with self.lock:
            self.state_path = path or ""
            if not path or not os.path.exists(path):
                return
            try:
                with open(path, "r", encoding="utf-8") as f:
                    doc = json.load(f)
                if doc.get("format") != STATE_FORMAT:
                    raise ValueError("format")
            except Exception:
                self.state_err = "the live state file is not in a format this version understands; starting fresh"
                raise LiveError(self.state_err)
            ov = doc.get("threshold_overrides") or {}
            if ov:
                try:
                    self.set_thresholds(ov, reset=True)
                    self.activity, self.activity_seq = [], 0
                except LiveError:
                    pass
            self.restored_resolved = list(doc.get("resolved_incidents") or [])[-MAX_RESOLVED:]
            self.resolved = list(self.restored_resolved)
            self.restored_first_seen = dict(doc.get("open_first_seen") or {})
            if self.alerts is not None:
                self.alerts.restore(doc.get("alerts") or {})
            self.state_loaded_at = utcnow()

    def save_state(self):
        with self.lock:
            path = self.state_path
            if not path:
                return
            first_seen = {fp: inc.get("first_seen_at") for fp, inc in self.open.items()}
            for fp, t in self.restored_first_seen.items():
                first_seen.setdefault(fp, t)
            doc = {"format": STATE_FORMAT, "saved_at": iso(utcnow()), "gpulens_version": VERSION, "threshold_overrides": dict(self.overrides),
                   "resolved_incidents": copy.deepcopy(self.resolved), "open_first_seen": first_seen,
                   "alerts": self.alerts.snapshot() if self.alerts else {}}
        try:
            d = os.path.dirname(os.path.abspath(path))
            os.makedirs(d, exist_ok=True)
            tmp = os.path.join(d, f".gpulens-live-state-{os.getpid()}.tmp")
            fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                json.dump(doc, f, indent=2, default=str)
            os.replace(tmp, path)
            with self.lock:
                self.state_err, self.state_saved_at = "", utcnow()
        except OSError as e:
            with self.lock:
                self.state_err = sanitize_error(e)


STATE_FORMAT = "gpulens.live-state/v1"


# ---------------------------------------------------------------------------------------------
# Alerting (mirrors internal/alert): Slack + generic JSON webhook on incident open / resolve / severity change
# ---------------------------------------------------------------------------------------------
ALERT_SCHEMA = "gpulens.alert/v1"
_SEV_RANK = {"info": 1, "warning": 2, "critical": 3}


def _check_alert_url(what, raw, https_only):
    u = urllib.parse.urlparse((raw or "").strip())
    if not u.netloc:
        raise LiveError(f"alert: {what} is not a valid URL")
    if u.scheme != "https" and (https_only or u.scheme != "http"):
        raise LiveError(f"alert: {what} must use https" if https_only else f"alert: {what} must use http or https")
    if u.username or u.password:
        raise LiveError(f"alert: {what} must not embed credentials; use the signing secret instead")


def redact_url(raw):
    u = urllib.parse.urlparse(raw)
    host = u.hostname or ""
    if u.port:
        host += f":{u.port}"
    return f"{u.scheme}://{host}" + ("" if u.path in ("", "/") else "/…")


def slack_message(ev):
    esc = lambda t: (t or "").replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    prefix = "[EMULATED] " if ev.get("emulated") else ""
    cluster = ev.get("cluster") or "GPU Lens"
    emoji = {"critical": "🔴", "warning": "🟠"}
    if ev["type"] == "test":
        headline, body = "🧪 GPU Lens test alert", ev.get("message", "")
    else:
        inc = ev.get("incident") or {}
        sev = (inc.get("severity") or "").upper()
        if ev["type"] == "incident.opened":
            headline = f"{emoji.get(inc.get('severity'), '🔵')} Incident opened · {sev}"
        elif ev["type"] == "incident.resolved":
            d = ev.get("duration_seconds") or 0
            headline = "✅ Incident resolved" + ((" after " + (f"{d:.0f}s" if d < 90 else (f"{d / 60:.0f} min" if d < 5400 else f"{d / 3600:.1f} h"))) if d > 0 else "")
        else:
            headline = f"{emoji.get(inc.get('severity'), '🔵')} Severity changed · {(ev.get('previous_severity') or '').upper()} → {sev}"
        body = "*" + esc(inc.get("title")) + "*"
        summary = inc.get("summary") or ""
        if summary:
            body += "\n" + esc(summary if len(summary) <= 600 else summary[:599] + "…")
    bits = ["cluster `" + esc(cluster) + "`"]
    if ev.get("incident") and ev["incident"].get("rule_id"):
        bits.insert(0, "rule `" + esc(ev["incident"]["rule_id"]) + "`")
    if ev.get("emulated"):
        bits.append("*EMULATED data (Live Lab), not real hardware*")
    if ev.get("console_url"):
        bits.append("<" + ev["console_url"] + "|Open in GPU Lens>")
    return {"text": f"{prefix}{headline} on {cluster}",
            "blocks": [{"type": "section", "text": {"type": "mrkdwn", "text": "*" + esc(prefix + headline) + "*\n" + body}},
                       {"type": "context", "elements": [{"type": "mrkdwn", "text": " · ".join(bits)}]}]}


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class AlertNotifier:
    def __init__(self, slack_url="", webhook_url="", secret="", min_severity="warning", public_url=""):
        import queue
        min_severity = (min_severity or "warning").strip().lower()
        if min_severity not in _SEV_RANK:
            raise LiveError(f"alert: min severity must be info, warning or critical, got {min_severity!r}")
        if slack_url:
            _check_alert_url("Slack webhook", slack_url, True)
        if webhook_url:
            _check_alert_url("alert webhook", webhook_url, False)
        if public_url:
            _check_alert_url("public URL", public_url, False)
        self.min_severity, self.public_url = min_severity, (public_url or "").strip().rstrip("/")
        self.dests = []
        for kind, url in (("slack", slack_url), ("webhook", webhook_url)):
            if url:
                self.dests.append({"kind": kind, "url": url.strip(), "secret": secret if kind == "webhook" else "",
                                   "status": {"kind": kind, "target": redact_url(url.strip()), "signed": bool(secret) and kind == "webhook", "sent": 0, "failed": 0,
                                              "dropped": 0, "last_event": "", "last_attempt_at": None, "last_success_at": None, "last_error": ""}})
        self.lock = threading.Lock()
        self.known, self.open_refs = {}, {}
        self.q = queue.Queue(maxsize=256)
        self.opener = urllib.request.build_opener(_NoRedirect())
        threading.Thread(target=self._worker, daemon=True).start()

    @staticmethod
    def create(slack_url, webhook_url, secret, min_severity, public_url):
        if not (slack_url or webhook_url):
            for v in (min_severity,):
                if (v or "warning").strip().lower() not in _SEV_RANK:
                    raise LiveError(f"alert: min severity must be info, warning or critical, got {v!r}")
            return None
        return AlertNotifier(slack_url, webhook_url, secret, min_severity, public_url)

    def _event(self, kind, meta, ref):
        ev = {"schema": ALERT_SCHEMA, "type": kind, "delivery_id": os.urandom(12).hex(), "timestamp": iso(utcnow()), "cluster": meta.get("cluster", ""),
              "data_mode": meta.get("data_mode", ""), "emulated": meta.get("data_mode") == "live_emulated", "gpulens_version": meta.get("version", VERSION)}
        if ref is not None:
            ev["incident"] = ref
        if self.public_url:
            ev["console_url"] = self.public_url + "/?live_tab=incidents"
        return ev

    @staticmethod
    def _ref(inc):
        return {k: inc.get(k) for k in ("id", "fingerprint", "rule_id", "rule_name", "category", "severity", "eval_status", "evidence_strength",
                                        "title", "summary", "affected_resources", "first_seen_at")}

    def _enqueue(self, ev):
        import queue
        for d in self.dests:
            try:
                self.q.put_nowait((d, ev))
            except queue.Full:
                d["status"]["dropped"] += 1

    def sync(self, incidents, meta, allow_resolve):
        floor = _SEV_RANK[self.min_severity]
        with self.lock:
            seen = set()
            for inc in incidents:
                fp = inc.get("fingerprint") or inc.get("id")
                seen.add(fp)
                sev, ref = inc["severity"], self._ref(inc)
                prev = self.known.get(fp)
                if prev is None:
                    t = {"severity": sev, "notified": _SEV_RANK.get(sev, 0) >= floor}
                    if t["notified"]:
                        self._enqueue(self._event("incident.opened", meta, ref))
                    self.known[fp] = t
                elif prev["severity"] != sev:
                    t = {"severity": sev, "notified": prev["notified"]}
                    if prev["notified"]:
                        ev = self._event("incident.severity_changed", meta, ref)
                        ev["previous_severity"] = prev["severity"]
                        self._enqueue(ev)
                    elif _SEV_RANK.get(sev, 0) >= floor:
                        t["notified"] = True
                        self._enqueue(self._event("incident.opened", meta, ref))
                    self.known[fp] = t
                self.open_refs[fp] = ref
            if not allow_resolve:
                return
            for fp in sorted(set(self.known) - seen):
                t, ref = self.known.pop(fp), self.open_refs.pop(fp, None)
                if t["notified"] and ref is not None:
                    ev = self._event("incident.resolved", meta, ref)
                    first = parse_iso(ref.get("first_seen_at"))
                    if first:
                        ev["duration_seconds"] = max(0.0, (utcnow() - first).total_seconds())
                    self._enqueue(ev)

    def _post(self, d, ev, timeout):
        import hmac, hashlib
        body = json.dumps(slack_message(ev) if d["kind"] == "slack" else ev).encode("utf-8")
        req = urllib.request.Request(d["url"], data=body, method="POST", headers={"Content-Type": "application/json", "User-Agent": "gpu-lens/" + VERSION})
        if d["kind"] == "webhook":
            req.add_header("X-GPULens-Event", ev["type"])
            req.add_header("X-GPULens-Delivery", ev["delivery_id"])
            if d["secret"]:
                req.add_header("X-GPULens-Signature-256", "sha256=" + hmac.new(d["secret"].encode(), body, hashlib.sha256).hexdigest())
        try:
            with self.opener.open(req, timeout=timeout) as resp:
                resp.read(65536)
                if not 200 <= resp.status <= 299:
                    return f"HTTP {resp.status}"
        except urllib.error.HTTPError as e:
            return f"HTTP {e.code}"
        except urllib.error.URLError as e:
            return "timed out" if isinstance(e.reason, socket.timeout) else "request failed: " + sanitize_error(e.reason)
        except (socket.timeout, TimeoutError):
            return "timed out"
        except Exception as e:
            return "request failed: " + sanitize_error(e)
        return ""

    def _record(self, d, kind, err):
        with self.lock:
            st, now = d["status"], iso(utcnow())
            st["last_attempt_at"], st["last_event"] = now, kind
            if err:
                st["failed"] += 1
                st["last_error"] = err
            else:
                st["sent"] += 1
                st["last_success_at"], st["last_error"] = now, ""

    def _worker(self):
        while True:
            d, ev = self.q.get()
            err, delay = "", 1.0
            for attempt in range(3):
                err = self._post(d, ev, 10.0)
                if not err or attempt == 2:
                    break
                time.sleep(delay)
                delay *= 4
            self._record(d, ev["type"], err)

    def send_test(self, meta):
        ev = self._event("test", meta, None)
        ev["message"] = "Test alert from GPU Lens. If you can read this, alert delivery works."
        out = []
        for d in self.dests:
            err = self._post(d, ev, 5.0)
            self._record(d, "test", err)
            out.append({"kind": d["kind"], "target": d["status"]["target"], "ok": not err, "error": err})
        return out

    def status(self):
        with self.lock:
            return {"enabled": True, "min_severity": self.min_severity, "public_url": self.public_url, "tracked_incidents": len(self.known),
                    "destinations": [dict(d["status"]) for d in self.dests],
                    "note": "Alerts are sent when a live incident at or above the minimum severity opens, changes severity or resolves. Destinations are set at startup only."}

    def counters(self):
        with self.lock:
            return {d["kind"]: (d["status"]["sent"], d["status"]["failed"], d["status"]["dropped"]) for d in self.dests}

    def snapshot(self):
        with self.lock:
            return {fp: {"severity": t["severity"], "notified": t["notified"], "incident": self.open_refs.get(fp, {})} for fp, t in self.known.items()}

    def restore(self, rec):
        with self.lock:
            for fp, p in (rec or {}).items():
                self.known[fp] = {"severity": p.get("severity", ""), "notified": bool(p.get("notified"))}
                self.open_refs[fp] = p.get("incident") or {}


def alert_status(n):
    if n is None:
        return {"enabled": False, "min_severity": "warning", "public_url": "", "tracked_incidents": 0, "destinations": [],
                "note": "No alert destination is configured. Start GPU Lens with --alert-slack-webhook and/or --alert-webhook-url (or GPULENS_ALERT_SLACK_WEBHOOK_URL / GPULENS_ALERT_WEBHOOK_URL)."}
    return n.status()


# ---------------------------------------------------------------------------------------------
# HTTP surface (/api/v1/live/*) — same guards as internal/api/live_handlers.go
# ---------------------------------------------------------------------------------------------
LIVE_PREFIX = "/api/v1/live/"
LOCAL_ONLY_REASON = ("Connection settings can only be changed from the machine running GPU Lens (open it at http://localhost there), "
                     "or with GPULENS_* environment variables at startup. Start with --live-allow-remote-config to lift this.")


def is_live_path(path):
    return path.startswith(LIVE_PREFIX)


def _host_only(hostport):
    hostport = (hostport or "").strip()
    if hostport.startswith("["):
        return hostport[1:].split("]")[0]
    if hostport.count(":") == 1:
        return hostport.split(":")[0]
    return hostport


def _is_ip(s):
    import ipaddress
    try:
        ipaddress.ip_address(s)
        return True
    except ValueError:
        return False


def _is_loopback(addr):
    import ipaddress
    try:
        ip = ipaddress.ip_address(_host_only(addr).split("%")[0])
    except ValueError:
        return False
    if getattr(ip, "ipv4_mapped", None):
        ip = ip.ipv4_mapped
    return ip.is_loopback


def _forwarded(headers):
    return bool(headers.get("X-Forwarded-For") or headers.get("Forwarded") or headers.get("X-Real-IP"))


def _host_is_local(headers):
    host = _host_only(headers.get("Host", "")).lower()
    if host == "localhost" or host.endswith(".localhost") or _is_ip(host):
        return True
    return bool(host) and host == socket.gethostname().lower()


def _same_origin(headers):
    origin = headers.get("Origin")
    if origin:
        u = urllib.parse.urlparse(origin)
        return bool(u.netloc) and u.netloc.lower() == (headers.get("Host") or "").lower()
    return headers.get("Sec-Fetch-Site", "") in ("", "same-origin", "none")


# ---------------------------------------------------------------------------------------------
# Audit log (mirrors internal/audit): who changed what, including refused attempts
# ---------------------------------------------------------------------------------------------
class AuditLog:
    MAX = 1000

    def __init__(self, path=""):
        self.path, self.entries, self.seq, self.lock = path or "", [], 0, threading.Lock()
        if not self.path:
            return
        if os.path.exists(self.path):
            with open(self.path, "r", encoding="utf-8") as f:
                for line in f:
                    try:
                        e = json.loads(line)
                    except ValueError:
                        continue
                    if isinstance(e, dict) and int(e.get("seq") or 0) > 0:
                        self.entries.append(e)
                        self.seq = max(self.seq, int(e["seq"]))
            self.entries = self.entries[-self.MAX:]
        else:
            os.close(os.open(self.path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600))

    def record(self, actor, role, remote, action, detail, outcome="ok"):
        with self.lock:
            self.seq += 1
            e = {"seq": self.seq, "t": iso(utcnow()), "actor": actor, "role": role, "remote": remote, "action": action, "detail": detail, "outcome": outcome}
            self.entries = (self.entries + [e])[-self.MAX:]
            if self.path:
                try:
                    fd = os.open(self.path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
                    with os.fdopen(fd, "a", encoding="utf-8") as f:
                        f.write(json.dumps(e) + "\n")
                except OSError:
                    pass
            return e

    def list(self, since=0):
        with self.lock:
            return [e for e in self.entries if e["seq"] > since]


VIEWER_REASON = "You are signed in with the viewer role, which is read-only. Sign in as an admin to change connections, thresholds or collection."
VIEWER_DENIED = "your account has the viewer role, which is read-only: ask an admin to make this change"


def remote_label(remote_addr, headers):
    xff = (headers.get("X-Forwarded-For") or "").split(",")[0].strip()
    return f"{remote_addr} (X-Forwarded-For {xff})" if xff and _is_ip(xff) else remote_addr


def describe_patch(doc):
    parts = []
    if doc.get("cluster_name") is not None:
        parts.append(f'cluster name="{doc["cluster_name"]}"')
    if doc.get("interval_seconds") is not None:
        parts.append(f"interval={float(doc['interval_seconds']):g}s")
    for k, label in (("nvidia_smi_enabled", "nvidia-smi"), ("amd_smi_enabled", "amd-smi"), ("host_vitals_enabled", "host vitals")):
        if doc.get(k) is not None:
            parts.append(f"{label}={str(doc[k]).lower()}")
    for key, name in (("dcgm_exporter", "DCGM"), ("prometheus", "Prometheus"), ("kubernetes", "Kubernetes")):
        p = doc.get(key)
        if not isinstance(p, dict):
            continue
        bits = []
        if p.get("enabled") is not None:
            bits.append(f"enabled={str(p['enabled']).lower()}")
        if p.get("endpoint") is not None:
            ep = str(p["endpoint"]).strip()
            u = urllib.parse.urlparse(ep)
            if u.username or u.password:
                ep = u._replace(netloc=u.hostname + (f":{u.port}" if u.port else "")).geturl()
            bits.append("endpoint=" + (ep or "(default probe)"))
        if isinstance(p.get("bearer_token"), str) and p["bearer_token"].strip():
            bits.append("token set")
        if p.get("clear_token"):
            bits.append("token cleared")
        if bits:
            parts.append(f"{name} ({', '.join(bits)})")
    return "; ".join(parts) or "no changes"


class LiveAPI:
    """Routes and guards for the live endpoints. `handle` returns (http_status, json_payload)."""

    def __init__(self, engine, audit=None, auth_enabled=False):
        self.engine = engine
        self.audit = audit or AuditLog()
        self.auth_enabled = auth_enabled

    def access(self, remote_addr, headers, principal=None):
        user, role = principal or ("anonymous", "admin")
        base = {"role": role, "auth_enabled": self.auth_enabled, "user": user if self.auth_enabled else "", "can_operate": role == "admin",
                "config_mutable": False, "reason": ""}
        if role != "admin":
            base["reason"] = VIEWER_REASON
            return base
        if self.engine.allow_remote_config() or (_is_loopback(remote_addr) and not _forwarded(headers) and _host_is_local(headers)):
            base["config_mutable"] = True
            return base
        base["reason"] = LOCAL_ONLY_REASON
        return base

    def _record(self, principal, remote, action, detail, outcome="ok"):
        user, role = principal or ("anonymous", "admin")
        return self.audit.record(user, role, remote, action, detail, outcome)

    def _done(self, principal, remote, action, title, detail):
        e = self._record(principal, remote, action, detail)
        self.engine.add_audit(title, f"{detail} — by {e['actor']} ({e['role']}) from {e['remote']}.")
        self.engine.save_state()

    def handle(self, method, path, query, headers, remote_addr, body, principal=None):
        routes = {"status": "GET", "state": "GET", "start": "POST", "stop": "POST", "recheck": "POST", "config": "POST", "thresholds": "POST",
                  "alerts": "GET", "alerts/test": "POST", "summary": "GET"}
        name = path[len(LIVE_PREFIX):]
        if name not in routes:
            return 404, {"error": "not found"}
        want = routes[name]
        if method != want:
            return 405, {"error": "method not allowed"}
        if _is_loopback(remote_addr) and not _forwarded(headers) and not _host_is_local(headers):
            return 403, {"error": "live API refused: unexpected Host header for a local connection"}
        if not _same_origin(headers):
            return 403, {"error": "live API refused: cross-site requests are not allowed"}
        ct = (headers.get("Content-Type") or "").split(";")[0].strip().lower()
        if method == "POST" and ct != "application/json":
            return 415, {"error": "live API requires Content-Type: application/json"}
        access = self.access(remote_addr, headers, principal)
        remote = remote_label(remote_addr, headers)
        e = self.engine
        admin_only = {"stop": "live.stop", "recheck": "live.recheck", "config": "live.config", "thresholds": "live.thresholds", "alerts/test": "alerts.test"}
        if name in admin_only and access["role"] != "admin":
            self._record(principal, remote, admin_only[name], "refused: the viewer role is read-only", "denied")
            return 403, {"error": VIEWER_DENIED}
        try:
            if name == "status":
                return 200, e.status(access)
            if name == "summary":
                return 200, e.summary()
            if name == "alerts":
                return 200, e.alert_status()
            if name == "state":
                return 200, e.state(_int(query.get("since")), _int(query.get("activity_since")), access)
            doc = {}
            if body:
                if len(body) > 64 * 1024:
                    return 400, {"error": "invalid JSON payload: request body too large"}
                try:
                    doc = json.loads(body.decode("utf-8"))
                except ValueError as ex:
                    return 400, {"error": "invalid JSON payload: " + str(ex)[:160]}
                if not isinstance(doc, dict):
                    return 400, {"error": "invalid JSON payload: expected an object"}
            if name == "start":
                unknown = sorted(set(doc) - {"interval_seconds"})
                if unknown:
                    return 400, {"error": f'invalid JSON payload: json: unknown field "{unknown[0]}"'}
                if float(doc.get("interval_seconds") or 0) > 0 and access["role"] != "admin":
                    # Viewers may resume watching, but not change the refresh interval.
                    self._record(principal, remote, "live.start", "refused: the viewer role is read-only", "denied")
                    return 403, {"error": VIEWER_DENIED}
                was_running = e.is_running()
                e.start(float(doc.get("interval_seconds") or 0))
                if not was_running:
                    self._record(principal, remote, "live.start", "Live collection started.")
            elif name == "stop":
                e.stop("Stopped from the console.")
                self._done(principal, remote, "live.stop", "Collection paused", "Live collection stopped from the console")
            elif name == "recheck":
                e.recheck()
                self._record(principal, remote, "live.recheck", "Immediate re-check of every source.")
            elif name == "config":
                if set(doc) & Engine.CONNECTION_KEYS and not access["config_mutable"]:
                    self._record(principal, remote, "live.config", "refused: " + describe_patch(doc), "denied")
                    return 403, {"error": access["reason"]}
                try:
                    e.update_config(doc)
                except LiveError as ex:
                    self._record(principal, remote, "live.config", f"rejected: {describe_patch(doc)}: {ex}", "error")
                    raise
                self._done(principal, remote, "live.config", "Live configuration changed", describe_patch(doc))
            elif name == "thresholds":
                unknown = sorted(set(doc) - {"overrides", "reset"})
                if unknown:
                    return 400, {"error": f'invalid JSON payload: json: unknown field "{unknown[0]}"'}
                ov = doc.get("overrides") or {}
                keys = ", ".join(sorted(f"{k}={float(v):g}" if isinstance(v, (int, float)) and not isinstance(v, bool) else f"{k}={v}" for k, v in ov.items()))
                detail = "set " + keys
                if doc.get("reset"):
                    detail = "reset all live thresholds to defaults" + (", then set " + keys if keys else "")
                try:
                    e.set_thresholds(ov, bool(doc.get("reset")))
                except LiveError as ex:
                    self._record(principal, remote, "live.thresholds", f"rejected: {detail}: {ex}", "error")
                    raise
                self._done(principal, remote, "live.thresholds", "Thresholds changed", detail)
            elif name == "alerts/test":
                res = e.test_alerts()
                ok = sum(1 for r in res if r["ok"])
                self._record(principal, remote, "alerts.test", f"test alert accepted by {ok} of {len(res)} destination(s)")
                return 200, {"results": res, "status": e.alert_status()}
            return 200, e.state(0, 0, access)
        except LiveError as ex:
            return ex.status, {"error": str(ex)}
        except (TypeError, ValueError) as ex:
            return 400, {"error": "invalid JSON payload: " + str(ex)[:160]}


def _int(v):
    try:
        return max(0, int(v[0] if isinstance(v, list) else v))
    except (TypeError, ValueError):
        return 0


# ---------------------------------------------------------------------------------------------
# command line / environment wiring for scripts/local_runtime_server.py
# ---------------------------------------------------------------------------------------------
def add_cli_args(parser):
    g = parser.add_argument_group("live console (Go Live)")
    g.add_argument("--mode", default=os.environ.get("GPULENS_MODE", "fixture").strip() or "fixture", choices=["fixture", "live"],
                   help="fixture: simulated demo first (default); live: start live collection at boot")
    g.add_argument("--live-interval", type=float, default=None, help="live refresh interval in seconds (1-300)")
    g.add_argument("--live-idle-timeout", type=float, default=None, help="stop live collection after this many seconds without a viewer (0 = never)")
    g.add_argument("--cluster-name", default=None, help="name shown for the live cluster / host")
    g.add_argument("--dcgm-url", default="", help="dcgm-exporter metrics URL, e.g. http://gpu-node:9400/metrics")
    g.add_argument("--prometheus-url", default="", help="Prometheus base URL")
    g.add_argument("--prometheus-token-file", default="", help="file holding a bearer token for Prometheus")
    g.add_argument("--kubernetes-url", default="", help="Kubernetes API server URL (default: in-cluster, else probe kubectl proxy)")
    g.add_argument("--kubernetes-token-file", default="", help="file holding a bearer token for the Kubernetes API")
    g.add_argument("--kubernetes-ca-file", default="", help="PEM CA bundle for the Kubernetes API server")
    g.add_argument("--nvidia-smi-path", default="", help="path to nvidia-smi (default: PATH and well-known locations)")
    g.add_argument("--no-nvidia-smi", action="store_true", help="do not query the local NVIDIA driver")
    g.add_argument("--amd-smi-path", default="", help="path to amd-smi for AMD GPUs (default: PATH and /opt/rocm/bin) — experimental")
    g.add_argument("--no-amd-smi", action="store_true", help="do not query the local AMD driver")
    g.add_argument("--no-host-vitals", action="store_true", help="do not read local host vitals (/proc, /sys)")
    g.add_argument("--live-allow-remote-config", action="store_true", help="allow non-local browsers to change live connection settings")
    g.add_argument("--no-live", action="store_true", help="disable the live console entirely")
    a = parser.add_argument_group("access control, audit, alerting, durable state, fleet")
    a.add_argument("--basic-auth-user", default="", help="require HTTP basic auth: admin user name (full access)")
    a.add_argument("--basic-auth-pass", default="", help="admin password (prefer --basic-auth-pass-file or GPULENS_BASIC_AUTH_PASS)")
    a.add_argument("--basic-auth-pass-file", default="", help="file holding the admin password")
    a.add_argument("--viewer-user", default="", help="optional read-only viewer user name (needs the admin account)")
    a.add_argument("--viewer-pass", default="", help="viewer password (prefer --viewer-pass-file or GPULENS_VIEWER_PASS)")
    a.add_argument("--viewer-pass-file", default="", help="file holding the viewer password")
    a.add_argument("--audit-log-file", default="", help="append the audit log (who changed what) to this JSON-lines file")
    a.add_argument("--live-state-file", default="", help="keep live thresholds, resolved incidents and alert history in this file across restarts")
    a.add_argument("--alert-slack-webhook", default="", help="Slack incoming-webhook URL for incident alerts (https)")
    a.add_argument("--alert-webhook-url", default="", help="generic webhook URL: incident alerts are POSTed as JSON (schema gpulens.alert/v1)")
    a.add_argument("--alert-webhook-secret", default="", help="sign webhook bodies with HMAC-SHA256 (header X-GPULens-Signature-256)")
    a.add_argument("--alert-webhook-secret-file", default="", help="file holding the webhook signing secret")
    a.add_argument("--alert-min-severity", default="warning", help="lowest incident severity that is alerted: info, warning or critical")
    a.add_argument("--public-url", default="", help="external URL of this console, used for links in alerts")
    a.add_argument("--peer", action="append", default=[], help="another GPU Lens instance for the Fleet tab, as name=https://host:8080 (repeatable)")
    a.add_argument("--fleet-interval", type=float, default=15.0, help="seconds between polls of each fleet peer")


def _read_secret(path, what):
    try:
        with open(path, "r", encoding="utf-8") as f:
            v = f.read().rstrip("\r\n")
    except OSError as e:
        raise LiveError(f"cannot read the {what} file {path}: {sanitize_error(e)}")
    if not v:
        raise LiveError(f"the {what} file {path} is empty")
    return v


def build_extras(args, env=None):
    """Resolve the access-control, audit, alerting, durable-state and fleet settings (flags, then GPULENS_* env wins)."""
    env = os.environ if env is None else env
    pick = lambda flag, key: (env.get(key) or "").strip() or flag
    x = {"admin_user": pick(args.basic_auth_user, "GPULENS_BASIC_AUTH_USER"), "admin_pass": pick(args.basic_auth_pass, "GPULENS_BASIC_AUTH_PASS"),
         "viewer_user": pick(args.viewer_user, "GPULENS_VIEWER_USER"), "viewer_pass": pick(args.viewer_pass, "GPULENS_VIEWER_PASS"),
         "audit_file": pick(args.audit_log_file, "GPULENS_AUDIT_LOG_FILE"), "state_file": pick(args.live_state_file, "GPULENS_LIVE_STATE_FILE"),
         "slack": pick(args.alert_slack_webhook, "GPULENS_ALERT_SLACK_WEBHOOK_URL"), "webhook": pick(args.alert_webhook_url, "GPULENS_ALERT_WEBHOOK_URL"),
         "secret": pick(args.alert_webhook_secret, "GPULENS_ALERT_WEBHOOK_SECRET"), "min_severity": pick(args.alert_min_severity, "GPULENS_ALERT_MIN_SEVERITY"),
         "public_url": pick(args.public_url, "GPULENS_PUBLIC_URL"), "peers": list(args.peer), "fleet_interval": args.fleet_interval}
    for flag, key, dest, what in ((args.basic_auth_pass_file, "GPULENS_BASIC_AUTH_PASS_FILE", "admin_pass", "admin password"),
                                  (args.viewer_pass_file, "GPULENS_VIEWER_PASS_FILE", "viewer_pass", "viewer password"),
                                  (args.alert_webhook_secret_file, "GPULENS_ALERT_WEBHOOK_SECRET_FILE", "secret", "webhook secret")):
        path = pick(flag, key)
        if path:
            x[dest] = _read_secret(path, what)
    if env.get("GPULENS_PEERS", "").strip():
        x["peers"].append(env["GPULENS_PEERS"].strip())
    if bool(x["admin_user"]) != bool(x["admin_pass"]):
        raise LiveError("set both the admin user and the admin password, or neither")
    if bool(x["viewer_user"]) != bool(x["viewer_pass"]):
        raise LiveError("set both the viewer user and the viewer password, or neither")
    if x["viewer_user"] and not x["admin_user"]:
        raise LiveError("a viewer account needs an admin account (--basic-auth-user) as well")
    if x["viewer_user"] and x["viewer_user"] == x["admin_user"]:
        raise LiveError("the viewer and admin accounts must have different user names")
    if x["admin_user"] and len(x["admin_pass"]) < 8:
        raise LiveError("the admin password must be at least 8 characters")
    x["peers"] = parse_peers(x["peers"])
    return x


def check_basic_auth(header, extras):
    """Return (user, role) for valid credentials, or None. Comparisons are constant-time over SHA-256 digests."""
    import base64, hashlib, hmac
    if not header or not header.startswith("Basic "):
        return None
    try:
        user, _, pw = base64.b64decode(header[6:].strip()).decode("utf-8").partition(":")
    except Exception:
        return None
    d = lambda v: hashlib.sha256(v.encode("utf-8")).digest()
    for role, wu, wp in (("admin", extras["admin_user"], extras["admin_pass"]), ("viewer", extras["viewer_user"], extras["viewer_pass"])):
        if wu and wp:
            ok_u = hmac.compare_digest(d(user), d(wu))
            ok_p = hmac.compare_digest(d(pw), d(wp))
            if ok_u and ok_p:
                return user, role
    return None


# ---------------------------------------------------------------------------------------------
# Fleet hub (mirrors internal/fleet): poll other GPU Lens instances' read-only summaries
# ---------------------------------------------------------------------------------------------
_PEER_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$")


def parse_peers(items):
    out, seen = [], set()
    for raw in items:
        for item in str(raw).split(","):
            item = item.strip()
            if not item:
                continue
            name, eq, url = item.partition("=")
            name, url = name.strip(), url.strip()
            u = urllib.parse.urlparse(url)
            if not eq or not _PEER_NAME.match(name):
                raise LiveError(f"fleet: peer {item!r} must look like name=https://host:port")
            if u.scheme not in ("http", "https") or not u.hostname:
                raise LiveError(f"fleet: peer {name} has an invalid URL")
            if name in seen:
                raise LiveError(f"fleet: duplicate peer name {name}")
            seen.add(name)
            out.append({"name": name, "url": url.rstrip("/")})
    return out


def redact_peer(url):
    u = urllib.parse.urlparse(url)
    if u.username or u.password:
        host = u.hostname + (f":{u.port}" if u.port else "")
        return u._replace(netloc="***@" + host).geturl()
    return url


class FleetHub:
    def __init__(self, engine, peers, interval=15.0, timeout=5.0):
        self.engine, self.peers, self.interval = engine, peers, max(1.0, float(interval or 15.0))
        self.http = ReadOnlyHTTP(timeout)
        self.lock = threading.Lock()
        self.state = {p["name"]: {"name": p["name"], "kind": "peer", "url": redact_peer(p["url"]), "reachable": False, "error": "not polled yet",
                                  "last_ok_at": None, "latency_ms": 0.0, "summary": None} for p in peers}

    def enabled(self):
        return bool(self.peers)

    def start(self):
        if not self.peers:
            return

        def loop():
            while True:
                self.poll_once()
                time.sleep(self.interval)
        threading.Thread(target=loop, daemon=True).start()

    def poll_once(self):
        threads = [threading.Thread(target=self._poll, args=(p,), daemon=True) for p in self.peers]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

    def _poll(self, p):
        import base64
        u = urllib.parse.urlparse(p["url"])
        headers = {"Accept": "application/json", "User-Agent": "gpu-lens-fleet/" + VERSION}
        if u.username:
            headers["Authorization"] = "Basic " + base64.b64encode(f"{urllib.parse.unquote(u.username)}:{urllib.parse.unquote(u.password or '')}".encode()).decode()
        host = u.hostname + (f":{u.port}" if u.port else "")
        url = u._replace(netloc=host, path=u.path.rstrip("/") + "/api/v1/live/summary").geturl()
        begin = time.time()
        err, summary = "", None
        try:
            status, _, raw = self.http.request("GET", url, "", headers)
            summary = json.loads(raw.decode("utf-8"))
            if not isinstance(summary, dict) or summary.get("schema") != "gpulens.summary/v1":
                err, summary = "the response is not a GPU Lens summary", None
        except HTTPStatus as e:
            err = {401: "HTTP 401: the peer requires credentials (put user:password in the peer URL)",
                   404: "HTTP 404: the peer is not GPU Lens 0.3+ or its live console is disabled"}.get(e.status, f"HTTP {e.status}")
        except ValueError:
            err = "the response is not a GPU Lens summary"
        except Exception as e:
            err = "timed out" if "timed out" in str(e).lower() else "unreachable: " + sanitize_error(e)
        with self.lock:
            m = self.state[p["name"]]
            m["latency_ms"] = round((time.time() - begin) * 1000.0, 3)
            if err:
                m["reachable"], m["error"] = False, err
            else:
                m.update(reachable=True, error="", last_ok_at=iso(utcnow()), summary=summary)

    def view(self):
        now = iso(utcnow())
        members = []
        if self.engine is not None:
            s = self.engine.summary()
            members.append({"name": s["cluster"], "kind": "local", "url": "(this instance)", "reachable": True, "error": "", "last_ok_at": now,
                            "latency_ms": 0.0, "summary": s})
        with self.lock:
            members += [copy.deepcopy(self.state[n]) for n in sorted(self.state)]
        t = {"members": 0, "reachable": 0, "gpus_total": 0, "gpus_fresh": 0, "critical": 0, "warning": 0, "info": 0, "unhealthy": 0, "sources_connected": 0}
        for m in members:
            t["members"] += 1
            s = m["summary"]
            if not m["reachable"] or not s:
                t["unhealthy"] += 1
                continue
            inc = s.get("incidents") or {}
            t["reachable"] += 1
            t["gpus_total"] += s.get("gpus_total", 0)
            t["gpus_fresh"] += s.get("gpus_fresh", 0)
            for k in ("critical", "warning", "info"):
                t[k] += inc.get(k, 0)
            t["sources_connected"] += s.get("sources_connected", 0)
            if inc.get("critical", 0) > 0 or s.get("verdict") == "blocked":
                t["unhealthy"] += 1
        note = (f"Each peer is a separate GPU Lens instance that runs its own collection, rules and alerts. The hub reads their summaries every {self.interval:g}s (read-only GET)."
                if self.peers else "Add other GPU Lens instances with --peer name=https://host:8080 (repeatable) or GPULENS_PEERS to see every cluster here.")
        return {"enabled": bool(self.peers), "interval_seconds": self.interval, "members": members, "totals": t, "generated_at": now, "note": note}


# ---------------------------------------------------------------------------------------------
# /metrics (mirrors internal/api/metrics.go): GPU Lens's own health, never GPU telemetry
# ---------------------------------------------------------------------------------------------
def render_metrics(engine, http_counts, auth_enabled, started_at):
    import platform
    out = []

    def fam(name, typ, help_):
        out.append(f"# HELP {name} {help_}")
        out.append(f"# TYPE {name} {typ}")

    def smp(name, labels, v):
        lab = ""
        if labels:
            lab = "{" + ",".join(f'{k}="' + str(labels[k]).replace("\\", "\\\\").replace('"', '\\"') + '"' for k in sorted(labels)) + "}"
        out.append(f"{name}{lab} {int(v) if float(v).is_integer() else v}")

    b = lambda x: 1 if x else 0
    fam("gpulens_build_info", "gauge", "GPU Lens build information.")
    smp("gpulens_build_info", {"version": VERSION, "runtime": RUNTIME, "pyversion": platform.python_version()}, 1)
    fam("gpulens_process_start_time_seconds", "gauge", "Start time of the GPU Lens process since the Unix epoch.")
    smp("gpulens_process_start_time_seconds", None, int(started_at))
    fam("gpulens_auth_enabled", "gauge", "1 when HTTP basic authentication is required.")
    smp("gpulens_auth_enabled", None, b(auth_enabled))
    fam("gpulens_http_requests_total", "counter", "HTTP requests served, by status code.")
    for code in sorted(http_counts):
        smp("gpulens_http_requests_total", {"code": str(code)}, http_counts[code])
    if engine is not None:
        m = engine.metrics_snapshot()
        fam("gpulens_live_enabled", "gauge", "1 when the live console is available.")
        smp("gpulens_live_enabled", None, b(m["enabled"]))
        fam("gpulens_live_running", "gauge", "1 while a live collection session is running.")
        smp("gpulens_live_running", None, b(m["running"]))
        fam("gpulens_live_cycles", "gauge", "Collection cycles completed in the current session.")
        smp("gpulens_live_cycles", None, m["tick"])
        fam("gpulens_live_cycle_duration_seconds", "gauge", "Duration of the last collection cycle.")
        smp("gpulens_live_cycle_duration_seconds", None, round(m["last_tick_dur"], 6))
        fam("gpulens_live_last_cycle_timestamp_seconds", "gauge", "Time of the last completed collection cycle (0 if none).")
        smp("gpulens_live_last_cycle_timestamp_seconds", None, int(m["last_tick_at"].timestamp()) if m["last_tick_at"] else 0)
        fam("gpulens_live_interval_seconds", "gauge", "Configured live refresh interval.")
        smp("gpulens_live_interval_seconds", None, float(m["interval"]))
        fam("gpulens_live_source_enabled", "gauge", "1 when a telemetry source is enabled.")
        for src in m["sources"]:
            smp("gpulens_live_source_enabled", {"source": src["id"]}, b(src["enabled"]))
        fam("gpulens_live_source_up", "gauge", "1 when a telemetry source answered on its last check (connected or degraded).")
        for src in m["sources"]:
            smp("gpulens_live_source_up", {"source": src["id"], "state": src["state"]}, b(src["up"]))
        fam("gpulens_live_source_latency_seconds", "gauge", "Latency of the last check of each source.")
        for src in m["sources"]:
            smp("gpulens_live_source_latency_seconds", {"source": src["id"]}, round(src["latency_ms"] / 1000.0, 6))
        fam("gpulens_live_gpus", "gauge", "GPUs visible to the live console, by freshness.")
        smp("gpulens_live_gpus", {"freshness": "fresh"}, m["gpus_fresh"])
        smp("gpulens_live_gpus", {"freshness": "stale"}, m["gpus_stale"])
        fam("gpulens_live_incidents_open", "gauge", "Open live incidents, by severity.")
        for sev in ("critical", "warning", "info"):
            smp("gpulens_live_incidents_open", {"severity": sev}, m["open"].get(sev, 0))
        fam("gpulens_live_incidents_resolved", "gauge", "Incidents in the resolved log.")
        smp("gpulens_live_incidents_resolved", None, m["resolved"])
        fam("gpulens_live_threshold_overrides", "gauge", "Live rule thresholds that differ from the configured defaults.")
        smp("gpulens_live_threshold_overrides", None, m["overrides"])
        fam("gpulens_readonly_guard_ok", "gauge", "1 when the read-only transport self-test passed (mutating HTTP verbs are blocked).")
        smp("gpulens_readonly_guard_ok", None, b(m["guard_ok"]))
        fam("gpulens_alerts_sent_total", "counter", "Alerts delivered, by destination.")
        fam("gpulens_alerts_failed_total", "counter", "Alert deliveries that failed after retries, by destination.")
        fam("gpulens_alerts_dropped_total", "counter", "Alerts dropped because the delivery queue was full, by destination.")
        for d, (sent, failed, dropped) in sorted(m["alert_counters"].items()):
            smp("gpulens_alerts_sent_total", {"destination": d}, sent)
            smp("gpulens_alerts_failed_total", {"destination": d}, failed)
            smp("gpulens_alerts_dropped_total", {"destination": d}, dropped)
        if m["persistence"]:
            fam("gpulens_live_state_saved_timestamp_seconds", "gauge", "Time the live state file was last written (0 if never).")
            smp("gpulens_live_state_saved_timestamp_seconds", None, int(m["state_saved_at"].timestamp()) if m["state_saved_at"] else 0)
            fam("gpulens_live_state_save_ok", "gauge", "1 when the last live state save succeeded.")
            smp("gpulens_live_state_save_ok", None, b(not m["state_err"]))
    return "\n".join(out) + "\n"


def build_config(args, env=None):
    c = default_live_config()
    if args.live_interval is not None:
        c["interval_seconds"] = args.live_interval
    if args.live_idle_timeout is not None:
        c["idle_timeout_seconds"] = args.live_idle_timeout
    if args.cluster_name:
        c["cluster_name"] = args.cluster_name
    for key, url, tok, ca in (("dcgm_exporter", args.dcgm_url, "", ""), ("prometheus", args.prometheus_url, args.prometheus_token_file, ""),
                              ("kubernetes", args.kubernetes_url, args.kubernetes_token_file, args.kubernetes_ca_file)):
        s = c[key]
        if url:
            s["endpoint"], s["explicit"], s["enabled"] = url, True, True
        s["token_file"], s["ca_file"] = tok, ca
    if args.nvidia_smi_path:
        c["nvidia_smi_path"] = args.nvidia_smi_path
    if args.no_nvidia_smi:
        c["nvidia_smi_enabled"] = False
    if args.amd_smi_path:
        c["amd_smi_path"] = args.amd_smi_path
    if args.no_amd_smi:
        c["amd_smi_enabled"] = False
    if args.no_host_vitals:
        c["host_vitals_enabled"] = False
    if args.live_allow_remote_config:
        c["allow_remote_config"] = True
    if args.no_live:
        c["enabled"] = False
    apply_env(c, env)
    detect_in_cluster(c, env)
    c["auto_start"] = args.mode == "live"
    if c["auto_start"]:
        idle_set = args.live_idle_timeout is not None or bool((env if env is not None else os.environ).get("GPULENS_LIVE_IDLE_TIMEOUT_SECONDS"))
        if not idle_set:
            c["idle_timeout_seconds"] = 0.0  # a live service keeps collecting with no browser attached
    validate_live_config(c)
    return c
