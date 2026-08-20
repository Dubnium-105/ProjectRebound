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
