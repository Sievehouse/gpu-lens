# Fleet: several clusters in one view

Run one GPU Lens per Kubernetes cluster or GPU host — each keeps its own collection, rules, alerts and audit log —
and let any one of them act as a **hub** that shows them all on the **Fleet** tab.

![Fleet view](screenshots/08-golive-fleet.png)

## Set it up

On every member, run GPU Lens as usual (ideally with `-mode live`, so it collects with no browser attached).

On the hub, add each member with `-peer name=URL`:

```bash
gpulens -mode live \
  -peer cluster-a=https://gpulens.cluster-a.example.com \
  -peer cluster-b=https://gpulens.cluster-b.example.com \
  -peer gpu-box-7=http://10.0.4.7:8080
# or: GPULENS_PEERS="cluster-a=https://…,cluster-b=https://…"
```

If a member requires basic auth, put a **viewer** account in its URL: `https://viewer:password@gpulens.cluster-a…`.
The hub never displays the credentials (they show as `***@`).

The Fleet tab appears as soon as at least one peer is configured. It refreshes every `-fleet-interval` seconds
(default 15).

## What the hub reads

Every interval the hub calls `GET <peer>/api/v1/live/summary` on each member, through the same GET-only transport
used for every telemetry source. The summary is small and contains counts and verdicts only:

* cluster name, GPU Lens version and runtime, collection state, preflight verdict, data mode (and whether it is
  emulated),
* GPUs total / fresh, nodes, open incidents by severity and the titles of the top three,
* sources connected / enabled, time of the last collection cycle.

No tokens, pod names beyond the incident titles, or raw telemetry leave the member.

## How to read it

| Card | Meaning |
| :-- | :-- |
| Green edge | Reachable, no warning or critical incidents |
| Amber / red edge | Warning / critical incidents are open on that member |
| Grey edge, **unreachable** | The hub could not get a summary: the error says why (timeout, connection refused, `401` = needs credentials, `404` = older GPU Lens or live console disabled). Never shown as healthy. |
| "No GPU telemetry on this member" | It answered, but nothing is reporting GPUs. Not the same as healthy. |

Totals at the top add up the reachable members; **Need attention** counts unreachable, blocked or critical members.
Each peer card links to that member's own console for the details.

## Limits

* The hub aggregates *health*, it does not merge every member's GPUs into one rule evaluation, and alerts stay per
  member (configure alerting on each member).
* Members are polled over plain HTTP(S); put TLS on members that cross untrusted networks.
* The API is `GET /api/v1/fleet` on the hub (see [openapi.yaml](openapi.yaml)).
