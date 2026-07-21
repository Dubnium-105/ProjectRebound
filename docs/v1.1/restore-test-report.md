# V1.1 恢复演练报告

状态：`NOT_RUN`

| 字段 | 结果 |
| --- | --- |
| 演练环境/日期 | 待填写 |
| 加密备份文件与 SHA-256 | 待填写 |
| 备份时间 / 恢复点（RPO） | 待填写 |
| 恢复开始、结束与 RTO | 待填写 |
| PostgreSQL 版本 | 待填写 |
| 关键表行数校验 | 待填写 |
| player_id / 管理员验证 | 待填写 |
| 旧 Manifest 验签 | 待填写 |
| Relay 重新连接 | 待填写 |
| 旧活动 connection 未恢复 | 待填写 |
| 失败项目与证据链接 | 待填写 |

只有在全新隔离环境执行 `verify-backup.sh`、`postgres-restore.sh`、迁移和应用 smoke tests，并保存日志后，才可把状态改为 `PASS`。
