# 03 — Game Fixes Catalog

> 来源合并：Launcher Fixing Docs.md、Session_Compact（PvE Camera / Steam Names / UIShake 部分）
> 最后更新：2026-05-18
> 命名规范："LauncherFix" → "SideMountFix"（2026-05-19 重命名）

## 核心模式：IsLocallyControlled

所有 DS Bug 的根源是同一个模式：

- **ListenServer**: `IsLocallyControlled()` = true → 所有 BP 逻辑运行
- **Dedicated Server**: `IsLocallyControlled()` = false → 每个有此守卫的 BP 函数都被静默跳过

影响范围：状态转换、flag 清理、视觉效果、HUD 更新、音效。

**修复模式**：1) 找到触发但不执行的 ProcessEvent 2) 添加 Hook 3) 手动触发被跳过的逻辑。

---

## Fix 1: SideMount（副武器挂载，原 LauncherFix）

### 涉及文件

| 文件 | 角色 |
|------|------|
| `Utility/LauncherFix.cpp/.h` | 全部副武器 + 弹丸修复逻辑（→ 重构后拆为 `Server/SideMountFixServer.cpp` + `Client/SideMountFixClient.cpp`） |
| `Hooks/Hooks.cpp` | ProcessEvent dispatch |

### 已验证功能

| 功能 | 状态 |
|------|:--:|
| 所有 Delay 型发射器（smoke, impulse, HE/CQB, EMP）完整射击循环 | ✅ |
| Deploy/fire/undeploy 动画 | ✅ |
| Reload 进度条 | ✅ |
| 弹丸生成 + 飞行 + 撞击 | ✅ |
| 爆炸视觉效果（HE, EMP, Impulse） | ✅ |
| 烟雾效果（Squid 变体） | ✅ |
| 无哑弹（阻塞 BP 的 async timer 重复调用） | ✅ |
| 空弹夹射击不崩溃 | ✅ |
| Snapshot 发射器 — 假弹丸模型 | ✅ |
| Deploy 型瞄准线清理 | ✅ |

### 修复 1a：状态机修复（客户端 OnRep_PendingState）

当 `OnRep_PendingState` 触发时：

1. 强制 `CurrentState = PendingState`（修复卡死的状态机）
2. 对每种状态转换调用对应的 `K2_` BP 函数
3. 在 Standby(0) 和 Ready(3) 清除 fire flags

```
State 0 (Standby): bIsFiring=false, bPendingFiring=false, BurstCounter=0,
                    bIsFireControlEnabled=false, K2_Standby(), OnHidden_Event()
State 1: K2_Deploying()
State 2: K2_Undeploying(), OnHidden_Event()
State 3 (Ready): bIsFiring=false, bPendingFiring=false, K2_Ready()
State 4 (Reloading): K2_Reloading(), K2_ASingleAmmoReloaded()
State 5: K2_Handup()
```

**为什么 flags 会被卡住**：`bIsFiring`、`bPendingFiring`、`BurstCounter` 由客户端 BP 在射击期间设置。由 `K2_Standby` 清理。但 `K2_Standby` 有 `IsLocallyControlled()` 守卫 → 在 DS 客户端上被跳过 → flags 永远不清理。

### 修复 1b：哑弹阻塞

BP async timer `FireConfig.TimeCanRetriggerFire=0.25s` 在第一次 ServerFiring 后触发。Timer 再次调用 ServerFiring 但弹药已消耗 → 哑弹。阻塞 `ServerFiring` 当 `AmmoInClip == 0 && !HasInfiniteAmmo()`（客户端和服务器两侧）。

### 修复 1c：弹丸爆炸视觉效果

`MulticastExplode` multicast RPC 触发所有客户端上的视觉效果。原生代码：`if (IsLocallyControlled()) { MulticastExplode(...); }` → DS 上不调用。`OnRep_Exploded` 触发但 BP handler 有 IsLocallyControlled 守卫。修复：当 `OnRep_Exploded` + `bExploded==1` 时强制调用 `MulticastExplode(DummyHit)`。

### 已知问题

| 问题 | 状态 |
|------|------|
| 运动传感器模型遮挡视野 | 延后 |
| 运动传感器弹丸方向损坏 | 延后 |
| Snapshot/运动传感器第二发射击枪口火焰缺失 | 延后 |
| 烟雾弹（非 Squid）对自己引爆 | 未调查 |
| 钩爪 | 相同模式，尚未修复 |

---

## Fix 2: PvE 摄像机

### 问题

PvE 进场动画将摄像机与玩家分离。许多地图的序列比 CountdownToStart 更长。

### 解决方案

- `PVECamFix.cpp`：通过内存偏移 **0x02B0** 轮询 `ALevelSequenceActor::SequencePlayer` Status（不是 ProcessEvent）
- 触发器：Status 从 Playing(1) 变为 non-Playing
- `LateJoinManager::ForceFirstLifeSpawn(PC)`：将玩家以 RoleConfirmed 状态入队 → Tick() 驱动重生
- 序列结束后 10 秒内每 tick 尝试 Possess 窗口

### LevelSequence 检测

```cpp
// ALevelSequenceActor::SequencePlayer at offset 0x0250
// UMovieSceneSequencePlayer::Status at offset 0x02B0 (EMovieScenePlayerStatus: uint8)
// 0=Stopped, 1=Playing, 2=Scrubbing, 3=Jumping, 4=Stepping, 5=Paused
static bool IsSeqPlaying(ALevelSequenceActor *Actor) {
    auto *Player = *reinterpret_cast<ULevelSequencePlayer **>(
        reinterpret_cast<uintptr_t>(Actor) + 0x0250);
    if (!Player) return false;
    uint8 status = *reinterpret_cast<uint8 *>(
        reinterpret_cast<uintptr_t>(Player) + 0x02B0);
    return status == 1;
}
```

### 已知问题

- 首次生命发射器（钩爪、机动装备）不工作 — 需要重生流程调查
- 根因：首次生命重生流程不同于自然重生 — 发射器装备未正确初始化

---

## Fix 3: Steam 名称解析

### 问题

记分板显示 SteamID64 而非显示名称。

### 解决方案

- `UserNameFix.cpp`：WinHTTP GET `steamcommunity.com/profiles/{steamid}/?xml=1` — 不需要 API key
- 原地 FString 写入偏移 **0x0300**（PlayerNamePrivate）以避免 CRT/引擎分配器冲突
- FString Count 包含 null 终止符：`count = needed + 1`
- 异步 PostLogin hook → pending 队列 → TickFlush 在主线程排空
- 线程安全：在 pending 队列中存储 `std::string`，在主线程构造 `FString`

### 原地 FString 写入

```cpp
static void InPlaceFStringWrite(APlayerState *PS, uintptr_t offset, const wchar_t *text) {
    uintptr_t base = reinterpret_cast<uintptr_t>(PS);
    TCHAR*&  data  = *reinterpret_cast<TCHAR**>(base + offset + 0);   // pointer
    int32&   count = *reinterpret_cast<int32*> (base + offset + 8);   // ArrayNum
    int32&   max   = *reinterpret_cast<int32*> (base + offset + 12);  // ArrayMax
    int32 needed = static_cast<int32>(wcslen(text));
    if (max > needed) {
        wcscpy_s(data, max, text);
        count = needed + 1;  // FString Count INCLUDES null terminator!
    }
}
```

为什么不能用 `FString::operator=`：SDK CRT `delete[]` 会尝试释放引擎分配的 Data 指针 → 堆损坏。

### Steam API 细节

```
GET https://steamcommunity.com/profiles/{steamid64}/?xml=1
Response: <?xml><profile><steamID><![CDATA[PlayerName]]></steamID></profile>
```
- 不需要 API key。3s WinHTTP 超时。缓存：`std::unordered_map<std::string, std::string>`

### 已知问题

- 记分板 ID 行仍显示 `/765611`（SteamID 前缀）
- `GetDefaultIDStr()` 是 Native Final — 不经过 ProcessEvent，无法拦截
- 需要二进制函数 hook（SafetyHook on vtable entry）。延后。

---

## Fix 4: UI 冲刺抖动

### 问题

DS 上不在冲刺时冲刺抖动仍然存在。

### 解决方案

- `UIFix.cpp`：不再冲刺时清零 CamCache（bIsRunning==0 且 CharStatus 为 Idle/SlowlyMoving）
- 冲刺时合成正弦波（频率 8.7-12.3Hz，振幅 Loc~0.015-0.02, Rot~0.04-0.06）
- 调试日志：CamD（帧间增量）和 WpD（武器 delta）每 60 帧输出一次

### 关键偏移

| 偏移 | 成员 |
|------|------|
| 0x2810 | CameraModifiers_CacheRelativeLocation（FVector, 12 字节） |
| 0x281C | CameraModifiers_CacheRelativateRotation（FRotator, 12 字节） |

---

## Fix 5: LoadoutFix（军械库）

### 状态：延后（重构中将被删除）

- `LoadoutFix.cpp`：装备错误吞掉（ErrorCode=4 → 0）
- HTTP GET `127.0.0.1:8000/api/loadout/{playerId}`
- F8 热键 → `LoadoutFix_FlushRefresh()` → 尝试刷新模型
- **已知阻塞**：`OnEquipCharacterSlotDelegate::Broadcast()` 是 `TMulticastInlineDelegate` — 纯 C++ 模板，不经过 ProcessEvent。无法拦截。

---

## 验证清单

- [x] SideMount：所有发射器类型正常工作
- [x] 无哑弹
- [x] 无空弹夹服务器崩溃
- [x] PvE 摄像机在序列结束后重新连接
- [x] Steam 名称显示（非 SteamID64）
- [x] 冲刺抖动在空闲时清除
- [ ] 钩爪（相同 IsLocallyControlled 模式）
- [ ] 拾取武器残留（ActorChannel 关闭）
- [ ] 首次生命 PvE 发射器（钩爪/机动装备）

## 相关文档

- `02-DLL-Internals.md` — Hook 体系、日志系统
- `04-RE-Data.md` — 完整 SDK 偏移量表
