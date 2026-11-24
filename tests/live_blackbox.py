#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""
Black-box test of the live API ("Go Live") against a real GPU Lens process — the Go binary or the Python runtime.

    python3 tests/live_blackbox.py go       # builds bin/gpulens-test and runs it
    python3 tests/live_blackbox.py python   # runs scripts/local_runtime_server.py
    python3 tests/live_blackbox.py both

Nothing here needs GPU hardware: GPUs come from the Live Lab emulator (scripts/live_lab.py) and from the
nvidia-smi test double (tests/fixtures/fake-nvidia-smi). The bare-machine scenario runs first, before the lab is
started, so default probing finds nothing.
"""
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FAKE_SMI = os.path.join(ROOT, "tests", "fixtures", "fake-nvidia-smi")
FAILS = []
CHECKS = 0


def check(cond, msg):
    global CHECKS
    CHECKS += 1
    if not cond:
        FAILS.append(msg)
        print("   FAIL:", msg)


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def call(base, method, path, body=None, headers=None, raw_ct=True):
    data = None if body is None else json.dumps(body).encode()
    h = dict(headers or {})
    if method == "POST" and raw_ct and "Content-Type" not in h:
        h["Content-Type"] = "application/json"
    req = urllib.request.Request(base + path, data=data if method == "POST" else None, method=method, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            return r.status, json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, {"raw": raw[:200].decode("utf-8", "replace")}


class Server:
    def __init__(self, kind, extra_args=(), env=None):
        self.kind, self.port = kind, free_port()
        self.base = f"http://127.0.0.1:{self.port}"
        self.tmp = tempfile.mkdtemp(prefix="gpulens-bb-")
        e = dict(os.environ)
        for k in list(e):
            if k.startswith("GPULENS_"):
                del e[k]
        e.update(env or {})
        if kind == "go":
            cmd = [os.path.join(ROOT, "bin", "gpulens-test"), "--listen", f"127.0.0.1:{self.port}", "--static-dir", os.path.join(ROOT, "web", "dist"),
                   "--fixtures-dir", os.path.join(ROOT, "fixtures"), "--state-file", os.path.join(self.tmp, "state.db")]
        else:
            cmd = [sys.executable, os.path.join(ROOT, "scripts", "local_runtime_server.py"), "--host", "127.0.0.1", "--port", str(self.port),
                   "--static-dir", os.path.join(ROOT, "web", "dist"), "--fixtures-dir", os.path.join(ROOT, "fixtures"), "--db-path", os.path.join(self.tmp, "state.db")]
        self.proc = subprocess.Popen(cmd + list(extra_args), env=e, cwd=ROOT, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(100):
            try:
                urllib.request.urlopen(self.base + "/healthz", timeout=1).read()
                return
            except Exception:
                time.sleep(0.1)
        raise RuntimeError(f"{kind} server did not start")

    def close(self):
        self.proc.send_signal(signal.SIGTERM)
        try:
            self.proc.wait(5)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        shutil.rmtree(self.tmp, ignore_errors=True)

    def state(self, **q):
        qs = "&".join(f"{k}={v}" for k, v in q.items())
        return call(self.base, "GET", "/api/v1/live/state" + ("?" + qs if qs else ""))[1]

    def post(self, name, body=None):
        return call(self.base, "POST", "/api/v1/live/" + name, body or {})

    def wait(self, pred, timeout=30, label=""):
        end = time.time() + timeout
        st = {}
        while time.time() < end:
            st = self.state()
            try:
                if pred(st):
                    return st
            except Exception:
                pass
            time.sleep(0.5)
        check(False, f"timed out waiting for: {label}")
        return st


def checks_by_id(st):
    return {c["id"]: c for g in st["preflight"]["groups"] for c in g["checks"]}


def scenario_bare(kind):
    print(f"[{kind}] bare machine: nothing to monitor must never look healthy")
    s = Server(kind, ["--no-nvidia-smi"], {"GPULENS_HOST_VITALS": "true"})
    try:
        st = s.state()
        check(st["status"]["state"] == "idle", "starts idle")
        check(st["preflight"]["verdict"] == "idle", "idle verdict before Go Live")
        check(st["status"]["runtime"] == kind, f"runtime is {kind}, got {st['status']['runtime']}")
        s.post("start")
        st = s.wait(lambda x: x["preflight"]["verdict"] in ("blocked", "degraded", "ready"), 40, "first verdict")
        check(st["preflight"]["verdict"] == "blocked", f"verdict blocked, got {st['preflight']['verdict']}")
        check(st["overview"]["fleet_health_status"] == "no_telemetry", f"fleet no_telemetry, got {st['overview']['fleet_health_status']}")
        check(len(st["gpus"]) == 0, "no GPUs invented")
        c = checks_by_id(st)
        check(c["corr.telemetry"]["status"] == "fail" and c["corr.telemetry"]["hint"], "telemetry check fails with a hint")
        check(c["core.readonly_guard"]["status"] == "pass", "read-only guard self-test passes")
        check(c["host.vitals"]["status"] in ("pass", "info"), "host vitals check ran")
        states = {x["id"]: x["state"] for x in st["sources"]}
        check(states.get("dcgm_exporter") == "not_found", f"unused default endpoint is not_found, got {states.get('dcgm_exporter')}")
        stopped = s.post("stop")[1]
        check(stopped["status"]["state"] == "idle", "stop returns to idle")
    finally:
        s.close()


def scenario_guards(kind):
    print(f"[{kind}] request guards")
    s = Server(kind, ["--no-nvidia-smi"])
    try:
        b = s.base
        check(call(b, "GET", "/api/v1/live/status")[0] == 200, "status 200")
        check(call(b, "GET", "/api/v1/live/status", headers={"Host": "evil.example.com"})[0] == 403, "foreign Host header refused (DNS rebinding)")
        check(call(b, "GET", "/api/v1/live/status", headers={"Origin": "http://evil.example.com"})[0] == 403, "cross-origin refused")
        check(call(b, "GET", "/api/v1/live/status", headers={"Sec-Fetch-Site": "cross-site"})[0] == 403, "Sec-Fetch-Site cross-site refused")
        check(call(b, "POST", "/api/v1/live/stop", {}, headers={"Content-Type": "text/plain"})[0] == 415, "non-JSON POST refused")
        check(call(b, "GET", "/api/v1/live/start")[0] == 405, "GET on a POST route refused")
        check(call(b, "POST", "/api/v1/live/state", {})[0] == 405, "POST on a GET route refused")
        code, body = s.post("config", {"dcgm_exporter": {"endpoint": "ftp://x"}})
        check(code == 400 and "http" in body.get("error", ""), f"bad endpoint scheme rejected, got {code} {body}")
        code, body = s.post("config", {"interval_seconds": 0.1})
        check(code == 400, f"interval below 1s rejected, got {code}")
        code, body = s.post("thresholds", {"overrides": {"no_such_threshold": 1}})
        check(code == 400, f"unknown threshold rejected, got {code}")
        code, body = s.post("thresholds", {"overrides": {"temp_warning_celsius": 90, "temp_critical_celsius": 85}})
        check(code == 400, f"inconsistent thresholds rejected, got {code}")
        code, body = s.post("start", {"bogus": 1})
        check(code == 400, f"unknown field rejected, got {code}")
        code, _ = call(b, "POST", "/api/v1/live/config", {"cluster_name": "x"}, headers={"X-Forwarded-For": "10.1.2.3"})
        check(code == 403, f"connection changes refused behind a proxy, got {code}")
        st = call(b, "GET", "/api/v1/live/state", headers={"X-Forwarded-For": "10.1.2.3"})[1]
        check(st["status"]["config_mutable"] is False and st["status"]["config_mutable_reason"], "proxied caller sees config locked with a reason")
        check(call(b, "GET", "/api/v1/live/nope")[0] == 404, "unknown live route 404")
        # the demo is untouched
        check(call(b, "GET", "/api/v1/overview")[0] == 200, "demo overview still served")
        check(call(b, "GET", "/api/v1/live/state")[1]["status"]["state"] == "idle", "guard probes did not start anything")
    finally:
        s.close()


def scenario_fake_smi(kind):
    print(f"[{kind}] local driver via the nvidia-smi test double")
    s = Server(kind, ["--nvidia-smi-path", FAKE_SMI], {"GPULENS_DCGM_EXPORTER": "false", "GPULENS_PROMETHEUS": "false", "GPULENS_KUBERNETES": "false"})
    try:
        s.post("start")
        st = s.wait(lambda x: len(x["gpus"]) == 2 and x["status"]["tick"] >= 2, 40, "2 GPUs from nvidia-smi")
        check(len(st["gpus"]) == 2, f"2 GPUs, got {len(st['gpus'])}")
        check(all(g["sources"] == ["nvidia_smi"] for g in st["gpus"]), "GPUs sourced from nvidia_smi only")
        check(not st["status"]["emulated"], "a test double on PATH is not labelled as the lab emulator")
        c = checks_by_id(st)
        check(c["nvsmi.query"]["status"] in ("pass", "warn"), "driver query check ran")
        check(c["nvsmi.coverage"]["status"] == "warn", "smi-only coverage is called out as a warning")
        check(any(p["namespace"] == "host-processes" for p in st["pods"]), "busy GPU attributed to a host compute process, not a fake pod")
        smi = [x for x in st["sources"] if x["id"] == "nvidia_smi"][0]
        check(smi["state"] in ("connected", "degraded") and smi["read_only"], "nvidia_smi source connected and read-only")
        g0 = [g for g in st["gpus"] if g["device_index"] == 0][0]
        check(abs(g0["latest"]["gpu_util_percent"] - 97) < 1, f"GPU0 utilisation read through, got {g0['latest']['gpu_util_percent']}")
        check(g0["latest"]["xid_error_code"] == 0 and "xid_error_code" not in (st["overview"]["sources"][0].get("missing_required") or []), "xid reported as not exported rather than invented")
        check(len(st["history"]["points"]) >= 1, "history is recording")
        since = st["history"]["last_seq"]
        time.sleep(2.5)
        st2 = s.state(since=since)
        check(all(p["seq"] > since for p in st2["history"]["points"]), "history ?since returns only newer points")
    finally:
        s.close()


def scenario_lab(kind):
    print(f"[{kind}] Live Lab emulator (synthetic data, clearly labelled)")
    lab = subprocess.Popen([sys.executable, os.path.join(ROOT, "scripts", "live_lab.py")], cwd=ROOT, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(1.5)
    s = Server(kind, ["--no-nvidia-smi"], {"GPULENS_PROMETHEUS": "false"})
    try:
        s.post("start", {"interval_seconds": 1})
        st = s.wait(lambda x: x["preflight"]["verdict"] in ("ready", "degraded") and len(x["gpus"]) == 8, 40, "8 lab GPUs")
        check(len(st["gpus"]) == 8, f"8 GPUs, got {len(st['gpus'])}")
        check(st["status"]["emulated"] is True, "emulated flag set")
        check(st["overview"]["data_mode"] == "live_emulated", f"data mode live_emulated, got {st['overview']['data_mode']}")
        check("synthetic" in st["overview"]["scenario_description"].lower(), "description says synthetic")
        check(any("emulator" in n.lower() for n in st["notes"]), "emulator note shown")
        check(st["preflight"]["verdict"] == "ready", f"preflight ready, got {st['preflight']['verdict']}: {[c['id'] for c in checks_by_id(st).values() if c['status'] in ('fail', 'warn')]}")
        states = {x["id"]: x["state"] for x in st["sources"]}
        check(states.get("dcgm_exporter") == "connected" and states.get("kubernetes") == "connected", f"dcgm+k8s connected, got {states}")
        check(len(st["nodes"]) == 2 and len(st["pods"]) >= 4, f"cluster inventory merged, got {len(st['nodes'])} nodes {len(st['pods'])} pods")
        check(all(g["emulated"] for g in st["gpus"]), "every GPU marked emulated")
        check(st["overview"]["fleet_health_status"] in ("healthy", "warning", "critical", "telemetry_blind_spot"), "fleet verdict present")
        # threshold overrides change live verdicts only
        s.post("thresholds", {"overrides": {"temp_warning_celsius": 40, "temp_critical_celsius": 99}})
        st = s.wait(lambda x: any(i["rule_id"] == "GPU-THERM-006" for i in x["overview"]["active_incidents"]), 20, "thermal incident after lowering threshold")
        check(st["threshold_overrides"] == ["temp_critical_celsius", "temp_warning_celsius"], f"overrides listed, got {st['threshold_overrides']}")
        demo = call(s.base, "GET", "/api/v1/rules")[1]
        check(all(r.get("id") for r in demo["rules"]), "demo rules endpoint still answers")
        st = s.post("thresholds", {"reset": True})[1]
        check(st["threshold_overrides"] == [], "reset clears overrides")
        st = s.wait(lambda x: not any(i["rule_id"] == "GPU-THERM-006" for i in x["overview"]["active_incidents"]), 20, "thermal incident resolves after reset")
        check(any(r["incident"]["rule_id"] == "GPU-THERM-006" for r in st["resolved_incidents"]), "resolved incident is kept in the log")
        check(any("resolved" in a["title"].lower() for a in st["activity"]), "activity log records the resolution")
        # recheck + config restart
        check(s.post("recheck")[0] == 200, "recheck ok")
        code, st = s.post("config", {"cluster_name": "bb-cluster"})
        check(code == 200 and st["config"]["cluster_name"] == "bb-cluster", "cluster name applied")
        st = s.wait(lambda x: x["status"]["tick"] >= 2 and len(x["gpus"]) == 8, 30, "session restarted after config change")
        check(st["overview"]["cluster_name"] == "bb-cluster", "new cluster name on the overview")
        check(st["history"]["epoch"] >= 1, "history epoch present")
        # tokens are write-only
        code, st = s.post("config", {"dcgm_exporter": {"endpoint": f"http://127.0.0.1:9400/metrics", "bearer_token": "sekrit-token-123"}})
        check(code == 200, f"token config accepted, got {code}")
        blob = json.dumps(s.state()) + json.dumps(call(s.base, "GET", "/api/v1/live/status")[1])
        check("sekrit-token-123" not in blob, "bearer token is never returned by the API")
        check(s.state()["config"]["dcgm_exporter"]["token_set"] is True, "token_set flag shown instead")
        # a wrong endpoint is diagnosed, not silently healthy
        code, st = s.post("config", {"dcgm_exporter": {"endpoint": "http://127.0.0.1:1/metrics"}})
        st = s.wait(lambda x: any(c["status"] == "fail" for c in checks_by_id(x).values()), 30, "failing check for a dead endpoint")
        check(checks_by_id(st)["dcgm.reachable"]["status"] == "fail" and checks_by_id(st)["dcgm.reachable"]["hint"], "dead DCGM endpoint → FAIL with a hint")
        check(st["preflight"]["verdict"] != "ready", "verdict is not ready with a dead endpoint")
        d = [x for x in st["sources"] if x["id"] == "dcgm_exporter"][0]
        check(d["state"] == "unreachable" and d["consecutive_failures"] >= 1, f"source state unreachable, got {d['state']}")
        s.post("stop")
        st = s.state()
        check(st["status"]["state"] == "idle", "stopped")
    finally:
        s.close()
        lab.terminate()
        lab.wait(5)


# ---------------------------------------------------------------------------------------------
# Operations features: access control, audit, alerting, self-metrics, AMD, durable state, fleet
# ---------------------------------------------------------------------------------------------
import base64
import hashlib
import hmac
import http.server
import threading

FAKE_AMD = os.path.join(ROOT, "tests", "fixtures", "fake-amd-smi")


def basic(user, pw):
    return {"Authorization": "Basic " + base64.b64encode(f"{user}:{pw}".encode()).decode()}


def raw_get(base, path, headers=None):
    req = urllib.request.Request(base + path, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, r.read().decode("utf-8", "replace"), {k.lower(): v for k, v in r.headers.items()}
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace"), {k.lower(): v for k, v in e.headers.items()}


def scenario_auth_audit(kind):
    print(f"[{kind}] access control, roles and audit log")
    tmp = tempfile.mkdtemp(prefix="gpulens-audit-")
    audit_file = os.path.join(tmp, "audit.jsonl")
    s = Server(kind, ["--basic-auth-user", "admin", "--basic-auth-pass", "admin-pass-1", "--viewer-user", "viewer", "--viewer-pass", "viewer-pass-1",
                      "--no-nvidia-smi", "--no-amd-smi", "--no-host-vitals", "--audit-log-file", audit_file])
    admin, viewer = basic("admin", "admin-pass-1"), basic("viewer", "viewer-pass-1")
    try:
        for path in ("/", "/api/v1/overview", "/api/v1/live/state", "/metrics", "/api/v1/audit"):
            code, _, hdrs = raw_get(s.base, path)
            check(code == 401 and "Basic" in hdrs.get("www-authenticate", ""), f"{path} requires credentials (got {code})")
        check(raw_get(s.base, "/healthz")[0] == 200, "/healthz stays public for probes")
        check(raw_get(s.base, "/api/v1/overview", basic("admin", "nope"))[0] == 401, "wrong password refused")
        code, st = call(s.base, "GET", "/api/v1/live/status", headers=viewer)
        check(code == 200 and st.get("role") == "viewer" and st.get("can_operate") is False and st.get("config_mutable") is False, f"viewer status {st}")
        code, st = call(s.base, "POST", "/api/v1/live/thresholds", {"overrides": {"temp_warning_celsius": 75}}, viewer)
        check(code == 403 and "viewer" in st.get("error", ""), f"viewer cannot change thresholds ({code})")
        for name in ("stop", "recheck", "alerts/test"):
            check(call(s.base, "POST", "/api/v1/live/" + name, {}, viewer)[0] == 403, f"viewer cannot {name}")
        check(call(s.base, "POST", "/api/v1/live/start", {}, viewer)[0] == 200, "viewer may resume watching")
        code, st = call(s.base, "POST", "/api/v1/live/thresholds", {"overrides": {"temp_warning_celsius": 75}}, admin)
        check(code == 200 and st["thresholds"]["temp_warning_celsius"] == 75, "admin changes thresholds")
        check(any(a["kind"] == "audit" for a in st["activity"]), "the change shows in the activity log")
        call(s.base, "POST", "/api/v1/live/config", {"prometheus": {"endpoint": "http://127.0.0.1:1", "bearer_token": "TOPSECRET-1"}}, admin)
        call(s.base, "GET", "/api/v1/overview?scenario=scenario-3-gpu-health-warning", headers=viewer)
        code, au = call(s.base, "GET", "/api/v1/audit", headers=viewer)
        acts = [(e["action"], e["actor"], e["outcome"]) for e in au.get("entries", [])]
        check(("live.thresholds", "viewer", "denied") in acts, f"denied attempt audited: {acts}")
        check(("live.thresholds", "admin", "ok") in acts, "admin change audited")
        check(("simulation.scenario.select", "viewer", "ok") in acts, "scenario switch audited")
        check(au.get("persistent") is True, "audit endpoint reports the file")
        with open(audit_file) as f:
            body = f.read()
        check("TOPSECRET-1" not in body and "token set" in body, "tokens never reach the audit file")
        check(len(body.strip().splitlines()) == len(au["entries"]), "audit file matches the API")
    finally:
        s.close()
        shutil.rmtree(tmp, ignore_errors=True)


class Receiver:
    def __init__(self):
        self.events, self.headers = [], []
        outer = self

        class H(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(self):
                n = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(n)
                outer.events.append(body)
                outer.headers.append({k.lower(): v for k, v in self.headers.items()})
                self.send_response(200)
                self.end_headers()

        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}/hook"
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def types(self):
        return [json.loads(b)["type"] for b in self.events]

    def close(self):
        self.srv.shutdown()


def scenario_alerts_metrics(kind):
    print(f"[{kind}] alerting (generic webhook) and /metrics")
    rx = Receiver()
    s = Server(kind, ["--mode", "live", "--nvidia-smi-path", FAKE_SMI, "--no-amd-smi", "--no-host-vitals", "--live-interval", "1",
                      "--alert-webhook-url", rx.url, "--alert-webhook-secret", "hook-secret", "--public-url", "https://gpulens.example.com"],
               {"FAKE_SMI_TEMP0": "95"})
    try:
        end = time.time() + 20
        while time.time() < end and "incident.opened" not in rx.types():
            time.sleep(0.3)
        check("incident.opened" in rx.types(), f"an incident.opened alert is delivered (got {rx.types()})")
        if rx.events:
            ev = json.loads(rx.events[0])
            check(ev.get("schema") == "gpulens.alert/v1" and ev.get("console_url", "").startswith("https://gpulens.example.com/"), "alert payload schema and link")
            sig = rx.headers[0].get("x-gpulens-signature-256", "")
            check(sig == "sha256=" + hmac.new(b"hook-secret", rx.events[0], hashlib.sha256).hexdigest(), "HMAC signature verifies")
        n_before = len(rx.events)
        code, res = s.post("alerts/test")
        check(code == 200 and res["results"][0]["ok"] is True, f"test alert accepted ({code})")
        check(len(rx.events) == n_before + 1 and json.loads(rx.events[-1])["type"] == "test", "test alert arrives")
        st = s.state()
        check(st["alerts"]["enabled"] and st["alerts"]["destinations"][0]["sent"] >= 2 and "hook" not in st["alerts"]["destinations"][0]["target"],
              "alert status counts deliveries and hides the URL path")
        code, text, hdrs = raw_get(s.base, "/metrics")
        check(code == 200 and hdrs.get("content-type", "").startswith("text/plain"), "/metrics is Prometheus text")
        for want in ("gpulens_build_info{", "gpulens_live_running 1", 'gpulens_live_source_up{source="nvidia_smi",state="connected"} 1',
                     'gpulens_alerts_sent_total{destination="webhook"}', "gpulens_readonly_guard_ok 1"):
            check(want in text, f"/metrics has {want}")
        crit = [l for l in text.splitlines() if l.startswith('gpulens_live_incidents_open{severity="critical"}') or l.startswith('gpulens_live_incidents_open{severity="warning"}')]
        check(sum(float(l.split()[-1]) for l in crit) >= 1, "/metrics counts the open incident")
    finally:
        s.close()
        rx.close()


def scenario_amd(kind):
    print(f"[{kind}] AMD GPUs via the amd-smi test double")
    s = Server(kind, ["--mode", "live", "--no-nvidia-smi", "--amd-smi-path", FAKE_AMD, "--no-host-vitals", "--live-interval", "1"], {"FAKE_AMD_FORMAT": "wrapped"})
    try:
        st = s.wait(lambda x: len(x["gpus"]) == 2, 20, "two AMD GPUs")
        check(all(g["model_name"] == "AMD Instinct MI300X" for g in st["gpus"]), "AMD model names")
        src = [x for x in st["sources"] if x["id"] == "amd_smi"]
        check(src and src[0]["state"] == "connected", "amd_smi source connected")
        c = checks_by_id(st)
        check(c.get("amd.query", {}).get("status") == "pass" and c.get("amd.coverage", {}).get("status") == "warn", "AMD preflight checks")
        check(st["gpus"][1]["latest"]["gpu_temp_celsius"] == 39, "hotspot fallback when edge is N/A")
    finally:
        s.close()


def scenario_persistence_fleet(kind):
    print(f"[{kind}] durable live state and fleet aggregation")
    tmp = tempfile.mkdtemp(prefix="gpulens-state-")
    path = os.path.join(tmp, "live.json")
    args = ["--no-nvidia-smi", "--no-amd-smi", "--no-host-vitals", "--live-state-file", path]
    try:
        s = Server(kind, args)
        code, _ = s.post("thresholds", {"overrides": {"temp_critical_celsius": 91}})
        check(code == 200 and os.path.exists(path), "threshold change is saved to the state file")
        check(oct(os.stat(path).st_mode & 0o777) == "0o600", "state file is private (0600)")
        s.close()
        s = Server(kind, args)
        st = s.state()
        check(st["thresholds"]["temp_critical_celsius"] == 91 and st["persistence"]["enabled"] and st["persistence"]["loaded_at"], "override restored after restart")
        peer = Server(kind, ["--mode", "live", "--nvidia-smi-path", FAKE_SMI, "--no-amd-smi", "--no-host-vitals", "--live-interval", "1", "--cluster-name", "gpu-box"])
        hub = Server(kind, ["--no-nvidia-smi", "--no-amd-smi", "--no-host-vitals", "--peer", f"box={peer.base}", "--peer", "gone=http://127.0.0.1:1",
                            "--fleet-interval", "1"])
        try:
            fv = {}
            end = time.time() + 20
            while time.time() < end:
                fv = call(hub.base, "GET", "/api/v1/fleet")[1]
                m = {x["name"]: x for x in fv.get("members", [])}
                if m.get("box", {}).get("reachable") and (m["box"]["summary"] or {}).get("gpus_total", 0) == 2:
                    break
                time.sleep(0.5)
            m = {x["name"]: x for x in fv.get("members", [])}
            check(fv.get("enabled") and m.get("box", {}).get("reachable"), f"peer reachable from the hub: {m.get('box')}")
            check((m.get("box", {}).get("summary") or {}).get("cluster") == "gpu-box", "peer summary carries its cluster name")
            check(m.get("gone", {}).get("reachable") is False and m["gone"]["error"], "unreachable peer reported, not hidden")
            check(any(x["kind"] == "local" for x in fv.get("members", [])), "hub lists itself")
            check(fv.get("totals", {}).get("gpus_total", 0) >= 2, "totals add up peer GPUs")
        finally:
            hub.close()
            peer.close()
            s.close()
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def main():
    which = sys.argv[1] if len(sys.argv) > 1 else "both"
    kinds = ["go", "python"] if which == "both" else [which]
    if "go" in kinds:
        if not shutil.which("go"):
            print("go not found; skipping the Go backend")
            kinds.remove("go")
        else:
            subprocess.check_call(["go", "build", "-o", os.path.join(ROOT, "bin", "gpulens-test"), "./cmd/gpulens"], cwd=ROOT)
    for kind in kinds:
        for sc in (scenario_bare, scenario_guards, scenario_fake_smi, scenario_lab, scenario_auth_audit, scenario_alerts_metrics, scenario_amd,
                   scenario_persistence_fleet):
            try:
                sc(kind)
            except Exception as e:
                check(False, f"{sc.__name__} crashed: {e!r}")
    print(f"\n{CHECKS - len(FAILS)}/{CHECKS} checks passed")
    for f in FAILS:
        print(" -", f)
    sys.exit(1 if FAILS else 0)


if __name__ == "__main__":
    main()
