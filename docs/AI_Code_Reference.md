# AI Code Reference — Project Rebound Payload DLL

> **Purpose**: Survive `/compact` truncation. After every compaction, ask the AI to read this file first.
> **Last updated**: 2026-05-15
> **Session warning**: On 2026-05-15, `sed` and `git checkout` were used blindly, corrupting files
> and reverting uncommitted work. See [Emergency Recovery](#emergency-recovery) below.

---

## Critical Rules (Read First)

1. **NEVER use `sed` for mass renames.** Always use the Edit tool, one file at a time.
2. **NEVER use `git checkout` to "fix" things without first creating a backup branch.**
   Uncommitted changes ARE the current working state — `git checkout` destroys them.
3. **Only Edit tool for code changes.** No Bash scripting around source files.
4. **New files (not in git) survive git operations.** Git-tracked files are vulnerable.
5. **`git stash` before risky operations.** `git stash && risky-thing && git stash pop`.

---

## File Manifest

### New files (NOT in git — will survive `git checkout`)

| File | Purpose | Created |
|------|---------|---------|
| `Payload/Utility/UserNameFix.h` | Steam name resolution API | 2026-05-13 |
| `Payload/Utility/UserNameFix.cpp` | Steam name resolution + in-place FString write | 2026-05-13 |
| `Payload/Utility/PVECamFix.h` | PvE camera fix API | 2026-05-14 |
| `Payload/Utility/PVECamFix.cpp` | LevelSequence polling + LateJoin trigger | 2026-05-14 |
| `Payload/Utility/LauncherFix.h` | Launcher + projectile fix API | 2026-05-07 |
| `Payload/Utility/LauncherFix.cpp` | Side-mounted launcher fixes (~200 lines) | 2026-05-07 |
| `Payload/Utility/UIFix.h` | UI sprint shake diagnostic API | 2026-05-07 |
| `Payload/Utility/UIFix.cpp` | Sprint shake CamCache override | 2026-05-07 |
| `Payload/Loadout/LoadoutFix.h` | Loadout equip error swallow + HTTP fetch | 2026-05-07 |
| `Payload/Loadout/LoadoutFix.cpp` | Loadout fix logic (~300 lines) | 2026-05-07 |
| `Payload/Docs/LoadoutConfigSystem.txt` | Loadout config notes | 2026-05-07 |
| `docs/AI_Code_Reference.md` | This file | 2026-05-15 |
| `docs/ServerHangDoc.md` | Server hang root cause + fix doc | 2026-05-12 |
| `docs/Launcher Fixing Docs.md` | Launcher fix documentation | 2026-05-07 |
| `docs/MetaserverLoadoutResponse.md` | Metaserver loadout RPC analysis | 2026-05-07 |

### Deleted files

| File | Reason | Date |
|------|--------|------|
| `Payload/Utility/SteamNameResolver.h` | Replaced by UserNameFix.h | 2026-05-14 |
| `Payload/Utility/SteamNameResolver.cpp` | Replaced by UserNameFix.cpp | 2026-05-14 |

### Git-tracked files modified (VULNERABLE to `git checkout`)

| File | Key Changes | Verification Check |
|------|------------|-------------------|
| `Payload/Hooks/Hooks.cpp` | PostLogin, TickFlush, ProcessEvent hooks, all handler calls | grep for `UserNameFix_OnPostLogin`, `PVECamFix_Tick`, `HandleLauncherServerEvent`, `HandleLauncherClientEvent`, `HandleProjectileClientEvent`, `HandleUICharacterClientEvent`, `HandleEquipErrorSwallow`, `LoadoutFix_FlushRefresh`, `LoadoutFix_FetchAndLog` |
| `Payload/ServerLogic/ServerLogic.cpp` | `ExitProcess`→`TerminateProcess`, `OutputDebugStringA` exit logs | grep `TerminateProcess` |
| `Payload/ServerLogic/ServerLogic.h` | `IsServerShutdownRequested`, `IsTerminalRoundState` | grep these names |
| `Payload/ServerLogic/LateJoinManager.h` | `ForceFirstLifeSpawn` declaration | grep `ForceFirstLifeSpawn` |
| `Payload/ServerLogic/LateJoinManager.cpp` | `ForceFirstLifeSpawn` impl, `QueueLateJoinPlayer` | grep `ForceFirstLifeSpawn` |
| `Payload/Network/Network.cpp` | `SendJsonPost` shutdown guard, WinHTTP per-request | grep `IsServerShutdownRequested` inside `SendJsonPost` |
| `Payload/Network/Network.h` | Removed `ShutdownHttpSession` | grep `ShutdownHttpSession` should return NOTHING |
| `Payload/Network/NetDriverAccess.cpp` | `ScanForNetDriver` shutdown guard + include | grep `IsServerShutdownRequested` |
| `Payload/Utility/Utility.cpp` | `getObjectsOfClass`/`GetLastOfType` guards + includes | grep `IsServerShutdownRequested` |
| `Payload/dllmain.cpp` | `#include "Utility/UserNameFix.h"` | grep `UserNameFix` in includes |
| `Payload/Debug/Debug.h` | `ServerDebugLogEnabled`, `ClientDebugLogEnabled` | grep `DebugLogEnabled` |
| `Payload/Debug/Debug.cpp` | `ServerDebugLogEnabled`/`ClientDebugLogEnabled` defaults, `ServerDebugLog()`/`ClientLog()` gating | grep `DebugLogEnabled` |
| `Payload/Config/Config.cpp` | `-clientlog`/`-serverlog` flags, `ServerDebugLogEnabled`/`ClientDebugLogEnabled` set | grep `DebugLogEnabled` |
| `Payload/Replication/libreplicate.cpp` | ChannelsToClose processing in tick | grep `ChannelsToClose` |
| `Payload/Replication/libreplicate.h` | ChannelsToClose + mutex declarations | grep `ChannelsToClose` |
| `Payload/Payload.vcxproj` | New file entries (UserNameFix, PVECamFix, LauncherFix, UIFix, LoadoutFix) | grep these names |
| `ServerLauncherGUI/.../wrapper.cpp` | taskkill force kill, log rotation, `EnsureLogOpen` | grep `taskkill /F /T` |

---

## Feature-by-Feature Change Log

### 1. Server Hang Fix (2026-05-11)

Problem: DLL-injected server hangs after ~20 hours; wrapper watchdog fails to restart.

Files changed:
- `Payload/ServerLogic/ServerLogic.cpp`: `ExitProcess(0)` → `TerminateProcess(GetCurrentProcess(), 0)`
- `Payload/ServerLogic/ServerLogic.cpp`: Added `OutputDebugStringA` exit logging
- `Payload/Network/Network.cpp`: `SendJsonPost` gets `IsServerShutdownRequested()` early return
- `Payload/Network/Network.cpp`: WinHTTP session per-request (no pooling — `WinHttpOpen`/`Close` per call)
- `Payload/Network/Network.h`: Removed `ShutdownHttpSession` declaration
- `Payload/Network/NetDriverAccess.cpp`: `ScanForNetDriver` gets shutdown guard + include
- `Payload/Utility/Utility.cpp`: `getObjectsOfClass`/`GetLastOfType` get shutdown guards + includes
- `Payload/Hooks/Hooks.cpp`: TickFlush terminal round detection → `return TickFlush.call(...)`
- `ServerLauncherGUI/.../wrapper.cpp`: `StopServerLocked` uses `taskkill /F /T` directly, no `TerminateProcess`
- `ServerLauncherGUI/.../wrapper.cpp`: `EnsureLogOpen` log rotation (1MB limit, `_N` suffix)
- `ServerLauncherGUI/.../wrapper.cpp`: `StartTimestampLogger` (every 30s `yymmddhhmmss`)

Verification: `grep -n "TerminateProcess\|EXIT-GUARD\|taskkill /F\|EnsureLogOpen\|TimestampLogger"` in relevant files.

### 2. Steam Name Resolution (2026-05-13)

Problem: Scoreboard shows SteamID64 instead of Steam display name.

Files:
- `Payload/Utility/UserNameFix.h/.cpp`: Steam Web API resolver + in-place FString write
- `Payload/Hooks/Hooks.cpp`: `PostLogin` calls `UserNameFix_OnPostLogin`
- `Payload/Hooks/Hooks.cpp`: `TickFlushHook` calls `UserNameFix_DrainPending`
- `Payload/dllmain.cpp`: `#include "Utility/UserNameFix.h"`

Key implementation: `InPlaceFStringWrite()` writes directly into FString buffer at offset 0x0300 (PlayerNamePrivate) to avoid CRT/engine allocator conflicts.
FString Count includes null terminator (`count = needed + 1`).

Verification: `grep "UserNameFix\|NAME-FIX\|InPlaceFStringWrite\|0x0300"` in relevant files.

### 3. PvE Camera Fix (2026-05-14)

Problem: PvE intro cinematic detaches camera from player. First-life pawn broken.

Files:
- `Payload/Utility/PVECamFix.h/.cpp`: LevelSequence polling → `ForceFirstLifeSpawn`
- `Payload/ServerLogic/LateJoinManager.h/.cpp`: `ForceFirstLifeSpawn(PC)` — routes via LateJoin spawn chain
- `Payload/Hooks/Hooks.cpp`: `TickFlushHook` calls `PVECamFix_Tick(NetDriver, DeltaTime)`

Detection: Polls `ALevelSequenceActor::SequencePlayer` Status via memory offset 0x02B0 (not ProcessEvent).
Trigger: When Status transitions from Playing(1) → non-Playing.

Known issue: First-life launchers (grappling hook, mobility) don't work. Needs further investigation into respawn flow. Not fixed yet — deferred.

Verification: `grep "PVECamFix\|CAM-FIX\|ForceFirstLifeSpawn\|0x02B0"` in relevant files.

### 4. Launcher Fixes (2026-05-07)

Problem: Side-mounted launchers don't work on DS (IsLocallyControlled pattern).

Files:
- `Payload/Utility/LauncherFix.h/.cpp`: State machine fix, dud blocking, projectile visuals
- `Payload/Hooks/Hooks.cpp`: Server + client ProcessEvent dispatch to LauncherFix
- `Payload/Replication/libreplicate.cpp`: ChannelsToClose processing in tick

Verification: `grep "HandleLauncherServerEvent\|HandleLauncherClientEvent\|HandleProjectileClientEvent\|ChannelsToClose"` in relevant files.

### 5. Loadout Armory Fix (2026-05-07 — deferred)

Problem: Armory shows "unknown error" when equipping items.

Files:
- `Payload/Loadout/LoadoutFix.h/.cpp`: Equip error swallow, HTTP loadout fetch, F8 hotkey refresh
- `Payload/Hooks/Hooks.cpp`: `HandleEquipErrorSwallow`, `LoadoutFix_FlushRefresh`, `LoadoutFix_FetchAndLog`

Status: Equip error swallowing works. Model refresh on equip — partially broken, deferred.

Verification: `grep "LoadoutFix\|HandleEquipError\|EquipError\|LOADOUT"` in Hooks.cpp.

### 6. UI Sprint Shake Fix (2026-05-07)

Problem: Sprint shake persists when not sprinting on DS.

Files:
- `Payload/Utility/UIFix.h/.cpp`: CamCache zeroing when idle, synthetic sine wave when sprinting
- `Payload/Hooks/Hooks.cpp`: `HandleUICharacterClientEvent` in ProcessEventClient

Verification: `grep "UIFix\|HandleUICharacter\|UI-FIX"` in Hooks.cpp.

### 7. Wrapper Logging (2026-05-14)

Problem: Single log file grows unbounded; no timestamps.

Files:
- `ServerLauncherGUI/.../wrapper.cpp`: `EnsureLogOpen()`, `StartTimestampLogger()`, `g_CurrentLogPath`

Verification: `grep "EnsureLogOpen\|StartTimestampLogger\|g_CurrentLogPath"` in wrapper.cpp.

### 8. Wrapper Force Kill (2026-05-14)

Problem: `TerminateProcess` can't kill kernel-stuck zombie processes.

Files:
- `ServerLauncherGUI/.../wrapper.cpp`: `StopServerLocked()` uses `taskkill /F /T /PID` directly

Verification: `grep "taskkill /F /T"` in wrapper.cpp.

---

## Emergency Recovery

### If source files were corrupted by `sed` or `git checkout`:

1. **Do NOT run more sed or git checkout.**
2. Read this file's "Git-tracked files modified" table above.
3. For each file, grep for the verification check string.
4. If missing, restore from the Feature-by-Feature Change Log description.
5. New files (not in git) are safe — they can't be reverted by `git checkout`.

### Quick audit command (run from Payload/):
```bash
echo "=== Hooks ===" && grep -c "UserNameFix_OnPostLogin\|PVECamFix_Tick\|HandleLauncherServerEvent" Hooks/Hooks.cpp
echo "=== ServerLogic ===" && grep -c "TerminateProcess\|EXIT-GUARD" ServerLogic/ServerLogic.cpp
echo "=== Network ===" && grep -c "EXIT-GUARD" Network/Network.cpp Network/NetDriverAccess.cpp
echo "=== Utility ===" && grep -c "EXIT-GUARD" Utility/Utility.cpp
echo "=== dllmain ===" && grep -c "UserNameFix" dllmain.cpp
echo "=== LateJoin ===" && grep -c "ForceFirstLifeSpawn" ServerLogic/LateJoinManager.cpp ServerLogic/LateJoinManager.h
echo "=== libreplicate ===" && grep -c "ChannelsToClose" Replication/libreplicate.cpp
echo "=== vcxproj ===" && grep -c "UserNameFix\|PVECamFix\|LauncherFix\|UIFix" Payload.vcxproj
```
All counts should be >= 1.

---

## Architecture Principles

1. **No ProcessEvent FString operations from hooks.** Use direct memory offset reads/writes (e.g., `InPlaceFStringWrite` at offset 0x0300).
2. **Async HTTP in PostLogin.** Steam name resolution runs on detached thread to avoid blocking game thread.
3. **Game-thread operations via TickFlush.** `ChangeName`/`Possess` must be on game thread; use pending queues drained in `TickFlushHook`.
4. **SDK classes in `namespace SDK`.** All forward declarations must be inside `namespace SDK { }` or use `SDK::` prefix.
5. **Wrapper owns process lifecycle.** DLL should never call `ExitProcess` — use `TerminateProcess`.

---

## Debug Flags

| Flag | Controls | Where |
|------|----------|-------|
| `-serverdebuglog` | `ServerDebugLogEnabled` — gates `ServerDebugLog()` output | Config.cpp |
| `-clientdebuglog` | `ClientDebugLogEnabled` — gates `ClientDebugLog()` output | Config.cpp |

## Related Docs

| Doc | Content |
|-----|---------|
| `docs/LogSystemDoc.md` | Log architecture, LogManager thread, function catalog, rules |
| `docs/ServerHangDoc.md` | Server hang root cause, 4 rounds of investigation, all fixes including watchdog/watchlist |
