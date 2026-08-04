# MetaServer external API

English | [简体中文](metaserver-external.zh-CN.md)

Base URL: `https://meta.dubnium.top`. JSON endpoints other than
`/connectServer` use the standard envelope:

```json
{"data": {}, "request_id": "req_..."}
```

Errors use `{"error":{"code":"...","message":"...","details":{}},"request_id":"req_..."}`.
Send the request ID when reporting a problem. Never send a bearer token or Gate
ticket in a URL, query string, log, screenshot, or issue.

## Authentication and rate limits

Public read-only endpoints need no credential. Player endpoints require
`Authorization: Bearer <access-token>` issued by the existing control plane.
They apply the same signature, expiry, revocation, `RequireActive`, request-ID,
IP/player rate-limit, and banned-account rules as other player APIs. Request
player IDs are never accepted as identity.

## Public discovery

| Method and path | Request | Response `data` |
| --- | --- | --- |
| `GET /health/live` | none | process liveness |
| `GET /health/ready` | none | PostgreSQL, Redis, and migration 35 readiness |
| `GET /` | none | service/protocol and dynamic Relay server compatibility list |
| `GET /v1/meta/regions` | none | `items[]` containing region and `qos_endpoints[]` |
| `GET /v1/meta/playlists` | none | enabled playlists ordered by `sort_order` |
| `GET /v1/meta/notifications?locale=en` | optional locale | active notifications for locale plus global fallback |

Relay endpoints are not static configuration. Only nodes that are READY,
heartbeat-fresh, and accepting new allocations appear.

## Session and MetaTunnel

`POST /v1/meta/sessions` accepts:

```json
{"client_version":"1.1.0","protocol_version":1,"platform":"windows"}
```

It returns HTTP 201 with `user_id`, `gate_ticket`, `endpoint`,
`expires_in_seconds`, and `protocol_version`. The ticket has 256 bits of random
entropy, expires after at most 60 seconds, and can be consumed once.

The game compatibility path is `POST /connectServer`. MetaTunnel calls it with
the bearer header and one JSON object. Shipped Boundary builds disagree on both
the names and JSON types of their release-specific fields, so every body field
is ignored. The server labels these clients `boundary-legacy` and selects its
own protocol version. Identity always comes from the bearer token, never from
legacy `playerId` or `loginToken` values. Modern MetaServer endpoints continue
to enforce their typed schemas. The compatibility path's direct game-shaped
response is:

```json
{"error":0,"userId":"...","aceId":"...","gateToken":"...","endpoint":"logic.dubnium.top:443"}
```

The Browser must launch `meta-tunnel.exe`, write only the Access Token followed
by a newline to its anonymous stdin pipe, read the one-line readiness JSON, then
set the game's LogicServerURL to the reported loopback HTTP port. MetaTunnel
rewrites the endpoint to its loopback TCP listener and validates the remote TLS
certificate; applications must not implement a certificate bypass.

## Player profile and loadouts

| Method and path | Body | Result |
| --- | --- | --- |
| `GET /v1/users/me/meta-profile` | none | level, XP, currencies, statistics, revision |
| `GET /v1/users/me/loadouts` | none | all role loadouts |
| `GET /v1/users/me/loadouts/{role_id}` | none | one definition-validated snapshot and revision |
| `PUT /v1/users/me/loadouts/{role_id}` | `snapshot` object, current `revision` | updated snapshot and incremented revision |

Use revision `0` only for first creation. Every later update must send the
revision returned by the last read/write. A stale value returns HTTP 409
`META_LOADOUT_REVISION_CONFLICT`; read the resource and merge deliberately
instead of blindly retrying.

## Party

| Method and path | Body | Result |
| --- | --- | --- |
| `POST /v1/meta/parties` | `mode`, `region`, `client_version` | new Party with caller as leader |
| `GET /v1/meta/parties/{party_id}` | none | Party only when caller is a member |
| `POST /v1/meta/parties/{party_id}/ready` | `{"ready":true}` | updated Party |
| `POST /v1/meta/parties/{party_id}/presence` | `{"presence":"ONLINE"}` | updated Party |

Presence is `ONLINE`, `AWAY`, or `IN_GAME`. A player may belong to one active
Party. Unauthorized membership lookup is hidden as not found.

## Matchmaking

Create with `POST /v1/meta/matchmaking/tickets`:

```json
{"party_id":"mp_...","mode":"default","region":"hgh","client_version":"1.1.0"}
```

Omit `party_id` for a solo ticket. A Party queues as a unit and only its leader
may start it. The response is HTTP 202. Poll
`GET /v1/meta/matchmaking/tickets/{ticket_id}` until `state` becomes `MATCHED`,
`FAILED`, or `TIMED_OUT`. On `MATCHED`, `match_id` and the assigned Dedicated
Server `endpoint` are present. Cancel a queued ticket with `DELETE` and expect
204. There is no automatic player-hosted P2P fallback when no server is ready.

## Common Meta errors

| Status/code | Meaning |
| --- | --- |
| 400 `META_INVALID_REQUEST` | malformed JSON, unsupported definition, invalid label or transition input |
| 401 standard auth code | missing, expired, revoked, or invalid Access Token |
| 404 `META_*_NOT_FOUND` | absent resource or ownership/membership intentionally hidden |
| 409 `META_LOADOUT_REVISION_CONFLICT` | stale loadout revision |
| 409 `META_PARTY_ALREADY_JOINED` | player already belongs to an active Party |
| 409 `META_PARTY_TICKET_REQUIRED` | an active Party member must queue with the Party ID |
| 409 `META_PARTY_NOT_QUEUEABLE` | Party state or membership is not eligible for queueing |
| 409 `META_MATCH_TICKET_EXISTS` | player already has an active Ticket |
| 409 `META_MATCH_TICKET_NOT_CANCELLABLE` | Ticket has left the queued state |
| Ticket failure `META_MATCH_CONNECTION_TIMEOUT` | the Dedicated Server reservation expired before a player connected |
| Ticket failure `META_MATCH_CANCELLED_BY_ADMIN` | an audited administrator action cancelled the active match |
| 429 standard rate-limit code | retry only after the indicated delay |

The complete field schema is
[`Backend/api/openapi/openapi.yaml`](../../Backend/api/openapi/openapi.yaml).
Native TCP behavior is documented separately in the
[native protocol](../architecture/metaserver-native-protocol.md).
