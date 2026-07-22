# Production incident runbooks

English | [简体中文](README.zh-CN.md)

| Alert or event | Runbook |
| --- | --- |
| Relay offline, capacity exhaustion, or migration failure | [Relay outage](relay-outage.md) |
| PostgreSQL or Redis unavailable | [Database/Redis outage](database-redis-outage.md) |
| Login abuse, token replay, or invitation anomaly | [Authentication abuse](auth-abuse.md) |
| Signing key, Relay CA, or node credential compromise | [Key compromise](key-compromise.md) |
| Backup failure or restore drill | [Backup and restore](backup-restore.md) |
| Isolated weak-network or fault injection | [Chaos testing](chaos-testing.md) |

Contain the impact first, preserve logs and a timeline, then perform reversible recovery. Never restart the whole Relay fleet merely to refresh status.
