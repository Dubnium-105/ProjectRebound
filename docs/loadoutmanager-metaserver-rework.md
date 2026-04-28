# LoadoutManager 重做 —— 基于 Metaserver 服务化架构

## 概述

将 LoadoutManager 从"客户端捕获 + 聊天通道传输 + 本地文件持久化"的 hack 模式，重做为"metaserver 权威服务 + 原生 protobuf 协议 + 服务端权威应用"的架构。

**核心变更：**
- Metaserver 成为配装存储和物品定义的权威服务端
- LoadoutManager 移除所有客户端 ProcessEvent 钩子
- 游戏客户端通过原生 `GetPlayerArchiveV2` 协议获取配装
- Payload 服务端从 metaserver HTTP API 获取配装并权威应用
- 不再使用 `__LDS__` 聊天通道传输配装数据

---

## 一、Metaserver 变更

### 新建文件

#### `BoundaryMetaServer/game/definitionIndex.js`
物品定义内存索引。启动时一次性加载所有 `game/definitions/DT_*.json` 到内存 Map/Set，提供：
- 角色 → 武器/装备 Scope 查询
- 武器 → 各槽位配件 Scope 查询
- 武器重定向（基础武器 ↔ 角色专有武器）
- 物品类型查询
- `validateLoadout(loadoutJson)` — 配装修验
- `filterLoadout(loadoutJson)` — 过滤不兼容物品

#### `BoundaryMetaServer/game/loadoutStore.js`
玩家配装持久化存储（`data/loadouts/{playerId}.json`），提供：
- `getRoleArchive(playerId, roleIds[])` — 返回 GetPlayerArchiveV2 格式数据
- `updateRoleArchive(playerId, roleId, data)` — 更新单个角色配装
- `getFullLoadout(playerId)` / `setFullLoadout(playerId, data)` — 完整 loadout JSON 读写
- `getRoleLoadoutSnapshot(playerId, roleId)` / `setRoleLoadoutSnapshot(...)` — 单角色快照读写

#### `BoundaryMetaServer/start-metaserver.bat`
一键启动 metaserver 的批处理文件。自动检测 Node.js、首次运行时安装 npm 依赖、启动服务。监听端口：
- HTTP API: `http://127.0.0.1:8000`
- TCP Game: `127.0.0.1:6969`
- UDP QoS: `127.0.0.1:9000`

### 修改文件

#### `BoundaryMetaServer/index.js`

**新增 REST API 端点：**

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/health` | 健康检查 |
| `GET` | `/api/definitions/roles` | 所有角色 ID 列表 |
| `GET` | `/api/definitions/roles/:roleId` | 角色定义（WeaponScope 等） |
| `GET` | `/api/definitions/weapons` | 所有武器 ID 列表 |
| `GET` | `/api/definitions/weapons/:weaponId` | 武器定义（各槽位 Scope） |
| `GET` | `/api/definitions/resolve-weapon/:roleId/:baseWeaponId` | 武器重定向 |
| `GET` | `/api/definitions/items/:itemId/type` | 物品类型查询 |
| `GET` | `/api/loadout/:playerId` | 获取玩家完整配装 |
| `PUT` | `/api/loadout/:playerId` | 更新玩家配装（Browser 调用） |
| `GET` | `/api/loadout/:playerId/:roleId` | 获取玩家单角色配装快照 |
| `POST` | `/api/loadout/validate` | 校验 loadout JSON |
| `POST` | `/api/loadout/filter` | 过滤不兼容物品 |

**启用原生 protobuf 协议（之前被注释掉）：**

- `GetPlayerArchiveV2` — 从 LoadoutStore 读取玩家配装，返回 `PlayerRoleData[]`
- `QueryAssets` — 返回 DT_ItemType 中所有物品（标记为已拥有）
- `UpdateRoleArchiveV2` — 返回成功（完整请求逆向待后续）

---

## 二、Payload C++ 变更

### 新建文件

#### `Payload/Loadout/MetaserverClient.h/cpp`
WinHTTP 客户端，封装对 metaserver REST API 的调用：
- `GetRole(roleId)` / `GetWeapon(weaponId)` — 定义查询（带缓存）
- `GetPlayerLoadout(playerId)` / `GetPlayerRoleLoadout(playerId, roleId)` — 配装查询
- `ValidateLoadout(loadout)` / `FilterLoadout(loadout)` — 配装修验/过滤
- `IsAvailable()` — 健康检查
- 3 秒超时，metaserver 不可用时返回 `nullopt`

#### `Payload/Loadout/LoadoutSerializer.h/cpp`
从原 LoadoutManager.cpp 提取的 JSON ↔ SDK 结构体互转：
- 基础工具：`NameToString`, `IsBlankText`, `IsBlankName`, `NameFromString`
- 空 JSON 工厂：`EmptyRoleJson`, `EmptyWeaponJson` 等
- JSON I/O：`ReadJsonFile`, `WriteJsonFile`
- SDK → JSON：`WeaponToJson`, `InventoryToJson`, `RoleToJson` 等
- JSON → SDK：`WeaponFromJson`, `InventoryFromJson`, `CharacterFromJson` 等
- 快照解析：`TryResolveRoleConfig`, `TryResolveWeaponConfigFromSnapshot`
- 自定义配装：`LoadCustomLoadoutConfig`, `RoleFromCustomLoadout`

#### `Payload/Loadout/LoadoutApplication.h/cpp`
从原 LoadoutManager.cpp 提取的服务端权威应用逻辑：
- 对象查找：`GetFieldModManager`, `GetLocalPlayerController`, `FindPlayerControllerForCharacter`
- 角色 ID 解析：`ResolveCharacterRoleId`, `ResolveLiveCharacterRoleId`
- 库存推送：`PreSpawnApply`, `BuildInventoryListFromSnapshot`
- 配装应用：`PostSpawnApply`, `ApplyLauncherConfig`, `ApplyMeleeConfig`, `ApplyMobilityConfig`
- 武器辅助：`FindWeaponForConfig`, `RefreshWeaponRuntimeVisuals`, `MarkActorForReplication`

### 修改文件

#### `Payload/Loadout/LoadoutManager.h`
- 公有 API 保持不变（Hooks.cpp 兼容性）
- `OnClientProcessEventPre/Post`、`TickClient` 保留声明但内部为空桩
- `OnServerLoadoutDataReceived` 保留声明但内部为 no-op

#### `Payload/Loadout/LoadoutManager.cpp`
完全重写（从 ~2524 行缩减到 ~320 行）：

**移除的功能：**
- `CaptureSnapshotFromMenu()` — 不再从菜单 UI 捕获
- `ExportSnapshotNow()` / `TriggerAsyncExport()` — metaserver 负责持久化
- `MaybeOverrideInitWeaponConfig()` — 不再覆盖武器初始化参数
- `UploadClientSnapshotForRole()` — 不再通过聊天通道上传
- `ClientTick()` — 空桩
- `OnClientProcessEventPre/Post()` — 空桩
- `OnServerLoadoutDataReceived()` — no-op（`__LDS__` 通道已弃用）

**保留并改造的功能：**
- `PreloadSnapshot()` — 初始化 MetaserverClient，检查 metaserver 可达性
- `OnRoleSelectionConfirmed()` — 从 metaserver HTTP API 获取配装 → 校验 → 过滤 → 存储按玩家快照 → 推送库存
- `OnServerProcessEventPre()` — 复活时重新推送库存（移除 InitWeapon 覆盖）
- `TickServer()` — 轮询待应用快照，`PostSpawnApply` 权威应用
- `RememberMenuSelectedRole()` — 保留兼容性

**新增的数据流：**
```
Player 选择角色 (ServerConfirmRoleSelection)
  → MetaserverClient::GetPlayerLoadout(playerId)
    → HTTP GET /api/loadout/:playerId
  → MetaserverClient::ValidateLoadout(snapshot)
    → HTTP POST /api/loadout/validate
  → MetaserverClient::FilterLoadout(snapshot)
    → HTTP POST /api/loadout/filter
  → LoadoutApplication::PreSpawnApply(config)
    → ServerPreOrderInventory RPC（原生）
  → [Player spawns]
  → LoadoutApplication::PostSpawnApply(character, config)
```

#### `Payload/Hooks/Hooks.cpp`

**服务端 ProcessEvent hook 变更：**
- 移除 `__LDS__` 前缀解析（`ServerSay` 拦截块中删除 `__LDS__` 分支，保留 `__DBG__` 分支）
- 保留 `gLoadoutManager->TickServer()` 和 `OnServerProcessEventPre()`
- 保留 `gLoadoutManager->OnRoleSelectionConfirmed()` 调用

**客户端 ProcessEvent hook 变更：**
- 移除 `gLoadoutManager->TickClient()` 调用
- 移除 `gLoadoutManager->OnClientProcessEventPre()` 调用
- 移除 `gLoadoutManager->OnClientProcessEventPost()` 调用

#### `Payload/Payload.vcxproj` / `Payload/Payload.vcxproj.filters`
- 新增 `MetaserverClient.h/cpp`、`LoadoutSerializer.h/cpp`、`LoadoutApplication.h/cpp`

---

## 三、Python Browser 变更

#### `Desktop/.../browser_loadout.py`
- 新增 `set_metaserver_url(url)` — 允许外部设置 metaserver 地址
- `load_current_loadout_snapshot()` — 优先从 metaserver HTTP API 加载，降级到本地文件
- `write_launch_loadout_snapshot()` — 优先上传到 metaserver，降级到本地文件
- `prepare_launch_loadout_snapshot()` — 保持不变，适配新流程
- 本地磁盘 JSON 文件保留为降级路径

#### `Desktop/.../project_rebound_browser.py`
- **移除** `ensure_fake_login_server()` 方法 — 不再自动启动 node.exe + index.js
- **移除** `login_server_creation_flags()` 方法 — 不再需要
- **新增** `check_metaserver()` 方法 — 通过 HTTP 健康检查（`GET /api/health`）检测 metaserver 可达性
- **新增** metaserver 不可达时弹窗警告，提示用户运行 `start-metaserver.bat`
- `start_host()` — `ensure_fake_login_server()` 调用替换为 `check_metaserver()`
- `launch_client_via_batch()` — 移除 `node.exe` 自动启动和依赖检查；改为 PowerShell 调用 `/api/health` 可达性检测
- `launch_host_via_batch()` — 同上
- `load_config()` — 过滤已保存 JSON 中的未知键，防止旧配置中的遗留字段（如 `legacy_list_url`）导致 `AppConfig.__init__()` 崩溃

---

## 四、不变的部分

| 组件 | 说明 |
|------|------|
| `BoundaryMetaServer/game/definitions/*.json` | 物品定义数据源不变 |
| `BoundaryMetaServer/game/proto/*.proto` | protobuf 协议定义不变 |
| `Payload/SDK/ProjectBoundary_structs.hpp` | SDK 结构体不变 |
| `Payload/SDK/ProjectBoundary_classes.hpp` | SDK 类不变 |
| `Payload/SDK/ProjectBoundary_parameters.hpp` | 参数结构体不变 |
| `Payload/dllmain.cpp` | 全局 `gLoadoutManager` 构造和调用不变 |
| `Payload/Libs/json.hpp` | nlohmann/json 不变 |
| `Payload/Debug/Debug.h` | `ClientLog` 等调试函数不变 |
| `Backend/` 全部文件 | MatchServer 不受影响 |

---

## 五、架构对比

### 之前（客户端捕获模式）

```
游戏菜单 UI (DisplayCharacter, FieldModManager)
  → ProcessEvent 钩子捕获
    → CaptureSnapshotFromMenu()        [客户端]
    → ExportSnapshotNow()              [客户端 → 磁盘 JSON]
    → EnsureSnapshotLoaded()          [从磁盘读取]
    → __LDS__ 聊天通道                 [客户端 → 服务端]
    → OnServerLoadoutDataReceived()   [服务端接收]
    → StorePerPlayerSnapshot()        [服务端存储]
    → MaybeOverrideInitWeaponConfig() [客户端+服务端 覆盖武器参数]
    → PostSpawnApply()                [客户端+服务端 应用]
```

### 之后（metaserver 服务端权威模式）

```
Browser 设置配装
  → PUT /api/loadout/:playerId         [metaserver 持久化]

游戏客户端进入菜单
  → GetPlayerArchiveV2 (protobuf)      [游戏原生协议 → metaserver]
  → QueryAssets (protobuf)             [游戏原生协议 → metaserver]
  → 客户端自然显示配装                  [原生体验，无需注入]

游戏服务端 (Payload)
  → OnRoleSelectionConfirmed()
    → MetaserverClient::GetPlayerLoadout()  [HTTP GET → metaserver]
    → MetaserverClient::ValidateLoadout()   [HTTP POST → metaserver]
    → MetaserverClient::FilterLoadout()     [HTTP POST → metaserver]
    → PreSpawnApply()                       [ServerPreOrderInventory RPC]
    → TickServer → PostSpawnApply()         [服务端权威应用]
```
