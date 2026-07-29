# V1.1 Repository Audit

English | [简体中文](repository-audit.zh-CN.md)

Audit baseline: `af4343a4cdc0b60836a417c182d0c65c0917c197` (`main`)

Audit date: 2026-07-21

Scope: Focus on the Go control plane and Edge Relay in `Backend/`, while checking the old backend implementation in contracts, deployment, monitoring, testing and repositories.

## 1. Current directory structure

```text
ProjectRebound/
├── .github/workflows/          # CI 构建、GHCR 镜像和分离部署
├── Backend/
│   ├── api/
│   │   ├── openapi/            # HTTP OpenAPI 与权限矩阵
│   │   ├── proto/              # Relay mTLS gRPC 控制流
│   │   └── relay-protocol.md   # 当前 UDP Relay v1
│   ├── cmd/
│   │   ├── control-plane/      # 生产控制面入口
│   │   ├── edge-relay/         # 最小 Edge Relay 入口
│   │   └── main.go             # 旧 SQLite/UDP 单体入口
│   ├── internal/
│   │   ├── auth, admin, player
│   │   ├── gameserver, p2proom, connection
│   │   ├── relayregistry       # 控制面 Relay 注册、调度、迁移和 mTLS
│   │   ├── relayruntime        # Edge UDP 数据面
│   │   ├── observability, health, cache, database
│   │   └── http, server, db, store, udp, matchmaking, models
│   │       # 尚未删除的旧单体实现
│   ├── migrations/             # PostgreSQL 000001～000008
│   ├── deployments/            # Compose、监控、边缘节点和公网网关
│   ├── scripts/                # 构建、部署、备份、验证与回滚
│   └── tests/                  # k6 HTTP 负载与 Relay netem 矩阵
├── docs/                       # 外部/内部 API、CI/CD 与 Debian 运维
├── Desktop/                    # 客户端浏览器工具
├── Payload/                    # 游戏 Payload
└── Tools/                      # 文档和 NAT 测试工具
```

The production image only builds `cmd/control-plane` and `cmd/edge-relay`. The old `cmd/main.go`, SQLite database and corresponding package can still be compiled, but should not be extended further as the V1.1 authoritative implementation.

## 2. Current public API

The authoritative route is located at `internal/controlplane/server.go` and the Schema is located at `api/openapi/openapi.yaml`.

|field|methods and paths|Current authentication|
| --- | --- | --- |
|healthy| `GET /health/live`, `GET /health/ready` |none|
| Authentication | `POST /v1/auth/bind`, `POST /v1/auth/refresh` | None; bind is rate-limited only by IP |
|current player| `POST /v1/auth/logout`, `GET /v1/users/me` | Player Access Token |
| Dedicated Server | `POST/GET /v1/game-servers`, `GET/DELETE /v1/game-servers/{id}`, `POST .../heartbeat` |Register Token, Server Token or read publicly|
|P2P room|`GET/POST /v1/p2p-rooms`, query, join, leave, heartbeat, start, delete|Public reading; writing requires Active Player, and Host Token is required for host operations.|
|connect| `POST /v1/connections`, `GET/DELETE /v1/connections/{id}` |Participant Access Token|
|real time| `GET /v1/realtime/connect` | Access Token + WebSocket Upgrade |
|renew| `/v1/updates/check`, `/v1/updates/{platform}/{version}/manifest`, `/v1/updates/files/{file_id}` |none|
|Client configuration| `GET /v1/client/config` |none|

Currently, the V1.1 player session list/revocation interface, invitation code interface and authentication risk query interface are missing. `/auth/bind` requests only accept `steam_id`, `persona_name`; unknown fields will be rejected, so optional fields in the brief are not yet compatible.

## 3. Current internal and management API

|interface|Current protection and uses|
| --- | --- |
| `/v1/admin/players*` |Admin Token + trusted CIDR; query, modify players and cancel all sessions|
| `POST /internal/v1/relay-nodes/enroll` |One-time Bootstrap Token; issue Node Token and certificate|
| `POST /internal/v1/relay-nodes/{id}/certificate/renew` |Node Token; renew CSR, certificate and Keyset|
| `GET /internal/v1/relay-nodes[/...]` |Admin Token + trusted CIDR; dynamic Relay list|
| `POST .../{id}/drain`, `resume`, `revoke` |Admin Token + trusted CIDR; node status migration|
| `GET /internal/metrics` |trusted CIDR; Prometheus crawling|
| RelayControl `Connect` |TLS 1.3 mTLS bidirectional flow, TCP 9090 (production exposed via FRP/SNI gateway)|
| Edge `/metrics` |Default `127.0.0.1:9100`, only local monitoring agent capture|

Control flow uses `google.protobuf.Struct` envelope. Edge reports `Hello`, `Heartbeat`/`CapacityReport`, `TrafficReport`, allocation switch events and running errors; the control plane delivers configuration, Keyset, Drain, allocation revocation, certificate rotation prompt and Shutdown.

## 4. Current database tables and migrations

The migrator uses PostgreSQL advisory lock, per-migration transactions, SHA-256 checksum and `schema_migrations`, and applied migrations cannot be modified silently. Current migration:

|Version|Main tables/changes|
| --- | --- |
| `000001_baseline` |version baseline|
| `000002_auth` | `players`, `auth_sessions`, `auth_login_audit_logs` |
| `000003_admin` | `admin_audit_logs` |
| `000004_game_servers` | `game_servers` |
| `000005_p2p_rooms` | `p2p_rooms`, `p2p_room_members` |
| `000006_connections` | `connections`, `connection_candidates`, `connection_path_checks` |
| `000007_relay_registry` | `relay_bootstrap_tokens`, `relay_nodes`, `relay_allocations`, `relay_node_audit_logs` |
| `000008_relay_migrations` |`MIGRATING_RELAY` status, allocation failure reason, `relay_migrations`|

There are no `auth_risk_events`, standardized `auth_login_events`, independent `auth_refresh_tokens`, `invite_codes`, `invite_code_uses`, `relay_node_credentials`, `relay_signing_keys`. `relay_allocations` has no bound time, close reason and bidirectional byte persistence fields. The update manifest is currently from the deployment directory and the `update_releases`/`update_files` database tables listed in the task book do not exist.

## 5. Current authentication and session process

1. Bind verifies the 16-20 digit SteamID and normalizes the persona name.
2. Upsert player by unique SteamID within the transaction; concurrent bind ensures that only one player is created by the unique constraint.
3. Create the `auth_sessions` line, Refresh Token uses a 48-byte secure random number and saves only SHA-256.
4. Access Token is Ed25519 JWT, with player, session, provider, auth level and token version.
5. Refresh uses `SELECT ... FOR UPDATE`, creates a new Session row and marks the old row as `ROTATED`.
6. Reusing the old Refresh Token will revoke the entire `token_family_id` and record `REFRESH_TOKEN_REUSE`. The Session will be queried for each Access authentication, so the revocation can take effect immediately.
7. Logout cancels the current Session; the administrator can cancel all active sessions of the player.

The core safety semantics of Refresh Token rotation and reuse detection are implemented. Gaps include an in-process token bucket that rate-limits bind only by IP; no Redis-backed authentication risk controls; Device ID read only from `X-Device-Id` and stored in plaintext in the session; and no per-device, SteamID, or combined-dimension rate limits, invitation codes, risk events, failed-login specification table, player session-management API, or masked-IP display.

## 6. Current Relay BIND protocol

The current mark is `ProtocolVersion=1`, but many security mechanisms in the mission book have been implemented:

```text
BIND(token) -> CHALLENGE(cookie) -> BIND_PROOF(cookie, token) -> BIND_OK(handle, role)
```

- Cookie is HMAC, binding source IP/port, Token hash and current/last time bucket; Relay does not save unverified challenge status.
- Challenge is fixed at 38 bytes, and the code is guaranteed to be no larger than the BIND request.
- HOST and PEER must complete bind before forwarding.
- DATA contains random 64-bit handle, role, 64-bit sequence, 16-byte HMAC tag and opaque payload.
- Each end maintains a 64-bit replay window; duplicates or window outsourcing are discarded.
- The data packet does not carry any destination address, and Relay cannot be used as a general UDP forwarder.

Differences from target V2: BIND_INIT does not have client nonce and requested MTU; Cookie is indirectly bound to the Token instead of explicit allocation/client nonce; there is no v1 compatibility switch; the default maximum datagram is 1280 instead of the target default of 1200; the protocol name, data key derivation label and document are still v1.

## 7. Current Relay Token format

Ed25519 `relay+jwt` includes and validates `iss`, `aud`, `kid`, `jti`, `relay_node_id`, `allocation_id`, `connection_id`, `room_id`, `endpoint_role`, `protocol`, `nbf`, `exp`, `allocation_expires_at`, `max_bps`, `max_pps`, and `max_total_bytes`.

Edge caches allocation, role, source endpoint and expiration time according to `jti`; retrying the same endpoint is idempotent, and reusing different endpoints will be rejected. Controlled re-challenge updates after NAT port changes are currently not supported; Keyset is just a public key array without version and without overall signature.

## 8. Current Relay node registration and control channel

- Bootstrap Token only saves the hash and consumes it in a successful registration transaction.
- Edge generates Ed25519 private key and CSR locally; `identity.json` saves Node Token, private key, certificate, CA and Keyset in 0600 atoms.
- Production must explicitly configure the persistent Relay CA, Relay Token private key, and Bootstrap Token.
- Node certificates are currently valid for 24 hours by default; there is less than 1 hour left when the process starts to try to renew.
- The mTLS service verifies the certificate CA and binds the database record according to fingerprint, validity period and node status when Hello.
- Revoke sets the node to `REVOKED` and issues Shutdown; subsequent renewal and Hello are rejected.

Gap: The database only saves the current fingerprint/expiry at `relay_nodes`, and there is no certificate sequence history, revocation reason, and rotation audit table; the certificate is only renewed when Edge starts, and there is no automatic renewal schedule during running; there is no threshold configured in proportion to the certificate validity period.

## 9. Current failover migration capabilities

The node sweeper converts the nodes to `UNHEALTHY`/`OFFLINE` according to the heartbeat/certificate period, and the Scheduler only selects nodes with `READY` and whose capacity is lower than the threshold. Migrating sweeper will:

1. Find the active allocation on the failed node;
2. Use the unique index of the database to ensure that each connection has at most one `BINDING` migration;
3. Select another READY node and create a new allocation;
4. Mark the old allocation failed and connect to `MIGRATING_RELAY`;
5. Send old allocation revocation and new Relay Token/WebSocket events;
6. After the new node reports `AllocationOpened`, migration is completed and the migrated event is issued.

Currently missing are migration deadline, number of attempts, candidate node history, maximum retries, next node selection after timeout and explicit final failure loop when there are no nodes. The Drain API request body does not support custom deadlines or `migrate_existing`. Naturally, Drain does not actively migrate; Revoke/faults will trigger existing migration sweepers.

## 10. Current indicators and logs

The control plane already exposes HTTP counters/histograms, authentication-bind results, Refresh Token reuse, room and server state, Relay state/allocations, database-pool state, Go goroutine/memory data, and centralized Relay telemetry. Edge nodes expose allocations, packets received/forwarded/dropped, forwarded bytes, bind results, invalid tokens, rate-limit drops, and control-channel metrics.

Main gaps: no `http_active_requests`; per-dimension authentication rate limits; risk, invitation-code, and Refresh Token totals; WebSocket reconnections; migration outcomes; Redis latency; or background-task duration/failure metrics. Edge nodes lack detailed metrics for bind init/challenge, Cookie or Token replay, authentication failure, oversized packets, replay drops, ingress bytes, node load, goroutines, and memory.

The log uses the structured `slog`. Authentication errors only record error codes/internal errors. No codes that actively output the complete Token or private key are found. Special secret scan tests and log contract tests still need to be added in V1.1.

## 11. Current deployment method

- CI performs gofmt, module verification, vet, race tests, builds, OpenAPI/Compose/script/documentation verification on Linux, and publishes control-plane/edge-relay GHCR images pinned with full commit SHA and provenance attestation.
- CD can deploy control plane and edge nodes separately; use independent GitHub Environment, fixed SSH host key and immutable image reference.
- The control plane Compose includes PostgreSQL, Redis, and optional Prometheus/Grafana; Docker Socket is not mounted.
- Edge uses standalone Compose/raw Docker, host networking and persistent identity volume, without connecting to PostgreSQL/Redis.
- `remote-deploy.sh` Back up the control plane before releasing it. If the health check fails, the previous release/image will be tried.
- The public network is returned to the origin by Cloudflare HTTP proxy + HAProxy SNI gateway + independent FRP QUIC; Relay mTLS uses independent FRP, configuration and service isolation.

Gap: Automatic migration when the application starts, no independent Expand/Migrate/Contract gate; no unified release preflight, database schema/protocol/build metadata; the image still generates `main` floating label (although not used for deployment); backup does not have encryption, hierarchical retention, off-site upload and periodic recovery drills; Prometheus does not have version control alarm rules.

## 12. Old implementations that conflict or overlap with V1.1

1. `cmd/main.go` and `internal/http|server|db|store|udp|matchmaking|models` are SQLite/old UDP monomers that coexist with the PostgreSQL control plane and Edge Relay; V1.1 should not be developed repeatedly in the two sets of implementations.
2. The roots `config.yaml`, `matchserver.db`, and `deploy/` belong to the old deployment path and can easily be mistaken for the production authoritative entrance.
3. Relay already has most of the package formats and security semantics of the V2 target, but it is still publicly named v1; directly changing the version will destroy the deployed client, and compatibility switches and clear migration strategies must be used.
4. Refresh Token has implemented rotation/reuse, but the data model is "one Session per refresh", which is different from the planned independent refresh-token table; the existing Token immediate revocation semantics should be maintained during migration and expand-first should be adopted.
5. The current authentication-bind IP rate limit is implemented in generic middleware and stored in single-process memory, so it cannot meet multidimensional, Redis-consistency, or risk-audit requirements.
6. The existing migration can complete a single Relay replacement, but does not meet the complete state machine of retry, timeout and Drain strong migration; the current situation must not be mistakenly reported as the Release Gate has passed.

## 13. Documents to be modified and added

The following is the estimated scope after the current audit; each Milestone will remain independently submitted and refined during implementation:

- Authentication and database: added `migrations/000009_auth_security.sql`; modified `internal/auth/*`, `internal/config/config.go`, `internal/controlplane/server.go`, and `internal/observability/metrics.go`; added invitation, risk, session, and Redis/local-limiter domain files and tests.
- API: Modify `api/openapi/openapi.yaml`, permission matrix, `docs/api/external.md` and `docs/api/internal.md`.
- Relay V2: Modify `internal/relayruntime/{protocol,cookie,token,runtime,config,metrics}.go`, corresponding tests, `api/relay-protocol.md` and edge example configurations; retain controlled v1 compatibility paths.
- Relay resources and migration: Add subsequent expand migration, modify `internal/relayregistry/{model,repository,service,http,migration_sweeper,token,authority,control}.go`, connection real-time events and integration tests.
- Key/Certificate: Added signing key, node credential repository/service, rotation background task, Keyset version/signature and certificate automatic renewal test.
- Testing tools: Added `cmd/load-bot/`, `internal/loadbot/`, standard scenario and report Schema; extended `tests/load/`.
- Weak network/fault: Added `scripts/netem/`, `scripts/chaos/`, `tests/netem/` scenarios and `docs/operations/runbooks/chaos-testing.md`.
- Backup and recovery: Added Linux `scripts/backup/postgres-{backup,restore}.sh`, verification/retention script, systemd timer example and recovery drill report.
- Monitoring: Split/extend Grafana dashboards, add Prometheus rule files, configuration verification and alarm testing.
- Release: Added `scripts/release/preflight.sh`, version/build metadata, migration gate, rollback runbook and release manifest; adjusted CI image/test matrix.
- V1.1 Documentation: Ongoing maintenance of baselines, protocols, migrations, keys, backups, testing, releases and final acceptance reports in `docs/testing/v1.1/`.

## Audit conclusion

The current implementation covers several of the more difficult foundational capabilities in the V1.1 goal, but has not yet reached the complete release gate in the task specification. Further work should reuse the existing PostgreSQL transactions, Relay data plane, and migration framework; complete authentication-abuse defenses first; then evolve Relay v1 security compatibly and complete observability, lifecycle, and operations evidence.
