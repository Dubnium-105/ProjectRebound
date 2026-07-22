# ProjectRebound Internal, Administrative, and Relay API

English | [简体中文](internal.zh-CN.md)

This document describes the interfaces that are not exposed to normal game clients: Admin HTTP API, Relay registration/renewal, mTLS gRPC control flow, Prometheus metrics, and UDP Relay data plane. The complete JSON Schema is located at `Backend/api/openapi/openapi.yaml`, and the gRPC service is located at `Backend/api/proto/relay_control.proto`.

## 1. Network entry and trust boundary

| Interface | Default address | Caller | Protection |
| --- | --- | --- | --- |
| Admin HTTP | `127.0.0.1:18080/v1/admin/*` | Operations backend or SSH tunnel | Admin Token + trusted CIDR |
| Internal Relay management HTTP | `127.0.0.1:18080/internal/v1/relay-nodes/*` | Operations backend | Admin Token + trusted CIDR |
| Relay registration/renewal HTTP | Public HTTPS `/internal/v1/relay-nodes/enroll`, `.../certificate/renew` | Edge Relay | One-time Bootstrap Token or Node Token |
| Control-plane metrics | `127.0.0.1:18080/internal/metrics` | Prometheus | Trusted CIDR; public proxy returns 404 |
| Relay control channel | Control-plane TCP 9090 | Edge Relay | TLS 1.3 mutual certificate authentication |
| Relay data plane | Edge UDP 8443 | Game endpoint | Signed Relay Token + Cookie Challenge + packet HMAC |
| Relay metrics | Edge `127.0.0.1:9100/metrics` | Node-local agent | Loopback only |

Public network Caddy must return 404 for `/v1/admin*` and `/internal/*`, and only two machine interfaces, Relay enroll and certificate renew, are allowed. Source IP whitelist cannot replace Token, nor can Token replace network isolation.

## 2. Admin Token

The control plane reads through environment variables:

```text
ADMIN_TOKENS=operator=<high-entropy-token>;automation=<another-token>
ADMIN_TRUSTED_CIDRS=127.0.0.0/8,10.0.0.0/8,...
```

Request:

```http
Authorization: Bearer <admin-token>
```

Player Access Tokens are not available for use in the management interface. When `TRUST_PROXY_HEADERS=true` is enabled, the control plane can only be placed behind the trusted reverse proxy, and the client is prohibited from bypassing the proxy for direct connection; otherwise, the forged forwarding header may affect the source address determination.

## 3. Player Management API

| Method | Path | Parameters/request | Success |
| --- | --- | --- | --- |
| GET | `/v1/admin/players` | `cursor`, `limit`, `account_status` | Paginated player list |
| GET | `/v1/admin/players/{player_id}` | Path ID | Complete administrative record |
| PATCH | `/v1/admin/players/{player_id}` | At least one of `account_status`, `is_vip`, `revoke_sessions` | Updated player and number of revoked sessions |
| POST | `/v1/admin/players/{player_id}/revoke-sessions` | None | Revocation count and time |

Patch example:

```http
PATCH /v1/admin/players/player_123 HTTP/1.1
Authorization: Bearer <admin-token>
Content-Type: application/json

{
  "account_status": "BANNED",
  "is_vip": false,
  "revoke_sessions": true
}
```

`account_status` is `ACTIVE`, `BANNED` or `DELETED`. Updates are written to audit records; set `revoke_sessions=true` when an existing login needs to be invalidated immediately, or call the standalone revoke-sessions endpoint. External work order reasons should be correlated through a controlled audit system, and Tokens, private data, or game payloads should not be put into requests or logs.

### 3.1 Invitation code management

|method|path|parameters/request|success|
| --- | --- | --- | --- |
| POST | `/v1/admin/invite-codes` |`batch_name`, `max_uses`; optional `expires_at`, `permissions`|Create metadata and return plaintext `code` only this time|
| GET | `/v1/admin/invite-codes` | `cursor`, `limit` |Paginated list of metadata, does not return clear text or hash|
| GET | `/v1/admin/invite-codes/{id}` | Path ID |Single piece of metadata, no plaintext or hash returned|
| PATCH | `/v1/admin/invite-codes/{id}` |At least one of `batch_name`, `max_uses`, `expires_at`, `enabled`, `permissions`|Updated metadata|
| POST | `/v1/admin/invite-codes/{id}/revoke` | None | Idempotently disable and record revocation time |

The clear text invitation code in the creation response is secret and appears only once; only the SHA-256 hash is stored in the database. `max_uses` shall not be reduced below `used_count`. Row-level locks are used to consume quotas when binding a new SteamID, so only one transaction will succeed in competing for the last quota concurrently. If an existing player binds again, the invitation code will not be consumed again.

### 3.2 Authentication risk events

Use `GET /v1/admin/auth/risk-events` with `cursor`, `limit`, `player_id`, `event_type`, `severity`, and `unresolved_only` to query authentication risk records. V1.1 records and displays these events but does not ban accounts automatically. The response omits the Device ID hash and masks the IP address. Database event details must not contain Access Tokens, Refresh Tokens, or complete Authorization headers.

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
