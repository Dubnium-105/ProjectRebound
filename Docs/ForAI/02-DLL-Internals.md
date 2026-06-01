# 02 — DLL Internals

> 来源合并：AI_Code_Reference.md（Feature Log）、LogSystemDoc.md、ServerHangDoc.md
> 最后更新：2026-05-15（日志重构完成）

## 初始化流程

```
DllMain (Entry.cpp)
  └─ CreateThread → MainThread()
       ├─ LoadConfig() — 解析命令行
       ├─ 判断服务端/客户端
       │   ├─ [服务端] InitServerHooks() → 安装 ProcessEvent + PostLogin + TickFlush hooks
       │   └─ [客户端] InitClientHook() → 安装客户端 ProcessEvent hook
       └─ 日志输出：ServerLog("[SERVER] Hooks installed.")
```

入口文件 `dllmain.cpp` 当前包含全局变量和 MainThread 逻辑，重构后将拆分为 `Core/Entry.cpp`（MainThread）和 `Core/Globals.cpp`（全局变量），另新建 `Core/Bootstrap.cpp` 编排初始化序列。

## Hook 体系

### 当前 Hooks.cpp（~900 行，重构中将拆分为 5 文件）

```
服务端 ProcessEvent Hook:
  QuickRespawn → PlayerRespawnAllowedMap[PC] = true
  ServerRestartPlayer → 检查 PlayerRespawnAllowedMap
  ClientBeKilled → PlayerRespawnAllowedMap[PC] = false
  PlayerCanRestart → return HasMatchStarted()
  ServerConfirmRoleSelection → LateJoinManager::OnRoleConfirmed
  SideMountFix → HandleLauncherServerEvent
  其他 → ProcessEvent.call(original)

客户端 ProcessEvent Hook:
  EnterGameConstruct / EnterGameActivated → PressSpace() after 1s
  MainMenuConstruct → LoadoutFix_FetchAndLog()
  ConnectMatchServerTimeout → ConnectToMatch()
  SideMountFix → HandleLauncherClientEvent + HandleProjectileClientEvent
  UIShake → HandleUICharacterClientEvent
  LoadoutFix → HandleEquipErrorSwallow
  After call → LoadoutFix_FlushRefresh()

PostLogin Hook:
  PostLoginHook.call(original)
    → UserNameFix_OnPostLogin(GameMode, PC)
    → LateJoinManager::OnPostLogin

TickFlush Hook:
  NoteServerGameTick() → UserNameFix_DrainPending() → PVECamFix_Tick(NetDriver, DeltaTime)
    → 复制批处理 → LateJoinManager::Tick(DeltaTime)
    → 回合状态检查 → 终端回合检测
```

### 重构后 Hooking/ 目录（5 文件）

| 文件 | 内容 |
|------|------|
| `HookInstall.cpp` | FInlineHookSpec 结构、InitServerHooks/InitClientHook/InitMessageBoxHook、Hook 生命周期 |
| `ServerDispatch.cpp` | ClassifyServerProcessEvent、ProcessEventHook 服务端派发 |
| `ClientDispatch.cpp` | ClassifyClientProcessEvent、ProcessEventHookClient 客户端派发 |
| `EnginePatches.cpp` | MessageBoxWHook、IsDedicatedServerHook、HudCrash 修复、GameEngineTick 等 |
| `GameHooks.cpp` | TickFlush hook、PostLogin hook、FTickReplicationBatch |

## 日志系统

### 四函数架构

| 函数 | 门控 | Flush | 用途 |
|------|------|-------|------|
| `ServerLog()` | 无 | Immediate | 心跳、启动、生命周期、ERROR |
| `ServerDebugLog()` | `-serverdebuglog` | Batch (30行) | 服务端诊断日志 |
| `ClientLog()` | 无 | Immediate | 客户端必要输出 |
| `ClientDebugLog()` | `-clientdebuglog` | Batch (30行) + 文件 | 客户端诊断日志 |

### LogManager 线程

```cpp
// 队列 + 工作线程
struct LogEntry { std::string msg; bool immediate; };
static std::deque<LogEntry> g_Queue;
static std::mutex g_QueueMutex;
static std::condition_variable g_QueueCv;

void WorkerLoop() {
    std::deque<LogEntry> local;  // swap 到本地以减少锁时间
    int sinceFlush = 0;
    while (true) {
        {
            std::unique_lock lock(g_QueueMutex);
            g_QueueCv.wait(lock, [] { return !g_Queue.empty() || !g_WorkerRunning; });
            if (g_Queue.empty() && !g_WorkerRunning) return;
            local.swap(g_Queue);  // O(1) swap
        }
        for (auto &entry : local) {
            std::cout << entry.msg << "\n";   // \n 不是 std::endl — 避免每行 flush
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

### 设计规则

1. **禁止直接 `std::cout`** — 全部走四函数
2. **禁止 `std::endl`** — LogManager 自己管理 flush。`std::endl` 在管道满时会使游戏线程阻塞
3. **禁止 Write/sed/Bash 修改源文件** — Edit tool only
4. **CommandFramework 有自己内部的 `Log()`** — 不要重命名它

## 服务端挂死修复

### 四轮调查

| 轮次 | 症状 | 根因 | 修复 |
|------|------|------|------|
| 1 | 20h 后 ntdll 内核锁自旋 | `ExitProcess` → `TerminateThread` 杀死了 WinHttpOpen DNS 解析中的心跳线程 | `ExitProcess` → `TerminateProcess(GetCurrentProcess(), 0)` |
| 2 | exit() 20min 后 GObjects 链表死循环 | `exit()` → CRT atexit → SDK atexit 遍历半销毁的 GObjects | 不再使用 exit() |
| 3 | 5h 后 xtree 红黑树旋转死循环 | WinHTTP session 池化：3600+ Connect 共用一个 session → 堆损坏 | WinHTTP session 按请求创建/销毁（不池化） |
| 4 | ntdll 堆损坏 + Access Violation | std::map 树损坏触发 heap walker 自检 → double fault | 综合修复 |

### x64dbg 诊断速查表

| 现场特征 | 原因 |
|----------|------|
| `cmp [r12], [r15+8]` + `call ntdll.xxxWait` 循环 | 内核锁自旋 |
| `mov rax, [rax+10]` + `cmp [rax+19], 0` 循环 | GObjects 链表遍历 |
| `mov rax, [rdx+8]` + `cmp [rax+18], 0` + `jmp back` | std::map 树旋转（堆损坏） |
| `NtRaiseException` + `mov [rax-20], r10` | 二次异常（heap walker） |

### 所有修复点

| 文件 | 修改 |
|------|------|
| `ServerLogic.cpp` | `ExitProcess(0)` → `TerminateProcess`；添加 `OutputDebugStringA` 退出日志 |
| `Network.cpp` | `SendJsonPost` 添加 `IsServerShutdownRequested()` 提前返回；WinHTTP 按请求创建 session |
| `Hooks.cpp` | TickFlush 终端回合检测后立即 `return` |
| `NetDriverAccess.cpp` | `ScanForNetDriver` 添加 shutdown 守卫 |
| `Utility.cpp` | `getObjectsOfClass`/`GetLastOfType` 添加 shutdown 守卫 |
| Wrapper | `StopServerLocked`: TerminateProcess → 1s 等 → taskkill /F /T 升级 |

### WinHTTP 使用模式

```cpp
// 按请求创建 session（禁止池化 — 防止堆损坏）
HINTERNET hSession = WinHttpOpen(L"BoundaryDLL/1.0", ...);
WinHttpSetTimeouts(hSession, 3000, 3000, 3000, 3000);
HINTERNET hConnect = WinHttpConnect(hSession, host, port, 0);
HINTERNET hRequest = WinHttpOpenRequest(hConnect, L"POST", path, ...);
WinHttpSendRequest(hRequest, ...);
WinHttpReceiveResponse(hRequest, NULL);
// ... 读取循环 ...
WinHttpCloseHandle(hRequest);
WinHttpCloseHandle(hConnect);
WinHttpCloseHandle(hSession);
```

### Wrapper 侧修复

| 修改 | 目的 |
|------|------|
| `StopServerLocked`: TerminateProcess → 等 1 秒 → taskkill /F /T | 快速杀正常进程，僵尸升级到内核级强杀 |
| Watchdog 每 20 分钟查 Backend `/servers` 列表 | 服务器不在列表（心跳中断 >15s）立即重启 |
| `InitServerUniqueId()` 生成持久化 8 位 hex ID | 跨重启唯一标识 |
| `LaunchServerLocked` 传递 `-serverid=` 给 DLL | DLL 心跳上报 serverId |

### 设计原则

**Wrapper 负责进程生命周期，DLL 不应处理自己的退出。**
- DLL 触发退出 → `TerminateProcess` 硬杀（不留 atexit / CRT 清理）
- 退出路径全部加 shutdown 守卫
- 僵尸进程：Wrapper 不等、不确认，直接关句柄起新进程

## 相关文档

- `01-System-Overview.md` — 系统全景
- `03-Game-Fixes.md` — 逐个修复记录
- `04-RE-Data.md` — SDK 偏移量全集
