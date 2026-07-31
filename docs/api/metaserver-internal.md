# MetaServer internal and administrative API

English | [简体中文](metaserver-internal.zh-CN.md)

These routes are not client APIs. Internal Dedicated Server routes stay on the
private Meta origin. Administrative routes are additionally constrained by the
trusted admin network at the reverse proxy and application middleware.

## Dedicated Server authentication

Each request sends both:

```http
Authorization: Bearer gst_<opaque-token>
X-Game-Server-Id: <server-id>
```

The database stores only the token hash. The token must be unexpired, identify
the same server, include the required Meta match scope, and belong to a server
that is not DRAINING, UNHEALTHY, or OFFLINE. A valid credential still grants no
global player access: the repository checks that the match is active, assigned
to this server, and contains the requested player.

| Method and path | Required scope | Behavior |
| --- | --- | --- |
| `GET /internal/v1/meta/matches/{match_id}/players/{player_id}/loadout` | `meta.loadouts.read` | returns the definition-validated match snapshot |
| `POST /internal/v1/meta/matches/{match_id}/players/{player_id}/connected` | `meta.matches.connect` | marks only that roster member connected; starts the reserved match when applicable |
| `POST /internal/v1/meta/matches/{match_id}/completed` | `meta.matches.complete` | accepts `{"result":{...}}`, completes match, releases server to READY |
| `PUT /internal/v1/meta/battlelog/reports/{report_id}` | `meta.battlelog.write` | validates and idempotently persists a schema-v2 server snapshot, then completes a linked match |

BattleLog identity and integrity decisions reuse the existing security model.
The match roster snapshots `unverified`, `verified`, or `trusted` when the match
is reserved. Only `verified` and `trusted` roster members become official
participants. The raw report never supplies this level. Validation findings use
the existing `LOW`, `MEDIUM`, `HIGH`, and `CRITICAL` severity vocabulary.

The same `report_id` and canonical SHA-256 returns `200` as a safe retry. Reusing
the ID with different content returns `409`. If the snapshot has no match ID,
the backend links the Game Server's unique active assignment. With no active
assignment it retains the report as non-official standalone evidence.

Return 404/403 semantics deliberately avoid revealing players or matches
assigned elsewhere. Do not retry DRAINING/OFFLINE or assignment failures with a
different player ID.

## Administrative authentication

All `/v1/admin/meta/*` requests require:

1. source address in the configured trusted admin CIDRs;
2. a current human administrator Access Token, never a player/static token;
3. the endpoint permission;
4. for writes, `X-Admin-Step-Up` bound to the same session;
5. a `reason` field of 8–1000 characters without credentials.

Every successful write records administrator, action, target, redacted
old/new values, reason, request ID, client address, user agent, result, and
timestamp in the existing audit log.

| Method and path | Permission | Step-up | Body/result |
| --- | --- | --- | --- |
| `GET /v1/admin/meta/overview` | `meta.read` | no | profile, active Party, queued Ticket, active match counts |
| `GET /v1/admin/meta/players/{player_id}/loadouts` | `meta.loadouts.read` | no | player loadouts |
| `PUT /v1/admin/meta/players/{player_id}/loadouts/{role_id}` | `meta.loadouts.update` | yes | `snapshot`, current `revision`, `reason` |
| `GET /v1/admin/meta/matches` | `meta.read` | no | last 100 matches |
| `POST /v1/admin/meta/matches/{match_id}/cancel` | `meta.matches.manage` | yes | `reason`; atomically cancels and releases reservation |
| `PUT /v1/admin/meta/playlists/{slug}` | `meta.content.manage` | yes | display fields, mode, definition, enabled, order, reason |
| `PUT /v1/admin/meta/notifications/{id}` | `meta.content.manage` | yes | localized content, window, enabled, priority, reason; use ID `new` to create |

Admin loadout updates use the same optimistic revision rule as player updates.
Step-up and permission failures are not bypassed by using the Meta HTTP origin
directly because enforcement is in the Go service.

## Metrics

`GET /internal/metrics` exposes Prometheus text only to the private monitoring
network. Important metric families include:

- HTTP request totals/latency and readiness;
- active/total native TCP connections, RPC count/latency, malformed frames;
- Gate issue, consume, and replay;
- loadout revision conflicts;
- matchmaking queue depth and assignment outcomes/latency;
- BattleLog reports by PvE/PvP type, validation status, and idempotent replay;
- Relay `0x59` QoS request, malformed, and rate-limit counters.

Grafana discovers the service through `service="project-rebound-meta-server"`;
adding a MetaServer replica must not require a hard-coded dashboard panel.

## Import utility

`meta-import` is an offline operator tool, not an HTTP endpoint. It never mounts
legacy JSON into the production service. Default execution is dry-run:

```bash
meta-import --source /secure/export --database-url "$DATABASE_URL"
meta-import --source /secure/export --database-url "$DATABASE_URL" --apply
```

Dry-run validates definitions, maps players by current ID or Steam ID, and emits
conflict/error reports without writing. `--apply` uses one serializable
transaction and must be run only after backup and report review. The source
directory remains read-only.

The authoritative schemas, security schemes, and error envelopes are in
[`openapi.yaml`](../../Backend/api/openapi/openapi.yaml).
