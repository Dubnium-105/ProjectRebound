# ProjectRebound 文档中心

[English](README.md) | 简体中文

这里是整个项目的文档入口。先阅读[系统总览](architecture/overview.zh-CN.md)，再按任务进入 API、运维、测试或组件文档。历史方案集中在 [`archive/`](archive/README.md)，不得作为当前实现或部署依据。

## 权威顺序

发生冲突时按以下顺序判断：

1. 机器可读契约：`Backend/api/openapi/openapi.yaml`、`Backend/api/proto/relay_control.proto`；
2. 当前实现、数据库迁移和自动化测试；
3. `docs/api/`、`docs/architecture/` 和 `docs/operations/`；
4. `docs/testing/` 中的特定版本证据；
5. `docs/archive/` 仅供追溯。

## 按角色进入

| 角色 / 任务 | 首选入口 |
| --- | --- |
| 新开发者理解系统 | [系统总览](architecture/overview.zh-CN.md) |
| 客户端或游戏服务器接入 | [API 文档](api/README.zh-CN.md) |
| 部署控制面、公网网关或中继 | [部署入口](operations/deployment.zh-CN.md) |
| 日常发布、回滚或故障处理 | [运维文档](operations/README.zh-CN.md) |
| 验证 V1.1 候选版本 | [V1.1 验收索引](testing/v1.1/README.zh-CN.md) |
| 维护某个代码组件 | 组件目录内的 `README.md` |

## 信息架构

| 目录 | 内容 |
| --- | --- |
| [`architecture/`](architecture/README.zh-CN.md) | 系统边界、认证、Relay 协议、迁移和桌面运行时命令 |
| [`api/`](api/README.zh-CN.md) | 外部 HTTP/WebSocket、内部管理、Relay HTTP/mTLS API |
| [`operations/`](operations/README.zh-CN.md) | 部署、CI/CD、发布、备份、证书和连续在线策略 |
| [`operations/runbooks/`](operations/runbooks/README.zh-CN.md) | 事故响应和恢复步骤 |
| [`testing/`](testing/README.zh-CN.md) | 测试策略、版本验收证据和 Release Gate |
| [`archive/`](archive/README.md) | 被替代的 API、方案、审计和实施快照 |

## 靠近代码的组件文档

以下文档保留在实现旁边，以便随代码同步维护：

- `Backend/api/openapi/auth-permission-matrix.md`：权限矩阵；
- `Backend/api/relay-protocol.md`：UDP 数据面机器级协议说明；
- `Backend/deployments/README.md`：Compose 和部署资产；
- `Backend/tests/integration/README.md`：真实控制面/双中继集成门禁；
- `Backend/tests/load/README.md`：负载与长稳测试；
- `Backend/tests/netem/README.md`：弱网矩阵；
- `Desktop/ProjectRebound.Browser.Python/README.md`：旧 Python 浏览器兼容范围；
- `Tools/` 下各 README：独立诊断工具。

## 维护规则

- 遵循[双语文档标准](documentation-standard.zh-CN.md)。
- API 变化先更新 OpenAPI/proto，再同步人类可读 API 文档和测试。
- 架构文档解释稳定边界，不记录一次性主机 IP、临时 Token 或实施日志。
- 生产示例只使用不可变 commit SHA 或 digest，不使用 `latest`，不在目标机现场构建。
- 健康 Relay 不做周期重启；故障注入与连续在线长稳分开执行。
- 被替代的文档移动到 `archive/`，并说明替代入口和停止维护原因。
- 提交前运行文档标准中列出的两项检查。
