# Roadmap

What is coming next, roughly in order. Every item keeps GPU Lens's ground rules: read-only, deterministic, and
missing data is never healthy. Want to help with one? Comment on the matching issue or open one.

## Now: real-hardware validation (0.1.x)

* **Reports from real machines** — multi-GPU NVIDIA servers, DCGM exporter on Kubernetes, kube-prometheus-stack.
  Fix whatever field-level surprises they reveal.
* **AMD Instinct validation** — run the `amd-smi` adapter on MI200/MI300 hardware and tune the JSON parsing,
  temperature sensor choice and power fields.
* **Prometheus parity in the Python runtime** — history backfill, scrape-target audit and clock-skew check.

## Next: bigger fleets (0.2)

* **Merged multi-cluster evaluation** — today the Fleet tab aggregates each member's health; next, a hub that can
  drill into a member's incidents and GPUs without leaving the page.
* **Shared state store for HA** — move durable state and the audit log behind a store interface with a PostgreSQL
  implementation, so two replicas can run behind a load balancer.
* **Alert routing** — per-severity and per-cluster destinations, quiet hours, and a digest mode.
* **OIDC sign-in** — native single sign-on in addition to basic auth and the reverse-proxy pattern.

## Later: richer hardware models (0.3+)

* **MIG-aware capacity** — model Multi-Instance GPU profiles and slice geometry in the capacity math.
* **Kubernetes DRA** — understand `ResourceClaim` allocations alongside `nvidia.com/gpu`.
* **AMD fault signals** — map RAS / ECC block counters and resets to the hardware-health rule.
* **Fabric signals** — NVLink / NVSwitch topology and InfiniBand counters where an exporter provides them.

## Not planned

* **Automated remediation** (cordon, drain, reset). GPU Lens explains; tools like NVIDIA NVSentinel act. Keeping
  GPU Lens strictly read-only is what lets you run it with minimal permissions everywhere.
* **AI/LLM-generated diagnoses.** Every verdict stays explainable and reproducible.
