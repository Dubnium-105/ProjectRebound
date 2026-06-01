# ProjectRebound

Community dedicated-server project for Boundary. Injects a DLL into the last shipping game client exe to pull out the existing ListenServer Logic, to build a somehow working Dedicated Server.

On the client side, we use the developer reserved -LogicServerURL= parameter to redirect to our custom made LoginMetaServer

Also includes a ServerLauncher, which manages the server lifecycle.
Helped by a backend server to collect all the servers and display a Server List.

Based on [SyST3MDeV/ProjectRebound](https://github.com/SyST3MDeV/ProjectRebound) by **gwog** — massive thanks to the original author for making this possible.

Designed to work with [LinchenFur/rust-boundary-tool-box](https://github.com/LinchenFur/rust-boundary-tool-box).  Currently use the ToolBox for one-click launch; a standalone launch guide will be added later.

> **Note:** This project and it's documentation is still a work in progress, and the codebase is undergoing a refactor to improve readability. 
>If you love this game and want to help on the development, we welcome any and all contributors!
>
> Join us on Discord: [discord.gg/chaitea](https://discord.gg/chaitea)

## Root Directory

```
ProjectRebound/
├── ProjectReboundMainDLL/    ← Core: Injected DLL to provide all the server logic and related client logic
├── dxgi/                     ← Injection DLL
├── ServerLauncher/           ← Used to start and manage the Dedicated Server Exe
├── Metaserver/               ← Login server, using -LogicServerURL= on the game exe to redirect the game login requests
├── ServerListBackend/        ← Community Server ServerList backend
├── Docs/                     ← Project documentation, with both docs for Human to read, and docs for AI - for vibecoding
├── Deprecated/               ← Archived legacy code
└── README.md
```

---

## ProjectReboundMainDLL

Build output: `Payload.dll`, loaded via `dxgi.dll` proxy injection
into `ProjectBoundarySteam-Win64-Shipping.exe`.

### Module Map

**Core/** — DLL entry point and shared infrastructure
- `Entry.cpp/h` — `DllMain` and main thread bootstrap (server / client init orchestration)
- `GameOffsets.h` — Engine memory offset constants and global variable declarations

**Config/** — Command-line argument parsing
- `Config.cpp/h` — Parses `-server`, `-map`, `-port`, `-online`, `-pipe`, `-match`, etc.

**Logging/** — Log system
- `LogManager.cpp/h` — Four-function API (`ServerLog`, `ServerDebugLog`, `ClientLog`, `ClientDebugLog`) with background-thread batch flush

**Hooking/** — All hook infrastructure, split by responsibility
- `HookCore.cpp/h` — SafetyHookInline variables, inline-hook helpers, ProcessEvent classification cache, `InitServerHooks` / `InitClientHook` / `InitMessageBoxHook`
- `ServerLogicHooks.cpp/h` — Tick-driven server hooks: `TickFlush` (round state machine, replication batching, late-join driver), `PostLogin`
- `ServerEventHooks.cpp/h` — Event-driven server hooks: `ProcessEvent` dispatch (respawn, role selection, launcher), `Notify` hooks
- `ClientHooks.cpp/h` — Client-side `ProcessEvent` dispatch (login, launcher, projectile, armory), death crash fix
- `EnginePatchHooks.cpp/h` — Engine behaviour overrides: `IsDedicatedServer` / `IsServer` / `IsStandalone`, font popup suppression, `ObjectNeedsLoad` / `ActorNeedsLoad`

**Server/** — Server-side runtime logic
- `RoundManager.cpp/h` — Server lifecycle: map load, round detection, `StartMatch`, safe shutdown via `TerminateProcess`
- `LateJoin.cpp/h` — Mid-game join state machine: role selection, pawn spawning with 3-level fallback, client sync
- `Backend.cpp/h` — HTTP backend communication: heartbeat (`/server/status`), room lifecycle events (`/v1/rooms/...`)
- `Replication.cpp/h` — `LibReplicate`: actor channel management, NetDriver creation, `CallFromTickFlushHook`
- `NetDriverAccess.cpp/h` — NetDriver discovery / cache / snapshot; exported C ABI (`PR_GetActiveNetDriver` etc.)
- `PlayerNaming.cpp/h` — Steam name resolution: WinHTTP GET to `steamcommunity.com`, in-place `FString` write at offset 0x0300
- `PvECamera.cpp/h` — PvE intro sequence detection: polling `LevelSequence` status via memory offset to fix detached camera
- `SideMountFixServer.cpp/h` — Side-mounted pod server-side guard: empty-clip `ServerFiring` blocking, diagnostics

**Client/** — Client-side fixes
- `AutoConnect.cpp/h` — Auto-connect: armory init, match join, `travel` command
- `SideMountFixClient.cpp/h` — Side-mounted pod client fix: state machine, `OnRep_PendingState` handler, dud blocking, projectile explosion VFX
- `UIShake.cpp/h` — Sprint-shake fix: CamCache override, synthetic sine-wave shake waveform

**API/** — Engine utilities and external interfaces
- `APIInternal.cpp/h` — Engine utility wrappers: `GObjects` scan, `FString` read/write, key-press simulation
- `APIExposed.cpp/h` — External API surface (empty placeholder, reserved for future)
- `ExternalCommandPipe.cpp/h` — Named-pipe IPC: command channel between launcher and DLL (`ping`/`pong`/`join` protocol)

**Loadout/** — Armory loadout fix (unfinished, deferred)

**Libs/** — Third-party libraries: safetyhook (inline hook), json.hpp (nlohmann), Zydis (disassembler)

**SDK/** — Auto-generated UE4 SDK headers (`SDK.hpp`, `SDKHelper/`)

**Original/** — Pre-refactor source archive

Root files: `framework.h` (standard Windows DLL header), `Payload.vcxproj` (MSBuild project)