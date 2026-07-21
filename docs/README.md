# ProjectRebound 文档索引

本目录只在顶层保留当前可执行、可维护的说明。历史 API、实施前审计和旧架构方案集中在 [`archive/`](archive/README.md)，不再参与当前运维决策。

## 权威顺序

发生冲突时按以下顺序判断：

1. 机器可读契约：`Backend/api/openapi/openapi.yaml`、`Backend/api/proto/relay_control.proto`；
2. 当前 API 文档与实现测试；
3. 当前部署和 CI/CD 手册；
4. `archive/` 历史快照仅供追溯，永远不是当前依据。

## 当前文档

| 主题 | 文档 | 用途 |
| --- | --- | --- |
| CI/CD | [`cicd.md`](cicd.md) | GitHub Environments、GHCR SHA 镜像、自动部署与回滚 |
| Debian 部署 | [`debian-deployment-and-ops.md`](debian-deployment-and-ops.md) | 控制面和 Edge Relay 分离部署、升级、备份和验收 |
| V1.1 部署入口 | [`deployment.md`](deployment.md) | 控制面、公网网关与 Edge Relay 的角色边界和发布入口 |
| V1.1 认证 | [`auth.md`](auth.md) | bind 风控、邀请码、Refresh rotation 与 Session 管理 |
| 外部 API | [`control-plane-external-api.md`](control-plane-external-api.md) | 客户端、Dedicated Server、P2P、连接与更新 API |
| 内部 API | [`control-plane-internal-api.md`](control-plane-internal-api.md) | Admin、Relay 注册、mTLS gRPC、指标与 UDP 数据面 |
| Relay 协议 V2 | [`relay-protocol.md`](relay-protocol.md) | Challenge、Token、认证数据包、MTU 与兼容性 |
| Relay 迁移 | [`relay-migration.md`](relay-migration.md) | 故障、Drain、幂等迁移和恢复状态机 |
| 密钥与证书 | [`key-rotation.md`](key-rotation.md) | Relay Token Keyset、mTLS 续期与撤销 |
| 备份恢复 | [`backup-restore.md`](backup-restore.md) | PostgreSQL、离线密钥、校验和恢复顺序 |
| 发布回滚 | [`release-and-rollback.md`](release-and-rollback.md) | 不可变镜像、preflight、滚动 Edge 和自动回滚 |
| 命名管道 | [`command-framework.md`](command-framework.md) | 桌面浏览器与 Payload 的运行时指令协议 |

## V1.1 验收和 Runbook

| 文档 | 用途 |
| --- | --- |
| [`v1.1/test-report.md`](v1.1/test-report.md) | Release Gate、自动化结果和未执行的长稳/故障项目 |
| [`v1.1/restore-test-report.md`](v1.1/restore-test-report.md) | 全新环境恢复演练证据模板 |
| [`v1.1/release-checklist.md`](v1.1/release-checklist.md) | 候选版本发布签字清单 |
| [`runbooks/auth-abuse.md`](runbooks/auth-abuse.md) | bind 滥用、Token 重放和邀请码异常 |
| [`runbooks/relay-outage.md`](runbooks/relay-outage.md) | Relay 离线、容量和迁移故障 |
| [`runbooks/database-redis-outage.md`](runbooks/database-redis-outage.md) | PostgreSQL/Redis 故障 |
| [`runbooks/key-compromise.md`](runbooks/key-compromise.md) | Relay Key/证书泄露 |
| [`runbooks/backup-restore.md`](runbooks/backup-restore.md) | 备份失败和恢复演练 |
| [`runbooks/chaos-testing.md`](runbooks/chaos-testing.md) | 隔离环境弱网与故障注入 |

## 组件文档和契约

| 路径 | 内容 |
| --- | --- |
| `Backend/api/openapi/openapi.yaml` | 外部 HTTP API 机器可读契约 |
| `Backend/api/openapi/auth-permission-matrix.md` | Access Token 权限矩阵 |
| `Backend/api/proto/relay_control.proto` | Relay 控制面 gRPC 契约 |
| `Backend/api/relay-protocol.md` | Edge Relay UDP 数据面协议 |
| `Backend/deployments/README.md` | Compose 与部署入口 |
| `Backend/deployments/updates/README.md` | 签名更新描述符 |
| `Backend/tests/load/README.md` | 控制面负载测试 |
| `Backend/tests/netem/README.md` | Relay 弱网测试 |
| `Desktop/ProjectRebound.Browser.Python/README.md` | 旧 Python 浏览器兼容原型及限制 |
| `Tools/NatPunchTest/README.md` | 旧内嵌 NAT/Relay 兼容测试，不适用于当前 Edge Relay |

## 维护规则

- 当前文档不记录短期实施清单或某次测试机 IP；这类信息进入 issue、变更单或历史归档。
- API 变化先更新 OpenAPI/proto，再同步人类可读文档和测试。
- 部署示例只使用完整 commit SHA 镜像；不以 `latest`、可移动标签或目标机现场构建作为生产流程。
- 被替代的文档移动到 `archive/`，在标题后标明替代文档和停止维护原因。
- 提交前运行 `python Tools/Docs/check_markdown_links.py`；CI 会拒绝失效的仓库内相对链接。
