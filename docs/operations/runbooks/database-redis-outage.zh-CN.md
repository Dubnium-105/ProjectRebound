# PostgreSQL 和 Redis 中断操作手册

[English](database-redis-outage.md) | 简体中文

在 `PostgreSQLUnavailable`、`DatabasePoolNearlyExhausted`、`RedisUnavailable` 或重复的后台作业失败警报上触发。

1. 冻结发布和破坏性管理。检查主机磁盘/索引节点、容器状态、最近重新启动、池利用率和依赖延迟。
2.PostgreSQL具有权威性。根据数据库提供商的要求恢复连接或故障转移；不要从未经验证的备份启动第二个可写副本。
3.Redis用于分布式绑定限制。及时恢复；有限的局部后备仍然是防御性的，但不是整个舰队的替代品。
4. 恢复后验证 `/health/ready`、`postgres_available`、`redis_available`、迁移校验和状态、池恢复、身份验证绑定/刷新、房间心跳和中继分配。
5. 运行清理清理程序并确认没有卡住的会话、房间、连接、迁移或分配。仅在确认损坏或数据丢失时升级到备份/恢复 Runbook。
