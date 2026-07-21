# V1.1 release and rollback

Production deployment uses immutable GHCR references: `sha-<40-character commit>`, a release tag such as `1.1.0`, or preferably `@sha256:<digest>`. `latest` is rejected. CI records the Git commit, UTC build time, Go version, image digest, Relay protocol version, and schema version with every image artifact.

## Migration policy

V1.1 migrations 000009 through 000016 are Expand/Migrate changes: they add tables, indexes, constraints, compatible fields, and the non-destructive Relay allocation `MIGRATING` state. The control-plane migrator serializes startup with a PostgreSQL advisory lock, wraps each migration in a transaction, and rejects the deployment if an already-applied checksum changed. No V1.1 migration drops a table or column. Contract changes are deferred to a later release after old code is retired and a restore drill has passed; normal image rollback therefore does not roll back the database.

## Control Plane release

Prepare `DATABASE_URL`, `BACKUP_ENCRYPTION_RECIPIENT`, `CONTROL_PLANE_ENV_FILE`, `CONTROL_PLANE_IMAGE`, an accessible `PREFLIGHT_OBJECT_STORAGE_PROBE_URL`, and optionally an off-host `BACKUP_RCLONE_REMOTE`. Then run:

```bash
cd Backend
scripts/release/control-plane.sh
```

The script creates and validates an encrypted backup, runs `scripts/release/preflight.sh`, pulls and verifies the image digest, starts the new control plane (which applies compatible migrations), performs public/internal smoke checks, and observes PostgreSQL/Redis metrics. A failed deployment restores the previous image and leaves compatible migrations in place.

For a single control-plane instance, announce a maintenance window before running it. A multi-instance deployment should start one new instance, pass health and a small traffic canary, then move the remaining traffic.

## Relay rolling release

Upgrade exactly one node at a time:

```bash
export RELAY_NODE_ID=relay_hgh
export RELAY_ADMIN_BASE_URL=http://127.0.0.1:18080
export RELAY_ADMIN_TOKEN='...'
export EDGE_RELAY_IMAGE='ghcr.io/dubnium-105/projectrebound-edge-relay@sha256:...'
Backend/scripts/release/rolling-edge-relay.sh
```

The script requests `DRAINING` with migration enabled, waits for `active_allocations=0`, deploys the pinned image, waits for the mTLS control connection, resumes the node, and verifies `READY`. On failure it attempts the previous image and resumes the node. Do not run this concurrently on every Relay.

## Manual rollback

Stop further releases, preserve logs and the release record, and run:

```bash
CONTROL_PLANE_ENV_FILE=/secure/control-plane.env \
  Backend/scripts/release/rollback.sh control-plane \
  ghcr.io/dubnium-105/projectrebound-control-plane@sha256:...
```

For a Relay, drain it first and use `edge-relay` as the target. After rollback validate Health, Auth bind/refresh, room create/join/heartbeat, WebSocket delivery, Relay BIND/data forwarding, and migration. Use database restore only for an explicitly reviewed destructive migration or confirmed data corruption; it is not part of an ordinary V1.1 image rollback. Record the trigger, timestamps, image digests, database schema, affected nodes, validation results, and operator in the incident report.

## Release gates

- configuration has no placeholders and secret files are mode 0600;
- PostgreSQL, Redis, object storage probe, OpenAPI, migration state, disk, backup freshness, and image digest pass preflight;
- CI `go test -race ./...`, `go vet ./...`, Compose, shell, Caddy, Prometheus, and image jobs pass;
- no `latest` image and no simultaneous Relay fleet restart;
- backup checksum/verification succeeds before migration;
- dashboards and alerts show the new instance healthy during the observation window.
