# API 文档

[English](README.md) | 简体中文

| 文档 | 面向对象 |
| --- | --- |
| [外部 API](external.zh-CN.md) | 游戏客户端、桌面浏览器、Dedicated Server 和更新客户端 |
| [ToolBox 注册与接入指导书](toolbox-integration-guide.zh-CN.md) | 接入玩家、专服、P2P 与 VNT 注册流程的 ToolBox 开发者和运营者 |
| [ToolBox 模块代码更改与四类接入流程指导书](toolbox-code-change-and-registration-flow-guide.zh-CN.md) | 实施玩家、专服、P2P 与 VNT 节点完整接线的 ToolBox 开发和交付人员 |
| [内部 API](internal.zh-CN.md) | 管理员、Relay 节点、监控系统和控制面内部组件 |
| [MetaServer 外部 API](metaserver-external.zh-CN.md) | MetaTunnel、游戏客户端、档案、Party 和匹配 |
| [MetaServer 内部 API](metaserver-internal.zh-CN.md) | Dedicated Server、管理员、指标和导入 |
| [OpenAPI 契约](../../Backend/api/openapi/openapi.yaml) | HTTP 机器可读契约 |
| [Relay 控制面 protobuf](../../Backend/api/proto/relay_control.proto) | Relay mTLS gRPC 机器可读契约 |
| [权限矩阵](../../Backend/api/openapi/auth-permission-matrix.zh-CN.md) | Token 权限 |

外部与内部入口必须使用不同的网络信任边界。Relay UDP 数据面不是 HTTP API，见[架构层 Relay 协议](../architecture/relay-protocol.zh-CN.md)。
