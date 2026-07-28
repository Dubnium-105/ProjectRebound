# 生产事故 Runbook

[English](README.md) | 简体中文

| 告警 / 事件 | Runbook |
| --- | --- |
| Relay 离线、容量不足、迁移失败 | [Relay outage](relay-outage.zh-CN.md) |
| PostgreSQL 或 Redis 不可用 | [Database/Redis outage](database-redis-outage.zh-CN.md) |
| 登录滥用、Token 重放、邀请码异常 | [Auth abuse](auth-abuse.zh-CN.md) |
| Admin Turnstile、Siteverify 或登录入口异常 | [Admin Turnstile login](admin-turnstile-login.zh-CN.md) |
| MetaServer readiness、Logic TLS、Gate 重放、匹配或 QoS 故障 | [MetaServer incident](metaserver.zh-CN.md) |
| 签名密钥、Relay CA 或节点凭据泄露 | [Key compromise](key-compromise.zh-CN.md) |
| 备份失败或恢复演练 | [Backup/restore](backup-restore.zh-CN.md) |
| 隔离环境弱网和故障注入 | [Chaos testing](chaos-testing.zh-CN.md) |

处理事件时先阻止扩大影响，保留日志与时间线，再执行可逆恢复。不要为了“刷新状态”同时重启全部 Relay。
