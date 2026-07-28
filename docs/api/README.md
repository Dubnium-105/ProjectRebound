# API documentation

English | [简体中文](README.zh-CN.md)

| Document | Audience |
| --- | --- |
| [External API](external.md) | Game clients, desktop browser, dedicated servers, and update clients |
| [Internal API](internal.md) | Administrators, Relay nodes, monitoring, and control-plane components |
| [MetaServer external API](metaserver-external.md) | MetaTunnel, game clients, profile, Party, and matchmaking |
| [MetaServer internal API](metaserver-internal.md) | Dedicated Servers, administrators, metrics, and import |
| [OpenAPI contract](../../Backend/api/openapi/openapi.yaml) | Machine-readable HTTP contract |
| [Relay control protobuf](../../Backend/api/proto/relay_control.proto) | Machine-readable mTLS gRPC contract |
| [Authorization matrix](../../Backend/api/openapi/auth-permission-matrix.md) | Token permissions |

External and internal endpoints use different network trust boundaries. The Relay UDP data plane is not an HTTP API; see the [Relay protocol architecture](../architecture/relay-protocol.md).
