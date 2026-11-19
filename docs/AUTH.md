# Access control and the audit log

GPU Lens reads your infrastructure but never writes to it. Its own console, however, has a few controls that change
what it does: live rule thresholds, connection endpoints, pausing collection, test alerts. When more than one person
can reach the console, protect those controls.

## Option 1 — built-in basic auth with two roles

```bash
echo -n 'a-long-admin-password' > /run/secrets/gpulens-admin
echo -n 'a-long-viewer-password' > /run/secrets/gpulens-viewer
gpulens -mode live \
  -basic-auth-user admin  -basic-auth-pass-file  /run/secrets/gpulens-admin \
  -viewer-user     oncall -viewer-pass-file      /run/secrets/gpulens-viewer \
  -audit-log-file /var/lib/gpulens/audit.jsonl
```

| Role | Can | Cannot |
| :-- | :-- | :-- |
| **admin** | Everything: thresholds, connections, pause / resume / re-check, refresh interval, test alerts | — |
| **viewer** | Read every page and API, resume collection (to start watching), switch simulation scenarios | Change thresholds, connections, interval; pause; re-check; send test alerts |

* Every path except `/healthz` and `/readyz` (kept public for load-balancer and Kubernetes probes) requires
  credentials, including `/metrics` — configure `basic_auth` in your Prometheus scrape job (the Helm chart's
  ServiceMonitor does this for you).
* Passwords are compared in constant time. The admin password must be at least 8 characters.
* Prefer `-…-pass-file` or `GPULENS_…_PASS` environment variables over `-…-pass` flags: command lines are visible to
  other users of the machine.
* Viewers see a *Read-only access* banner; buttons they cannot use are disabled. The server enforces the role either
  way, so a viewer calling the API directly gets `403`.
* Connection settings (endpoints and tokens) additionally require a browser on the machine running GPU Lens, or
  `-live-allow-remote-config`. Basic auth does not lift that rule on its own.

**Basic auth sends the password with every request. Use TLS** whenever GPU Lens is reachable from another machine —
see the proxy examples below.

The Python runtime takes the same settings as `--basic-auth-user`, `--viewer-user`, `--audit-log-file`, and so on.

## Option 2 — your single sign-on in front (reverse proxy)

If your organisation already has SSO, put an authenticating proxy in front and keep GPU Lens on localhost or a
private network. GPU Lens treats any request that arrives with `X-Forwarded-For` / `Forwarded` / `X-Real-IP` as
remote, so connection settings stay read-only behind a proxy unless you start it with `-live-allow-remote-config`.

**nginx with TLS and basic auth**

```nginx
server {
  listen 443 ssl;
  server_name gpulens.example.com;
  ssl_certificate     /etc/ssl/gpulens.crt;
  ssl_certificate_key /etc/ssl/gpulens.key;

  auth_basic           "GPU Lens";
  auth_basic_user_file /etc/nginx/gpulens.htpasswd;

  location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
  }
}
```

**oauth2-proxy (GitHub, Google, Okta, Azure AD …)**

```bash
oauth2-proxy --provider=github --github-org=your-org \
  --upstream=http://127.0.0.1:8080 --http-address=0.0.0.0:4180 \
  --cookie-secure=true --email-domain='*'
```

With a proxy you can still enable GPU Lens's own basic auth for defence in depth (the proxy then has to forward the
`Authorization` header), or rely on the proxy alone.

**Kubernetes:** `helm install … --set auth.enabled=true --set auth.adminPassword=… --set ingress.enabled=true` and
add your ingress controller's TLS / auth annotations under `ingress.annotations`.

## The audit log

Every action that changes GPU Lens — and every refused attempt — is recorded:

| Action | Recorded when |
| :-- | :-- |
| `live.thresholds` | Thresholds are set or reset (the exact keys and values) |
| `live.config` | Endpoints, interval, cluster name or source toggles change (tokens appear only as *token set* / *token cleared*) |
| `live.start`, `live.stop`, `live.recheck` | Collection is started (when it was not running), paused, or re-checked |
| `alerts.test` | A test alert is sent |
| `simulation.scenario.select` | The active simulation scenario changes |

Each entry has a sequence number, time, actor (the basic-auth user, or `anonymous` without auth), role, remote
address (plus the first `X-Forwarded-For` hop when present), action, detail and outcome (`ok`, `denied`, `error`).

* **Console:** Activity tab → filter **Audit (who changed what)**.
* **API:** `GET /api/v1/audit?since=<seq>`.
* **File:** with `-audit-log-file`, entries are appended as JSON lines (file mode `0600`) and reloaded at startup, so
  the record survives restarts. Ship the file to your log pipeline if you need retention beyond one machine.

Secrets are never written to the audit log: not tokens, not passwords, not credentials embedded in URLs.
