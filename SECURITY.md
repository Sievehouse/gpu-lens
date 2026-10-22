# GPU Lens — Security Policy, Boundaries, and Threat Model

## 1. Non-Negotiable Operational Safety Boundaries

GPU Lens is designed as a **strictly read-only, non-invasive diagnostic and capacity intelligence system**. It enforces the following operational boundaries by design:

1. **Zero Cluster or Workload Mutation**:
   - GPU Lens never creates, updates, patches, deletes, evicts, or scales Kubernetes resources (`Pods`, `Jobs`, `Nodes`, `Deployments`, `DaemonSets`, `ResourceClaims`).
   - GPU Lens never cordons, uncordons, taints, labels, or drains Kubernetes nodes.
2. **Zero Host Execution or Invasive GPU Diagnostics**:
   - GPU Lens never opens SSH sessions, `pods/exec` streams, or privileged host containers.
   - GPU Lens never triggers active GPU stress tests or invasive diagnostics (such as `dcgmi diag -r 2/3` or `nccl-loopback`), which can disrupt production workloads or require exclusive GPU access.
3. **Zero External Data Egress or AI API Dependencies**:
   - GPU Lens contains no OpenAI, Anthropic, Gemini, or other LLM/AI SDKs, no MCP servers, and no external telemetry beacons.
   - All diagnostic evaluation happens locally in process using deterministic rules.

---

## 2. Minimum Kubernetes RBAC Permissions

When the optional live Kubernetes adapter is enabled (`source.mode: "live"` or `kubernetes.enabled: true`), GPU Lens requires only `get`, `list`, and `watch` on three core resources (`nodes`, `pods`, `events`). It requires **zero access to `secrets`, `configmaps`, `pods/log`, or `pods/exec`**.

See `deploy/kubernetes/rbac-readonly.yaml`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: gpulens-reader
  namespace: gpu-lens
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: gpulens-readonly-clusterrole
rules:
  - apiGroups: [""]
    resources:
      - nodes
      - pods
      - events
    verbs:
      - get
      - list
      - watch
  - apiGroups: ["events.k8s.io"]
    resources:
      - events
    verbs:
      - get
      - list
      - watch
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: gpulens-readonly-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: gpulens-readonly-clusterrole
subjects:
  - kind: ServiceAccount
    name: gpulens-reader
    namespace: gpu-lens
```

---

## 3. Defense-in-Depth Read-Only Enforcement

To prevent accidental mutation even if a bug is introduced into a future adapter or if an overly permissive kubeconfig is supplied during local testing:
1. **`ReadOnlyRoundTripper` HTTP Guard (`internal/source/readonly_transport.go`)**:
   - Wraps the underlying `http.RoundTripper` used by the Kubernetes and Prometheus adapters.
   - Explicitly rejects any request whose HTTP method is not `GET` or `HEAD` (blocking `POST`, `PUT`, `PATCH`, `DELETE`) with `ErrMutatingMethodForbidden`.
   - Note on Prometheus: GPU Lens issues `GET /api/v1/query` and `GET /api/v1/query_range` requests with URL-encoded query parameters rather than `POST`, ensuring strict read-only HTTP verb enforcement across all external adapters.
2. **Pod Spec Sanitization**:
   - When reading Kubernetes `Pod` objects, the normalizer extracts only metadata (`name`, `namespace`, `uid`, `ownerReferences`, non-sensitive `labels`), scheduling constraints (`nodeName`, `nodeSelector`, `tolerations`, integer `nvidia.com/gpu` resource requests/limits), and container status (`phase`, `restartCount`, `lastState.terminated`).
   - It **never** reads or stores `spec.containers[].env`, `spec.containers[].command`, `spec.containers[].args`, or annotations, preventing accidental ingestion of secrets passed via environment variables or CLI flags.

---

## 4. Threat Model (STRIDE Analysis)

| Threat Category | Vector / Scenario | Impact | Mitigation Implemented in GPU Lens |
| :--- | :--- | :--- | :--- |
| **Spoofing / Credential Exposure** | Kubeconfig bearer tokens or Prometheus Basic/Bearer credentials leaked in application logs or `/api/v1/sources` responses. | Unauthorized access to Kubernetes API or Prometheus. | `config.Redact()` and structured logger strip `BearerToken`, `BearerTokenFile`, and `Authorization` headers before logging or returning source status over `/api/v1/sources`. |
| **Tampering (Cluster Mutation)** | Compromised config or code path attempts to delete pods or cordon nodes via Kubernetes client. | Production workload disruption or denial of service. | `ReadOnlyRoundTripper` blocks all non-`GET`/`HEAD` HTTP methods in-process; Kubernetes `ClusterRole` restricts verbs to `get`, `list`, `watch`. |
| **Information Disclosure (Multi-Tenant Metadata)** | Pod environment variables or command-line arguments contain proprietary model paths, dataset URLs, or API tokens. | Sensitive tenant data exposed in incident reports or SQLite DB. | Normalizer strips `env`, `command`, `args`, and raw annotations at ingestion; only resource counts, phases, exit codes, and event messages are retained. |
| **Denial of Service (Unbounded Scrape / Query)** | Huge Prometheus range response or slow Kubernetes API hangs the collector or exhausts memory. | GPU Lens process OOM or hung HTTP handlers. | Explicit `context.WithTimeout` on all outbound requests (default `10s`), `io.LimitReader` response body cap (`16 MiB` max), bounded query lookback windows, and HTTP server `ReadHeaderTimeout` / `WriteTimeout`. |
| **Elevation of Privilege** | Container breakout or privileged socket access on GPU nodes. | Host compromise. | GPU Lens runs as a single non-root deployment (`runAsNonRoot: true`, `readOnlyRootFilesystem: true` except `/data` for SQLite) and never mounts `/var/lib/kubelet/pod-resources` or `/dev/nvidia*` directly—it queries Prometheus and the Kubernetes API over HTTP/HTTPS. |
| **Repudiation / Misleading Health Status** | Silent Prometheus scrape failure causes dashboard to show "0 Incidents / Healthy" during an active GPU outage. | Operators miss hardware failures due to monitoring failure. | Explicit stale-data policy (`GPU-FRESH-005`): missing or stale telemetry transitions cluster health to `Degraded / Telemetry Blind Spot` and never defaults to `Healthy`. |

---

## 5. Multi-Tenant, Data Retention, and Production Assumptions

1. **Multi-Tenant Visibility**:
   - The MVP console displays cluster-wide incidents, pods, and capacity across namespaces. In a multi-tenant cluster where tenants must not see other namespaces' pod names, GPU Lens should be deployed behind an authenticating reverse proxy (e.g., oauth2-proxy or gateway) restricted to platform SREs, not exposed directly to untrusted tenant networks.
2. **Data Retention**:
   - Incident history stored in SQLite is bounded by configurable retention (`store.retention_days`, default `14` days) and deduplicated by incident fingerprint so repeated evaluations update counters rather than growing unboundedly.
3. **What Unit Tests Prove vs. What Requires Production Verification**:
   - Unit and integration tests in this repository prove that the `ReadOnlyRoundTripper` rejects mutating HTTP verbs, that sensitive config fields are redacted in API outputs, that malformed inputs are rejected, and that missing/stale metrics trigger blind-spot warnings.
   - Unit tests **do not** replace network policy enforcement (`NetworkPolicy`), TLS certificate validation against a live cluster CA, or RBAC verification (`kubectl auth can-i --list`) in a live production environment.
