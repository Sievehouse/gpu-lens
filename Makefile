.PHONY: all demo live live-lab build test test-live verify-local fixtures frontend lint docker-up clean vuln helm-lint release-snapshot docker-multiarch

GO_BIN := $(shell command -v go 2>/dev/null)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/Sievehouse/gpu-lens/internal/live.Version=$(VERSION)

all: fixtures frontend test

## Generate deterministic offline scenario JSON fixtures
fixtures:
	python3 scripts/generate_fixtures.py

## Compile TypeScript + TSX frontend into web/dist
frontend:
	node web/scripts/build_offline.js

## Build Go binary (or verify local runtime when Go toolchain is not installed on host)
build: frontend
ifdef GO_BIN
	gofmt -s -w cmd/ internal/
	go vet ./...
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/gpulens ./cmd/gpulens
else
	@echo "[Info] 'go' binary not found in PATH; using verified local runtime engine (scripts/local_runtime_server.py)."
endif

## Run automated unit & integration tests (Go test suite + offline verification harness + live API black-box tests)
test: fixtures frontend
ifdef GO_BIN
	go vet ./...
	go test -race ./...
endif
	python3 scripts/run_verification_suite.py
	python3 tests/live_blackbox.py both

## Black-box test of the Go Live API against the real server(s): bare machine, request guards, nvidia-smi test double, Live Lab
test-live: frontend
	python3 tests/live_blackbox.py both

## Run offline verification harness explicitly
verify-local: fixtures frontend
	python3 scripts/run_verification_suite.py

## Start the offline demo server on http://127.0.0.1:8080
demo: fixtures frontend
ifdef GO_BIN
	go run ./cmd/gpulens -listen :8080 -mode fixture -fixtures-dir ./fixtures -static-dir ./web/dist
else
	python3 scripts/local_runtime_server.py --port 8080 --fixtures-dir ./fixtures --static-dir ./web/dist
endif

## Start with live collection already running (auto-detects nvidia-smi, DCGM on :9400, Prometheus on :9090, kubectl proxy on :8001)
live: frontend
ifdef GO_BIN
	go run ./cmd/gpulens -listen :8080 -mode live -fixtures-dir ./fixtures -static-dir ./web/dist
else
	python3 scripts/local_runtime_server.py --port 8080 --mode live --fixtures-dir ./fixtures --static-dir ./web/dist
endif

## Start the Live Lab: a clearly-labelled SYNTHETIC dcgm-exporter (:9400) + Kubernetes API (:8001) so Go Live can be tried without GPUs
live-lab:
	python3 scripts/live_lab.py

## Start via Docker Compose
docker-up:
	docker compose up --build

clean:
	rm -rf bin/ gpulens.db gpulens-state.json

## Scan Go code and the standard library for known vulnerabilities (needs network access to vuln.go.dev)
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd web && npm audit --audit-level=high

## Lint the Helm chart (needs helm)
helm-lint:
	helm lint deploy/helm/gpulens

## Build release archives locally without publishing (needs goreleaser)
release-snapshot:
	goreleaser release --snapshot --clean

## Build the container image for linux/amd64 and linux/arm64 (needs docker buildx)
docker-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t gpu-lens:$(VERSION) .
