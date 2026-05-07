# ProjectRebound 运行时指令框架文档

生成日期：2026-04-27

本文描述 CommandFramework 模块的设计、协议、集成方式和使用示例。

## 0. 动机

当前 Python 启动器与游戏 DLL 之间仅通过命令行参数 `-match=IP:PORT` 单向传递连接目标。启动器启动游戏后即失去控制能力——无法中途切换比赛、无法获知游戏状态、无法在连接失败后自动重试。

CommandFramework 引入一条带外长连接通道（Windows Named Pipe + 文本行协议），使启动器在游戏运行期间可持续与 DLL 交互。

## 1. 架构总览

```
┌─────────────────────────────┐     Named Pipe      ┌──────────────────────────┐
│  Python 启动器（浏览器 GUI） │ ◄══════════════════▶ │  游戏 DLL（CommandFramework）│
│                             │  \\.\pipe\ProjReb_xx │                          │
│  PipeClient                 │    <CMD>\t<JSON>\n   │  ListenerLoop (1 thread)  │
│  ├─ send_command()          │                      │  ├─ ParseAndDispatch()    │
│  ├─ _reader_loop()          │                      │  ├─ Dispatch()           │
│  └─ _heartbeat_loop()       │                      │  └─ SendResponse()       │
└─────────────────────────────┘                      └──────────────────────────┘
```

- **管道名称**：`\\.\pipe\ProjectRebound_{uuid}`，由启动器生成 UUID 并通过 `-pipe=` 传入
- **I/O 模型**：Overlapped I/O，无独立看门狗线程，超时由 `WaitForSingleObject` 1s 轮询实现
- **线程模型**：DLL 侧单 ListenerLoop 线程；Python 侧 reader + heartbeat 两个 daemon 线程
- **安全模型**：NULL DACL，允许同桌面任意进程连接

## 2. 协议规范

### 2.1 帧格式

```
<CMD>\t<JSON>\n
```

| 元素 | 说明 |
|------|------|
| `<CMD>` | 命令标识符，大小写敏感，不含空白字符 |
| `\t` | 分隔符（`PROTOCOL_DELIM`） |
| `<JSON>` | JSON 参数体，**必须为单行（不含换行）** |
| `\n` | 行终止符（`PROTOCOL_NEWLINE`）。发出时始终用 `\n`，接收端兼容 `\r\n` |

### 2.2 命令表

| 方向 | 命令 | JSON 参数 | 响应 | 说明 |
|------|------|-----------|------|------|
| L → D | `ping` | `{}` | `pong\t{}` | 心跳请求 |
| L → D | `join` | `{"ip":"1.2.3.4:7777","token":""}` | `join_ack\t{"status":"ok"}` | 请求连接目标比赛 |
| D → L | `error` | `{"msg":"...","cmd":"...","detail":"..."}` | — | 解析错误 / 未知命令 |

> 方向：L = 启动器（Python）、D = DLL（C++）

### 2.3 示例帧

```text
ping	{}
pong	{}
join	{"ip":"127.0.0.1:7777","token":""}
join_ack	{"status":"ok"}
error	{"msg":"unknown command","cmd":"foobar"}
```

## 3. C++ 侧 — CommandFramework 类

### 3.1 文件

| 文件 | 用途 |
|------|------|
| `Payload/CommandFramework.h` | 类声明、回调类型、协议常量 |
| `Payload/CommandFramework.cpp` | 实现 |

### 3.2 生命周期

```
构造 → SetPipeName() → SetJoinCallback() → SetLogCallback()
     → Start()                        // 创建管道，启动 ListenerLoop 线程
     → [运行中：接收命令、分发回调]
     → Stop()                         // 设置停止标志 → CancelIo → join(5s)
     → 析构（自动调用 Stop()）
```

### 3.3 ListenerLoop 状态机

```
┌──────────────┐
│ CreateNamed  │◄────────────────────────────┐
│ Pipe         │                             │
└──────┬───────┘                             │
       │                                     │
       ▼                                     │
┌──────────────┐    超时 / running==false     │
│ ConnectNamed │─────────────────────────────┤
│ Pipe (over)  │                             │
└──────┬───────┘                             │
       │ 客户端连接                           │
       ▼                                     │
┌──────────────┐                             │
│ ReadFile     │◄──── 每次迭代重置计时器       │
│ (overlapped) │                             │
└──────┬───────┘                             │
       │                                     │
    ┌──┴──────────────┐                      │
    │  bytesRead == 0 │──► 客户端断开 ─────────┘
    │  bytesRead > 0  │──► lineBuf.append → 拆行 → Dispatch
    │  Wait TIMEOUT   │──► 看门狗超时 ──────────┘
    └─────────────────┘
```

### 3.4 回调接口

```cpp
// join 命令回调 — 在 ListenerLoop 线程内同步执行
using JoinCallback = std::function<void(const std::string& ip,
                                        const std::string& token)>;

// 日志回调 — 所有内部事件均通过此接口输出
using LogCallback = std::function<void(const std::string& msg)>;
```

### 3.5 看门狗机制

- 无独立看门狗线程
- `ConnectNamedPipe` 和 `ReadFile` 均使用 overlapped I/O
- 等待循环以 1s 为粒度轮询 `WaitForSingleObject(overlappedEvent, 1000)`
- 每次轮询检查 `running` 标志：`running == false` → 退出循环
- 连接超时（默认 30s）内无数据到达 → `CancelIo` → 关闭连接 → 重建管道
- `Stop()` 调用后 1s 内线程可响应退出

### 3.6 线程安全

| 操作 | 保护机制 |
|------|----------|
| `SendResponse()` 写入管道 | `writeMutex` 互斥锁 |
| `hCurrentPipe` 读写 | `writeMutex` 保护（连接/断开/写入/Stop） |
| `running` 标志 | `std::atomic<bool>` |
| 回调执行 | 均在 ListenerLoop 线程内同步顺序执行 |

## 4. Python 侧 — PipeClient 类

### 4.1 文件

`Desktop/ProjectRebound.Browser.Python/project_rebound_browser.py`

### 4.2 接口

```python
class PipeClient:
    def __init__(self, pipe_name: str, log_callback=None)
    def set_on_response(self, callback)          # callback(cmd, args)
    def connect(self, timeout_ms: int = 10000) -> bool
    def send_command(self, cmd: str, args: dict | None = None) -> bool
    def start(self, heartbeat_interval: float = 10.0) -> None
    def stop(self) -> None
    def is_connected(self) -> bool
```

### 4.3 线程模型

| 线程 | 职责 | 类型 |
|------|------|------|
| `_reader_loop` | 阻塞 `ReadFile`，按 `\n` 拆行，调用 `on_response` 回调 | daemon |
| `_heartbeat_loop` | 每 `heartbeat_interval` 秒发送 `ping` | daemon |

### 4.4 使用示例

```python
import uuid
from project_rebound_browser import PipeClient

pipe_name = f"ProjectRebound_{uuid.uuid4().hex[:8]}"

# 创建并连接
client = PipeClient(pipe_name, log_callback=append_gui_log)
client.set_on_response(lambda cmd, args: print(f"Got: {cmd} {args}"))

if not client.connect():
    raise RuntimeError("Failed to connect to game pipe")

# 启动心跳和读线程
client.start(heartbeat_interval=10)

# 发送 join 命令
client.send_command("join", {"ip": "43.240.193.246:7777", "token": ""})

# 启动游戏时传入 pipe 名称
app.start_client(connect="43.240.193.246:7777", pipe_name=pipe_name)

# ... 游戏运行中 ...

# 关闭
client.stop()
```

## 5. 集成点

### 5.1 DLL 侧（dllmain.cpp）

| 位置 | 集成内容 |
|------|----------|
| `LoadClientConfig()` | 从 `-pipe=NAME` 命令行参数读取管道名称到 `MatchPipeName` |
| `MainThread()` 客户端分支 | 构造 `CommandFramework`，注入 `OnJoinFromPipe` 回调 + `ClientLog`，`Start()` |
| `OnJoinFromPipe()` | 收到 pipe join 时更新 `MatchIP`（加 `MatchIPMutex`），触发 `ConnectToMatch()` 或 `AutoConnectToMatchFromCmdline()` |

### 5.2 Python 侧（project_rebound_browser.py）

| 位置 | 集成内容 |
|------|----------|
| `start_client()` | 新增可选 `pipe_name` 参数 |
| `launch_client_via_batch()` | 拼接 ` -pipe={pipe_name}` 到启动命令行 |

### 5.3 构建系统

- `Payload.vcxproj`：已添加 `CommandFramework.h`（ClInclude）和 `CommandFramework.cpp`（ClCompile）
- `Payload.vcxproj.filters`：已添加对应筛选器条目

## 6. 典型时序

### 6.1 启动器引导游戏连接

```
 Python                          DLL
   │                               │
   │  (游戏进程启动，带 -pipe=xxx)    │
   │                               │  CommandFramework::Start()
   │                               │  CreateNamedPipe → 等待连接...
   │                               │
   │── PipeClient.connect() ──────▶│  ConnectNamedPipe 完成
   │                               │
   │── PipeClient.start() ────────▶│  (reader + heartbeat 线程启动)
   │                               │
   │── ping\t{}\n ────────────────▶│  → pong\t{}\n
   │                               │
   │── join\t{"ip":"..."}\n ──────▶│  → OnJoinFromPipe(ip, "")
   │                               │  → MatchIP = ip (加锁)
   │                               │  → AutoConnectToMatchFromCmdline()
   │◀── join_ack\t{"status":"ok"}\n│
   │                               │
   │   ... 每 10s ping/pong ...    │
   │                               │
```

### 6.2 看门狗超时与重连

```
 Python                          DLL
   │                               │
   │           (网络卡顿 35s)       │
   │                               │  ReadFile WaitForSingleObject 超时
   │                               │  CancelIo → Disconnect → CloseHandle
   │                               │  CreateNamedPipe → 等待新客户端...
   │                               │
   │   PipeClient reader 检测断开   │
   │   → stop() → connect()        │
   │── PipeClient.connect() ──────▶│  ConnectNamedPipe 完成
   │                               │
```

### 6.3 游戏中重新指定比赛

```
 Python                          DLL
   │                               │  (已在比赛中)
   │── join\t{"ip":"NEW_IP"}\n ──▶│  → OnJoinFromPipe("NEW_IP", "")
   │                               │  → MatchIP = "NEW_IP" (加锁)
   │                               │  → ConnectToMatch()
   │                               │    → ShowLoadingScreen
   │                               │    → GoToRange
   │                               │    → travel NEW_IP
   │◀── join_ack\t{"status":"ok"}\n│
   │                               │
```

## 7. 配置项

| 配置 | 默认值 | 位置 | 说明 |
|------|--------|------|------|
| `watchdogTimeoutMs` | 30000 | `CommandFramework::SetWatchdogTimeout()` | 接收超时（毫秒） |
| `heartbeat_interval` | 10.0 | `PipeClient.start(heartbeat_interval)` | Python 心跳间隔（秒），应小于超时的一半 |
| pipe 名称 | 启动器生成 | `-pipe=NAME` 命令行参数 | `ProjectRebound_{uuid8}` |

## 8. 限制与后续规划

| 项目 | 现状 | 可能的改进 |
|------|------|-----------|
| 多客户端 | 单实例 Named Pipe（`PIPE_UNLIMITED_INSTANCES` 未启用） | 改为多实例 + 多线程接受，支持调试工具 + 主启动器同时连接 |
| 超时策略 | 硬编码 30s | 改为可配置，或根据 RTT 动态调整 |
| 错误恢复 | Python 侧 reader 检测到断开后需手动重连 | 添加自动重连逻辑 |
| 命令扩展 | 当前仅 ping / join | 可按需添加：`status` 查询游戏状态、`reconnect` 强制重连、`exec` 执行控制台命令 |
| DLL 卸载安全 | `Stop()` 带 5s 超时 join，超时则 detach | 进程退出时由 OS 回收线程，正常场景无问题 |
