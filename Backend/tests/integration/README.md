# V1.1 disposable integration gates

English | [简体中文](README.zh-CN.md)

These gates are intentionally separate from the normal Go module. They require a Linux Docker host and create only temporary containers, volumes, networks, keys, players, rooms, and Relay identities.

The integration image uses the explicitly gated `test-ticket-verifier` fixture
to exercise verified Steam sessions. It does not decrypt Steam tickets and is
not included in the default production image target. Never set
`ALLOW_INSECURE_TEST_TICKET_VERIFIER=1` outside an isolated test stack.

## Control Plane, two Relay nodes, weak network, and short fault recovery

The Testcontainers gate explicitly rebuilds the current source (so a stale local image cannot bypass a code change), starts PostgreSQL 17, Redis 7, the Control Plane with its integrated workers, and two Edge Relay nodes on `198.18.11.0/24`. It runs the real auth, room, WebSocket, Relay allocation, protocol-v2 BIND, and UDP data path. It then injects mild, moderate, and severe `netem` profiles into one Relay container, runs a 100-client/50-allocation reconnect storm, SIGKILLs an active Relay and requires successful migration, and restarts Redis, PostgreSQL, and the Control Plane before repeating the clean end-to-end flow. Post-restart readiness requires a Relay heartbeat newer than the Control Plane restart, not a stale READY row.

Port `127.0.0.1:28080` and subnet `198.18.11.0/24` must be unused. The safety wrapper refuses to run without an explicit disposable-environment acknowledgement:

```bash
cd Backend/tests/integration
sudo env \
  V11_INTEGRATION_I_UNDERSTAND=disposable-docker-stack \
  TESTCONTAINERS_RYUK_DISABLED=true \
  ./run-gate.sh
```

The wrapper and test always request Compose volume/orphan cleanup. If the process is killed outside Go cleanup, find only projects whose names start with `project-rebound-v11-`, inspect their labels, and remove those exact temporary projects.

## Encrypted PostgreSQL restore drill

The restore drill creates two unrelated PostgreSQL 17 containers. It migrates and seeds the source, including deliberately active room/connection/Relay records, runs the production backup, checksum, encryption, verification, and restore scripts, then verifies that the restored schema reaches the latest migration file, checks 22 core recovery-invariant tables, restores the fixture player and backup/restore metrics, and reruns migration idempotency against the fresh target. The production restore transaction must also close the restored room, fail the restored connection/allocation, mark the Relay OFFLINE, and reset active member/allocation counts so ephemeral live state cannot be resurrected from a snapshot.

It requires matching PostgreSQL client tools, `age`, and Docker:

```bash
cd Backend/tests/integration
sudo env \
  PATH="$PATH" \
  RESTORE_DRILL_I_UNDERSTAND=disposable-postgres-containers \
  ./run-restore-drill.sh
```

The command prints `RESTORE_DRILL_OK`, RTO, total duration, schema version, restored player ID, required-table count, and encrypted-backup SHA-256. The generated identity and backup exist only in the validated temporary directory and are deleted by the exit trap.

Neither script is permitted in a production environment. The 6-hour/24-hour soak scenarios remain separate in [`../load/README.md`](../load/README.md).
