# MatchServer 完整修改方案

## Context

MatchServer 运行在 `204.12.195.98:9000`，同时处理 UDP（QoS 延迟测量）和 TCP（游戏逻辑）。MetaServer (`127.0.0.1:8000/6968/9000`) 已基本实现大厅/Lobby 功能。MatchServer 需要实现：QoS 响应、匹配后端、游戏 TCP 协议、loadout 同步。

## 整体架构

```
客户端                           MetaServer (:8000)             MatchServer (:9000)
  │                                    │                              │
  ├─ HTTP GET / ──────────────────────>│ (返回 MatchServer IP:port)     │
  │                                    │                              │
  ├─ TCP :6969 ───────────────────────>│                              │
  │  ├─ QueryMatchmakingRegion         │                              │
  │  ├─ QueryPlayList                  │                              │
  │  ├─ StartUnityMatchmaking ────────>│──> 后端通知 MatchServer         │
  │  │                                 │    (创建匹配/分配服务器)       │
  │  ├─ UDP QoS ping ──────────────────────────────────────────────>│
  │  │  (0x59...→←0x95 0x00...)       │                              │
  │  ├─ QueryUnityMatchmaking ────────>│ (轮询匹配状态)                 │
  │  └─ 匹配成功，获取 MatchServer 地址  │<── 返回服务器 IP:port        │
  │                                    │                              │
  ├─ TCP :9000 (游戏协议) ───────────────────────────────────────────>│
  │  ├─ 玩家加入比赛                     │                              │
  │  ├─ Loadout 同步                    │                              │
  │  └─ 游戏内数据交换                   │                              │
  └─ UDP :9000 (游戏内 QoS) ─────────────────────────────────────────>│
```

---

## 1. UDP QoS 协议（必须实现）

### 协议格式（已逆向）

**客户端请求：**
```
[0x59] [10 bytes padding/data] [variable payload]
```

**服务器响应：**
```
[0x95] [0x00] [echo bytes from offset 11 of request]
```

### 实现要求

```javascript
// 参考 metaserver/index.js:845-887 的现有实现
udp.on('message', (msg, info) => {
  if (msg[0] === 0x59) {
    const header = Buffer.from([0x95, 0x00]);
    const echo = msg.subarray(11);
    udp.send(Buffer.concat([header, echo]), info.port, info.address);
  }
});
```

QoS 用于客户端测量到各区域服务器的延迟，选择最优匹配区域。

---

## 2. Matchmaking 后端（需要与 MetaServer 协作）

### 2.1 匹配请求流程

客户端调用 MetaServer 的 RPC：

| Step | RPCPath | 客户端发送 | MetaServer 返回 | MatchServer 需要 |
|------|---------|-----------|-----------------|------------------|
| 1 | `StartUnityMatchmaking` | `{Payload: {MatchmakingRequestorUserId, UnknownMessage: [{RegionId, UnknownUInt}]}, GameMode: "Purge"}` | `{StatusCode: 0}` | 接受匹配请求，加入队列 |
| 2 | `QueryUnityMatchmaking` | `{ticketId, userId}` | — | 返回匹配状态 |
| 3 | `StopUnityMatchmaking` | `{ticketId, userId}` | — | 取消匹配 |

### 2.2 MetaServer 需要的接口

MatchServer 需提供 HTTP API 供 MetaServer 调用：

```
POST /matchmaking/enqueue
Body: { userId, regionId, gameMode, qosData }
Response: { ticketId, status: "queued" }

GET /matchmaking/status/{ticketId}
Response: { ticketId, status: "queued"|"found"|"timeout", serverIp, serverPort }

POST /matchmaking/cancel/{ticketId}
Response: { ticketId, status: "cancelled" }
```

### 2.3 匹配逻辑

```
当 N 个玩家加入同一区域/模式的队列：
  → 凑齐人数（如 6v6 = 12人）或超时
  → 分配/启动一个 GameServer 实例
  → 返回 { status: "found", serverIp, serverPort }
  → MetaServer 将此结果返回给轮询的客户端
```

---

## 3. TCP 游戏协议（核心）

MetaServer 的 TCP 9000 handler 目前只有 `console.log("MOGGEDDDDDDDDD")` 占位。需要实现完整的游戏服务器。

### 3.1 协议框架

客户端使用与 MetaServer TCP 6969 **相同的长度前缀 protobuf 协议**：

```
[4 bytes Big-Endian uint32 payload length] [RequestWrapper protobuf]
```

### 3.2 已知的 Gameplay RPC

从游戏二进制提取的 proto：

| RPCPath | 请求 | 说明 |
|---------|------|------|
| `/gameplay.Gameplay/PlayerJoinMatch` | `{ userId }` | 玩家加入比赛 |
| `/matchmaking.Matchmaking/QueryPlayList` | — | 从 MetaServer 处理 |

以及游戏内需要的（待逆向）：
- 玩家移动/射击同步
- 伤害/击杀/死亡
- 计分板
- 游戏结束

### 3.3 游戏服务器基础框架

```javascript
// GameServer 基本结构
class GameServer {
  constructor() {
    this.players = new Map();    // userId → PlayerState
    this.matchState = 'lobby';   // lobby → warmup → playing → ended
    this.tickRate = 128;         // 128 tick (matching the metaserver)
  }

  // 玩家连接处理
  onPlayerConnect(socket) {
    // 接收 PlayerJoinMatchReq
    // 验证 ticketId
    // 加载玩家 loadout（从 MetaServer 获取）
    // 同步游戏状态
  }

  // 游戏循环
  gameLoop() {
    // 每 tick 处理：
    // 1. 接收玩家输入
    // 2. 更新游戏状态
    // 3. 广播状态给所有玩家
  }
}
```

---

## 4. Loadout 同步（关键）

### 问题

当前 MetaServer 保存了 loadout（`data/loadouts/{playerId}.json`），但进战局后**没使用自定义武器配件**。原因是 MatchServer 不知道玩家的 loadout 配置。

### 解决方案

**方案 A — MetaServer 提供 loadout API（推荐）：**

MatchServer 在玩家加入时向 MetaServer 查询 loadout：

```
GET http://127.0.0.1:8000/api/loadout/{playerId}/{roleId}
Response: {
  primaryWeapon: "PEACE_RU-AKM",
  secondaryWeapon: "PEACE_GSW-DMR",
  ...slot data...,          ← GetPlayerArchiveV2 返回的 7 个槽位
  _weaponArchiveRaw: "0a05...",  ← UpdateWeaponArchiveV2 原始武器配件 hex
  _skinToken: "PEACE_TOK-DOS-01",
  _ornamentId: "PEACE_ORN-AK_Anime_02"
}
```

MatchServer 解析 `_weaponArchiveRaw` 还原武器配件配置，应用到游戏内。

**方案 B — 客户端直传：**

客户端进入战局时将 loadout 数据包含在 `PlayerJoinMatchReq` 中。需要逆向该请求的完整 proto 结构来确认是否包含 loadout 数据。

### 武器配件数据结构

从 `UpdateWeaponArchiveV2` 抓包（~500 bytes）解码：

```protobuf
// 角色 → 多把武器 → 每个武器的配件列表
message WeaponArchivePayload {
  string roleId = 1;                    // "PEACE"
  repeated WeaponConfig weapons = 2;
}

message WeaponConfig {
  string weaponId = 1;                  // "PEACE_RU-AKM"
  repeated WeaponPartSlot parts = 2;
}

message WeaponPartSlot {
  int32 slotIndex = 1;                  // 1-10 (配件槽位)
  string partId = 2;                    // "RU-AKM_MZL-COMPENSATOR"
  PartOrigin origin = 3;               // { partOri: "PartOri", ... }
}
```

MatchServer 需要根据 `weaponId` 和 `partId` 查找配件属性（从 `DT_ItemType.json` 定义数据），应用后坐力、伤害、精度等修正。

---

## 5. 定义数据同步

MatchServer 需要访问物品定义数据才能正确应用 loadout。两种方式：

**方式 1：共享 game/definitions/ 目录**

MatchServer 直接读取 `DT_*.json` 定义文件（18 角色、46 武器、257 配件、40462 物品类型）。确保 MatchServer repo 有相同版本的定义文件。

**方式 2：MetaServer API**

```
GET http://127.0.0.1:8000/api/definitions/roles/{roleId}
GET http://127.0.0.1:8000/api/definitions/weapons/{weaponId}
GET http://127.0.0.1:8000/api/definitions/items/{itemId}/type
```

推荐方式 1（零网络延迟），用 git submodule 或文件同步保证一致性。

---

## 6. 实现优先级

### Phase 1 — 最小可玩（UDP QoS + 匹配 + 基础游戏）

1. **UDP QoS 协议**（0x59/0x95）— 30 分钟
2. **匹配后端 API**（enqueue/status/cancel）— 2 小时
3. **MetaServer StartUnityMatchmaking 改造** — 通知 MatchServer 而非返回 stub — 1 小时
4. **TCP 游戏连接处理**（PlayerJoinMatch + 基础游戏循环）— 4-8 小时

### Phase 2 — Loadout 同步

5. **MetaServer loadout API 改造** — 在 `GetPlayerArchiveV2` 返回中包含 `_weaponArchiveRaw` — 30 分钟
6. **MatchServer loadout 解析** — 解析武器配件数据并应用到游戏内 — 3-4 小时
7. **物品定义同步** — 建立共享定义数据 — 1 小时

### Phase 3 — 完整体验

8. **MetaServer QueryUnityMatchmaking 改造** — 轮询 MatchServer 状态 — 1 小时
9. **MetaServer StopUnityMatchmaking 改造** — 取消匹配 — 30 分钟
10. **游戏内协议完善** — 计分板、回合、游戏结束 — 待定
11. **皮肤/装饰品同步** — SkinPayload 数据传递 — 1 小时

---

## 7. MetaServer 侧需要的修改

MatchServer 就绪后，MetaServer 需要以下改动：

### 7.1 `StartUnityMatchmaking` handler 改造

当前只是返回 `{StatusCode: 0}` stub。改为：

```javascript
// index.js ~line 667
else if(RPCPath === "/matchmaking.Matchmaking/StartUnityMatchmaking"){
  const req = decode StartMatchmakingRequest;
  const userId = req.Payload.MatchmakingRequestorUserId;
  const regionIds = req.Payload.UnknownMessage.map(m => m.RegionId);
  const gameMode = req.GameMode;

  // 通知 MatchServer
  const ticket = await fetch(`http://${MatchmakingHost}:9001/matchmaking/enqueue`, {
    method: 'POST',
    body: JSON.stringify({ userId, regionIds, gameMode }),
  });
  const ticketId = ticket.ticketId;

  // 返回 ticketId 给客户端（用于轮询）
  socket.write(WrapMessageAndSerialize(MessageId, RPCPath, { StatusCode: 0 }));
}
```

### 7.2 `QueryUnityMatchmaking` 改造

当前没有 handler。改为轮询 MatchServer：

```javascript
// 轮询匹配状态，找到后返回 serverIp:port
const result = await fetch(`http://${MatchmakingHost}:9001/matchmaking/status/${ticketId}`);
if (result.status === 'found') {
  // 返回 MatchServer 连接信息给客户端
}
```

### 7.3 `GetPlayerArchiveV2` 增强

返回中包含武器配件原始数据：

```javascript
// 在构建 ResponseObj.PlayerRoleDatas 时
PlayerRoleDatas.push({
  RoleID: roleId,
  ...slot data...,
  // 附加武器配件原始数据（MatchServer 需要）
  WeaponArchiveRaw: role._weaponArchiveRaw || '',
  SkinToken: role._skinToken || '',
  OrnamentId: role._ornamentId || '',
});
```

需要同步修改 `GetPlayerArchiveV2Response.proto` 添加对应字段。

---

## 8. 验证方案

### 端到端测试
1. 启动 MetaServer → 客户端登录 → 自定义 loadout（武器+配件+皮肤）
2. 启动 MatchServer → MetaServer 通知 MatchServer 开始匹配
3. 客户端点击匹配 → StartUnityMatchmaking → MatchServer 接受
4. MatchServer 凑齐人数 → 分配 GameServer 实例
5. MetaServer 轮询 → 返回 MatchServer IP:port → 客户端连接
6. 进入战局 → 验证：
   - 武器槽位正确（PrimaryWeapon/SecondaryWeapon）
   - 武器配件正确（镜/握把/弹匣/枪托等）
   - 皮肤/装饰品正确
   - 挂载/近战武器/机动装置正确

### 代理抓包验证
```
启动 proxy → 完整走一遍 登录→配装→匹配→进战局→退出
→ 运行 tools/analyze-logs.js --latest
→ 确认 0 个未知 RPC
```
