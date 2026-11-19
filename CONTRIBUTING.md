# Contributing to GPU Lens

Thank you for helping! GPU Lens is small enough that one pull request can make a real difference. The most valuable
contributions right now are **reports from real hardware**: run Go Live on your machine or cluster and open an issue
with a screenshot of the *Check Everything* tab.

## Ground rules

These are what make GPU Lens trustworthy. A change that breaks one of them will not be merged, however useful.

1. **Read-only.** Never write to the infrastructure GPU Lens observes: no Kubernetes mutations, no non-GET HTTP to
   telemetry sources, no GPU or driver state changes. New local commands need a fixed allow-list of arguments, no
   shell, a timeout and bounded output.
2. **Deterministic.** Rules are explicit, testable code. No LLM or AI-API calls, no probabilistic "confidence %".
3. **Missing data is never healthy.** Absent telemetry is reported as stale, unavailable or *not exported* — never as
   zero, never as green.
4. **Both runtimes.** The Go binary and the Python runtime serve the same API. A live-API change lands in both, and
   `tests/live_blackbox.py both` must pass.
5. **The simulation stays identical.** Go Live changes must not change the simulation's DOM.

## Set up

You need Go 1.22+, Python 3.9+ and Node.js 20+.

```bash
git clone https://github.com/Sievehouse/gpu-lens.git && cd gpu-lens
cd web && npm ci && cd ..          # TypeScript compiler for the frontend
make fixtures frontend             # deterministic fixtures + web/dist
make demo                          # http://127.0.0.1:8080
python3 scripts/live_lab.py        # optional: a synthetic GPU cluster for Go Live
```

No GPU? Use the Live Lab and the test doubles in `tests/fixtures/` (`fake-nvidia-smi`, `fake-amd-smi`).

## Before you open a pull request

```bash
gofmt -s -w cmd/ internal/
go vet ./...
go test -race ./...
node web/scripts/build_offline.js       # rebuild web/dist and commit it (Render serves the committed bundle)
python3 scripts/run_verification_suite.py
python3 tests/live_blackbox.py both     # the real Go binary and the Python runtime
```

* Commit `web/dist` whenever you change `web/src`.
* New endpoints go into `docs/openapi.yaml` (a test checks the route list).
* New Go and Python files start with `// SPDX-License-Identifier: Apache-2.0` (`#` for Python).
* Add a line to `CHANGELOG.md` under **Unreleased**.
* On Windows, keep the test doubles executable: `git update-index --chmod=+x tests/fixtures/fake-*`.

## Where things live

| You want to… | Look at |
| :-- | :-- |
| Add or change a rule | `internal/rules/engine.go` (Go), the Python mirror in `scripts/local_runtime_server.py`, `RULES.md` |
| Add a telemetry source | `internal/source/`, wire it in `internal/live/engine.go`, `collect.go`, `merge.go`, `preflight.go`; mirror in `scripts/gpulens_live.py` |
| Change the live console | `web/src/live/` (never `web/src/App.tsx` behaviour) |
| Alerting, audit, auth, fleet | `internal/alert`, `internal/audit`, `internal/api/auth.go`, `internal/fleet` |
| Deploy bits | `Dockerfile`, `deploy/helm/gpulens`, `.goreleaser.yaml` |

## Commit and pull-request style

Small, focused commits with an imperative subject (`live: carry incident age across restarts`). Describe *why* in the
pull request, list how you tested it, and include screenshots for UI changes. Be honest about what you could not test.

## Reporting bugs and security issues

Use the issue templates. For security vulnerabilities, **do not open a public issue** — use GitHub private
vulnerability reporting (see [SECURITY.md](SECURITY.md#7-reporting-a-vulnerability)).

By contributing you agree that your contributions are licensed under the Apache License 2.0 and that you will follow
the [Code of Conduct](CODE_OF_CONDUCT.md).
