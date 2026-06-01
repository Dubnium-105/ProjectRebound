# 01 — System Overview

> 来源合并：AI_Code_Reference（架构部分）、Mermaid/ProjectRebound_Architecture.md、game-and-server-launch-flow.md
> 最后更新：2026-05-22

## 项目全景

ProjectRebound 是一个 Dedicated Server（DS）改造项目：在《Boundary》游戏中注入 Payload.dll，使标准客户端进程伪装为 DS，支持 PvE/PvP 多人联机。

### 组件清单

| 组件 | 路径 | 职责 |
|------|------|------|
| **Payload.dll** | `ProjectReboundMainDLL/` | C++ DLL，注入游戏进程，Hook ProcessEvent，提供 DS 逻辑 |
| **dxgi.dll** | `dxgi/` | Proxy DLL，劫持 DirectX 加载链，自动加载 Payload.dll |
| **ServerLauncher** | `ServerLauncher/` | C++ 桌面应用（Slint UI），管理 DS 进程生命周期（启动/停止/Watchdog） |
| **Metaserver** | `Metaserver/` | Node.js 后端，提供 login server + 装备存档 + TCP RPC（端口 6969） |
| **Backend** | `Backend/` | 服务端心跳后端（已迁移到 `Deprecated/TestProjects/Backend/`，由 Metaserver 替代） |
| **Toolbox** | `rust-boundary-tool-box/` | Rust GUI 工具箱，管理安装/更新/启动（同事维护的独立仓库） |

### 数据流

```
Toolbox (Rust)
  └─ 下载 Release.zip → 解压到 Win64/
       └─ ServerLauncher.exe
            └─ 启动游戏进程（注入 dxgi.dll → Payload.dll）
                 ├─ [服务端路径] InitServerHooks() → TickFlush → 心跳 POST /server/status
                 ├─ [客户端路径] InitClientHook() → ProcessEvent 拦截修复
                 └─ PipeServer IPC ← → Wrapper 通信（日志/指令）
```

### 7 模块架构（目标，重构中）

```
Payload/
├── Core/          ① Entry, Globals, GameOffsets, Bootstrap
├── Config/        ② 命令行解析
├── Logging/       ③ LogManager, 四函数日志 API
├── Hooking/       ④ HookInstall, ServerDispatch, ClientDispatch, EnginePatches, GameHooks
├── Server/        ⑤ Replication, Backend, RoundManager, LateJoin, PlayerNaming, PvECamera, SideMountFixServer
├── Client/        ⑥ SideMountFixClient, UIShake, AutoConnect
└── API/           ⑦ APIInternal, APIExposed, PipeServer
```

当前状态：**重构尚未开始**，所有源码仍在旧目录结构下（`Hooks/`, `ServerLogic/`, `Utility/`, `Debug/` 等）。

### 关键架构决策

1. **所有 DLL 日志通过 LogManager 线程输出** — 禁止直接 `std::cout`
2. **FString 操作走内存偏移读写** — 不经过 SDK wrapper，避免 CRT/引擎分配器冲突
3. **SDK 类在 `namespace SDK` 下** — 前向声明必须用 `SDK::` 前缀
4. **异步 HTTP（PostLogin）** — Steam 名称解析在 detach 线程执行
5. **游戏线程操作通过 TickFlush** — pending 队列在主线程排空
6. **Wrapper 持有进程生命周期** — DLL 用 `TerminateProcess`，不用 `ExitProcess`

### 端口架构（Metaserver）

| 协议 | 端口 | 用途 |
|------|------|------|
| HTTP API | 8000 | login、loadout、connectServer |
| TCP RPC | 6969 | 游戏客户端 protobuf 通信 |
| UDP QoS | 9000 | matchmaking ping/pong |
| TCP MM | 9000 | matchmaking TCP |

### 相关文档

- `02-DLL-Internals.md` — DLL 内部机制
- `03-Game-Fixes.md` — 逐个修复记录
- `04-RE-Data.md` — SDK 偏移量全集
- `05-Backend-API.md` — 后端 API 规范
- `06-Infrastructure.md` — 部署与运维
- `07-Toolbox.md` — 工具箱
- `../Mermaid/ProjectRebound_Architecture.md` — 6 张架构图
