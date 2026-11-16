# Go Live — real, read-only telemetry

GPU Lens has two parts in one UI:

| | Part 1 — Simulated demo | Part 2 — **Go Live** |
| :-- | :-- | :-- |
| What you see | The original console and its six deterministic scenarios, unchanged | A live console fed by this machine and your cluster |
| Data | `fixtures/*.json` | `nvidia-smi`, host vitals, DCGM exporter, Prometheus, Kubernetes API |
| How to open | Default page | Press the floating **Go Live** button (or open `/?view=live`) |
| How to leave | — | **Exit live** (top right) returns to the demo exactly as it was |

The button lives outside the demo's root element, so Part 1 renders byte-for-byte as it did before Go Live existed.

## What happens when you press Go Live

1. The collector starts. Every source is checked **in parallel, every cycle** (default 2 s; DCGM, Prometheus and Kubernetes at most every 5 s / 15 s / 15 s).
2. A **preflight checklist** grades every layer — GPU Lens itself, the host, the local NVIDIA driver, DCGM, Prometheus, Kubernetes, and cross-source correlation — as pass / warn / fail / skip / info, each with a plain-language detail and, when something is wrong, a hint that says how to fix it.
3. Per-GPU observations from every source are merged by GPU UUID. Nodes and pods are assembled; with no Kubernetes, GPUs are attributed to the compute process holding most of their memory (namespace `host-processes`) rather than to invented pods.
4. The same six deterministic rules as the demo run on the live data every cycle. Incidents are fingerprint-deduplicated, tracked from first sight, and move to the resolved log when they stop firing.
5. The verdict is one of `ready`, `degraded`, `blocked`, `checking`, `idle`.

**Missing data is never healthy.** A machine with no reachable telemetry yields `blocked` and fleet status `no_telemetry`; a metric that is not exported is reported as *not exported*, never as `0`.

## Views

Overview (fleet verdict, KPIs, incidents, capacity) · Preflight · GPUs (per-GPU cards, sparklines, throttle reasons, PCIe, processes) · Nodes & Pods · Incidents (evidence, resolved log) · Capacity · Trends (history charts with a time cursor) · Host vitals · Sources (latency history, facts, exported-metric audit) · Events · Activity log · Rules & Thresholds (live overrides, never touching the demo) · Connections (endpoints, tokens, interval) · Export (JSON snapshot / CSV).

## Trying it without a GPU: the Live Lab

```bash
python3 scripts/live_lab.py        # serves a SYNTHETIC dcgm-exporter on :9400 and a read-only Kubernetes API on :8001
make live                          # or: make demo, then press Go Live
```

Those are the addresses GPU Lens probes by default, so no configuration is needed. The lab replays a 12-minute storyline (idle notebook, thermal excursion, Pending 4-GPU job, Xid 79 + pod crash, stalled exporter). It is labelled three ways — a `gpulens_lab_emulator_info` metric, an `X-GPULens-Emulated` header, and a `-gpulens-lab` Kubernetes version suffix — and the console shows an *emulated endpoint* banner and data mode `live_emulated` whenever it is connected. What is real is the pipeline: HTTP collection, parsing, merging, rules, lifecycle.

## Connecting real sources

| Source | Default | Set with |
| :-- | :-- | :-- |
| Local NVIDIA driver | `nvidia-smi` on `PATH` and well-known locations | `--nvidia-smi-path`, `GPULENS_NVIDIA_SMI_PATH`, `--no-nvidia-smi` |
| Host vitals | `/proc`, `/sys` (Linux) | `--no-host-vitals` |
| DCGM exporter | probe `http://127.0.0.1:9400/metrics` | `--dcgm-url`, `GPULENS_DCGM_EXPORTER_URL` |
| Prometheus | probe `http://127.0.0.1:9090` | `--prometheus-url`, `GPULENS_PROMETHEUS_URL`, `…_TOKEN_FILE` |
| Kubernetes | in-cluster service account, else probe `kubectl proxy` on `:8001` | `--kubernetes-url`, `GPULENS_KUBERNETES_URL`, `…_TOKEN_FILE`, `…_CA_FILE` |

Other settings: `--live-interval` / `GPULENS_LIVE_INTERVAL_SECONDS` (1–300), `--live-idle-timeout` / `GPULENS_LIVE_IDLE_TIMEOUT_SECONDS` (default 900 s without a viewer; 0 with `--mode live`), `--cluster-name`, `--mode live` (start collecting at boot, no browser needed), `--no-live` (demo only), `--live-allow-remote-config`.

For Kubernetes apply `deploy/kubernetes/rbac-readonly.yaml` (get/list/watch on nodes, pods, events — nothing else) and pass that service account's token by file.

A source that is not found at its *default* address is shown as **not found** (neutral). A source you configured explicitly, or that worked earlier in the session and stopped, is shown as **unreachable** (a failure).

## HTTP API

All under `/api/v1/live/`. Same-origin only, no CORS headers.

| Route | Method | Purpose |
| :-- | :-- | :-- |
| `status` | GET | Engine state, tick, interval, runtime (`go` / `python`) |
| `state?since=N&activity_since=M` | GET | Full payload; only history points / activity entries newer than the cursors |
| `start` | POST `{interval_seconds?}` | Start collecting (idempotent) |
| `stop` | POST | Stop collecting |
| `recheck` | POST | Re-probe every source now |
| `config` | POST | Patch cluster name, interval, source enable/endpoint/token. Restarts the session unless only the interval changed |
| `thresholds` | POST `{overrides, reset}` | Live rule-threshold overrides (validated; demo unaffected) |

Guards on every route: a loopback caller must send a local `Host` header (DNS-rebinding defence); cross-origin requests are refused; POST requires `Content-Type: application/json`. Connection settings (endpoints, tokens, cluster name) can only be changed from the machine running GPU Lens, or with `--live-allow-remote-config`; a request that came through a reverse proxy (`X-Forwarded-For`, `Forwarded`, `X-Real-IP`) is never treated as local. Bearer tokens are write-only: the API reports `token_set: true`, never the token. File paths (nvidia-smi binary, token/CA files) are not settable over the API.

## Testing

```bash
go test -race ./...                 # engine, merge, lifecycle, preflight, API guards (test doubles, no network)
python3 tests/live_blackbox.py both # the real Go binary and the Python runtime: bare machine → blocked, request guards,
                                    # nvidia-smi test double, Live Lab (rules, thresholds, config, token secrecy, dead endpoint)
```

`tests/fixtures/fake-nvidia-smi` is a test double that prints canned answers to the handful of read-only queries GPU Lens issues; it is not a GPU tool.

## Honest limits

- No GPU hardware, real Prometheus or real Kubernetes cluster was available while this was built. The nvidia-smi adapter was exercised against a test double and the other adapters against the Live Lab and Go `httptest` servers. Field-level behaviour on real hardware (older drivers, MIG, unusual `nvidia-smi` output) deserves a first run on a real machine.
- The Python runtime (used when Go is not installed) reads Prometheus with one instant query: no history backfill, no scrape-target audit, no clock-skew check. Those three preflight checks say so rather than pretending.
- MIG slices and shared-GPU resources appear as separate devices but are not part of the capacity math.
