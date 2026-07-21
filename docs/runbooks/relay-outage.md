# Relay outage and migration runbook

Trigger on `RelayNodeOffline`, `NoRelayAvailable`, high capacity, BIND failure, memory growth, or migration failure alerts.

1. Stop new Relay releases. Check the dynamic inventory, control connection, heartbeat age, load state, capacity, software/protocol version, and certificate expiry.
2. If the process is unhealthy but reachable, drain with `migrate_existing=true` and a bounded deadline. Do not restart all nodes in a region together.
3. Confirm affected connections emit `connection.relay_migrating`, receive new participant-specific allocations, and end in `relay_migrated` or a bounded `relay_failed`.
4. Restart or roll back only the affected node. It must reconnect as `CONNECTING`, heartbeat, and reach `READY` before resume/new scheduling.
5. If no target exists, add capacity or recover a node; do not loop migrations. Clients must surface the failed connection.
6. Compare allocation counts before/after and confirm old handles no longer forward. Preserve Relay/control logs without Tokens or private keys.
