# 运维文档

[English](README.md) | 简体中文

## 部署与发布

- [部署入口](deployment.zh-CN.md)：三类主机的职责和推荐顺序；
- [Debian 完整部署手册](deployment-guide.zh-CN.md)：主机、端口、防火墙、FRP/HAProxy 和首次注册；
- [CI/CD](ci-cd.zh-CN.md)：GitHub Actions、GHCR 不可变产物和 Environment；
- [发布与回滚](release-and-rollback.zh-CN.md)：控制面发布与逐节点 Relay 滚动发布。
- [MetaServer 部署](metaserver-deployment.zh-CN.md)：控制面、公网网关、Relay、客户端和回滚分离清单；
- [Dedicated Server 注册](dedicated-server-registration.zh-CN.md)：邀请码/资格验证、一次性注册、Windows Agent 与节点身份轮转；
- [Admin Web 使用手册](admin-console-user-guide.zh-CN.md)：登录、玩家、联机、发布、治理和审计操作；
- [Admin Web 安全手册](admin-console-security.zh-CN.md)：入口分层、Secret 边界、管理员生命周期与安全检查。
- [下载目录存储](download-catalog.zh-CN.md)：S3/R2/MinIO、浏览器 CORS、上传校验与恢复。

## 稳定性与数据安全

- [Relay 连续在线与恢复](relay-continuity.zh-CN.md)：禁止周期重启、掉线恢复、证书续签和监控；
- [密钥与证书轮换](key-and-certificate-rotation.zh-CN.md)：Relay Token Keyset、节点证书和撤销；
- [备份与恢复](backup-and-restore.zh-CN.md)：PostgreSQL、离线密钥和恢复顺序；
- [事故 Runbook](runbooks/README.zh-CN.md)：按告警类型处理生产故障。
