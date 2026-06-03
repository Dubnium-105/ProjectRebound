# ProjectRebound 逆向工程完整指南

> 最后更新：2026-06-04 | 覆盖 metaserver 协议逆向 + Native DLL 注入 + Frida 动态分析

---

## 目录

1. [整体工作链](#1-整体工作链)
2. [部署方式](#2-部署方式)
3. [已知函数地址全集](#3-已知函数地址全集)
4. [关键函数操作详解](#4-关键函数操作详解)
5. [JS 脚本设计指南](#5-js-脚本设计指南)

---

## 1. 整体工作链

### 1.1 系统全景图

```
┌────────────────────────────────────────────────────────────────────┐
│                         游戏客户端 (Shipping.exe)                     │
│                                                                      │
│  ┌─────────────┐   TCP:6969    ┌──────────────┐   TCP:6968   ┌──────┐│
│  │ Game Client │ ─────────────→│  proxy.js    │─────────────→│Real  ││
│  │ (protobuf)  │←──────────────│  (MITM 日志)  │←─────────────│Meta  ││
│  └─────────────┘               └──────┬───────┘              │Server││
│                                       │                       └──────┘│
│                                       │ 写 msgid_map.json              │
│                                       ▼                               │
│                              ┌────────────────┐                      │
│                              │ logs/binary/    │                      │
│                              │ logs/msgid_map  │                      │
│                              └────────────────┘                      │
└────────────────────────────────────────────────────────────────────┘
                                  │
                                  │ HTTP :8000
                                  ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        Metaserver (Node.js)                          │
│                                                                      │
│  ┌──────────────┐     ┌──────────────────────┐                      │
│  │  proxy.js     │     │  index.js             │                      │
│  │  (TCP MITM)   │     │  (Express REST API)   │                      │
│  │  端口: 6969   │     │  端口: 8000           │                      │
│  │              │     │                      │                      │
│  │  解码所有     │     │  GET  /api/health    │                      │
│  │  protobuf     │     │  GET  /api/loadout/  │                      │
│  │  流量并记录   │     │  PUT  /api/loadout/  │                      │
│  │              │     │  POST /api/connect    │                      │
│  └──────────────┘     └──────────────────────┘                      │
└─────────────────────────────────────────────────────────────────────┘
                                  │ HTTP REST
                                  ▼
┌─────────────────────────────────────────────────────────────────────┐
│               ProjectReboundMainDLL.dll (C++ 注入载荷)                │
│                                                                      │
│  ┌─────────────────┐  ┌──────────────────┐  ┌───────────────────┐  │
│  │ Hooking/         │  │ Loadout/          │  │ Server/            │  │
│  │                 │  │                  │  │                   │  │
│  │ HookCore        │  │ LoadoutManager   │  │ LibReplicate      │  │
│  │ ProcessEvent    │  │ Serializer       │  │ RoundManager      │  │
│  │ TickFlush       │  │ MetaserverClient │  │ LateJoin          │  │
│  │ EnginePatches   │  │ Application      │  │ PlayerNaming      │  │
│  │ ClientHooks     │  │ Showroom         │  │ NetDriverAccess   │  │
│  └─────────────────┘  └──────────────────┘  └───────────────────┘  │
│                                                                      │
│  工具: safetyhook (inline hook) | WinHTTP | nlohmann/json            │
└─────────────────────────────────────────────────────────────────────┘
                                  │ Frida attach
                                  ▼
┌─────────────────────────────────────────────────────────────────────┐
│               Frida 半自动逆向工具集 (tools/)                          │
│                                                                      │
│  Session 1-22: 从 MessageId 映射 → handler 捕获 → 指令跟踪            │
│                → 校验定位 → 内存扫描 → 根因定位                         │
│                                                                      │
│  辅助: analyze-logs.js (日志分析) | master_trace.js (一体机)           │
│        session4_ida_find_validation.py (IDA 静态分析)                 │
└─────────────────────────────────────────────────────────────────────┘
```

### 1.2 工作流阶段

| 阶段 | 工具 | 输入 | 产出 |
|------|------|------|------|
| **0. 流量捕获** | `proxy.js` | TCP :6969 流量 | `logs/binary/` 原始二进制 + `msgid_map.json` RPC 映射 |
| **1. 静态分析** | IDA Pro | `ProjectBoundary-Win64-Shipping.exe` | 函数 RVA 列表、交叉引用图 |
| **2. 动态探测** | Frida `session1-3` | 游戏进程 + RVA | MessageId↔RPC 映射、handler 地址 |
| **3. 指令跟踪** | Frida `session4-9` | handler 地址 | 调用链、ErrorCode 变色点 |
| **4. 校验定位** | Frida `session10-22` | 校验函数 RVA | 错误传播路径、内存布局 |
| **5. DLL 修复** | `ProjectReboundMainDLL` | 逆向发现 | ProcessEvent Hook + Loadout 桥接 |
| **6. 验证** | 游戏内测试 + `analyze-logs.js` | DLL 日志 | 修复确认 / 下一轮迭代 |

---

## 2. 部署方式

### 2.1 启动 Metaserver

```bash
# 1. 安装依赖
cd g:\wksp\boundaries\ProjectRebound\Metaserver
npm install

# 2. 启动 metaserver HTTP API (端口 8000)
node index.js

# 3. 另开终端，启动 proxy (端口 6969, 后端 6968)
node proxy.js
```

**proxy.js 环境变量：**
| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PROXY_PORT` | `6969` | 客户端连接端口 |
| `BACKEND_HOST` | `127.0.0.1` | 真实 metaserver 地址 |
| `BACKEND_PORT` | `6968` | 真实 metaserver 端口 |
| `LOG_DIR` | `logs/` | 日志输出目录 |

**index.js 环境变量：**
| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `8000` | HTTP API 端口 |
| `METASERVER_URL` | — | 外部 metaserver URL（可选） |

### 2.2 启动游戏（带 DLL 注入）

```bash
# 方式 A: 通过 Steam 启动 + 自动注入 (推荐)
# DLL 位于游戏目录，Steam 启动时自动加载

# 方式 B: 手动注入 (调试)
# 使用 Cheat Engine 或自定义注入器将 ProjectReboundMainDLL.dll 注入
# 命令行参数:
#   -server          → 启动服务端模式
#   -LogicServerURL= → 指定 metaserver URL (默认 http://127.0.0.1:8000)
```

**DLL 注入后的初始化顺序：**
1. `DllMain` → `MainThread()`
2. 等待 `UWorld` 可用
3. 打 `ServerModeFlag0/1` 补丁（强制服务端模式）
4. 安装所有引擎补丁 Hook (`IsServer=true`, `IsDedicatedServer=true`, `IsStandalone=false`)
5. 服务端路径：安装 `ProcessEvent` Hook → 创建 `LoadoutManager` → 初始化 `LibReplicate` → 启动心跳
6. 客户端路径：安装 `ProcessEvent` Hook → 创建调试控制台 → 启动命名管道 IPC

### 2.3 Frida 动态分析部署

```bash
# 前置条件：proxy.js 已运行（需要 msgid_map.json）

# 1. 确认游戏进程 PID
tasklist | findstr "ProjectBoundary"

# 2. 一体机模式（推荐）
frida -p <PID> -l tools\master_trace.js

# 3. 分步深度分析
frida -p <PID> -l tools\session1_probe_dispatch.js   # MessageId 映射
frida -p <PID> -l tools\session2_capture_handler.js  # Handler 捕获
frida -p <PID> -l tools\session3_handler_probe.js    # Handler 内部探测
frida -p <PID> -l tools\session4_find_validator.js   # 二分定位校验
frida -p <PID> -l tools\session5_pinpoint.js         # 精确定位 ErrorCode
frida -p <PID> -l tools\session16_force_pass.js      # 强制绕过校验
frida -p <PID> -l tools\session20_find_chars.js      # 堆扫描角色结构
frida -p <PID> -l tools\session22_map_all.js         # 全量内存映射

# 4. 日志分析
node tools\analyze-logs.js --latest           # 分析最近会话
node tools\analyze-logs.js --latest --llm     # + LLM 语义分析
```

### 2.4 IDA 静态分析部署

```
1. 打开 IDA Pro，加载 ProjectBoundary-Win64-Shipping.exe
2. 等待自动分析完成
3. 跳转到 handler RVA (如 0x9C48B0)
4. Alt+F7 → 运行 session4_ida_find_validation.py
5. 脚本自动：定位 protobuf 解码调用 → 找校验分支 → 输出 patch 建议
```

---

## 3. 已知函数地址全集

### 3.1 Metaserver RPC 派发链 (来自 IDA/Frida)

| 函数 | RVA | 运行时地址公式 | 作用 |
|------|-----|---------------|------|
| **响应派发入口** | `0x9C4780` | `base + 0x9C4780` | **sub_9C4780** — r8=MessageId，查哈希表派发 handler。所有 RPC 响应的入口 |
| **哈希表查找** | `0x99E820` | `base + 0x99E820` | **sub_99E820** — 按 MessageId 搜索回调槽位，内部通过 vtable[0x108] 调用 handler |
| **GetPlayerArchiveV2 handler** | `0x9C48B0` | `base + 0x9C48B0` | rcx+0x0C = ErrorCode（4 = 失败） |
| **校验函数** | `0x9BF020` | `base + 0x9BF020` | **sub_9BF020** — 检查 `*(a4+796) != 2 → return 0`。仅 UpdateRoleArchiveV2 走此路径 |
| **响应处理器(部分RPC)** | `0x9B99A0` | `base + 0x9B99A0` | **sub_9B99A0** — 调用 sub_9BF020 校验。间接调用（无 xrefs） |
| **GetPlayerArchiveV2 处理器** | `0xA49E10` | `base + 0xA49E10` | **sub_A49E10** — 遍历角色，查缓存，复制条目 |
| **条目复制器** | `0xA3E770` | `base + 0xA3E770` | **sub_A3E770** — 通过 sub_887BA0 做 memcpy |
| **结构体复制** | `0x887BA0` | `base + 0x887BA0` | **sub_887BA0** — memcpy 式操作，传播 ErrorCode=4 |
| **ErrorCode 工厂(type=12)** | `0xBA0FC0` | `base + 0xBA0FC0` | **sub_BA0FC0** — 分配 16 字节，设 `[+0]=vtable, [+8]=12, [+0xC]=4` |
| **ErrorCode 工厂(type=8)** | `0xBB4B60` | `base + 0xBB4B60` | **sub_BB4B60** — 同上，type=8, ErrorCode=4 |
| **槽位回收** | `0x8BD3B0` | `base + 0x8BD3B0` | **sub_8BD3B0** — 位图标记已消费槽位 |

### 3.2 UpdateRoleArchiveV2 注册链

| 函数 | IDA 地址 | 作用 |
|------|----------|------|
| RPC 注册入口 | `0x1406209C0` | 注册 UpdateRoleArchiveV2 路径到查找表 |
| RPC 注册入口 | `0x14061E550` | 备用注册入口 |
| RPC 注册入口 | `0x1406249C0` | 备用注册入口 |
| Handler 包装器 | `0x1440EFFA0` | 调 sub_1418E5490，走虚函数派发 |
| Handler 包装器 | `0x1440EF580` | 调 sub_1418E5490 |
| Handler 包装器 | `0x1440F13F0` | 调 sub_1418E5490 |
| 派发器 | `0x1418E5490` | `(*(vtable[6]))(obj, arg)` 虚函数调用 |
| 初始化 | `0x1418E55A0` → `0x1419E1E50` → `0x1418CF000` | 创建 0x8B0 字节对象 |

### 3.3 DLL Hook 偏移 (GameOffsets.h)

#### 内存/引擎标记

| 常量 | RVA | 说明 |
|------|-----|------|
| `FMemoryInit` | `0x18F4350` | 内存分配器初始化 |
| `ServerModeFlag0` | `0x5CE2404` | 服务端模式标记 0 (1 字节) |
| `ServerModeFlag1` | `0x5CE2405` | 服务端模式标记 1 (1 字节) |

#### Hook 目标

| Hook 名 | RVA | 类别 | 操作 |
|---------|-----|------|------|
| `NotifyActorDestroyed` | `0x33403E0` | 服务端 | 追踪 actor 销毁 |
| `NotifyAcceptingConnection` | `0x36CDC90` | 服务端 | 始终返回 1（接受所有连接） |
| `NotifyControlMessage` | `0x36CDCE0` | 服务端 | 观察 NetDriver 控制消息 |
| `TickFlush` | `0x33E05F0` | 服务端 | **主 tick**：回合状态机、复制批处理、late-join |
| `ProcessEvent` | `0x1BCBE40` | 双端 | **核心 Hook**：拦截所有 RPC/蓝图函数调用 |
| `ObjectNeedsLoad` | `0x1B7B710` | 服务端 | 始终返回 1（强制加载） |
| `ActorNeedsLoad` | `0x3124E70` | 服务端 | 始终返回 1（强制加载） |
| `OnFireWeapon` | `0x1610500` | 服务端 | 返回地址守卫：仅允许特定调用者 |
| `PostLogin` | `0x32903B0` | 服务端 | 玩家加入追踪、late-join 检测 |
| `IsDedicatedServer` | `0x33266F0` | 服务端 | 始终返回 true |
| `IsServer` | `0x3326C60` | 服务端 | 始终返回 true |
| `IsStandalone` | `0x3326CE0` | 服务端 | 始终返回 false |
| `ClientDeathCrash` | `0x16ABE10` | 客户端 | NOP — 返回 0 防止崩溃 |

#### 返回地址守卫

| 常量 | RVA | 说明 |
|------|-----|------|
| `OnFireWeaponAllowedCaller` | `0x1608B31` | 唯一允许调用 OnFireWeapon 的返回地址 |

#### LibReplicate 复制层

| 函数 | RVA | 说明 |
|------|-----|------|
| `InitListen` | `0x91AEB0` | 初始化监听 |
| `CreateChannel` | `0x33A66D0` | 创建 actor 通道 |
| `SetChannelActor` | `0x31F44F0` | 设置通道 actor |
| `ReplicateActor` | `0x31F0070` | 复制 actor 属性 |
| `FMemoryMalloc` | `0x18F1810` | 内存分配 |
| `FMemoryFree` | `0x18E5490` | 内存释放 |
| `OrigNotifyControlMessage` | `0x36CDCE0` | 原始控制消息通知 |
| `CreateNamedNetDriver` | `0x366ADB0` | 创建命名 NetDriver |
| `ActorChannelClose` | `0x31DA270` | 关闭 actor 通道 |
| `SetWorld` | `0x33DF330` | 设置 UWorld |
| `CallPreReplication` | `0x2FEFBD0` | 调用 PreReplication |
| `SendClientAdjustment` | `0x3506320` | 发送客户端修正 |

### 3.4 已知 MessageId ↔ RPC 映射

| MessageId | RPC 路径 | 类型 |
|-----------|----------|------|
| 2 | `/assets.Assets/GetPlayerArchiveV2` | 响应 |
| 42 | `/assets.Assets/GetPlayerArchiveV2` | (另一条路径) |
| 49 | `/assets.Assets/UpdateRoleArchiveV2` | 请求 |
| 58 | `/assets.Assets/UpdateRoleArchiveV2` | 响应 |
| — | `/assets.Assets/UpdateWeaponArchiveV2` | ✅ 正常 |
| — | `/chat.chat/TextFilter` | ✅ 已修复 (空响应) |
| — | `/party.party/Get` | ✅ 已修复 (空响应) |

### 3.5 关键数据结构偏移

#### APBDisplayCharacter（显示角色）

| Offset | 成员 | 类型/大小 |
|--------|------|-----------|
| `0x0370` | `DisplayLeftPylon` | `APBDisplayPod*` |
| `0x0378` | `DisplayRightPylon` | `APBDisplayPod*` |
| `0x0380` | `DisplayFirstWeapon` | `APBDisplayWeapon*` |
| `0x0388` | `DisplaySecondWeapon` | `APBDisplayWeapon*` |
| `0x0390` | `DisplayMeleeWeapon` | `APBDisplayMeleeWeapon*` |
| `0x0398` | `DisplayMobilityModule` | `APBDisplayMobilityModule*` |
| `0x03A0` | `RoleConfig` | `FPBRoleNetworkConfig` (0xF8 字节) |

#### FPBRoleNetworkConfig（0xF8 字节）

| Offset | 成员 | 大小 |
|--------|------|------|
| `0x00` | `CharacterID` | — |
| `0x08` | `CharacterData` (角色皮肤) | 0x28 |
| `0x30` | `FirstWeaponPartData` (主武器) | 0x38 |
| `0x68` | `SecondWeaponPartData` (副武器) | 0x38 |
| `0xA0` | `MeleeWeaponData` (近战) | 0x10 |
| `0xB0` | `LeftLauncherData` (左挂载) | 0x10 |
| `0xC0` | `RightLauncherData` (右挂载) | 0x10 |
| `0xD0` | `MobilityModuleData` (机动模块) | 0x08 |
| `0xD8` | `InventoryData` (库存) | 0x20 |

#### FPBWeaponNetworkConfig（0x38 字节）

| Offset | 成员 |
|--------|------|
| `0x00` | `WeaponPartSlotTypeArray` |
| `0x10` | `WeaponPartConfigs` |
| `0x20` | `OrnamentID` |
| `0x28` | `WeaponID` |
| `0x30` | `WeaponClassID` |

#### ErrorCode 结构体（运行时）

| Offset | 成员 | 值 |
|--------|------|----|
| `0x00` | vtable 指针 | — |
| `0x08` | Type | 8 或 12 |
| `0x0C` | ErrorCode | 4 = `UnknowError` |

---

## 4. 关键函数操作详解

### 4.1 sub_9C4780 — 响应派发入口

```
签名: sub_9C4780(rcx=this, rdx=responseWrapper, r8=MessageId, r9=0)
操作:
  1. 进入临界段 (EnterCriticalSection)
  2. 校验 MessageId 范围
  3. +0xFD: call sub_99E820(rcx=hashTable, rdx=MessageId)
       → 返回 handler 函数指针 (vtable[0x108] 调用)
  4. 调用 handler(responseWrapper)
  5. 槽位回收 (sub_8BD3B0)
  6. 离开临界段
```

### 4.2 sub_99E820 — 哈希表查找

```
签名: sub_99E820(rcx=hashTable, rdx=MessageId)
操作:
  1. 按 MessageId 计算哈希索引
  2. 遍历槽位链表
  3. 匹配 MessageId → 找到 handler 回调
  4. +0xE1: call [rip+disp] → 间接调用 handler
  5. 返回 handler 执行结果
```

### 4.3 sub_9C48B0 — GetPlayerArchiveV2 Handler

```
签名: sub_9C48B0(rcx=responseStruct, ...)
操作:
  - rcx+0x0C = ErrorCode (进入时已预设为 4)
  - 此 handler 自身不设 ErrorCode
  - 调用 sub_A49E10 做实际的角色数据处理
  - ErrorCode=4 来自全局模板复制 (xmmword_41CB330)
```

### 4.4 sub_9BF020 — 校验函数

```
签名: sub_9BF020(a1, a2, a3, a4)
操作:
  1. 检查 *(a4 + 796) != 2 → return 0 (失败)
  2. *(a4 + 796) == 2 → return 1 (通过)
  
注意: GetPlayerArchiveV2 不经过此路径!
      仅 UpdateRoleArchiveV2 等 RPC 走 sub_9B99A0 → sub_9BF020
```

### 4.5 sub_A49E10 — GetPlayerArchiveV2 响应处理

```
签名: sub_A49E10(responseData)
操作:
  1. 遍历 PlayerRoleData[]（6 角色 × 6 槽位）
  2. 对每个角色:
     a. 查缓存 (v7+72 vs v7+116)
     b. 缓存命中 → sub_A3E770 → sub_887BA0 (复制已有条目)
     c. 缓存缺失 → sub_887BA0(全局模板) (从零创建)
     d. vtable[8] 回调通知处理完成
  3. ErrorCode 结构体通过 sub_887BA0 memcpy 传播
```

### 4.6 ProjectReboundMainDLL ProcessEvent Hook

```
Hook: ProcessEvent (RVA: 0x1BCBE40)

PRE 阶段:
  1. 线程本地缓存: FCachedProcessEventInfo (避免重复字符串比较)
  2. 分类函数名 → EServerProcessEventKind / EClientProcessEventKind
  3. 服务端:
     - QuickRespawn, ServerRestartPlayer → 快速重生逻辑
     - CanPlayerSelectRole, CanSelectRole → 强制返回 true
     - ServerConfirmRoleSelection → LoadoutManager 介入
     - ClientBeKilled, MatchHasEnded → 回合管理
  4. 客户端:
     - MainMenuConstruct → 自动登录 + LoadoutManager 初始化
     - OnEquipCharacterSlotComplete → 吞 ErrorCode=4（设为 0）
     - K2_OnEquipComplete → 吞 ErrorCode=4
  5. LoadoutManager::OnProcessEventPre → 拦截参数，注入 snapshot 值

ORIGINAL:
  ProcessEvent.call(Object, Function, Parms)

POST 阶段:
  1. LoadoutManager::OnProcessEventPost → UI widget 修正
  2. 查询拦截: GetWeaponNetworkConfig, GetChildByCharacterSlot 等 → 注入返回值
```

### 4.7 LoadoutManager 核心操作（客户端）

```
操作序列:
  1. MainMenuConstruct → LoadoutFix_FetchAndLog()
     → GetPlayerId() (PlayerState→PlatformUniqueIDJsonString)
     → MetaserverClient::GetPlayerLoadout(id)
     → LoadoutSerializer::NormalizeLoadoutFormat(json)
     → 缓存到 gLoadedSnapshot

  2. OnClientProcessEventPre (拦截原生 inventory 刷新):
     → 读取 gLoadedSnapshot 对应角色/槽位
     → 覆盖 ProcessEvent 的 Parms (参数注入)

  3. Widget 修正:
     → 扫描 UPBItemCSTM_Base widgets
     → bIsEquipped (offset 0x269) ← 根据 snapshot 修正
     → EquippedItemID / PreviewInventoryID ← 根据 snapshot 修正

  4. 装备提交:
     → 用户点击装备 → 更新本地 snapshot
     → MetaserverClient::PutPlayerLoadout(id, snapshot)
     → RefreshItem() 重绘所有 widget (ScopedClientProcessEventSuppression)

  5. PreviewItem:
     → 创建临时 snapshot (当前 snapshot + 预览物品)
     → 应用到 showroom widget
     → 鼠标离开 → 恢复原始 snapshot
```

### 4.8 TickFlushHook 操作（服务端）

```
操作序列 (每帧):
  1. NoteServerGameTick() → 推进回合计时器
  2. CollectTickReplicationBatch()
     → 遍历所有 Levels
     → 收集 bReplicates=true && RemoteRole!=ROLE_None 的 actors
     → 收集 PlayerControllers
  3. CallFromTickFlushHook(actors, players, connections)
     → LibReplicate::ReplicateActor() 批量复制
  4. LateJoinManager::Tick()
     → 驱动 PendingRoleSelection → RoleConfirmed → Spawned 状态机
     → 超时处理 (ROLE_SELECTION_TIMEOUT=30s)
  5. PlayerNaming::Tick()
     → 从 pending queue 取解析完成的 Steam 名称
     → 写入 Scoreboard
  6. LoadoutManager::TickServer()
     → 轮询 pending snapshots
     → PostSpawnApply() 应用到 gameplay 实体
```

---

## 5. JS 脚本设计指南

### 5.1 Frida 脚本规范

#### 5.1.1 模块自动检测（标准模板）

```javascript
// 每个脚本都应该自动检测游戏模块
const BASE = (() => {
    const known = [
        "ProjectBoundarySteam-Win64-Shipping.exe",
        "ProjectBoundary-Win64-Shipping.exe",
        "ProjectBoundarySteam.exe",
    ];
    for (const n of known) {
        try { return Process.getModuleByName(n).base; } catch(_) {}
    }
    // Fallback: 找最大的 .exe
    let best = null;
    for (const m of Process.enumerateModules()) {
        if (!m.name.endsWith('.exe')) continue;
        if (!best || m.size > best.size) best = m;
    }
    return best ? best.base : null;
})();
```

#### 5.1.2 RVA 寻址模式

```javascript
// 所有地址 = BASE + RVA
const DISPATCH_RVA = 0x9C4780;  // sub_9C4780
const dispatchFunc = BASE.add(DISPATCH_RVA);

// 16进制输出用 NativePointer.toString()
function hex(n) {
    return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16).toUpperCase();
}
```

#### 5.1.3 Interceptor.attach 模式

```javascript
// 标准格式：onEnter 收集 + onLeave 分析
Interceptor.attach(targetFunc, {
    onEnter(args) {
        this.ctx = this.context;     // 保存寄存器上下文
        this.ret = this.returnAddress;
        // 收集参数、寄存器值
    },
    onLeave(retval) {
        // 对比变化、dump 分析结果
    }
});
```

#### 5.1.4 指令扫描模式

```javascript
// 扫描函数内的 CALL/JMP 指令
function scanCalls(funcAddr, maxSize) {
    const calls = [];
    let addr = funcAddr;
    const end = funcAddr.add(maxSize || 0x800);

    while (addr.compare(end) < 0) {
        try {
            const insn = Instruction.parse(addr);
            if (insn.mnemonic === 'call') {
                const off = addr.sub(funcAddr).toInt32();
                let target = null;
                if (insn.operands[0]?.type === 'imm') {
                    target = ptr(insn.operands[0].value);
                }
                calls.push({ offset: off, addr, target, mnemonic: insn.mnemonic });
            }
            addr = insn.next;
        } catch (_) {
            addr = addr.add(1);
        }
    }
    return calls;
}
```

#### 5.1.5 内存安全读取

```javascript
// 始终 try/catch 保护内存读取
function safeRead(addr, size) {
    try { return addr.readByteArray(size); } catch (_) { return null; }
}

function safeS32(addr, off) {
    try { return addr.add(off).readS32(); } catch (_) { return null; }
}

function safePtr(addr, off) {
    try { return addr.add(off).readPointer(); } catch (_) { return null; }
}
```

#### 5.1.6 Stalker 指令跟踪模式

```javascript
// 谨慎使用 Stalker（高性能开销）
// 仅在需要指令级可见性时使用 (session3, session14)
Stalker.follow(threadId, {
    transform(iterator) {
        let insn;
        while ((insn = iterator.next()) !== null) {
            // 只跟踪 CALL 指令以减少开销
            if (insn.mnemonic === 'call') {
                iterator.putCallout((ctx) => {
                    const target = ctx.rax;  // 间接调用目标
                    const from = ctx.rip;
                    // 记录调用关系
                });
            }
            iterator.keep();
        }
    }
});
```

#### 5.1.7 代理映射集成

```javascript
// 从 proxy.js 的 msgid_map.json 读取 RPC 路径映射
const MSGID_MAP_PATH = 'g:/wksp/boundaries/ProjectRebound/Metaserver/logs/msgid_map.json';

function loadProxyMap() {
    try {
        const f = new File(MSGID_MAP_PATH, 'r');
        const raw = f.read(); f.close();
        const obj = JSON.parse(raw);
        for (const [k, v] of Object.entries(obj)) {
            msgIdToRpc[parseInt(k)] = v;
        }
    } catch (_) {}
}
```

#### 5.1.8 触发控制模式

```javascript
// 限制日志量：前 N 次详细记录，后续只计数
const MAX_DETAIL_LOG = 5;
let hitCount = 0;

// 在 onEnter 中:
if (hitCount < MAX_DETAIL_LOG) {
    console.log(`[DETAIL] ...`);
}
hitCount++;

// 或用条件断点
if (this.msgId === TARGET_MSG_ID) {
    // 详细日志
}
```

### 5.2 Node.js 工具规范

#### 5.2.1 日志分析框架

```javascript
// analyze-logs.js 的风格模式
const fs = require('fs');
const path = require('path');

const BASE_DIR = path.join(__dirname, '..');
const LOG_DIR = path.join(BASE_DIR, 'logs');

// 自动发现最近的会话
function findLatestSession() {
    const dirs = fs.readdirSync(LOG_DIR)
        .filter(d => d.startsWith('proxy-'))
        .sort()
        .reverse();
    return dirs[0] || null;
}

// CLI 参数解析
const args = process.argv.slice(2);
const useLLM = args.includes('--llm');
const latest = args.includes('--latest');
```

#### 5.2.2 Protobuf 解码模式

```javascript
const protobuf = require('protobufjs');

// 从目录加载 proto 定义
function loadAllProtos() {
    const root = new protobuf.Root();
    const protoDir = path.join(__dirname, 'game', 'proto');
    // 递归加载 Request/ 和 Response/ 目录下的 .proto
    for (const sub of ['Request', 'Response']) {
        const dir = path.join(protoDir, sub);
        if (!fs.existsSync(dir)) continue;
        for (const file of fs.readdirSync(dir).filter(f => f.endsWith('.proto'))) {
            root.loadSync(path.join(dir, file));
        }
    }
    return root;
}

// 按消息名查找类型
function findMessageType(root, msgName) {
    try {
        const ns = root.lookup('ProjectBoundary');
        return ns.lookupType(msgName);
    } catch (_) { return null; }
}
```

#### 5.2.3 目标函数签名设计模式

```javascript
// 所有逆向 session 脚本都应遵循：
//   1. 文档字符串在文件顶部
//   2. 清晰的命名: session<N>_<目的>.js
//   3. TARGET_RVA 作为常量
//   4. 限制输出 (MAX_HITS, MAX_LOG)
//   5. 结构化结果导出

const TARGET_RVA = 0x9C4780;
const TARGET_MSG_ID = 2;
const MAX_HITS = 5;
const MAX_INTERNAL_LOG = 3;
```

### 5.3 REPL 命令设计模式

```javascript
// 每个脚本应有标准 REPL 命令集:
//   start()   — 开始跟踪
//   dump()    — 显示收集的数据
//   status()  — 当前状态
//   export()  — 导出结果

// 全局状态跟踪
let started = false;
const hits = {};
const hitList = [];

function start() {
    if (started) { console.log("[*] Already started."); return; }
    started = true;
    console.log("[+] Tracking active.");
}

function dump() {
    console.log(`\n=== Results (${hitList.length} entries) ===`);
    for (const entry of hitList) {
        console.log(`  ${JSON.stringify(entry)}`);
    }
}

function exportData() {
    const output = JSON.stringify({ hits: hitList }, null, 2);
    // 写文件...
}

// 暴露到 REPL
global.start = start;
global.dump = dump;
global.exportData = exportData;
```

### 5.4 常见陷阱与解决方案

| 陷阱 | 解决方案 |
|------|------|
| ASLR 基址变化 | 始终运行时计算 `BASE + RVA`，不硬编码绝对地址 |
| JS Number 精度丢失（64位地址） | 用 `NativePointer` / `ptr()` 不用 `parseInt()` |
| 内存读取崩溃 | 始终 `try/catch` 包裹 `read*()` |
| Stalker 性能开销过大 | 只跟踪 CALL/JMP，不跟踪每条指令 |
| Frida 脚本中不能使用 `require()` | 内联所有代码，用 REPL 全局变量共享状态 |
| Interceptor.attach 过多导致冻结 | 限制 hook 数量；先扫描确定目标再安装 |
| 间接调用无法解析目标 | 记录 `ctx.rax`（x64 thiscall 返回值）= 调用目标 |
| 线程安全 | Frida Interceptor 是线程安全的，但全局变量需要原子操作 |
| 进程退出时脚本未清理 | 使用 `Process.setExceptionHandler()` 捕获退出 |
| 多线程并发 | 使用 `this.threadId` 区分线程 |

---

## 附录

### A. 文件索引

| 文件 | 位置 | 说明 |
|------|------|------|
| `ReverseEngineeringGuide.md` | 本文件 | 完整指南 |
| `tools/README.md` | `ReverseEngineering/tools/` | Frida 工具快速入门 |
| `tools/session1-22_*.js` | `ReverseEngineering/tools/` | 分步逆向脚本 |
| `tools/master_trace.js` | `ReverseEngineering/tools/` | 一体机：自动 MessageId + handler |
| `tools/analyze-logs.js` | `ReverseEngineering/tools/` | 日志分析 + proto 修正 |
| `tools/session4_ida_find_validation.py` | `ReverseEngineering/tools/` | IDA Python 静态分析 |
| `MetaserverLoadoutResponse.md` | `Docs/Raw/` | RPC 逆向详细笔记 |
| `GameOffsets.h` | `ProjectReboundMainDLL/Core/` | 所有 DLL Hook RVA 偏移 |

### B. 外部工具版本要求

| 工具 | 版本 | 用途 |
|------|------|------|
| Node.js | ≥18 | metaserver (proxy.js, index.js) |
| Frida | 17.10 | 动态 hook / Stalker |
| IDA Pro | ≥8.x | 静态反编译 |
| x64dbg | 最新 | 硬件断点调试 (不稳定) |
| Cheat Engine | ≥7.5 | 内存监控、硬件写断点 |
| Visual Studio | 2022 | ProjectReboundMainDLL 编译 |
