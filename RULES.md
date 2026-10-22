# GPU Lens — Diagnostic Incident Rule Reference (`RULES.md`)

## 1. Design Principles of the Rule Engine

The GPU Lens diagnostic rule engine (`internal/rules`) evaluates a normalized `ClusterSnapshot` against an extensible registry of deterministic rules. Every rule adheres to five engineering invariants:

1. **Deterministic Evaluation**: Given the same `ClusterSnapshot` and `RuleThresholds`, the engine always produces the exact same incidents, evidence items, and fingerprints.

2. **Explicit Separation of Observed Facts vs. Possible Explanations**:

   * Every incident records `ObservedFacts` (verbatim metric values, timestamps, pod phases, exit codes, and Kubernetes events), `SupportingEvidence`, and `ContradictingEvidence`.

   * Every incident lists `PossibleCauses` as hypotheses to verify—never as proven causation.

3. **Explicit Suppression and Inconclusive Conditions**:

   * When a symptom is observed (e.g., pod crash or low utilization) but telemetry is stale, sample count is below `min_samples`, or GPU-to-pod mapping is missing, the rule either marks the incident `EvalStatus: "inconclusive"` or suppresses per-pod claims rather than guessing.

4. **Evidence Strength Rubric**:

   * `strong`: Direct multi-signal corroboration across independent layers (e.g., pod termination + assigned GPU hardware error metric + node/kubelet event within correlation window) with fresh telemetry.

   * `moderate`: Single authoritative signal (e.g., `FailedScheduling` event with node capacity breakdown, or node-level GPU hardware error without pod UUID binding) with fresh telemetry.

   * `contextual`: Complete, fresh telemetry crosses an operational threshold (e.g., persistent low SM utilization), but external application context (I/O, CPU, memory residency) is required to interpret intent.

   * `inconclusive`: Missing, stale, or unmapped telemetry prevents full verification.

5. **Stable Deduplication Fingerprinting**:

   * Each incident computes a deterministic fingerprint:
     `SHA256(RuleID + "|" + ClusterName + "|" + TargetKind + "|" + Namespace + "|" + TargetName + "|" + PrimaryReasonCode)[:16]`

   * Repeated evaluations of the same active condition update `LastSeenAt`, increment `OccurrenceCount`, and refresh `Evidence` on the existing incident rather than generating duplicate alerts.

## 2. Registered Diagnostic Rules Catalog

| Rule ID | Name | Default Severity | Primary Use Case | Required Input Signals | 
| ----- | ----- | ----- | ----- | ----- | 
| **`GPU-SCHED-001`** | Pending GPU Workload Scheduling Bottleneck | `warning` (or `critical` if pending > `critical_pending_minutes`) | Use Case A: GPU scheduling diagnosis | Kubernetes `WorkloadPod` (`Phase == Pending`, `RequestedGPUs > 0`), `K8sEvent` (`Reason == FailedScheduling`), `NodeState` (`GPUCapacity`, `GPUAllocatable`, `GPUAllocated`, `Schedulable`, `Taints`, `Labels`) | 
| **`GPU-HEALTH-002`** | GPU Hardware Fault or Health Degradation | `critical` (XID / DBE / Row Remap / Health FAIL) or `warning` (PCIe replay / NVLink CRC / Health WARN) | Use Case B: GPU health & performance warnings | DCGM metrics (`DCGM_FI_DEV_XID_ERRORS`, `DCGM_FI_DEV_ROW_REMAP_FAILURE`, `DCGM_FI_DEV_PCIE_REPLAY_COUNTER`, optional `DCGM_FI_DEV_ECC_DBE_VOL_TOTAL`, `DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL`, `DCGM_EXP_GPU_HEALTH_STATUS`), `NodeState` (`GPUAllocatable < GPUCapacity`, NPD conditions) | 
| **`GPU-UTIL-003`** | Persistently Low GPU Compute Utilization During Active Allocation | `info` / `warning` | Use Case C: Persistently low utilization | `WorkloadPod` (`Phase == Running`, `RequestedGPUs > 0`), mapped `GPUDevice` samples (`DCGM_FI_DEV_GPU_UTIL`, `DCGM_FI_DEV_MEM_COPY_UTIL`, `DCGM_FI_DEV_FB_USED`, `DCGM_FI_DEV_FB_FREE`) over `low_util_window_minutes` | 
| **`GPU-CORR-004`** | Workload Failure or Restart Correlated with GPU / Node Signals | `critical` (hardware-corroborated) or `warning` (application/ambiguous) | Use Case D: Workload failure correlation | `WorkloadPod` (`RestartCount > 0` or `Phase == Failed`, `LastTermination`), assigned/node `GPUDevice` telemetry & events (`DCGM_FI_DEV_XID_ERRORS`, ECC, thermal, NVLink), `K8sEvent` (`BackOff`, `XIDError`, `NodeNotReady`) | 
| **`GPU-FRESH-005`** | Telemetry Source Unavailable, Stale, or Uncorrelated | `critical` (source down / cluster-wide stale) or `warning` (partial node stale / missing pod labels) | Use Case E: Monitoring data freshness | `SourceHealth` (`Available`, `LastScrapeTime`), `GPUDevice` (`LastSampleTime`, `MappingStatus`), `WorkloadPod` (`Running` pods requesting GPUs vs. mapped GPU UUIDs) | 
| **`GPU-THERM-006`** | GPU Thermal, Power Brake, or Memory Pressure Warning | `warning` (or `critical` if >= `critical_temp_celsius` with active throttling) | Use Case B & Rule 6: Memory, thermal, or power warning conditions | `DCGM_FI_DEV_GPU_TEMP`, `DCGM_FI_DEV_MEMORY_TEMP`, `DCGM_FI_DEV_POWER_USAGE`, `DCGM_FI_DEV_FB_USED`, optional `DCGM_FI_DEV_THERMAL_VIOLATION`, `DCGM_FI_DEV_POWER_VIOLATION`, `DCGM_EXP_CLOCK_EVENTS_COUNT`, NPD `GPUClockThrottling` condition | 

## 3. Detailed Rule Specifications

### Rule 1: `GPU-SCHED-001` — Pending GPU Workload Scheduling Bottleneck

* **Condition Evaluated**: A Kubernetes pod requesting `nvidia.com/gpu >= 1` has remained in `Pending` phase for at least `pending_grace_seconds` (default: `60s`).

* **Configurable Thresholds**:

  * `pending_grace_seconds`: `60`

  * `critical_pending_minutes`: `30`

* **Firing & Classification Logic**:

  1. Inspects observed `FailedScheduling` events for the pod (`InvolvedObjectName == pod.Name`).

  2. Cross-checks live `NodeState` inventory across the cluster:

     * **Cluster-Wide Insufficient Allocatable GPUs**: Total free allocatable GPUs across ready, schedulable nodes is less than `pod.RequestedGPUs`.

     * **Node Fragmentation (Contiguous Allocation Bottleneck)**: Cluster-wide free GPUs `>= pod.RequestedGPUs`, but no single ready, schedulable node has `>= pod.RequestedGPUs` free (e.g., pod requests 4 GPUs on a single node, while 2 nodes each have 2 free GPUs).

     * **Degraded Node Allocatable (`allocatable < capacity`)**: One or more nodes have `GPUAllocatable < GPUCapacity` (indicating the device plugin marked a GPU unhealthy) or `Schedulable == false` (cordoned), blocking scheduling.

     * **Affinity / Selector / Taint Constraint**: `FailedScheduling` message cites node affinity/selector mismatch (e.g., requesting `nvidia.com/gpu.product=NVIDIA-H100-80GB-HBM3` when only A100 nodes are free) or untolerated taints.

* **Suppression / Inconclusive Conditions**:

  * **Suppressed** if the pod has been pending for `< pending_grace_seconds` (normal scheduler startup latency).

  * **Marked `inconclusive` (`EvidenceStrength: "inconclusive"`)** if the pod is `Pending` for `>= pending_grace_seconds`, **no** `FailedScheduling` event is present in the snapshot, and node resource inventory is missing or stale. The rule explicitly states: *"Observed pod in Pending phase, but no FailedScheduling event or node allocatable state is available; refusing to guess scheduling reason."*

* **Suggested Next Checks**:

  * Inspect `kubectl describe pod <namespace>/<name>` events and `nodeSelector`/`affinity` fields.

  * Check nodes where `status.allocatable["nvidia.com/gpu"] < status.capacity["nvidia.com/gpu"]` for unhealthy devices (`GPU-HEALTH-002`).

  * If fragmentation blocks multi-GPU pods, evaluate gang scheduling/queueing via Kueue or Volcano.

### Rule 2: `GPU-HEALTH-002` — GPU Hardware Fault or Health Degradation

* **Condition Evaluated**: A GPU device or GPU node exhibits hardware fault signals in DCGM telemetry, Kubernetes device plugin capacity drops, or Node Problem Detector conditions.

* **Configurable Thresholds**:

  * `xid_error_threshold`: `1` (any non-zero `DCGM_FI_DEV_XID_ERRORS` value or increase)

  * `ecc_dbe_threshold`: `1` (any volatile/aggregate uncorrectable double-bit ECC error > 0, when `DCGM_FI_DEV_ECC_DBE_VOL_TOTAL` is exported)

  * `row_remap_failure_threshold`: `1` (`DCGM_FI_DEV_ROW_REMAP_FAILURE >= 1`)

  * `pcie_replay_warning_threshold`: `10` (`DCGM_FI_DEV_PCIE_REPLAY_COUNTER >= 10` in observation window)

  * `nvlink_crc_error_threshold`: `1` (`DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL >= 1` or `DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL >= 1`, when exported)

* **Severity Mapping**:

  * `critical`: Non-zero XID error (e.g., XID 48 Double Bit ECC, XID 74 NVLink Error, XID 79 Fallen Off Bus, XID 94/95 Contained/Uncontained ECC), `DCGM_FI_DEV_ROW_REMAP_FAILURE > 0`, `DCGM_FI_DEV_ECC_DBE_VOL_TOTAL > 0`, `DCGM_EXP_GPU_HEALTH_STATUS == 20 (FAIL)`, or node `GPUAllocatable < GPUCapacity`.

  * `warning`: Elevated PCIe replay counter (`>= pcie_replay_warning_threshold`), NVLink CRC flit errors with link still up, `DCGM_EXP_GPU_HEALTH_STATUS == 10 (WARN)`, or high correctable remapped rows.

* **Suppression / Inconclusive Conditions**:

  * Never fabricates findings for optional metrics (`DCGM_FI_DEV_ECC_DBE_VOL_TOTAL`, `DCGM_FI_DEV_NVLINK_*`, `DCGM_EXP_GPU_HEALTH_STATUS`) when those metrics are not exported by the target source (`MetricAvailability == "not_exported"`).

  * If telemetry for a GPU is stale (`age > max_staleness_seconds`), defers to `GPU-FRESH-005` and marks health verification `inconclusive`.

* **Suggested Next Checks**:

  * Inspect node kernel logs (`dmesg -T | grep -iE "nvrm|xid|sxid"`) during a maintenance window.

  * Check whether `status.allocatable["nvidia.com/gpu"]` on the node dropped below `status.capacity["nvidia.com/gpu"]`.

  * If row-remap failure or uncorrectable ECC DBE is confirmed, schedule node cordon after workload checkpointing and initiate RMA/hardware break-fix.

### Rule 3: `GPU-UTIL-003` — Persistently Low GPU Compute Utilization During Active Allocation

* **Condition Evaluated**: A `Running` workload pod has held `>= 1` allocated GPUs for at least `low_util_window_minutes` (default: `60m`), and mean `DCGM_FI_DEV_GPU_UTIL` across its mapped GPUs over that window is below `low_util_threshold_percent` (default: `15.0%`).

* **Configurable Thresholds**:

  * `low_util_threshold_percent`: `15.0`

  * `low_util_window_minutes`: `60`

  * `min_utilization_samples`: `6`

* **Epistemic Safeguards (Non-Waste Policy)**:

  * **Never labels the workload as "proven waste."**

  * Explicitly inspects and reports accompanying signals:

    * **Framebuffer Memory Occupancy** (`DCGM_FI_DEV_FB_USED` / total MiB): If framebuffer usage is high (e.g., `> 50%`, such as a 70B LLM inference server holding weights in VRAM with low request QPS), the explanation highlights *Memory-Capacity-Bound Allocation* as a primary contradicting indicator against simple downsizing.

    * **Memory Copy Utilization** (`DCGM_FI_DEV_MEM_COPY_UTIL`): Checks whether PCIe/HBM transfers are active while SM compute is low.

* **Suppression / Inconclusive Conditions**:

  * **Suppressed** if the workload has been running for less than `low_util_window_minutes` (prevents false positives during model initialization, container image startup, or dataset warm-up).

  * **Suppressed / Marked `inconclusive`** if fewer than `min_utilization_samples` exist in the window, or if GPU-to-pod label mapping (`pod`, `namespace`) is missing (`MappingStatus != "mapped"`).

* **Suggested Next Checks (Follow-Up Checklist)**:

  1. Check PyTorch/training data loader throughput and storage I/O wait (CPU-to-GPU starvation).

  2. Check distributed `NCCL` synchronization barriers if one straggler rank is holding peer GPUs at 0% compute.

  3. Verify if the workload is an interactive notebook or low-QPS inference endpoint that requires full GPU VRAM (`DCGM_FI_DEV_FB_USED`) to keep model weights resident.

  4. If the workload genuinely needs only a fraction of a GPU's compute and VRAM, evaluate NVIDIA MIG partitioning or time-slicing.

### Rule 4: `GPU-CORR-004` — Workload Failure or Restart Correlated with GPU / Node Signals

* **Condition Evaluated**: A GPU workload pod is in `Failed` phase or has `RestartCount >= 1` with a recent `LastTermination` within the lookback window, and GPU Lens correlates the termination timestamp (`FinishedAt`) against GPU telemetry, node conditions, and Kubernetes events within `[-correlation_lead_minutes, +correlation_lag_minutes]` (default: `[-15m, +5m]`, aligned with Meta FAIR's `[-10m, +5m]` attribution methodology in [arXiv:2410.21680v2](https://www.google.com/search?q=https%3A%2F%2Farxiv.org%2Fhtml%2F2410.21680v2)).

* **Configurable Thresholds**:

  * `correlation_lead_minutes`: `15`

  * `correlation_lag_minutes`: `5`

* **Differential Diagnosis Logic (Supporting vs. Contradicting Evidence)**:

  * **Case 4A — Hardware-Corroborated Failure (`Severity: "critical"`, `EvidenceStrength: "strong"`)**:

    * *Supporting Evidence*: Pod container terminated (`ExitCode != 0`) within `[-15m, +5m]` of an observed hardware anomaly on its assigned GPU or node (`DCGM_FI_DEV_XID_ERRORS > 0`, `DCGM_FI_DEV_ECC_DBE_VOL_TOTAL > 0`, `DCGM_FI_DEV_ROW_REMAP_FAILURE > 0`, NVLink CRC/recovery error spike, thermal slowdown violation, or node `GPUAllocatable < GPUCapacity`).

    * *Contradicting Evidence*: Lists any non-hardware signals (e.g., whether `OOMKilled` was false, whether other pods on the node remained unaffected).

    * *Epistemic Notice*: States clearly that temporal and topological correlation between the hardware fault and container crash is **strong supporting evidence, not mathematical proof of sole causation**.

  * **Case 4B — Application / Userspace Failure with Healthy GPU Telemetry (`Severity: "warning"`, `EvidenceStrength: "moderate"`)**:

    * *Supporting Evidence*: Pod terminated (e.g., `ExitCode: 137 / OOMKilled` or `ExitCode: 1 / Error`).

    * *Contradicting Evidence (Against GPU Hardware Fault)*: Assigned GPU devices show `DCGM_FI_DEV_XID_ERRORS == 0`, `DCGM_FI_DEV_ROW_REMAP_FAILURE == 0`, normal temperature/power, and healthy node conditions throughout the termination window.

    * *Explanation*: Helps operators avoid unnecessary node draining or hardware RMA when GPU telemetry rules out a hardware fault.

  * **Case 4C — Pod Failure with Stale or Unmapped GPU Telemetry (`Severity: "warning"`, `EvalStatus: "inconclusive"`, `EvidenceStrength: "inconclusive"`)**:

    * If a GPU pod failed/restarted but telemetry on that node was stale or missing during the termination window, the rule explicitly reports `inconclusive` rather than claiming the GPU hardware was healthy.

### Rule 5: `GPU-FRESH-005` — Telemetry Source Unavailable, Stale, or Uncorrelated

* **Condition Evaluated**: Audits the observability pipeline itself across three dimensions:

  1. **Source Availability**: A configured telemetry source (Prometheus or Kubernetes API) failed its last collection or is unreachable.

  2. **Metric Staleness**: One or more GPU devices have `Now - LastSampleTime > max_staleness_seconds` (default: `180s`), or have zero samples.

  3. **Pod-to-GPU Attribution Blind Spot**: One or more `Running` pods are allocated `nvidia.com/gpu >= 1` on a node, and DCGM metrics exist for that node's GPUs, but the GPU metrics lack `pod`/`namespace` labels (`MappingStatus == "missing_pod_labels"`).

* **Configurable Thresholds**:

  * `max_staleness_seconds`: `180`

* **Epistemic Invariant**:

  * Missing or stale observations **always** generate an explicit `GPU-FRESH-005` incident (`critical` if an entire source or node telemetry stream is down/stale; `warning` if pod attribution labels are missing) and set the cluster/node telemetry freshness banner to `Stale / Blind Spot Detected`. Missing data is never reported as `Healthy`.

* **Suggested Next Checks**:

  * Verify `dcgm-exporter` DaemonSet pod status and Prometheus `/targets` scrape health.

  * If metrics are fresh but `pod`/`namespace` labels are missing, verify `dcgm-exporter` is started with `--kubernetes` and mounts `/var/lib/kubelet/pod-resources/` (the directory, not the socket file inode, so kubelet restarts do not break `PodResourcesLister` gRPC connections).

### Rule 6: `GPU-THERM-006` — GPU Thermal, Power Brake, or Memory Pressure Warning

* **Condition Evaluated**: Evaluates GPU operating envelope constraints using default and optional DCGM metrics:

  1. **Thermal Warning / Critical**: `DCGM_FI_DEV_GPU_TEMP >= temp_warning_celsius` (default: `80°C`) or `>= temp_critical_celsius` (default: `87°C`), or `DCGM_FI_DEV_MEMORY_TEMP >= mem_temp_warning_celsius` (default: `90°C`), or optional `DCGM_FI_DEV_THERMAL_VIOLATION > 0` / clock event `sw_thermal` / `hw_thermal` / NPD `GPUClockThrottling`.

  2. **Power Cap / Brake Throttling**: `DCGM_FI_DEV_POWER_USAGE` sustained at `>= 98%` of device power limit alongside optional `DCGM_FI_DEV_POWER_VIOLATION > 0` or `hw_power_brake` clock event.

  3. **Framebuffer Memory Exhaustion Risk**: `DCGM_FI_DEV_FB_USED / (DCGM_FI_DEV_FB_USED + DCGM_FI_DEV_FB_FREE) >= fb_memory_warning_percent` (default: `95.0%`) on an active workload.

* **Configurable Thresholds**:

  * `temp_warning_celsius`: `80.0`

  * `temp_critical_celsius`: `87.0`

  * `mem_temp_warning_celsius`: `90.0`

  * `fb_memory_warning_percent`: `95.0`

* **Suppression / Inconclusive Conditions**:

  * Evaluates `DCGM_FI_DEV_THERMAL_VIOLATION` and `DCGM_FI_DEV_POWER_VIOLATION` only when exported by the data source; never assumes zero violations when those optional counters are unconfigured.

  * Suppressed if GPU telemetry is stale (`Now - LastSampleTime > max_staleness_seconds`).

* **Suggested Next Checks**:

  * For thermal/power warnings: inspect chassis fan RPMs, datacenter rack inlet temperature, heatsink seating, and PSU health (`ipmitool sdr`).

  * For framebuffer memory `>= 95%`: check workload batch size, KV-cache reservation, or activation checkpointing to prevent imminent CUDA OOM crashes.