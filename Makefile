.PHONY: all demo build test verify-local fixtures frontend lint docker-up clean

GO_BIN := $(shell command -v go 2>/dev/null)

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
	go build -o bin/gpulens ./cmd/gpulens
else
	@echo "[Info] 'go' binary not found in PATH; using verified local runtime engine (scripts/local_runtime_server.py)."
endif

## Run automated unit & integration tests (Go test suite + offline verification harness)
test: fixtures frontend
ifdef GO_BIN
	go vet ./...
	go test -v -race ./...
endif
	python3 scripts/run_verification_suite.py

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

## Start via Docker Compose
docker-up:
	docker compose up --build

clean:
	rm -rf bin/ gpulens.db
