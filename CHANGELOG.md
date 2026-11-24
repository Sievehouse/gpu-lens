# Changelog

All notable changes to GPU Lens are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Hydra Maintainer configuration for optional contributor onboarding, issue management,
  PR health scoring, contributor progression, and reviewer assignment.

## [0.1.0] - 2026-11-24

The first public release of GPU Lens: a self-hosted, strictly read-only console that tells you what your GPUs are
doing right now and why something is wrong, with deterministic, evidence-backed incidents.

### Go Live — real telemetry
- Opens first and checks every source in parallel: a **Check Everything** preflight grades GPU Lens itself, the host,
  the local NVIDIA and AMD drivers, DCGM exporter, Prometheus, Kubernetes and cross-source correlation, each with a
  plain-language reason and a hint on how to fix it.
- Per-GPU merge across `nvidia-smi`, DCGM exporter, Prometheus and Kubernetes; GPU cards with utilization, VRAM,
  temperature, power, fan, throttle reasons, PCIe link, sparklines and the processes or pods using each GPU.
- **AMD GPUs (experimental)** through `amd-smi`.
- Live incidents with a resolved log, trends, heat map, capacity, workloads, nodes and events, live threshold
  overrides, connections, and JSON export.
- **Alerting**: Slack and a signed JSON webhook when an incident opens, changes severity or resolves.
- **Access control**: optional basic auth with an admin and a read-only viewer role, and an **audit log** of every
  change, including refused attempts.
- **Fleet view**: one GPU Lens shows every other instance — each cluster or GPU host — side by side.
- **Durable state**: thresholds, resolved incidents and alert history survive restarts.
- **`/metrics`**: GPU Lens's own health in the Prometheus format.
- The **Live Lab**: a clearly labelled synthetic DCGM exporter and Kubernetes API for trying Go Live without GPUs.

### Simulation — no GPU needed
- Six deterministic cluster scenarios: healthy, pending GPU workloads, hardware and thermal faults, persistently low
  utilization, workload failure correlation, and stale telemetry.
- Six deterministic diagnostic rules with supporting **and** contradicting evidence; missing data is never treated as
  healthy.
- Capacity and low-utilization audit with transparent GPU-hour math.

### Run it anywhere
- Single Go binary with no third-party dependencies, plus a Python runtime (standard library only) for machines
  without Go; both serve the same API.
- Release binaries for Linux, macOS and Windows (amd64, arm64), a multi-arch container image on GHCR, Docker Compose,
  and a Helm chart with read-only RBAC.
- OpenAPI 3.1 spec for every endpoint.

### Safety and quality
- Read-only by construction: GET-only transport for every source, read-only Kubernetes RBAC, fixed allow-lists for
  `nvidia-smi` and `amd-smi`, no AI/LLM calls. The only outbound write is alert delivery to destinations you set at
  startup.
- Security headers, CodeQL, govulncheck, npm audit, Trivy image scan, OpenSSF Scorecard and Dependabot.
- White theme by default with a dark-mode toggle.

### Known limits
- Real-hardware coverage is still small (see the README's *Current limits*); AMD support is experimental.

[Unreleased]: https://github.com/Sievehouse/gpu-lens/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Sievehouse/gpu-lens/releases/tag/v0.1.0
