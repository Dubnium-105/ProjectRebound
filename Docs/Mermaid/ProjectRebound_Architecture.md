# ProjectRebound 系统架构 Mermaid 图表集

用于组会逐文件讲解。6 张图 + 端口速查附录。

---

## 图 0: 系统全景大图

```mermaid
graph TD
    subgraph EXTERNAL["外部服务"]
        GITHUB["GitHub Releases"]
        STEAM["Steam Web API"]
    end

    subgraph TOOLBOX["工具箱 (Rust)"]
        T_VER["版本检查"]
        T_DL["下载 Release.zip"]
        T_EXT["解压安装"]
        T_RUN["启动 Launcher"]
    end

    subgraph WRAPPER["Wrapper / ServerLauncher (C++)"]
        W_CFG["serverconfig.json 读写"]
        W_PROC["游戏进程生命周期"]
        W_DOG["Watchdog 心跳监测"]
        W_LOG["日志轮转 log-*.txt"]
    end

    subgraph GAME["游戏进程 (Payload.dll 注入)"]
        LOGMGR["LogManager 后台线程"]
        subgraph S_PATH["服务端路径 (DS模式)"]
            S_HOOK["PostLogin / TickFlush / ProcessEvent 钩子"]
            S_NAME["Steam 名称解析"]
            S_CAM["PvE 摄像机修复"]
            S_LNCHR["Launcher 服务端校验"]
            S_LATE["LateJoin 晚加入管理"]
            S_NET["心跳上报 Backend"]
            S_EXIT["安全停机 TerminateProcess"]
        end
        subgraph C_PATH["客户端路径 (Client模式)"]
            C_LNCHR["Launcher 状态修复"]
            C_PROJ["Projectile 爆炸修复"]
            C_UI["UI 冲刺抖动修复"]
        end
    end

    subgraph BACKEND["Backend (Node.js, port 3000)"]
        B_POST["POST /server/status"]
        B_GET["GET /servers"]
        B_CLEAN["超时清理 15s"]
    end

    subgraph META["Metaserver (Node.js)"]
        M_HTTP["HTTP API (8000)"]
        M_TCP["TCP RPC (6969)"]
        M_UDP["UDP QoS (9000)"]
        M_TCP2["TCP Matchmaking (9000)"]
    end

    TOOLBOX -->|"HTTP API"| GITHUB
    TOOLBOX -->|"写入 Win64 目录"| WRAPPER
    TOOLBOX -->|"spawn 进程"| WRAPPER
    WRAPPER -->|"spawn 游戏进程"| GAME
    WRAPPER -->|"stdin / stdout 管道"| LOGMGR
    WRAPPER -->|"GET /servers"| BACKEND
    S_NET -->|"POST /server/status"| BACKEND
    S_NAME -->|"WinHTTP GET"| STEAM
    C_PATH -->|"TCP 6969"| META
    C_PATH -->|"HTTP 8000"| META
    S_PATH -->|"TCP 6969"| META
```

---

## 图 1: 安装与启动链路

```mermaid
sequenceDiagram
    participant TB as 工具箱
    participant GH as GitHub
    participant FS as 文件系统 (Win64)
    participant WL as Wrapper Launcher
    participant GP as 游戏进程
    participant DLL as Payload.dll

    TB->>GH: GET /releases/latest (工具箱本体)
    TB->>GH: GET /releases/latest (ProjectRebound)
    GH-->>TB: tag + download_url
    Note over TB: 比较本地 version.txt, pr_is_newer 判定

    alt 需要安装或更新
        TB->>GH: GET Release.zip
        GH-->>TB: zip bytes
        TB->>FS: extract_zip_to_dir()
        Note over FS: Payload.dll, ServerLauncher/, BoundaryMetaServer-main/, version.txt
    end

    TB->>WL: spawn ServerLauncher.exe -cli
    Note over WL: CWD = ServerLauncher 目录

    WL->>WL: LoadConfigFile() / SaveConfigFile()
    WL->>WL: InitServerUniqueId() 生成 8 字符 hex
    Note over WL: serverId 写入 serverconfig.json

    WL->>GP: CreateProcess()
    Note over GP: -server, -serverdebuglog, -serverid={id}, -match=127.0.0.1

    GP->>DLL: LoadLibrary()
    DLL->>DLL: LoadConfig() 解析命令行参数
    DLL->>DLL: InitDebugConsole() 控制台初始化
    DLL->>DLL: LogManager 后台线程启动
    DLL->>DLL: InitServerHooks()
    Note over DLL: ProcessEvent, PostLogin, TickFlush 钩子安装完成

    DLL-->>WL: stdout 输出: [SERVER] Hooks installed.
    WL-->>TB: 进程就绪
```

---

## 图 2: 服务器运行时数据交换

```mermaid
sequenceDiagram
    participant DLL as Payload.dll (DS)
    participant BE as Backend (port 3000)
    participant WL as Wrapper
    participant FS as 日志文件 (log-*.txt)

    loop 每 N 秒心跳
        DLL->>BE: POST /server/status
        Note over DLL,BE: name, region, mode, map, port, playerCount, serverState, serverId
        BE-->>DLL: 200 OK
    end

    loop LogManager 批量输出
        DLL-->>WL: stdout 管道输出
        Note over DLL: ServerLog() 即时 flush, ServerDebugLog() 30行批量 flush
        WL->>FS: 写入日志文件
        Note over FS: 超过 1MB 自动轮转, 文件名加 _N 后缀
    end

    loop Watchdog 每 20 分钟
        WL->>BE: GET /servers
        BE-->>WL: JSON 数组 [{name, serverId, ...}]
        Note over WL: 检查本机 serverId 是否仍在服务器列表中
    end

    opt 服务器停机
        WL->>WL: StopServerLocked()
        WL->>DLL: TerminateProcess(process, 0)
        Note over WL: 等待 1 秒
        alt 进程未退出
            WL->>WL: taskkill /F /T /PID 强制终止
        end
    end

    loop Backend 每 5 秒
        BE->>BE: 清理超时服务器
        Note over BE: lastHeartbeat 超过 15 秒则移除
    end
```

---

## 图 3: 客户端运行时数据交换

```mermaid
sequenceDiagram
    participant GP as 游戏客户端进程
    participant DLL as Payload.dll (Client)
    participant META as Metaserver (8000 / 6969)
    participant STEAM as Steam Web API

    Note over DLL: ProcessEvent 钩子触发

    DLL->>DLL: HandleLauncherClientEvent()
    Note over DLL: OnRep_PendingState 强制同步, OnRep_Exploded 处理, ServerFiring 哑弹拦截 --- 详见 图5

    DLL->>DLL: HandleProjectileClientEvent()
    Note over DLL: MulticastExplode 强制触发 --- 详见 图5

    DLL->>DLL: HandleUICharacterClientEvent()
    Note over DLL: TickHelmetOffset 每帧, bIsRunning 检测, CamCache 清零或合成抖动

    DLL->>DLL: PVECamFix_Tick()
    Note over DLL: LevelSequence Status 内存轮询, Playing 转 Stopped 检测, ForceFirstLifeSpawn 触发

    DLL->>DLL: UserNameFix_DrainPending()
    Note over DLL: 异步解析队列排水, FString 直接内存写入偏移 0x0300

    DLL->>STEAM: GET /profiles/{steamid64}/?xml=1
    STEAM-->>DLL: XML 响应: steamID 对应 PlayerName
    Note over DLL: 结果缓存, 异步线程处理

    GP->>META: TCP 6969 protobuf RPC
    GP->>META: HTTP 8000 /api/*
```

---

## 图 4: 文件级模块组织

```mermaid
graph LR
    subgraph PROJ["ProjectRebound 仓库"]
        subgraph PL["Payload (C++ DLL)"]
            PL_MAIN["dllmain.cpp 入口"]
            PL_DBG["Debug/ LogManager"]
            PL_CFG["Config/ 参数解析"]
            PL_HOOK["Hooks/ 钩子中心"]
            PL_SL["ServerLogic/ 回合与停机"]
            PL_UTIL["Utility/ 修复模块"]
            PL_NET["Network/ 心跳与HTTP"]
        end
        subgraph SLG["ServerLauncherGUI (C++)"]
            SLG_WRP["wrapper.cpp 核心逻辑"]
            SLG_CLI["initcli.cpp CLI入口"]
            SLG_GUI["initgui.cpp GUI入口"]
            SLG_MAIN["main.cpp 分发入口"]
        end
        subgraph BE_PROJ["Backend (Node.js)"]
            BE_IDX["index.js 服务器注册与列表"]
        end
    end

    subgraph META_REPO["BoundaryMetaServer 仓库"]
        subgraph TB["TestBuild/"]
            TB_IDX["index.js 路由与API"]
            TB_LS["game/loadoutStore.js"]
            TB_DI["game/definitionIndex.js"]
            TB_PXY["proxy.js 代理模式"]
        end
    end

    subgraph TOOL_REPO["rust-boundary-tool-box 仓库"]
        subgraph SRC_CORE["src/core/ 核心逻辑"]
            SRC_MAIN["core.rs 常量与清单"]
            SRC_INST["install_ops.rs 安装流程"]
            SRC_PAY["payload.rs 解压与下载"]
            SRC_PROC["process.rs 启动管理"]
            SRC_RT["runtime_ops.rs 运行时模式"]
        end
        subgraph SRC_APP["src/app/ 应用层"]
            SRC_CTL["controller.rs 初始化"]
            SRC_UPD["update.rs 版本检查"]
            SRC_MSG["messages.rs 消息处理"]
        end
    end

    PL_UTIL --> PL_UTIL_D["UserNameFix.cpp, PVECamFix.cpp, LauncherFix.cpp, UIFix.cpp, Utility.cpp"]
    PL_HOOK --> PL_HOOK_D["Hooks.cpp: ProcessEvent(Server+Client), PostLogin, TickFlush"]

    style PL fill:#2d5a27,color:#fff
    style SLG fill:#5a2727,color:#fff
    style BE_PROJ fill:#275a5a,color:#fff
    style META_REPO fill:#5a5a27,color:#fff
    style TOOL_REPO fill:#4a275a,color:#fff
```

---

## 图 5: IsLocallyControlled 问题概念图谱

```mermaid
graph TD
    ILC["IsLocallyControlled()"]
    ILC -->|true| LS["ListenServer (主机玩家)"]
    ILC -->|false| DS["Dedicated Server (所有客户端)"]

    LS --> OK["[OK] 所有 BP 逻辑正常执行"]
    DS --> GATE["[FAIL] 带 IsLocallyControlled 门控的 BP 函数被静默跳过"]

    GATE --> L1["Launcher 状态机损坏"]
    GATE --> L2["UI 冲刺抖动残留"]
    GATE --> L3["Projectile 爆炸动画缺失"]
    GATE --> L4["PvE 摄像机脱离"]
    GATE --> L5["Grappling Hook 状态机损坏 (待修复)"]

    L1 --> F1["修复方案: HandleLauncherClientEvent\nOnRep_PendingState 强制同步 CurrentState\n清除 bIsFiring / BurstCounter 卡死标志\n手动调用 K2_Standby / K2_Ready"]

    L2 --> F2["修复方案: HandleUICharacterClientEvent\nTickHelmetOffset 每帧检测\nbIsRunning 为 0 时清零 CamCache\nbIsRunning 为 1 时合成正弦抖动"]

    L3 --> F3["修复方案: HandleProjectileClientEvent\nOnRep_Exploded 触发时\n绕过门控直接调用 MulticastExplode"]

    L4 --> F4["修复方案: PVECamFix_Tick\nLevelSequence Status 内存轮询\nPlaying 转 Stopped 检测\nForceFirstLifeSpawn 重连摄像机"]

    L5 --> F5["(同一模式, 待逆向工程)"]

    style ILC fill:#333,color:#fff
    style DS fill:#8b0000,color:#fff
    style LS fill:#006400,color:#fff
    style GATE fill:#cc0000,color:#fff
```

---

## 附录: 端口与协议速查

| 组件 | 端口 | 协议 | 用途 |
|------|------|------|------|
| Backend | 3000 | HTTP | 服务器注册、心跳、列表查询 |
| Metaserver | 8000 | HTTP | 登录、Loadout、ConnectServer |
| Metaserver | 6969 | TCP | Protobuf RPC 游戏客户端通信 |
| Metaserver | 9000 | UDP | Matchmaking QoS Ping / Pong |
| Metaserver | 9000 | TCP | Matchmaking |
| Steam API | 443 | HTTPS | 玩家名称解析 (xml=1) |
| GitHub API | 443 | HTTPS | 版本检查、Release 下载 |
