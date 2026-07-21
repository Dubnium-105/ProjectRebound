# ProjectRebound 内部、管理与 Relay API

本文档描述不面向普通游戏客户端的接口：管理员 HTTP API、Relay 注册/续签、mTLS gRPC 控制流、Prometheus 指标和 UDP Relay 数据面。完整 JSON Schema 位于 `Backend/api/openapi/openapi.yaml`，gRPC service 位于 `Backend/api/proto/relay_control.proto`。

## 1. 网络入口和信任边界

| 接口 | 默认地址 | 调用方 | 防护 |
| --- | --- | --- | --- |
| 管理 HTTP | `127.0.0.1:18080/v1/admin/*` | 运维后台/SSH 隧道 | Admin Token + trusted CIDR |
| 内部 Relay 管理 HTTP | `127.0.0.1:18080/internal/v1/relay-nodes/*` | 运维后台 | Admin Token + trusted CIDR |
| Relay 注册/续签 HTTP | 公网 HTTPS `/internal/v1/relay-nodes/enroll`、`.../certificate/renew` | Edge Relay | 一次性 Bootstrap Token 或 Node Token |
| 控制面指标 | `127.0.0.1:18080/internal/metrics` | Prometheus | trusted CIDR；公网代理返回 404 |
| Relay 控制流 | 控制面 TCP 9090 | Edge Relay | TLS 1.3 双向证书认证 |
| Relay 数据面 | Edge UDP 8443 | 游戏端点 | 签名 Relay Token + Cookie Challenge + 数据包 HMAC |
| Relay 指标 | Edge `127.0.0.1:9100/metrics` | 节点本地 agent | 仅回环监听 |

公网 Caddy 必须对 `/v1/admin*` 和 `/internal/*` 返回 404，只允许 Relay enroll 和 certificate renew 两条机器接口。源 IP 白名单不能替代 Token，Token 也不能替代网络隔离。

## 2. Admin Token

控制面通过环境变量读取：

```text
ADMIN_TOKENS=operator=<high-entropy-token>;automation=<another-token>
ADMIN_TRUSTED_CIDRS=127.0.0.0/8,10.0.0.0/8,...
```

请求：

```http
Authorization: Bearer <admin-token>
```

玩家 Access Token 不可用于管理接口。启用 `TRUST_PROXY_HEADERS=true` 时只能把控制面放在受信反向代理之后，禁止让客户端绕过代理直连；否则伪造的转发 Header 可能影响源地址判断。

## 3. 玩家管理 API

| 方法 | 路径 | 参数/请求 | 成功 |
| --- | --- | --- | --- |
| GET | `/v1/admin/players` | `cursor`, `limit`, `account_status` | 玩家分页列表 |
| GET | `/v1/admin/players/{player_id}` | Path ID | 完整管理记录 |
| PATCH | `/v1/admin/players/{player_id}` | 至少一个：`account_status`, `is_vip`, `revoke_sessions` | 更新后的玩家、撤销 session 数 |
| POST | `/v1/admin/players/{player_id}/revoke-sessions` | 无 | 撤销数量和时间 |

Patch 示例：

```http
PATCH /v1/admin/players/player_123 HTTP/1.1
Authorization: Bearer <admin-token>
Content-Type: application/json

{
  "account_status": "BANNED",
  "is_vip": false,
  "revoke_sessions": true
}
```

`account_status` 为 `ACTIVE`、`BANNED` 或 `DELETED`。更新会写入审计记录；需要立即使现有登录失效时设置 `revoke_sessions=true`，或调用独立的 revoke-sessions 端点。外部工单原因应通过受控审计系统关联，不应把 Token、隐私数据或游戏 Payload 放入请求或日志。

### 3.1 邀请码管理

| 方法 | 路径 | 参数/请求 | 成功 |
| --- | --- | --- | --- |
| POST | `/v1/admin/invite-codes` | `batch_name`, `max_uses`；可选 `expires_at`, `permissions` | 创建元数据并仅本次返回明文 `code` |
| GET | `/v1/admin/invite-codes` | `cursor`, `limit` | 元数据分页列表，不返回明文或哈希 |
| GET | `/v1/admin/invite-codes/{id}` | Path ID | 单条元数据，不返回明文或哈希 |
| PATCH | `/v1/admin/invite-codes/{id}` | `batch_name`, `max_uses`, `expires_at`, `enabled`, `permissions` 中至少一个 | 更新后的元数据 |
| POST | `/v1/admin/invite-codes/{id}/revoke` | 无 | 幂等禁用并记录撤销时间 |

创建响应中的明文邀请码是秘密，只出现一次；数据库仅存 SHA-256 哈希。`max_uses` 不得降低到 `used_count` 以下。绑定新 SteamID 时使用行级锁消费名额，因此并发争抢最后一个名额只会有一个事务成功。已有玩家再次 bind 不重复消费邀请码。

### 3.2 认证风险事件

`GET /v1/admin/auth/risk-events` 按 `cursor`、`limit`、`player_id`、`event_type`、`severity` 和 `unresolved_only` 查询认证风险记录。V1.1 只记录和展示，不执行自动封禁。响应不包含 Device ID 哈希，IP 在返回前脱敏；数据库中的事件详情也不得写入 Access Token、Refresh Token 或完整 Authorization Header。

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
