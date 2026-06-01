# 05 — Backend API

> 来源合并：backend-api-spec-v2.md（主体）、backend-api-spec-v1.md（兼容端点）
> 最后更新：2026-04-26
> 当前状态：后端正从 ASP.NET MatchServer 迁移到 Node.js Metaserver。v2 规范仍是权威 API 设计文档。

## 当前实现

- 后端：`.NET 8 / ASP.NET Core Minimal API / EF Core SQLite`（→ 迁移到 Metaserver）
- 桌面浏览器 GUI：`Python 3.11 + tkinter`（已废弃，在 `Deprecated/TestProjects/Desktop/` 中）
- 已废弃的 WPF 原型：`Desktop/ProjectRebound.Browser`（不再维护）

## 约定

- 协议：HTTP/1.1 JSON
- 基础路径：`/v1`
- 认证：`Authorization: Bearer <player_token>`（兼容阶段写操作可先放开；公共查询匿名）
- 网络模型：V1 优先公网直连 / UDP 打洞；主机掉线本局结束

## 兼容端点（必须保留）

### POST /server/status

旧版心跳端点。DLL 当前唯一调用的后端接口。

Request：
```json
{
  "name": "CN-1",
  "region": "CN",
  "mode": "/Game/Online/GameMode/BP_PBGameMode_Rush_PVE_Normal.BP_PBGameMode_Rush_PVE_Normal_C",
  "map": "Warehouse",
  "port": 7777,
  "playerCount": 3,
  "serverState": "RoundInProgress"
}
```

处理规则：
- 按 `remote_ip + payload.port` 或显式 `serverId` 去重
- 更新 `lastSeenAt`。若 `now - lastSeenAt > ttlSeconds`（建议 15s），标记 stale/offline
- V2 扩展：有 `roomId/hostToken` 时映射到对应房间心跳
- 始终返回 200

---

## V2 API

### 匿名认证

**POST /v1/auth/guest**

```json
// Request
{ "displayName": "Player", "deviceToken": null }
// Response
{ "playerId": "uuid", "displayName": "Player", "accessToken": "token" }
```

### UDP 可达性探测

**POST /v1/host-probes** — 创建探测。后端向请求来源公网 IP 的指定 UDP 端口发送 `nonce`。
```json
// Request: { "port": 7777 }
// Response: { "probeId": "uuid", "publicIp": "1.2.3.4", "port": 7777, "nonce": "token", "expiresAt": "..." }
```

**POST /v1/host-probes/{probeId}/confirm** — GUI 收到 UDP `nonce` 后确认。

### 房间

**POST /v1/rooms** — 创建玩家主机房间。
```json
// Request
{ "probeId": "uuid", "name": "CN Room", "region": "CN", "map": "Warehouse",
  "mode": "pve", "version": "dev", "maxPlayers": 8 }
// Response
{ "roomId": "uuid", "hostToken": "secret", "heartbeatSeconds": 5 }
```

**GET /v1/rooms** — 房间列表。参数：region, map, mode, version, state, page, pageSize。

**GET /v1/rooms/{roomId}** — 房间详情。

**POST /v1/rooms/{roomId}/join** — 加入房间。
```json
// Response: { "connect": "1.2.3.4:7777", "joinTicket": "token", "expiresAt": "..." }
```

**POST /v1/rooms/{roomId}/leave** — 释放加入位置。
**POST /v1/rooms/{roomId}/heartbeat** — 主机心跳（5s 间隔）。
**POST /v1/rooms/{roomId}/start** — 标记房间为 InGame。
**POST /v1/rooms/{roomId}/end** — 标记房间为 Ended。

### 快速匹配

**POST /v1/matchmaking/tickets** — 创建匹配 ticket。
**GET /v1/matchmaking/tickets/{ticketId}** — 轮询匹配状态。
**DELETE /v1/matchmaking/tickets/{ticketId}** — 取消匹配。

### NAT / 打洞

**POST /v1/nat/bindings** — 创建 UDP rendezvous 绑定。客户端需从同一 UDP socket 发送含 bindingToken 的 JSON 包到后端 UDP rendezvous 端口（5001）。

**POST /v1/nat/bindings/{bindingToken}/confirm** — 确认后端已观察到 UDP 包，返回公网 endpoint。

**POST /v1/rooms/{roomId}/punch-tickets** — 创建打洞会话。交换 client 和 host 的 NAT binding。

**GET /v1/rooms/{roomId}/punch-tickets?hostToken=...** — 主机 proxy 轮询待打洞 client endpoint。

### UDP Relay 兜底

**POST /v1/relay/allocations** — 分配最小 relay 凭据。host 用 `hostToken`，client 用 `joinTicket`。

Relay 模式：
- Host proxy 注册到 `relayHost:relayPort`（5002/udp）
- Client proxy 也注册
- Relay 只转发 UDP datagram：host→client，client→host
- 不解析游戏协议

### 保留接口

- `POST /v1/rooms/{roomId}/host-migration/*` — V1 返回 `501 NOT_IMPLEMENTED`
- `POST /v1/server/register`（可选）
- `GET /v1/admin/servers`（管理接口）

---

## 生命周期

| 事件 | 超时 |
|------|------|
| 心跳间隔 | 5s |
| 房间 stale | 15s 无心跳 |
| 房间 ended | 45s 无心跳 → `endedReason = "host_lost"` |
| 主机掉线 | 不迁移。后续版本预留接口 |

---

## 统一错误格式

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "port must be between 1 and 65535",
    "details": [{ "field": "port", "reason": "out_of_range" }],
    "requestId": "8b9f2d24f23b4dfb"
  }
}
```

---

## Metaserver 端口（Node.js，当前实现）

| 服务 | 端口 |
|------|------|
| HTTP API | 8000 |
| TCP RPC（protobuf） | 6969（注意：曾是 6968，已修复为 6969） |
| UDP QoS | 9000 |
| TCP Matchmaking | 9000 |

---

## 相关文档

- `01-System-Overview.md` — 系统全景
- `06-Infrastructure.md` — 部署与运维
