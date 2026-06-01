# Log System Architecture

## Overview

Four-function architecture backed by a single background thread (LogManager).
All log output goes through the queue → worker thread → batch-flushed stdout → wrapper pipe.

```
ServerLog()      ServerDebugLog()     ClientLog()      ClientDebugLog()
    │                 │                   │                   │
    │ always-on       │ -serverdebuglog   │ always-on         │ -clientdebuglog
    │ immediate flush │ batch flush(30)   │ immediate flush   │ batch flush(30)
    └─────────┬───────┴───────────────────┴─────────┬─────────┘
              │           PushEntry(msg, immediate) │
              ▼                                     │
     ┌───────────────────────────────────────────────┘
     │  LogManager Queue (std::deque + mutex + condvar)
     │  Worker thread drains → writes to stdout
     │  Batch flush: immediate entries force flush; others every 30
     └──────────────┬───────────────────────────────
                    │ stdout
                    ▼
              wrapper pipe
```

## Functions

| Function | Gating | Flush | Purpose |
|----------|--------|-------|---------|
| `ServerLog()` | None | Immediate | Heartbeat, startup, lifecycle, ERROR |
| `ServerDebugLog()` | `-serverdebuglog` | Batch (30) | Diagnostic server-side logs |
| `ClientLog()` | None | Immediate | Client essential output |
| `ClientDebugLog()` | `-clientdebuglog` | Batch (30) | Diagnostic client-side logs + file |

## Files

| File | Role |
|------|------|
| `Payload/Debug/Debug.h` | Declares all 4 functions + flags |
| `Payload/Debug/Debug.cpp` | LogManager thread + queue + implementations |
| `Payload/Config/Config.cpp` | Parses `-serverdebuglog` / `-clientdebuglog` flags |

## Flag Behavior

- No flags → only `ServerLog()` and `ClientLog()` (essential) produce output
- `-serverdebuglog` → all diagnostic server logs activate
- `-clientdebuglog` → all diagnostic client logs activate + file output to `clientlogs/`

## ServerLog() Callers

| File | Count | Tags |
|------|:-----:|------|
| `ServerLogic.cpp` | 18 | `[SERVER]`, `[ERROR]`, `[SERVER_LIFECYCLE]` |
| `dllmain.cpp` | 7 | `[SERVER]`, `[ERROR]` |
| `Config.cpp` | 6 | `[SERVER]` |
| `Network.cpp` | ~7 | `[HEARTBEAT]`, `[HEALTH]`, `[ONLINE]` |
| `Hooks.cpp` | 1 | `[HOOK]` |

## ServerDebugLog() Callers

| File | Count | Tags |
|------|:-----:|------|
| `LateJoinManager.cpp` | 13 | `[LATEJOIN]` |
| `UserNameFix.cpp` | 5 | `[NAME-FIX]` |
| `Hooks.cpp` | 8 | `[POST-LOGIN]`, `Selecting role`, etc. |
| `LauncherFix.cpp` | 4 | `[POD]`, `[POD-FIRE]`, `[POD-STANDBY]` |
| `PVECamFix.cpp` | 1 | `[CAM-FIX]` |
| `ServerLogic.cpp` | 2 | `[SERVER_LIFECYCLE]` |

## ClientDebugLog() Callers

| File | Count | Tags |
|------|:-----:|------|
| `LoadoutFix.cpp` | 20 | `[LOADOUT-FIX]` |
| `LauncherFix.cpp` | ~12 | `[POD-CLIENT-*]`, `[PROJ-CLIENT-*]` |
| `Debug.cpp` | 5 | `[CLIENT]`, `[DEBUG]` |
| `ClientLogic.cpp` | 5 | `[CLIENT]` |
| `Config.cpp` | 2 | `[CLIENT]` |
| `Hooks.cpp` | 3 | `[LOGIN]`, `[PE]` |
| `dllmain.cpp` | 3 | `[PIPE]`, `[BOOT]`, `[CLIENT]` |
| `UIFix.cpp` | 2 | `[UI-FIX]` |

## Design Rules

1. **NEVER `std::cout` directly.** Always go through one of the 4 functions.
2. **NEVER `std::endl` in log functions.** The LogManager worker handles flushing.
3. **NEVER use Write/sed/Bash to modify source files.** Edit tool only, one change at a time.
4. **`CommandFramework` has its own internal `Log()`** — do NOT rename it. It routes through `onLog` callback.

## Wrapper Logging

Wrapper has its own independent log system (not connected to DLL's LogManager):
- `LauncherLog()` → writes to `logs/log-*.txt` + stdout + GUI callback
- `PipeReader` → reads server stdout pipe → writes to same log file
- `EnsureLogOpen()` → rotates log file at 1 MB
- `StartTimestampLogger()` → logs `[TIMESTAMP] yymmddhhmmss` every 30 s
