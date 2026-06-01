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
| FName 字符串转换 | ✅ |
| 写 RoleConfig | ✅ |
| UI 报错吞掉 | ✅ |
| 3D 模型刷新 | ❌ Put-And-Refresh 后值可能被覆盖 |
| 正确装备显示 | ❌ RoleConfig 值不持久 |

---

## 当前方向：定位缓存填充代码（阶段 1）

### 目标
找 Native 解析 `GetPlayerArchiveV2` 后，往 `RoleConfig` 写数据的代码。

### 方法
1. 在 LoadoutFix 的 F8 热键里 log `RoleConfig` 的运行时地址
2. x64dbg 对该地址设硬件写断点
3. 退出再进军械库 → `SpawnDisplayCharacter` 写 `RoleConfig` → 断点命中 → Call Stack 显示谁传了 Config

---

## 已修复

- [x] `/chat.chat/TextFilter` — 加空响应（之前导致 ErrorCode=-1）
- [x] `/party.party/Get` — 加空响应
- [x] 冲刺 UI 抖动 — CamCache 接管 + 合成正弦波
- [ ] `UpdateRoleArchiveV2` 换装报错 — 根因是 `OnEquipCharacterSlotDelegate` 广播 ErrorCode=4，走 Delegate 不经过 ProcessEvent
- [ ] 军械库全默认装备 — 根因是 GetPlayerArchiveV2 被拒，RoleConfig 未被填充
