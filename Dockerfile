# Stage 1: Build Go Backend Binary
FROM golang:1.22-alpine AS backend-builder
WORKDIR /app
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gpulens ./cmd/gpulens

# Stage 2: Minimal Non-Root Runtime Container
FROM alpine:3.20
RUN adduser -D -u 10001 gpulens && mkdir -p /app/data && chown -R gpulens:gpulens /app
WORKDIR /app
COPY --from=backend-builder /out/gpulens /app/gpulens
COPY web/dist /app/web/dist
COPY fixtures /app/fixtures
COPY config /app/config
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/gpulens"]
CMD ["-listen", ":8080", "-mode", "fixture", "-fixtures-dir", "/app/fixtures", "-static-dir", "/app/web/dist", "-state-file", "/app/data/gpulens-state.json"]
