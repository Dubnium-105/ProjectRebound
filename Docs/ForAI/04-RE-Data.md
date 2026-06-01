# 04 — Reverse Engineering Data

> 来源合并：Launcher Fixing Docs（附录）、MetaserverLoadoutResponse.md（数据结构）、ServerHangDoc.md（x64dbg）、Session_Compact（Deep Technical Details + SDK Offsets Reference）
> 最后更新：2026-05-18

## SDK 偏移量全集

### APlayerState / APBPlayerState

| 偏移 | 成员 | 类型 | 用途 |
|------|------|------|------|
| 0x0300 | PlayerNamePrivate | FString (Net+RepNotify) | Steam 名称写入（UserNameFix） |
| 0x0250 | UniqueId | FUniqueNetIdRepl (Net+RepNotify) | — |
| 0x03D8 | PlatformUniqueIDJsonString | FString (Net+RepNotify) | APBPlayerState — 尝试用于名称（失败） |
| 0x08D0 | PlayerNameBeforeFilter | FString (Protected) | APBPlayerState |

### APlayerController / APBPlayerController

| 偏移 | 成员 | 类型 | 用途 |
|------|------|------|------|
| 0x02B8 | PlayerCameraManager | APlayerCameraManager* | 摄像机诊断 |
| 0x0588 | PBAimingComponent | — | 后坐力调查 |
| 0x0598 | AllyCameraComponent | UCameraComponent* | PvE 修复方案 |
| 0x05B0 | PBCharacter | APBCharacter* | PvE 修复方案 |

### ALevelSequenceActor / UMovieSceneSequencePlayer

| 偏移 | 成员 | 类型 | 用途 |
|------|------|------|------|
| 0x0250 | SequencePlayer | ULevelSequencePlayer* | PvE 摄像机 |
| 0x02B0 | Status | EMovieScenePlayerStatus (uint8) | PvE 摄像机 |

### APBLauncher（副武器挂载）

| 偏移 | 成员 | 类型 |
|------|------|------|
| 0x0278 | FireComponent | UPBFireComponent* |
| 0x02C1 | CurrentState | EPBLauncherState |
| 0x02D4 | PendingState | EPBLauncherState (Net, RepNotify) |
| 0x0338 | ReloadingDelay | float |
| 0x033C | ReloadingDuration | float |
| 0x0370 | FireConfig | FPBFiringConfig |
| 0x03B8 | BurstCounter | int32 (Net, RepNotify) |
| 0x03BC bit 0 | bIsFiring | bool (Net, RepNotify) |
| 0x03BC bit 1 | bPendingFiring | bool (Net, RepNotify) |
| 0x0410 bit 0 | bIsFireControlEnabled | bool |
| 0x0428 bit 0 | bInProjectileControlMode | bool (Net, RepNotify) |
| 0x0440 bit 0 | bInSpecialMode | bool |
| 0x0454 | MagazineConfig | FPBMagazineConfig |
| 0x0468 | Magazine | FPBMagazine (Net, RepNotify) |
| 0x0498 bit 0 | bInForceState | bool (Net) |

### APBLauncher_Deploy_BP_C

| 偏移 | 成员 | 类型 |
|------|------|------|
| 0x0658 | ProjectilePathTracer | — |
| 0x0660 | FireRocoil | — |

### APBProjectile

| 偏移 | 成员 | 类型 |
|------|------|------|
| 0x0244 bit 0 | bIsDisabled | bool (Net, RepNotify) |
| 0x0250 | MovementComp | UProjectileMovementComponent* |
| 0x0258 | CollisionComp | USphereComponent* |
| 0x0260 | ParticleComp | UParticleSystemComponent* |
| 0x0620 | bExploded | bool (Net, RepNotify) |
| 0x062C | ImpulseScale | float |
| 0x0638 | OwnerPlayerState | APBPlayerState* (Net, RepNotify) |

### APBCharacter

| 偏移 | 成员 |
|------|------|
| 0x1F50 | CurrentLeftLauncher (Net, RepNotify) |
| 0x1F58 | CurrentRightLauncher (Net, RepNotify) |

### UProjectileMovementComponent

| 偏移 | 成员 |
|------|------|
| 0x00C4 | Velocity（继承自 UMovementComponent） |
| 0x00F0 | InitialSpeed |
| 0x00F4 | MaxSpeed |
| 0x00F8 bit 3 | bInitialVelocityInLocalSpace |

### FPBMagazine

| 偏移 | 成员 |
|------|------|
| 0x00 | Config |
| 0x0C | AmmoInClip |
| 0x10 | TotalAmmo |
| 0x14 | AmmoInMagazine |
| 0x18 | MagazineCapacity |

### FPBFiringConfig

| 偏移 | 成员 |
|------|------|
| 0x00 | TimeBetweenFire |
| 0x04 | TimeBetweenBurst |
| 0x08 | TimeCanRetriggerFire |
| 0x0C | PostFireDuration |
| 0x10 | BurstCount |
| 0x14 bit 0 | bEnableBurst |
| 0x14 bit 1 | bEnableAutoFire |

### APBDisplayCharacter（军械库显示角色）

| 偏移 | 成员 | 类型 |
|------|------|------|
| 0x0370 | DisplayLeftPylon | APBDisplayPod* |
| 0x0378 | DisplayRightPylon | APBDisplayPod* |
| 0x0380 | DisplayFirstWeapon | APBDisplayWeapon* |
| 0x0388 | DisplaySecondWeapon | APBDisplayWeapon* |
| 0x0390 | DisplayMeleeWeapon | APBDisplayMeleeWeapon* |
| 0x0398 | DisplayMobilityModule | APBDisplayMobilityModule* |
| 0x03A0 | RoleConfig | FPBRoleNetworkConfig（0xF8 字节） |

### FPBRoleNetworkConfig（0xF8 字节）

| 偏移 | 成员 | 对应存档字段 |
|------|------|-----------|
| 0x00 | CharacterID | RoleID |
| 0x08 | CharacterData | 角色皮肤 (0x28) |
| 0x30 | FirstWeaponPartData | PrimaryWeapon (0x38) |
| 0x68 | SecondWeaponPartData | SecondWeapon (0x38) |
| 0xA0 | MeleeWeaponData | MeleeWeapon (0x10) |
| 0xB0 | LeftLauncherData | LeftPylon (0x10) |
| 0xC0 | RightLauncherData | RightPylon (0x10) |
| 0xD0 | MobilityModuleData | MobilityModule (0x08) |
| 0xD8 | InventoryData | 库存配置 (0x20) |

### FPBWeaponNetworkConfig（0x38 字节）

| 偏移 | 成员 |
|------|------|
| 0x00 | WeaponPartSlotTypeArray |
| 0x10 | WeaponPartConfigs |
| 0x20 | OrnamentID |
| 0x28 | WeaponID |
| 0x30 | WeaponClassID |

### FPBLauncherNetworkConfig（0x10 字节）

| 偏移 | 成员 |
|------|------|
| 0x00 | ID |
| 0x08 | SkinID |

### 军械库 Widget

| 类 | 偏移 | 成员 | 类型 |
|----|------|------|------|
| UPBItemCSTM_Base | 0x260 | ItemId | FName |
| UPBItemCSTM_Base | 0x269 | bIsEquipped | bool |
| UPBPanelCSTM_EditCharacterSlot | 0x358 | EditingCharacterSlot | — |
| UPBPanelCSTM_EditCharacterSlot | 0x35C | EquippedInventoryID | — |
| UPBPanelCSTM_EditCharacterSlot | 0x364 | PreviewInventoryID | — |
| UPBShowRoomManager | 0x38 | ShowRoomStateMachine | USMInstance* |
| UPBShowRoomManager | 0x110 | CacheActorArray | — |
| UPBShowRoomManager | 0x120 | CacheActorMap | TMap<FName, APBDisplayActor*> |
| UPBShowRoomManager | 0x1D0 | ViewTargetID | — |

### APBPlayerCameraManager

| 偏移 | 成员 | 用途 |
|------|------|------|
| 0x2810 | CameraModifiers_CacheRelativeLocation (FVector, 12B) | UIShake |
| 0x281C | CameraModifiers_CacheRelativateRotation (FRotator, 12B) | UIShake |

---

## 枚举

### EPBLauncherState

| 值 | 名称 | 说明 |
|:--:|------|------|
| 0 | Standby | 射击完成，flags 已清除 |
| 1 | Deploying | 正在部署（空闲→就绪） |
| 2 | Undeploying | 正在收起（就绪→空闲） |
| 3 | Ready | 就绪可射击，状态干净 |
| 4 | Reloading | 重新装弹 |
| 5 | Handup | 收起/手持 |

### EMovieScenePlayerStatus

| 值 | 名称 |
|:--:|------|
| 0 | Stopped |
| 1 | Playing |
| 2 | Scrubbing |
| 3 | Jumping |
| 4 | Stepping |
| 5 | Paused |
| 6 | MAX |

### EPBMatchPhase

| 值 | 名称 |
|:--:|------|
| 0 | EnteringMap |
| 1 | WaitingToJoin |
| 2 | MatchIntro（PvE 动画在此阶段播放） |
| 3 | WaitingToStart_Round |
| 4 | RoleSelection_Round |
| 5 | CountdownToStart_Round |
| 6 | InProgress_Round |
| 7 | WaitingPostRound_Round |

### RoundState 字符串值（日志中观察到）

`InvalidState` → `RoleSelection` → `CountdownToStart` → `InProgress` → `ShowingMatchResult` → `MatchEnding` → `WaitingToEndGame`

---

## 陷阱参考

### 陷阱 1：FString 赋值跨越 CRT 边界

**症状**：名称变为乱码如 "STanNam" 或 "UserName"
**根因**：SDK `FString::operator=` 调用 CRT `delete[]` 释放引擎分配的 Data 指针
**修复**：通过内存偏移直接写入 FString 缓冲区（读 TCHAR*/Count/Max，用 `wcscpy_s`，更新 Count）

### 陷阱 2：FString Count 包含 null 终止符

**症状**：名称被截断 1 个字符（如 "STanJ" 而非 "STanJK"）
**根因**：设置 `count = wcslen(text)` (6) 但 UE4 FString Count 包含 null → 7
**修复**：`count = needed + 1`

### 陷阱 3：WinHTTP session 池化导致堆损坏

**症状**：服务器 5h+ 后挂死，x64dbg 中 std::map 死循环
**根因**：单个 WinHttpOpen session 上 3600+ WinHttpConnect → 堆损坏
**修复**：每次请求 WinHttpOpen/WinHttpCloseHandle（不重用 session）

### 陷阱 4：ExitProcess 触发 atexit 死锁

**症状**：进程退出时挂死，x64dbg 中 GObjects 链表循环
**根因**：ExitProcess → DLL_PROCESS_DETACH → CRT atexit → SDK atexit 遍历半销毁的 GObjects
**修复**：使用 `TerminateProcess(GetCurrentProcess(), 0)` — 硬杀，不清理

### 陷阱 5：`std::endl` 在满管道上阻塞

**症状**：Wrapper 冻结，killserver 挂死
**根因**：`std::endl` 刷新 → 管道缓冲区满 → 游戏线程阻塞于写入
**修复**：使用 `\n` 不刷新，LogManager 每 N 行批量刷新

### 陷阱 6：ProcessEvent hook 内递归 ProcessEvent

**症状**：栈溢出或崩溃
**根因**：从 ProcessEvent hook 内调用 SDK 函数（该函数再调用 ProcessEvent）
**修复**：使用直接内存偏移读写替代 SDK wrapper

### 陷阱 7：`replace_all` 子串冲突

**症状**：函数名损坏（ClientDebugServerLog, LoadoutFix_FetchAndServerLog）
**根因**：`Log(` 这样的短 old_string 匹配到较长函数名内部
**修复**：使用带上下文的精确 old_string：`Log("[SERVER] Hooks installed.")` 而非仅 `Log(`

### 陷阱 8：SDK 命名空间遮蔽

**症状**："undefined type APBPlayerController"，即使包含了 SDK.hpp
**根因**：`class APBPlayerController;` 在全局命名空间 ≠ `SDK::APBPlayerController`
**修复**：使用 `namespace SDK { class APBPlayerController; }` 或在头文件中包含 SDK.hpp

---

## x64dbg 诊断速查表

| 现场特征 | 原因 |
|----------|------|
| `cmp [r12], [r15+8]` + `call ntdll.xxxWait` 循环 | 内核锁自旋：线程在中途被杀死 |
| `mov rax, [rax+10]` + `cmp [rax+19], 0` 循环 | GObjects 链表遍历：atexit 访问已销毁对象 |
| `mov rax, [rdx+8]` + `cmp [rax+18], 0` + `jmp back` | std::map 树旋转：堆损坏导致树循环 |
| `NtRaiseException` + `mov [rax-20], r10` | 二次异常：heap walker 访问坏指针 |

---

## 军械库 RPC 逆向（MetaserverLoadoutResponse）

### GetPlayerArchiveV2 链路

```
请求: UpdateRoleArchiveV2 → protobuf 编码
响应: ResponseWrapper { MessageId, ErrorCode=0 } → Inner { StatusCode=0 }
  → Native 解码通过 (RAX=1)
  → [?] 额外校验 → 判定失败 → ErrorCode=4
  → OnEquipCharacterSlotDelegate::Broadcast(ErrorCode=4) ← TMulticastInlineDelegate，不经过 ProcessEvent
  → Native 监听器跳过 SpawnInventory ← 我们 Hook 不到
  → BP 监听器什么都不做
```

### ProcessEvent vs Delegate Broadcast

| 方式 | 可 Hook | 示例 |
|------|:--:|------|
| ProcessEvent | ✅ | K2_OnEquipComplete, OnEquipCharacterSlotComplete |
| Delegate Broadcast | ❌ | OnEquipCharacterSlotDelegate → 跳过 SpawnInventory |

---

## 所有发射器 BP 配置

| 发射器 | 初始弹药 | 弹夹数 | BurstCount | bEnableBurst | bEnableAuto | 装弹(秒) |
|--------|:--:|:--:|:--:|:----:|:----:|:--:|
| AutoCanon | 15 | ? | 5 | true | — | 15 |
| Auto Missile | 25 | 1 | 5 | true | — | — |
| MiniGun | 100 | 3 | — | — | true | 4.2 |
| MiniGun+Shield | 200 | 1 | — | — | — | — |
| EMP Auto | 6 | ? | 3 | true | — | 6 |
| EMP Delay INF | 3 | 11 | — | — | — | — |
| Grenade Auto | 6 | ? | 3 | — | true | 18 |
| CQB (HE) | 1 | 3 | 3 | — | — | 6 |
| Smoke Squid | 1 | 3 | 3 | — | — | 12 |
| Impulse | 1 | 2 | 3 | — | — | 9 |
| Deployable ADS | 1 | 2 | 3 | — | — | 15 |

## 相关文档

- `02-DLL-Internals.md` — Hook 体系
- `03-Game-Fixes.md` — SideMount/PvE/Steam/UI 修复
