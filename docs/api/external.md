# ProjectRebound External API

English | [简体中文](external.zh-CN.md)

This document describes the APIs accessible to clients, Dedicated Servers, and Updaters. Machine-readable field types, lengths, enumerations, and all response schemas are subject to `Backend/api/openapi/openapi.yaml`; this file is verified with the routing contract test.

## 1. Connection conventions

- Production Base URL: `https://api.example.com`
- HTTP request and response: `application/json; charset=utf-8`
- WebSocket: `wss://api.example.com/v1/realtime/connect`
- Time: RFC 3339 UTC, for example `2026-07-18T12:00:00Z`
- ID: Opaque string with resource prefix; clients must not parse or construct it themselves.
- Paging: `cursor` + `limit`, `limit` range 1–100, default 50.
- Idempotency: Repeated logout, leave, close, and deregister operations return the current final state; clients should still avoid concurrent duplicate writes.

Except for the diagnostic-report upload described below, the successful response is unified as:

```json
{
  "data": {},
  "request_id": "req_..."
}
```

The failure response is unified as:

```json
{
  "error": {
    "code": "INVALID_REQUEST",
    "message": "Request validation failed.",
    "details": {}
  },
  "request_id": "req_..."
}
```

The client can send `X-Request-Id`, but the server will verify and normalize it; when troubleshooting, the `request_id` in the response shall prevail. The request body limit is 1 MiB by default.

## 2. Authentication types

| Name | Header | How to obtain | Use |
| --- | --- | --- | --- |
| Player Access Token | `Authorization: Bearer <jwt>` |`/v1/auth/bind` or `/refresh`|Player write operations, personal information, connection/WebSocket|
| Refresh Token | JSON field `refresh_token` | Bind/refresh response | Rotate the Access Token; the previous value expires after each use |
| Game Server Registration Token | `Authorization: Bearer <token>` | Preconfigured by operators | Register a Dedicated Server |
| Game Server Token | `Authorization: Bearer <token>` | Registration response; returned only once | Server heartbeat and deregistration |
| Room Host Token | `X-Room-Host-Token: <token>` | Room-creation response; returned only once | Host heartbeat, start, and shutdown operations |

Players at `account_status=BANNED` can still bind, refresh, logout, and read personal data, but cannot perform room or connection write operations. A bind without `encrypted_ticket` remains compatible and issues an `unverified` session. A valid Steam Encrypted App Ticket issues a `verified` session; only verified sessions can perform game, room, connection, and MetaServer operations.

## 3. Endpoint summary

### 3.1 Health and client configuration

| Method | Path | Authentication | Success | Description |
| --- | --- | --- | --- | --- |
| GET | `/health/live` | None | 200 | The process is alive; dependencies are not checked |
| GET | `/health/ready` | None | 200/503 | Required dependencies such as PostgreSQL and Redis are available |
| GET | `/v1/client/config` | None | 200 | Protocol version, feature flags, STUN, and available Relay regions |

Client configuration does not return specific Relay addresses. The specific endpoint is only delivered through WebSocket events after the connection is scheduled.

### 3.2 Authentication and players

| Method | Path | Authentication | Request | Success |
| --- | --- | --- | --- | --- |
| POST | `/v1/auth/bind` | None; rate-limited by IP, SteamID, Device ID, and combined dimensions | Required: `steam_id`, `persona_name`; optional: `device_id`, `invite_code`, `encrypted_ticket` | 200 Player + Access/Refresh Token + authentication level + initial integrity nonce |
| POST | `/v1/auth/refresh` | None | `refresh_token` | 200 new Access/Refresh Token; the old Refresh Token expires |
| POST | `/v1/auth/logout` | Player | None | 200 current session revoked |
| GET | `/v1/users/me` | Player | None | 200 current player status and permission fields |
| GET | `/v1/users/me/sessions` | Player | None | 200 active sessions for the current player |
| DELETE | `/v1/users/me/sessions/{session_id}` | Player | Path session ID | 200 specified session revoked |
| POST | `/v1/users/me/sessions/revoke-others` | Player | None | 200 all sessions except the current session revoked |
| POST | `/v1/integrity/challenge` | Player | None | 200 fresh one-time `nonce`; empty when no verified ticket is held in memory |
| POST | `/v1/integrity/proof` | Player | `nonce`, `proof`, `component=toolbox` | 200 `ok`; success promotes the session to `trusted` |
| POST | `/v1/integrity/verify` | Player | Same as `/proof` | Deprecated compatibility alias |
| POST | `/v1/diagnostic/report` | Player | Required raw diagnostic-text field `report` | 200 bare `{"ok":true}` after the text is stored |

Bind example:

```http
POST /v1/auth/bind HTTP/1.1
Content-Type: application/json

{
  "steam_id": "76561198000000000",
  "persona_name": "Player",
  "device_id": "hardware-uuid|disk-serial|cpu-id",
  "encrypted_ticket": "0123456789abcdef",
  "invite_code": "TEST-ABCD-EFGH"
}
```

Old clients can continue to omit optional fields or send an opaque installation ID; these sessions remain `unverified`. New clients submit a hexadecimal `encrypted_ticket`. The backend passes it only through stdin to the configured external verifier and uses the decrypted SteamID as authoritative. A ticket is accepted when decryption succeeds and its SteamID matches the requested SteamID; AppID, issue time, VAC state, and prior use do not gate bind. Plaintext tickets are never persisted.

For a verified bind, `data.integrity_challenge.nonce` contains the first one-time challenge. The client computes `SHA256(PE_certificate_bytes || decoded_encrypted_ticket_bytes || nonce_ascii)` and submits its 64-character hexadecimal digest to `/v1/integrity/proof`. Each challenge replaces the previous nonce. A correct proof sets the session to `trusted`; three consecutive failures revoke it. Challenge and raw-ticket state is process-local, so an empty nonce after a backend restart means the client must bind again.

New clients may send `uuid|disk|cpu`. The server independently HMAC-hashes each factor. It also accepts `v1|uu:<digest>|ds:<digest>|cp:<digest>`, where each digest is exactly 16 hexadecimal characters. A factor may be omitted in the versioned form. Legacy opaque printable-ASCII values without pipes remain accepted.

`device_id` is up to 128 printable ASCII bytes. It is only used for throttling and risk observation, is not a trusted identity, and will not bypass SteamID unique constraints. The server never stores the three submitted factor values directly: it creates separate domain-separated HMAC-SHA-256 digests plus a composite digest, links the resulting internal fingerprint record to sessions and login/risk events, and does not expose those digests through the external API. Whether to require an invitation code is determined by the server `auth.invite_required` configuration. When the binding exceeds the limit of any dimension, `429 AUTH_BIND_RATE_LIMITED` is returned, and the response contains both `Retry-After` and `details.retry_after_seconds`.

Do not write Access/Refresh Tokens to URLs, logs, or crash reports. When Refresh Token is replayed, the server will revoke the entire token family.

The diagnostic endpoint associates the report with the `player_id` from the Access Token and stores the submitted string verbatim. It does not parse, validate, or index the report content. Its bare `{"ok":true}` success body is an intentional compatibility exception to the standard success envelope; errors still use the standard error envelope.

The session list returns only the session ID, a four-character device display suffix, creation and last-used times, a masked IP address, and `is_current`. For a structured fingerprint the suffix comes from its opaque internal record ID, not from any hardware factor; for a legacy opaque Device ID it remains the last four characters. The API never returns token hashes or the complete device identifier. Deleting a session that does not belong to the current player returns the same 404 response as a nonexistent session to prevent cross-account enumeration.

### 3.3 Dedicated Server

|method|path|Authentication|Request/Query|success|
| --- | --- | --- | --- | --- |
| POST | `/v1/game-servers` | Registration Token | `instance_id`, `display_name`, `region`, `mode`, `version`, `public_host`, `public_port`, `max_players` | 201 Server + one-time `server_token` |
| GET | `/v1/game-servers` | None | `region`, `mode`, `version`, `state`, `cursor`, `limit` | 200 public directory |
| GET | `/v1/game-servers/{server_id}` |none| — |200 public status|
| POST | `/v1/game-servers/{server_id}/heartbeat` |Corresponding Server Token| `state`, `player_count` |200 status and next heartbeat information|
| DELETE | `/v1/game-servers/{server_id}` | Corresponding Server Token | — | 200 deregister and revoke token |

`instance_id` registration is idempotent. Server Token can only manage `server_id` in the response. It is recommended to heartbeat every 15 seconds; the default is 45 seconds to `UNHEALTHY` and 90 seconds to `OFFLINE`. Public responses do not contain token hashes, internal audit fields, or other server secrets.

### 3.4 P2P Room

| Method | Path | Authentication | Request/additional header | Success |
| --- | --- | --- | --- | --- |
| GET | `/v1/p2p-rooms` | None | `region`, `mode`, `version`, `state`, `has_slots`, `cursor`, `limit` | 200 public directory |
| GET | `/v1/p2p-rooms/{room_id}` | None | — | 200 public room status |
| POST | `/v1/p2p-rooms` | Active Player | `display_name`, `region`, `mode`, `version`, `max_players` | 201 room + one-time `host_token` |
| POST | `/v1/p2p-rooms/{room_id}/join` | Active Player | `version` | 200 joined; repeated calls are idempotent |
| POST | `/v1/p2p-rooms/{room_id}/leave` | Active Player | — | 200 left; repeated calls are idempotent |
| POST | `/v1/p2p-rooms/{room_id}/heartbeat` | Active Player + Host Token | `X-Room-Host-Token` |200 heartbeats|
| POST | `/v1/p2p-rooms/{room_id}/start` | Active Player + Host Token | `X-Room-Host-Token` | 200 `LOBBY -> CONNECTING` |
| DELETE | `/v1/p2p-rooms/{room_id}` | Active Player + Host Token | `X-Room-Host-Token` |200 Close; call idempotent repeatedly|

Public room responses do not return candidate addresses, host tokens, or member secrets. The host cannot call leave and must close the room. By default, if there is no host heartbeat for 45 seconds, it will enter the expiration process, and it will be closed after 90 seconds. A valid host heartbeat also renews all non-terminal connections to the room in the same database transaction; final connections are not restored.

### 3.5 P2P BattleLog v3

All endpoints require an active, Steam-verified Player access token and frozen-roster membership. P2P evidence is stored separately from dedicated-server `battlelog_*` data.

| Method | Path | Additional authentication/body | Success |
| --- | --- | --- | --- |
| GET | `/v1/p2p-rooms/{room_id}/matches/active` | — | 200 server-created match context |
| POST | `/v1/p2p-matches/{match_id}/report-capability` | — | 201 session-family-bound `report_token`, capability ID, and nonce |
| PUT | `/v1/p2p-matches/{match_id}/presence/me` | Monotonic presence sequence and process/connection status | 200 presence or reconnect segment |
| PUT | `/v1/p2p-matches/{match_id}/reports/{report_id}` | `X-P2P-Report-Token`; direct raw v3 JSON body | 200 accepted, quarantined, or idempotent duplicate |
| GET | `/v1/p2p-matches/{match_id}/result` | — | 200 collection progress or final decision |

Launcher must retain the report token and add it only during upload; the injected DLL receives only the non-secret match ID, capability ID, and server nonce. One immutable `FINAL` report is accepted per reporter. Missing reporters—including a host or player that leaves early—do not block indefinitely: the first final report, all reporters reaching result/left state, or closure of the room opens the collection deadline. The service then records peer-confirmed, self-reported, disputed, incomplete, or expired status. `PARTIAL` reports remain evidence but do not count toward final quorum.

### 3.6 Connection coordination and WebSocket

| Method | Path | Authentication | Request | Success |
| --- | --- | --- | --- | --- |
| POST | `/v1/connections` | Active Player | `room_id`, `peer_player_id` |201 Create or return an existing active connection|
| GET | `/v1/connections/{connection_id}` |Player, must be a participant| — |200 Current status and peer candidates|
| DELETE | `/v1/connections/{connection_id}` |Active Player, must be a participant| — |200 closed|
| GET/WSS | `/v1/realtime/connect` | Active Player | WebSocket Upgrade | 101 |

WebSocket's `Authorization` must be placed in the Header and is prohibited from being placed in query parameters. JSON envelope:

```json
{
  "type": "connection.candidate",
  "connection_id": "conn_...",
  "payload": {}
}
```

Client uplink events:

- `connection.candidate`: Submit an ICE/NAT candidate.
- `connection.check_result`: Report direct connection check results and selected paths.

Server-to-client events:

- `connection.candidate`
- `connection.check_result`
- `connection.relay_allocated`
- `connection.relay_migrating`
- `connection.relay_migrated`
- `connection.relay_failed`
- `error`

Relay distribution event example:

```json
{
  "type": "connection.relay_allocated",
  "connection_id": "conn_...",
  "payload": {
    "allocation_id": "alloc_...",
    "relay": {
      "node_id": "relay_...",
      "protocol": "UDP",
      "host": "203.0.113.20",
      "port": 8443
    },
    "relay_token": "...",
    "expires_at": "2026-07-18T12:05:00Z"
  }
}
```

For the specific fields and enumerations of the event, see `Connection*Event`, `ConnectionData`, and `RelayTokenClaims` in OpenAPI. The client should handle events `connection_id` idempotently, re-GET the current connection status after disconnection, and should not repeatedly create rooms or connections due to reconnection.

### 3.6 Update

| Method | Path | Authentication | Query | Success |
| --- | --- | --- | --- | --- |
| GET | `/v1/updates/check` | None | Required: `platform`, `current_version`; optional: `architecture`, `channel` | 200 latest version and whether the update is available/mandatory |
| GET | `/v1/updates/{platform}/{version}/manifest` | None | `architecture`, `channel` | 200 Ed25519-signed manifest |
| GET | `/v1/updates/files/{file_id}` | None | — | 200 immutable CDN download metadata |

`channel` is `stable`, `beta`, or `toolbox`. `beta` publishes the complete game `Release.zip`; `toolbox` publishes only `Rebound_Toolbox.exe`. Manifest contains `schema_version`, product/platform/architecture/channel/version, minimum supported version, release date, file list, `manifest_hash`, `signature_algorithm=Ed25519`, `key_id` and `signature`. Updaters must:

1. Verify the signature using the public key built into the client and corresponding to `key_id`;
2. Verify manifest hash;
3. Download from CDN;
4. Verify exact file size and SHA-256;
5. Do not install if any step fails.

## MetaServer route index

MetaServer uses the existing player Access Token and the same error envelope.
The complete field-level contract and MetaTunnel flow are documented in the
[MetaServer external API](metaserver-external.md).

| Method | Path | Authentication | Purpose |
| --- | --- | --- | --- |
| POST | `/connectServer` | Active Player | MetaTunnel-compatible Gate bootstrap |
| GET | `/v1/meta/regions` | None | Dynamic READY Relay/QoS discovery |
| GET | `/v1/meta/playlists` | None | Enabled matchmaking playlists |
| GET | `/v1/meta/notifications` | None | Active localized notifications |
| POST | `/v1/meta/sessions` | Active Player | Issue a 60-second single-use Gate Ticket |
| GET | `/v1/users/me/meta-profile` | Active Player | Current Meta profile |
| GET | `/v1/users/me/loadouts` | Active Player | All role loadouts |
| GET | `/v1/users/me/loadouts/{role_id}` | Active Player | One role loadout |
| PUT | `/v1/users/me/loadouts/{role_id}` | Active Player | Definition-validated optimistic update |
| POST | `/v1/meta/parties` | Active Player | Create a Party |
| GET | `/v1/meta/parties/{party_id}` | Party member | Read Party state |
| POST | `/v1/meta/parties/{party_id}/ready` | Party member | Update ready state |
| POST | `/v1/meta/parties/{party_id}/presence` | Party member | Update presence |
| POST | `/v1/meta/matchmaking/tickets` | Active Player/leader | Queue a solo player or whole Party |
| GET | `/v1/meta/matchmaking/tickets/{ticket_id}` | Ticket owner/member | Poll assignment |
| DELETE | `/v1/meta/matchmaking/tickets/{ticket_id}` | Ticket owner/leader | Cancel a queued Ticket |

## 4. HTTP status and retry

| Status | Meaning | Client behavior |
| --- | --- | --- |
| 200/201 |success|Handle envelope normally|
| 400 | Invalid format or field | Fix the request; do not retry blindly |
| 401 | Token is missing, expired, revoked, or mismatched | Refresh the token or authenticate/register again |
| 403 | Account state, participation, or credential does not permit the operation | Do not retry; show a permission error |
| 404 | Resource or intentionally hidden route does not exist | Verify the resource ID and endpoint |
| 409 | State conflict, room full, or version mismatch | Refresh resource state before deciding whether to retry |
| 429 | Rate limited | Use exponential backoff with jitter |
| 500 | Internal service error | Report the `request_id`; retry only a limited number of times |
| 503 | Service not ready or dependency unavailable | Use exponential backoff; do not poll frequently |

Only idempotent operations or operations with explicit idempotent semantics are automatically retried. Recommended backoffs are 250 ms, 500 ms, 1 s, 2 s, upper limit 5–10 s, and add random jitter.

## 5. Contract documents

- Complete OpenAPI: `Backend/api/openapi/openapi.yaml`
- Authentication matrix: `Backend/api/openapi/auth-permission-matrix.md`
- Relay UDP protocol: `Backend/api/relay-protocol.md`
- Internal API: `docs/api/internal.md`
