# GPU Lens — System Architecture & Architecture Decision Records (ADRs)

## 1. Architectural Overview

GPU Lens is a self-hosted, strictly read-only diagnostic and capacity intelligence platform for Kubernetes GPU fleets. It ingests and normalizes GPU hardware telemetry and Kubernetes cluster state, evaluates an extensible registry of deterministic diagnostic rules, computes transparent GPU capacity and low-utilization reports, persists incident state with fingerprint deduplication, and serves a responsive operator console.

### High-Level System Diagram

```
+---------------------------------------------------------------------------------------------------+
|                                   External / Offline Data Sources                                 |
|                                                                                                   |
|  +-------------------------+  +-------------------------+  +-----------------------------------+  |
|  | 1. Offline Demo Fixture |  | 2. Kubernetes API       |  | 3. Prometheus / DCGM Exporter     |  |
|  |    Source (Bundled JSON |  |    Read-Only Source     |  |    Read-Only HTTP Source          |  |
|  |    6 Deterministic      |  |    (GET/LIST Pods,      |  |    (/api/v1/query, /query_range,  |  |
|  |    Scenarios)           |  |     Nodes, Events only) |  |     or direct /metrics text)      |  |
|  +------------+------------+  +------------+------------+  +-----------------+-----------------+  |
+---------------|----------------------------|---------------------------------|--------------------+
                |                            |                                 |
                v                            v                                 v
+---------------------------------------------------------------------------------------------------+
|                           Source Collection & Normalization Layer (internal/source)               |
|  - Enforces bounded timeouts, read-only HTTP verbs, and configurable metric/label mappings        |
|  - Maps DCGM Exporter default & optional fields -> Normalized GPUMetricSample & GPUDevice         |
|  - Maps K8s Nodes/Pods/Events -> Normalized Node, WorkloadPod, K8sEvent                           |
|  - Audits scrape timestamps, missing optional metrics, and pod-to-GPU label attribution coverage  |
+------------------------------------------------+--------------------------------------------------+
                                                 |
                                                 | Normalized ClusterSnapshot
                                                 v
+---------------------------------------------------------------------------------------------------+
|                        Core Intelligence Layer (Pure Domain & Deterministic Logic)                |
|                                                                                                   |
|  +---------------------------------------------+  +--------------------------------------------+  |
|  | Incident Rule Engine (internal/rules)       |  | Capacity Intelligence (internal/capacity)  |  |
|  | - Registry of 6 deterministic rules         |  | - Total / Allocatable / Allocated GPUs     |  |
|  | - Explicit thresholds & observation windows |  | - Node-level fragmentation & unschedulable |  |
|  | - Evidence collection (Supporting vs.       |  | - Low-utilization GPU-hour integration     |  |
|  |   Contradicting facts)                      |  | - Pod-mapping coverage & blind-spot check  |  |
|  | - Suppression & Inconclusive safeguards     |  | - Optional sample rate estimate (labeled)  |  |
|  | - SHA-256 Incident Fingerprint generator    |  | - Transparent step-by-step methodology     |  |
|  +----------------------+----------------------+  +---------------------+----------------------+  |
+-------------------------|-----------------------------------------------|-------------------------+
                          |                                               |
                          v                                               v
+---------------------------------------------------------------------------------------------------+
|                         Persistence & State Layer (internal/store / SQLite)                       |
|  - Deduplicates incidents by stable fingerprint (updates last_seen, occurrence_count, evidence)   |
|  - Stores active scenario selection, operator rule threshold overrides, and evaluation history    |
+------------------------------------------------+--------------------------------------------------+
                                                 |
                                                 v
+---------------------------------------------------------------------------------------------------+
|                           HTTP API Layer (internal/api, Read/Write Timeouts)                      |
|  GET /api/v1/overview     GET /api/v1/incidents        GET /api/v1/incidents/{id}                 |
|  GET /api/v1/capacity     GET /api/v1/sources          GET /api/v1/rules                          |
|  GET /api/v1/scenarios    POST /api/v1/scenarios/select GET /healthz & GET /readyz                |
+------------------------------------------------+--------------------------------------------------+
                                                 |
                                                 v
+---------------------------------------------------------------------------------------------------+
|                        Operator Console UI (web/ - TypeScript + TSX, own JSX runtime)             |
|  - Persistent Simulated vs. Live Telemetry Banner & Scenario Switcher                             |
|  - Views: Overview | Incidents | Incident Detail | Capacity | Data Sources | Rules                |
|  - Explicit UI states: Loading, Empty, Error, Stale Telemetry, Insufficient Evidence              |
+---------------------------------------------------------------------------------------------------+
```

---

## 2. Strict Separation of Concerns

The codebase is organized into distinct layers with unidirectional dependencies:

1. **`internal/domain`**:
   - Pure data structures with zero I/O or third-party dependencies.
   - Defines `ClusterSnapshot`, `NodeState`, `GPUDevice`, `GPUMetricPoint`, `WorkloadPod`, `ContainerTermination`, `K8sEvent`, `SourceHealth`, `Incident`, `EvidenceItem`, `RuleDefinition`, and `CapacityReport`.
2. **`internal/config`**:
   - Typed configuration loader and validator (`Config`, `RuleThresholds`, `MetricMappingConfig`, `SourceConfig`).
   - Validates thresholds, observation windows, HTTP timeouts, and metric mappings at startup, returning actionable startup errors on invalid values.
3. **`internal/source`**:
   - Defines the `TelemetryCollector` interface returning a normalized `*domain.ClusterSnapshot`.
   - Provides four separated implementations:
     1. `FixtureSource`: Loads deterministic, version-controlled JSON fixtures (`fixtures/*.json`) for offline demo and CI testing.
     2. `KubernetesSource`: Strictly read-only client (`GET` on `/api/v1/nodes`, `/api/v1/pods`, `/api/v1/events`) with a compile-time and test-verified read-only HTTP transport guard that rejects any non-`GET`/`HEAD` request.
     3. `PrometheusSource`: Read-only Prometheus HTTP API v1 client (`/api/v1/query`, `/api/v1/query_range`) using configurable metric and label mappings (`UUID`/`uuid`, `pod`/`pod_name`, `namespace`/`pod_namespace`).
     4. `DCGMExporterSource`: Direct Prometheus text-exposition parser for scraping `dcgm-exporter` `/metrics` endpoints directly in single-node or test setups.
4. **`internal/rules`**:
   - Contains the `Engine` and `Rule` interface.
   - Consumes only `*domain.ClusterSnapshot` and `config.RuleThresholds`—never talks to Kubernetes, Prometheus, or SQLite directly.
   - Computes deterministic SHA-256 incident fingerprints from `(RuleID, ClusterName, TargetKind, Namespace, TargetName, PrimarySubreason)` so repeated evaluations update an existing incident rather than creating duplicates.
5. **`internal/capacity`**:
   - Pure calculation package that computes cluster capacity, allocatable vs. allocated GPUs, node fragmentation, pod-to-GPU attribution coverage, and allocated GPU-hours observed below the configured utilization threshold using trapezoidal/rectangular time-window integration.
6. **`internal/store`**:
   - Manages incident lifecycle deduplication (`FirstSeenAt`, `LastSeenAt`, `OccurrenceCount`, `Status`) and runtime scenario/configuration state (backed by SQLite / atomic state storage).
7. **`internal/api`**:
   - HTTP router, request validation, structured JSON logging (redacting any bearer tokens or kubeconfig paths), CORS headers, and static asset serving for the compiled frontend.

---

## 3. Normalized Domain Model Key Relationships

Every entity in `ClusterSnapshot` uses explicit keys so correlation across layers is deterministic:
- **Node (`NodeState`)** is keyed by `Name` (matching Kubernetes `node.metadata.name` and DCGM `hostname` / Prometheus `instance` node label). Tracks `GPUCapacity`, `GPUAllocatable`, `GPUAllocated`, `Schedulable`, `Taints`, and `Conditions`.
- **GPU Device (`GPUDevice`)** is keyed by `UUID` (e.g., `GPU-a100-node01-0`) and linked to `NodeName`, `DeviceIndex` (`gpu="0"`), `ModelName`, `PCIBusID`, `AllocatedPod` (`namespace/name` when mapped via kubelet `pod-resources`), `MappingStatus` (`mapped`, `unallocated`, `missing_pod_labels`), latest instantaneous metrics, and `Samples []GPUMetricPoint`.
- **Workload (`WorkloadPod`)** is keyed by `Namespace/Name` and tracks `UID`, `OwnerKind`/`OwnerName` (e.g., `PyTorchJob/llama3-finetune-70b`), `Phase` (`Pending`, `Running`, `Failed`, `Succeeded`), `NodeName`, `RequestedGPUs`, `AssignedGPUUUIDs`, `RestartCount`, `LastTermination` (`ExitCode`, `Reason`, `StartedAt`, `FinishedAt`), `NodeSelector`, and `Tolerations`.
- **Kubernetes Event (`K8sEvent`)** links `InvolvedObjectKind`, `InvolvedObjectNamespace`, `InvolvedObjectName`, `Reason` (`FailedScheduling`, `BackOff`, `XIDError`, `NodeNotReady`), `Message`, `Count`, `FirstTimestamp`, and `LastTimestamp`.

---

## 4. Architecture Decision Records (ADRs)

### ADR-001: Deterministic Rule Engine Over Statistical Anomaly Detection or LLMs
- **Context**: Operators investigating a failed 64-GPU training job or a pending workload need to know *which exact metric or event* triggered a finding and *what evidence contradicts it*.
- **Decision**: Implement all diagnostics as explicit, deterministic rules with configurable numeric thresholds, minimum sample counts, and explicit supporting/contradicting evidence lists. No LLMs, external AI APIs, or black-box ML models are used.
- **Consequences**:
  - *Pros*: 100% reproducible, unit-testable, zero external API cost or data-egress risk, explainable in post-mortems, and immune to hallucinated root causes.
  - *Cons*: Cannot parse arbitrary unstructured application stack traces from container stdout; requires new rules to be codified as Go structs with unit tests.

### ADR-002: Explicit Evidence Strength & Status Instead of Probabilistic "Confidence %"
- **Context**: Many monitoring tools output arbitrary percentage scores (e.g., "87% confidence") without mathematical calibration.
- **Decision**: Every incident reports two explicit, auditable fields:
  1. `EvalStatus`: `triggered` (all required signals present and threshold crossed), `inconclusive` (anomaly or failure detected, but required correlating telemetry or minimum observation window is missing/stale), or `suppressed`.
  2. `EvidenceStrength`: Computed deterministically from signal corroboration:
     - `strong`: Multi-layer corroboration (e.g., pod failure + matching GPU hardware fault metric on assigned GPU + corroborating node/kubelet event within the correlation window, with fresh telemetry).
     - `moderate`: Single-layer primary signal meets threshold with fresh telemetry, or multi-layer temporal correlation where exact pod-to-GPU UUID binding is unavailable (node-level correlation only).
     - `contextual`: Threshold crossed (e.g., persistent low utilization), where telemetry is complete and fresh, but hardware telemetry alone cannot distinguish between workload bottlenecks (I/O, CPU stall, memory-bound inference) and idle reservation.
     - `inconclusive`: Missing or stale telemetry prevents complete verification.
- **Consequences**: Operators immediately understand whether a finding is backed by multi-signal hardware proof or requires human verification of application behavior.

### ADR-003: Separation of Default vs. Optional DCGM Exporter Metrics
- **Context**: As documented in the [NVIDIA DCGM Exporter Metrics Reference](https://www.google.com/search?q=https%3A%2F%2Fdocs.nvidia.com%2Fdatacenter%2Fdcgm%2Flatest%2Freference%2Fdcgm-exporter-metrics.html), default collector CSVs enable `DCGM_FI_DEV_GPU_TEMP`, `DCGM_FI_DEV_POWER_USAGE`, `DCGM_FI_DEV_GPU_UTIL`, `DCGM_FI_DEV_MEM_COPY_UTIL`, `DCGM_FI_DEV_XID_ERRORS`, `DCGM_FI_DEV_PCIE_REPLAY_COUNTER`, `DCGM_FI_DEV_FB_USED`, and `DCGM_FI_DEV_ROW_REMAP_FAILURE`, whereas counters like `DCGM_FI_DEV_THERMAL_VIOLATION`, `DCGM_FI_DEV_POWER_VIOLATION`, `DCGM_FI_DEV_ECC_DBE_VOL_TOTAL`, `DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL`, and `DCGM_EXP_GPU_HEALTH_STATUS` are commented out by default.
- **Decision**: Normalize metrics through a configurable mapping table that distinguishes *required default metrics* from *optional extended metrics*. If an optional metric is absent from Prometheus, GPU Lens marks that metric as `not_exported` in the source status rather than defaulting its value to `0` or raising a false scrape failure.
- **Consequences**: Works out-of-the-box with stock `dcgm-exporter` DaemonSets while unlocking deeper ECC, NVLink, and throttling violation diagnostics when custom collector CSVs are enabled.

### ADR-004: Defense-in-Depth Read-Only Enforcement for Infrastructure Adapters
- **Context**: A diagnostic tool must never accidentally mutate cluster state, kill pods, cordon nodes, or run invasive GPU stress tests (`dcgmi diag`).
- **Decision**: Enforce read-only behavior at three independent layers:
  1. **Code/Interface level**: Adapter interfaces (`TelemetryCollector`) expose only `Collect(ctx) (*ClusterSnapshot, error)`. No mutating methods exist.
  2. **HTTP Transport level**: The Kubernetes and Prometheus HTTP clients wrap `http.RoundTripper` in a `ReadOnlyRoundTripper` that rejects any HTTP method other than `GET` (and `HEAD`) before a network packet can leave the process.
  3. **Kubernetes RBAC level**: Shipped `deploy/kubernetes/rbac-readonly.yaml` grants only `verbs: ["get", "list", "watch"]` on `nodes`, `pods`, and `events`.
- **Consequences**: Provable non-mutating behavior verified by automated unit tests (`TestReadOnlyRoundTripperBlocksMutations`).

### ADR-005: Dual-Mode Execution (Go Standard-Library Backend + Portable Verification Harness)
- **Context**: GPU Lens is architected in Go (`cmd/gpulens`, `internal/...`) with a React + TypeScript frontend (`web/`). Engineers reviewing or running the project in restricted CI/sandbox environments without an installed Go compiler or live GPU cluster still need to run the full test suite, build the TypeScript UI, start the server, and inspect live scenarios.
- **Decision**: Keep the Go backend free of heavy CGo or external router dependencies so `go test ./...` and `go build ./cmd/gpulens` compile cleanly with standard Go, and provide a 1:1 local runtime & verification suite (`scripts/local_runtime_server.py` and `scripts/run_verification_suite.py`) that consumes the exact same `fixtures/*.json`, `config/*.yaml`, SQLite schema, and compiled `web/dist/` bundle when `go` is absent on the host.
- **Consequences**: Guarantees 100% offline reproducibility across both standard Go development workstations/Docker environments and air-gapped sandbox VMs.

---

## 5. Go Live: live telemetry engine

Go Live adds a second, isolated path next to the fixture demo. The demo (`/api/v1/overview`, `/incidents`, …) is untouched; everything live sits behind `/api/v1/live/*` and the Go Live UI.

```text
 nvidia-smi ─┐                                                    ┌─ /api/v1/live/state ─► LiveConsole (web/src/live)
 host /proc ─┤  per-source goroutine/thread   ┌── merge by GPU UUID ┤
 DCGM /metrics ─┤  (parallel, own cadence,    │   nodes + pods      │
 Prometheus ─┤   latency + failure tracking) ─┤   windowed rules    │
 Kubernetes ─┘                                 └── lifecycle ────────┘
                                                   (open → tracked → resolved), history ring, activity log, preflight
```

- **`internal/live`** — `collect.go` (source scheduling and state machine: pending → connected / degraded / unreachable / not_found / disabled / error), `merge.go` (per-UUID observation merge, inferred nodes and host-process pods, windowed low-utilisation input), `lifecycle.go` (incident tracking, history, activity diffing), `preflight.go` (the checklist), `view.go` (payload). The rules and capacity calculator are the *same* `internal/rules` and `internal/capacity` code the demo uses; the engine only builds a `ClusterSnapshot` from live observations.
- **Epistemics preserved.** A GPU with no fresh sample is stale and is excluded from health verdicts and raises `GPU-FRESH-005`. A metric absent from every source is "not exported", never `0`. The low-utilisation window is built from real observation time (`anchorWindow` pins its ends so the capacity calculator and the rule agree) so a freshly-seen workload is not accused of being idle for an hour.
- **Isolation of the UI.** The launcher is plain DOM outside `#root`; Go Live is the default view and the simulation is `?view=demo`; the root factory swaps `<LiveConsole/>` and `<App/>`. `App.tsx` only gained `export`s. A DOM-snapshot regression (125 states, loaded with `?view=demo`) against the original build proves the simulation is unchanged. The white theme is CSS only (`html[data-theme]`), including attribute-selector overrides for inline-styled text and SVG chart colours, so no markup differs between themes.

### Operations around the engine: alerting, audit, fleet, AMD, durable state

```text
                     ┌──────────── internal/api ─────────────┐
 browser / API ──► basic auth (admin | viewer) ─► routes ─► role check ─► audit.Log (memory + JSONL file)
                     │   security headers, /metrics (own health)          │
                     └──────────────────────────────────────────────────────┘
 live.Engine ── each cycle ──► alert.Notifier.Sync(open incidents) ──► queue ──► Slack / signed webhook (startup-only URLs)
      │                                                                          (retries, no redirects)
      ├── SaveState every 30 s / on change ──► live-state file (overrides, resolved log, first-seen, alert record)
      └── Summary() ◄── GET /api/v1/live/summary ◄── fleet.Hub on another instance (GET-only transport)
 amd-smi ──► source.AMDSMISource ──► same result type as nvidia-smi ──► same merge, rules, preflight
```

- **`internal/alert`** keeps its own record of announced incidents (fingerprint → severity) and diffs each cycle's
  open set against it, so restarts never re-announce and warm-up cycles never report false resolutions.
- **`internal/audit`** is deliberately simple: a bounded ring plus an append-only file, reloaded at startup.
- **`internal/fleet`** aggregates summaries of other instances; it does not merge their GPUs into one rule run, which
  keeps every member authoritative for its own incidents and alerts.
- **AMD** reuses the local-driver path end to end: `amd_smi` is a peer of `nvidia_smi` in source priority, provenance
  annotation ("read via amd-smi") and preflight.
- **Version** is a build-time variable (`-ldflags -X …/internal/live.Version=`), set from the git tag by GoReleaser,
  the Dockerfile `VERSION` argument and `make build`.

### Python runtime parity

`scripts/gpulens_live.py` is a standard-library port of the engine, used by `scripts/local_runtime_server.py` when Go is not installed. It serves the same routes, guards and JSON shapes (verified by running `tests/live_blackbox.py` against both servers). Differences:

| Area | Go | Python |
| :-- | :-- | :-- |
| Prometheus | instant query **and** `query_range` history backfill, scrape-target audit (`up`), clock-skew check | one instant query; those three checks report "Go-runtime feature" |
| Rules / capacity | `internal/rules`, `internal/capacity` | the existing Python mirrors in `local_runtime_server.py` |
| Concurrency | goroutines, context cancellation | one daemon thread per source call, generation counter to discard stale results |
| Kubernetes read-only stats | counted by the transport guard | counted by the transport guard |
| Alerting, audit, roles, /metrics, AMD, durable state, fleet | `internal/alert`, `internal/audit`, `internal/api`, `internal/source`, `internal/live`, `internal/fleet` | the same features in `gpulens_live.py` and `local_runtime_server.py`; the black-box suite checks both |

