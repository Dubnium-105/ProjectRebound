# Server Hang 调查与修复文档

## 问题

Payload.dll 注入游戏进程伪装 DS 后，长时间运行（~20 小时）进程卡死。Wrapper 看门狗（30s 心跳超时）理论上应自动重启，但实际失效，需手动任务管理器终止进程树。

---

## 调查过程

### 第一轮：ntdll 内核锁自旋（原始 20h 卡死）

**x64dbg 断点现场：**
```
loop:
    call ntdll.7FFEA13BE0FC     ← RtlpWaitOnAddress / 退让
    cmp [r12], [r15+8]          ← 等待另一个线程改内存值
    jne loop                    ← 没改就继续等
```

**根因**：`DelayedExitAfterMatchEnd` 调用 `ExitProcess(0)`。`ExitProcess` 强制终止所有线程（`TerminateThread`）。心跳线程在 `WinHttpOpen` 内核态 DNS 解析中被杀 → 锁永不释放 → 等待线程死循环。

**证据**：`ExitProcess` 会触发 `DLL_PROCESS_DETACH`，进而 CRT 调 `atexit` 回调。SDK 的 atexit handler 遍历 `GObjects` 链表 → 引擎退出时链表已半销毁 → 野指针 → 死循环。

### 第二轮：GObjects 链表遍历死循环（exit() 尝试后 20min 卡死）

**尝试修复**：`ExitProcess` → `ExecuteConsoleCommand("exit")`，让引擎走正常退出流。

**x64dbg 断点现场：**
```
    mov rax, [rax+10]           ← 链表指针跳转
    cmp byte [rax+19], 0        ← 检查节点标记
    je  loop                    ← 标记为 0 继续循环
```

**根因**：`exit()` → `ucrtbase._execute_onexit_table` → SDK atexit handler → 遍历 `GObjects` → 链表已被引擎先一步销毁 → 永远找不到出口。

### 第三轮：xtree（std::map）红黑树旋转死循环（5h 卡死）

**x64dbg 断点现场：**
```
payload.7FFA0393F061:
    mov rax, [rdx+8]            ← xtree 节点访问
    cmp byte [rax+18], 0        ← 红黑树颜色检查
    je   inner_loop             ← 树旋转/再平衡
    ...
    jmp  loop_start             ← 永久循环
```

**根因**：WinHTTP session 池化。`WinHttpOpen` 创建一次 session，3600+ 次 `WinHttpConnect` 共用一个 session。WinHTTP 内部连接池 / DNS 缓存 / TLS 状态在单 session 内无限膨胀 → 堆损坏 → `std::map`（`nlohmann::json` 内部或 CRT locale 子系统）的红黑树结构被破坏 → 旋转操作死循环。

### 第四轮：ntdll 堆损坏 + Access Violation（TerminateProcess 尝试后）

**根因**：`std::map` 树结构损坏触发 heap walker 自检 → `NtRaiseException` → 异常处理中二次访问坏指针 → double fault。

---

## 修复方案总览

### DLL 侧（Payload）

| 文件 | 修改 | 目的 |
|------|------|------|
| `Network.cpp` | `SendJsonPost` 加 `IsServerShutdownRequested()` 提前返回 | 退出时不再发 HTTP |
| `Hooks.cpp` | `TickFlushHook` 终端回合检测后立即 `return` | 退出时不再处理复制 |
| `NetDriverAccess.cpp` | `ScanForNetDriver` 加 shutdown 守卫 | 退出时不遍历 GObjects |
| `Utility.cpp` | `getObjectsOfClass` / `GetLastOfType` 加 shutdown 守卫 | 退出时不遍历 GObjects |
| `ServerLogic.cpp` | `ExitProcess(0)` → `TerminateProcess` | 不触发 atexit / CRT 清理 |
| `Network.cpp` | WinHTTP session **每请求创建/销毁**（不池化） | 避免长 session 堆膨胀 |

### Wrapper 侧（ServerLauncherGUI）

| 修改 | 目的 |
|------|------|
| `StopServerLocked`: `TerminateProcess` → 等 1 秒 → 升级 `taskkill /F /T` | 快速杀正常进程，僵尸升级到内核级强杀 |
| 30 分钟硬限制重启 | 防心跳线程 pipe 阻塞导致假活 |
| Watchdog 每 20 分钟查 Backend `/servers` 列表 | 如果服务器不在列表上（心跳中断 >15s）立即重启 |
| `InitServerUniqueId()` 生成持久化 8 位 hex ID | 跨重启唯一标识，用于 Backend 查活 |
| `LaunchServerLocked` 传递 `-serverid=` 给 DLL | DLL 心跳上报 serverId |

---

## 设计原则

**Wrapper 负责进程生命周期，DLL 不应处理自己的退出。**

- DLL 触发退出 → `TerminateProcess` 硬杀（不留任何 atexit / CRT 清理）
- DLL 可能进入的退出路径（心跳、GObjects 遍历、复制 Tick）全部加 shutdown 守卫
- 旧进程 = 僵尸时，Wrapper 不等待、不确认：`TerminateProcess` → 关句柄 → 启动新进程

---

## x64dbg 诊断速查表

| 现场特征 | 原因 |
|----------|------|
| `cmp [r12], [r15+8]` + `call ntdll.xxxWait` 循环 | 内核锁自旋：线程被杀在中途 |
| `mov rax, [rax+10]` + `cmp [rax+19], 0` 循环 | GObjects 链表遍历：atexit 访问已销毁对象 |
| `mov rax, [rdx+8]` + `cmp [rax+18], 0` + `jmp back` | std::map 树旋转：堆损坏导致树循环 |
| `NtRaiseException` + `mov [rax-20], r10` | 二次异常：heap walker 访问坏指针 |

---

## 未确定项

- 20h+ 长时间运行尚未验证（需压力测试）
- `TerminateProcess` 硬杀是否会丢失服务器状态？理论上不会——状态由 metaserver 持有，进程重启后 metaserver 数据仍保留
- 心跳线程是否可能在非退出场景下出现异常？当前 `SendJsonPost` 有 3s 超时，不会无限阻塞
