# ProjectRebound 内部、管理与 Relay API

[English](internal.md) | 简体中文

本文档描述不面向普通游戏客户端的接口：管理员 HTTP API、Relay 注册/续签、mTLS gRPC 控制流、Prometheus 指标和 UDP Relay 数据面。完整 JSON Schema 位于 `Backend/api/openapi/openapi.yaml`，gRPC service 位于 `Backend/api/proto/relay_control.proto`。

## 1. 网络入口和信任边界

| 接口 | 默认地址 | 调用方 | 防护 |
| --- | --- | --- | --- |
| 管理 HTTP | `127.0.0.1:18080/v1/admin/*` | 独立 Admin Web | Turnstile + 管理员密码 + TOTP + 短期 Admin Access Token + trusted CIDR |
| 内部 Relay 管理 HTTP | `127.0.0.1:18080/internal/v1/relay-nodes/*` | 运维后台 | Admin Token + trusted CIDR |
| Relay 注册/续签 HTTP | 公网 HTTPS `/internal/v1/relay-nodes/enroll`、`.../certificate/renew` | Edge Relay | 一次性 Bootstrap Token 或 Node Token |
| 控制面指标 | `127.0.0.1:18080/internal/metrics` | Prometheus | trusted CIDR；公网代理返回 404 |
| Relay 控制流 | 控制面 TCP 9090 | Edge Relay | TLS 1.3 双向证书认证 |
| Relay 数据面 | Edge UDP 8443 | 游戏端点 | 签名 Relay Token + Cookie Challenge + 数据包 HMAC |
| Relay 指标 | Edge `127.0.0.1:9100/metrics` | 节点本地 agent | 仅回环监听 |

公网 Caddy 必须对 `/v1/admin*` 和 `/internal/*` 返回 404，只允许 Relay enroll 和 certificate renew 两条机器接口。源 IP 白名单不能替代 Token，Token 也不能替代网络隔离。

## 2. 管理员认证

浏览器管理员认证与玩家认证完全隔离。登录流程为：

1. `GET /v1/admin/auth/config` 获取公开的 Turnstile Sitekey 和 action；
2. `POST /v1/admin/auth/login` 提交用户名、密码和 `turnstile_token`；
3. 控制面调用 Cloudflare Siteverify，并校验 `success`、预期 hostname 和 `admin_login` action；
4. 密码通过后返回五分钟有效的一次性 MFA challenge；
5. `POST /v1/admin/auth/mfa/verify` 提交 TOTP 或恢复码；
6. 成功后返回短期 Admin Access Token，并设置 `HttpOnly; SameSite=Strict` Refresh Cookie；
7. `POST /v1/admin/auth/refresh` 轮换 Refresh Cookie，重复使用旧 Token 会撤销会话。
8. 高风险操作通过 `POST /v1/admin/auth/step-up` 重新校验 TOTP 或恢复码，取得绑定当前 Session 的短时证明。

已登录管理员使用：

```text
POST   /v1/admin/auth/logout
GET    /v1/admin/auth/me
GET    /v1/admin/auth/sessions
DELETE /v1/admin/auth/sessions/{session_id}
POST   /v1/admin/auth/step-up
```

玩家 Access Token 和机器 Admin Token 均不能代替 Admin Web 的 Access Token。权限由服务端 RBAC 强制执行，前端菜单隐藏不构成安全边界。Relay 撤销还必须在 `X-Admin-Step-Up` 中携带二次 MFA 证明；证明只保存在浏览器内存并按配置的短时 TTL 过期。

首次管理员通过 `go run ./cmd/adminctl` 创建。密码只从 `ADMINCTL_PASSWORD` 环境变量读取；TOTP provisioning URI 和恢复码只在创建成功时显示一次。持久环境必须预先配置 `ADMIN_MFA_ENCRYPTION_KEY_BASE64`。

## 2.1 机器 Admin Token

静态 Admin Token 仅保留给内部 Relay 运维和自动化接口，不再接受于 `/v1/admin/*` 的人类管理 API。控制面通过环境变量读取：

```text
ADMIN_TOKENS=operator=<high-entropy-token>;automation=<another-token>
ADMIN_TRUSTED_CIDRS=127.0.0.0/8,10.0.0.0/8,...
```

请求：

```http
Authorization: Bearer <admin-token>
```

玩家 Access Token 不可用于任何管理接口。启用 `TRUST_PROXY_HEADERS=true` 时只能把控制面放在受信反向代理之后，禁止让客户端绕过代理直连；否则伪造的转发 Header 可能影响源地址判断。

## 3. 玩家管理 API

| 方法 | 路径 | 参数/请求 | 成功 |
| --- | --- | --- | --- |
| GET | `/v1/admin/players` | `cursor`, `limit`, `account_status` | 玩家分页列表 |
| GET | `/v1/admin/players/{player_id}` | Path ID | 完整管理记录 |
| GET | `/v1/admin/players/{player_id}/sessions` | Path ID | 带设备与 IP 摘要的近期 Session |
| GET | `/v1/admin/players/{player_id}/risk-events` | Path ID | 已脱敏的近期风险事件 |
| GET | `/v1/admin/players/{player_id}/login-events` | Path ID | 近期认证结果 |
| PATCH | `/v1/admin/players/{player_id}` | `reason`，以及至少一个 `account_status`、`is_vip`、`revoke_sessions`；可选 `internal_note` | 更新后的玩家、撤销 session 数 |
| POST | `/v1/admin/players/{player_id}/revoke-sessions` | `reason` | 撤销数量和时间 |

Patch 示例：

```http
PATCH /v1/admin/players/player_123 HTTP/1.1
Authorization: Bearer <admin-access-token>
Content-Type: application/json

{
  "account_status": "BANNED",
  "is_vip": false,
  "revoke_sessions": true,
  "reason": "客服工单 CS-4812 已确认存在退款滥用",
  "internal_note": "值班运营负责人已复核"
}
```

`account_status` 为 `ACTIVE`、`BANNED` 或 `DELETED`。所有写操作都必须填写人类可读原因，并在审计记录中保存原因、请求编号、管理员、来源地址、User-Agent、修改前后内容和结果。需要立即使现有登录失效时设置 `revoke_sessions=true`，或调用独立的 revoke-sessions 端点。原因和备注中禁止放入 Token、密码、Cookie、隐私数据或游戏 Payload。

### 3.1 邀请码管理

| 方法 | 路径 | 参数/请求 | 成功 |
| --- | --- | --- | --- |
| POST | `/v1/admin/invite-codes` | `batch_name`, `max_uses`, `reason`；可选 `quantity`（1–100）、`expires_at`, `permissions` | 原子创建一批元数据并仅本次返回明文邀请码 |
| GET | `/v1/admin/invite-codes` | `cursor`, `limit` | 元数据分页列表，不返回明文或哈希 |
| GET | `/v1/admin/invite-codes/{id}` | Path ID | 单条元数据，不返回明文或哈希 |
| GET | `/v1/admin/invite-codes/{id}/uses` | `cursor`, `limit` | 成功使用记录，IP 仅返回掩码后的网段摘要 |
| PATCH | `/v1/admin/invite-codes/{id}` | `reason` 和 `batch_name`, `max_uses`, `expires_at`, `enabled`, `permissions` 中至少一个 | 更新后的元数据 |
| POST | `/v1/admin/invite-codes/{id}/revoke` | `reason` | 幂等禁用并记录撤销时间 |

创建响应中的明文邀请码是秘密，只出现一次；数据库仅存 SHA-256 哈希。`max_uses` 不得降低到 `used_count` 以下。每次成功兑换都使用行级锁保护名额，因此并发争抢最后一个名额只会有一个事务成功。新玩家和已有玩家只要在 Steam bind 中提交邀请码，都会消费一次；已有玩家不提交 `invite_code` 时不会消费。

每条成功使用记录都会保存一份不可变的 `permissions` 快照。Admin Web 创建批次时提供以下相互独立的选项：

| 权限字段 | 作用 |
| --- | --- |
| `allow_create_account` | 允许邀请码满足新玩家的强制邀请码校验 |
| `allow_p2p_room_registration` | 授予 `p2p_room_registration` |
| `allow_game_server_registration` | 授予 `game_server_registration` |
| `allow_vnt_node_registration` | 授予 `vnt_node_registration` |

权限截止时间在兑换时复制自邀请码的 `expires_at`；邀请码无到期时间时授予永久权限。bind、refresh 和 `/v1/users/me` 的能力列表会过滤已到期权限，对应鉴权也会失败。之后编辑或撤销邀请码只影响未来兑换，不会撤回或扩大已有授权。玩家再次兑换合格邀请码时，只会延长而不会缩短期限；永久授权优先。

### 3.2 Dashboard、风险事件与审计

Dashboard 使用 `GET /v1/admin/dashboard/summary`、`GET /v1/admin/dashboard/timeseries` 和 `GET /v1/admin/dashboard/alerts`。趋势周期只允许 `1h`、`24h`、`7d`、`30d`，前端不能提交任意 SQL 分组表达式。

使用 `GET /v1/admin/risk-events` 和 `GET /v1/admin/risk-events/{event_id}` 查询认证风险记录。`POST /v1/admin/risk-events/{event_id}/resolve` 必须提交 `reason`，并记录处理管理员和时间。响应不包含 Device ID 哈希、内部设备指纹 ID 或任何单因子 HMAC 摘要；IP 会脱敏，详情中类似凭据的字段也会递归脱敏。当前没有按原始因子查询或封禁设备的外部/管理员 API。将来的封禁流程只能提供经过鉴权的服务端匹配操作，绝不能返回已存摘要。

写操作审计通过 `GET /v1/admin/audit-logs` 和 `GET /v1/admin/audit-logs/{audit_id}` 查询；管理员登录及 Turnstile 诊断通过 `GET /v1/admin/login-audit` 查询。登录审计只包含校验结果、错误码、hostname、action 和延迟，绝不记录 Turnstile Token、Secret、密码、Cookie 或 Authorization Header。

### 3.3 联机运营

人类管理员使用以下基于 Session 的接口：

```text
GET  /v1/admin/p2p-rooms
GET  /v1/admin/p2p-rooms/{room_id}
GET  /v1/admin/p2p-rooms/{room_id}/members
POST /v1/admin/p2p-rooms/{room_id}/close
POST /v1/admin/p2p-rooms/{room_id}/members/{player_id}/remove

GET  /v1/admin/p2p-battlelog/matches/{match_id}
GET  /v1/admin/p2p-battlelog/reports/{evidence_id}/raw

GET  /v1/admin/game-servers
POST /v1/admin/game-servers/registration-tokens
GET  /v1/admin/game-servers/{server_id}
POST /v1/admin/game-servers/{server_id}/drain
POST /v1/admin/game-servers/{server_id}/resume
POST /v1/admin/game-servers/{server_id}/disable

GET  /v1/admin/connections
GET  /v1/admin/connections/{connection_id}
POST /v1/admin/connections/{connection_id}/close
POST /v1/admin/connections/{connection_id}/migrate-relay

GET  /v1/admin/relay-nodes
GET  /v1/admin/relay-nodes/{node_id}
POST /v1/admin/relay-nodes/{node_id}/drain
POST /v1/admin/relay-nodes/{node_id}/resume
POST /v1/admin/relay-nodes/{node_id}/revoke
```

读取 P2P BattleLog 标准化证据要求 `p2p.battlelog.read`；独立的原始证据接口要求 `p2p.battlelog.raw.read`，响应强制 `Cache-Control: no-store`，普通运维/客服角色不获得该权限。其报告标识和数据表与专用服务器 BattleLog 完全分离。

所有写操作都必须提交 `reason`；Relay Drain 还可提交 `deadline_seconds` 和 `migrate_existing`。`POST /v1/admin/game-servers/registration-tokens` 还要求 `game_servers.register` 权限和 MFA Step-up；请求包含 `instance_id` 与 1–168 小时有效期，会撤销该实例之前尚未消费的凭据，只保存 SHA-256 哈希，并仅在带 `Cache-Control: no-store` 的创建响应中返回一次明文 `gsr_...` Token。停用专服会将其标记为离线并撤销 Server Token。房间操作返回 `connections_cleanup_complete`；若为 false，说明房间变更已成功，但需按 Runbook 确认下游连接清理。Connection 的 Relay 迁移不接受浏览器提交目标地址或节点，目标由后端调度器从合格的 READY 节点中选择。其他响应绝不包含房主 Token、节点 Token、Allocation Token、注册 Token 哈希、私钥或完整 ICE Candidate。

### 3.4 客户端发布管理

```text
GET  /v1/admin/releases
POST /v1/admin/releases
GET  /v1/admin/releases/{release_id}
POST /v1/admin/releases/{release_id}/validate
POST /v1/admin/releases/{release_id}/publish
POST /v1/admin/releases/{release_id}/rollback
POST /v1/admin/releases/{release_id}/archive
```

创建请求包含平台、架构、stable/beta/toolbox 渠道、语义化版本、强制更新策略和对象存储文件描述。校验会检查文件路径、大小、SHA-256、压缩方式、CDN Object Key 与实际 `HEAD` 可用性、兼容版本顺序以及生成的 Ed25519 签名。只有 `READY` 版本可发布；正式发布、回滚和归档都必须填写原因，并由后端强制执行 MFA Step-up。公开更新目录只读取 `PUBLISHED` 的管理版本；回滚会让该版本退出后续更新检查，但不会删除审计历史。归档只接受 `DRAFT`、`READY` 或 `ROLLED_BACK`，沿用 `updates.rollback` 权限并保留全部记录。

### 3.5 管理员与角色治理

```text
GET   /v1/admin/admins
POST  /v1/admin/admins
PATCH /v1/admin/admins/{admin_id}
POST  /v1/admin/admins/{admin_id}/reset-mfa
GET   /v1/admin/roles
PATCH /v1/admin/roles/{role_id}
```

管理员账号与玩家身份完全隔离。创建管理员时必须分配至少一个已有角色，TOTP 配置 URI 和十个恢复码只在成功响应中返回一次。更新接口可修改显示名、启用状态、角色并撤销 Session；重置 MFA 会轮换加密保存的 TOTP Secret、替换全部恢复码哈希，并撤销该管理员的全部 Session。

所有治理写操作都必须提交人类可读原因，具备对应的 `admins.create`、`admins.update` 或 `roles.manage` 权限，并提供与当前 Session 绑定的 MFA Step-up 凭证。最后一个有效 `SUPER_ADMIN` 不能被停用或移除该角色，并发请求同样受保护。`SUPER_ADMIN` 永远拥有完整权限目录且不可编辑。列表和审计值都不包含密码、TOTP Secret、恢复码明文、Cookie 或 Access Token。

### 3.6 功能、能力、系统设置与集成

```text
GET   /v1/admin/features
GET   /v1/admin/capabilities
GET   /v1/admin/settings
PATCH /v1/admin/settings
```

功能与能力发现接口只返回非秘密信息，包括可选模块、支持的资源和操作、批量上限、实时订阅能力以及轮询回退周期。系统设置只暴露数据库白名单中的功能开关与 HTTPS 集成链接；配置 Secret、数据库连接串、Token 和私钥永远不会进入该模型。

读取设置要求 `settings.read`。更新要求 `settings.update`、操作原因与 MFA Step-up，并在同一事务中写入审计。URL 只允许不含嵌入凭据的 HTTPS 地址。Grafana 配置仅作为只读 Dashboard 链接；Admin Web 不重复实现完整监控系统，也不代理 Grafana 凭据。

## 4. Relay HTTP 生命周期 API

### 4.1 首次注册

```http
POST /internal/v1/relay-nodes/enroll
Authorization: Bearer <one-time-bootstrap-token>
Content-Type: application/json
```

请求字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `display_name` | string | 运维可读名称 |
| `region`, `zone`, `provider` | string | 调度与资产标签 |
| `software_version` | string | Relay 版本 |
| `protocol_version` | integer | V1.1 Edge 必须为 2；客户端 v1 兼容由 Edge 显式开关控制 |
| `advertised_endpoints` | array | `protocol`, `host`, `port`；客户端真实可达地址 |
| `supported_protocols` | string[] | 当前包含 `UDP` |
| `capacity` | object | `max_allocations`, `max_egress_bps` |
| `csr_pem` | string | Edge 本地生成的 Ed25519 CSR |

成功返回 201：

```json
{
  "data": {
    "node": {"node_id": "relay_...", "state": "BOOTSTRAPPING"},
    "node_token": "returned-once",
    "certificate_pem": "-----BEGIN CERTIFICATE-----...",
    "ca_certificate_pem": "-----BEGIN CERTIFICATE-----...",
    "certificate_expires_at": "2026-07-19T12:00:00Z",
    "relay_token_keyset": {"keys": []}
  },
  "request_id": "req_..."
}
```

Bootstrap Token 在成功事务中标记为已消费；`node_token` 仅返回一次，控制面只保存 hash。Edge 将 Node Token、私钥、证书、CA 和验证 keyset 以权限 600 写入持久化 `identity.json`。

### 4.2 证书续签

```http
POST /internal/v1/relay-nodes/{node_id}/certificate/renew
Authorization: Bearer <node-token>
Content-Type: application/json

{"csr_pem":"-----BEGIN CERTIFICATE REQUEST-----..."}
```

成功返回新证书、CA、过期时间和当前 Relay Token 公钥集。Node ID 和 Node Token 必须对应，已撤销节点不能续签。Edge 默认在证书剩余不足一小时时尝试续签。

### 4.3 查询和运维状态迁移

以下接口要求 Admin Token 和 trusted CIDR：

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| GET | `/internal/v1/relay-nodes` | 分页查询全部已注册节点；支持 `region`、`zone`、`provider`、`state`、`cursor`、`limit`，包括离线和已撤销节点 |
| GET | `/internal/v1/relay-nodes/{node_id}` | 查询 endpoint、容量、负载、证书和状态；不返回凭据 |
| POST | `/internal/v1/relay-nodes/{node_id}/drain` | `READY -> DRAINING`；可选 `{deadline_seconds,migrate_existing}`，空请求默认只停止新 allocation |
| POST | `/internal/v1/relay-nodes/{node_id}/resume` | 恢复为 `READY` |
| POST | `/internal/v1/relay-nodes/{node_id}/revoke` | 永久撤销 Node Token/证书身份 |
| POST | `/internal/v1/relay-signing-keys/{key_id}/activate` | 所有 READY 节点确认 staged Keyset 后激活签名 key |

状态包括 `BOOTSTRAPPING`、`CONNECTING`、`READY`、`DRAINING`、`UNHEALTHY`、`OFFLINE`、`REVOKED`。默认 15 秒心跳，45 秒无心跳转 `UNHEALTHY`，90 秒转 `OFFLINE`。Drain 的 `migrate_existing=false` 保留现有 allocation 到自然结束或 deadline；`true` 使用有界故障迁移状态机逐步迁移；Revoke 不可逆。

## 5. Relay mTLS gRPC 控制流

服务定义：

```protobuf
service RelayControl {
  rpc Connect(stream google.protobuf.Struct)
      returns (stream google.protobuf.Struct);
}
```

连接目标是控制面 TCP 9090。TLS 要求：

- 最低 TLS 1.3；
- Edge 提交由持久化 Relay CA 签发的客户端证书；
- 控制面验证证书并以 SHA-256 fingerprint 绑定数据库节点；
- Edge 使用注册响应中的 CA 验证服务证书；
- 当前服务证书 DNS SAN 为 `control-plane` 和 `localhost`，分离部署仍应设置 `control_server_name: control-plane`。

所有消息使用同一 envelope：

```json
{"type":"Heartbeat","payload":{}}
```

Edge -> Control Plane：

| Type | 关键 payload | 说明 |
| --- | --- | --- |
| `Hello` | `node_id`, `software_version`, `protocol_version` | 必须是连接首包 |
| `Heartbeat` | `active_allocations`, `current_egress_bps`, `current_ingress_bps`, `load_state` | 租约与负载；`load_state` 为 `NORMAL`、`DEGRADED`、`REJECT_NEW` 或 `DRAINING` |
| `CapacityReport` | 同负载字段 | 容量更新 |
| `TrafficReport` | 心跳负载字段，以及 `process_id`、单调递增 `sequence` 和累计 packets/bytes/bind/token/rate-limit/reconnect 计数器 | 复用已认证 mTLS 流更新租约、负载和节点遥测；累计整数使用十进制字符串，避免 `protobuf.Struct` 的浮点精度损失 |
| `AllocationOpened` | `allocation_id` | allocation 已安装 |
| `AllocationClosed` | `allocation_id` | allocation 已释放 |
| `RuntimeError` | 实现定义的非秘密诊断 | 运行错误报告 |
| `DrainCompleted` | drain 完成信息 | 排空完成 |

Control Plane -> Edge：

| Type | 关键 payload | 说明 |
| --- | --- | --- |
| `ConfigSnapshot` | `config_version`, `heartbeat_interval_seconds`, `lease_seconds`, `node_state`, drain 字段 | 首次连接配置；节点重连时恢复持久化 Drain |
| `KeysetUpdate` | Relay Token 公钥集 | 签名密钥轮换 |
| `EnterDrain` | RFC 3339 drain deadline、`migrate_existing` | 停止接受新 allocation |
| `ExitDrain` | — | 恢复接收 |
| `RevokeAllocation` | `allocation_id` | 释放指定 allocation |
| `CertificateRotation` | 轮换提示 | 触发续签流程 |
| `Shutdown` | 原因/deadline | 有序停止 |

未知 Type 返回 gRPC `InvalidArgument`。证书或节点身份不匹配返回 `Unauthenticated`/`PermissionDenied`。心跳或 allocation 状态不合法返回 `FailedPrecondition`。控制流断开时 Edge 指数退避重连；短时断开不应立即清除仍有效的本地 allocation。

## 6. Relay UDP 数据面

完整二进制格式见 `Backend/api/relay-protocol.md`。摘要：

1. Client 发送带 `client_nonce`、`requested_mtu` 和签名 Relay Token 的 v2 `BIND_INIT`；
2. Relay 返回不放大的 `server_nonce + expires_in_ms + HMAC Cookie`；
3. Client 从相同 UDP endpoint 原样携带 nonce、MTU、Cookie 和 Token 发送 `BIND_PROOF`；
4. Relay 无状态校验 Cookie，再验签并返回 `BIND_OK`、随机 8 字节 handle 和协商 MTU；
5. HOST 与 PEER 都绑定后，才转发带 HMAC tag 和 sequence 的 `DATA`。

Relay Token claims 绑定 `allocation_id`、`connection_id`、`relay_node_id`、端点角色、有效期、带宽/PPS/总字节限制和协议版本。数据包不包含任意目标地址，因此只能在同一 allocation 的 HOST 与 PEER 间转发。Relay 不解密游戏 Payload，并丢弃未知 handle、错误角色/来源、无效 tag、重放/窗口外序号、超时或超限包。

## 7. 指标 API

控制面：

```http
GET /internal/metrics
Accept: text/plain
```

关键指标包括 HTTP 请求/延迟、bind 成败、session/refresh 重放、P2P 房间、Dedicated Server 状态、Relay 节点/allocation、数据库连接池和 Go runtime。控制面还为数据库中的每个 Relay 输出 `relay_node_info`、`relay_node_state`、心跳/租约、容量和 mTLS 连接状态；已升级 Relay 通过 `TrafficReport` 额外输出 `relay_node_*_total` 累计遥测。旧版节点无需同步升级，仍会出现在节点清单、状态与租约指标中。公网 Caddy 必须返回 404。

边缘节点：

```http
GET http://127.0.0.1:9100/metrics
```

关键指标包括 active allocations、收发/丢弃包数、转发字节、bind 成败、token invalid、rate-limit drop、控制流连接和重连次数，以及按 `state` 标注的 `relay_load_state` 和状态切换计数。过载状态经 mTLS `TrafficReport` 上报并持久化；调度器不会把新连接或迁移分配到 `REJECT_NEW`/`DRAINING` 节点。通过节点本地 Prometheus agent 抓取，不得把 9100 暴露公网。

## 8. 内部错误和审计

HTTP 错误仍使用统一 `error` + `request_id` envelope。管理写操作、登录、Refresh Token 重放、Relay 注册/续签/状态迁移必须记录结构化审计字段，但不得记录：

- Access/Refresh/Admin/Game Server/Bootstrap/Node/Relay Token 全文；
- 私钥、CSR 私钥或完整 `identity.json`；
- 完整游戏 Payload；
- 数据库连接 URL 中的密码。

排障使用 request ID、actor/resource ID、状态迁移、证书 fingerprint、容器镜像 digest 和时间范围。证书 fingerprint 不是私钥，可以用于身份关联。

## 9. 兼容性与权威契约

- HTTP Schema：`Backend/api/openapi/openapi.yaml`
- gRPC Service：`Backend/api/proto/relay_control.proto`
- 生成的 Go gRPC binding：`Backend/api/proto/relay_control_grpc.go`
- UDP v1：`Backend/api/relay-protocol.md`
- 权限矩阵：`Backend/api/openapi/auth-permission-matrix.md`

新增字段应保持向后兼容；删除/改名、枚举收紧、认证方式改变或二进制包头改变都需要新 API/协议版本。内部路径并不代表可以忽略兼容性，因为边缘节点允许滚动升级。
## MetaServer 内部与管理路由

完整安全和字段契约见
[MetaServer 内部 API](metaserver-internal.zh-CN.md)。Dedicated Server 调用必须
同时通过 Game Server Token 哈希、`X-Game-Server-Id`，以及由同一凭据代际
绑定的节点证书私钥生成的 Ed25519 请求签名。时间戳和 nonce 用于阻止重放；
后端还会校验节点新鲜度与状态、路由 scope、对局归属和玩家名单。管理写操作
还要求可信网段、人工管理员会话、权限、Step-up、原因和审计。

| 方法 | 路径 | Scope 或权限 |
| --- | --- | --- |
| GET | `/internal/v1/meta/matches/{match_id}/players/{player_id}/loadout` | Game Server `meta.loadouts.read` |
| POST | `/internal/v1/meta/matches/{match_id}/players/{player_id}/connected` | Game Server `meta.matches.connect` |
| POST | `/internal/v1/meta/matches/{match_id}/completed` | Game Server `meta.matches.complete` |
| PUT | `/internal/v1/meta/battlelog/reports/{report_id}` | Game Server `meta.battlelog.write` |
| GET | `/v1/admin/meta/overview` | `meta.read` |
| GET | `/v1/admin/meta/players/{player_id}/loadouts` | `meta.loadouts.read` |
| PUT | `/v1/admin/meta/players/{player_id}/loadouts/{role_id}` | `meta.loadouts.update` + Step-up |
| GET | `/v1/admin/meta/matches` | `meta.read` |
| POST | `/v1/admin/meta/matches/{match_id}/cancel` | `meta.matches.manage` + Step-up |
| PUT | `/v1/admin/meta/playlists/{slug}` | `meta.content.manage` + Step-up |
| PUT | `/v1/admin/meta/notifications/{notification_id}` | `meta.content.manage` + Step-up |
