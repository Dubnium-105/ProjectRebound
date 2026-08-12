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

除下文的诊断报告上传接口外，成功响应统一为：

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
| Game Server Registration Token | `Authorization: Bearer <token>` | 已验证玩家以既有资格或专服邀请码调用 `/v1/game-server-registration-tokens`，仅返回一次 | 仅注册对应的一台 Dedicated Server |
| Game Server Runtime Credential | Bearer Token + Ed25519 请求签名 | 注册或凭证轮转响应，仅返回一次 | 对应 Server 的心跳、轮转、注销与 MetaServer 内部调用 |
| Room Host Token | `X-Room-Host-Token: <token>` | 创建房间响应，仅返回一次 | 房主心跳、启动和关闭 |

`account_status=BANNED` 的玩家仍可 bind、refresh、logout 和读取本人资料，但不能执行房间或连接写操作。未提交 `encrypted_ticket` 的旧客户端仍可 bind，但只获得 `unverified` 会话；有效 Steam Encrypted App Ticket 会建立 `verified` 会话。游戏、房间、连接和 MetaServer 操作只允许 verified 会话。

## 3. 端点总表

### 3.1 健康与客户端配置

| 方法 | 路径 | 鉴权 | 成功 | 说明 |
| --- | --- | --- | --- | --- |
| GET | `/health/live` | 无 | 200 | 进程存活，不检查依赖 |
| GET | `/health/ready` | 无 | 200/503 | PostgreSQL、Redis 等必需依赖是否可用 |
| GET | `/v1/client/config` | 无 | 200 | 协议版本、功能开关、STUN、可用 Relay 区域 |

客户端配置不会返回具体 Relay 地址。具体 endpoint 只在 connection 被调度后通过 WebSocket 事件下发。

`features.vnt_rooms` 是服务端权威的 VNT 房间开关。值为 `false` 时，客户端必须隐藏新的 VNT create/rebind 操作，相应受保护 API 也会拒绝这些操作。受控关闭期间，已有 VNT 房间仍可执行 bootstrap、heartbeat、presence、start、leave 与 close，以便安全排空活动会话。

### 3.2 登录和玩家

| 方法 | 路径 | 鉴权 | 请求 | 成功 |
| --- | --- | --- | --- | --- |
| POST | `/v1/auth/bind` | 无；按 IP、SteamID、Device ID 及组合维度限流 | 必需 `steam_id`, `persona_name`；可选 `device_id`, `invite_code`, `encrypted_ticket` | 200 Player + Access/Refresh Token + 认证等级 + 首次完整性 nonce |
| POST | `/v1/auth/refresh` | 无 | `refresh_token` | 200 新 Access/Refresh Token；旧 Refresh Token 失效 |
| POST | `/v1/auth/logout` | Player | 无 | 200 当前 session 撤销 |
| GET | `/v1/users/me` | Player | 无 | 200 实时玩家状态和权限字段 |
| GET | `/v1/users/me/sessions` | Player | 无 | 200 当前玩家的有效会话列表 |
| DELETE | `/v1/users/me/sessions/{session_id}` | Player | Path session ID | 200 撤销属于当前玩家的指定会话 |
| POST | `/v1/users/me/sessions/revoke-others` | Player | 无 | 200 撤销除调用会话外的全部会话 |
| POST | `/v1/integrity/challenge` | Player | 无 | 200 返回新的一次性 `nonce`；trusted 会话不需要 ticket，空 nonce 表示未 trusted 会话必须重新 bind |
| POST | `/v1/integrity/proof` | Player | `nonce`、`proof`、`component=toolbox` | 200 返回 `ok`；成功持久化 session 完整性信任，任何失败都会立即清除信任 |
| POST | `/v1/integrity/verify` | Player | 与 `/proof` 相同 | 已弃用的兼容别名 |
| POST | `/v1/diagnostic/report` | Player | 必需的诊断文本字段 `report` | 文本存储后返回 200 裸响应 `{"ok":true}` |

Bind 示例：

```http
POST /v1/auth/bind HTTP/1.1
Content-Type: application/json

{
  "steam_id": "76561198000000000",
  "persona_name": "Player",
  "device_id": "hardware-uuid|disk-serial|cpu-id",
  "encrypted_ticket": "0123456789abcdef",
  "invite_code": "TEST-ABCD-EFGH"
}
```

旧客户端可继续省略可选字段或发送不透明安装 ID，但会话保持 `unverified`。新客户端提交十六进制 `encrypted_ticket`。后端只通过 stdin 将其传给外部验证程序，并以解密出的 SteamID 为权威身份。只要解密成功且 ticket SteamID 与请求 SteamID 一致就接受；AppID、签发时间、VAC 状态和此前是否使用过均不再阻断 bind。系统绝不保存 ticket 明文。

verified bind 的 `data.integrity_challenge.nonce` 包含首次一次性 challenge。客户端计算 `SHA256(PE_certificate_bytes || decoded_encrypted_ticket_bytes || nonce_ascii)`，并把 64 字符十六进制摘要提交到 `/v1/integrity/proof`。成功后 session 持久化 `integrity_trusted=true` 和 `SHA256(PE_certificate_bytes)`，refresh 原样继承两者；之后的 challenge（包括控制服重启后）统一使用不查询 ticket 的 `SHA256(PE_certificate_bytes || nonce_ascii)`。每次 challenge 都会替换前一个 nonce；任何 proof 失败都会立即清除完整性信任，连续三次失败则撤销会话。ticket 原始字节只在首次 bind proof 完成或过期前存在于进程内存；未 trusted 会话收到空 nonce 时必须重新 bind。

新客户端可以发送 `uuid|disk|cpu`，服务端分别对三个因子执行 HMAC；也继续接受 `v1|uu:<摘要>|ds:<摘要>|cp:<摘要>`，其中摘要为 16 个十六进制字符，版本化格式允许省略无法取得的因子。无竖线的旧版不透明 Device ID 仍然兼容。

`device_id` 最长 128 字节，只允许可打印 ASCII；它仅用于限流和风险观察，不是可信身份，也不会绕过 SteamID 唯一约束。服务端不会直接保存三个提交值，而是分别生成带域隔离的 HMAC-SHA-256 摘要和组合摘要，再将内部指纹记录关联到会话、登录事件和风险事件；外部 API 不返回这些摘要。是否要求邀请码创建账户由 `auth.invite_required` 决定；新老玩家在 bind 时携带的邀请码都会按权限快照给该玩家授予 `p2p_room_registration`、`game_server_registration`、`vnt_node_registration` 中对应的独立权限，权限截止时间取消费当时的邀请码 `expires_at`；无截止时间的邀请码授予永久权限。bind、refresh 和 `/v1/users/me` 只返回当前未到期的 `capabilities`。绑定超过任一维度限制时返回 `429 AUTH_BIND_RATE_LIMITED`，响应同时包含 `Retry-After` 与 `details.retry_after_seconds`。

不要把 Access/Refresh Token 写入 URL、日志或崩溃报告。Refresh Token 发生重放时，服务端会撤销整个 token family。

诊断接口使用 Access Token 中的 `player_id` 关联报告，并原样存储提交的字符串；服务端不解析、不验证、不索引报告内容。其成功响应使用裸 `{"ok":true}`，这是对统一成功 envelope 的有意兼容例外；错误仍使用统一错误 envelope。

会话列表只返回 session ID、四字符设备展示后缀、创建/最近使用时间、脱敏 IP 和 `is_current`。结构化指纹的后缀来自不透明的内部记录 ID，不取自任何硬件因子；旧版不透明 Device ID 仍显示其后四位。API 不会返回 Token 哈希或完整设备标识。删除不属于当前玩家的 session 与不存在的 session 一样返回 404，避免跨账号探测。

### 3.3 Dedicated Server

| 方法 | 路径 | 鉴权 | 请求/查询 | 成功 |
| --- | --- | --- | --- | --- |
| POST | `/v1/game-server-registration-tokens` | 已验证 Player Access Token | `instance_id`；尚无资格时同时提交 `invite_code` | 201 单次 `registration_token` |
| POST | `/v1/game-servers` | Registration Token | 节点信息 + 节点本地生成 Ed25519 密钥对应的 `csr_pem` | 201 Server + 一次性 Token + 节点证书/CA |
| GET | `/v1/game-servers` | 无 | `region`, `mode`, `version`, `state`, `cursor`, `limit` | 200 公共目录 |
| GET | `/v1/game-servers/{server_id}` | 无 | — | 200 公共状态 |
| POST | `/v1/game-servers/{server_id}/heartbeat` | Token + 当前/重叠证书私钥签名 | `state`, `player_count` | 200 状态与下一次心跳信息 |
| POST | `/v1/game-servers/{server_id}/credential/rotate` | 当前 Token + 当前私钥签名 | 新密钥对应的 `csr_pem` | 200 新 Token、证书、代数和重叠截止时间 |
| DELETE | `/v1/game-servers/{server_id}` | 当前 Token + 当前私钥签名 | — | 200 注销并撤销凭证 |

专服邀请码的不可变权限快照必须包含 `allow_game_server_registration: true`。新玩家或已有玩家都可以在 Steam bind 时消费；签发 Registration Token 时直接兑换邀请码的旧流程仍兼容。消费后授予的 `game_server_registration` 与消费当时的邀请码在同一时间到期；之后修改邀请码不会追溯改变已记录的截止时间或扩大权限。再次兑换合格邀请码只会延长、不会缩短权限，永久权限优先。Registration Token 有效 10 分钟且绑定一个 `instance_id`。注册会原子消费它并将实例绑定到玩家；其他玩家不能抢占同一实例 ID。

使用已验证玩家的 Access Token 请求一次性 Registration Token。玩家已有有效权限时省略 `invite_code`；否则提交一枚属于该玩家且具备对应权限的邀请码：

```bash
curl --fail-with-body -X POST 'https://api.project-rebound.space/v1/game-server-registration-tokens' \
  -H "Authorization: Bearer $PLAYER_ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"instance_id":"hk-dedicated-01","invite_code":"REPLACE_IF_NEEDED"}'
```

响应中的 `data.registration_token` 是 `gsr_...` 秘密，只能在 `POST /v1/game-servers` 中以 `Authorization: GameServerRegistration <token>` 使用一次。它不是服务器的运行凭据；注册成功后不得继续记录或保存。

节点必须自行生成并仅在本机保存 Ed25519 私钥，注册提交由该私钥签名的 PKCS#10 CSR。后端签发 24 小时节点证书，身份 URI 为 `spiffe://projectrebound/game-server/{server_id}`；后端只保存公钥和证书指纹，不接收或生成节点私钥。

所有运行写请求必须同时发送 Bearer Token、`X-Game-Server-Certificate`、`X-Game-Server-Timestamp`、`X-Game-Server-Nonce`、`X-Game-Server-Generation` 和 `X-Game-Server-Signature`。签名为 Ed25519 对以下以换行连接的规范串签名：`PR-GAME-SERVER-V1`、大写 HTTP 方法、原始 path+query、正文 SHA-256 十六进制、Unix 时间戳、base64url nonce、Server ID、凭证代数、Token SHA-256 十六进制。时间允许偏差 60 秒；nonce 解码后为 16–64 字节且由 PostgreSQL 全局防重放。

Token 与证书默认均有效 24 小时。轮转请求由当前私钥签名并携带新 CSR，服务端原子替换 Token 与证书；旧凭证对仅可在 60 秒重叠期内继续普通运行流量，不能再次轮转或注销节点。Control Plane 与 MetaServer 共用相同验证器和 nonce 表。明文 Token 只返回一次并带 `Cache-Control: no-store`。现网无证书旧节点仅有迁移落库后 24 小时兼容窗口。`cmd/game-server-agent` 下的 Go 命令仅为协议参考和开发诊断客户端，不属于生产启动链路。

生产注册、签名心跳、私钥和凭证轮转由 Rust Toolbox 负责。Toolbox 为每次启动生成高熵命名管道名，通过 Windows Wrapper 传递 `-pipe=<name>`，并且只从 Payload 读取非秘密的 `state`、`player_count` 和 `round_state`。`serverconfig.json` 配置如下：

```json
{
  "backend": "https://api.project-rebound.space",
  "offline": false,
  "registrationToken": "gsr_替换为一次性注册凭证",
  "serverId": "hk-dedicated-01",
  "publicHost": "替换为公网IP",
  "maxPlayers": 10
}
```

`serverId` 是 Registration Token 绑定的 `instance_id`，不是后端生成的 Server ID。注册成功后，Toolbox 保存当前用户 DPAPI 加密的 `game-server-identity-<serverId>.dpapi`，并从配置中删除 `registrationToken`。Wrapper 和 Payload 不会接触 Registration Token、节点私钥、运行 Token、CSR 或签名。

完整的签发、命名管道、回退、DPAPI、轮转和生产 CA 流程见[Dedicated Server 注册与运行身份](../operations/dedicated-server-registration.zh-CN.md)。

### 3.4 P2P 房间

| 方法 | 路径 | 鉴权 | 请求/附加 Header | 成功 |
| --- | --- | --- | --- | --- |
| GET | `/v1/p2p-rooms` | 无 | `region`, `mode`, `version`, `state`, `has_slots`, `cursor`, `limit` | 200 公共目录 |
| GET | `/v1/p2p-rooms/{room_id}` | 无 | — | 200 公共房间状态 |
| POST | `/v1/p2p-rooms` | Active Player | `display_name`, `region`, `mode`, `version`, `max_players`；可选 `transport_kind`, `vnt_node_id` | 201 房间 + 一次性 `host_token` |
| POST | `/v1/p2p-rooms/{room_id}/join` | Active Player | `version` | 200 加入；重复调用幂等 |
| POST | `/v1/p2p-rooms/{room_id}/leave` | Active Player | — | 200 离开；重复调用幂等 |
| POST | `/v1/p2p-rooms/{room_id}/heartbeat` | Active Player + Host Token | `X-Room-Host-Token` | 200 心跳 |
| POST | `/v1/p2p-rooms/{room_id}/start` | Active Player + Host Token | `X-Room-Host-Token` | 200 `LOBBY -> CONNECTING` |
| DELETE | `/v1/p2p-rooms/{room_id}` | Active Player + Host Token | `X-Room-Host-Token` | 200 关闭；重复调用幂等 |

公共房间响应不会返回候选地址、Host Token 或成员秘密。房主不能调用 leave，必须关闭房间。默认 45 秒无房主心跳进入过期处理，90 秒关闭。有效的房主心跳还会在同一数据库事务中续租该房间的所有非终态连接；终态连接不会被恢复。

### 3.5 P2P BattleLog v3

全部接口都要求 Active、Steam 已验证的 Player Access Token，并要求玩家属于服务端冻结的对局名单。P2P 证据与专用服务器的 `battlelog_*` 数据完全分开保存。

| 方法 | 路径 | 附加鉴权/请求体 | 成功响应 |
| --- | --- | --- | --- |
| GET | `/v1/p2p-rooms/{room_id}/matches/active` | — | 200 服务端创建的对局上下文 |
| POST | `/v1/p2p-matches/{match_id}/report-capability` | — | 201 与会话族绑定的 `report_token`、Capability ID 和 nonce |
| PUT | `/v1/p2p-matches/{match_id}/presence/me` | 单调递增的在线序号及进程/连接状态 | 200 在线或重连分段 |
| PUT | `/v1/p2p-matches/{match_id}/reports/{report_id}` | `X-P2P-Report-Token`；请求体直接为 raw v3 JSON | 200 接收、隔离或幂等重复 |
| GET | `/v1/p2p-matches/{match_id}/result` | — | 200 收集进度或最终裁决 |

Launcher 必须自行保管报告 Token，只在上传时添加；注入 DLL 只能取得非秘密的 Match ID、Capability ID 与 server nonce。每位上报者只能提交一份不可变 `FINAL`。房主或玩家提前离开不会无限阻塞：首份 FINAL、全体上报者进入结果页/离开，或房间关闭都会开启收集截止窗口；窗口结束后将对局记为交叉确认、自报、争议、不完整或过期。`PARTIAL` 仅保留为证据，不计入最终法定人数。

### 3.6 连接协调和 WebSocket

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

`channel` 为 `stable`、`beta` 或 `toolbox`。`beta` 发布完整游戏包 `Release.zip`；`toolbox` 仅发布 `Rebound_Toolbox.exe`。Manifest 包含 `schema_version`、产品/平台/架构/频道/版本、最低支持版本、发布时间、文件列表、`manifest_hash`、`signature_algorithm=Ed25519`、`key_id` 和 `signature`。更新器必须：

1. 使用客户端内置且与 `key_id` 对应的公钥验证签名；
2. 验证 Manifest hash；
3. 从 CDN 下载；
4. 校验精确文件大小和 SHA-256；
5. 任一步失败都不得安装。

### 3.7 社区 VNT 节点与房间

VNT 节点注册由玩家的 `vnt_node_registration` 独立权限控制。创建 VNT 或 Legacy P2P 房间都要求 `p2p_room_registration`；Dedicated Server 注册则单独要求 `game_server_registration`。

| 方法 | 路径 | 鉴权 | 用途 |
| --- | --- | --- | --- |
| GET | `/v1/users/me/vnt-nodes` | ACTIVE、Steam verified 的节点所有者玩家 | 只列出调用者自己的节点及安全的生命周期/凭据到期信息；绝不返回凭据或哈希 |
| POST | `/v1/vnt/node-enrollments` | ACTIVE、Steam verified、integrity trusted 玩家 | 签发十分钟、单次使用的节点 enrollment code |
| POST | `/v1/vnt/nodes` | `VNTEnrollment` | 消费 enrollment code，Node Credential 仅返回一次 |
| GET | `/v1/vnt/nodes` | 无 | 返回公开节点 endpoint、版本、位置和剩余容量 |
| POST | `/v1/vnt/nodes/{node_id}/heartbeat` | VNT Node | 续租并提交健康遥测 |
| POST | `/v1/vnt/nodes/{node_id}/credential/rotate` | 当前 VNT Node Credential | 新凭据仅返回一次，同时返回到期时间和旧 Token 心跳重叠截止时间 |
| POST | `/v1/vnt/nodes/{node_id}/recover` | 所有者新签发的 `VNTEnrollment` Code | 重新认领未终止节点、撤销全部旧凭据并只返回一次替换凭据 |
| DELETE | `/v1/vnt/nodes/{node_id}` | VNT Node 或完成完整性验证的所有者玩家 | 进入 DRAINING 或 RETIRED |
| POST | `/v1/p2p-rooms/{room_id}/vnt/bootstrap` | 活动成员 | 返回当前 generation 的 VNT 运行秘密；响应为 `no-store` |
| PUT | `/v1/p2p-rooms/{room_id}/vnt/presence/me` | 活动成员 | 上报本地隧道状态和实际路径 |
| PUT | `/v1/p2p-rooms/{room_id}/vnt/host-ready` | 房主 + Host Token | 发布当前 generation 的房主就绪状态 |
| POST | `/v1/p2p-rooms/{room_id}/vnt/rebind` | 房主 + Host Token | 开局前换节点，并轮换 generation 和全部房间秘密 |

签发 Enrollment 还要求 integrity-trusted 会话，且默认每位玩家最多拥有 3 个非 `RETIRED` 节点。`GET /v1/users/me/vnt-nodes` 是只读接口，不要求完整性 Step-up，用于本地状态丢失后找回稳定 `node_id`；Recover 和 owner retirement 仍要求 Step-up。恢复 Code 必须属于原 owner，恢复会立即撤销全部旧 Node Credential；仍有活动房间时不能改变 endpoint/fingerprint。

VNT 数据面不经过 Control Plane。公共节点和房间响应不会包含 network token、E2E 密码、Node Credential、device ID 或成员虚拟地址。

`GET /v1/vnt/nodes` 使用基于 ID 的游标分页。将响应中的 `data.next_cursor` 作为下一次请求的 `cursor`；空值表示没有下一页。`limit` 范围为 `1..200`，默认值为 `100`。

每个节点都包含 `version_compatible`，它由服务端对当前已发布 ToolBox 客户端版本中经过校验的 sidecar 所提取出的 VNT 运行时版本对执行精确且区分大小写的匹配。发布或回滚 ToolBox 版本会立即更新节点资格；若没有已发布版本携带有效运行时元数据，策略将 fail closed。公开更新 Manifest 及其签名结构保持不变，以兼容旧客户端。ToolBox 应隐藏不兼容节点，但仍必须处理 `409 VNT_NODE_UNAVAILABLE`，因为 create/rebind 会在分配事务内再次检查状态、容量和两个版本。

有活动房间的节点在请求退役后进入 `DRAINING`，并可继续发送心跳及轮换凭据，直到相关房间结束。节点一旦进入 `RETIRED`，其余节点凭据会被原子撤销。

系统不存在一个公开的“VNT Token”接口。节点注册与房间 Bootstrap 面向不同持有者，返回的秘密也不同：

1. 具备有效 `vnt_node_registration`、当前为 ACTIVE/Steam verified/integrity trusted 且仍有 owner 配额的玩家先请求有效 10 分钟的 Enrollment Code：

   ```bash
   curl --fail-with-body -X POST 'https://api.project-rebound.space/v1/vnt/node-enrollments' \
     -H "Authorization: Bearer $PLAYER_ACCESS_TOKEN" \
     -H 'Content-Type: application/json' \
     --data '{"label":"hk-community-node-01"}'
   ```

2. VNT-Node 进程一次性消费 `data.enrollment_code`，响应返回 `data.node_id` 和只显示一次的 `data.node_token`。之后的 Heartbeat、Rotation 和 Retirement 使用 `Authorization: Bearer <node_token>`：

   ```bash
   curl --fail-with-body -X POST 'https://api.project-rebound.space/v1/vnt/nodes' \
     -H "Authorization: VNTEnrollment $VNT_ENROLLMENT_CODE" \
     -H 'Content-Type: application/json' \
     --data '{"advertised_host":"203.0.113.20","port":29878,"region":"hk","location":"Hong Kong","vnts_version":"REPLACE_PINNED_VERSION","wrapper_version":"REPLACE_WRAPPER_VERSION","server_key_fingerprint":"REPLACE_SHA256_FINGERPRINT","supported_transports":["udp","tcp"],"max_rooms":100}'
   ```

3. VNT 房间的 `network_token` 与 `e2e_password` 不会发给节点所有者，也不会出现在公共目录。只有 VNT 房间的活动成员可通过 `POST /v1/p2p-rooms/{room_id}/vnt/bootstrap` 获取当前 generation；客户端必须按 `Cache-Control: no-store` 处理并只保存在内存中。

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

## MetaServer 路由索引

MetaServer 复用现有玩家 Access Token 和统一错误 envelope。字段级契约与
MetaTunnel 流程见 [MetaServer 外部 API](metaserver-external.zh-CN.md)。

| 方法 | 路径 | 鉴权 | 用途 |
| --- | --- | --- | --- |
| POST | `/connectServer` | 活跃玩家 | 兼容 MetaTunnel 的 Gate 引导 |
| GET | `/v1/meta/regions` | 无 | 动态发现 READY Relay/QoS |
| GET | `/v1/meta/playlists` | 无 | 已启用匹配列表 |
| GET | `/v1/meta/notifications` | 无 | 活跃本地化通知 |
| POST | `/v1/meta/sessions` | 活跃玩家 | 签发 60 秒单次 Gate Ticket |
| GET | `/v1/users/me/meta-profile` | 活跃玩家 | 当前 Meta 档案 |
| GET | `/v1/users/me/loadouts` | 活跃玩家 | 全部角色配装 |
| GET | `/v1/users/me/loadouts/{role_id}` | 活跃玩家 | 单个角色配装 |
| PUT | `/v1/users/me/loadouts/{role_id}` | 活跃玩家 | 定义校验和乐观锁更新 |
| POST | `/v1/meta/parties` | 活跃玩家 | 创建 Party |
| GET | `/v1/meta/parties/{party_id}` | Party 成员 | 查询 Party |
| POST | `/v1/meta/parties/{party_id}/ready` | Party 成员 | 更新准备状态 |
| POST | `/v1/meta/parties/{party_id}/presence` | Party 成员 | 更新在线状态 |
| POST | `/v1/meta/matchmaking/tickets` | 玩家/队长 | 单人或整队排队 |
| GET | `/v1/meta/matchmaking/tickets/{ticket_id}` | Ticket 所有者/成员 | 轮询分配 |
| DELETE | `/v1/meta/matchmaking/tickets/{ticket_id}` | Ticket 所有者/队长 | 取消排队 |

## 6. 公共下载目录

公共下载目录与启动器使用的签名更新 Manifest 相互独立。

| 方法 | 路径 | 鉴权 | 用途 |
| --- | --- | --- | --- |
| GET | `/v1/downloads` | 无 | 返回启用分类、下载项目和全部已发布版本，支持 `ETag`/`If-None-Match` |
| GET | `/v1/downloads/files/{version_id}` | 无 | 确认版本仍处于发布状态，再跳转至不可变 CDN 对象 |

目录包含 `zh_cn`、`en` 双语文本、`latest_version_id`、文件大小、SHA-256、
发布时间和稳定 API 下载路径。草稿、失败和已归档版本在文件入口统一表现为未知 ID。
