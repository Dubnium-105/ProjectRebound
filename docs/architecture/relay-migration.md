# Relay failover

English | [简体中文](relay-migration.zh-CN.md)

## Triggering and scheduling qualifications

The control plane marks nodes `UNHEALTHY` and then `OFFLINE` according to the configured heartbeat lease. A disconnected control channel ultimately triggers the same process through lease timeout. The scheduler selects only nodes that meet both conditions below:

- The life cycle status is `READY`;
- The operating load status is `NORMAL` or `DEGRADED`;
- Relay protocol version is 2;
- Node certificates and heartbeat leases are still valid;
- The number of allocations and egress bandwidth are below the capacity threshold;
- Support required transport protocols.

The recovery node must reconnect and complete the heartbeat and return to the scheduling pool by pressing `OFFLINE -> CONNECTING -> READY`; the old allocation will not be revived.

## Failover migration state machine

1. An idempotent background task queries the active allocation on the failed node.
2. Add an active migration unique constraint to the connection to ensure that there is at most one `BINDING` migration at the same time.
3. The original allocation is immediately marked with `FAILED`, and a new allocation is created on another qualified node.
4. Control Plane sends `connection.relay_migrating` and independent `connection.relay_allocated` to both parties in sequence. The latter contains new short-term Relay Tokens.
5. After the HOST and PEER of the new Relay are both bound, `AllocationOpened` will be reported; repeated reporting is idempotent.
6. Migrate mark `COMPLETED`, connect back to `CONNECTED`, and send `connection.relay_migrated`.

Each attempt has `migration_id`, incrementing `attempt`, and bind deadline. By default, if BIND is not completed within 45 seconds, it is marked `BIND_TIMEOUT`, frees up the node capacity, and selects a node that has not been tried before. The default is 3 attempts at most. When there are no nodes available or attempts are exhausted, the connection enters `FAILED` and sends `connection.relay_failed`; the background task does not retry infinitely.

## WebSocket event sequence

```text
connection.relay_migrating
connection.relay_allocated
connection.relay_migrated | connection.relay_failed
```

`connection.relay_migrating` contains old node, old allocation, reason, attempt and `migration_id` does not contain credentials. `connection.relay_allocated` sends different Relay Tokens to HOST/PEER respectively, and attaches `migration_id` and the old allocation during migration. Clients MUST handle duplicate events idempotently with `migration_id` and `allocation_id`.

## Consistency Boundary

Real-time UDP endpoints are only stored in Edge memory and do not write to PostgreSQL. PostgreSQL saves connections, nodes, allocations, migration attempts, deadlines, and failure reasons. The database unique index prevents multiple concurrent active migrations on the same connection; row locks and conditional updates keep repeated sweeps, repeated BIND successes, and parallel execution of multiple Control Plane instances idempotent.

V1.1 migrations allow brief outages and do not promise lossless switchovers, packet retransmissions, or host migrations.

Force Drain to use make-before-break: the old allocation enters `MIGRATING` in the database and does not occupy the "current new allocation" unique index but is still counted in the node capacity; only after the BIND of both parties of the new allocation is completed, the old allocation becomes `CLOSED` and the undo command is received. If migration attempts are exhausted, the connection and `MIGRATING` allocation fail to clean up.

## Administrator Drain

```http
POST /internal/v1/relay-nodes/{node_id}/drain
Content-Type: application/json

{"deadline_seconds":600,"migrate_existing":false}
```

Empty request bodies remain compatible with older callers: they use the server's default deadline and do not actively migrate allocations. `migrate_existing=false` immediately stops new scheduling and lets existing allocations end naturally. `true` gradually migrates existing connections through the same bounded-retry state machine as failover migration, with reason `RELAY_DRAIN`. The deadline is delivered through the control channel; when it expires, the edge closes remaining allocations and reports their closure. `ConfigSnapshot` restores the persisted drain state when the node reconnects.

To restore the connection traffic, you need to call it explicitly:

```http
POST /internal/v1/relay-nodes/{node_id}/resume
```
