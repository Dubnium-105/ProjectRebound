# Relay outage and migration runbook

English | [简体中文](relay-outage.zh-CN.md)

Trigger on `RelayNodeOffline`, `NoRelayAvailable`, high capacity, BIND failure, memory growth, or migration failure alerts.

1. Stop new Relay releases. Check the dynamic inventory, container state, control connection, heartbeat age, load state, capacity, software/protocol version, certificate expiry, and active allocations. A stale database `READY` row is not proof that the node is online.
2. If the process is still running, allow its built-in backoff to reconnect. Do not restart a healthy or merely transiently disconnected node. If the process is unhealthy but reachable, drain with `migrate_existing=true` and a bounded deadline. Do not restart all nodes in a region together.
3. Confirm affected connections emit `connection.relay_migrating`, receive new participant-specific allocations, and end in `relay_migrated` or a bounded `relay_failed`.
4. Restart or roll back only a container confirmed stopped, or a drained node with zero active allocations. It must reconnect as `CONNECTING`, emit a new heartbeat, and reach `READY` before resume/new scheduling. If its certificate expired or identity was revoked, stop retrying and perform controlled re-enrollment; never copy another node's identity.
5. If no target exists, add capacity or recover a node; do not loop migrations. Clients must surface the failed connection.
6. Compare allocation counts before/after and confirm old handles no longer forward. Preserve Relay/control logs without Tokens or private keys.

See [Relay continuity and recovery](../relay-continuity.md) for certificate renewal, monitoring fields, and the separation between continuous soak and fault injection.
