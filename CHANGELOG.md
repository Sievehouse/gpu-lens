# Changelog

All notable changes to GPU Lens are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.3.0] - 2026-11-19

### Added
- **Alerting**: Slack and generic JSON webhook notifications when a live incident opens, changes severity or resolves.
  HMAC-SHA256 signed webhooks (`X-GPULens-Signature-256`), severity floor, test button, delivery status and retries.
  Destinations are startup-only. ([docs/ALERTING.md](docs/ALERTING.md))
- **Access control**: optional HTTP basic auth with an `admin` and a read-only `viewer` role, enforced by the server
  and reflected in the UI. ([docs/AUTH.md](docs/AUTH.md))
- **Audit log** of threshold edits, connection changes, collection start/stop/re-check, test alerts and simulation
  scenario switches, including refused attempts; `GET /api/v1/audit`, Activity → Audit filter, optional JSON-lines file.
- **Fleet view**: a hub polls other GPU Lens instances (`-peer name=url`) and shows every cluster or GPU host side by
  side on a new Fleet tab; `GET /api/v1/fleet`, `GET /api/v1/live/summary`. ([docs/FLEET.md](docs/FLEET.md))
- **AMD GPUs (experimental)** through `amd-smi` with a four-invocation read-only allow-list; Check Everything group,
  provenance notes and a test double covering three JSON layouts.
- **Durable live state** (`-live-state-file`): threshold overrides, the resolved-incident log, open-incident ages and
  the alert record survive restarts.
- **`/metrics`**: GPU Lens's own health in the Prometheus text format (cycles, per-source up/latency, GPUs, open
  incidents, alert deliveries, state saves).
- **OpenAPI 3.1 spec** for every endpoint (`docs/openapi.yaml`), checked against the server routes by a test.
- **Helm chart** (`deploy/helm/gpulens`) with read-only RBAC, auth/alert secrets, PVC, Ingress and ServiceMonitor.
- **Releases**: GoReleaser binaries for Linux, macOS and Windows (amd64, arm64); multi-arch container image on GHCR;
  version stamped from the git tag.
- **Security tooling**: CodeQL, govulncheck, npm audit, Trivy image scan, OpenSSF Scorecard, Dependabot.
- Security headers on every response; startup warning when authentication is off on a non-loopback address.
- Per-GPU `exported_metrics`, so the GPU details show "not exported" for metrics a source does not publish.
- `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `ROADMAP.md`, issue and pull-request templates, comparison table and demo
  GIF in the README.

### Changed
- Go module path is now `github.com/Sievehouse/gpu-lens`, matching the repository, so `go install` works.
- Container image builds on the latest Go 1.x and Alpine 3.x and includes a health check.
- `web/package.json` declares the Apache-2.0 license; Go and Python sources carry SPDX headers.
- Old dark-theme duplicate screenshots removed; the blind-spot screenshot regenerated in the light theme.

### Fixed
- GPU details showed `undefined ns` for thermal violation and `0` for metrics `nvidia-smi` cannot report (Xid, PCIe
  replay, HBM temperature). They now read "— (not exported)".
- An AMD-only machine could not start in `-mode live` (startup validation ignored `amd-smi`).
- The `nvidia-smi` test double lost its executable bit when committed from Windows, failing CI on Linux.

## [0.2.0] - 2026-11-16

### Added
- **Go Live**: the live console with a "check everything" preflight, per-GPU merge across `nvidia-smi`, DCGM exporter,
  Prometheus and Kubernetes, live incidents with a resolved log, trends, thresholds, connections and export.
- The Live Lab: a clearly labelled synthetic dcgm-exporter and Kubernetes API for trying Go Live without GPUs.
- Python runtime parity for the live API (`scripts/gpulens_live.py`) and black-box tests against both runtimes.
- White theme by default with a dark-mode toggle; Go Live opens first, the simulation is at `/?view=demo`.
- Sponsor links (`.github/FUNDING.yml`).

## [0.1.0]

### Added
- The simulation: six deterministic cluster scenarios, six deterministic diagnostic rules with supporting and
  contradicting evidence, capacity and low-utilization audit, Go backend and portable Python runtime.

[Unreleased]: https://github.com/Sievehouse/gpu-lens/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/Sievehouse/gpu-lens/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Sievehouse/gpu-lens/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Sievehouse/gpu-lens/releases/tag/v0.1.0
