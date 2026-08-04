# MetaServer 外部 API

[English](metaserver-external.md) | 简体中文

Base URL：`https://meta.dubnium.top`。除 `/connectServer` 外，JSON 成功响应使用统一
envelope：

```json
{"data": {}, "request_id": "req_..."}
```

错误响应为 `{"error":{"code":"...","message":"...","details":{}},"request_id":"req_..."}`。
报告问题时提供 request ID；不得把 bearer token 或 Gate Ticket 放入 URL、query、
日志、截图或 issue。

## 认证与限流

公开只读接口不需要凭据。玩家接口要求现有控制面签发的
`Authorization: Bearer <access-token>`，复用相同的签名、过期、撤销、
`RequireActive`、request ID、IP/玩家限流及封禁账号规则。请求中的玩家 ID 不作为身份。

## 公开发现

| 方法和路径 | 请求 | 响应 `data` |
| --- | --- | --- |
| `GET /health/live` | 无 | 进程存活 |
| `GET /health/ready` | 无 | PostgreSQL、Redis 和 35 号迁移 readiness |
| `GET /` | 无 | 服务/协议及动态 Relay 兼容列表 |
| `GET /v1/meta/regions` | 无 | 区域及 `qos_endpoints[]` |
| `GET /v1/meta/playlists` | 无 | 按 `sort_order` 排序的已启用 playlist |
| `GET /v1/meta/notifications?locale=zh-CN` | 可选 locale | 当前有效的本地化和全局通知 |

Relay endpoint 不是静态配置；只有 READY、心跳新鲜且接受新 allocation 的节点会出现。

## 会话与 MetaTunnel

`POST /v1/meta/sessions` 请求：

```json
{"client_version":"1.1.0","protocol_version":1,"platform":"windows"}
```

HTTP 201 响应包含 `user_id`、`gate_ticket`、`endpoint`、
`expires_in_seconds` 和 `protocol_version`。Ticket 具有 256 位随机熵，最多 60 秒
有效，且只能消费一次。

游戏兼容路径为 `POST /connectServer`。MetaTunnel 携带 bearer header 和受全局大小限制
的游戏旧请求体调用。不同 Boundary 正式版本的正文编码、字段名称及值类型并不一致，
因此兼容路径将正文视为不透明数据并直接丢弃，不执行 JSON 解码。服务端统一标记客户端
为 `boundary-legacy` 并选用服务端协议版本；身份始终来自 bearer token，不采用旧
`playerId` 或 `loginToken` 值。现代 MetaServer 接口仍严格校验字段类型和未知字段。
兼容路径直接返回游戏形状：

```json
{"error":0,"userId":"...","aceId":"...","gateToken":"...","endpoint":"logic.dubnium.top:443"}
```

Browser 启动 `meta-tunnel.exe` 后，只通过匿名 stdin 管道写入一行 Access Token，
读取一行 readiness JSON，再把游戏 LogicServerURL 指向其中的 loopback HTTP 端口。
MetaTunnel 将 endpoint 改写到本地 TCP listener 并校验远端 TLS 证书；应用不得实现
证书绕过。

## 玩家档案与配装

| 方法和路径 | Body | 结果 |
| --- | --- | --- |
| `GET /v1/users/me/meta-profile` | 无 | 等级、经验、货币、统计和 revision |
| `GET /v1/users/me/loadouts` | 无 | 全部角色配装 |
| `GET /v1/users/me/loadouts/{role_id}` | 无 | 一个经 definitions 校验的快照与 revision |
| `PUT /v1/users/me/loadouts/{role_id}` | `snapshot` 对象和当前 `revision` | 更新快照并递增 revision |

只有首次创建使用 revision `0`，后续必须发送上次读/写返回的 revision。过期 revision
返回 HTTP 409 `META_LOADOUT_REVISION_CONFLICT`；应重新读取并明确合并，不能盲重试。

## Party

| 方法和路径 | Body | 结果 |
| --- | --- | --- |
| `POST /v1/meta/parties` | `mode`、`region`、`client_version` | 调用者为 leader 的新 Party |
| `GET /v1/meta/parties/{party_id}` | 无 | 仅成员可见的 Party |
| `POST /v1/meta/parties/{party_id}/ready` | `{"ready":true}` | 更新后的 Party |
| `POST /v1/meta/parties/{party_id}/presence` | `{"presence":"ONLINE"}` | 更新后的 Party |

Presence 可为 `ONLINE`、`AWAY` 或 `IN_GAME`。每名玩家只能属于一个活动 Party；
无成员资格的查询按 not found 隐藏。

## 匹配

使用 `POST /v1/meta/matchmaking/tickets` 创建：

```json
{"party_id":"mp_...","mode":"default","region":"hgh","client_version":"1.1.0"}
```

单人匹配省略 `party_id`。Party 整体排队且只有 leader 可发起。接口返回 HTTP 202。
轮询 `GET /v1/meta/matchmaking/tickets/{ticket_id}`，直到 `state` 成为 `MATCHED`、
`FAILED` 或 `TIMED_OUT`。`MATCHED` 时包含 `match_id` 和 Dedicated Server
`endpoint`。队列中的 Ticket 可用 `DELETE` 取消，成功为 204。无 READY 服务器时不会
自动降级为玩家 P2P Host。

## 常见 Meta 错误

| 状态/code | 含义 |
| --- | --- |
| 400 `META_INVALID_REQUEST` | JSON、definition、标签或状态转换输入非法 |
| 401 通用认证 code | Access Token 缺失、过期、撤销或非法 |
| 404 `META_*_NOT_FOUND` | 资源不存在，或为防 IDOR 隐藏所有权/成员关系 |
| 409 `META_LOADOUT_REVISION_CONFLICT` | 配装 revision 过期 |
| 409 `META_PARTY_ALREADY_JOINED` | 玩家已属于活动 Party |
| 409 `META_PARTY_TICKET_REQUIRED` | 活动 Party 成员必须携带 Party ID 整体排队 |
| 409 `META_PARTY_NOT_QUEUEABLE` | Party 状态或成员关系不允许排队 |
| 409 `META_MATCH_TICKET_EXISTS` | 玩家已有活动 Ticket |
| 409 `META_MATCH_TICKET_NOT_CANCELLABLE` | Ticket 已离开排队状态 |
| Ticket 失败 `META_MATCH_CONNECTION_TIMEOUT` | 玩家连接前 Dedicated Server 预留已超时 |
| Ticket 失败 `META_MATCH_CANCELLED_BY_ADMIN` | 经审计的管理员操作取消了活动对局 |
| 429 通用限流 code | 仅在指定等待时间后重试 |

完整字段 Schema 见
[`Backend/api/openapi/openapi.yaml`](../../Backend/api/openapi/openapi.yaml)，原生 TCP
行为见[原生协议](../architecture/metaserver-native-protocol.zh-CN.md)。
