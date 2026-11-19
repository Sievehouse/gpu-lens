## What and why

<!-- What does this change, and why is it needed? Link the issue if there is one. -->

## How I tested it

<!-- Commands you ran, hardware or emulator you used, screenshots for UI changes. Say what you could not test. -->

## Checklist

- [ ] GPU Lens stays read-only towards the infrastructure it observes
- [ ] Missing data is still never shown as healthy or zero
- [ ] `go test -race ./...` and `python3 tests/live_blackbox.py both` pass
- [ ] Live-API changes are in both runtimes (Go and Python) and in `docs/openapi.yaml`
- [ ] `web/dist` is rebuilt and committed if `web/src` changed
- [ ] `CHANGELOG.md` has a line under **Unreleased**
