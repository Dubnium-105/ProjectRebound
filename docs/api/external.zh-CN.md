# ProjectRebound 外部 API

[English](external.md) | 简体中文

本文档描述客户端、Dedicated Server 和更新器可访问的 API。机器可读的字段类型、长度、枚举和所有响应 Schema 以 `Backend/api/openapi/openapi.yaml` 为准；该文件与路由契约测试一起校验。

## 1. 连接约定

- 生产 Base URL：`https://api.example.com`
- HTTP 请求和响应：`application/json; charset=utf-8`
- WebSocket：`wss://api.example.com/v1/realtime/connect`
- 时间：RFC 3339 UTC，例如 `2026-07-18T12:00:00Z`
- ID：带资源前缀的不透明字符串；客户端不得解析或自行构造。
- 分页：`cursor` + `limit`，`limit` 范围 1–100，默认 50。
- 幂等：重复 logout、leave、close、deregister 返回当前最终状态；客户端仍应避免并发重复写入。

成功响应统一为：

```json
{
  "data": {},
  "request_id": "req_..."
}
```

失败响应统一为：

```json
{
  "error": {
    "code": "INVALID_REQUEST",
    "message": "Request validation failed.",
    "details": {}
  },
  "request_id": "req_..."
}
```

客户端可以发送 `X-Request-Id`，但服务端会验证并规范化；排障时以响应中的 `request_id` 为准。请求体上限默认 1 MiB。

## 2. 鉴权类型

| 名称 | Header | 获得方式 | 用途 |
| --- | --- | --- | --- |
| Player Access Token | `Authorization: Bearer <jwt>` | `/v1/auth/bind` 或 `/refresh` | 玩家写操作、个人资料、连接/WebSocket |
| Refresh Token | JSON 字段 `refresh_token` | bind/refresh | 轮换 Access Token；每次使用后旧值失效 |
| Game Server Registration Token | `Authorization: Bearer <token>` | 运维预配置 | 注册 Dedicated Server |
| Game Server Token | `Authorization: Bearer <token>` | 注册响应，仅返回一次 | 对应 Server 心跳和注销 |
| Room Host Token | `X-Room-Host-Token: <token>` | 创建房间响应，仅返回一次 | 房主心跳、启动和关闭 |

`account_status=BANNED` 的玩家仍可 bind、refresh、logout 和读取本人资料，但不能执行房间或连接写操作。当前 bind 接受客户端自述的 SteamID，`auth_provider=steam_client_asserted`、`auth_level=unverified`，不能证明 Steam 账户所有权。

## 3. 端点总表

### 3.1 健康与客户端配置

| 方法 | 路径 | 鉴权 | 成功 | 说明 |
| --- | --- | --- | --- | --- |
| GET | `/health/live` | 无 | 200 | 进程存活，不检查依赖 |
| GET | `/health/ready` | 无 | 200/503 | PostgreSQL、Redis 等必需依赖是否可用 |
| GET | `/v1/client/config` | 无 | 200 | 协议版本、功能开关、STUN、可用 Relay 区域 |

客户端配置不会返回具体 Relay 地址。具体 endpoint 只在 connection 被调度后通过 WebSocket 事件下发。

### 3.2 登录和玩家

| 方法 | 路径 | 鉴权 | 请求 | 成功 |
| --- | --- | --- | --- | --- |
| POST | `/v1/auth/bind` | 无；按 IP、SteamID、Device ID 及组合维度限流 | 必需 `steam_id`, `persona_name`；可选 `device_id`, `invite_code` | 200 Player + Access/Refresh Token + `is_new_player` |
| POST | `/v1/auth/refresh` | 无 | `refresh_token` | 200 新 Access/Refresh Token；旧 Refresh Token 失效 |
| POST | `/v1/auth/logout` | Player | 无 | 200 当前 session 撤销 |
| GET | `/v1/users/me` | Player | 无 | 200 实时玩家状态和权限字段 |
| GET | `/v1/users/me/sessions` | Player | 无 | 200 当前玩家的有效会话列表 |
| DELETE | `/v1/users/me/sessions/{session_id}` | Player | Path session ID | 200 撤销属于当前玩家的指定会话 |
| POST | `/v1/users/me/sessions/revoke-others` | Player | 无 | 200 撤销除调用会话外的全部会话 |

Bind 示例：

```http
POST /v1/auth/bind HTTP/1.1
Content-Type: application/json

{
  "steam_id": "76561198000000000",
  "persona_name": "Player",
  "device_id": "v1|uu:b09bc26d38bb76d6|ds:a867d4d49d01c90a|cp:f846ffb743eca479",
  "invite_code": "TEST-ABCD-EFGH"
}
```

旧客户端可继续省略两个可选字段，或发送不透明的安装 ID。新客户端应发送 `v1|uu:<摘要>|ds:<摘要>|cp:<摘要>`；每个摘要必须恰好为 16 个小写十六进制字符，分别表示客户端现有的 SMBIOS UUID、磁盘序列号和 CPU ID 单向摘要，无法取得的因子可以省略。服务端也兼容当前未带版本的 `uu:...|ds:...|cp:...` 格式、任意因子顺序和大写十六进制，并统一规范化为 `v1`、小写及 `uu`、`ds`、`cp` 顺序。以 `v1|`、`uu:`、`ds:` 或 `cp:` 开头的值若存在格式错误、重复或未知因子，将返回 `400 INVALID_REQUEST`。

`device_id` 最长 128 字节，只允许可打印 ASCII；它仅用于限流和风险观察，不是可信身份，也不会绕过 SteamID 唯一约束。服务端不会直接保存三个提交值，而是分别生成带域隔离的 HMAC-SHA-256 摘要和组合摘要，再将内部指纹记录关联到会话、登录事件和风险事件；外部 API 不返回这些摘要。是否要求邀请码由服务端 `auth.invite_required` 配置决定。绑定超过任一维度限制时返回 `429 AUTH_BIND_RATE_LIMITED`，响应同时包含 `Retry-After` 与 `details.retry_after_seconds`。

不要把 Access/Refresh Token 写入 URL、日志或崩溃报告。Refresh Token 发生重放时，服务端会撤销整个 token family。

会话列表只返回 session ID、四字符设备展示后缀、创建/最近使用时间、脱敏 IP 和 `is_current`。结构化指纹的后缀来自不透明的内部记录 ID，不取自任何硬件因子；旧版不透明 Device ID 仍显示其后四位。API 不会返回 Token 哈希或完整设备标识。删除不属于当前玩家的 session 与不存在的 session 一样返回 404，避免跨账号探测。

### 3.3 Dedicated Server

| 方法 | 路径 | 鉴权 | 请求/查询 | 成功 |
| --- | --- | --- | --- | --- |
| POST | `/v1/game-servers` | Registration Token | `instance_id`, `display_name`, `region`, `mode`, `version`, `public_host`, `public_port`, `max_players` | 201 Server + 一次性 `server_token` |
| GET | `/v1/game-servers` | 无 | `region`, `mode`, `version`, `state`, `cursor`, `limit` | 200 公共目录 |
| GET | `/v1/game-servers/{server_id}` | 无 | — | 200 公共状态 |
| POST | `/v1/game-servers/{server_id}/heartbeat` | 对应 Server Token | `state`, `player_count` | 200 状态与下一次心跳信息 |
| DELETE | `/v1/game-servers/{server_id}` | 对应 Server Token | — | 200 注销并撤销 token |

`instance_id` 注册是幂等的。Server Token 只能管理响应中的 `server_id`。建议每 15 秒心跳；默认 45 秒转 `UNHEALTHY`，90 秒转 `OFFLINE`。公共响应不包含 token hash、内部审计字段或其他服务器秘密。

### 3.4 P2P 房间

| 方法 | 路径 | 鉴权 | 请求/附加 Header | 成功 |
| --- | --- | --- | --- | --- |
| GET | `/v1/p2p-rooms` | 无 | `region`, `mode`, `version`, `state`, `has_slots`, `cursor`, `limit` | 200 公共目录 |
| GET | `/v1/p2p-rooms/{room_id}` | 无 | — | 200 公共房间状态 |
| POST | `/v1/p2p-rooms` | Active Player | `display_name`, `region`, `mode`, `version`, `max_players` | 201 房间 + 一次性 `host_token` |
| POST | `/v1/p2p-rooms/{room_id}/join` | Active Player | `version` | 200 加入；重复调用幂等 |
| POST | `/v1/p2p-rooms/{room_id}/leave` | Active Player | — | 200 离开；重复调用幂等 |
| POST | `/v1/p2p-rooms/{room_id}/heartbeat` | Active Player + Host Token | `X-Room-Host-Token` | 200 心跳 |
| POST | `/v1/p2p-rooms/{room_id}/start` | Active Player + Host Token | `X-Room-Host-Token` | 200 `LOBBY -> CONNECTING` |
| DELETE | `/v1/p2p-rooms/{room_id}` | Active Player + Host Token | `X-Room-Host-Token` | 200 关闭；重复调用幂等 |

公共房间响应不会返回候选地址、Host Token 或成员秘密。房主不能调用 leave，必须关闭房间。默认 45 秒无房主心跳进入过期处理，90 秒关闭。有效的房主心跳还会在同一数据库事务中续租该房间的所有非终态连接；终态连接不会被恢复。

### 3.5 连接协调和 WebSocket

| 方法 | 路径 | 鉴权 | 请求 | 成功 |
| --- | --- | --- | --- | --- |
| POST | `/v1/connections` | Active Player | `room_id`, `peer_player_id` | 201 创建或返回现有活动连接 |
| GET | `/v1/connections/{connection_id}` | Player，必须是参与者 | — | 200 当前状态和对端候选 |
| DELETE | `/v1/connections/{connection_id}` | Active Player，必须是参与者 | — | 200 关闭 |
| GET/WSS | `/v1/realtime/connect` | Active Player | WebSocket Upgrade | 101 |

WebSocket 的 `Authorization` 必须放在 Header，禁止放在查询参数。JSON envelope：

```json
{
  "type": "connection.candidate",
  "connection_id": "conn_...",
  "payload": {}
}
```

客户端上行事件：

- `connection.candidate`：提交一个 ICE/NAT candidate。
- `connection.check_result`：报告直连检查结果和选中的路径。

服务端下行事件：

- `connection.candidate`
- `connection.check_result`
- `connection.relay_allocated`
- `connection.relay_migrating`
- `connection.relay_migrated`
- `connection.relay_failed`
- `error`

Relay 分配事件示例：

```json
{
  "type": "connection.relay_allocated",
  "connection_id": "conn_...",
  "payload": {
    "allocation_id": "alloc_...",
    "relay": {
      "node_id": "relay_...",
      "protocol": "UDP",
      "host": "203.0.113.20",
      "port": 8443
    },
    "relay_token": "...",
    "expires_at": "2026-07-18T12:05:00Z"
  }
}
```

事件具体字段和枚举见 OpenAPI 中 `Connection*Event`、`ConnectionData`、`RelayTokenClaims`。客户端应按 `connection_id` 幂等处理事件，断线后重新 GET 当前连接状态，不应因重连重复创建房间或连接。

### 3.6 更新

| 方法 | 路径 | 鉴权 | 查询 | 成功 |
| --- | --- | --- | --- | --- |
| GET | `/v1/updates/check` | 无 | 必需 `platform`, `current_version`；可选 `architecture`, `channel` | 200 最新版本、是否可用/强制 |
| GET | `/v1/updates/{platform}/{version}/manifest` | 无 | `architecture`, `channel` | 200 Ed25519 签名 Manifest |
| GET | `/v1/updates/files/{file_id}` | 无 | — | 200 immutable CDN 下载元数据 |

`channel` 为 `stable` 或 `beta`。Manifest 包含 `schema_version`、产品/平台/架构/频道/版本、最低支持版本、发布时间、文件列表、`manifest_hash`、`signature_algorithm=Ed25519`、`key_id` 和 `signature`。更新器必须：

1. 使用客户端内置且与 `key_id` 对应的公钥验证签名；
2. 验证 Manifest hash；
3. 从 CDN 下载；
4. 校验精确文件大小和 SHA-256；
5. 任一步失败都不得安装。

## 4. HTTP 状态与重试

| 状态 | 含义 | 客户端行为 |
| --- | --- | --- |
| 200/201 | 成功 | 正常处理 envelope |
| 400 | 格式/字段非法 | 修复请求，不要盲目重试 |
| 401 | Token 缺失、过期、撤销或不匹配 | refresh 或重新登录/注册 |
| 403 | 账户状态、参与关系或独立凭据拒绝 | 不重试，提示权限问题 |
| 404 | 不存在或该网络入口故意隐藏 | 校验 ID/入口 |
| 409 | 状态冲突、房间满、版本不匹配 | 刷新资源状态后决定 |
| 429 | 限流 | 使用指数退避和 jitter |
| 500 | 服务内部错误 | 携带 `request_id` 报告，有限重试 |
| 503 | 未就绪/依赖异常 | 指数退避，切勿高频轮询 |

只对幂等操作或带明确幂等语义的操作自动重试。推荐退避 250 ms、500 ms、1 s、2 s，上限 5–10 s，并添加随机抖动。

## 5. 契约文件

- 完整 OpenAPI：`Backend/api/openapi/openapi.yaml`
- 鉴权矩阵：`Backend/api/openapi/auth-permission-matrix.md`
- Relay UDP 协议：`Backend/api/relay-protocol.md`
- 内部 API：`docs/api/internal.md`
