# Session Compact: 2026-05-15 to 2026-05-18

## Log System Refactor

### Final Architecture
- Four-function system: `ServerLog()` always-on, `ServerDebugLog()` gated by `-serverdebuglog`, `ClientLog()` always-on, `ClientDebugLog()` gated by `-clientdebuglog`
- LogManager background thread with batch flush (30-line intervals) via `std::deque` + mutex + condition_variable
- `Debug.cpp`: WorkerLoop drains queue → writes to stdout with immediate flush for essential logs, batch for diagnostic
- `Debug.h`: Declares all 4 functions + `ServerDebugLogEnabled` / `ClientDebugLogEnabled` flags
- `Config.cpp`: Parses `-serverdebuglog` / `-clientdebuglog` flags

### Key Lessons from Refactor
- NEVER use `sed` or batch operations on source files — only Edit tool
- NEVER use `Write` tool to overwrite files — it drops functions not included
- NEVER use `git checkout` without backup — uncommitted changes ARE the working state
- `replace_all` with short substrings like `Log(` hits `ClientLog(`, `ServerDebugLog(`, `LoadoutFix_FetchAndLog` etc.
- Always use exact `old_string` with surrounding context for renames

### Files Converted
| File | From | To |
|------|------|----|
| `LateJoinManager.cpp` | `std::cout` | `ServerDebugLog()` (13 lines) |
| `UserNameFix.cpp` | `std::cout` | `ServerDebugLog()` (5 lines) |
| `PVECamFix.cpp` | `std::cout` | `ServerDebugLog()` (1 line) |
| `Hooks.cpp` | `std::cout` | `ServerDebugLog()` (7 lines + NO TICKY) |
| `ServerLogic.cpp` | `std::cout` | `ServerLog()` (lifecycle) |
| `Config.cpp` | `std::cout` | `ServerLog()` |
| `dllmain.cpp` | `std::cout` | `ServerLog()` |
| All `ClientLog()` | → | `ClientDebugLog()` (gated) |
| `Log()` everywhere | → | `ServerLog()` (always-on) |
| `LauncherFix.cpp` | compile-time gate | runtime `ServerDebugLog()` |
| `DebugLocateSubsystems` | `std::cout` | `ClientDebugLog()` |

### Remaining Log TODO
- Wrapper `EnsureLogOpen` optimization (reduce file_size checks)
- Timestamp from wrapper → DLL LogManager
- Wrapper-side pipe+file management thread (separate project)

## PvE Camera Fix

### Problem
PvE intro cinematic detaches camera from player. Sequences longer than CountdownToStart on many maps.

### Solution
- `PVECamFix.cpp`: Poll `ALevelSequenceActor::SequencePlayer` Status via memory offset 0x02B0 — NOT ProcessEvent
- Trigger: Status transitions from Playing(1) → non-Playing
- `LateJoinManager::ForceFirstLifeSpawn(PC)`: Queues player with RoleConfirmed state → Tick() drives spawn
- Per-tick Possess window for 10 seconds after sequence ends to catch pawn spawn

### Known Issue
- First-life launchers (grappling hook, mobility) don't work until first respawn
- Root cause: spawn flow differs from natural respawn — launcher equipment not properly initialized
- Deferred. Not blocking.

## Steam Name Resolution

### Problem
Scoreboard shows SteamID64 instead of display name.

### Solution
- `UserNameFix.cpp`: WinHTTP GET to `steamcommunity.com/profiles/{steamid}/?xml=1` — no API key
- In-place FString write at offset 0x0300 (PlayerNamePrivate) to avoid CRT/engine allocator conflict
- FString Count includes null terminator: `count = needed + 1`
- Async PostLogin hook → pending queue → TickFlush drain on game thread
- Thread-safe: store `std::string` in pending queue, construct `FString` on game thread

### Known Issue
- Scoreboard ID row still shows /765611 (SteamID prefix)
- `GetDefaultIDStr()` is Native Final — not routed through ProcessEvent, cannot intercept
- Will require binary function hook. Deferred.

## Server Hang Fixes
- `ExitProcess(0)` → `TerminateProcess(GetCurrentProcess(), 0)` in `DelayedExitAfterMatchEnd`
- `OutputDebugStringA` exit logging throughout
- `SendJsonPost` early return on `IsServerShutdownRequested()`
- `ScanForNetDriver` shutdown guard
- `getObjectsOfClass` / `GetLastOfType` shutdown guards
- TickFlush terminal round → `return TickFlush.call(...)`
- WinHTTP session per-request (no pooling — prevents heap corruption)

## Wrapper Improvements

### Kill Server Flow
- `TerminateProcess` → 1s `WaitForSingleObject` → escalate to `taskkill /F /T /PID`
- No more `system()` blocking main thread
- Default mode changed to offline, default game mode to PVE

### Watchdog
- Heartbeat timeout 30s
- 20-minute backend serverlist health check via unique serverId
- `InitServerUniqueId()`: 8-char hex, persisted to `serverId.json`, loaded from `serverconfig.json`
- `project_rebound_version.txt` in Release.zip for version tracking

### Logging
- Log rotation at 1MB with `_N` suffix
- 30-second wall-clock timestamps
- `PrintMapList` / `help` command output → `LauncherLog()` for GUI visibility

## Toolbox (rust-boundary-tool-box) Changes

### ProjectRebound Integration
- `PROJECT_REBOUND_ONLINE_FILES`: Payload.dll, ServerLauncher/ProjectReboundServerLauncher.exe, slint_cpp.dll, BoundaryMetaServer-main, project_rebound_version.txt
- `LaunchFiles`: wrapper_exe → launcher_exe pointing to `ServerLauncher/ProjectReboundServerLauncher.exe`
- `launch_files()`: `target_win64.join("ServerLauncher").join("ProjectReboundServerLauncher.exe")`
- `launch_pve()`: `hidden_command(launcher_exe).current_dir(ServerLauncher).arg("-cli").spawn()`
- PVE game launch adds `-match=127.0.0.1`
- CWD changed to `ServerLauncher\` so Launcher finds game exe via `..\ProjectBoundarySteam-Win64-Shipping.exe`

### PR Version Check
- `UpdateCheckResult`: added `project_rebound_tag`, `project_rebound_local_tag`, `pr_is_newer`, `project_rebound_published_at`, `project_rebound_url`
- `check_latest_release(target_win64)`: reads local `project_rebound_version.txt`, compares with GitHub API `releases/latest`
- `fetch_project_rebound_tag()`: GitHub API with `.no_proxy()` to bypass local proxy conflicts
- Dialog split into "工具箱版本" and "社区服版本" sections
- `pr_is_newer` triggers "安装/更新" button → calls `start_install()`

### Install Flow Simplified
- `extract_zip_to_dir()`: unzips entire Release.zip to Win64 — no per-file validation
- `is_nodejs_online_item`: returns `true` (downloaded online)
- `is_boundary_meta_server_online_item`: returns `false` (from Release.zip)
- Node.js: in `core.rs` MANAGED_ITEMS (online download), NOT in `build.rs` MANAGED_ITEMS
- `proxied_github_url`: added `api.github.com/` support

### Port Fix
- Metaserver TCP: `server.listen(6968)` → `server.listen(6969)` to match endpoint response
- `REQUIRED_TCP_PORTS` and `MONITORED_PORTS` already have 6969

### Default Config
- `OfflineMode = true`, `CurrentMode = "pve"`
- `serverconfig.json` includes `serverUniqueId`
- `GameExePath = "..\\ProjectBoundarySteam-Win64-Shipping.exe"` (CWD is ServerLauncher\)

### Bug Fixed: `file_name()` Path Matching
- `read_project_rebound_release_file` was using `Path::new(name).file_name()` which stripped directory components
- `ServerLauncher/ProjectReboundServerLauncher.exe` was matched as just `ProjectReboundServerLauncher.exe` — failed
- Fixed: exact path matching `name.eq_ignore_ascii_case(item_name)`

### Bug Fixed: Missing User-Agent
- `fetch_project_rebound_tag` was missing `User-Agent` header → GitHub returned 403
- Added `.header("User-Agent", format!("boundary-toolbox/{APP_VERSION}"))`

### Bug Fixed: Payload Not Found
- `build.rs` `MANAGED_ITEMS` included `MapleMono-NF-CN-unhinted.zip` but file was missing from payload directory
- `find_payload_root` requires ALL items → failed entirely
- Fix: removed font from build.rs list (font handled by online download)

### Bug Fixed: ClientLog → ClientDebugLog Substring Collision
- `replace_all Log(` hit `ClientLog(` changing it to `ClientDebugServerLog(`
- Also: `LoadoutFix_FetchAndLog` → `LoadoutFix_FetchAndServerLog`
- Fixed with matching exact function names

### Bug Fixed: Double Dialog
- Added log to track `UpdateCheckFinished` events for diagnosis

### Remaining Toolbox TODO
- Separate install/update flows (don't re-download Node.js every time)
- Launch progress bar with port verification
- Close guard: detect all processes (wrapper, game, node) before exit
- Wrapper background image not showing on some machines
- Font/Node.js embedding in payload (blocked by colleague's insistence on online downloads)
- PR auto-download on update check (currently only shows dialog)

## Wrapper (ServerLauncherGUI) Changes
- `SaveConfigFile()` + `LoadConfigFile()`: added `serverUniqueId`
- `serverId.json` legacy load in `InitServerUniqueId()` — also loaded from `serverconfig.json`
- `PrintMapList` / `help` → `LauncherLog()` instead of `std::cout`

## Key Architecture Decisions
1. All DLL log output routes through LogManager thread — no direct `std::cout` from hooks
2. FString operations in ProcessEvent context avoided — use direct memory offset reads/writes
3. SDK classes in `namespace SDK` — forward declarations must use `SDK::` prefix
4. Async HTTP in PostLogin — Steam name resolution on detached thread
5. Game-thread operations via TickFlush — pending queues drained on main thread
6. Wrapper owns process lifecycle — DLL calls TerminateProcess, never ExitProcess

## Next Session Priorities
1. Fix first-life launcher/mobility issue in PvE
2. Scoreboard ID row replace /765611 with custom label
3. Toolbox: separate PR update from full install
4. Toolbox: launch progress bar + port check
5. GitHub API proxy compatibility (tested with `.no_proxy()`, more testing needed)
6. Merge Changes_For_Server_Launcher.md changes with colleague
7. Build and test full Release.zip distribution

---

## Deep Technical Details

### LogManager Implementation (Debug.cpp)
```cpp
// Queue + worker thread
struct LogEntry { std::string msg; bool immediate; };
static std::deque<LogEntry> g_Queue;
static std::mutex g_QueueMutex;
static std::condition_variable g_QueueCv;
static std::thread g_Worker;
static bool g_WorkerRunning = false;

void WorkerLoop() {
    std::deque<LogEntry> local;  // swap to local to minimize lock time
    int sinceFlush = 0;
    while (true) {
        {
            std::unique_lock<std::mutex> lock(g_QueueMutex);
            g_QueueCv.wait(lock, [] { return !g_Queue.empty() || !g_WorkerRunning; });
            if (g_Queue.empty() && !g_WorkerRunning) return;
            local.swap(g_Queue);  // O(1) swap, no copy
        }
        for (auto &entry : local) {
            std::cout << entry.msg << "\n";
            ++sinceFlush;
            if (entry.immediate || sinceFlush >= 30) {
                std::cout << std::flush;
                sinceFlush = 0;
            }
        }
        local.clear();
    }
}
```
Key: `PushEntry()` calls `EnsureLogThread()` which lazily starts the worker on first use. Thread-safe via mutex+condvar. `std::deque::swap` is O(1). `\n` not `std::endl` — avoids per-line flush.

### In-Place FString Write (UserNameFix.cpp)
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
Why not FString::operator=: SDK CRT `delete[]` would try to free engine-allocated Data pointer → heap corruption → "STanNam" (mixed garbage). FString layout confirmed via SDK: `TCHAR*` (8 bytes) + `int32 Count` (4) + `int32 Max` (4) = 16 bytes = 0x10.

### LevelSequence Detection (PVECamFix.cpp)
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
All reads via direct memory offsets — no ProcessEvent, no SDK wrappers.

### SDK Namespace Trap
`SDK/Basic.hpp:21`: `namespace SDK {` wraps ALL classes. Forward declaration `class APBPlayerController;` in GLOBAL namespace creates `::APBPlayerController` which is DIFFERENT from `SDK::APBPlayerController`. Fix: either `namespace SDK { class APBPlayerController; }` or `#include "../SDK.hpp"` in header, then use `SDK::APBPlayerController*`.

### GetDefaultIDStr Not Interceptable
- `APBPlayerState::GetDefaultIDStr()`: `Final, Native, Public, BlueprintCallable, BlueprintPure, Const`
- Goes through ProcessEvent for BP calls, but NOT for native C++ calls (scoreboard BP code calls it through BP VM → ProcessEvent, but `PlayerStateUI.PlayerName` uses native base class `UPBPlayerInfoWidget` which calls GetDefaultIDStr natively)
- PlatformUniqueIDJsonString at offset 0x03D8 — NOT read by GetDefaultIDStr
- Scoreboard ID display: `UKismetStringLibrary::Left(GetDefaultIDStr, 6)` → `/765611`
- Solution requires binary function hook (SafetyHook on vtable entry)

### Steam API Details
```
GET https://steamcommunity.com/profiles/{steamid64}/?xml=1
Response: <?xml><profile><steamID><![CDATA[PlayerName]]></steamID></profile>
```
- No API key required. 3s WinHTTP timeout. Cache in `std::unordered_map<std::string, std::string>`.
- PostLogin hook spawns `std::thread([GameMode, PC, steamIdStr](){...}).detach()` — async, non-blocking.
- Must use `std::string` in PendingNameChange struct — FString not thread-safe across CRT boundaries.

### BP Findings from FModel Exports
- `PBCharacter_BP.cpp`: `K2_EquipedLauncher()` fires on launcher equip
- `UMG_PlayerInfo_InGame.cpp`: `PlayerStateUI.PlayerName` for name, `GetDefaultIDStr` for ID
- `PlayerController_BP.cpp`: `K2_ControllerClientRestart()` at line 1103, `K2_PawnReplicatedPossess()` at line 1111, `PossessOn()` at line 2466 (empty stub)
- PvE MatchIntro: `EPBMatchPhase::MatchIntro`(2) → `K2_StartMatchIntro()` on GameState and PlayerController
- `UMG_MatchState_C`: shows MatchIntroAnim widget animation during intro
- Sequence sub-levels: `MapName_Sequence.json` LevelScriptActor with `MatchStart` LevelSequenceActor containing 4+ CineCameraActors

### SDK Offsets Reference
| Offset | Class | Field | Notes |
|--------|-------|-------|-------|
| 0x0300 | APlayerState | PlayerNamePrivate | FString, Net+RepNotify |
| 0x0250 | APlayerState | UniqueId | FUniqueNetIdRepl, Net+RepNotify |
| 0x03D8 | APBPlayerState | PlatformUniqueIDJsonString | FString, Net+RepNotify |
| 0x08D0 | APBPlayerState | PlayerNameBeforeFilter | FString, Protected |
| 0x0250 | ALevelSequenceActor | SequencePlayer | ULevelSequencePlayer* |
| 0x02B0 | UMovieSceneSequencePlayer | Status | EMovieScenePlayerStatus (uint8) |
| 0x02B8 | APlayerController | PlayerCameraManager | APlayerCameraManager* |
| 0x0290 | AGameModeBase | DefaultPlayerName | FText ("UserName") |
| 0x0598 | APBPlayerController | AllyCameraComponent | UCameraComponent* |
| 0x05B0 | APBPlayerController | PBCharacter | APBCharacter* |

### ChangeName Trap
`AGameModeBase::ChangeName()` goes through ProcessEvent. When passed FString via SDK wrapper, ProcessEvent copies FString bytes from Params struct. If SDK's FString layout doesn't match engine native FString (Data pointer at wrong offset), engine reads null → falls back to `DefaultPlayerName` ("UserName"). This is why `ChangeName("STanJK")` produced "UserName". Fix: bypass ChangeName entirely, write PlayerNamePrivate directly.

### CommandFramework Internal Log()
`CommandFramework.h:144`: `void Log(const std::string& msg);` — renamed to `CommandLog()` to avoid collision with global `Log()`. ALL `sed` operations must exclude CommandFramework files. The `onLog` callback routes to `ClientLog`, set at `dllmain.cpp:169`.

### Font Download Fallback Chain
`MapleMono-NF-CN-unhinted.zip` (19.6MB) → fallback `MapleMono-NF-CN.zip` (152MB). "CN" = Chinese character support. If unhinted not found in release assets, downloads the full 152MB file. Fixed by embedding in payload.

### Node.js Version Requirement
MUST be v24.14.0. Other versions have protobuf parsing incompatibility with BoundaryMetaServer's `protobufjs` usage. Version is checked via `node -v` before npm install.

### Port Architecture (TestBuild metaserver)
```
HTTP API:    8000  (login, server status, /connectServer)
TCP RPC:     6969  (game client protobuf communication — WAS 6968, fixed)
UDP QoS:     9000  (matchmaking UDP ping/pong)
TCP MM:      9000  (matchmaking TCP)
```
Proxy was: game → 6969 proxy → 6968 metaserver. Without proxy: game → 6969 metaserver directly.

### IsLocallyControlled Pattern (from Launcher Fixing Docs)
The core pattern across ALL DS bugs:
- In ListenServer: `IsLocallyControlled()` = true for host → all BP logic runs
- In Dedicated Server: `IsLocallyControlled()` = false for ALL → every BP function with this guard silently skipped
- Affected: state transitions, flag clearing, visual effects, HUD updates, sound effects, launcher animations
- Fix pattern: 1) Identify ProcessEvent that fires but doesn't execute 2) Add hook 3) Manually trigger skipped logic
- This pattern likely applies to ANY other DS-broken system

### Toolbox Update Check Flow
1. `initialize()` → `start_ui_font_check()` → if font installed, `start_update_check(true)`
2. `start_update_check` → spawns thread → `check_latest_release(target)` → GitHub API
3. Two API calls: toolbox releases + ProjectRebound releases/latest
4. Result via `AppMessage::UpdateCheckFinished` → handler shows dialog
5. `pr_is_newer` computed: compare `project_rebound_local_tag` (from version.txt) with `project_rebound_tag` (from API)
6. If newer or not installed → "安装/更新" button → `start_install()`

### Toolbox Install Flow (Simplified)
1. Download Release.zip from GitHub (dynamic URL with latest tag)
2. `extract_zip_to_dir()` → unzips everything to Win64\
3. Download BoundaryMetaServer if online item (currently from Release.zip)
4. Download Node.js if online item (from nodejs.org)
5. Write `project_rebound_version.txt` with latest tag
6. Write `state.json` with install metadata
7. ServerLauncher launched with `-cli`, CWD = `Win64\ServerLauncher\`

### Toolbox Key File Paths
- `Win64\installer_tool\state.json` — install state/metadata
- `Win64\installer_tool\app_config.json` — user preferences (language, proxy)
- `Win64\project_rebound_version.txt` — installed PR version
- `Win64\serverconfig.json` — ServerLauncher config (map, mode, ports, serverId)
- `Win64\ServerLauncher\ProjectReboundServerLauncher.exe` — Launcher binary
- `Win64\ServerLauncher\slint_cpp.dll` — Slint runtime

### Fatal Errors & Recovery
1. **sed `s/\Log(/ServerLog(/g'`**: Corrupted CommandFramework, ClientLog→ClientDebugServerLog. Recovery: git checkout.
2. **git checkout lost 2 days work**: Uncommitted changes in 15+ tracked files. Recovery: transcript + ServerHangDoc + manual re-apply.
3. **Write() overwrote Debug.cpp**: Dropped InitDebugConsole, HotkeyThread, etc. Recovery: git show + append.
4. **replace_all `Log(` → `ServerLog(`**: Hit ClientLog, ServerDebugLog, LoadoutFix_FetchAndLog. Recovery: manual fix each.
5. **payload root not found**: Missing MapleMono zip. Recovery: removed font from build.rs.
6. **file_name() path matching bug**: ServerLauncher/ProjectReboundServerLauncher.exe not found in zip. Recovery: exact path match.
7. **Missing User-Agent header**: 403 from GitHub API. Recovery: added header.
8. **Node.js not installed**: Removed from core.rs MANAGED_ITEMS. Recovery: added back.

### Toolbox Rust Compilation Notes
- Must set `BOUNDARY_PAYLOAD_ROOT` env var during build
- `cargo build --release` takes ~60s on rebuild, ~5min on clean
- PAYLOAD_ZIP_BYTES: `include_bytes!(concat!(env!("OUT_DIR"), "/payload.zip"))` — embedded at compile time
- `find_payload_root`: checks ALL MANAGED_ITEMS exist; single missing file = fails
- `build.rs` MANAGED_ITEMS ↔ `core.rs` MANAGED_ITEMS are separate lists! build.rs for payload embedding, core.rs for install processing

---

## Extended Reference: SDK, BP, and Reverse Engineering Knowledge

### Hooks.cpp Full Architecture
```
Server ProcessEvent Hook:
  QuickRespawn → PlayerRespawnAllowedMap[PC] = true
  ServerRestartPlayer → check PlayerRespawnAllowedMap, deny if false
  ClientBeKilled → PlayerRespawnAllowedMap[PC] = false
  PlayerCanRestart → return HasMatchStarted()
  ServerConfirmRoleSelection → LateJoinManager::OnRoleConfirmed
  Launcher server events → HandleLauncherServerEvent (LauncherFix)
  All others → ProcessEvent.call(original)

Client ProcessEvent Hook:
  EnterGameConstruct → PressSpace() after 1s
  EnterGameActivated → PressSpace() after 1s
  MainMenuConstruct → LoadoutFix_FetchAndLog()
  ConnectMatchServerTimeout → ConnectToMatch()
  Launcher → HandleLauncherClientEvent
  Projectile → HandleProjectileClientEvent
  Character → HandleUICharacterClientEvent (UIFix)
  EquipError → HandleEquipErrorSwallow (LoadoutFix)
  After call → LoadoutFix_FlushRefresh()

PostLogin Hook:
  PostLoginHook.call(original) → UserNameFix_OnPostLogin(GameMode, PC) → LateJoinManager::OnPostLogin

TickFlush Hook:
  NoteServerGameTick() → UserNameFix_DrainPending() → PVECamFix_Tick(NetDriver, DeltaTime)
  → replication batch → LateJoinManager::Tick(DeltaTime) → round state checks
  → canStartMatch / StartMatch → terminal round detection
```

### LauncherFix Deep Architecture
**Server-side handler** (`HandleLauncherServerEvent`):
- `ServerFiring`: logs AmmoInClip/TotalAmmo/bIsFiring/BurstCtr/State → blocks fire if AmmoInClip==0 && !HasInfiniteAmmo()
- `K2_Standby`: logs bIsFiring/bPendingFiring/BurstCtr/State
- Returns TRUE to consume event, FALSE to let original ProcessEvent run

**Client-side handler** (`HandleLauncherClientEvent`):
- `OnRep_PendingState`: core fix → forces `CurrentState = PendingState`, clears flags at Standby(0)/Ready(3)
- State 0 (Standby): `bIsFiring=false, bPendingFiring=false, BurstCounter=0, bIsFireControlEnabled=false, K2_Standby()`, `OnHidden_Event()` on ProjectilePathTracer for Deploy types
- State 1: `K2_Deploying()`
- State 2: `K2_Undeploying()`, `OnHidden_Event()`
- State 3 (Ready): `bIsFiring=false, bPendingFiring=false, K2_Ready()`
- State 4 (Reloading): `K2_Reloading(), K2_ASingleAmmoReloaded()`
- State 5: `K2_Handup()`
- `ServerFiring`: blocks dud if AmmoInClip==0
- `OnRep_Exploded` → `MulticastExplode(DummyHit)` forces visual effects

**Why flags get stuck**: `bIsFiring`, `bPendingFiring`, `BurstCounter`, `bIsFireControlEnabled` set by client BP during fire. Cleared during `K2_Standby`. But `K2_Standby` gated behind `IsLocallyControlled()`. DS client: IsLocallyControlled=false → K2_Standby skipped → flags never cleared → ServerFiring refused.

**Dud projectile root cause**: BP async timer `FireConfig.TimeCanRetriggerFire=0.25s` starts after first ServerFiring. Timer fires → calls ServerFiring again. But ammo already consumed → creates projectile with AmmoInClip=0. In ListenServer: ammo state consistent, harmless. In DS: dud arrives after real projectile, can overwrite replication.

**Projectile explosion missing**: `MulticastExplode` multicast RPC triggers visuals on all clients. Native code: `if (IsLocallyControlled()) { MulticastExplode(...); }` → NOT called on DS. `OnRep_Exploded` fires but BP handler has IsLocallyControlled gate → does nothing.

### UIFix Deep Architecture
- `HandleUICharacterClientEvent`: called from client ProcessEvent for APBCharacter objects
- `TickHelmetOffset`: fires every frame for locally controlled character (BP IsLocallyControlled gate)
- `CameraModifiers_CacheRelativeLocation` at offset 0x2810 (FVector, 12 bytes)
- `CameraModifiers_CacheRelativateRotation` at offset 0x281C (FRotator, 12 bytes)
- Fix: zero CamCache when bIsRunning==0 AND CharStatus is Idle(0)/SlowlyMoving(1)
- Sprint: synthetic sine wave on Cache (freq 8.7-12.3Hz, amplitude Loc~0.015-0.02, Rot~0.04-0.06)
- Debug logging: CamD (delta between frames) and WpD (weapon delta) logged every 60 frames
- `UIFixDebugEnabled` compile-time toggle (currently true — should be false for release)

### LoadoutFix Deep Architecture
- `HandleEquipErrorSwallow`: intercepts `OnEquipCharacterSlotComplete` and `K2_OnEquipComplete`
- Both have `EPBEquipErrorCode` param → zeroes it to suppress "unknown error"
- `LoadoutFix_FetchAndLog()`: WinHTTP GET from `127.0.0.1:8000/api/loadout/{playerId}`
- `LoadoutFix_FlushRefresh()`: main-thread spawn, calls SpawnInventory
- `GetPlayerId()`: returns hardcoded "76561198950613585" — TODO: get from UPBUserObject when initialized earlier
- `ToFName()`: `UKismetStringLibrary::Conv_StringToName(std::wstring)`
- `HttpGet()`: synchronous WinHTTP GET helper
- **KNOWN BLOCKER**: `OnEquipCharacterSlotDelegate::Broadcast()` is `TMulticastInlineDelegate` — pure C++ template, does NOT route through ProcessEvent. Cannot intercept. This is why model refresh after equip doesn't work. Delegate broadcasts ErrorCode=4 BEFORE our BP callbacks fire.

### PvE Camera Flow Details
1. GameState enters `MatchIntro` (EPBMatchPhase=2)
2. `APBGameState::K2_StartMatchIntro()` fires → BP override
3. `APBPlayerController::K2_StartMatchIntro()` fires → BP override
4. Sequence LevelScriptActor `CheckForStateChange()` detects MatchIntro → `GetSequencePlayer()` on MatchStart LevelSequenceActor
5. LevelSequence plays multi-CineCameraActor flythrough (4+ cameras per map)
6. Camera transitions via `SetViewTargetWithBlend` from `PlayerController_BP`
7. After MatchIntroTime expires → state advances → `K2_StartMatchIntro` stops → sequence stops
8. `BP_NotifyStopThreePersonCamera()` calls `SetViewTargetWithBlend(GetPBCharacter, 0.5, ...)`
9. BUT on DS: `GetPBCharacter` may return null or wrong character → camera detaches

### Why Old Fix Worked on Warehouse but Not Others
- RoundState transition: `InvalidState` → `RoleSelection` → `CountdownToStart` → `InProgress`
- Old fix: per-tick `Possess(Pawn)` ONLY during `CountdownToStart`
- Warehouse: sequence ends BEFORE CountdownToStart → Possess works, camera attaches
- Other maps: sequence STILL PLAYING during CountdownToStart → Possess overridden by sequence camera
- Our fix: poll sequence Status directly (0x02B0), wait for Playing→Stopped, THEN trigger LateJoin spawn

### Toolbox Rust Module Organization
```
src/
  main.rs — entry, slint::include_modules!()
  core.rs — constants, structs, APP_VERSION, PROJECT_REBOUND_*, MANAGED_ITEMS, LaunchFiles
  core/
    font.rs — UI font detection/download/install
    payload.rs — zip extraction, online file management, download helpers
    install_ops.rs — full install flow with progress reporting
    process.rs — launch_files(), runtime process collection
    runtime_ops.rs — launch_pve(), launch_pvp(), launch_login_server()
    util.rs — hidden_command(), ensure_dir(), taskkill helpers
    github_proxy.rs — proxy list fetch, speed test, selection
    filesystem.rs — copy/delete helpers
    cleanup.rs — engine.ini cleanup, legacy mod removal
  app/
    mod.rs — AppController struct, AppMessage enum
    controller.rs — initialization, page switching, port refresh
    actions.rs — start_install(), launch, uninstall
    updates.rs — start_update_check()
    update.rs — check_latest_release(), fetch_project_rebound_tag(), dialog text
    messages.rs — drain_messages(), dialog handlers, i18n strings
    dialogs.rs — dialog action handling, path input
    proxy_list.rs — GitHub proxy list model
    target.rs — path mode switching (Auto/Manual)
    font.rs — font progress reporting
    prefs.rs — AppPrefs load/save
    window.rs — adaptive window sizing
```

### Toolbox Key Function Signatures
```rust
pub(crate) fn check_latest_release(target_win64: Option<&Path>) -> Result<UpdateCheckResult>
fn fetch_project_rebound_tag(client: &Client) -> Result<(String, Option<String>, Option<String>)>
pub(crate) fn extract_zip_to_dir(zip_bytes: &[u8], target: &Path) -> Result<()>
fn launch_pve(&self, target_win64: &Path) -> Result<String>
pub(crate) fn launch_files(target_win64: &Path) -> LaunchFiles
pub(crate) fn proxied_github_url(proxy_prefix: &str, url: &str) -> String
fn hidden_command(program: impl AsRef<OsStr>) -> Command  // CREATE_NO_WINDOW + null stdio
```

### Version Check Data Flow
```
Toolbox startup
  → check_latest_release(target_win64)
  → Client: GET {proxy}api.github.com/repos/LinchenFur/rust-boundary-tool-box/releases
  → Client: GET {proxy}api.github.com/repos/STanJK/ProjectRebound/releases/latest
  → Read target_win64/project_rebound_version.txt (local installed version)
  → Compute pr_is_newer: is_version_newer(latest_tag, local_tag) OR local_tag is None
  → UpdateCheckResult { toolbox fields, pr fields, pr_is_newer }
  → UpdateCheckFinished handler
  → Dialog: "工具箱版本: x.x.x", "社区服版本: y.y.y / (未安装)"
  → Button: if pr_is_newer → "安装/更新" → start_install()
```

### Wrapper Key Flow Details
```cpp
InitWrapperCore():
  ResetHeartbeatClock() → LoadCommandLineConfig() → ResetHeartbeatClock()
  → InitServerUniqueId() → create logs/ dir → open log file
  → LauncherLog startup messages → LoadConfigFile() or SaveConfigFile() defaults
  → InitCommands()

StopServerLocked():
  if !g_ServerProcess → clean state, return
  g_ServerState = Stopping → g_ServerGeneration++ → TerminateProcess(process, 0)
  → WaitForSingleObject(process, 1000) → if timeout → system("taskkill /F /T /PID ...")
  → CloseHandle → reset state

StartWatchdog(process, generation):
  loop: check shutdown/generation/state/exitcode every 1s
  → 20min serverlist check via WinHTTP GET /servers
  → heartbeat timeout 30s → RequestRestart
  → also checks generation mismatch for restart
    
InitServerUniqueId():
  try read serverId.json → if not found, generate 8-char hex
  → also read from serverconfig.json if present
  → save to serverconfig.json via SaveConfigFile()
```

### Config Propagation Chain
```
wrapper.cpp: InitServerUniqueId() → ServerUniqueId (global)
  ↓ SaveConfigFile() → serverconfig.json → LoadConfigFile()
  ↓ LaunchServerLocked() → -serverid={ServerUniqueId} → DLL cmdline
  ↓
DLL: Config.cpp LoadConfig() → Config.ServerUniqueId
  ↓ Network.cpp BuildServerStatusPayload() → {"serverId": Config.ServerUniqueId}
  ↓ POST /server/status → Backend index.js: servers[name].serverId
  ↓ GET /servers → wrapper watchdog checks ServerUniqueId in response
```

### Backend (BoundaryMatchBackend) Full API
```
POST /server/status  → stores {name, region, mode, map, port, playerCount, serverState, serverId, ip, lastHeartbeat}
GET  /servers        → returns JSON array of all active servers
GET  /               → HTML status page with auto-refresh
Cleanup: every 5s, removes servers with lastHeartbeat > 15s old
```

### Metaserver (TestBuild/index.js) Port Details
```javascript
// HTTP API
app.listen(process.env.PORT || 8000)  → port 8000

// TCP RPC (protobuf)
server.listen(6969)  → WAS 6968, FIXED TO 6969

// UDP QoS (matchmaking ping/pong)
matchmakingUDPServer.bind(9000)  → port 9000

// TCP Matchmaking
matchmakingTCPServer.listen(9000)  → port 9000

// /connectServer response
{ error: 0, userId: playerId, aceId: "test", gateToken: "...", endpoint: "127.0.0.1:6969" }
// NOTE: no displayName field → this is why player names show as SteamID64
```

### PostLogin Hook Deep Flow
```
1. PostLoginHook.call(original) — game's native PostLogin
2. UserNameFix_OnPostLogin(GameMode, PC):
   - if !PC || !PlayerState || !GameMode → return
   - GetDefaultIDStr() → steamIdStr
   - GetPlayerName() → currentName
   - if !LooksLikeSteamId64(steamIdStr) → skip
   - spawn thread: ResolveSteamName(steamIdStr) → push to g_Pending queue
3. LateJoinManager::OnPostLogin:
   - if IsLateJoinWindowOpen() → QueueLateJoinPlayer(PC)
   - if late join → return true (skip normal flow)
4. if !lateJoin && PC->Pawn → PC->ServerSuicide(0) (force first spawn)
```

### TickFlushHook Deep Flow
```
1. NoteServerGameTick() — updates gLastServerGameTickMs
2. UserNameFix_DrainPending() — processes g_Pending queue on game thread
3. PVECamFix_Tick(NetDriver, DeltaTime) — LevelSequence polling
4. if IsServerShutdownRequested() → return TickFlush.call(original) (early exit)
5. if listening && NetDriver && World:
   - NetDriverAccess::Observe
   - SelectRoleForQueuedPlayers (timer-based)
   - CollectTickReplicationBatch → CallFromTickFlushHook → increment counter
   - gLateJoinManager->Tick(DeltaTime)
6. if !IsRoundInProgress():
   - terminal round detection → HandleServerMatchEndSignal → return early
   - InvalidState → player count / match start countdown logic
7. if canStartMatch && !DidProcStartMatch → StartMatch() → HandleServerMatchStarted()
```

### Memory Offsets Used in Fixes (Complete)
| Offset | Struct/Class | Field | Used By |
|--------|-------------|-------|---------|
| 0x0300 | APlayerState | PlayerNamePrivate (FString) | UserNameFix |
| 0x0250 | ALevelSequenceActor | SequencePlayer (ULevelSequencePlayer*) | PVECamFix |
| 0x02B0 | UMovieSceneSequencePlayer | Status (uint8) | PVECamFix |
| 0x2810 | APBPlayerCameraManager | CameraModifiers_CacheRelativeLocation | UIFix |
| 0x281C | APBPlayerCameraManager | CameraModifiers_CacheRelativateRotation | UIFix |
| 0x03D8 | APBPlayerState | PlatformUniqueIDJsonString | UserNameFix (attempted) |
| 0x05B0 | APBPlayerController | PBCharacter (APBCharacter*) | PvE fix idea |
| 0x02B8 | APlayerController | PlayerCameraManager | Camera diagnostics |
| 0x02C1 | APBLauncher | CurrentState (EPBLauncherState) | LauncherFix |
| 0x02D4 | APBLauncher | PendingState (EPBLauncherState) | LauncherFix |
| 0x03B8 | APBLauncher | BurstCounter | LauncherFix |
| 0x03BC | APBLauncher | bIsFiring/bPendingFiring | LauncherFix |
| 0x0468 | APBLauncher | Magazine (FPBMagazine) | LauncherFix |
| 0x0278 | APBLauncher | FireComponent | LauncherFix |
| 0x0620 | APBProjectile | bExploded | LauncherFix |
| 0x0250 | APBProjectile | MovementComp | LauncherFix |
| 0x0258 | APBProjectile | CollisionComp | LauncherFix |
| 0x0260 | APBProjectile | ParticleComp | LauncherFix |
| 0x0658 | APBLauncher_Deploy_BP_C | ProjectilePathTracer | LauncherFix |
| 0x0660 | APBLauncher_Deploy_BP_C | FireRocoil | LauncherFix |
| 0x0588 | APBPlayerController | PBAimingComponent | Recoil investigation |
| 0x1F50 | APBCharacter | CurrentLeftLauncher | Launcher management |
| 0x1F58 | APBCharacter | CurrentRightLauncher | Launcher management |

### EPBLauncherState Enum
```
0 = Standby    — fire complete, flags cleared
1 = Deploying   — launcher deploying (swapping from idle to ready)
2 = Undeploying — launcher undeploying (swapping back from ready to idle)
3 = Ready       — launcher ready to fire, clean state
4 = Reloading   — reloading ammo
5 = Handup      — launcher stowed/handed up
```

### EMovieScenePlayerStatus Enum
```
0 = Stopped
1 = Playing
2 = Scrubbing
3 = Jumping
4 = Stepping
5 = Paused
6 = MAX
```

### EPBMatchPhase Enum
```
0 = EnteringMap
1 = WaitingToJoin
2 = MatchIntro         ← PvE cinematic plays during this phase
3 = WaitingToStart_Round
4 = RoleSelection_Round
5 = CountdownToStart_Round
6 = InProgress_Round
7 = WaitingPostRound_Round
...
```

### Current RoundState String Values (Observed in Logs)
- `InvalidState` — no round active (waiting for players, pre-match)
- `RoleSelection` — players selecting roles
- `CountdownToStart` — countdown before round begins
- `InProgress` — round is active
- `ShowingMatchResult` — match results screen
- `MatchEnding` — match ending transition
- `WaitingToEndGame` — waiting for game to end

### WinHTTP Usage Pattern (from Network.cpp and UserNameFix.cpp)
```cpp
// Session per request (NO pooling — prevents 5h heap corruption)
HINTERNET hSession = WinHttpOpen(L"BoundaryDLL/1.0", ...);
WinHttpSetTimeouts(hSession, 3000, 3000, 3000, 3000);
HINTERNET hConnect = WinHttpConnect(hSession, host, port, 0);
HINTERNET hRequest = WinHttpOpenRequest(hConnect, L"POST", path, ...);
WinHttpSendRequest(hRequest, headers, -1, body, bodyLen, bodyLen, 0);
WinHttpReceiveResponse(hRequest, NULL);
// Read loop...
WinHttpCloseHandle(hRequest);
WinHttpCloseHandle(hConnect);
WinHttpCloseHandle(hSession);
```
Key: All handles closed in reverse order. 3s timeout per phase. Per-request session (critical fix for hang issue).

### Server Hang Investigation Timeline (from ServerHangDoc)
1. **Round 1**: ntdll kernel lock spin — ExitProcess kills heartbeat thread in WinHttpOpen DNS → lock never released
2. **Round 2**: GObjects linked list dead loop — atexit handler traverses semi-destroyed GObjects chain
3. **Round 3**: std::map (xtree) red-black rotation dead loop — WinHTTP session pooling causes heap corruption → 3600+ Connect/Close on single session → std::map (nlohmann::json or CRT locale) corrupted → infinite tree rotation
4. **Round 4**: ntdll heap double fault — corrupted std::map triggers heap walker self-check → NtRaiseException → secondary access violation in exception handler

### x64dbg Hang Signatures
```
cmp [r12], [r15+8] + call ntdll.xxxWait  → kernel lock spin: thread killed mid-call
mov rax, [rax+10] + cmp [rax+19], 0      → GObjects linked list: atexit accessing destroyed objects
mov rax, [rdx+8] + cmp [rax+18], 0 + jmp back → std::map tree rotation: heap corruption
NtRaiseException + mov [rax-20], r10          → double fault: heap walker accessing bad pointer
```

### Toolbox Build Requirements
- Rust toolchain: `stable-x86_64-pc-windows-msvc`
- Cargo.toml dependencies: slint, reqwest, serde, serde_json, zip, walkdir, image, windows, winreg, sha2, anyhow, chrono, uuid, sysinfo, regex, netstat2, encoding_rs, tokio, vnt_ipc
- build-dependencies: slint-build, embed-resource, image (for ico generation)
- `BOUNDARY_PAYLOAD_ROOT` env var must be set for full build
- `SLINT_INCLUDE_GENERATED` auto-set by slint-build via build.rs
- `PAYLOAD_ZIP_BYTES` compiled via `include_bytes!(concat!(env!("OUT_DIR"), "/payload.zip"))`
- `APP_VERSION` constant in core.rs (currently "19.20.2")

### Release.zip Structure (for GitHub Release)
```
Release.zip
├── Payload.dll           ← from Payload project Release build
├── ServerLauncher/
│   ├── ProjectReboundServerLauncher.exe   ← from ServerLauncherGUI build
│   └── slint_cpp.dll                      ← Slint runtime (bundled with launcher)
├── BoundaryMetaServer-main/               ← from TestBuild directory
│   ├── index.js          ← metaserver main (with 6969 TCP fix)
│   ├── package.json
│   ├── package-lock.json
│   ├── game/
│   │   ├── loadoutStore.js
│   │   ├── definitionIndex.js
│   │   └── definitions/  ← DT_* item/weapon/character definitions
│   ├── data/loadouts/    ← player equipment data
│   └── node_modules/     ← npm dependencies
└── project_rebound_version.txt  ← contains tag (e.g. "V0.8.0")
```

### Things We Intentionally Removed
1. `ForceServerSuicideForAllPlayers()` + F8 hotkey — early experimental fix, superseded
2. `DebugLauncherLogs` compile-time gate → `ServerDebugLog()` runtime gate
3. `-debuglog` flag → `-serverdebuglog` / `-clientdebuglog`
4. Old `Log()` → renamed to `ServerLog()` (always-on)
5. Old `ClientLog()` (gated) → now always-on; diagnostic client logs → `ClientDebugLog()` (gated)
6. Per-file validation in Release.zip install → full zip extraction
7. 3-second wait loop in killserver → 1s TerminateProcess + taskkill escalation
8. `NO TICKY` tick-skip hack (was 50% tick rate for unknown debug purposes)
9. SteamNameResolver files → replaced by UserNameFix

### Things Deferred / Known Broken
1. First-life PvE launchers (grappling hook, mobility) — need respawn flow investigation
2. Scoreboard ID row (/765611) — GetDefaultIDStr native interception needed
3. Motion sensor model occlusion during fire
4. Motion sensor projectile direction corruption
5. Snapshot/motion sensor second shot muzzle flash
6. Smoke grenade burst mode
7. Grappling hook state machine (same IsLocallyControlled pattern)
8. Weapon residue on pickup (channel close in LibReplicate)
9. Toolbox: separate PR-only update (no Node re-download)
10. Toolbox: launch progress bar with port verification
11. Toolbox: close guard with all-process detection
12. Wrapper: EnsureLogOpen file_size throttle
13. Wrapper: pipe+file management thread
14. Timestamp from wrapper → DLL LogManager
15. BoundaryMetaServer online download → embedded in payload
16. Node.js online download → embedded in payload
17. Font online download → embedded in payload
18. Double dialog popup on update check

---

## Build & Deploy Commands

### Payload DLL Build
```powershell
"C:/Program Files/Microsoft Visual Studio/18/Community/MSBuild/Current/Bin/amd64/MSBuild.exe" "c:/STanJK/Development/Boundary/ProjectRebound/Payload/Payload.vcxproj" -p:Configuration=Release -p:Platform=x64 -v:minimal
```
Output: `Payload/x64/Release/Payload.dll`

### Deploy DLL to Game
```powershell
cp "c:/STanJK/Development/Boundary/ProjectRebound/Payload/x64/Release/Payload.dll" "D:/SteamLibrary/steamapps/common/Boundary/ProjectBoundary/Binaries/Win64/Payload.dll"
```
Must close game first (DLL is loaded, file locked).

### Toolbox Build
```powershell
cd C:\STanJK\Development\Boundary\rust-boundary-tool-box
$env:BOUNDARY_PAYLOAD_ROOT = "C:\STanJK\Development\Boundary\rust-boundary-tool-box\payload"
cargo build --release
```
Output: `target/release/boundary_toolbox.exe`

### Git Branches
- `rust-boundary-tool-box`: colleague's repo, main branch, we work directly as collaborator
- `ProjectRebound`: our repo, main branch. Release tag V0.8.0 for distribution

### Project Directory Map
```
c:\STanJK\Development\Boundary\
  ProjectRebound\                    ← main project
    Payload\                         ← DLL source (C++, MSBuild)
    ServerLauncherGUI\               ← Wrapper/Launcher source (C++, CMake)
    Backend\                         ← ASP.NET match backend
    BoundaryMetaServer\              ← Node.js metaserver
      TestBuild\                     ← working metaserver version
    docs\                            ← all documentation
  rust-boundary-tool-box\            ← Rust toolbox (colleague's repo)
    src\core\                        ← core logic
    src\app\                         ← app controller/UI logic
    ui\                              ← Slint UI files
    payload\                         ← embedded payload files
```

### Wrapper Log Files
- Location: `D:/SteamLibrary/.../Binaries/Win64/Launcher/logs/log-*.txt`
- Each "SERVER] Server is now listening" marks a new session
- Rotated at 1MB with _N suffix

### Client Log Files
- Location: `D:/SteamLibrary/.../Binaries/Win64/clientlogs/clientlog-*.txt`
- Enabled via `-clientdebuglog` flag
- Written by `ClientDebugLog()` through Debug.cpp

### Toolbox Logs
- Installation state: `Win64/installer_tool/state.json`
- User preferences: `Win64/installer_tool/app_config.json`
- Session log: `Win64/installer_tool/logs/*.log`

---

## File-by-File Change Reference (Complete)

### Payload/Config/Config.h
- Added `std::string ServerUniqueId` to ServerConfig
- `extern bool ServerDebugLogEnabled` moved to Debug.h

### Payload/Config/Config.cpp
- `LoadConfig()`: added `-serverid=` parsing → `Config.ServerUniqueId`
- `LoadClientConfig()`: added `-serverdebuglog` / `-clientdebuglog` flag parsing
- `std::cout << "[SERVER] Online backend"` → `ServerLog()`

### Payload/Debug/Debug.h
- Added: `extern bool ServerDebugLogEnabled`
- Added: `void ServerDebugLog(const std::string &msg)`
- Added: `void ClientDebugLog(const std::string &msg)`
- Renamed: `void Log(...)` → `void ServerLog(...)`

### Payload/Debug/Debug.cpp
- Complete rewrite to LogManager architecture
- `ServerLog()`: always-on, immediate flush via `PushEntry(msg, true)`
- `ServerDebugLog()`: gated, batch flush via `PushEntry(msg, false)`
- `ClientLog()`: always-on client, immediate flush
- `ClientDebugLog()`: gated client, batch flush + file write
- `ServerDebugLogEnabled` / `ClientDebugLogEnabled` globals (default: false)
- `InitDebugConsole()`, `EnableUnrealConsole()`, `HotkeyThread()`, `DebugLocateSubsystems()`, `DebugDumpSubsystemsToFile()`, `DebugDumpWeaponPartsToFile()` — preserved intact
- `ClientLog()` calls inside HotkeyThread → `ClientDebugLog()` (diagnostic, not essential)

### Payload/ServerLogic/ServerLogic.cpp
- `ExitProcess(0)` → `TerminateProcess(GetCurrentProcess(), 0)` in `DelayedExitAfterMatchEnd`
- Added `OutputDebugStringA` exit logging
- `std::cout << "[SERVER_LIFECYCLE]"` → `Log()` / `ServerLog()`
- All `Log()` → `ServerLog()`

### Payload/ServerLogic/ServerLogic.h
- Declares: `IsServerShutdownRequested()`, `IsTerminalRoundState()`, `HandleServerMatchEndSignal()`, `HandleServerMatchStarted()`

### Payload/ServerLogic/LateJoinManager.h
- Added: `void ForceFirstLifeSpawn(SDK::APBPlayerController* PC)`

### Payload/ServerLogic/LateJoinManager.cpp
- `ForceFirstLifeSpawn()`: `QueueLateJoinPlayer(PC)` + set `State = RoleConfirmed`, `ElapsedSeconds = 0`
- All `std::cout << "[LATEJOIN]"` → `ServerDebugLog()` (13 lines)
- Added `#include "../Debug/Debug.h"`, removed `<iostream>`

### Payload/Network/Network.cpp
- `SendJsonPost`: added `IsServerShutdownRequested()` early return with `OutputDebugStringA`
- `BuildServerStatusPayload()`: added `{"serverId", Config.ServerUniqueId}` to JSON
- WinHTTP session per request (Open/Close per call — no pooling)
- Removed `g_hHttpSession` and `g_HttpMutex` (no longer needed)
- All `std::cout` heartbeat/online → kept as-is (essential)

### Payload/Network/Network.h
- Removed `ShutdownHttpSession()` declaration (dead code)

### Payload/Network/NetDriverAccess.cpp
- `ScanForNetDriver()`: added `IsServerShutdownRequested()` guard with `OutputDebugStringA`
- Added `#include "../ServerLogic/ServerLogic.h"`

### Payload/Utility/Utility.cpp
- `getObjectsOfClass()`: added shutdown guard + `OutputDebugStringA`
- `GetLastOfType()`: added shutdown guard + `OutputDebugStringA`
- Added `#include "../ServerLogic/ServerLogic.h"` and `<iostream>`

### Payload/Utility/UserNameFix.h (NEW)
- Declares: `UserNameFix_OnPostLogin(AGameMode*, APBPlayerController*)`
- Declares: `UserNameFix_DrainPending()`

### Payload/Utility/UserNameFix.cpp (NEW)
- Steam Web API: `steamcommunity.com/profiles/{steamid64}/?xml=1`
- In-place FString write at 0x0300 via `InPlaceFStringWrite()`
- Pending queue: `std::vector<PendingNameChange>` with `std::mutex`
- PostLogin: async thread spawn → `ResolveSteamName()` → push to queue
- Drain: called from TickFlushHook, constructs FString on game thread, writes directly to PlayerNamePrivate
- Cache: `std::unordered_map<std::string, std::string>` for resolved names

### Payload/Utility/PVECamFix.h (NEW)
- Declares: `PVECamFix_Tick(SDK::UNetDriver*, float DeltaTime)`

### Payload/Utility/PVECamFix.cpp (NEW)
- `FindPlayingSequence()`: GObjects scan for ALevelSequenceActor with Playing Status
- `IsSeqPlaying()`: memory offset read at 0x0250 + 0x02B0
- `FixAllPlayers()`: calls `gLateJoinManager->ForceFirstLifeSpawn(PBPC)`
- State machine: Phase 1 (find) → Phase 2 (poll until stop) → Phase 3 (fix and reset)

### Payload/Utility/LauncherFix.h (NEW)
- Declares: `HandleLauncherServerEvent()`, `HandleLauncherClientEvent()`, `HandleProjectileClientEvent()`

### Payload/Utility/LauncherFix.cpp (NEW)
- Server: `ServerFiring` + `K2_Standby` handlers with logging (now `ServerDebugLog`)
- Client: `OnRep_PendingState` state machine fix (forces CurrentState, clears flags, calls K2_ functions)
- Client: `ServerFiring` dud blocking (AmmoInClip==0)
- Client: `OnRep_Exploded` → `MulticastExplode` force-call
- Removed compile-time `DebugLauncherLogs` gate → runtime `ServerDebugLog`/`ClientDebugLog`

### Payload/Utility/UIFix.h (NEW)
- Declares: `HandleUICharacterClientEvent()`

### Payload/Utility/UIFix.cpp (NEW)
- CamCache zeroing when idle (bIsRunning==0, CharStatus Idle/SlowlyMoving)
- Synthetic sine wave shake when sprinting (bIsRunning==1)
- Debug logging: CamD and WpD delta values per frame

### Payload/Loadout/LoadoutFix.h (NEW)
- Declares: `HandleEquipErrorSwallow()`, `LoadoutFix_FlushRefresh()`, `LoadoutFix_FetchAndLog()`

### Payload/Loadout/LoadoutFix.cpp (NEW)
- `HandleEquipErrorSwallow`: zeroes EPBEquipErrorCode param on OnEquipCharacterSlotComplete and K2_OnEquipComplete
- `LoadoutFix_FetchAndLog`: HTTP GET from metaserver, JSON parse, log each role's equipment
- `LoadoutFix_FlushRefresh`: main-thread spawn inventory refresh
- `GetPlayerId`: hardcoded SteamID64 (TODO)
- `HttpGet`: WinHTTP synchronous GET helper

### Payload/Hooks/Hooks.cpp
- PostLogin: added `UserNameFix_OnPostLogin(GameMode, PC)` after original call
- TickFlush: added `UserNameFix_DrainPending()` + `PVECamFix_Tick(NetDriver, DeltaTime)` at top
- TickFlush: terminal round detection → `return TickFlush.call(NetDriver, DeltaTime)`
- Server ProcessEvent: added `HandleLauncherServerEvent()` before `ProcessEvent.call()`
- Client ProcessEvent: added `HandleLauncherClientEvent()`, `HandleProjectileClientEvent()`, `HandleUICharacterClientEvent()`, `HandleEquipErrorSwallow()` before call; `LoadoutFix_FlushRefresh()` after call
- Client ProcessEvent: `MainMenuConstruct` → `LoadoutFix_FetchAndLog()` (with `LoginCompleted` guard)
- Removed `ForceServerSuicideForAllPlayers()` and F8 hotkey
- Removed `NO TICKY` tick-flip hack
- Added includes: UserNameFix, PVECamFix, LauncherFix, UIFix, LoadoutFix
- Removed unused includes: `<mutex>`, `<vector>`

### Payload/dllmain.cpp
- Added `#include "Utility/UserNameFix.h"`
- Server startup: `InitServerHooks()` → `ServerLog()` for each milestone
- Client path: `ClientLog`/`ClientDebugLog` calls preserved
- Error handler: `std::cout` → `ServerLog()`

### Payload/Payload.vcxproj
- Added ClCompile entries: UserNameFix.cpp, PVECamFix.cpp, LauncherFix.cpp, UIFix.cpp, LoadoutFix.cpp
- Paths: UserNameFix and PVECamFix in Utility\, LoadoutFix in Loadout\

---

## Common Pitfalls & Solutions Reference

### Pitfall 1: FString assignment crosses CRT boundary
**Symptom**: Name becomes garbage like "STanNam" or "UserName"
**Root cause**: SDK `FString::operator=` calls CRT `delete[]` on engine-allocated Data pointer
**Fix**: Write directly to FString buffer via memory offsets. Read `TCHAR*` / `Count` / `Max`, use `wcscpy_s`, update Count.

### Pitfall 2: FString Count includes null terminator
**Symptom**: Name truncated by 1 character (e.g. "STanJ" instead of "STanJK")
**Root cause**: Set `count = wcslen(text)` (6) but UE4 FString Count includes null → 7
**Fix**: `count = needed + 1`

### Pitfall 3: WinHTTP session pooling causes heap corruption
**Symptom**: Server hangs after 5+ hours, std::map dead loop in x64dbg
**Root cause**: 3600+ WinHttpConnect on single WinHttpOpen session → internal connection pool / TLS state ballooning → heap corruption
**Fix**: WinHttpOpen/WinHttpCloseHandle per request (no session reuse)

### Pitfall 4: ExitProcess triggers atexit deadlock
**Symptom**: Process hangs on exit, GObjects linked list loop in x64dbg
**Root cause**: ExitProcess → DLL_PROCESS_DETACH → CRT atexit handlers → SDK atexit traverses semi-destroyed GObjects
**Fix**: Use `TerminateProcess(GetCurrentProcess(), 0)` — hard kill, no cleanup

### Pitfall 5: `std::endl` blocks on full pipe
**Symptom**: Wrapper freezes, killserver hangs
**Root cause**: `std::endl` flushes → pipe buffer full → game thread blocks on write
**Fix**: Use `\n` without flush, batch flush every N lines in LogManager

### Pitfall 6: ProcessEvent inside ProcessEvent hook
**Symptom**: Stack overflow or crash
**Root cause**: Calling SDK function (which calls ProcessEvent) from inside ProcessEvent hook
**Fix**: Use direct memory offset reads/writes instead of SDK wrappers

### Pitfall 7: `replace_all` substring collision
**Symptom**: Function names corrupted (ClientDebugServerLog, LoadoutFix_FetchAndServerLog)
**Root cause**: Short `old_string` like `Log(` matches inside longer function names
**Fix**: Use exact `old_string` with surrounding context: `Log("[SERVER] Hooks installed.")` not just `Log(`

### Pitfall 8: Write() drops unmentioned functions
**Symptom**: Linker errors for InitDebugConsole, HotkeyThread, etc.
**Root cause**: Write() replaces entire file content; functions not in the new content are lost
**Fix**: Use Edit() to append/modify, never Write() to "rewrite"

### Pitfall 9: SDK namespace shadow
**Symptom**: "undefined type APBPlayerController" even with SDK.hpp included
**Root cause**: `class APBPlayerController;` in global namespace ≠ `SDK::APBPlayerController`
**Fix**: Use `namespace SDK { class APBPlayerController; }` or include SDK.hpp in header

### Pitfall 10: CWD-dependent relative paths
**Symptom**: GameExePath works from PS but not from toolbox launch
**Root cause**: CWD differs: PS uses ServerLauncher\, toolbox uses Win64\ (or vice versa)
**Fix**: Align CWD with GameExePath. Toolbox sets CWD to ServerLauncher\, GameExePath = "..\\ProjectBoundarySteam-Win64-Shipping.exe"

---

## Quick Diagnostic Commands

### Verify all Hang Fixes
```bash
grep -n "TerminateProcess\|EXIT-GUARD\|taskkill /F\|IsServerShutdownRequested" Payload/ServerLogic/ServerLogic.cpp Payload/Network/Network.cpp Payload/Network/NetDriverAccess.cpp Payload/Utility/Utility.cpp Payload/Hooks/Hooks.cpp
```

### Verify Log System
```bash
grep -rn "ServerLog\|ServerDebugLog\|ClientLog\|ClientDebugLog" Payload/ --include="*.cpp" --include="*.h" | grep -v "SDK\|safetyhook\|Libs\|\.git"
```

### Verify Toolbox Changes
```bash
grep -n "launcher_exe\|ServerLauncher\|PROJECT_REBOUND_ONLINE_FILES\|project_rebound\|pr_is_newer\|extract_zip_to_dir" rust-boundary-tool-box/src/core.rs rust-boundary-tool-box/src/core/runtime_ops.rs rust-boundary-tool-box/src/app/update.rs
```

### Find Remaining std::cout
```bash
grep -rn "std::cout" Payload/ --include="*.cpp" | grep -v "SDK\|safetyhook\|Libs\|Debug.cpp.*WorkerLoop\|Debug.cpp.*std::cout.clear"
```

---

## Edit Tool Operations Safety Rules (Memory-Backed)
1. ONLY Edit tool for code changes
2. For renames, use exact old_string with context, NOT short substring replace_all
3. NEVER Write (full file overwrite) without explicit permission
4. NEVER Bash sed/cat/mv on source files without explicit permission
5. NEVER git checkout/revert without explicit permission
6. Each Edit is one atomic change — propose batch operations first
7. If unsure about a change affecting other files, grep first

## Session Collaboration Rules
1. Read `AI_Code_Reference.md` at session start
2. Read this `Session_Compact` for recent context
3. Check `memory/MEMORY.md` for standing rules
4. Before any code change, verify current git status
5. After any code change, verify build compiles
6. Document all new technical discoveries in this file
