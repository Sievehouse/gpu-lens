# Alerting

GPU Lens can tell Slack, or any service that accepts a JSON webhook, when a live incident

* **opens** (`incident.opened`),
* **changes severity** (`incident.severity_changed`), or
* **resolves** (`incident.resolved`, with how long it lasted).

Alerting is off until you configure a destination **at startup**. Destinations can never be set or changed from the
browser or the API, so the console cannot be turned into a request relay. This is the only outbound write GPU Lens
can make; it never touches your telemetry sources.

## Slack

1. In Slack, create an [incoming webhook](https://api.slack.com/messaging/webhooks) for the channel you want.
2. Start GPU Lens with it:

```bash
gpulens -mode live \
  -alert-slack-webhook "$(cat /run/secrets/slack-webhook)" \
  -public-url https://gpulens.example.com          # optional: adds an "Open in GPU Lens" link
```

3. Open **Connections → Alerting → Send a test alert**.

Messages carry the severity, title, summary, rule ID and cluster. Alerts computed from the Live Lab emulator are
prefixed **[EMULATED]**. The webhook URL is a credential: GPU Lens shows only `https://hooks.slack.com/…`.

## Generic webhook (PagerDuty bridges, Opsgenie, n8n, your own service)

```bash
gpulens -mode live \
  -alert-webhook-url https://alerts.example.com/gpulens \
  -alert-webhook-secret-file /run/secrets/gpulens-webhook-secret
```

GPU Lens POSTs this JSON body (schema `gpulens.alert/v1`):

```json
{
  "schema": "gpulens.alert/v1",
  "type": "incident.opened",
  "delivery_id": "9f2c4a…",
  "timestamp": "2026-11-19T15:43:12Z",
  "cluster": "prod-a",
  "data_mode": "live",
  "emulated": false,
  "incident": {
    "id": "inc-health-d310d558", "fingerprint": "d310d55813550f41", "rule_id": "GPU-HEALTH-002",
    "rule_name": "GPU Hardware Fault or Health Degradation", "category": "hardware_health",
    "severity": "critical", "eval_status": "triggered", "evidence_strength": "strong",
    "title": "GPU hardware warning/fault on gpu-node-02/gpu-2 …", "summary": "… XID 79 (GPU has fallen off the bus) …",
    "affected_resources": ["node/gpu-node-02", "gpu/GPU-…"], "first_seen_at": "2026-11-19T15:42:58Z"
  },
  "console_url": "https://gpulens.example.com/?live_tab=incidents",
  "gpulens_version": "0.3.0"
}
```

`incident.severity_changed` adds `previous_severity`; `incident.resolved` adds `duration_seconds`.

Headers: `Content-Type: application/json`, `X-GPULens-Event: <type>`, `X-GPULens-Delivery: <delivery_id>`, and, with
a secret, `X-GPULens-Signature-256: sha256=<hex HMAC-SHA256 of the raw body>`.

### Verifying the signature

```python
import hmac, hashlib

def verify(raw_body: bytes, header: str, secret: str) -> bool:
    expected = "sha256=" + hmac.new(secret.encode(), raw_body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header or "")
```

```js
const crypto = require("crypto");
function verify(rawBody, header, secret) {
  const expected = "sha256=" + crypto.createHmac("sha256", secret).update(rawBody).digest("hex");
  return header && crypto.timingSafeEqual(Buffer.from(expected), Buffer.from(header));
}
```

Always verify against the raw request bytes, before parsing the JSON.

## Behaviour

| Topic | Behaviour |
| :-- | :-- |
| Severity floor | `-alert-min-severity warning` (default) sends warning and critical. An incident that escalates from below the floor is sent as `incident.opened`. |
| Duplicates | Each incident (by fingerprint) is announced once. Restarting collection or the session does not re-announce open incidents. With `-live-state-file`, this survives a GPU Lens restart too. |
| Warm-up | While a new session is settling (first cycles), incidents are never reported as resolved just because their sources have not been read yet. |
| Delivery | A background queue (256 deep) with three attempts (1 s, 4 s back-off) and a 10 s timeout per attempt. Redirects are not followed. A full queue drops the alert and counts it. |
| Status | Connections → Alerting shows sent / failed / dropped counts, last delivery and last error per destination. `GET /api/v1/live/alerts` returns the same. `/metrics` exports `gpulens_alerts_sent_total`, `…_failed_total`, `…_dropped_total`. |
| Emulated data | Alerts computed from the Live Lab carry `"emulated": true` and the Slack text says EMULATED. |

## Helm

```bash
helm upgrade --install gpulens deploy/helm/gpulens \
  --set alerts.slackWebhookUrl=https://hooks.slack.com/services/… \
  --set alerts.publicUrl=https://gpulens.example.com
# or keep the URLs in your own Secret (keys: slack-webhook-url, webhook-url, webhook-secret):
#   --set alerts.existingSecret=gpulens-alerts
```
