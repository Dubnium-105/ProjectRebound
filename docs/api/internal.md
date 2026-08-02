# ProjectRebound Internal, Administrative, and Relay API

English | [简体中文](internal.zh-CN.md)

This document describes the interfaces that are not exposed to normal game clients: Admin HTTP API, Relay registration/renewal, mTLS gRPC control flow, Prometheus metrics, and UDP Relay data plane. The complete JSON Schema is located at `Backend/api/openapi/openapi.yaml`, and the gRPC service is located at `Backend/api/proto/relay_control.proto`.

## 1. Network entry and trust boundary

| Interface | Default address | Caller | Protection |
| --- | --- | --- | --- |
| Admin HTTP | `127.0.0.1:18080/v1/admin/*` | Dedicated Admin Web | Turnstile + administrator password + TOTP + short-lived Admin Access Token + trusted CIDR |
| Internal Relay management HTTP | `127.0.0.1:18080/internal/v1/relay-nodes/*` | Operations backend | Admin Token + trusted CIDR |
| Relay registration/renewal HTTP | Public HTTPS `/internal/v1/relay-nodes/enroll`, `.../certificate/renew` | Edge Relay | One-time Bootstrap Token or Node Token |
| Control-plane metrics | `127.0.0.1:18080/internal/metrics` | Prometheus | Trusted CIDR; public proxy returns 404 |
| Relay control channel | Control-plane TCP 9090 | Edge Relay | TLS 1.3 mutual certificate authentication |
| Relay data plane | Edge UDP 8443 | Game endpoint | Signed Relay Token + Cookie Challenge + packet HMAC |
| Relay metrics | Edge `127.0.0.1:9100/metrics` | Node-local agent | Loopback only |

Public network Caddy must return 404 for `/v1/admin*` and `/internal/*`, and only two machine interfaces, Relay enroll and certificate renew, are allowed. Source IP whitelist cannot replace Token, nor can Token replace network isolation.

## 2. Administrator authentication

Browser administrator authentication is completely isolated from player authentication:

1. `GET /v1/admin/auth/config` returns the public Turnstile Sitekey and action.
2. `POST /v1/admin/auth/login` submits the username, password, and `turnstile_token`.
3. The control plane calls Cloudflare Siteverify and verifies `success`, the expected hostname, and the `admin_login` action.
4. A valid password produces a single-use MFA challenge that expires after five minutes.
5. `POST /v1/admin/auth/mfa/verify` accepts a TOTP or recovery code.
6. Success returns a short-lived Admin Access Token and sets an `HttpOnly; SameSite=Strict` refresh cookie.
7. `POST /v1/admin/auth/refresh` rotates the refresh cookie. Reuse of the previous token revokes the session.
8. High-risk actions call `POST /v1/admin/auth/step-up` with TOTP or a recovery code and receive a short-lived, session-bound proof.

Authenticated administrators use:

```text
POST   /v1/admin/auth/logout
GET    /v1/admin/auth/me
GET    /v1/admin/auth/sessions
DELETE /v1/admin/auth/sessions/{session_id}
POST   /v1/admin/auth/step-up
```

Neither Player Access Tokens nor machine Admin Tokens can replace an Admin Web Access Token. RBAC is enforced server-side; hiding a frontend menu is not a security boundary. Relay revoke additionally requires the step-up proof in `X-Admin-Step-Up`; the proof stays in browser memory and expires after the configured short TTL.

Create the first administrator with `go run ./cmd/adminctl`. The password is read only from the `ADMINCTL_PASSWORD` environment variable. The TOTP provisioning URI and recovery codes are displayed once after creation. Persistent environments must configure `ADMIN_MFA_ENCRYPTION_KEY_BASE64` first.

## 2.1 Machine Admin Token

Static Admin Tokens remain available only for internal Relay operations and automation. They are no longer accepted by the human-facing `/v1/admin/*` API. The control plane reads them from environment variables:

```text
ADMIN_TOKENS=operator=<high-entropy-token>;automation=<another-token>
ADMIN_TRUSTED_CIDRS=127.0.0.0/8,10.0.0.0/8,...
```

Request:

```http
Authorization: Bearer <admin-token>
```

Player Access Tokens are never accepted by management interfaces. When `TRUST_PROXY_HEADERS=true` is enabled, the control plane can only be placed behind the trusted reverse proxy, and clients must not be able to bypass that proxy; otherwise, forged forwarding headers can influence source-address checks.

## 3. Player Management API

| Method | Path | Parameters/request | Success |
| --- | --- | --- | --- |
| GET | `/v1/admin/players` | `cursor`, `limit`, `account_status` | Paginated player list |
| GET | `/v1/admin/players/{player_id}` | Path ID | Complete administrative record |
| GET | `/v1/admin/players/{player_id}/sessions` | Path ID | Recent sessions with device and IP summaries |
| GET | `/v1/admin/players/{player_id}/risk-events` | Path ID | Recent masked risk events |
| GET | `/v1/admin/players/{player_id}/login-events` | Path ID | Recent authentication outcomes |
| PATCH | `/v1/admin/players/{player_id}` | `reason`, plus at least one of `account_status`, `is_vip`, `revoke_sessions`; optional `internal_note` | Updated player and number of revoked sessions |
| POST | `/v1/admin/players/{player_id}/revoke-sessions` | `reason` | Revocation count and time |

Patch example:

```http
PATCH /v1/admin/players/player_123 HTTP/1.1
Authorization: Bearer <admin-access-token>
Content-Type: application/json

{
  "account_status": "BANNED",
  "is_vip": false,
  "revoke_sessions": true,
  "reason": "Confirmed chargeback abuse in ticket CS-4812",
  "internal_note": "Reviewed by the duty operations lead"
}
```

`account_status` is `ACTIVE`, `BANNED` or `DELETED`. Every write requires a human-readable reason and stores the reason, request ID, administrator, source address, User-Agent, before/after values, and outcome in the audit record. Set `revoke_sessions=true` when an existing login needs to be invalidated immediately, or call the standalone revoke-sessions endpoint. Never put tokens, passwords, cookies, private data, or game payloads into reasons or notes.

### 3.1 Invitation code management

|method|path|parameters/request|success|
| --- | --- | --- | --- |
| POST | `/v1/admin/invite-codes` |`batch_name`, `max_uses`, `reason`; optional `quantity` (1–100), `expires_at`, `permissions`|Atomically create a batch and return its plaintext codes only this time|
| GET | `/v1/admin/invite-codes` | `cursor`, `limit` |Paginated list of metadata, does not return clear text or hash|
| GET | `/v1/admin/invite-codes/{id}` | Path ID |Single piece of metadata, no plaintext or hash returned|
| GET | `/v1/admin/invite-codes/{id}/uses` | `cursor`, `limit` |Successful redemption records with masked IP network summaries|
| PATCH | `/v1/admin/invite-codes/{id}` |`reason` and at least one of `batch_name`, `max_uses`, `expires_at`, `enabled`, `permissions`|Updated metadata|
| POST | `/v1/admin/invite-codes/{id}/revoke` | `reason` | Idempotently disable and record revocation time |

The clear text invitation code in the creation response is secret and appears only once; only the SHA-256 hash is stored in the database. `max_uses` shall not be reduced below `used_count`. Row-level locks are used to consume quotas when binding a new SteamID, so only one transaction will succeed in competing for the last quota concurrently. If an existing player binds again, the invitation code will not be consumed again.

### 3.2 Dashboard, risk events, and audit

The dashboard uses `GET /v1/admin/dashboard/summary`, `GET /v1/admin/dashboard/timeseries`, and `GET /v1/admin/dashboard/alerts`. Time-series periods are restricted to `1h`, `24h`, `7d`, and `30d`; the UI cannot submit arbitrary SQL grouping expressions.

Use `GET /v1/admin/risk-events` and `GET /v1/admin/risk-events/{event_id}` to query authentication risk records. `POST /v1/admin/risk-events/{event_id}/resolve` requires `reason` and records the administrator and resolution time. The response omits the Device ID hash, internal device-fingerprint ID, and all per-factor HMAC digests; it also masks the IP address and recursively redacts credential-like detail keys. There is currently no external or administrator API for raw factor lookup or device banning. A future ban workflow must expose only an authorization-checked server-side match operation, never stored digests.

Write audits are available through `GET /v1/admin/audit-logs` and `GET /v1/admin/audit-logs/{audit_id}`. Administrator authentication and Turnstile diagnostics are available through `GET /v1/admin/login-audit`. Login audit records contain the verification result, error codes, hostname, action, and latency, but never the Turnstile token, secret, password, cookie, or Authorization header.

### 3.3 Online operations

Human administrators use these session-authenticated routes:

```text
GET  /v1/admin/p2p-rooms
GET  /v1/admin/p2p-rooms/{room_id}
GET  /v1/admin/p2p-rooms/{room_id}/members
POST /v1/admin/p2p-rooms/{room_id}/close
POST /v1/admin/p2p-rooms/{room_id}/members/{player_id}/remove

GET  /v1/admin/p2p-battlelog/matches/{match_id}
GET  /v1/admin/p2p-battlelog/reports/{evidence_id}/raw

GET  /v1/admin/game-servers
POST /v1/admin/game-servers/registration-tokens
GET  /v1/admin/game-servers/{server_id}
POST /v1/admin/game-servers/{server_id}/drain
POST /v1/admin/game-servers/{server_id}/resume
POST /v1/admin/game-servers/{server_id}/disable

GET  /v1/admin/connections
GET  /v1/admin/connections/{connection_id}
POST /v1/admin/connections/{connection_id}/close
POST /v1/admin/connections/{connection_id}/migrate-relay

GET  /v1/admin/relay-nodes
GET  /v1/admin/relay-nodes/{node_id}
POST /v1/admin/relay-nodes/{node_id}/drain
POST /v1/admin/relay-nodes/{node_id}/resume
POST /v1/admin/relay-nodes/{node_id}/revoke
```

P2P BattleLog normalized evidence requires `p2p.battlelog.read`. The separate raw endpoint requires `p2p.battlelog.raw.read`, returns `Cache-Control: no-store`, and is not granted to ordinary operations/support roles. Its report identifiers and tables are separate from dedicated-server BattleLog storage.

Every write requires `reason`; Relay drain also accepts `deadline_seconds` and `migrate_existing`. `POST /v1/admin/game-servers/registration-tokens` additionally requires `game_servers.register` and MFA step-up. It accepts `instance_id` plus a 1–168 hour lifetime, revokes any older unconsumed token for that instance, stores only a SHA-256 hash, and returns the plaintext `gsr_...` token once with `Cache-Control: no-store`. Game-server disable marks the server offline and revokes its Server Token. Room actions report `connections_cleanup_complete`; a false value means the room mutation succeeded but the operator should follow the runbook to confirm downstream connection cleanup. Connection Relay migration never accepts a target address or node from the browser: the backend scheduler selects an eligible READY node. Other responses never include host tokens, node tokens, allocation tokens, registration-token hashes, private keys, or full ICE candidates.

### 3.4 Managed client releases

```text
GET  /v1/admin/releases
POST /v1/admin/releases
GET  /v1/admin/releases/{release_id}
POST /v1/admin/releases/{release_id}/validate
POST /v1/admin/releases/{release_id}/publish
POST /v1/admin/releases/{release_id}/rollback
POST /v1/admin/releases/{release_id}/archive
```

Creation accepts platform, architecture, stable/beta/toolbox channel, semantic version information, forced-update policy, and object-storage file descriptors. Validation checks file paths, sizes, SHA-256 values, compression, CDN object keys and actual `HEAD` availability, compatibility ordering, and the generated Ed25519 signature. Only a `READY` release can be published. Publish, rollback, and archive require both an operation reason and server-enforced MFA step-up. The public update catalog reads only `PUBLISHED` managed manifests; rollback removes that release from future update checks without deleting its audit history. Archive accepts only `DRAFT`, `READY`, or `ROLLED_BACK`, uses `updates.rollback`, and preserves all records.

### 3.5 Administrator and role governance

```text
GET   /v1/admin/admins
POST  /v1/admin/admins
PATCH /v1/admin/admins/{admin_id}
POST  /v1/admin/admins/{admin_id}/reset-mfa
GET   /v1/admin/roles
PATCH /v1/admin/roles/{role_id}
```

Administrator accounts remain separate from player identities. Creation assigns one or more existing roles and returns the TOTP provisioning URI and ten recovery codes only once. Updating an administrator can change the display name, active state, role assignments, and revoke sessions. MFA reset rotates the encrypted TOTP secret, replaces all recovery-code hashes, and revokes all sessions.

Every governance write requires a human-readable reason, the corresponding `admins.create`, `admins.update`, or `roles.manage` permission, and a session-bound MFA step-up proof. The last active `SUPER_ADMIN` cannot be disabled or stripped of that role, including under concurrent requests. `SUPER_ADMIN` always owns the full permission catalog and cannot be edited. Passwords, TOTP secrets, recovery-code plaintext, cookies, and access tokens are excluded from list responses and audit values.

### 3.6 Features, capabilities, settings, and integrations

```text
GET   /v1/admin/features
GET   /v1/admin/capabilities
GET   /v1/admin/settings
PATCH /v1/admin/settings
```

Features and capabilities are non-secret discovery endpoints for optional modules, supported resources and operations, batch limits, realtime availability, and polling fallbacks. Settings expose only a database-backed whitelist of feature switches and HTTPS integration links; configuration secrets, connection strings, tokens, and private keys are never part of this model.

Settings reads require `settings.read`. Updates require `settings.update`, an operation reason, and MFA step-up, and are atomically audited. URL settings accept only HTTPS URLs without embedded credentials. The Grafana value is a link to a read-only dashboard; the Admin Web does not reproduce the full monitoring system or proxy Grafana credentials.

## 4. Relay HTTP lifecycle API

### 4.1 First time registration

```http
POST /internal/v1/relay-nodes/enroll
Authorization: Bearer <one-time-bootstrap-token>
Content-Type: application/json
```

Request fields:

| Field | Type | Description |
| --- | --- | --- |
| `display_name` | string | Human-readable operations name |
| `region`, `zone`, `provider` | string |Scheduling and asset tags|
| `software_version` | string |Relay version|
| `protocol_version` | integer |V1.1 Edge must be 2; client v1 compatibility is controlled by Edge explicit switch|
| `advertised_endpoints` | array |`protocol`, `host`, `port`; client’s real reachable address|
| `supported_protocols` | string[] |Currently contains `UDP`|
| `capacity` | object | `max_allocations`, `max_egress_bps` |
| `csr_pem` | string |Ed25519 CSR generated locally by Edge|

Return 201 successfully:

```json
{
  "data": {
    "node": {"node_id": "relay_...", "state": "BOOTSTRAPPING"},
    "node_token": "returned-once",
    "certificate_pem": "-----BEGIN CERTIFICATE-----...",
    "ca_certificate_pem": "-----BEGIN CERTIFICATE-----...",
    "certificate_expires_at": "2026-07-19T12:00:00Z",
    "relay_token_keyset": {"keys": []}
  },
  "request_id": "req_..."
}
```

Bootstrap Token is marked as consumed in a successful transaction; `node_token` is returned only once, and the control plane only saves the hash. Edge writes the Node Token, private key, certificate, CA, and verification keyset to persistence `identity.json` with permission 600.

### 4.2 Certificate renewal

```http
POST /internal/v1/relay-nodes/{node_id}/certificate/renew
Authorization: Bearer <node-token>
Content-Type: application/json

{"csr_pem":"-----BEGIN CERTIFICATE REQUEST-----..."}
```

Successfully returns the new certificate, CA, expiration time, and current Relay Token public key set. Node ID and Node Token must correspond. Revoked nodes cannot be renewed. By default, Edge attempts to renew a certificate when less than one hour remains.

### 4.3 Administrative queries and lifecycle transitions

The following interfaces require Admin Token and trusted CIDR:

| Method | Path | Effect |
| --- | --- | --- |
| GET | `/internal/v1/relay-nodes` |Paging query for all registered nodes; supports `region`, `zone`, `provider`, `state`, `cursor`, `limit`, including offline and revoked nodes|
| GET | `/internal/v1/relay-nodes/{node_id}` |Query endpoint, capacity, load, certificates, and status; does not return credentials|
| POST | `/internal/v1/relay-nodes/{node_id}/drain` |`READY -> DRAINING`; optional `{deadline_seconds,migrate_existing}`, empty request only stops new allocation by default|
| POST | `/internal/v1/relay-nodes/{node_id}/resume` |Revert to `READY`|
| POST | `/internal/v1/relay-nodes/{node_id}/revoke` |Permanently revoke Node Token/certificate identity|
| POST | `/internal/v1/relay-signing-keys/{key_id}/activate` |All READY nodes activate the signature key after confirming the staged Keyset|

States include `BOOTSTRAPPING`, `CONNECTING`, `READY`, `DRAINING`, `UNHEALTHY`, `OFFLINE`, and `REVOKED`. The default heartbeat interval is 15 seconds; a node becomes `UNHEALTHY` after 45 seconds without a heartbeat and `OFFLINE` after 90 seconds. Drain with `migrate_existing=false` preserves existing allocations until they end naturally or reach the deadline. When set to `true`, bounded failover migration moves them gradually. Revocation is irreversible.

## 5. Relay mTLS gRPC control flow

Service definition:

```protobuf
service RelayControl {
  rpc Connect(stream google.protobuf.Struct)
      returns (stream google.protobuf.Struct);
}
```

The connection target is control plane TCP 9090. TLS requirements:

- Minimum TLS 1.3;
- Edge submits the client certificate signed by the persistent Relay CA;
- The control plane verifies the certificate and binds the database node with SHA-256 fingerprint;
- Edge validates the service certificate using the CA in the registration response;
- Current service certificate DNS SANs are `control-plane` and `localhost`, detached deployments should still set `control_server_name: control-plane`.

All messages use the same envelope:

```json
{"type":"Heartbeat","payload":{}}
```

Edge -> Control Plane:

| Type | Key payload | Description |
| --- | --- | --- |
| `Hello` | `node_id`, `software_version`, `protocol_version` |Must be the first packet of connection|
| `Heartbeat` | `active_allocations`, `current_egress_bps`, `current_ingress_bps`, `load_state` |Lease and load; `load_state` is `NORMAL`, `DEGRADED`, `REJECT_NEW` or `DRAINING`|
| `CapacityReport` |Same as load field|capacity update|
| `TrafficReport` |Heartbeat payload field, as well as `process_id`, monotonically increasing `sequence`, and cumulative packets/bytes/bind/token/rate-limit/reconnect counters|Reuse authenticated mTLS flows to update leases, payloads, and node telemetry; cumulative integers use decimal strings to avoid the floating point precision loss of `protobuf.Struct`|
| `AllocationOpened` | `allocation_id` |allocation installed|
| `AllocationClosed` | `allocation_id` |allocation released|
| `RuntimeError` | Implementation-defined non-secret diagnostics | Runtime error report |
| `DrainCompleted` | Drain completion message | Drain completed |

Control Plane -> Edge:

| Type | Key payload | Description |
| --- | --- | --- |
| `ConfigSnapshot` |`config_version`, `heartbeat_interval_seconds`, `lease_seconds`, `node_state`, drain field|First connection configuration; restore persistent drain when node reconnects|
| `KeysetUpdate` |Relay Token public key set|Signing key rotation|
| `EnterDrain` | RFC 3339 drain deadline, `migrate_existing` | Stop accepting new allocations |
| `ExitDrain` | — | Resume accepting allocations |
| `RevokeAllocation` | `allocation_id` |Release the specified allocation|
| `CertificateRotation` |rotation tips|Trigger renewal process|
| `Shutdown` |reason/deadline|orderly stop|

Unknown Type returned gRPC `InvalidArgument`. Certificate or node identity mismatch returns `Unauthenticated`/`PermissionDenied`. If the heartbeat or allocation status is illegal, `FailedPrecondition` is returned. Edge exponentially backs off reconnection when control flow is disconnected; short disconnections should not immediately clear still valid local allocations.

## 6. Relay UDP data plane

See `Backend/api/relay-protocol.md` for the complete binary format. summary:

1. Client sends v2 `BIND_INIT` with `client_nonce`, `requested_mtu`, and a signed Relay Token;
2. Relay returns `server_nonce + expires_in_ms + HMAC Cookie` without amplification;
3. Client sends `BIND_PROOF` from the same UDP endpoint carrying nonce, MTU, Cookie and Token as they are;
4. Relay stateless verification Cookie, then verify the signature and return `BIND_OK`, random 8-byte handle and negotiated MTU;
5. After both HOST and PEER are bound, `DATA` with HMAC tag and sequence is forwarded.

Relay Token claims binding `allocation_id`, `connection_id`, `relay_node_id`, endpoint role, validity period, bandwidth/PPS/total byte limit and protocol version. The packet does not contain any destination address, so it can only be forwarded between HOST and PEER in the same allocation. Relay does not decrypt the game payload and discards unknown handles, wrong characters/sources, invalid tags, replay/out-of-window serial numbers, timeout or over-limit packets.

## 7. Metrics API

Control plane:

```http
GET /internal/metrics
Accept: text/plain
```

Key metrics include HTTP request/latency, bind success or failure, session/refresh replay, P2P room, Dedicated Server status, Relay node/allocation, database connection pool and Go runtime. The control plane also outputs `relay_node_info`, `relay_node_state`, heartbeat/lease, capacity, and mTLS connection status for each Relay in the database; upgraded Relays additionally output `relay_node_*_total` cumulative telemetry via `TrafficReport`. Older nodes do not need to be upgraded simultaneously and will still appear in the node list, status and lease indicators. Public network Caddy must return 404.

Edge node:

```http
GET http://127.0.0.1:9100/metrics
```

Key metrics include active allocations, packets sent/received/dropped, forwarded bytes, bind success or failure, invalid tokens, rate-limit drops, control-channel connection and reconnection counts, `relay_load_state`, and state-transition counts labeled by `state`. Overload status is reported through mTLS `TrafficReport` and persisted; the scheduler will not assign new connections or migrations to `REJECT_NEW` or `DRAINING` nodes. Port 9100 is scraped by a node-local Prometheus agent and must not be exposed publicly.

## 8. Internal Errors and Audits

HTTP errors still use the unified `error` + `request_id` envelope. Management write operations, login, Refresh Token replay, Relay registration/renewal/state migration must record structured audit fields, but must not record:

- Access/Refresh/Admin/Game Server/Bootstrap/Node/Relay Token full text;
- Private key, CSR private key or complete `identity.json`;
- Complete game payload;
- The password in the database connection URL.

Troubleshoot using request ID, actor/resource ID, state migration, certificate fingerprint, container image digest and time range. The certificate fingerprint is not a private key and can be used for identity association.

## 9. Compatibility and Authoritative Contract

- HTTP schema: `Backend/api/openapi/openapi.yaml`
- gRPC service: `Backend/api/proto/relay_control.proto`
- Generated Go gRPC binding: `Backend/api/proto/relay_control_grpc.go`
- UDP v1: `Backend/api/relay-protocol.md`
- Permission matrix: `Backend/api/openapi/auth-permission-matrix.md`

Newly added fields should remain backward compatible; deletion/renaming, tightening of enumerations, changes in authentication methods, or changes in binary headers all require new API/protocol versions. The internal path does not mean that compatibility can be ignored, as edge nodes allow rolling upgrades.

## 10. MetaServer internal and administration routes

The full security and field contract is in the
[MetaServer internal API](metaserver-internal.md). Dedicated Server calls require
a hashed Game Server Token, matching `X-Game-Server-Id`, and an Ed25519 request
signature made by the node certificate for the same credential generation.
Timestamp and nonce checks prevent replay. The backend also checks a fresh
eligible server, route scope, assigned match, and roster membership.
Administrative writes additionally require trusted CIDR, human admin session,
permission, step-up, reason, and audit.

| Method | Path | Required scope or permission |
| --- | --- | --- |
| GET | `/internal/v1/meta/matches/{match_id}/players/{player_id}/loadout` | Game Server `meta.loadouts.read` |
| POST | `/internal/v1/meta/matches/{match_id}/players/{player_id}/connected` | Game Server `meta.matches.connect` |
| POST | `/internal/v1/meta/matches/{match_id}/completed` | Game Server `meta.matches.complete` |
| PUT | `/internal/v1/meta/battlelog/reports/{report_id}` | Game Server `meta.battlelog.write` |
| GET | `/v1/admin/meta/overview` | `meta.read` |
| GET | `/v1/admin/meta/players/{player_id}/loadouts` | `meta.loadouts.read` |
| PUT | `/v1/admin/meta/players/{player_id}/loadouts/{role_id}` | `meta.loadouts.update` + step-up |
| GET | `/v1/admin/meta/matches` | `meta.read` |
| POST | `/v1/admin/meta/matches/{match_id}/cancel` | `meta.matches.manage` + step-up |
| PUT | `/v1/admin/meta/playlists/{slug}` | `meta.content.manage` + step-up |
| PUT | `/v1/admin/meta/notifications/{notification_id}` | `meta.content.manage` + step-up |
