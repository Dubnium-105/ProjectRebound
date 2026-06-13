# MetaServer 换装 RPC 逆向文档

## 问题

客户端军械库换装（武器/挂载）报"未知错误"（`EPBEquipErrorCode::UnknowError = 4`）。服务端 `UpdateRoleArchiveV2` 响应返回 `{StatusCode: 0}`，proto 字节完全合法，但 Native 应用层校验拒绝。

**同时**：军械库初始化后所有角色显示默认武器，`GetPlayerArchiveV2` 返回了正确数据但被 Native 拒绝。这是同一根因的两个表现。

换武器配件（`UpdateWeaponArchiveV2`）正常，皮肤/挂饰正常。

---

## RPC 请求/响应字节

**Request** `UpdateRoleArchiveV2`（换 Sniper 主武器为例）：
```
08 02 12 06 53 6e 69 70 65 72 1a 0f 53 4e 49 50 45 52 5f 52 55 2d 4d 4f 53 49 4e
```
字段：`Operation=2`, `RoleId="Sniper"`, `ItemId="SNIPER_RU-MOSIN"`

**Response** (meta server → client)：
```
08 3a 12 22 /assets.Assets/UpdateRoleArchiveV2 18 00 22 02 08 00
```
- ResponseWrapper: `MessageId=58`, `ErrorCode=0`
- Inner message: `08 00` = `StatusCode=0`

**Worked comparison** `UpdateWeaponArchiveV2` response 格式完全相同，也是 `08 00`。

---

## x64dbg / IDA 链路追踪成果

### 已验证

| 事实 | 证据 |
|------|------|
| 响应 proto 解码通过 | x64dbg 断点 `call 7FF6A314FA20` 返回 `RAX=1` |
| 响应不查 RPC 路径字符串 | `Alt+T` 搜索 `StatusCode` 无编译器引用 |
| 请求/响应用两套派发 | 请求走 RPC 路径查表，响应走 MessageId 查哈希表 |
| 响应反序列化后 Native 额外校验失败 | RAX=1 通过 protobuf 解码，但应用层设 ErrorCode=4 |
| `OnEquipCharacterSlotDelegate` 是 Delegate Broadcast | 不经过 ProcessEvent，我们无法 Hook |

### 核心调用链（x64dbg 运行时地址）

```
0x7FF6F37D9413  lea rax, "UpdateRoleArchiveV2"     ← RPC 注册表查询
0x7FF6F37CA668  返回到调用者，开始字符串匹配循环
0x7FF6F37CA6C2  call sub_1407CFA20                 ← 按 RPCPath 查表
0x7FF6F37CA731  call sub_1407D4780                 ← 反序列化后的回调分派 ← IDA: 0x1409C4780
0x7FF6A3154780  call sub_1409C4780                 ← 将响应排入 MessageId 哈希表
```

### IDA 静态地址映射

| 函数 | IDA 地址 | 作用 |
|------|----------|------|
| RPC 注册 `UpdateRoleArchiveV2` | `0x1406209C0` / `0x14061E550` / `0x1406249C0` | 三个 handler，注册 RPC 路径到查找表 |
| Handler 包装器 | `0x1440EFFA0` / `0x1440EF580` / `0x1440F13F0` | 都调 `sub_1418E5490`，走虚函数派发 |
| 派发器 | `sub_1418E5490` | `(*(vtable[6]))(obj, arg)` 虚函数调用 |
| 初始化 | `sub_1418E55A0` → `sub_1419E1E50` → `sub_1418CF000` | 创建 0x8B0 字节对象 |
| 响应排入哈希表 | `sub_1409C4780`（IDA: `0x1409C4780`） | MessageId 匹配，临界段保护 |
| 哈希表查找 | `sub_14099E820` | 按 MessageId 找匹配槽位 |
| 槽位回收 | `sub_1408BD3B0` | 位图标记已消费槽位 |

### 踩坑记录

1. **vtable[6] 追偏了** — `off_144B63040` 的 vtable[6] 是 `FMallocBinned2::Malloc`
2. **响应不查字符串** — 响应按 MessageId 派发，不查字段名字符串
3. **同一个断点被请求和响应共用** — 注册和响应都走字符串查找
4. **ASLR 基址变化** — 每次重启基址不同，跨会话地址需要重新计算
5. **`Pad_D8` 是垃圾数据** — `UPBCustomizeManager` 的 Pad 区域不是 TMap

---

## 军械库完整流程

### 初始化

```
1. 玩家点"军械库"按钮
     → BP 打开 UMG_CustomizeWidget

2. ShowRoom 状态机启动 → ShowRoomManager::SpawnCharacters()
     → 遍历所有角色 ID
     → 每个角色调 UPBDisplayActorLibrary::SpawnDisplayCharacter(World, Class, Config, bAttach)
     → Config 来源: GetPlayerArchiveV2 解析后的缓存
     → Native 拒绝了响应 → Config = CDO 默认值 → 所有角色显示默认装备 ← 根因

3. GetPlayerArchiveV2 响应回来
     → Native 解析 PlayerRoleData[]（6 角色 × 6 槽位）
     → [?] 存到某缓存（可能是 UPBCustomizeManager 内部 Pad 区域或 ShowRoomManager 状态机内）
     → [?] 写到 APBDisplayCharacter::RoleConfig（FPBRoleNetworkConfig at offset 0x03A0）

4. 对每个角色的每个槽位，SpawnInventory/SpawnWeapon/SpawnPod
     → 创建显示 actor，attach 到角色骨骼

5. 军械库界面显示
```

### 换配件（UpdateWeaponArchiveV2）— 正常

```
用户点配件按钮
  → UPBItemCSTM_Base::PreviewItem() [native]
     → ShowRoom::SpawnWeaponPart() → 3D 配件模型刷新 ✅
  → 用户点确认 → EquipItem() → 发 RPC → 服务器返回
  → Native 解码 → 判定成功
  → 广播 OnEquipWeaponSlotDelegate(ErrorCode=0, ...)
     ├→ Native 监听器: SpawnWeaponPart() → 3D 模型刷新 ✅
     └→ BP 回调: OnEquipWeaponSlotComplete(ErrorCode=0) → UI 更新
  → K2_OnEquipComplete(ErrorCode=0) → "已装备" + 成功音效
```

### 换武器/挂载（UpdateRoleArchiveV2）— 坏掉

```
用户点武器按钮
  → PreviewItem() → SpawnInventory(charID, itemID)
     → 从 RoleConfig 读 WeaponID → 因为 RoleConfig 是默认值 → 预览也是默认模型 ← 🔴
  → 用户点确认 → EquipItem() → 发 RPC → 服务器返回
  → Native 解码通过（RAX=1）
  → [?] 额外校验 → 判定失败 → ErrorCode=4
  → 广播 OnEquipCharacterSlotDelegate(ErrorCode=4, ...) ← 这是 TMulticastInlineDelegate::Broadcast()
     ├→ Native 监听器: ErrorCode!=0 → 跳过 SpawnInventory ← 不经过 ProcessEvent，我们 Hook 不到
     └→ BP 监听器: ErrorCode!=0 → 什么都不做
  → OnEquipCharacterSlotComplete(ErrorCode=4, ...) ← 我们吞成 0（走 ProcessEvent）
  → K2_OnEquipComplete(ErrorCode=4, ...) ← 我们吞成 0
  → "已装备"文字显示 ← RefreshItem() 独立于 equip 回调，从 CustomizeManager 读 bIsEquipped
```

### "已装备"标签机制

不走 `EquipItem` 的成功/失败。走 `RefreshItem()`——它从 `UPBCustomizeManager` 读取数据，设 `bIsEquipped`（`UPBItemCSTM_Base` offset `0x269`）。这就是为什么吞了 ErrorCode 后能看到"已装备"——`RefreshItem` 独立于 equip 回调。

### PreviewItem 机制

`PreviewItem()` → ShowRoom 状态机 → `SpawnInventory(charID, itemID)` → 从 `RoleConfig` 读 WeaponID → 创建显示 actor。因为 `RoleConfig` 全是默认值（GetPlayerArchiveV2 被拒），预览阶段就已经是默认装备。

### ProcessEvent vs Delegate Broadcast

| 方式 | 我们能否 Hook | 例子 |
|------|:--:|------|
| ProcessEvent | ✅ | `K2_OnEquipComplete`, `OnEquipCharacterSlotComplete` |
| Delegate Broadcast | ❌ | `OnEquipCharacterSlotDelegate` → 跳过 `SpawnInventory` |

`TMulticastInlineDelegate::Broadcast()` 是纯 C++ 模板函数，不走 `UObject::ProcessEvent`。我们 Hook 不到，所以吞不了 SpawnInventory 被跳过的决策。

---

## 关键数据结构

### APBDisplayCharacter — 显示角色
| Offset | 成员 | 作用 |
|--------|------|------|
| 0x03A0 | `RoleConfig` | `FPBRoleNetworkConfig`（大小 0xF8）— **解析后的角色装备配置** |
| 0x0380 | `DisplayFirstWeapon` | `APBDisplayWeapon*` |
| 0x0388 | `DisplaySecondWeapon` | `APBDisplayWeapon*` |
| 0x0370 | `DisplayLeftPylon` | `APBDisplayPod*` |
| 0x0378 | `DisplayRightPylon` | `APBDisplayPod*` |
| 0x0390 | `DisplayMeleeWeapon` | `APBDisplayMeleeWeapon*` |
| 0x0398 | `DisplayMobilityModule` | `APBDisplayMobilityModule*` |

### FPBRoleNetworkConfig — 角色装备配置（大小 0xF8）
| Offset | 成员 | 对应 archive 字段 |
|--------|------|-----------------|
| 0x00 | `CharacterID` | `RoleID` |
| 0x08 | `CharacterData` | 角色皮肤 (0x28) |
| 0x30 | `FirstWeaponPartData` | `PrimaryWeapon` (0x38) |
| 0x68 | `SecondWeaponPartData` | `SecondWeapon` (0x38) |
| 0xA0 | `MeleeWeaponData` | `MeleeWeapon` (0x10) |
| 0xB0 | `LeftLauncherData` | `LeftPylon` (0x10) |
| 0xC0 | `RightLauncherData` | `RightPylon` (0x10) |
| 0xD0 | `MobilityModuleData` | `MobilityModule` (0x08) |
| 0xD8 | `InventoryData` | 库存配置 (0x20) |

### FPBWeaponNetworkConfig — 武器配置（大小 0x38）
| Offset | 成员 |
|--------|------|
| 0x00 | `WeaponPartSlotTypeArray` |
| 0x10 | `WeaponPartConfigs` |
| 0x20 | `OrnamentID` — **映射 archive 的 OrnamentId** |
| 0x28 | `WeaponID` — **武器 ID** |
| 0x30 | `WeaponClassID` |

### FPBLauncherNetworkConfig — 挂载配置（大小 0x10）
| Offset | 成员 |
|--------|------|
| 0x00 | `ID` — **挂载 ID** |
| 0x08 | `SkinID` |

### UPBItemCSTM_Base — 物品 widget 基类
| Offset | 成员 | 类型 |
|--------|------|------|
| 0x260 | `ItemId` | `FName` |
| 0x269 | `bIsEquipped` | `bool` — "已装备"标签的数据源 |

### UPBPanelCSTM_EditCharacterSlot — 角色装备槽位面板
| Offset | 成员 |
|--------|------|
| 0x358 | `EditingCharacterSlot` |
| 0x35C | `EquippedInventoryID` — 服务器已装备 |
| 0x364 | `PreviewInventoryID` — 当前预览 |

### UPBShowRoomManager — 展示间管理器
| Offset | 成员 |
|--------|------|
| 0x38 | `ShowRoomStateMachine` (`USMInstance*`) |
| 0x110 | `CacheActorArray` |
| 0x120 | `CacheActorMap` (`TMap<FName, APBDisplayActor*>`) |
| 0x1D0 | `ViewTargetID` |

---

## LoadoutFix.cpp — 当前 DLL 修复

### 架构

```
DLL (Payload)                                   Metaserver
─────────                                       ──────────

MainMenuConstruct → LoadoutFix_FetchAndLog()
  → GetPlayerId() (硬编码 76561198950613585)
  → HTTP GET /api/loadout/{id}
  → JSON 解析 → 日志输

F8 热键 → LoadoutFix_ForceRefresh()
  → HTTP GET JSON
  → 主线程 FlushRefresh
  → 写 RoleConfig + K2_RefreshDisplayActor

OnEquipCharacterSlotComplete(ErrorCode=4)  → 吞成 0
K2_OnEquipComplete(ErrorCode=4)            → 吞成 0
```

### 已验证

| 功能 | 状态 |
|------|:--:|
| HTTP GET 配装 JSON | ✅ |
| FName 字符串转换 | ❌ **SDK AppendString 偏移量 `0x019D82B0` 损坏——调用即崩溃。** `NameToString` 对所有有效 FName 均返回 `"None"`。 |
| 写 RoleConfig | ❓ 偏移量 `0x03A0` 来自 SDK，可能错误。**DLL 在错误偏移量进行写入可能无效。** |
| UI 报错吞掉 | ✅ |
| 3D 模型刷新 | ❌ `IsBlankName` 由于 AppendString 损坏总是返回 true → `ApplyWeaponSlotToDisplayCharacter` 立即返回 false |
| 正确装备显示 | ❌ **整个 DLL 配装系统因 SDK 偏移量错误而无法工作** |

---

## 响应处理 / 校验链路逆向（2026-06-03 ~ 06-04，30+ 会话）

### 工具链

| 工具 | 用途 |
|------|------|
| Frida 17.10 | 动态 Hook、Stalker 指令跟踪、内存扫描、指令 patch |
| IDA Pro + MCP | 静态反编译、交叉引用、字节搜索 |
| Cheat Engine | 内存写监控 |
| x64dbg | 硬件写断点（多次尝试，不稳定） |

### 关键函数映射

| 函数 | RVA | 作用 | 参与 GetPlayerArchiveV2? |
|------|-----|------|:--:|
| `sub_9C4780` | `0x9C4780` | **响应派发入口**。r8=MessageId，查哈希表派发 handler | ✅ |
| `sub_99E820` | `0x99E820` | **通用事件派发器**（非 RPC 专属）。vtable[33] 调 handler。仅 0x2E 字节 | ✅ |
| `sub_9C4840` | `0x9C4840` | Archive 消息 handler 模板。`[rcx+31Ch]==2` 检查。126 字节 | ❌ 不通过 sub_99E820 派发 |
| `sub_99B4C0` | `0x99B4C0` | 同上模板变体（104 字节分配） | ❌ |
| `sub_9BBB10` | `0x9BBB10` | 同上模板变体（80 字节分配） | ❌ |
| `sub_9BF020` | `0x9BF020` | 同上模板变体。被 sub_9B99A0 间接调用 | ❌ |
| `sub_8DD370` | `0x8DD370` | **结果投递器**。所有 handler 模板调它。InterlockedExchange64 入队 | ❌ 从未触发 |
| `sub_887BA0` | `0x887BA0` | **结构体 memcpy** | ❌ 从未触发 |
| `sub_A3E770` | `0xA3E770` | **条目复制器**。调 `sub_887BA0` | ❌ |
| `sub_A49E10` | `0xA49E10` | 缓存处理器（遍历角色，查缓存） | ❌ |
| `sub_9D5120` | `0x9D5120` | **protobuf codec**（序列化/反序列化 RoleArchiveDataV2） | ❓ 通过注册表间接调用 |
| `sub_E23450` | `0xE23450` | **响应队列收集器**。循环调 `sub_E2FB50`。sort + process | ❓ 头发渲染相关 |

### 完整调用链（GetPlayerArchiveV2 已知部分）

```
网络字节到达
  → ResponseWrapper 解码
  → sub_9C4780(..., MessageId=2)           [派发入口, r8=MessageId]
    → +0xFD: call sub_99E820               [通用事件派发]
      → call vtable[index_33]              [handler=RVA 0xE23570, 非存档相关]
  → [? 真正的存档 handler 如何被调?]      [未知 — 走另一条独立的派发链]
```

### 20 处 `[rcx+31Ch]==2` 校验位置

`83 B9 1C 03 00 00 02` 模式在二进制中共出现 20+ 次，分属不同函数：

| 地址 | 所属函数 | 大小 | 分配/调用 | 通过 sub_99E820? |
|------|----------|------|-----------|:--:|
| 0x9C484F | `sub_9C4840` | 112B / sub_98D2B0 | ❌ |
| 0x99B4CF | `sub_99B4C0` | 104B / sub_98CFD0 | ❌ |
| 0x9BBB1F | `sub_9BBB10` | 80B / sub_96E9D0 | ❌ |
| 0x9BF02A | `sub_9BF020` | — / 间接调用 | ❌ |
| 0x9C0796 | `sub_9C0790` | 128B | ❌ |
| 其他 15+ 处 | 分散在各段 | 类似模板 | ❌ |

**所有函数共享同一模板**：`if([rcx+31Ch]!=2) return 0; → alloc(size) → process → sub_8DD370(入队)`

### ErrorCode=4 的传播

**发现：ErrorCode struct 非新分配，而是从全局模板复制后通过 vtable 链传播。**

1. `sub_A49E10` 为每个角色创建条目时，调用 `sub_887BA0` 复制模板
2. 模板源：`xmmword_41CB330`（UTF-16 字符串 `"Response"`）
3. BAD struct（ErrorCode=4）通过 `sub_A3E770`→`sub_887BA0` 复制传播
4. **ErrorCode=4 的原始出处尚未定位**——两个工厂 `sub_BA0FC0`/`sub_BB4B60` 和所有 20 个 `[rcx+31Ch]==2` handler 均不参与 GetPlayerArchiveV2

### 已排除的假设

| 假设 | 验证方法 | 结论 |
|------|----------|------|
| RoleConfig 被后续代码覆盖 | CE 硬件写监控 | ❌ Spawn 后无人再写 RoleConfig |
| sub_9BF020 参与校验 | Frida hook：0 次命中 | ❌ |
| sub_887BA0 参与数据复制 | Frida hook：0 次命中 | ❌ |
| sub_8DD370（结果投递器）被调 | Frida hook：0 次命中 | ❌ |
| sub_A49E10 处理 GetPlayerArchiveV2 | Frida hook：0 次命中 | ❌ |
| 清 handler 入口 ErrorCode=0 可修复 | Session 8：仅改 ErrorCode 无效 | ❌ |
| Patch `[rcx+31Ch]` 0→2 可修复 | Session 26：field 已改为 2，仍无效 | ❌ |
| Patch sub_9C4840 jz→jmp 可绕过 | Session 24：1 字节 patch 无效 | ❌ |
| sub_9C4840 通过 sub_99E820 被调 | Session 31：vtable[33] 不指向它 | ❌ |

### 核心发现

**sub_99E820 是引擎级通用事件派发器**——渲染、音频、游戏逻辑等多种子系统复用。真正的存档 handler（20 个 `[rcx+31Ch]==2` 函数）走**另一条独立的派发链**，不经过 sub_99E820 的 vtable。

**CE 确认 RoleConfig 从未被覆盖**——问题不在覆盖，而在武器 spawn 时序：DisplayCharacter 创建时读的是 CDO 默认缓存，随后 DLL 修正 RoleConfig 但武器 actor 已 spawn。

### 当前状态

逆向完成约 85%。已知完整的派发入口、校验点分布、以及两条独立派发链的存在。剩余工作：找到真正的存档 handler 派发链入口，定位 `[rcx+31Ch]` 字段的初始化代码。

---

## FName 系统逆向与内存缓存扫描（2026-06-13，Sessions 35-55）

### 目标

1. 定位 FName 池（GName Pool），实现 FName ComparisonIndex → 字符串的双向解析
2. 通过字符串/二进制模式扫描找到运行时配装缓存地址
3. 直接修改缓存中的数据以绕过校验

### SDK 偏移量验证

| SDK 常量 | SDK RVA | 实际状态 |
|----------|---------|---------|
| `AppendString` | `0x019D82B0` | ❌ **位于 `sub_19D8120` 指令中间**（`mov rdi, [rsp+0x28]` 的第 2 字节 `0x7C`）。调用会执行 `jl` 跳转到另一条指令中间 → **游戏崩溃**（exit code 139） |
| `GNames` | `0x05D29C80` | ❌ **无交叉引用**。字节为编码指针（`33 f9 41 01...`），非 FName 池 |
| `GObjects` | `0x05D65FE0` | ❌ 指向零初始化结构体（`num=0, max=0`），运行时不可用 |

**结论：SDK 是从不同游戏版本生成的，当前构建的所有偏移量均无效。**

### FName 池调查

#### 死代码池 `0x5D29280`

- 被 `sub_19D8120`（FName 比较）、`sub_19D9570`（条目查找）、`sub_19D3490`（池初始化器）引用
- **运行时确认为全零**——`byte_5D29278` 标志为 0，无有效块指针
- `sub_19D3490` 从嵌入的名称表 `word_20000`（RVA `0x20000`）加载硬编码名称数据
- 该池**从未在正常游戏操作中初始化**——相应的 FName 比较/查找函数从未被调用
- 此池在 0x19D8xxx-0x19D9xxx 范围内的约 30 个函数共享，全部为死代码

#### sub_19D8120 分析（完整反汇编）

```
19d9576: mov eax, [rcx]        ← 从 *rcx 读取 ComparisonIndex（参数 1 = 指向 FName 的指针）
19d957a: shr ebx, 10h          ← blockIdx = compIdx >> 16
19d9584: movzx ecx, ax         ← entryOffset = compIdx & 0xFFFF
19d9591: lea rdx, unk_5D29280  ← 已初始化路径：rdx = 池基址
19d959a: lea rcx, unk_5D29280  ← 未初始化路径：在调用 sub_19D3490 前设置 rcx
19d95a1: call sub_19D3490      ← 延迟初始化池
19d95bb: add rax, [rdx+rbx*8+10h] ← 返回 pool[blockIdx+2] + 2*entryOffset
```

此函数接受**一个**指针参数（rcx = &ComparisonIndex），而非像之前假设的那样接受 4 个参数。NativeFunction 签名：`'pointer', ['pointer']`。

#### 真正 FName 池的搜索

在 `.text` 段搜索标准 UE4 FName 解析模式（`shr r32, 10h; movzx r32, r16`）：
- `C1 E8 10 0F B7`：2 个匹配（0x2bb6110, 0x36cce7f）
- `C1 EA 10 0F B7`：3 个匹配（0x19dcecf, 0x2388abb, 0x2388c3a）
- `C1 E9 10 0F B7`：9 个匹配（0x2bc1c19 等）
- 0x19dcecf 的匹配（sub_19DCCC0）：使用已死池 `unk_5D29280` 的 FName 创建/查找
- 0x2388abb 的匹配（sub_2388A60）：哈希表查找，非 FName 解析
- 0x2BB 范围内的匹配：通用的 HIWORD/LOWORD 分割，非特指 FName

**真正的 GName 池尚未定位。** 游戏使用已剥离的二进制文件；标准 UE4 符号不可用。

### Protobuf 消息注册

在 IDA 中找到并映射了以下 protobuf 消息字符串：

| 字符串 | IDA 地址 | 注册函数 |
|--------|---------|---------|
| `assets.GetPlayerArchiveReq` | 0x41fcb50 | — |
| `assets.GetPlayerArchiveRespV2` | 0x41fcb70 | `sub_9D3450`（0x36 字节） |
| `assets.RoleArchiveDataV2` | 0x41fcc00 | `sub_9D5460`（0x36 字节） |
| `assets.UpdateRoleArchiveReqV2` | 0x41fcc50 | — |
| `assets.UpdateWeaponArchiveReqV2` | 0x41fcc78 | — |
| `assets.WeaponArchiveDataV2` | 0x41fcca0 | — |
| `assets.RoleArchiveDataV2.PrimaryWeapon` | 0x41fd538 | — |
| `assets.RoleArchiveDataV2.secondWeapon` | 0x41fd560 | — |
| `assets.RoleArchiveDataV2.roleID` | 0x41fd588 | — |

- `sub_989540`：Protobuf 字符串字段设置器（被所有注册函数调用，有 200+ 调用者）
- 注册函数通过函数指针表调用；无直接交叉引用

### 配装相关字符串

| 字符串 | IDA 地址 | 关联 |
|--------|---------|------|
| `PresetLoadout` | 0x4b2eee0 | 预设配装 |
| `CurrentLoadout` | 0x4b2eef0 | **当前配装** — 无代码交叉引用 |
| `PBWeaponPartLoadout` | 0x4b2f4e0 | 武器配件配装 |
| `WeaponPartLoadouts` | 0x4b39158 | 配件配装映射键 |
| `PBWeaponLoadout` | 0x4b39238 | 武器配装结构体 |
| `SpawnDisplayCharacter` | 0x4a2f6a0 | 展示角色生成 — 无代码交叉引用（通过反射访问） |
| `InDisplayCharacterClass` | 0x4a2f9d8 | 展示角色类 |
| `ShowRoomPawn_LookUp` | 0x49755a0 | 展示间操作 |
| `GoToShowRoom` | 0x4a9d828 | BP 函数 |

### 金丝雀注入测试（Session 46-48）

**方法：** 修改 metaserver 代理，在 `GetPlayerArchiveV2Response` 中将武器 ID 替换为唯一标记（`ZZCANARYX001` 等，长度相等以避免 protobuf 重新编码），然后使用 Frida 扫描游戏内存中的标记。

**结果：**
- ✅ 金丝雀标记在堆内存中被找到（例如 `0x222eb2806ff`）
- ✅ 数据处于原始 protobuf 二进制格式（带有 protobuf 字段标记如 `0x0a`、`0x12`、`0x1a`）
- ✅ 标记后跟着 `:`（`0x3A`）作为 protobuf 字段分隔符
- ❌ 金丝雀仅出现在**原始网络缓冲区**中，而非解析后的数据结构中
- ❌ `ZZCANARYX001` 不是真实的武器名称，因此游戏无法将其转换为 FName
- ❌ 真实的武器交换（`PROBE_RU-AKM`→`PROBE_RU-SVD`）无法通过客户端校验
- ❌ 分发监控显示 `msgId=923598848`（非 2）——`sub_9C4780` 并非我们之前认为的 MessageId 分发器

**结论：** 代理注入功能正常，但客户端校验会拒绝修改后的数据。`sub_9C4780` 是一个哈希表管理函数（非 protobuf 消息分发器），因此 `msgId=2` 从未通过它触发。

### 堆内存扫描（Sessions 42, 46, 50-55）

#### 武器字符串发现（Session 42, 46）

- **UTF-16LE 扫描：0 次命中** — 武器 ID 在 Windows 上不以 FString（UTF-16）形式存在于内存中
- **窄 ASCII 扫描：31+ 次命中** — 武器 ID 以各种格式存在：
  - Protobuf 二进制缓冲区（网络数据）
  - 以冒号分隔的字符串表（`"PROBE_RU-AKM:PROBE_GSW-AR..."`）
  - 空终止符 + 8 字节元数据（如 `"PROBE_RU-AKM"\x00 [8 bytes]`）
  - Frida 脚本源代码（误报——我们的脚本存在于游戏进程内存中）
- 元数据表中条目间的 8 字节值在**多次游戏重启中保持稳定**（低 32 位），表明是内容哈希而非指针

#### 已发现的字符串表（Session 47）

`"PROBE_RU-AKM"` 出现的元数据表被分类为**蓝图状态机反射元数据**——包含 `SMGraphK2Node`、`SMConduit`、`SMEvent_Kill_AKMHeadShot_C`、`ScriptStruct`、`SMBlueprintGeneratedClass` 等字符串。**非 FName 池或配装缓存。**

#### UObject 查找尝试（Sessions 49-55）

| 方法 | 会话 | 结果 |
|------|------|------|
| GUObjectArray 结构扫描（`.data` 中的 `{heap_ptr, count, capacity}` 模式） | 49, 53 | ❌ 未找到匹配 |
| 通过 vtable→module 检查进行的堆 UObject 扫描 | 50 | 10 个候选（误报——非 8 字节对齐的 "vtables" 实际上是代码指针） |
| 通过对 Class 指针（偏移 0x10）的验证进行的密集 UObject 扫描 | 51-52 | 找到 5 个候选，全部为误报或有规律间隔的数据记录 |
| FObjectItem 数组的堆扫描（每 24 字节检查一次密集的有效堆指针） | 54 | 找到 5 个候选（包括 2 个各约 43K 条目的数组） |
| FObjectItem 候选验证 | 55 | **全部验证失败**（0/100 有效 UObject）——那些 "堆指针" 并非指向真正的 UObject |
| SDK GObjects 偏移量直接访问 | 54 | 指向零初始化结构体（`num=0, max=0`） |

**核心问题：** GUObjectArray 无法通过结构模式匹配或 SDK 偏移量定位。已剥离的二进制文件使得在不了解正确布局的情况下无法找到该数组。扫描到的密集 "FObjectItem 类" 数组实际上是用于不同目的的其他数据结构（可能是 TMap 桶或内存分配器位图）。

### 发现总结

| 发现 | 置信度 |
|------|:--:|
| SDK 偏移量（AppendString、GNames、GObjects）在当前构建中全部错误 | ✅ 已确认 |
| FName 池 `0x5D29280` 为死代码——游戏运行时从未初始化 | ✅ 已确认 |
| `sub_19D8120` 是 FName 比较函数，非 AppendString | ✅ 已确认 |
| `sub_19D9570` 是 FName 条目查找（`pool[blockIdx+2] + 2*offset`）——接受单指针参数 | ✅ 已确认 |
| Protobuf 消息注册函数已映射（sub_9D3450 等） | ✅ 已确认 |
| 金丝雀注入有效——响应数据到达原始 protobuf 缓冲区 | ✅ 已确认 |
| `sub_9C4780` 和 `sub_99E820` 是哈希表/通用事件函数——非 GetPlayerArchiveV2 的 MessageId 分发器 | ✅ 已确认 |
| GetPlayerArchiveV2 在军械库进入期间**不会**触发——可能在登录时触发 | ✅ 已确认 |
| DLL 的配装实现因 SDK 偏移量损坏而完全失效 | ✅ 已确认 |
| 真正的 GName 池和 GUObjectArray 在运行时仍未定位 | ❌ 未解决 |
| 配装缓存地址仍未找到 | ❌ 未解决 |

### 更新的 Frida 脚本工具集

位置：`ProjectRebound/Metaserver/tools/`

| 脚本 | 会话 | 用途 |
|------|------|------|
| `session35_resolve_fnames.js` | 35 | 直接读取 FName 池（失败——池为空） |
| `session36_hook_fname.js` | 36 | 钩住错误的 AppendString 地址（崩溃） |
| `session37_resolve_via_sub.js` | 37 | 通过 NativeFunction 调用 sub_19D9570（签名错误） |
| `session38_direct_pool.js` | 38 | 直接读取池内存，强制初始化 |
| `session39_fixed_resolve.js` | 39 | 修正的 NativeFunction 调用（单指针参数） |
| `session40_trace_dll.js` | 40 | 追踪 DLL 的 AppendString 调用（崩溃，确认偏移量错误） |
| `session41_trace_archive.js` | 41 | 钩住分发器，追踪 msgId=2 处理程序 |
| `session42_utf16_scan.js` | 42 | 扫描 UTF-16LE 武器字符串（0 次命中） |
| `session43_trace_write.js` | 43 | 堆快照前后对比——未检测到 msgId=2 |
| `session44_analyze_table.js` | 44 | 分析发现的字符串表 |
| `session45_find_pool.js` | 45 | 动态 FName 池搜索（带空终止符的模式搜索） |
| `session46_flex_scan.js` | 46 | 灵活的窄 ASCII 武器子字符串扫描 |
| `session47_deep_table.js` | 47 | 对元数据表进行深度分析和分类 |
| `session48_canary_monitor.js` | 48 | 监控分发 + 扫描金丝雀标记 |
| `session49_find_char.js` | 49 | 在 `.data` 中扫描 GUObjectArray |
| `session50_heap_uobjects.js` | 50 | 扫描 vtable→module 的堆 UObject |
| `session51_real_uobjects.js` | 51 | 扫描具有已验证 Class 指针的真实 UObject |
| `session52_dense_scan.js` | 52 | 每 8 字节密集堆扫描 |
| `session53_gobject_array.js` | 53 | 在模块 `.data` 中搜索 GUObjectArray 模式 |
| `session54_dll_gobjects.js` | 54 | SDK 偏移量测试 + FObjectItem 数组的堆扫描 |
| `session55_verify_ga.js` | 55 | 验证并遍历 GUObjectArray 候选 |

---

## 下一阶段：硬件断点定向爆破（Phase 2）

### 目标
找到**谁调用了 20 个 `[rcx+31Ch]==2` handler**，以及**谁设置了 `[rcx+31Ch]` 字段为 2**。

### 计划

#### Step 1：冷启动监控
```
1. 重启游戏
2. 先运行 Frida 脚本，hook 所有 20 个 check 函数 (sub_9C4840, sub_99B4C0 等)
3. 从登录界面开始 → 进入主菜单 → 进军械库
4. 捕获哪些函数在"游戏启动→登录→主菜单"阶段被首次触发
   （推测它们只在首次获得装备数据时被调，后续进入军械库走缓存）
```

#### Step 2：上下文对象追踪
```
1. 从 Session 25 已知：obj+0x31C=0 的对象地址为 0x16039a01c28
2. x64dbg 硬件写断点: ba w 4 <objAddr>+0x31C
3. 重启游戏 → 断点命中 → Call Stack 显示谁写入了 2
4. 从写入函数向上追溯，找到创建上下文对象的代码
```

#### Step 3：vtable 彻底排查
```
1. 在 Session 31 基础上，打印所有 vtable[0..50] 的 handler RVA
2. 在 IDA 中逐一检查，找到 RVA 落在 0x9C 或 0x99 区域（存档 handler 附近）
3. 定位存档 handler 实际使用的 vtable index
```

### Frida 脚本工具集

位置：`ProjectRebound/Metaserver/tools/`

| 脚本 | 用途 |
|------|------|
| `session1_probe_dispatch.js` | 探测 sub_9C4780 函数签名和 MessageId |
| `session2_scan_calls.js` | 扫描 sub_9C4780 内部 call 指令 |
| `session3_handler_probe.js` | Hook handler 0x9C48B0，首次详情/后续静默 |
| `session15_group_calls.js` | 按调用者 RVA 分组 sub_99E820 调用 |
| `session20_find_chars.js` | 堆扫描找 DisplayCharacter RoleConfig 地址 |
| `session22_map_all.js` | 批量 hook 7 个已映射函数 |
| `session24_bypass_final.js` | 1 字节 patch jz→jmp |
| `session25_capture_obj.js` | 捕获 *(a1+0x18) 对象并 dump +0x31C |
| `session26_patch_obj.js` | 直接 patch obj+0x31C 0→2 |
| `session30_monitor_obj.js` | 监控 *(a1+0x18) 的 vtable 变化 |
| `session31_read_handler.js` | 读所有有效 vtable 的 index 33 |

---

## 已修复

- [x] `/chat.chat/TextFilter` — 加空响应（之前导致 ErrorCode=-1）
- [x] `/party.party/Get` — 加空响应
- [x] 冲刺 UI 抖动 — CamCache 接管 + 合成正弦波
- [x] `/chat.chat/TextFilter` — 加空响应（之前导致 ErrorCode=-1）
- [x] `/party.party/Get` — 加空响应
- [x] 冲刺 UI 抖动 — CamCache 接管 + 合成正弦波
- [x] **代理武器 ID 交换** — proxy.js 字节级查找替换，支持金丝雀注入（用于内存追踪）和真实武器交换（用于功能测试）
- [x] **金丝雀注入通过代理验证** — 确认 metaserver→客户端通信正常，protobuf 数据到达客户端内存
- [ ] `UpdateRoleArchiveV2` 换装报错 — 根因是 `OnEquipCharacterSlotDelegate` 广播 ErrorCode=4，走 Delegate 不经过 ProcessEvent
- [ ] 军械库全默认装备 — 逆向 ~85%：两条独立派发链已识别，20 个校验点已定位，存档 handler 的派发入口待定位
- [ ] **定位真正的 GName 池** — 死代码池 `0x5D29280` 已排除；真正池的 HIWORD/LOWORD 模式搜索尚未有定论
- [ ] **定位 GUObjectArray** — SDK 偏移量、结构扫描和堆扫描均失败
- [x] **DLL 配装系统不可用** — AppEndString、GNames、GObjects 偏移量全部错误；需要为当前游戏版本重新生成 SDK

---

## 代理武器 ID 交换 (proxy.js 修改)

### 实现

修改了 `processServerFrame` 函数以在转发到客户端**之前**解析和修改响应。添加了 `modifyArchiveResponse()` 函数用于字节级查找替换（等长字符串以避免 protobuf 重新编码）。

### 关键更改

1. **将 `clientSock.write(framedMsg)` 从函数开始移动到解析之后** —— 确保修正在转发之前生效
2. **添加了 `WEAPON_SWAPS` 配置表** —— 等长字符串对用于字节交换
3. **金丝雀模式**（已测试）：
   - `PROBE_RU-AKM` (12 字节) → `ZZCANARYX001` (12 字节)
   - 已验证：金丝雀出现在位于 `0x222eb280xxx` 的客户端堆 protobuf 缓冲区中
4. **真实武器交换模式**（已测试，被校验阻止）：
   - `PROBE_RU-AKM` → `PROBE_RU-SVD`
   - `PEACE_GSW-AR` → `PROBE_GSW-AR`
   - `SNIPER_RU-MOSIN` → `SNIPER_GSW-AMR`
5. **添加了 `modifyArchiveResponse()` 函数** —— 当 `rpcPath.includes('GetPlayerArchiveV2')` 时调用

### 已知限制

- 代理在 `GetPlayerArchiveV2` 响应中替换武器 ID，但**客户端校验仍然拒绝修改后的数据**
- 仅等长交换是安全的；不等长交换会使 protobuf 长度前缀失效
- 交换仅影响原始 protobuf 有效载荷，不保证游戏将解析后的 FName 存储到其缓存中
