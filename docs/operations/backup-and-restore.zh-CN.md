# PostgreSQL 与密钥备份恢复

[English](backup-and-restore.md) | 简体中文

## 每日数据库备份

安装匹配服务器主版本的 PostgreSQL client、`age` 和可选 `rclone`。备份脚本先生成 custom-format 压缩 dump，以 `pg_restore --list` 校验，再用 age 公钥加密并生成 SHA-256。未配置加密接收者时拒绝备份。

```bash
export DATABASE_URL='postgres://...'
export BACKUP_DIRECTORY=/srv/projectrebound-backups/postgres
export BACKUP_ENCRYPTION_RECIPIENT='age1...'
export BACKUP_RETENTION_DAYS=14
export BACKUP_RCLONE_REMOTE='remote:projectrebound/postgres' # 可选异机/对象存储
scripts/backup/postgres-backup.sh
```

systemd timer/cron 每日调用；周备份复制到 8 周保留前缀，月备份复制到 12 月保留前缀。监控 `BACKUP_OK` 时间、文件大小、校验结果和远端对象存在性。私钥、CA、Access/Relay/Manifest 签名密钥及管理员恢复凭据必须使用另一套 age 接收者离线备份至少两份，且至少一份不在控制面主机；不得进入 Git 或数据库 dump。

## 校验与全新环境恢复

```bash
export BACKUP_AGE_IDENTITY_FILE=/secure/offline/backup-age-key.txt
scripts/backup/verify-backup.sh /srv/.../projectrebound-....dump.age

export DATABASE_URL='postgres://.../empty_projectrebound?sslmode=require'
export RESTORE_I_UNDERSTAND=replace-target-database
scripts/backup/postgres-restore.sh /srv/.../projectrebound-....dump.age
```

For alerting, set `BACKUP_METRICS_DIRECTORY=/var/lib/node_exporter/textfile_collector` and enable node-exporter's textfile collector for that directory. Backup, verification, and restore-drill timestamps are written atomically to separate `.prom` files. A failed run updates its status gauge without deleting the previous successful backup timestamp.

恢复顺序：空 PostgreSQL → 数据库 dump → 恢复脚本在单事务内终止快照中的房间/connection/allocation/migration 并将节点置为 OFFLINE → Access/Relay/Manifest 密钥与 Relay CA → Control Plane 非破坏迁移 → 管理员与 player_id 验证 → 旧 Manifest 验签 → Relay 重新连接或重新注册。易失状态必须在接入公网前完成清理，不能等待普通 TTL。

每周在全新隔离环境演练并更新 `docs/testing/v1.1/restore-test-report.md`：备份与独立密钥包 hash、开始/结束时间、数据库恢复耗时、应用 RTO/RPO、表行数、关键身份/Manifest 验证、Relay 重连和失败项。没有真实执行证据时不得写“恢复成功”。
