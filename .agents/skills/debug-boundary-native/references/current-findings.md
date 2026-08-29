# Boundary 当前逆向成果

本文件是固定构建的证据摘要。状态分为：**已证实**、**当前兼容实现**、**已排除**和**待验收**。更新地址或语义时写明新的 EXE 哈希和证据来源。

## 1. 固定客户端构建

| 项目 | 当前值 | 状态 |
| --- | --- | --- |
| 主模块 | `ProjectBoundarySteam-Win64-Shipping.exe` | 已证实 |
| `SizeOfImage` | `105431040` | 已证实 |
| EXE SHA-256 | `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843` | 已证实 |
| `GObjects` RVA | `0x05D65FE0` | 已证实 |
| `FName::AppendString` RVA | `0x019D82B0` | 已证实 |
| `UObject::ProcessEvent` RVA | `0x01BCBE40` | 已证实 |

所有以下 RVA、vtable 偏移和成员布局只适用于这个哈希。

## 2. QueryAssets 与军械库所有权

### 已证实

- `UPBArmoryManager` 的原生拥有物品数组：data `+0x40`、num `+0x48`、max `+0x4C`、new-item counter `+0x50`。
- 元素大小为 `0x10`：物品 `FName` 位于 `+0x00`，`Count` 位于 `+0x08`，`bIsNew` 位于 `+0x0C`。
- 静态反编译和运行时探针一致证明 `HasItem` 按 `0x10` 步长遍历并只比较物品 ID。`Count` 没有参与所有权判断，因此“未解锁是 Count 未增加”已经排除。
- `QueryAssets` native consumer 位于 VA `0x1416DF990`（RVA `0x016DF990`）。顶层 field 1 虽命名为 `ItemCount`，实际是 result/status；值大于 299 时在读取任何 row 之前被拒绝，成功捕获值为 `1`。
- repeated field 2 才是物品集合。当前响应包含 40,462 个去重、按 FName 严格排序的 ID；顶层成功状态为 `1`，每个 row 中三个未被客户端读取的标量在 wire 中省略为默认值 0。
- 确定性 protobuf 大小为 `1,372,853` 字节，golden SHA-256 为 `d3aa4e84d75689e42ecc54f9735b6842762c56b5814e61ef8b2c5e01b4e31531`。
- 原生完成链为 `FOnlineAsyncTaskQueryAssets -> LogicServer delegate -> PBArmoryManager`，最终只复制每行 `ItemId`。
- PersistentUser 的 saved/runtime armory 分别位于 `+0x48` 和 `+0x68`，仅用于只读对照来源。

### 已排除

- `UserAsset` 候选 schema 只让原生库存得到 268 项，不能表示当前完整所有权集合。
- 定时扫描 `DT_ItemType` 并覆盖 `OwnedItems` 会污染证据并覆盖原生状态，不能作为修复。

### 当前兼容实现

- 固定客户端在 VA `0x1409C37BB` 拒绝大于 1 MiB 的帧；同一调用链的输出分配、容量计算和清零长度也分别固定为 `1 MiB + 10`。
- Payload 只在 EXE 大小和 SHA 都匹配时，原子地把以下四处限制从 1 MiB 提升到 2 MiB：

| 作用 | RVA |
| --- | --- |
| 长度 guard | `0x009C37BB` |
| 输出分配 | `0x009C3B47` |
| 输出容量 | `0x009C3B68` |
| 输出清零 | `0x009C3B87` |

- 只改长度 guard 已复现输出缓冲区溢出，四处必须作为一个带原字节校验的事务处理。

## 3. 等级与解锁筛选

### 已证实

- `QueryAssets` 决定原生所有权；玩家/角色等级另外决定 progression/UI 的可选过滤，两者不是同一校验。
- 当前 MetaServer 默认报告玩家等级 70、角色等级 30。
- 统计键必须保持精确拼写：`Level_Player`、`Exp_Player`、`Level_PEACE` 等；狙击角色使用混合大小写 `Level_Sniper` / `Exp_Sniper`。
- `PlayerLevelExpDataTable` 的运行时最高等级由只读 Frida 获取，不能对新构建硬编码旧上限。

## 4. Player archive 协议与角色标识

### 已证实

- 内部六角色为 `PEACE`、`PROBE`、`Sniper`、`FORT`、`FIXER`、`SPIKE`；请求和响应的角色 ID 大小写必须保留。UI 的 ORLAN 对应内部 `PEACE`。
- `PlayerRoleData` 字段为：1 role ID、2 左挂载、3 右挂载、4 机动、5 近战、6 主武器、7 副武器、8 weapon archive raw、9 skin token、10 ornament/painting ID。
- 六个装备槽保留显式字符串 `None`。空或非法主武器由原生规则回退，不应靠直接写内存伪造成功。
- field 8 是当前角色各武器 archive 的嵌套 protobuf bundle，不是普通字符串 JSON。
- 当前客户端构建不会把 field 8 和全部角色外观自动分发到 `PBCustomizeManager`；仅证明 Meta 响应正确不足以证明 UI 状态已初始化。

## 5. 原生 archive completion

### 已证实的入口

| completion | RVA |
| --- | --- |
| 角色装备槽 | `0x016DD080` |
| 角色皮肤涂装 | `0x016DCEC0` |
| 角色外观槽 | `0x016DCD80` |
| 武器挂件 | `0x016DD1D0` |
| 武器配件皮肤涂装 | `0x016DD490` |
| 武器配件槽 | `0x016DD5F0` |
| 武器套装/皮肤 | `0x016DD740` |

已恢复的 ABI：

```cpp
CompleteCharacterSlot(manager, int32 code, FName item, FName role, EPBCharacterSlotType slot)
CompleteCharacterAppearance(manager, int32 code, FName item, FName role, EPBSkinClass skinClass)
CompleteCharacterSkinPainting(manager, int32 code, FName skin, FName painting, FName role)
CompleteWeaponSlot(manager, int32 code, FName part, FName role, FName weapon, EPBPartSlotType slot)
CompleteWeaponSuite(manager, int32 code, FName suite, FName painting, FName role, FName weapon)
CompleteWeaponPartSkinPainting(manager, int32 code, FName skin, FName painting, FName role, FName weapon, FName part)
CompleteWeaponOrnament(manager, int32 code, FName ornament, FName role, FName weapon)
```

- 已观测 `EPBSkinClass`：`Skin=1`、`Ornaments=3`、`SpecialSlot=4`。
- x64 调用中枚举寄存器的未使用高位可能是脏数据；Frida 解析 selector 时必须取低字节。

### 当前兼容实现

- 客户端每个 `ULocalPlayer` 生命周期只读取一次已认证 loadout snapshot，不进行周期性 REST mirror。
- snapshot 经严格角色、物品类型和长度校验后，调用上述原生 completion 入口补齐当前构建未分发的内容。
- FieldMod 初始化使用校验后的角色数组调用原生 `ClientInitFieldMod`；不直接写 `FieldModManager + 0x98` 或 `APBPlayerState + 0x6E0`。

## 6. 保存路由与错误码

### UpdateRoleArchiveV2

- skin payload 的 `token_id` 持久化到 `skinModel`，`ornament_id` 持久化到 `skinPaint`。
- 物品槽路由优先依据 definitions 的 `ItemType`：`Weapon`、`Pod`、`Mobility`、`MeleeWeapon`、`ArmBadge`、`HeadAccessory`。
- operation 值存在复用，不能脱离物品类型单独推导槽位。当前 fallback 映射为：2 左挂载、3 右挂载、4 机动、5 近战、6 主武器、7 副武器，其他值回落到主武器。
- 武器和 Pod 的实际路由还会结合 operation；skin-only payload 不能清空装备槽。
- 角色外观 snapshot 别名映射：`skinModel -> Skin`、`headOrnament -> Ornaments`、`armBadge -> SpecialSlot`、`skinPaint -> skinPaintingId`。
- 后端必须验证 `headOrnament` 是 `HeadAccessory`、`armBadge` 是 `ArmBadge`，角色兼容性也必须通过 definitions。

### UpdateWeaponArchiveV2

- 按精确 role/weapon/slot 校验并保存 protobuf archive；随后 `GetPlayerArchiveV2` 必须返回同一 archive。
- 武器配件、配件皮肤、武器皮肤/套装和武器挂件已经在游戏中正常保存，并通过冷启动恢复。

### completion 状态码

- 通用持久化 completion：仅 `404 -> 0`。
- 角色装备路径：`404 -> 0`，并允许 `9002 (0x232A) -> 0`。
- 通用角色/武器外观路径必须保留 `9002`，不能全局吞掉未知错误。

## 7. FieldMod、PlayerState 与服务端桥接

### 已证实布局

| 对象 | 成员/入口 | 偏移或 RVA |
| --- | --- | --- |
| `UPBFieldModManager` | pre-ordering map | `+0x98`，element `0x30` |
| `APBPlayerState` | equipping map | `+0x6E0`，element `0x30` |
| `APBPlayerState` vtable | RefreshPreOrdering | `+0x700` |
| `APBPlayerState` vtable | RefreshEquipping | `+0x708` |
| `APBPlayerState` vtable | InitFieldMod | `+0x710` |
| `UPBCareerManager` | user profile data | `+0x48` |
| `UPBCareerManager` | character map | `+0xF0`，element `0x88` |
| `UPBCareerManager` | native profile query | RVA `0x016E8240` |

### 当前职责边界

- `LoadoutManager` 的客户端 inventory/UI 入口保持 inert；客户端 archive 由 `QueryAssets`、`GetPlayerArchiveV2` 和最小 completion replay 负责。
- 服务端 bridge 仍处理 listen-host 的 baseline override、pre-order intercept、角色确认延迟和 spawn application。
- 当前实现已经删除共享 role/spawn lease：角色确认 grace 1 秒只用于等待该连接自己的 baseline；原生 `ServerPreOrderInventory` 成功写入该玩家 `+0x6C0` 后才记录 runtime override。post-spawn 仍以 50 ms 间隔、2 秒窗口重试，但用 player/connection generation、pawn、role、equipping hash 验证旧事件，不再阻塞原生出生。

## 8. 最近一次端到端结果

截至 2026-08-16：

- 全量军械库解锁可用，线上配装可应用。
- 武器改装（配件、配件涂装、武器皮肤/套装和挂件）保存并冷启动恢复正常。
- 角色外观修复后的 PEACE/ORLAN 样本在 UI 中为 `DART FROG RED`、`ROCKET`、`NO ENTRY`；native 值分别为皮肤 `PEACE_ORIGINAL`、涂装 `PEACE_ORIGINAL_PTDartFrogRed`、头饰 `HORocket`、臂章 `ABGNoEntry`。
- 两次冷启动均恢复该样本，保存状态码为 0；Frida 观察到 12 次角色外观 completion 和 6 次角色涂装 completion，均为 code 0。
- 对应 Payload 行为提交为 `6ebe460`；随后 `7f008ac` 只整理 Frida FName helper。
- 已部署 `Payload.dll` SHA-256 为 `2FEB23BF51745A65145D3D62BB57DEC555018BC36DC09D0FB68891A19C379912`，回滚副本为 `Payload.pre-character-appearance-20260816-034104.dll`。

### 待验收/残余风险

- 角色外观已完成两次冷启动 UI 验证，但任何新 completion 或 server-spawn 变更仍需重新覆盖首次出生、复活、换角色、晚加入和断线重连。
- 新游戏构建必须重新解析所有 RVA、结构偏移、最高等级和 frame patch 原字节；不得只更新哈希绕过 guard。
- 协议命名包含若干历史误名，例如 `ItemCount` 实为状态、field 10 名为 ornament 但承载角色涂装语义；修改 proto 名称会破坏兼容，不应仅为可读性改字段号或 wire 类型。

## 9. 主要证据入口

- Frida 联合探针：`Tools/Frida/armory_probe.js`
- 启动与采集：`Tools/Frida/capture_armory.py`、`Tools/Frida/run-armory-probe.ps1`
- QueryAssets 专项：`Tools/Frida/logic_server_armory_probe.js`、`query_assets_*_ab.js`
- 客户端兼容层：`Payload/ClientLogic/ClientLogic.cpp`
- completion 策略：`Payload/Hooks/ArchiveCompletionPolicy.h`
- snapshot 序列化：`Payload/Loadout/LoadoutSerializer.cpp`
- 服务端出生桥：`Payload/Loadout/LoadoutManager.h/.cpp`
- Meta RPC：`Backend/internal/metaserver/native_rpc.go`
- 保存/清洗：`Backend/internal/metaserver/p2p_loadout.go`
- protobuf：`Backend/api/proto/metaserver/metaserver.proto`
- 回归测试：`Backend/internal/metaserver/native_rpc_test.go`、`p2p_loadout_test.go`、`Payload/Tests`

## 10. 对局内配装的原生双模式链

以下结论适用于第 1 节固定 EXE SHA-256，并由 IDA 静态分析确认。

### 入口与模式判定

- PE entry 为 VA `0x1440D401C`（RVA `0x040D401C`），依次进入 CRT、`WinMain` VA `0x14087CC00`、`DefaultMain` VA `0x1408729C0`，再执行 PreInit、Init 和 tick loop。
- 原生模式不是单一进程布尔值，而是按 `UWorld::GetNetMode` 动态判定：`0=Standalone`、`1=DedicatedServer`、`2=ListenServer`、`3=Client`。`IsDedicatedServer` RVA `0x033266F0` 只接受 mode 1；`IsServer` RVA `0x03326C60` 接受 0/1/2；`IsStandalone` RVA `0x03326CE0` 只接受 mode 0。
- `GIsClient` 位于 RVA `0x05CE2404`，`GIsServer` 位于 RVA `0x05CE2405`。存在 authority net driver 时，原生函数 VA `0x1433CCED0` 以 `GIsClient` 区分 dedicated（1）与 listen（2）；无 authority 时返回 client（3）。URL 中的 `listen` 也会使 world 进入 mode 2。
- 当前 Payload 另有一次性进程分支：`GetCommandLineA().find("-server")`。该判断不是原生 NetMode，且会把 `-servername=`、`-serverregion=` 等包含子串的参数视为 server；不带 `-server` 的 listen host 则会走 Payload client 分支，即使原生 world 是 mode 2。

### 客户端初始化与编辑

- `APBPlayerState` 实例虚表为 `off_144999598`。三个 RPC 的实际实现为：RefreshPreOrdering VA `0x14165D580`（vtable `+0x700`）、RefreshEquipping VA `0x14165D520`（`+0x708`）、InitFieldMod VA `0x14165D130`（`+0x710`）。
- `ClientInitFieldMod(ServerEquippingSaved, RoleIDs, OwnedQuotas)` 将完整角色配置同时复制到 `PlayerState +0x6C0`（pre-ordering）和 `+0x6E0`（equipping），为每个角色在 `+0x700` 生成 `0x110` 字节的展开记录，在 `+0x7A0` 建立配额状态，并把 RoleIDs 交给本 world 的 `UPBFieldModManager`。因此两个单角色 Refresh RPC 不能替代 Init。
- `ClientRefreshRolePreOrderingInventory` 只更新 `+0x6C0` 的对应角色并重算配额；`ClientRefreshRoleEquippingInventory` 同时更新 `+0x6E0` 与 `+0x6C0`，再重算配额。
- `UPBFieldModManager +0x98` 是 UI 的 pre-ordering map。`GetPreOrderingItemIDInSlotType` 读该 map；`GetEquippingItemIDInSlotType` 转到本地 `APBPlayerState` 读取权威 equipping。`SelectCharacter`、`SelectCharacterSlot`、`SelectInventoryItem` 只改变选择状态并广播 delegate。
- `SavePreOrderGameSaved` VA `0x14172FAB0` 遍历 FieldModManager `+0x98`，把每个角色配置先复制到本地 PlayerState `+0x6C0`，再通过 VA `0x14183FE80` 发送 `ServerPreOrderInventory`。本地 UI 变化发生在服务端接受之前，后续必须由服务端 refresh/reject 对账。

### 服务端接受、确认与出生

- `ServerPreOrderInventory` reflected thunk RVA `0x01843740`；原生实现 RVA `0x015C1110`，其 NetValidate vtable 槽返回恒真。语义校验在 implementation 内完成：逐个非空物品检查角色兼容集合、比较角色 owned quota、从 used quota 扣除旧配置并加入新配置、更新服务端 PlayerState `+0x6C0`，随后回发 `ClientRefreshRolePreOrderingInventory`；失败时回发 `ClientPreOrderUnlockInventory` 和原配置。
- `ServerConfirmRoleSelection` reflected thunk RVA `0x01843390`；validate RVA `0x015C09C0` 检查角色是否存在于服务端允许角色表，implementation RVA `0x015C0890` 通过 RVA `0x015BD760` 合并展开角色记录与当前 pre-ordering，把最终配置缓存到 controller `+0x818`，写 `SelectedCharacterID (+0x334)`，再调用 `APBGameMode::RestartPlayer`。`APBGameMode` vtable `+0x690` 的 VA `0x141627200` 在解析默认 Pawn class 时也读取该 controller 缓存。
- `APBGameMode` 实例虚表为 `off_14498A628`。`RestartAllPlayers` VA `0x14163D170` 收集 controllers 后调用 `RestartPlayers` VA `0x14163D420`；后者逐个进入 vtable `+0x790` 的 `RestartPlayer` VA `0x14163D250`。角色确认也进入同一 `RestartPlayer` 槽，因此首次出生、复活和确认角色最终汇合到同一服务端生成路径。
- 在占有角色后，VA `0x14167FB20` 将该 `PossessedCharacterId (+0x33C)` 对应的 `+0x6C0` pre-ordering 提升/复制到 `+0x6E0` equipping，重建 `+0x700` 六槽展开记录，并回发 `ClientRefreshRoleEquippingInventory`。这一步才把待选配置变为当前生命的装备配置。
- `UPBFieldModManager::SpawnWeapon` VA `0x1417316B0` 根据已解析的角色/武器记录创建本地武器 actor，经 VA `0x141716E50` 生成详细武器配置并调用武器虚函数应用；它不读取 Meta archive。`K2_InventorySpawned` 是 inventory actors 已存在后的 Blueprint 事件，适合作为只读验收点，不是原生 archive 初始化入口。

### 当前兼容层边界

- 客户端兼容层每个 `ULocalPlayer` 生命周期获取一次 snapshot，并调用原生 archive completion 与 `ClientInitFieldMod`；它不是游戏原生 Meta RPC 调度器，但复用了原生 consumer。
- 服务端 `LoadoutManager` 现在只在角色确认前通过原生 `ServerPreOrderInventory` 注入该连接的 effective 六槽配置，并在原生调用后只读验证 `PlayerState +0x6C0`；不再维护共享 lease、直接写 FieldMod/PlayerState、或门控出生。`K2_InventorySpawned` 后只在 role 与 `+0x6E0` equipping 精确一致时覆盖角色外观以及主/副武器 archive 细节。
- 仅修改 completion 错误码只能改变 UI/保存完成结果；若原生 `ServerPreOrderInventory` implementation 没有实际更新 `+0x6C0`，或 `ServerConfirmRoleSelection` 没有提交 `SelectedCharacterID`，出生仍会消费默认/旧配置。

### 已实现的对局验证观测点（2026-08-16）

- Payload 启动会对所有 client/server 路径校验固定 EXE SHA-256，并用精确 argv token 识别 `-server`、`-debug`、`-NativeArchiveOnly` 与 loadout feature flags；不再允许 `-servername` 等前缀误触发。
- `Tools/Frida/armory_probe.js` 同时记录 reflected `ServerPreOrderInventory`、`ServerConfirmRoleSelection`、`K2_InventorySpawned`、`OnRep_PossessedCharacterID`，以及原生 implementation RVA `0x015C1110`、`0x015C0890`、占有提升 RVA `0x0167FB20` 的调用边界。
- `player_state.snapshot/changed` 现在在同一事件中记录 `SelectedCharacterID (+0x334)`、`PossessedCharacterId (+0x33C)`、pre-ordering `+0x6C0` 与 equipping `+0x6E0` 的逐角色槽位和集合哈希，可直接判定失败发生在 RPC 接受、角色确认、占有提升还是 actor 细节 overlay。

## 11. 死亡后显式 F 复活接线（2026-08-20）

以下结论仅适用于第 1 节固定 EXE SHA-256。

### 原生入口与调用关系

- `ServerQuickRespawn` reflected thunk RVA `0x01843870`，PB implementation RVA `0x015C14C0`，主体跳转到 RVA `0x015BDF90`。该主体先执行 PB controller/observer 相关动作，再通过生成的 `ServerRestartPlayer` wrapper 进入 Engine 重启链；不能把 reflected `ProcessEvent` enter/leave 当作 implementation 已接受请求。
- Engine `ServerRestartPlayer` reflected thunk RVA `0x0380FF30`，implementation RVA `0x03506BD0`。其实现验证状态后直接调用 GameMode `RestartPlayer`，不会执行 PB QuickRespawn 的前置动作，因此对 managed 显式复活不能把它当作与 PB QuickRespawn 完全等价的入口。
- `ExitObserverState` implementation RVA `0x015AE240`；`APBGameMode::RestartPlayers` / `RestartPlayer` RVA 分别为 `0x0163D420` / `0x0163D250`。出生后角色配置提升仍发生在 RVA `0x0167FB20`。

### A/B 根因与修复

- A（旧接线）在 `local-pve/20260820-160700/server-launcher.stdout.log` 和 `runtime-tests/20260816-pve-retest-9/server.stdout.log` 可复现：显式 F 的 `ServerQuickRespawn` 被 Payload 标记后直接 return，下一帧改走 generic `RestartPlayers`。日志停在 inventory-spawn，缺少 Finalized/Spawn complete，并在 QuickRespawn、ServerSuicide 重试后超时。UI intent 成立，但原始 PB implementation 没有进入。
- 根因是 managed hook 把显式原生 RPC 吞掉并替换成异步通用出生入口，破坏了 PB QuickRespawn 自带的 observer/controller 清理与原生时序；`K2_InventorySpawned` 只能证明 actor 出现，不能证明 Pawn 已占有或 UI 已回到 Playing。
- B（当前实现）只在玩家处于 `AwaitingRespawnInput` 且请求可排队时接受一次显式请求，并在 per-player restart permit 内同步转发原生调用。来自 `PlayerController.ServerRestartPlayer` 的 managed 显式请求在同一 permit 内规范化为 `ServerQuickRespawn`，从而统一经过 PB 前置动作；内部/非 managed/已有 permit 的调用保持 pass-through。旧的 suppressed 行为仍可用 `-RespawnExplicitNative=0` 做对照。
- 原生返回后 manager 仅负责验证 live Pawn、selected/possessed role、占有与装备提升，并补发 `ClientGotoState(Playing)`、`ClientRestart`、`ClientRetryClientRestart`、`ServerAcknowledgePossession`；attempt 1/2 的 generic RestartPlayers/ServerSuicide 只保留为超时 fallback，不再与 attempt 0 双重接线。

### 固定构建运行时验收

- B 会话：`local-pve/20260820-174803/server-launcher.attempt-2.stdout.log`；client/server Frida：`frida-captures/20260820-respawn-normalized/{client,server}/events.jsonl`。
- PEACE/ORLAN 首次出生成功后，连续 6 次真实 `ClientBeKilled -> F -> ServerQuickRespawn` 均在 lifecycle `1..6`、attempt `0` 完成。每轮都观测到 PB Quick implementation、Engine Restart implementation、`RestartPlayer`、`SpawnDefaultPawn*` 返回非空 Pawn、RVA `0x0167FB20` promotion、`OnRep_PossessedCharacterID`、PEACE equipping hash、`ClientGotoState(Playing)`、`ClientRestart`、acknowledge、`Spawn complete`；没有 timeout 或 fallback。
- 六轮结束时客户端均为可操作 HUD，无死亡/选角遮罩，`selected=PEACE`、`possessed=PEACE`。本次实际部署并验收的 `Payload.dll` SHA-256 为 `5C1FD98DB23FA4C6C6D491DBE539BFA05E7825A5CFF8D32F928C529CE778D446`；部署前回滚副本为 `Payload.pre-respawn-engine-normalize-20260820.dll`。
- `Payload/Tests/RespawnStatePolicyTests.cpp` 覆盖 awaiting-input、重复/非法请求、A/B suppress/forward、permit pass-through，以及仅将 managed Engine restart 规范化到 QuickRespawn；2026-08-20 的 Release/x64 回归为 8/8 tests passed。

## 12. 首发客户端靶场 UI 残留（2026-08-20）

以下结论仅适用于第 1 节固定 EXE SHA-256。

### 根因证据

- 旧 `ClientLogic::PumpPendingClientCommands` 的真实连接顺序为：主菜单登录完成后等待 2 秒、调用 `UPBLocalPlayer::GoToRange(0)`、再等待 1 秒执行 `open <target>`。`clientlogs/clientlog-20260820_191500.txt` 和 Frida `frida-captures/20260820-range-ui-prefx/events.jsonl` 均观测到该顺序；其中 `GoToRange` reflected thunk/exec RVA 为 `0x01822DD0`，implementation RVA 为 `0x0166DFB0`。
- `GoToRange(0)` 的 implementation 立即读取 `UPBLocalPlayer::RangeLevel` 并启动 RangeLevel travel。进入靶场不是网络连接的必要前置条件，而是旧自动连接代码为规避过早主菜单覆盖而保留的中转 world。
- `UPBLocalPlayer` 会跨 world travel 持续存在，且保存当前/排队确认 UI。`ShowConfirmPage` reflected thunk RVA `0x01823890` 调用 implementation RVA `0x01682310`，后者把 `0x78` 字节 `FConfirmInfo` 压入 LocalPlayer 内部数组并显示队首；`PBPlayerController::ShowConfirm` thunk RVA `0x01843EE0`、implementation RVA `0x015C46C0` 最终也转发到同一 LocalPlayer 队列。因此先进入 Range 再打开战局可以把靶场退出确认/输入状态带入首发连接。
- `PBPlayerController::ExitRange` thunk RVA `0x018424A0`、implementation RVA `0x015AE450` 通过当前 LocalPlayer 执行退出范围动作。SDK 同时确认靶场蓝图控制器持有 `ShootingRangePanel`、`IsExitingRange`，ESC 事件为 `K2_InputKeyToExitRange`。
- 修复前冷启动的首发玩家在完成角色选择和首次出生后，ESC 会直接显示 `LEAVE MATCH / PLEASE CONFIRM YOUR COMMAND`，而不是正常对局菜单。该次首发只观察到 `ClientReadyAtStartSpot`，没有中途加入分支会补发的 `ClientMatchHasStarted`、`ClientRoundHasStarted`、`NotifyGameStarted`；这解释了为什么晚加入可由完整 client-start 生命周期覆盖残留，而首发不能。
- 正常登录会依次构造 `UMG_EnterGame_C -> UMG_LoginGate_C -> UMG_MainMenuBase_C`。旧直接连接只停用 `PBMainMenuManager` 返回的顶层 `UMG_MainMenuBase_C`；`GetTopMenuWidget` 在停用后仍返回同一 MainMenu，无法暴露下层认证 widget，结果 `UMG_LoginGate_C/UMG_Login_C` 的 `CONNECTING TO PLATFORM SERVER` 覆盖到战局 UI。
- 固定构建中 `PBGameInstance::HideLoadingScreen` exec RVA `0x017F5800`、implementation RVA `0x01568110`；MainMenu handler RVA `0x0154FE20` 也会先调用该 implementation。但运行时单独调用 `HideLoadingScreen` 后覆盖层仍在。`PBCustomManager_BP_C::HideWaitingForServerTips` 同样无法清除，且失败窗口没有对应 `ShowWaitingForServerTips`，因此 loading movie/WaitingTips 均不是该覆盖层的 owner。
- 决定性动态证据是：新战局 `UWorld` 激活后，对精确的 `UMG_LoginGate_C` 和 `UMG_Login_C` 实例调用 `UWidget::RemoveFromParent`，覆盖层立即消失；Frida 同时记录两类 widget 的 enter/leave，随后角色选择、装备刷新、`OnRep_PossessedCharacterID` 和 playable possession 正常完成。

### 修复边界

- 客户端连接状态机只保留 `Idle -> Queued -> WaitingAfterLogin -> Idle`。登录稳定后只对精确白名单 `UMG_EnterGame_C/UMG_LoginGate_C/UMG_MainMenuBase_C` 执行 `Hidden + DeactivateWidget`，随后直接在记录的游戏线程执行 `open <target>`；删除 `WaitingAfterRange`、`RangeSettleDelay` 和全部自动 `GoToRange` 调用。新战局 `UWorld` 激活后再调用原生 `HideLoadingScreen`，并对精确的 `UMG_LoginGate_C/UMG_Login_C` 实例执行 `Collapsed + RemoveFromParent`。30 秒维护窗口只重试上述前端白名单，不能扩展到确认页或局内菜单。
- 首发服务器状态机在 `DidBroadcastRoleSelection` 证明原生 StartMatch 已完成后，补发 `ClientStartOnlineGame -> ClientMatchHasStarted -> ClientRoundHasStarted`，再延迟重试 `ClientSelectRole`。这与中途加入的比赛状态追赶语义对齐，但不会在 StartMatch 前提前宣告回合；纯策略边界由 `JoinUiSyncPolicyTests.cpp` 固定。
- 不拦截 ESC、不伪造确认结果、也不在 travel 前强行清空 LocalPlayer 确认队列；修复只弹出前端菜单栈并补齐真实比赛生命周期，保留正常对局角色菜单和合法确认页语义。
- `Tools/Frida/armory_probe.js` 已增加 GoToRange、Range ESC、前端 widget 构造/激活/停用/移除、LoginGate、WaitingTips、战局菜单、确认页、GameInstance 和 client-start 生命周期观测点；`fieldmod.native_call` 记录 `object_class`，避免把同名 `Construct/RemoveFromParent` 误归类。

### 最终运行验收

- 冷启动会话：`local-pve/20260820-194050`；客户端日志：`clientlogs/clientlog-20260820_194128.txt`；Frida：`frida-captures/20260820-range-ui-final-clean/events.jsonl`。
- 客户端日志顺序为 `Match transition queued -> Deactivated frontend menu -> Connecting directly to match`。Frida 中 `GoToRange/K2_GoToRange=0`，`DeactivateWidget` 一次 enter/leave，随后各一次 `ClientStartOnlineGame`、`ClientMatchHasStarted`、`ClientRoundHasStarted`、`ClientSelectRole` enter/leave；`ShowConfirm=0`、`ExitRange=0`。
- 首发玩家在没有进入靶场的情况下完成选角、装备确认和 FIXER 首生；ESC 显示正常的 `IN GAME` 角色界面，没有 `LEAVE MATCH / PLEASE CONFIRM YOUR COMMAND`。随后 AI 击杀触发 lifecycle 1，显式原生 `ServerRestartPlayer -> ServerQuickRespawn` attempt 0 完成，再次 `Spawn complete`，证明复活接线未回归。
- 平台覆盖层基线为 `frida-captures/20260820-platform-overlay-direct-baseline/events.jsonl`；修复后探针会话为 `frida-captures/20260820-platform-overlay-auth-detach-cold1/events.jsonl`，冷启动会话为 `local-pve/20260820-235614`。事件顺序为新 world 的 `HideLoadingScreen`、`UMG_LoginGate_C::RemoveFromParent`、`UMG_Login_C::RemoveFromParent`，之后进入完整 client-start/选角/出生链。
- 第二次不挂 Frida 的独立冷启动 `local-pve/20260821-000151` 中，20 秒时只显示对局的 `WAITING FOR THE GAME TO START`，40 秒后进入角色选择且平台连接层没有回弹；右上角齿轮打开 `YOU ARE IN GAMING / LEAVE MATCH` 正常对局菜单，而不是靶场确认页。
- 最终 Release 构建与游戏目录部署的 `Payload.dll` SHA-256 均为 `B60AB8BD76EF6918CFDA8071B224E8077B2A67E0BB80DE0F34CCC4D07F803CD5`。

## 13. 原生 progression / quest 链（2026-08-20）

以下结论仅适用于第 1 节固定 EXE SHA-256。

### SDK 布局与原生实现

- `UPBProgressionManager` 的 `ProgressionMap` 在 `+0x58`，`QuestManager` 在 `+0xA8`；`UPBQuestManager` 的 `QuestMap`、`ProgressionProgress`、`LastProgressionProgress` 分别在 `+0x30`、`+0x80`、`+0xD0`。固定构建运行时实际创建 762 个 progression 节点和 811 个 quest 定义；节点类型分布为 PlayerLevel 69、CharacterLevel 174、WeaponMaster 22、WeaponMain 491、WeaponChallenge 6。
- `GetPlayerLevelProgression` exec RVA `0x0184EB00` 调用 RVA `0x01715FE0`，按 `Player_Level{N}` 构造 ID；`GetCharacterLevelProgression` exec RVA `0x0184E7F0` 调用 RVA `0x01714560`，按 `{Character}_Level{N}` 构造 ID；两者最终通过 RVA `0x01716350` 查询 `ProgressionMap`。
- `UPBProgression::RefreshProgress` exec RVA `0x0184F8A0` 跳转 RVA `0x0172E7B0`：遍历节点 `EventArray (+0x88)`，从 QuestManager 当前进度读取事件完成数并写回事件 `+0x3C`，再更新节点 `ProgressionState (+0x98)`。Quest 读取核心 RVA `0x0175EFC0` / `0x0175F720` 使用 `ProgressionProgress (+0x80)`；`OnMatchHasStarted` exec RVA `0x0185A2F0` 跳转 RVA `0x0176B8F0`，把当前进度复制到 `LastProgressionProgress (+0xD0)`，形成赛前/赛后快照。
- QuestManager 初始化 RVA `0x017631E0` 注册登录、查询完成和实时事件回调，然后由 RVA `0x01763EE0` 从数据表物化 QuestMap。登录回调 RVA `0x0176B7A0` 通过 MetaGateway 发出空请求；与同一登录时刻动态观测到的 `/mission.Mission/QueryProgress` 空 payload 对应。成功完成回调 RVA `0x0176E310` 把响应内两个容器解码到 `ProgressionProgress (+0x80)` 和私有运行时容器 `+0x120` 后广播刷新；实时事件回调 RVA `0x0176E670` 增量更新同两类容器。`QueryProgressResp` 的 protobuf 字段名和 `+0x120` 容器语义尚未完整恢复，不能按现有 tentative proto 猜测实现。

### 固定构建动态证据

- `frida-captures/20260820-progression-state/events.jsonl` 中，登录前 762 个 progression 节点状态全部为 0，811 个 quest 定义已存在，但当前/上一份 QuestProgress 都为空。样例节点包含 `Player_Level2 -> Player-LevelUp2 -> ABGNSFW01`、`Player_Level3 -> SpaceCoin x600` 和六角色 Level 2/29/30 外观奖励。
- 登录后 `/playerdata.PlayerDataClient/GetDataStatisticsInfo` 成功返回 14 个精确 key，CareerManager 变为玩家 70 级、六角色 30 级；同批 `/mission.Mission/QueryProgress` 请求 payload 为 0，响应 `error_code=1` 且 payload 为 0，因此 QuestProgress 没有被写入。
- 原生 PROGRESSION UI 随后能显示角色 Level 30 / MAX LEVEL，但各等级事件仍是 `0/N`、节点锁定；奖励 tile 同时有绿色 owned 标记。前者证明 Career 等级快照已经接通而 QueryProgress/QuestProgress 未接通，后者来自 `QueryAssets` 全定义 owned 兼容路径，不能当作 progression 奖励已结算。

### 当前服务端缺口

- `GetDataStatisticsInfo` 仍按全局配置返回固定等级和零 EXP，没有按已认证玩家读取 profile/repository；现有 profile 的 level、experience、statistics 字段没有进入该原生 RPC。
- TCP dispatch 没有 `/mission.Mission/QueryProgress`，因此客户端收到通用错误 1；`mission.proto` 中 QueryProgress 请求/响应仍为空 tentative 声明，需先恢复准确 wire schema，再实现按玩家持久化的 quest/event progress。
- `AddDataStatisticsInfo` / 对局后 settlement 写回没有 dispatch 或持久化实现。BattleLog 只记录对局日志，没有把经验、等级、事件进度、货币与奖励作为幂等事务写回 profile、QuestProgress 和 entitlement。
- `QueryAssets` 全定义 owned 会掩盖真实奖励发放。完整接线需要把 entitlement 改成逐玩家所有权，并将 progression 完成、奖励发放、货币变更和新物品/已读状态放进同一可重试事务。

## 14. Dedicated 对局结束与优雅退场（2026-08-20）

以下结论仅适用于第 1 节固定 EXE SHA-256；仓库基线为 `08848b9e6160e2d76002d9402b7d0762bf7370b2`。

### 原生生命周期与进程边界

- `APBGameMode` 在 `WaitingPostMatch` 内按 `MatchSubState` 依次分派：`ShowingMatchResult -> vtable +0x9C0 / RVA 0x0162B190`、`MatchEnding -> +0x9C8 / RVA 0x0162AE80`、`WaitingToEndGame -> +0x9D0 / RVA 0x0162B1C0`。分派主体和倒计时 tick 分别在 RVA `0x0163FE70`、`0x016491E0`；GameState 的 `MatchSubState`、`RemainingTime` 分别在 `+0x290`、`+0x340`。
- `ShowingMatchResult` 和 `MatchEnding` 分别装载配置 `ShowingMatchResultTime (+0x38C)`、`MatchEndingTime (+0x390)`。`WaitingToEndGame` thunk 从 GameMode `WaitingToCleanUp (+0x404)` 读取最终清理窗口、清 `+0x4C4`，再跳到 RVA `0x0163EFD0`。
- RVA `0x0163EFD0` 在 dedicated NetMode 下以原生 `FTimerManager` 延迟调用 RVA `0x019EFEE0` 的 `RequestExit(false)`；等待值小于等于 0 时立即退出。因此固定构建的服务端是 **process-per-match**，外层 service/launcher 在退出后拉起新进程是设计边界，不应通过拦截 `RequestExit`、调用 `RestartGame` 或强留旧 world 改成进程内循环。

### 掉线现象的两层根因

1. `MatchEnding` 遍历服务器侧 PlayerController 并进入原生 `GameHasEnded`。PB override RVA `0x015A7770` 先调用 RVA `0x015C8CF0` 的 InGameMenu 退场，再转 Engine `ClientGameEnded`；headless controller 的本地 UI root `+0xF8` 为 null，但 RVA `0x015C8D0E` 仍读取其 `+0x118`，造成 access violation。
2. 修复崩溃后，dedicated 的 `WaitingToEndGame` 分支会直接安排进程退出，却没有调用已经存在的 `APBGameMode::NotifyAllClientsReturnToMainMenu` RVA `0x01633990`。该函数原生遍历 controllers 并调用 `ClientReturnToMainMenuWithTextReason`；缺少它时，客户端只能在 socket 消失后走 `HOST CLOSED THE CONNECTION` 网络失败页。

同一 null-read 栈已在 2026-04-21、2026-07-31、2026-08-16 和 2026-08-20 的 crash context 中重复出现。2026-08-20 会话的关键 RVA 栈为 `0x015C8D0E -> 0x015A7827 -> 0x0380A810 -> 0x01A68EC9 -> 0x01BCC122 -> Payload ProcessEvent hook -> 0x0162AFC4 -> 0x0163FF25`，故“服务重启”是崩溃或正常退出后的外层结果，不是最早故障点。

### 当前最小兼容实现

- 服务端在 RVA `0x015C8CF0` 只增加一个 headless guard：当 controller 为空或 `controller +0xF8` 为空时跳过不可能存在的本地 InGameMenu 操作；其余 controller 和后续原生 `ClientGameEnded` 生命周期保持原样。
- 服务端在 RVA `0x0162B1C0` 进入最终清理窗口时先调用原生 RVA `0x01633990` 广播返回主菜单，然后显式复现该三指令 thunk 的 `+0x404` 读取、`+0x4C4` 清零，并调用原生 RVA `0x0163EFD0`。不能依赖 inline trampoline 重定位这个以无条件 `jmp` 结尾的短 thunk；实测那会发送 RPC，但不创建退出 timer。
- 未伪造结算 UI、未直接 client travel、未压缩正常结果/MatchEnding 时间，也未吞掉 `RequestExit`。修复只补 headless 空指针边界和原生已有但 dedicated 分支漏掉的通知。

### 固定构建运行时验收

- 原始失败会话 `local-pve/20260820-173008` 在服务端/客户端均到达 `K2_StartShowingMatchResult` 和 `K2_MatchHasEnded` 后，于 17:45:59 在上述 null-read 崩溃；最新 crash 目录仍停留在 17:48:23，没有由修复后会话产生的新目录。
- 完整通知会话 `local-pve/20260820-213429`；Frida 为 `frida-captures/20260820-match-lifecycle-final/{server,client2}/events.jsonl`。服务端依次到达 `ShowingMatchResult`、`MatchEnding`、`ReadyToEndGame=true`，记录 headless guard 和 native return-to-menu broadcast；随后原生 `ClientReturnToMainMenuWithTextReason` 完成，客户端进入 `ClientGotoState(Inactive)`，服务端 heartbeat 的 PlayerCount 从 6 变为 0。连接在进程退出前已主动退场，不再由 socket close 驱动。
- 清理 timer 定点验收为 `frida-captures/20260820-match-lifecycle-cleanup-final/events.jsonl`：PID 16040、GameMode `0x221255FB050`。仅为缩短 smoke test 把 `WaitingToCleanUp` 从正式配置 60 秒临时设为 2 秒；hook 返回后 `FTimerHandle=436207637`（非零），进程随后由原生 timer/RequestExit 自行退出。
- 最终 Release/x64 `Payload.dll` SHA-256 为 `B8D757EF0131D70D5792756E8E23A59FBEB7F5301306091A1F74BD0C8EF997D9`。原始回滚副本为 `%LOCALAPPDATA%\ProjectRebound\payload-backups\20260820-match-lifecycle\Payload.before-match-lifecycle.dll`，SHA-256 `12D4F6E891B9D46A91B0C8341B67974A16778198DB70CD2CBEA4312CADB04920`。

## 15. 整局结束到下一局边界（2026-08-20）

以下静态地址仍只适用于第 1 节固定 EXE SHA-256。此节把“下一回合”和“下一局”分开：前者在当前 World/进程内，后者不在 dedicated 对局状态机内。

### 服务端结果冻结与分发

- Engine `AGameMode::EndMatch` RVA `0x0327D9A0` 把 MatchState 切到 `WaitingPostMatch`，PB handler RVA `0x0162ABD0` 随后进入结果冻结函数 RVA `0x01637850`。
- RVA `0x01637850` 先调用 APBGameMode vtable `+0xA38`（RVA `0x01639A20`）。该函数把 winner、队伍比分和 MVP 写入 `APBGameState::MatchResult (+0x408)`：`MatchWinnerTeamID +0x410`、`TeamMatchScores +0x418`、`MVPPBPlayerState +0x428`；每回合积累的 `RoundResults` 位于 `+0x430`。
- 随后 RVA `0x01637850` 遍历 PlayerController，对满足 vtable `+0xA68` 判定的玩家调用 RVA `0x0183E010`。该函数是 `APBPlayerController::ClientMatchHasEnded(FPBMatchResult)` 的 RPC serializer，入参正是 `GameState +0x408`。最后还会清理在线 session 成员、调用赛后统计/事件汇总 RVA `0x01645770`，并把 GameMode 私有结束标记写成 `+0x420 = 4`、`+0x41C = -1`。
- 客户端 RPC implementation RVA `0x015A7C10` 调用 RVA `0x01681300`，把收到的 `FPBMatchResult` 复制到客户端 `APBGameState::MatchResult`，然后进入 K2 `MatchHasEnded`。动态记录中 winner、`[0,2]` 队伍比分、MVP 和参与者统计均完整，说明比分/结果界面的原生数据链没有缺口。

### 结果展示、退场与客户端 session cleanup

- 结果冻结之后，服务器继续使用原生时序：`ShowingMatchResult`（本次配置/实测 5 秒）→ `MatchEnding`（10 秒）→ `WaitingToEndGame`。`StartMatchEnding` RVA `0x0162AE80` 会进入每个 controller 的 `GameHasEnded`；dedicated 只需要第 14 节的 headless UI guard，不能跳过后续 Engine/客户端退场逻辑。
- `NotifyAllClientsReturnToMainMenu` RVA `0x01633990` 发出的 `ClientReturnToMainMenuWithTextReason` 在客户端经 RPC exec RVA `0x0380B860` 落到 PB override RVA `0x015A8430`。该 override 关闭局内 UI/状态后调用 `UPBGameInstance::GotoState` RVA `0x015650F0`，再调用 Engine 基类 RVA `0x034EF180` 执行正常断开/旅行。
- 固定构建的 GameInstance FName 全局已由静态初始化表反解：`0x05C72990=None`、`0x05C72998=PendingInvite`、`0x05C729A0=WelcomeScreen`、`0x05C729A8=MainMenu`、`0x05C729B0=MessageMenu`、`0x05C729B8=Playing`。PB override 读取 `0x05C729A8`，明确把 pending state 设为 `MainMenu`，不是进入网络失败状态。
- GameInstance 状态机 RVA `0x0156DB60` 比较 current `+0x200` 与 pending `+0x208`。从 `Playing` 退出到 `MainMenu` 时调用 RVA `0x0155D6C0`；普通对局（GameState mode 不是 `Menu`）由此进入 `CleanupSessionOnReturnToMenu` RVA `0x01553C80`，随后再进入 MainMenu handler RVA `0x0154FE20`。因此不应额外调用 `ExitMatchReturnToMainMenu`、直接 `ClientTravel` 或重复调用 session cleanup。

### 下一回合、下一局与外层进程

- `StartNextRoundLoop` 的 UFunction exec RVA `0x017F7480` 跳到 native RVA `0x01643F50`；它与 `CurrentRoundCount (+0x2A0)`、`RoundState (+0x298/+0x410)` 协作，只负责同一场 match 的下一回合。`RestartPlayers` 同样是出生/复活流程，二者都不是下一局。
- SDK 和固定二进制的 APBGameMode 生命周期没有 `StartNextMatch`、`ReturnToLobby` 或赛后 `RestartGame/ServerTravel` 路径。专用服在 `WaitingToCleanUp (+0x404)` 结束后调用 `RequestExit(false)`；下一局必须由新进程承载。
- `UPBMatchmakingManager` 的 `BP_FindGatheringMatch`、`StartConnectingMatchServer`、`ResetMatchmakingManager` 等入口属于返回主菜单后的 UI/subsystem 流程；没有从上述赛后 native 链自动重新排队的静态调用关系。玩家要开始下一局，原生边界是回到前端后再次匹配/加入，由控制面分配新的服务端进程。
- C++ `ServerWrapper` 的 exit watcher 会把仍处于 Running 状态的任意子进程退出记为 `process exit` 并调用 `RequestRestart(true, ...)`，所以它会用新 PID 拉起下一台常驻可用服务（通常轮换地图）；这不是游戏二进制内的下一局。`Backend/cmd/meta-tunnel/startgame.ps1` 和 `Tools/PVE/start-local-pve.ps1` 则没有赛后循环，后者的重试只覆盖初始启动/world travel 失败。

### 与奖励结算的边界

- `APBGameMode::GetMatchResultInfo` native RVA `0x016294A0` 生成每名玩家的 `FMatchResultInfo`；`UPBGameInstance::SaveMatchResultInfo` exec RVA `0x017F6990` 调用 native RVA `0x01583520`，后者只深拷贝到 `GameInstance::LocalMatchResultInfo (+0x4F0)`。`ClientMatchHasEnded` RVA `0x015A7C10` 本身不调用它。
- 本地 PVE 动态结果页已有完整 `FPBMatchResult`，但 `LocalMatchResultInfo`、career post-match settlement 和 `SaveMatchResultInfo` 参数仍为空。这更符合第 13 节尚未接通 `/mission`、statistics/settlement 写回的后端缺口，而不是回菜单时序缺口。
- 在准确恢复原生 settlement wire schema 和幂等事务之前，不应从生命周期 hook 伪造 `FMatchResultInfo`、主动调用 `SaveMatchResultInfo` 或注入 career settlement；这些行为会把 UI 缓存当作权威结算，并产生重复奖励风险。

## 16. 场景交互输入与受管控出生同步（2026-08-21）

以下静态地址、SDK 布局和构建产物只适用于第 1 节固定 EXE SHA-256；实现基线为 `13fe6a7a0e17fa2cb0d3a88635c3a0ab8eea9cd0`。本节在实际对局验证前停止，因此不得把这里的静态接线和编译通过表述为舱门、滑索已经运行验收成功。

### 固定构建静态链

- `APBPlayerController::BindGameInputComponetKey` native RVA `0x015C3570` 为 `InteractAirLockController`、`InteractAirLockHatch`、`InteractExpressTransit` 安装特殊动作绑定，并进入公共 pressed callback RVA `0x015AF300`。该 callback 再进入 `UPBInteractionManager` pressed/released RVA `0x0171C770` / `0x0171C810`。
- interaction state dispatcher RVA `0x0156A8A0` 只在状态低字节为 `Finish (3)` 时进入场景交互发送链；`ServerInteractWithScene` outbound wrapper RVA `0x017B7490`。其 ProcessEvent 参数为目标对象 `+0x00`、`EPBInteractionEventType +0x08`、客户端位置 `FVector +0x0C`，总大小 `0x18`。
- 固定 SDK 中事件 `14..19` 依次为开/关舱门、加/减压、连接/脱离滑索。`UPBInteractionManager` 的对象信息数组、Interactor、CurrentInfo 分别在 `+0x38/+0x58/+0x60`；`FPBInteractiveObjectInfo` 大小 `0x10`，已知 Interaction config 指针在 `+0x08`。`UPBInteractionConfig` 的事件、动作名数组、优先级、持续时间在 `+0x28/+0x30/+0x40/+0x44`。
- 用户反馈中的提示可见、长按不推进且射击/换弹正常，把首要缺口限定在特殊交互绑定到 Finish/场景 RPC 之前；它不支持直接修改舱门/滑索状态，也不支持由 Payload 主动调用交互 RPC。

### 当前兼容实现

- `Tools/Frida/armory_probe.js` 保持统一只读探针：新增特殊动作 Bind/Unbind、PlayerController 的 InputComponent/Pawn/AcknowledgedPawn/InteractionManager 快照、manager pressed/released、Start/Stop/Finish、scene RPC、airlock/transit Multicast 和上述 native boundary/backtrace。探针不写游戏内存、不改返回值、不主动调用 RPC；SDK 未命名的 `FPBInteractiveObjectInfo +0x00` 仅记录为 opaque raw pointer，不猜测语义。
- 受管控首次出生、中途加入、重生和换角现在都进入 `FinalizeLateJoinSpawn`。首发首次补 `ClientReadyAtStartSpot -> ClientGotoState(Playing) -> ClientRestart -> ClientRetryClientRestart -> ServerAcknowledgePossession`；中途首次在此基础上补齐 match/round/game-start；后续重生和换角只恢复 Playing 与 possession 链。
- `LastClientSyncedPawn + LastClientSyncedRespawnLifecycleId` 是客户端同步幂等键。死亡、活体换角和从 Spawned 状态直接排队的受管控重生都会取得新 lifecycle；同一对键不会重复发送，Pawn 或 lifecycle 任一变化都会作为新一代同步。活体换角检测到目标 Pawn 后不再绕过公共最终化。
- `ManagedPossessionSyncPolicyTests.cpp` 固定首发首次、中途首次、后续生成和同代 no-op，并单独覆盖同 Pawn/新 lifecycle、同 lifecycle/新 Pawn。2026-08-21 顺序 Release 测试为 11/11 passed；`armory_probe.js` 经 Node.js v24.14.0 `--check` 通过。

### 静态交付状态

- `Release|x64` Payload 使用 Visual Studio 18.3 / MSVC 14.50 顺序构建成功，0 warning、0 error。构建产物为 `Payload/x64/Release/Payload.dll`，大小 `1,403,904` 字节，x64 PE，SHA-256 `6B4B02740038EAA1E078FE0F37A9885EFC002363B48958C20D99C33D45B8B430`。
- 部署前游戏目录 `Payload.dll` 已备份到 `%LOCALAPPDATA%/ProjectRebound/payload-backups/20260821-interaction-sync/Payload.before-interaction-sync.dll`，备份 SHA-256 `B60AB8BD76EF6918CFDA8071B224E8077B2A67E0BB80DE0F34CCC4D07F803CD5`。部署后源文件和游戏目录目标文件 SHA-256 均为 `6B4B02740038EAA1E078FE0F37A9885EFC002363B48958C20D99C33D45B8B430`。
- 本次按人工交付边界没有启动游戏、没有附加 Frida、没有创建服务器、没有进入实际对局。运行验收由用户在部署后执行，结果应另行追加，不能回填为本节已有证据。

## 17. Dedicated seamless 目标图的上一局 HUD 根层（2026-08-24）

以下结论仅适用于第 1 节固定 EXE SHA-256。

### 动态根因与最小清理边界

- 在保留同一客户端连接和 PlayerController 的 `Warehouse -> OSS` seamless travel 中，目标图角色选择前仍可观察到上一局雷达、弹药、手模和赛况层。此时目标图 controller 尚无可玩 Pawn；GObjects 中仍有上一局的 `HelmetHUDContainer_C`、`UMG_InGameHUD_Mother_C`、`UMG_InGameHUD_TopScoreBar_TDM_C`、`UMG_InGameTopScore_TDM_C`、`UMG_MatchState_C`、`Effect_WinBoard_C` 和 `UMG_EndGameScoreboardPage_C` 实例。原生返回主菜单会移除这些根层，但 opt-in seamless 路径不会经过该 teardown。
- `APBPlayerController::NotifyClearInterface` 对目标图角色页上的残留无效；`PlayerController_BP_C::EventOnMatchEnd` 会错误地再次打开蓝/红结果板，不能作为清理入口。`APBHUD::K2_Hidden*` 能结束其自身结果/死亡状态，但不是雷达、弹药和手模根层的 owner。
- 当前兼容层只在精确的 owned seamless `PlayerController.ClientTravelInternal` 上 arm 一次，并在目标图第一个 start RPC 进入前执行：`K2_StopKillCamera`、`K2_StopQuickRespawn`、`K2_HiddenRoundResult`、`K2_HiddenMatchResult`、`K2_HiddenMatchResult_TDM`、`K2_HiddenSummary`，随后仅对上述精确生成类 token、且 `IsInViewport()` 的旧 `UUserWidget` 根执行 `Collapsed -> RemoveFromParent()`。上限为 24；不匹配角色选择、局内菜单和通用消息层，不写 Pawn、camera、input 或武器字段，也不处理 F/F5。
- 清理必须发生在目标图 start RPC 之前；这些生成类名也会被新局 HUD 复用，若在 `NotifyGameStarted` 返回后才枚举可能误删新建层。当前 gate 在 APBHUD 已就绪但 `detached=0` 时仍会消费；若未来样本证明旧根注册更晚，应把 `0` 视为有限重试条件，不能扩大白名单。

### 本地跨图验收

- 会话目录为 `local-pve/20260824-184132`，服务端 PID `11728` 在 `Warehouse -> OSS` 期间保持不变。服务端依次经过原生 `ShowingMatchResult -> MatchEnding -> WaitingToEndGame`，保留一个 engine tick 后进入 seamless travel，generation 从 1 增加到 2，同一连接在 OSS 被重新排入原生首发角色选择。
- 客户端日志 `clientlogs/clientlog-20260824_184210.txt` 记录：owned travel arm 后，在第二局角色页之前精确 detached 3 个 `HelmetHUDContainer_C`，随后 `Destination source-match layers detached=3` 与 `Finalized retained HUD state at destination startup`。Computer Use 画面确认旧雷达、弹药、手模和战斗 HUD 消失，角色选择页保留；确认角色并 Deploy 后，新局雷达、弹药和武器 HUD 重新创建。
- 服务端在第二局记录 `client_possession_sync result=native_initial_join`、非空 Pawn 和 `Spawn complete`；随后角色被 AI 击杀并进入原生等待复活输入。击杀后的客户端只读采样为 `StateName=Inactive, Pawn=0`，因此该样本不用于宣称第二局移动验收，只证明清理没有阻止新局 HUD 创建和服务端首次出生。
- `DirectMatchUiCleanupPolicyTests` 覆盖精确白名单正例及角色选择、局内菜单、MatchMessage 反例；Release 配置全套 12/12 tests passed。构建产物与游戏目录部署的 `Payload.dll` SHA-256 均为 `C1C9159922FDE6C9BC69399FBC24613B3E3B90059FBCAE390B57E27796B3913E`，部署前备份位于 `.tmp/runtime-deploy/20260824-184117-multimatch`。

## 18. OSS seamless 首命开场与相机边界（2026-08-25）

以下结论仅适用于第 1 节固定 EXE SHA-256；动态验收会话为 `local-pve/20260825-034524`，playlist 为 `Warehouse,OSS`。

### 根因与窄修复

- `ViewTarget == Pawn` 不是第一人称相机已经恢复的充分条件。固定构建的 `StartThirdPersonCamera` 可以在 ViewTarget 仍指向 Pawn 时把相机放到身体后方；因此旧策略在 `viewTargetMatchesPawn` 时消费恢复请求会留下 OSS 首命第三人称/开场相机。
- 更深一层的服务端 race 是 fresh seamless 目的图在角色确认栈内、Pawn 尚未生成时就把 native `ReadyToMatchIntro=false` 覆盖为 true。OSS 的开场镜头长于 DataCenter；提前 `StartMatch` 会让 bot 在 OSS 开场相机尚未交还时攻击/击杀首命，视觉上又会进入跟随身体的死亡相机。DataCenter 能正常播完开场并不能证明该 gate 正确，只说明其地图时序没有暴露同一 race。
- `JoinUiSyncPolicy` 现在只在 fresh seamless 首发玩家已经满足 `AreInitialPlayersReadyForStart()` 后恢复一次 destination native Ready。预生成阶段保留 native false；`RestartPlayers -> Spawn complete` 后下一次评估才恢复 true，随后沿用原生 `MatchIntro -> one NetDriver flush -> StartMatch`。
- 客户端 fallback 只在 owned seamless、已观察到目的图 round start、`Pawn == AcknowledgedPawn == PBCharacter`、角色 Alive 且相机 POV 已回到 Pawn 附近时执行一次原生 `StopKillCamera -> StopThirdPersonCamera`，并结束相应 HUD K2 状态。它不写相机/角色字段、不生成 ViewTarget RPC，也不处理 F/F5。

### 动态验收与限制

- 服务端日志的决定性顺序是：`Preserved pre-spawn destination ReadyToMatchIntro result=0 initial_players_ready=0` -> `RestartPlayers` -> `Spawn complete` -> `Restored post-spawn destination ReadyToMatchIntro result (native=0)` -> `Native MatchIntro observed` -> `Completed native MatchIntro NetDriver flush` -> `StartMatch`。
- 客户端日志 `clientlogs/clientlog-20260825_034601.txt` 先记录远端 POV 距 Pawn `7876.77` uu，随后在 POV 回到 Pawn 附近时记录 `camera_action=stop-kill-and-third-person ... camera_distance=21.8231`。OSS 初始首命只读快照 `.tmp/camera-model-snapshot-034524-oss-before-w.json` 同步记录：`StateName=Playing`，`Pawn == AcknowledgedPawn == PBCharacter`，`life=Alive(0)`，`ready=1`，Current/PendingWeapon 非空；ViewTarget 为该 Pawn，实际 CameraComponent 为 `FirstPersonCamera`，camera cache `(3634.87,3026.80,-681.67)` 距 Pawn root `(3638.80,3026.80,-703.40)` 约 22 uu，不再位于身体后方。
- 同会话后续生命的输入只读检查显示 `PBInputComponent`、`PlayerInput`、`PBCharacterMovement` 均非空，`IsMoveInputIgnored=false`、`IsLookInputIgnored=false`、MovementMode=Flying(5)、MaxWalkSpeed=600。Computer Use 的离散 W/A/S 合成按键没有产生 Axis/Velocity 事件；固定游戏对移动使用 raw input，故该自动化样本不能作为“实际键盘移动已通过”的证据，也不能反推游戏输入仍被锁。人工原始设备移动仍需作为最终 acceptance。
- 原生 camera tracer 中 `StartThirdPersonCamera` 在角色存活一段时间后出现并约 8 秒后随 Pawn 清空而结束，与后续 bot 击杀/死亡相机吻合；不能把死亡相机重新归因于首命开场未结束。
- Release 全套 14/14 tests passed。构建产物与游戏目录部署 DLL SHA-256 均为 `10B1CB19BE31123DA8F167502C99D262ABF156230894BC7558D8E1A79260A929`；部署前备份位于 `.tmp/runtime-deploy/20260825-034514-post-spawn-native-intro-ready-gate/Payload.previous.dll`。

## 19. Toolbox 内建 MetaTunnel 与无脚本本地 PVE QoS（2026-08-25）

以下固定地址与 Payload 策略只适用于第 1 节固定 EXE SHA-256；ProjectRebound 实现基线为
`e7711e30ee82ff003520a37d7fb314b945d57757`，Toolbox 上游基线为
`63e5ee598b860b8f5d53dd147d436921ad290098`。

### Toolbox 进程与认证边界

- MetaTunnel 由 Go 1.26.6 以 `GOOS=windows GOARCH=amd64 go build -buildvcs=false -trimpath` 固定构建，连续两次独立构建的大小均为 `9,819,136` 字节、SHA-256 均为 `24BE4159F3B49A2D8225E0C1BF32581A7B30FE735F7DFB7DDE238886E31CCBBD`。Toolbox 嵌入清单固定源码 revision、工具链、命令、大小、x64 PE 与完整哈希；提取目标按完整哈希分目录并在损坏时原子自修复。
- PVP、加入房间和 Toolbox 本地 PVE 的玩家入口统一启动受管 MetaTunnel；监听端口均由 OS 在 loopback 随机分配。访问令牌只经匿名 stdin 发送，401 和到期前刷新共用认证 singleflight，动态 token pump 不把令牌暴露到参数、环境变量或日志。无账号本地冒烟已验证 readiness PID 精确匹配、随机 HTTP/TCP 均为 `127.0.0.1` 且 `/_meta-tunnel/health/live` 返回 `live`。
- 状态跟踪改为带角色、协议、动态端口、owner PID 和精确 executable identity 的类型化记录；停止与 PID reuse 检查不再依赖固定 `127.0.0.1:8000`。Production Server 页面仍走原生产配置，不接收本地 PVE LogicServerURL。
- 安装/更新不再自动安装 Node，也不会创建或覆盖已有 `BoundaryMetaServer-main`；只有显式卸载会清理旧 `nodejs` 与旧 MetaServer 目录。

### 固定构建 QoS 改写边界

- Toolbox 的 Rust QoS 服务复现旧兼容协议，但使用随机 loopback TCP/UDP：HTTP `GET/HEAD /servers` 返回固定 region/location 与动态 UDP 端口；UDP 只对长度至少 11 且首字节为 `0x59` 的请求返回 `[0x95,0] + request[11..]`。客户端仅收到 `-LocalPveQosDiscoveryUrl=http://127.0.0.1:<port>/servers` 和 `-LocalPveQosReadyEvent=Local\ProjectRebound.Qos.<32 lower hex>`。
- 固定构建全局 FString `UnityMatchmaker.ChinaDiscoverURL` 位于 RVA `0x05C63C88`，原生 initializer 位于 RVA `0x0068ADE0`；Payload 只接受 initializer 精确前缀 `48 83 EC 28 BA 51 00 00 00 48 8D 0D 98 8E 5D 05` 和原始发现 URL。只有非服务端、两个 opt-in 参数均严格有效、完整 EXE SHA-256 与 SizeOfImage 已匹配时才可安装临时 initializer hook 或原位改写。
- 改写还要求 FString 可读写、容量足够，并在写入、Num 更新和 readback 全部成功后才设置 readiness event；部分参数、URL/event 格式错误、原值/前缀/内存保护不符或 15 秒超时均 fail closed。普通客户端和服务器启动完全 no-op，不依赖 PowerShell、Python 或 Frida。

### 构建、部署与验收状态

- Toolbox Rust 单元测试 `169/169`、前端测试 `41/41`、格式检查、Tauri check、前端生产构建与 Tauri release 构建均通过；Payload Release/x64 全套 `15/15` tests passed。MetaTunnel Go tests 通过，嵌入 EXE 的独立本地 readiness/health 冒烟通过。
- 新 `Payload.dll` 大小为 `1,564,160` 字节，构建源与游戏目录目标 SHA-256 均为 `C8306242FE602C0F8AFB164B4B4B3BD6AF0B2091EDBFF57CC0CF269E62305317`。部署前目标已备份到 `.tmp/runtime-deploy/20260825-153647-toolbox-metatunnel-qos/Payload.previous.dll`，备份 SHA-256 为 `10B1CB19BE31123DA8F167502C99D262ABF156230894BC7558D8E1A79260A929`。
- 2026-08-26 使用与内嵌证书完全一致的项目签名材料为最终 Tauri release EXE 签名并加 Sectigo RFC3161 时间戳；最终 EXE 大小为 `47,317,392` 字节、SHA-256 为 `699CFDD1ED092095DCAEA708C5CDD26593FAB58A1785454F419F5A606F1E690D`，签名证书 SHA-256 为 `18440D9B14590787C0FC5217FEB64AFCFB8DBF64243B4AB031D5E0B12D7A7590`。自签根在本机 Authenticode 链状态为 `UntrustedRoot`，符合 Toolbox 完整性门允许的证书匹配边界；未绕过门禁。
- 首轮签名构建和修复后的最终签名构建各完成一次独立 GUI 冷启动，均通过完整性门并进入主界面。首轮发现前端 `capabilities_updated` 处理器与后端 `fresh_token()` 无条件重发事件形成反馈循环；后端现只在身份配置实际变化时发事件并保留仅驻留运行时的 auth ticket。最终构建第二次冷启动中调试日志计数在 9 秒观察窗内保持 `14` 不变。
- 2026-08-27 确认邀请码兑换的冷启动缺口：Toolbox 从持久化 session 恢复身份时按设计不恢复 Steam ticket，但旧兑换路径仍要求 `AuthIdentity.auth_ticket` 非空，因此稳定返回 `Steam verification ticket is unavailable`。当前实现不落盘 ticket；仅在兑换且内存票据缺失时调用内建 Steam helper 获取新票，并在提交邀请码前要求其 SteamID 与 Toolbox 当前身份精确一致。既有内存票据保持复用。Toolbox `cargo test --features lab-testing --lib` 为 `194/194`、Tauri tests 为 `7/7`；本次未消费真实一次性邀请码，因此只把回归与桌面壳接线记为自动验证通过，不把生产邀请码消费记为动态通过。
- 2026-08-26 再次 `git fetch --prune origin` 后，Toolbox `origin/main` 仍为 `63e5ee598b860b8f5d53dd147d436921ad290098`，与集成基线一致；但 GUI 的发布策略仍显示本地 `0.9.0 -> 0.9.1`、`Update required`。同时固定游戏目录仍有 `41` 个未受当前 Toolbox 管理的旧 `meta-tunnel.exe`，触发 `Unexpected processes`。因此本节只把签名门禁和 GUI 冷启动记为动态通过，**没有**把本地 PVE 入局或 QoS readiness event 记为通过；清理旧进程并解决发布版本策略后仍需完成最终动态 acceptance。

## 20. 死亡角色页 Deploy 与原生复活入口（2026-08-26）

以下静态地址、运行时状态和部署产物只适用于第 1 节固定 EXE SHA-256；动态验收会话为 `local-pve/20260826-152025`，地图为 Warehouse PVE Normal。

### 根因与固定构建语义

- `APBGameMode::RestartPlayer` RVA `0x0163D250` 不是死亡角色页的完整复活入口。旧样本中 Controller 仍挂有死亡 Pawn 时，`ServerConfirmRoleSelection -> RestartPlayer` 会向客户端发送旧角色的 `ClientRestart`，随后尸体销毁并留下 `StateName=Inactive, Pawn=null`；当 Pawn 已清除后，重复 Deploy 仍然只进入同一 `RestartPlayer`，不会生成新 Pawn。
- `APBGameMode +0x428/+0x430` 是另一套 deferred respawn queue mode/queue，不是当前 PVE 的冷却谓词。失败样本中 `+0x428=0`、queue count `0`，因此不能用该字段判断是否保留角色确认内的 raw restart。
- PB 的死亡复活入口是 `APBPlayerController::ServerQuickRespawn` RVA `0x015C14C0`。它先执行本构建的复活许可/冷却判定，再进入 observer/controller 清理和 Engine restart 链；成功时在 RPC 返回前同步生成并占有目标 Pawn，冷却拒绝时不生成 Pawn。

### 最小修复

- 对从 `AwaitingRespawnInput` 进入的同 Controller `ServerConfirmRoleSelection`，保留原生角色与 pre-order commit，但无条件拦截其同步 `RestartPlayer`；Pawn 是否存在及 `+0x428` 只记录为观测值，不再参与决策。活体角色修改仍只提交下一命，初始角色确认和其他 Controller 保持原生。
- 角色 commit 验证成功后，以 manager restart permit 调用一次精确的 `ServerQuickRespawn`。若目标角色 Pawn 已同步产生，则进入公共 possession/HUD finalizer；若没有 Pawn，则恢复 `AwaitingRespawnInput`、`SpawnAttempts=0`、`ExplicitNativeRespawnDispatched=false` 和 respawn gate=false。冷却拒绝不再进入 2 秒后的 `RestartPlayers -> QuickRespawn -> Suicide` 通用回退，因此后续 Deploy 或 F 可重新提交原生意图。
- `ServerPreOrderInventory` 继续只表示配置变更，不会在死亡等待期间提前消费复活意图；普通 F 发出的精确 `ServerQuickRespawn` 转发路径未修改。

### 动态验收与交付

- 首生 PROBE 正常完成。首次死亡后从角色页选择 SPIKE 并 Deploy：日志记录 `restart_suppressed=1, existing_pawn=1, native_queue_mode=0`，随后 `origin=death_role_deploy ... native_result=spawned`；SPIKE 的两个 weapon-detail overlay 均为 `result=applied`，目标 Pawn 经公共 finalizer 完成。客户端人工确认视角、移动和 UI 表现正确。
- 后续活体把 PEACE 提交为下一命，再次死亡后按原生 F：日志记录 `origin=explicit_f request_kind=ServerQuickRespawn`，返回 `selected=PEACE possessed=PEACE` 并完成公共 finalizer。继续测试到 lifecycle 9 时，角色页 Deploy 又正确生成 SPIKE、Sniper，普通 F 连续正确生成 SPIKE、Sniper；全程没有 `managed_explicit_fallback`、Suicide fallback 或 spawn timeout。这证明角色页修复没有破坏普通 F 复活路径。
- `RespawnStatePolicyTests` 连同全套 Payload 策略测试为 Release `15/15` passed；主 DLL Release/x64 构建为 0 warning、0 error。源码产物与游戏目录 `Payload.dll` SHA-256 均为 `84CEE6BEF59A180049F876C54E25C5A48DCC254739F13BBE540F969AA8A0C2AE`，大小 `1,570,304` 字节。部署前版本备份为 `%LOCALAPPDATA%/ProjectRebound/payload-backups/20260826-152009-death-role-pb-quick/Payload.previous.dll`，SHA-256 `2011B8FB243F652D0DA846E3B7715BB9D394F1F6931DD70CAEB29BACB195F748`。

## 21. 严格名单的 NMT_Login 收发链（2026-08-26）

以下地址仅适用于第 1 节锁定 EXE。候选先由 Luna max/IDA 静态分析定位，随后在本地 Dedicated `NetMode=1` 与远端 Client `NetMode=3` 的真实连接中完成只读运行时验证；验证过程不采集 URL、Unique ID、token 或消息正文。

### 已定位的静态收发链

- 服务端 UWorld::NotifyControlMessage 候选位于 VA 0x1436CDCE0 / RVA 0x036CDCE0。其 case 5 为本构建的 Login 控制消息，依次读取 FString RequestURL、第二个 FString、Unique ID 和最后一个 FString。因此 Tools/Frida/armory_probe.js 的 nmtLoginType 已从错误的 0x0B 修正为 0x05。
- 客户端高度可信的挑战处理/登录发送候选位于 VA 0x143484770 / RVA 0x03484770。其 case 3 构造 FOutBunch，并在 RVA 0x03484EDE 写入消息类型 5。
- 同一路径的关键位置为：RVA 0x03484ED2 构造控制消息 bunch；0x03484F25 序列化 URL；0x03484F35 序列化第二个 FString；0x03484F48 序列化 Unique ID；0x03484F58 序列化最后一个 FString；随后经连接对象虚表 +712 发送。
- 当前静态调用序列可表示为 Challenge(type=3) -> RVA 0x03484770 case 3 -> Login(type=5) -> 服务端 RVA 0x036CDCE0 case 5。

### 未通过的安全门与下一步

- 动态会话 `.tmp/native-admission-runtime-20260826` 已记录客户端 RVA `0x03484770` 收到 challenge type 3，紧接着 RVA `0x03484EDE` 写入 message type 5；服务端随后在 RVA `0x036CDCE0` 收到 type 5，并依次进入 PB PreLogin RVA `0x01639D90` 与 Engine PreLogin RVA `0x03290DE0`。客户端随后完成 PlayerState、角色选择与出生，证明这条收发/PreLogin 顺序在锁定构建的 Dedicated 路径上成立。
- 本次只读 hook 没有读取发送对象、虚表目标或字段内容，因此没有单独证明连接对象类型和虚表 `+712` 的运行时归属；这些静态细节也不是启用准入写入的充分条件。
- 尚未证明四个字段中哪个 FString 可安全扩展，也未验证 FString 的原生分配器、所有权、长度/容量及释放语义。不得把 join grant 猜测写入 RequestURL、命令行或其他字段。
- 下一步应在同样的路径/进程/大小/SHA-256 门禁下恢复最后一个 FString 的原生构造与所有权，再实现只在 NMT_Login 组包瞬间写入的内存 grant，并在服务端 type 5 解码与进入 PlayerArray 之前完成验签和重放检查。
- 在客户端 grant 注入、服务端 PreLogin 准入、原生阵营写入、Listen authority 和 P2P 本地主机旁路全部通过前，StrictRoster native gate 保持 false；direct open、PostLogin 回退和结构偏移写入均不允许。

## 22. 严格名单成员断线重连闭环（2026-08-27）

以下实现仍只适用于第 1 节锁定 EXE。它补齐的是已冻结成员在同一 authority/world 存续期间的重连；不允许名单外加入、P2P 主机迁移或 authority 进程丢失后的重建。

### 连接事实与代次

- authority Payload 只在 `PostLogin` 已完成 Meta 席位与原生 Team/Camp 读回后产生一次 `CONNECTED` 事件；对应 controller 退出时产生一次 `DISCONNECTED`。事件带单调 sequence、attempt、player、grant JTI、connection generation 与 route generation，经命名管道由 Toolbox 上报，不携带 bearer token 或签名 grant。
- P2P 与 Dedicated 共用同一事件语义和 Meta 状态转换。重复的同 generation 连接/断线报告幂等；只有 Meta 已记录该席位 `DISCONNECTED` 后才可签发下一份 join grant。在 `CONNECTED` 状态直接请求重连返回 `MATCH_CONNECTION_STILL_ACTIVE`。
- 新 grant 的 connection generation 必须精确加一，仍绑定冻结快照中的同一 team、team slot 和 logical slot。旧 generation、旧 JTI、重复消费或不匹配 route generation 均 fail closed。
- P2P 本地主机的 authority-ready 已代表其本地席位接入，不走远端 `PreLogin` 重连。主机进程/world 丢失时必须中止 attempt；v1 不迁移主机，也不把 `retry` 用于主机恢复。

### 构建和自动验证

- Payload Release/x64 与测试构建均通过；CTest 为 `17/17`，覆盖 connected/disconnected 事件幂等、游标管道和重连授权消费。最终 `Payload.dll` 大小 `1,713,152` 字节，SHA-256 为 `EB61DAD112E06BFF5ADCD9EE34FF02B5DCAC85ED5CFC6DE5872D6E38C366F07F`。
- Toolbox `cargo test --features lab-testing --lib` 为 `188/188`，Tauri tests 为 `7/7`；签名实验 CLI 大小 `26,515,232` 字节，SHA-256 为 `C25A91835EF348095C271333AC0FFFDD44E216B433BD58629413C9B4CF9637BF`。authority admission/connection 上报的瞬时失败保留游标重试，不再让实验 CLI 错误地销毁仍存续的对局。
- 后端快速测试及真实 PostgreSQL 的 Dedicated/P2P lifecycle 集成测试通过；本地 `can_retry_connection` 只有在冻结席位不再是 `CONNECTED` 时才开放。实验控制平面 SHA-256 为 `F885E9F85A6E3BC7C3AE3F4246DC399AAC14EFA19EAC675430BCE66DF127EF92`。
- 新 API 冒烟实际完成 `CONNECTED(gen=1) -> DISCONNECTED -> grant(gen=2) -> 拒绝旧 grant -> CONNECTED(gen=2) -> COMPLETED`；重复连接/断线回报均保持 `RUNNING`。最终交付包 `.tmp/strict-roster-lab-20260827-reconnect-r3.zip` SHA-256 为 `C340172AB9E5324CA35C78508C9C471231F272DB1511314BCA1A0AE8ADBDD77C`，解压后的 14 项 manifest 回读通过。
- 两台机器上的真实验收仍需人工完成：先确认两名玩家各自拥有可移动/射击 Pawn，再只关闭从机游戏进程、保留其实验 CLI；authority 记录 `DISCONNECTED generation=1` 后在原 CLI 输入 `retry`，确认 generation=2 回收同一席位并第二次实际出生。未取得这份双机证据前，不把真实重连标记为动态通过。

## 23. LAN 严格名单实验的 Meta 服务分层与冻结成员重挂载（2026-08-27）

- Boundary 的 `-MetaServerUrl` HTTP 流程和后续原生 Logic TCP 都不是 match-lobby Control Plane 的接口。只把实验 MetaTunnel 指向 Control Plane 会令 `/connectServer` 得到 400/404；即使 Payload 的直接 travel 已排队，也不代表原生 Meta 会话成立。
- 隔离 LAN 实验现在同时运行 `control-plane` 与 `meta-server`，两者共享临时 access/admin 验签材料。MetaTunnel 仍只监听 loopback；它把游戏 HTTP 转发到私网 Meta HTTP，并在显式 `--allow-private-http-lab --allow-private-plain-logic-lab` 双门禁下把本地 Logic TCP 桥接到同一私网 IP。三个 upstream 必须是同一个私网/回环 IPv4。生产路径仍要求 HTTPS、TLS Logic server name 与 TLS 1.2+。
- 严格 P2P 房间在名单冻结后已经处于 `CONNECTING`，因此成员首次连接也表现为承载层重挂载。旧 `p2proom.Join` 的 `LOBBY-only` 前置检查会在权威名单校验前错误返回 `ROOM_NOT_JOINABLE`。新规则只为具有 `managed_lobby_id` 的房间开放 `CONNECTING/RUNNING` attach，并继续通过冻结名单投影拒绝未知账号；独立旧房间仍只能在 `LOBBY` 加入。
- Toolbox 对控制面短暂传输错误保留约 28 秒重试窗口，错误只记录脱敏后的 HTTP method/endpoint；持续失败才终止实验。MetaTunnel 子进程退出诊断现在保留安全 stderr 摘要，避免只剩无上下文的 `exit code: 1`。
- 自动验证包括 Backend 全套 Go tests、真实 PostgreSQL 的 P2P 生命周期/名单外拒绝、Toolbox `189/189` 核心测试和 Payload `17/17` 策略测试。最终双机出生与 generation 递增的重连仍以两台真实 Steam 账号的人工运行证据为准。
- 最终房主实机探针使用真实 Steam 会话和服务端合成的另一阵营成员触发启动：游戏实际连接受管 MetaTunnel，Meta HTTP Profile 与 `POST /connectServer` 均返回 200，Tunnel 同时建立到实验 Meta HTTP `18081` 和 Logic `16968` 的连接，随后 Payload 回报 `Strict P2P authority ready`，Toolbox 进入 `CONNECTING / Connected`。本轮没有启动前的 Meta 400、`send request` 致命退出或意外 MetaTunnel 终止；测试结束输入 `quit` 后的受管进程强制退出不属于运行时故障。
- 最终双机包为 `.tmp/strict-roster-lab-20260827-meta-r6.zip`，SHA-256 `560BEE53585AB572983C3DD8D88244E1B3ADD19B65C49D55A756FC3235F65E6D`；解压后的 15 项 manifest 回读通过。API 冒烟额外验证三服务健康和 `CONNECTED(gen=1) -> DISCONNECTED -> grant(gen=2) -> 旧授权拒绝 -> CONNECTED(gen=2) -> COMPLETED`。

## 24. Toolbox lab/production 认证缓存隔离（2026-08-27）

- 已复现 Toolbox 启动在 `obtain MetaTunnel access token` 前失败：持久日志只保留了顶层 context，但内置调试日志的先行认证请求明确返回 `refresh Project Rebound session: API 401 UNAUTHORIZED: Invalid refresh token.`。生产 Control Plane 的 live/ready 检查同时为 200，因此不是 MetaTunnel 提取、子进程 readiness 或普通断网故障。
- 只读检查确认缓存 access JWT 的非秘密 header `kid=access-dev-ephemeral`，而当前 Toolbox API origin 为生产 `https://api.project-rebound.space`。根因是 `strict_roster_lab` 在显式 lab origin 下仍复用生产 `%LOCALAPPDATA%\com.projectrebound.toolbox\app_config.json`：lab bind 把临时 Control Plane 会话写回共享配置，后续生产刷新必然查不到对应 refresh session。
- `lab-testing` 现在只要存在 `PROJECT_REBOUND_LAB_API_ORIGIN`，就把配置与 runtime 根定位到 `com.projectrebound.toolbox\lab\<normalized-origin-sha256>`，并禁止把 executable-adjacent legacy config 迁入该 scope。不同 lab origin、lab 与 production 均不再共享 access/refresh token、身份或 runtime 资产目录；路径只含 origin 哈希，不记录私网地址。
- 终止性 refresh 401 现在带有明确的“sign out and sign in again”诊断；`complete_launch` 使用完整 anyhow chain，不再把 `obtain MetaTunnel access token` 之后的真实原因截断。既有已污染生产缓存需要一次新的 Steam 登录，不能把 lab refresh token 搬回生产。
- Toolbox `cargo test --features lab-testing --lib` 为 `197/197`，Tauri tests 为 `7/7`，`cargo check --features lab-testing --bin strict_roster_lab` 通过；仅保留既有未使用函数 warning。

## 25. P2P 房间路由目录与 10 人上限（2026-08-27）

- 生产 `GET /v1/client/config` 当前明确返回 `relay=true`、`vnt_rooms=false`，可用 Relay region 为 `lax`；生产 VNT 在线节点目录为空。控制面 `https://api.project-rebound.space/v1/meta/regions` 返回 404，而 Meta 服务 `https://meta.project-rebound.space/v1/meta/regions` 正常返回 `lax` 及 `UDP 8443` QoS 端点。Toolbox 原先把 Meta region 路径交给通用 Control Plane HTTP origin，因此 Relay discovery 被稳定转成空候选，最终只留下 `No usable room route`。
- Meta region discovery 现在固定使用生产 Meta HTTPS origin；显式 LAN lab scope 则继续要求 `PROJECT_REBOUND_LAB_META_HTTP_ORIGIN`，且只允许与 lab Control Plane 相同私网/回环 IP 上的显式 HTTP 端口。固定 origin 的 path 组装拒绝 authority 替换、非预期 scheme 和带 path/凭据的 origin。
- 路由规划器不再把 VNT/Relay discovery 错误静默压成空列表。没有目录项、目录超时、目录请求失败及已发现 Relay 但 UDP QoS 全部无响应会返回不同的可操作诊断；100% loss 路由仍不被伪装成 ready。
- 房间创建现在把实际选中的 route region 写入 VNT/Legacy room request，而不是把 UI 偏好值（例如 `us-west`）保存到实际 `lax` Relay 房间。否则后端按错误 region 调度 allocation 时无法命中已注册 Relay。
- Boundary 房间容量在 Toolbox 核心固定为 2–10：React 下拉列表新增 10，旧 egui 输入同步为 2–10，native create path 和前端 mock bridge 均拒绝小于 2 或大于 10 的值。后端通用 P2P 配置仍可支持更高协议上限，本次没有把 Boundary 的游戏容量假设扩散为全局控制面常量。
- 自动验证：Toolbox `cargo test --features lab-testing --lib` 为 `202/202`，Tauri tests 为 `7/7`；前端 5 组测试合计 `42/42`，前端生产构建、Tauri release `cargo check` 和 `cargo fmt --check` 通过。当前执行环境对公布的 Relay UDP 8443 探测未收到响应，因此本节不把真实生产房间创建记为动态通过；需要在签名候选构建及真实玩家网络上完成一次 route-ready/create 验收，若仍返回新的 UDP 无响应诊断，则继续检查 Relay 监听、防火墙/NAT 与 advertised port。

## 26. Legacy LAN 加入端候选重放竞态（2026-08-28）

- 已定位从机错误 `selected LAN path omitted the remote candidate`：房主的 `connection.candidate` 会先持久化到 Control Plane，再通过不重放历史的 Realtime 广播。房主可在从机完成认证 WebSocket 订阅前发布 LAN 候选；后端随后仍可根据房主探测选择 `LAN`，但从机本地 `PeerPath` 从未收到该远端候选，旧实现因此把正常的订阅竞态误判为终止性协商失败。
- 加入端现在在认证 WebSocket ready 后读取一次权威 connection REST 快照，过滤自身、非 UDP 与未知类型，只把远端 `LAN/IPV6/SRFLX` 候选与握手后已排队的实时事件合并。该顺序覆盖“候选先于订阅提交”和“候选在快照后提交”两侧，不需要后端暴露秘密或新增重放接口。
- `connection.path_selected` 与 `connection.candidate` 现在允许乱序：直接路径先到且候选暂缺时保留所选路径并等待，不再立即写入 `connection_errors`；匹配候选到达后安装 direct endpoint 并标记 route ready。房主已完成探测的 endpoint 继续优先保留，未补齐的路径最终仍由既有有界 readiness timeout 失败关闭。
- Realtime 候选地址不再用 `address:port` 字符串拼接解析，而是先验证 `IpAddr` 和 `u16` 端口，再用 `SocketAddr::new` 构造，因此未加方括号的 IPv6 API 地址不会再在 Windows 路径上产生错误地址族文本。
- 自动验证：普通核心测试 `205/205`、`lab-testing` 核心测试 `209/209`、Tauri `7/7`、前端 `43/43`，均为 0 失败；新增确定性用例覆盖 REST 快照过滤、IPv6 构造和 `path_selected -> candidate` 乱序恢复。签名 0.9.6 EXE 大小 `48,725,904` 字节，SHA-256 `0B63A1C8C88B9CD2319ED1943DE3E6EC51CC78FCBF28457B9FA04C8C921804C0`，文件/产品版本均为 0.9.6，签名者指纹 `0A95D2BF69633F170BC383A78B71A52667A45780`，含 Sectigo RFC3161 时间戳。独立冷启动加载 `tauri.localhost` 内嵌前端并显示 BUILD 0.9.6；真实双 Steam 身份的加入/出生仍需双机烟测，当前不把该项记为动态通过。

## 27. Listen 主机在远端 NMT_Login 时的 PBGameViewportClient 空指针（2026-08-28）

### 复现与静态根因

- `-RoomAuthority` 主机在 Warehouse 等待阶段接收第二个客户端后稳定产生 `EXCEPTION_ACCESS_VIOLATION reading address 0x70`。失败样本 PID 25904、运行 351 秒，异常 RVA 为 `0x0156193B`；栈经 `0x01561BB8 -> 0x01584801 -> 0x015A31E3` 回到远端控制器创建链，再经 `UWorld::NotifyControlMessage` RVA `0x036CDCE0`。Payload 栈帧只是原函数外层 hook/trampoline，不能据此吞掉 NMT_Login。
- RVA `0x01584730` 是 APBPlayerController 的客户端 HUD/viewport layer helper。它调用 RVA `0x034FB080` 读取 `PlayerController +0x298`，且只有对象属于 `ULocalPlayer` 时返回该指针；远端控制器必然返回 null。原函数随后仍把该 null 作为 key 传给 PBGameViewportClient 的 player-layer map RVA `0x01561A60`。map 插入默认 value 后，RVA `0x0156193B` 无检查地执行 `mov rdx, [rax+70h]`，其中 `rax=0`。
- 这是客户端构建被用作原生 Listen authority 时暴露的客户端 HUD 假设：普通本地 PlayerController 有 ULocalPlayer，远端 PlayerController 没有；Dedicated 没有 GameViewport，因此不会进入该分支。仅把 `ObjectNeedsLoad/ActorNeedsLoad` 与强制 NetMode 限定回 Dedicated 是正确的模式隔离，但动态 A/B 证明它单独不能消除本次崩溃：SHA-256 `D4E88545...` 候选仍在同一 RVA 崩溃。

### 最小兼容实现

- `InitServerHooks(false)` 只为 Listen 安装 RVA `0x01584730` 的入口 guard。若 viewport owner 非空但 RVA `0x034FB080` 返回 null，则只跳过该远端控制器的客户端 viewport-layer 请求；本地主机控制器、原生 null-controller no-op、普通客户端和 Dedicated 路径全部转发原函数。
- guard 不跳过 APBPlayerController 初始化、PostLogin、LateJoin、角色选择或 possession，也不修改 PBGameViewportClient map 内存；第一次命中记录 `[LISTEN] Suppressed a remote PlayerController client viewport-layer request.`。Listen 同时记录 `server-only-load-overrides=native` 和 `remote-player-viewport-guard=enabled`，Dedicated 则保留前者 `enabled`、后者 `disabled`。
- `ServerHookPolicyTests` 固定 Dedicated/Listen 安装计划以及本地/远端/null-controller 转发矩阵。Payload Release 策略测试为 `18/18` passed。

### 动态验收与交付

- 同机双实例使用真实 UE 网络栈完成回归：PID 2480 以 `-RoomAuthority` 监听 `0.0.0.0:7777/UDP`，命名管道先报告 `authority_ready=true, player_count=1`；PID 13184 从 Unreal 控制台执行 `open 127.0.0.1:7777` 后进入 Warehouse，主机 guard 精确命中、记录第二次 `Player Connected!`，管道持续报告 `authority_ready=true, player_count=2`。
- 稳定检查时主机/从机运行时长分别为 256/204 秒，双方仍在 Warehouse `WAITING FOR THE GAME TO START`，最新 crash 目录时间仍为修复前的 12:17:50，没有新 dump。该证据覆盖原生远端 NMT_Login/PlayerController/viewport 崩溃链；它不替代生产 MetaTunnel、Relay/LAN carrier 与两台真实机器的端到端路由验收。
- 最终签名 `Payload.dll` 大小 `1,722,256` 字节，SHA-256 `69EA6630B6B0D2A8046D0737929C5EAE76D406D95ABD9A0DAFD03179A146D4F5`，签名者指纹 `0A95D2BF69633F170BC383A78B71A52667A45780`，含 RFC3161 时间戳。部署前备份位于 `%LOCALAPPDATA%\ProjectRebound\payload-backups\20260828-1228-listen-viewport-guard`。
- 0.9.7 运行时 ZIP 大小 `881,426` 字节，SHA-256 `023085B90601BD0B241C94BB8EB0E2259CCC5B074164574836A02FED63206945`；解压回读为 5 项，内含同一签名 Payload 哈希和版本 `0.9.7`。

## 28. Toolbox 受管运行时更新与旧 `ServerLauncher` 包隔离（2026-08-28）

### 根因与发布边界

- 从机启动前更新报 `Project Rebound release ZIP contains an unmanaged path: ServerLauncher`。安装器的拒绝是正确行为：玩家运行时发布包只允许顶层 `Payload.dll`、`dxgi.dll`、`DT_ItemType.json`、`steam_appid.txt`、`project_rebound_version.txt`；`ServerLauncher`、旧 JavaScript MetaServer 和开发仓库内容不能重新进入客户端安装边界。
- 生产 `/v1/downloads` 的受管 `rebound-release` 最新项当时为 `0.9.1`，文件大小 `3,063,236`、SHA-256 `D2ED49593FE2E6036E23B6582FEF7E10E86F423B87DDE6C1ACB19F7326E706F2`。该归档本身没有 `ServerLauncher`，且现有安装器可在跳过旧 `BoundaryMetaServer-main` 后提取受管根文件。
- 真正错误发生在 Toolbox 的 `latest` 解析：它先查询 GitHub latest，得到历史标签 `V0.8.4`，再以该标签匹配管理目录中的 `0.9.1`。版本标签不一致使目录查询落空，随后退回 GitHub 的旧完整 `Release.zip`，最终由受管路径门正确拒绝 `ServerLauncher`。

### 当前修复与验证

- `ReboundRelease` 的 `latest` 和显式版本现在只从生产受管下载目录解析；`v/V` 只在后接数字时规范化。目录必须恰好包含一个 `rebound-release` 条目和唯一的规范化版本标签。Project Rebound 运行时不再回退 GitHub 或未校验镜像。
- 下载完成后必须同时匹配目录声明的 `size_bytes` 和 SHA-256，之后才进入全归档预检与提取。未知路径仍在写入任何文件前原子拒绝；专门回归固定了 `ServerLauncher/` 目录项的精确错误和零落盘结果。
- 真实线上探针经新代码选择并下载上述 `0.9.1` 归档，回读大小和 SHA-256 完全一致，随后在临时目录成功提取 `Payload.dll`；未再走 GitHub 或触发 unmanaged path。
- Toolbox 核心全目标测试为 `221 + 221`、Tauri 为 `7/7`、前端为 `43/43`，均 0 失败。创建房间的 React 与旧 egui 默认容量同时改为 10，协议校验仍固定 2–10。
- 最终签名 EGUI 0.9.8 大小 `42,916,240` 字节、SHA-256 `9F5516B1991F18883A320FA4957C99D5F16145B5FDCCF7BD91246FACBFDBF2F0`；签名 Tauri 候选大小 `48,761,744` 字节、SHA-256 `D7C158E1CEE410B6C27958F1EC5880FC5F15D28BCDE312E34D3BEF413E9A2EA7`。二者签名者指纹均为 `0A95D2BF69633F170BC383A78B71A52667A45780`，包含 Sectigo RFC3161 时间戳。EGUI 候选独立冷启动通过完整性门并显示 `v0.9.8`。
- 最终 0.9.8 玩家运行时 ZIP 大小 `885,938` 字节、SHA-256 `6D30BB8C06731B0AC6ACD4A04BFD3C66071107180F8397AD76F28A47A06441E2`。解压回读严格为上述 5 项；其中签名 `Payload.dll` 大小 `1,731,984` 字节、SHA-256 `6C7B5E05540AC72A6D7A9FA78F867917285FCE00081156EE13D237AA4D6C24A3`，版本标记为 `0.9.8`。

## 29. Legacy carrier `CHECKING_DIRECT` 非重放反向竞态与 Toolbox 0.9.9（2026-08-28）

### 失败事实与根因

- 从机错误为 `Legacy carrier was not ready before timeout (state=CHECKING_DIRECT, candidates=3, selected_path=none, realtime=none, control_plane=none)`。`candidates=3` 证明候选已持久化，不能按“没有候选”或普通地址解析失败处理；真正缺失的是房主侧的探测与路径选择结果。
- Legacy coordination WebSocket 不重放历史事件。旧修复只让从机从 REST 快照恢复房主候选；相反方向仍有竞态：从机可在房主认证 WebSocket 握手或重连窗口内提交候选，Control Plane 保存成功但房主漏收实时事件。房主因而没有启动 direct probe/check-result，连接长期停在 `CHECKING_DIRECT`，也不会产生 `path_selected`。

### 有界恢复实现

- 从机仅在 Control Plane 为 `CREATED/GATHERING_CANDIDATES/CHECKING_DIRECT` 时重报自己的稳定候选，退避为 500 ms、1 s、2 s，之后 4 s；一旦选择 direct/relay 或状态推进就停止。房主收到重报时回显自己的稳定候选，因此任一侧短暂错过非重放事件都可重新汇合，且不会形成无界广播。
- 房主遇到未知 connection 的候选事件时，不再直接信任事件内容；它先从 REST 读取 connection，并精确验证 room、host player 和 connection ID 后才纳入本次协商。验证失败的跨房间、错误主机或未知连接继续 fail closed。
- `wait_for_connection` 的权威 REST 快照现在持续送入路由线程。从机重复合并合法远端候选，并可从快照恢复错过的 direct `path_selected`。由 REST 新恢复或发生变化的 SRFLX 候选只发送一次 NAT priming probe，不对未变化地址重复打洞。
- 新增确定性回归覆盖：房主在实时丢失后响应从机重报、快照恢复错过的 direct path、候选重报有界且在状态推进后停止，以及真实本地 UDP request/response 驱动房主成功上报 check-result 的闭环。

### 构建、签名与剩余验收

- Toolbox 控制器定向测试 `13/13`；`lab-testing` 核心与全目标、普通全目标合计执行 892 个 Rust 测试，0 失败；Tauri `7/7`，前端 5 组合计 `44/44`，生产构建与 `cargo fmt --check` 通过。仓库级 `clippy -D warnings` 仍被 78 个既有 lint 阻塞，本节没有把该基线债务记为通过。
- 发布号提升到 0.9.9。前端页眉、设置页和诊断文案不再硬编码版本，统一从 `package.json` 派生；签名 Tauri 冷启动确认加载内嵌生产前端并显示 `BUILD 0.9.9`。
- 签名 EGUI `Rebound_Toolbox.exe` 大小 `42,938,768` 字节，SHA-256 `62C1A3AAD389EA9F89660240044B1E053E2DC11596CD4C06941C06E265BA82B8`。签名 Tauri `Rebound_Toolbox_Tauri.exe` 大小 `48,791,440` 字节，SHA-256 `BD217B21C29F1DCB049690EAA54BA123BD6818E36A988256B2169D2E174C47F8`，文件/产品版本均为 0.9.9。两者签名者指纹均为 `0A95D2BF69633F170BC383A78B71A52667A45780`，包含 Sectigo RFC3161 时间戳。
- 自动测试已覆盖丢事件恢复与真实本地 UDP 探测，但仍需两台真实 Steam 身份都升级 0.9.9、创建全新房间/connection 后完成端到端验收。在取得双方 carrier ready、Boundary 启动并实际进入同一战局的证据前，不把生产双机动态验收标记为通过。

## 30. 选路后稳定候选重放竞态、Relay 探测预算与 Toolbox 0.9.10（2026-08-29）

### 生产证据与根因

- 房主创建 `room_5bfd44c30cf74b67927d0e09b723b54a` 时先显示 `Route ready: lax 175ms`，从机第一次加入却返回 `no online room route is currently advertised`；第二次加入返回 `INVALID_CONNECTION_STATE: Connection is not gathering direct candidates.`。生产数据库只读核对显示房间为 `RUNNING / lax / LEGACY_RELAY`，本次 connection 已保存 2 个 LAN 和 1 个 SRFLX 候选，且 LAN check 明确 `success=true, latency=10ms`。客户端在该成功 check 后约 1.19 秒主动关闭 connection，因此不能把第二次失败归因于 LAN 不通或没有候选。
- 路由规划器原先让 Meta region discovery 与 UDP QoS probes 共用 3 秒总 deadline。目录请求接近耗尽该窗口时，Relay endpoint 虽已发现却没有获得第一次 UDP 探测机会，空结果随后被错误描述为“没有在线路由”。现在 discovery 与 probe 各有独立的 3 秒有界窗口；已发现但 UDP 全丢包继续返回专门的 route-check 诊断。
- 0.9.9 从机会有界重报稳定候选以修复非重放 WebSocket 竞态。房主可先用第一次候选完成 LAN 选路，而已经进入队列的重复 candidate 随后才到达 Control Plane；旧后端在 `CONNECTED` 等推进状态返回 `INVALID_CONNECTION_STATE`，旧客户端又把任何 realtime `error` 都写成全局 `backend_error`，从而让过期错误抢先终止已成功的 carrier。

### 双侧兼容修复

- Toolbox 只把精确的 `INVALID_CONNECTION_STATE / Connection is not gathering direct candidates.` 识别为选路后稳定候选重放诊断，不再覆盖 connection 的 path-ready 结果。其他 realtime 错误仍保持 fail closed，匹配条件没有使用模糊子串。
- Control Plane 在 `ALLOCATING_RELAY/RELAY_BINDING/MIGRATING_RELAY/CONNECTED` 中只允许同一参与者、同一 foundation 且类型、协议、地址、端口、优先级全部相同的候选幂等重放，返回原 candidate ID，不修改数据库也不重新广播。缺失候选或任何字段变化仍返回 `INVALID_CONNECTION_STATE`。
- 后端热修复 commit `6c157daadbcd004ff77a56626d53224e6dcdb144` 已通过 GitHub CI 的真实 PostgreSQL race 测试、真实认证/房间/WebSocket/双 Relay UDP 集成、镜像 SBOM 与高危漏洞扫描，并部署到生产。外部 `/health/live` 与 `/health/ready` 均返回 200；生产 control-plane 容器使用 `sha-6c157d...` 且为 healthy。MetaServer 与 Edge Relay 保持原部署版本；同一 Control Plane Compose 栈中的无源码变化 AdminWeb 镜像随发布刷新。

### 构建、签名与剩余验收

- 普通 Toolbox 全目标两套各 `223/223`、`lab-testing` 两套各 `227/227`、Tauri `7/7`、前端 `44/44`，全部 0 失败；前端生产构建、Rust/Tauri release check、`cargo fmt --check` 和 Backend `go test ./...` 均通过。CI 的真实 PostgreSQL 用例额外固定“CONNECTED 后完全相同的候选重放成功，改变端口仍失败”。
- 版本提升到 0.9.10。签名 Tauri EXE 大小 `48,798,096` 字节、SHA-256 `50E26BE171AA2F51570309AB58E68B08971DA8999E30285729669778B809D083`，文件版本 0.9.10；签名 EGUI EXE 大小 `42,940,816` 字节、SHA-256 `5A3A19641CCF3BBEFE97BC037F364A89AF31424F45295C3EF4E9434D5A960E22`。两者签名者指纹均为 `0A95D2BF69633F170BC383A78B71A52667A45780`，包含 RFC3161 时间戳。发布暂存目录为 `.tmp/release-0.9.10-20260829`。
- 后端热修复可让既有 0.9.9 不再因完全相同的候选重放失败，但第一次 Relay discovery/probe 预算误报只能由 0.9.10 客户端修复。最终仍需主从机均升级 0.9.10、退出旧进程并创建全新房间，确认同一 connection carrier ready、Boundary 进入同一战局且能看到彼此、移动和射击；在取得该实机证据前不标记生产双机动态通过。

## 31. 权威清单生产接线、活动大厅恢复与 Toolbox 0.9.12（2026-08-29）

### 半成品绕过点与失败边界

- Control Plane、Payload 和 Toolbox Rust 核心已经具备冻结清单、签名准入、承载 barrier 与自动启动能力，但生产 React 页面仍只调用旧 `list_rooms/create_room/join_room/launch_room`。因此此前的修复只存在于未被生产 UI 使用的核心层；实际玩家仍会经历 Legacy 房间的候选竞态、房主手工启动与非权威成员状态。
- 生产 Compose 同时没有透传 `STRICT_ROSTER_V1_ENABLED`、锁定游戏哈希和 match-admission Ed25519 私钥。即使部署了代码，`/v1/client/config` 也不会安全公布 `strict_roster_v1=true`。这两个缺口共同构成此次“实现到一半”的根因。
- 新 UI 首先读取服务端签名配置投影。查询失败时房间协议 fail closed；为 `false` 时保持既有 Legacy 行为；精确为 `true` 时只使用 match-lobby 命令，任何创建、加入、冻结或承载错误都不得回退到 Legacy 房间。

### 完整权威链路

- React 创建 P2P 权威大厅时按总容量拆成两队（10 人为 5+5），房主席位固定为 team 1，并立即提交 ready。加入端先读取最新 revision，选择仍有容量的队伍，最多重试一次并发 revision 冲突；席位创建后立即 ready。只有 owner 且全部成员 ready 时才显示“冻结清单并启动”，成员没有手工启动入口。
- `start_match_lobby` 只负责冻结权威清单。后台随后签发 attempt/grant，Toolbox 的 match worker 建立 P2P carrier；只有 `match_carrier_ready` 后才触发 `match_auto_launch`。冻结后禁止普通离开；从机连接失败只使用 `retry_match_connection` 回收原席位，v1 不做主机迁移。
- 增加 `GET /v1/match-lobbies/active` 解决 Toolbox/页面重启后的内存断层。普通成员只恢复公开 snapshot；P2P owner 的 host credential 由 Control Plane 从既有密文中解密并经受认证的 owner-only 响应交给 Rust controller，Tauri 命令和 React DTO 均不返回该 secret。不存在活动大厅时返回精确的 `MATCH_LOBBY_NOT_ACTIVE`，本地状态随之清除。
- 前端监听 `match_lobby_updated`、`match_lobby_frozen`、`match_carrier_ready`、`match_auto_launch`、`match_connection_changed`、`match_returned_to_lobby` 和 `match_terminal`；严格模式下忽略旧 `rooms_updated`、`room_route_ready` 与 `active_room_changed`，避免两套状态机互相覆盖。

### 配置、构建和验证

- 生产与开发 Compose 已显式透传严格清单开关、锁定游戏 SHA-256、admission key ID 和 Ed25519 private seed；环境生成器为 admission 单独生成密钥。生产默认仍为 `false`，必须在 Control Plane、已签名玩家运行时和强制更新 Toolbox 同步可用后才切换。
- Backend `go test ./...` 通过；match-lobby、P2P room、OpenAPI 与 Compose 定向测试通过。活动大厅 owner/member/无大厅断言已加入 PostgreSQL 集成测试，但本机没有 `TEST_DATABASE_URL`，该 DB-backed 用例按既有门禁跳过，不能冒充真实数据库动态通过。
- Toolbox 普通 Rust 全目标两套各 `223/223`、`lab-testing` 两套各 `227/227`、Tauri `7/7`、前端 `48/48`，全部 0 失败；前端生产构建、Tauri release 构建、`cargo fmt --check` 与两仓库 `git diff --check` 通过。
- 最终 Tauri 0.9.12 签名候选为 `.tmp/release-0.9.12-20260829/rebound_toolbox.exe`，大小 `48,850,320` 字节，SHA-256 `D7CAEEBC8E5E1BB7B82F2DB146F78A8582D5D5ABD1424864869A9047483EDBC0`，文件/产品版本均为 0.9.12。签名者指纹 `0A95D2BF69633F170BC383A78B71A52667A45780`，包含 Sectigo RFC3161 时间戳；本机链状态仅因项目私有根未受系统信任而为 `UntrustedRoot`。
- 发布源需要同版本 `vnt-runtime-manifest.json` sidecar 供服务器提取运行时证明，但公开签名更新 Manifest 必须严格只含一个 `path=rebound_toolbox.exe, compression=none` 的文件。候选文件名已直接规范为小写，避免后台按对象原文件名自动填入 `Rebound_Toolbox.exe` 再触发客户端拒绝。
- 尚未执行生产切门或两台真实 Steam 机器的最终验收。最低动态通过标准为：双方强制升级 0.9.12，创建全新 P2P 权威大厅，日志不出现旧 `create_room/join_room/launch_room`，owner 冻结同一 revision 后双方各自收到 carrier-ready 与 auto-launch，并进入同一战局看到彼此、移动和射击。取得这组证据前不把生产闭环标记为动态通过。
