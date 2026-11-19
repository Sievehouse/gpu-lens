# syntax=docker/dockerfile:1
# GPU Lens container image. Multi-arch (linux/amd64, linux/arm64) via `docker buildx build --platform ...`.

# Stage 1: build the Go backend. golang:1-alpine tracks the latest Go 1.x release, so the binary always carries a
# patched standard library (go.mod sets only the minimum version).
FROM golang:1-alpine AS backend-builder
ARG VERSION=dev
WORKDIR /app
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X github.com/Sievehouse/gpu-lens/internal/live.Version=${VERSION}" \
      -o /out/gpulens ./cmd/gpulens

# Stage 2: minimal non-root runtime.
FROM alpine:3
ARG VERSION=dev
LABEL org.opencontainers.image.title="GPU Lens" \
      org.opencontainers.image.description="Read-only GPU fleet reliability and capacity console (NVIDIA and AMD)" \
      org.opencontainers.image.source="https://github.com/Sievehouse/gpu-lens" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
RUN apk upgrade --no-cache && adduser -D -u 10001 gpulens && mkdir -p /app/data && chown -R gpulens:gpulens /app
WORKDIR /app
COPY --from=backend-builder /out/gpulens /app/gpulens
COPY web/dist /app/web/dist
COPY fixtures /app/fixtures
COPY config /app/config
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/gpulens"]
CMD ["-listen", ":8080", "-mode", "fixture", "-fixtures-dir", "/app/fixtures", "-static-dir", "/app/web/dist", "-state-file", "/app/data/gpulens-state.json"]
