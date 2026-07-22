# 运维文档

[English](README.md) | 简体中文

## 部署与发布

- [部署入口](deployment.zh-CN.md)：三类主机的职责和推荐顺序；
- [Debian 完整部署手册](deployment-guide.md)：主机、端口、防火墙、FRP/HAProxy 和首次注册；
- [CI/CD](ci-cd.md)：GitHub Actions、GHCR 不可变产物和 Environment；
- [发布与回滚](release-and-rollback.md)：控制面发布与逐节点 Relay 滚动发布。

## 稳定性与数据安全

- [Relay 连续在线与恢复](relay-continuity.zh-CN.md)：禁止周期重启、掉线恢复、证书续签和监控；
- [密钥与证书轮换](key-and-certificate-rotation.md)：Relay Token Keyset、节点证书和撤销；
- [备份与恢复](backup-and-restore.md)：PostgreSQL、离线密钥和恢复顺序；
- [事故 Runbook](runbooks/README.zh-CN.md)：按告警类型处理生产故障。
