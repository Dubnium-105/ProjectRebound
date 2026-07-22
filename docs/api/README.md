# API 文档

| 文档 | 面向对象 |
| --- | --- |
| [外部 API](external.md) | 游戏客户端、桌面浏览器、Dedicated Server 和更新客户端 |
| [内部 API](internal.md) | 管理员、Relay 节点、监控系统和控制面内部组件 |
| [`Backend/api/openapi/openapi.yaml`](../../Backend/api/openapi/openapi.yaml) | HTTP 机器可读契约 |
| [`Backend/api/proto/relay_control.proto`](../../Backend/api/proto/relay_control.proto) | Relay mTLS gRPC 机器可读契约 |
| [`Backend/api/openapi/auth-permission-matrix.md`](../../Backend/api/openapi/auth-permission-matrix.md) | Token 权限矩阵 |

外部与内部入口必须使用不同的网络信任边界。Relay UDP 数据面不是 HTTP API，见[架构层 Relay 协议](../architecture/relay-protocol.md)。
