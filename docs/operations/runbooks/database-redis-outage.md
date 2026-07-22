# PostgreSQL and Redis outage runbook

Trigger on `PostgreSQLUnavailable`, `DatabasePoolNearlyExhausted`, `RedisUnavailable`, or repeated background-job failure alerts.

1. Freeze releases and destructive administration. Check host disk/inodes, container state, recent restarts, pool utilization, and dependency latency.
2. PostgreSQL is authoritative. Restore connectivity or fail over according to the database provider; do not start a second writable copy from an unverified backup.
3. Redis is used for distributed bind limiting. Restore it promptly; the bounded local fallback remains defensive but is not a fleet-wide substitute.
4. After recovery verify `/health/ready`, `postgres_available`, `redis_available`, migration checksum state, pool recovery, Auth bind/refresh, room heartbeat, and Relay allocation.
5. Run cleanup sweepers and confirm no stuck sessions, rooms, connections, migrations, or allocations. Escalate to the backup/restore runbook only for confirmed corruption or data loss.
