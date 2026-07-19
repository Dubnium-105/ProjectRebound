# Game control-plane production runbook

> [!WARNING]
> 历史运维摘要，已由 [`../debian-deployment-and-ops.md`](../debian-deployment-and-ops.md) 和 [`../cicd.md`](../cicd.md) 取代，请勿按本文执行生产部署。

## Deployment shape

Run the control plane as a modular monolith behind Caddy or another TLS reverse proxy. PostgreSQL and Redis are private dependencies. Relay nodes initiate outbound HTTPS enrollment and outbound TLS 1.3 mTLS gRPC control connections; the control plane never dials into a relay. Edge relays expose only the configured UDP data port and a loopback-only metrics listener.

The public proxy must deny `/v1/admin*` and `/internal/*`, except the two relay enrollment/certificate-renewal paths already allowed in the supplied Caddy file. `/internal/metrics` is additionally restricted by the control plane to trusted source CIDRs. Publish direct control-plane HTTP only on loopback. Expose the mTLS gRPC port only on a private/VPN network or restrict it to known relay source addresses.

## Required production secrets

Inject these through the platform secret store, not YAML, Compose files, images, command history, or Git:

- `ACCESS_TOKEN_PRIVATE_KEY_BASE64`
- `ADMIN_TOKENS`
- `GAME_SERVER_REGISTRATION_TOKENS`
- `RELAY_BOOTSTRAP_TOKENS`
- `RELAY_CA_CERT_PEM_BASE64` and `RELAY_CA_KEY_PEM_BASE64`
- `RELAY_TOKEN_PRIVATE_KEY_BASE64`
- `UPDATE_SIGNING_PRIVATE_KEY_BASE64`
- `DATABASE_URL` and `REDIS_PASSWORD`

Use distinct Ed25519 keys for access tokens, relay tokens, and update manifests. Keep old update public keys embedded in clients during rotation, publish the new `key_id` before using it, and remove an old public key only after every release it signed is outside the supported window. Relay bootstrap and registration tokens must be unique, high entropy, scoped, and rotated after exposure.

Production startup intentionally fails when signing keys, relay CA material, bootstrap credentials, administrator credentials, game-server registration credentials, or signed-update release descriptors are missing. Development-generated ephemeral keys are never suitable for rolling deployments.

## Release and update publishing

1. Upload immutable artifacts to object storage/CDN.
2. Calculate each artifact's exact byte size and lowercase SHA-256.
3. Add a release descriptor under the directory documented in `Backend/deployments/updates/README.md`.
4. Start a canary with the release directory mounted read-only and the signing key injected.
5. Fetch the manifest, verify its Ed25519 signature with the client public key, download an artifact, and verify size and SHA-256.
6. Promote the canary. Never give the CDN or object store the signing private key.

Keep stable and beta descriptors separate. A rollback publishes or selects a previously signed descriptor and rolls the control-plane image back; do not overwrite immutable object keys.

## Database migration, backup, and recovery

Migrations run at control-plane startup and are serialized by a PostgreSQL advisory lock. Before every production migration:

1. Run all migrations against a restored staging backup.
2. Create and verify a custom-format backup with `Backend/scripts/backup-postgres.ps1`.
3. Record the image digest, schema migration set, and backup object checksum.
4. Deploy one canary, validate readiness and smoke tests, then continue the rollout.

Store encrypted backups in a separate account/region with retention and immutability appropriate to the service. Test `Backend/scripts/restore-postgres.ps1 -ConfirmRestore` quarterly in an isolated database. The restore script uses `--single-transaction`; after restore, run migrations and smoke tests before admitting traffic. Rollback is application-first when migrations are backward compatible; for destructive schema incidents, stop writers and restore the verified backup under an incident plan.

## Monitoring and alerts

Start the local monitoring profile with:

```text
docker compose --env-file Backend/deployments/control-plane/.env \
  -f Backend/deployments/control-plane/docker-compose.yaml --profile monitoring up -d
```

Prometheus is bound to `127.0.0.1:9091` and Grafana to `127.0.0.1:3000`. Replace the local Grafana password. In production, scrape `/internal/metrics` only from a trusted private monitoring network and scrape each edge relay through a node-local agent on its loopback metrics address.

Alert on API P95 above 200 ms, elevated 5xx/error rate, any refresh-token reuse, sustained bind or room-join failures, PostgreSQL pool saturation, `control_plane_metrics_scrape_error`, goroutine/memory growth, relay allocation failures, relay nodes entering UNHEALTHY/OFFLINE, loss of relay control connectivity, high packet drops/rate-limit drops, and low relay capacity.

Structured logs must never contain access, refresh, relay, bootstrap, registration, or administrator tokens; private keys; or full game payloads. Retain request IDs, actor/resource IDs, status, duration, and sanitized failure codes.

## Capacity and resilience validation

Before promotion, run `Backend/tests/load/control-plane.js` with 100 virtual users and 100 short-lived staging access tokens. Require HTTP P95 below 200 ms, failures below 1%, checks above 99%, and stable WebSockets. Run a 30-minute soak while watching goroutines, memory, PostgreSQL pool usage, and Redis latency.

Run `Backend/tests/netem/run-relay-matrix.sh` only on an isolated Linux interface. Confirm control/WebSocket reconnection, idempotent joins, retryable BIND, relay migration, and heartbeat tolerance across latency, jitter, loss, reorder, duplication, rate limits, and short disconnects.

## Deployment smoke test

Validate, in order: liveness; readiness; client config; update check and signature; client bind/refresh/logout; banned-account write rejection; dedicated-server registration/heartbeat; P2P create/join/leave; direct connection; relay fallback; relay node drain/resume; and failure migration. Confirm public responses contain no internal addresses or credentials and confirm the public proxy returns 404 for admin and internal routes.

The complete separated-host procedure is in `docs/debian-deployment-and-ops.md`. Public API usage is in `docs/control-plane-external-api.md`; administrator, relay HTTP, mTLS gRPC, metrics, and UDP interfaces are in `docs/control-plane-internal-api.md`.
