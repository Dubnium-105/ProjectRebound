# Boundary 游戏调试与部署运行手册

本手册约束 Windows 本地 Meta-tunnel、Frida、游戏 UI 和 Payload DLL 的操作。只在用户请求修改或部署时写入文件；纯审计/诊断任务保持只读。

## 1. 运行时路径发现

不要把某台开发机的盘符或工作目录写成项目事实。每次会话先解析实际路径，并在后续命令中复用变量。

建议变量：

```powershell
# 仓库：优先使用当前工作目录；否则显式传入实际 clone 路径。
$RepoRoot = (Resolve-Path (Get-Location)).Path
if (-not (Test-Path -LiteralPath (Join-Path $RepoRoot '.git'))) {
    throw 'Set $RepoRoot to the active ProjectRebound checkout.'
}

# 游戏：优先由 Steam 库或用户提供的安装目录解析，不假设 C: 盘。
$GameBin = '<Boundary ProjectBoundary\Binaries\Win64 actual path>'
$GameExe = Join-Path $GameBin 'ProjectBoundarySteam-Win64-Shipping.exe'
$PayloadDll = Join-Path $GameBin 'Payload.dll'
$StartGame = Join-Path $GameBin 'startgame.ps1'
$StartLocalPve = Join-Path $GameBin 'start-local-pve.ps1'
```

如果存在独立的 MetaServer proto/log 辅助仓库，也应通过当前 checkout、环境变量或用户提供的路径解析；不要硬编码为固定目录。不要把运行时 PID 写入文档；每次启动后重新解析并校验。

## 2. 启动或附加前检查

1. 检查工作树并保留用户的无关改动：

```powershell
git -C $RepoRoot status --short
git -C $RepoRoot rev-parse HEAD
```

2. 校验 EXE 和已部署 DLL：

```powershell
Get-FileHash -Algorithm SHA256 -LiteralPath $GameExe
Get-FileHash -Algorithm SHA256 -LiteralPath $PayloadDll
```

3. 只接受完整路径匹配的游戏进程：

```powershell
Get-CimInstance Win32_Process |
  Where-Object { $_.ExecutablePath -eq $GameExe } |
  Select-Object ProcessId, ExecutablePath, CommandLine
```

4. 若 EXE SHA 与 `current-findings.md` 不同，停止使用现有 RVA 和注入构建，先重新静态分析。

## 3. 静态分析流程

1. 在 IDA/Hex-Rays MCP 可用时复用该精确 EXE 的数据库；不可用时使用本地反汇编/字符串/xref 工具，但不要猜测符号。
2. 从稳定的 RPC path、UFunction 名称、错误日志和 protobuf 字段访问开始找 xref。
3. 区分入口、consumer、delegate、completion 和 UI subscriber，记录每层的调用关系。
4. 对每个候选函数恢复：calling convention、参数宽度、FName 传值方式、枚举位置、返回值和对象所有权。
5. 用 RVA 记录地址，同时注明模块 SHA；不要只记录会随 ASLR 变化的绝对运行时地址。
6. 将函数命名、伪代码、调用点和成员偏移与只读 Frida 的实参/状态变化交叉验证。

## 4. Frida 只读联合探针

常规诊断只使用统一探针 `Tools/Frida/armory_probe.js` 及其启动器：

```powershell
Set-Location $RepoRoot
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1
```

附加已运行且已校验路径的进程：

```powershell
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1 -ProcessId <PID>
```

执行规则：

- 新观测点优先加入 `armory_probe.js` 的统一事件时间线，不新建重复的 FName/GObjects/ProcessEvent helper。
- `query_assets_*_ab.js`、`player_archive_level_ab.js`、`fieldmod_native_probe.js`、`persistent_armory_probe.js` 等文件属于历史专项/A-B 证据工具，不是当前默认启动链。只有在复现对应固定构建假设、且 `current-findings.md` 明确要求时才使用。
- 不把历史 A/B 探针的写内存、替换返回值或超时实验当成当前生产行为；若重新启用，必须固定 EXE SHA、限制单一变量、明确标注实验性质并可立即回滚。
- 在日志中关联 message ID、RPC path、角色、槽位、集合数量/哈希、completion 类型和状态码。
- 同时观察 Manager、PersistentUser saved/runtime、FieldMod pre-ordering、PlayerState equipping 和 UI/出生事件。
- 读取 `EPBSkinClass` 等小枚举时使用参数低字节，避免把寄存器高位脏数据误判为枚举。

对局内配装按下面的同一时间线判读：

1. `fieldmod.native_call/ServerPreOrderInventory enter` 中的 role、六槽和 `config_hash` 是提交值。
2. `match.native_boundary/server_preorder_inventory leave` 后，`player_state.changed.pre_ordering` 必须出现同一 role 与 hash；否则是原生服务端语义校验未接受。
3. `ServerConfirmRoleSelection leave` 后 `selected_role_id` 必须等于该 role；`possess_promote_inventory leave` 后 `possessed_role_id` 和 equipping hash 必须一致。
4. `K2_InventorySpawned leave` 只用于确认 actor 已创建；Payload 的 detail overlay 不得改变六槽 hash，只允许补角色外观与主/副武器 archive 细节。
5. 客户端和专用/监听服务端必须分别附加并记录 PID；不要把客户端出站 reflected RPC 当成服务端 implementation 已接受的证据。

## 5. Meta-tunnel 与游戏启动

当前玩家启动主路径是 **已签名的 Project Rebound Toolbox 发布版**：PVP、加入房间和
Toolbox 本地 PVE 客户端都会从 Toolbox 内嵌资产按完整 SHA-256 提取 MetaTunnel 到
`%LOCALAPPDATA%\com.projectrebound.toolbox\runtime\meta-tunnel\<sha256>`，使用随机
loopback HTTP/TCP 端口，并通过匿名 stdin 传递访问令牌。令牌刷新由 Toolbox 的共享
singleflight 认证流程负责；不得把令牌放入参数、环境变量或日志。Production Server
页面仍使用独立的生产配置，不得注入本地 PVE 的动态 LogicServerURL。

带 `lab-testing` 的实验 CLI 在设置 `PROJECT_REBOUND_LAB_API_ORIGIN` 时必须使用按
normalized origin SHA-256 隔离的
`%LOCALAPPDATA%\com.projectrebound.toolbox\lab\<origin-sha256>` 配置/runtime 根；不得读取、
登出或覆盖生产 `app_config.json`，也不得把 executable-adjacent legacy config 迁入 lab
scope。若生产启动报 `obtain MetaTunnel access token`，先展开完整错误链；当底层为
`refresh Project Rebound session: API 401 ... Invalid refresh token` 且缓存来自 lab
临时签名域时，执行一次新的 Steam 登录。不要复制、打印或手工搬运 access/refresh
token，也不要把该错误误判成 MetaTunnel EXE readiness 故障。

Toolbox 本地 PVE 还会在 Rust 内启动随机 loopback TCP/UDP QoS 兼容服务，并只给本次
客户端追加 `-LocalPveQosDiscoveryUrl` 与 `-LocalPveQosReadyEvent`。固定构建 Payload
必须在完整 EXE SHA-256、SizeOfImage、精确原始 URL、FString 可写容量和初始化器前缀
全部匹配后才改写发现 URL；成功回读后才设置命名事件。任一条件不匹配必须 fail closed，
不能退回 PowerShell、Python 或 Frida 运行时 patch。Toolbox 自身完整性校验不得绕过；
本地自编译 EXE 需使用项目认可的签名材料后才能做完整 GUI 冷启动验收。

以下 `startgame.ps1` 与 `Tools/PVE/start-local-pve.ps1` 流程保留为旧版/诊断路径，
不再是 Toolbox 的生产依赖。无 Frida 的旧版安装启动器调用方式为：

```powershell
powershell -ExecutionPolicy Bypass -File $StartGame
```

需要隐藏窗口启动时保留进程句柄或 PID 供后续精确检查：

```powershell
Start-Process -FilePath 'powershell.exe' `
  -ArgumentList @('-ExecutionPolicy', 'Bypass', '-File', $StartGame) `
  -WindowStyle Hidden -PassThru
```

不要绕过 Meta-tunnel 直接启动 EXE 来验收线上 archive；那会改变被测路径。生产 MetaTunnel 默认连接 `https://meta.project-rebound.space` 与 `logic.project-rebound.space:443`；`dubnium.top` 已弃用，不得作为 fallback。
`startgame.ps1` 只给游戏子进程追加 `NO_PROXY/no_proxy` 的 loopback 条目作为
防御性提示；本构建 UE 4.25 的显式 `httpproxy` 路径可能忽略该环境变量，因此不能
单凭它证明 `LogicServerURL` 已绕过系统代理。启动器不修改 Windows 全局代理设置。

本地 Dedicated PVE 使用仓库 `Tools/PVE/start-local-pve.ps1`。它不会调用旧
`ProjectReboundServerWrapper.exe`，而是通过独立 MetaTunnel 启动带精确
`-server -pve -LocalPveLoadout` 的服务端。默认只启动服务端：

```powershell
powershell -ExecutionPolicy Bypass -File `
  $StartLocalPve `
  -Map Warehouse -Difficulty normal -Port 7777
```

无 Boundary 客户端运行时可以增加 `-LaunchClient`，启动器会在服务端绑定 UDP
端口后启动带 `-match=127.0.0.1:<port>` 的客户端；已有客户端时应保持默认的
server-only 模式，再从游戏控制台执行 `open 127.0.0.1:<port>`。

自动连接在主菜单登录稳定后只停用精确白名单中的 `UMG_EnterGame_C`、
`UMG_LoginGate_C`、`UMG_MainMenuBase_C`，再直接执行 `open <target>`，不得先调用
`GoToRange`。新战局 `UWorld` 激活后必须调用 `HideLoadingScreen`，并对精确的
`UMG_LoginGate_C/UMG_Login_C` 实例执行 `RemoveFromParent`；只停用顶层 MainMenu
会留下 `CONNECTING TO PLATFORM SERVER` 登录认证层。白名单不得扩展到
`UMG_InGameOption_V2_C`、`ConfirmPage_C` 或未知 widget。

首发 UI 回归验收同时检查最新 `clientlogs/clientlog-*.txt` 中依次存在
`Hid direct-match frontend layer`、`Connecting directly to match`、
`Finalized direct-travel loading/auth UI`，且不存在 `Entering Shooting Range`。Frida
必须确认 `GoToRange=0`，并按 `object_class` 看到 `UMG_LoginGate_C`、`UMG_Login_C`
各自的 `RemoveFromParent` enter/leave；单独看到 `HideLoadingScreen` 或
`HideWaitingForServerTips` 不算通过。服务器原生 StartMatch 后还必须收到
`ClientStartOnlineGame/ClientMatchHasStarted/ClientRoundHasStarted/ClientSelectRole`。
进入等待和角色选择阶段均不得出现 `CONNECTING TO PLATFORM SERVER`。完成首次出生后
按 ESC 或点击齿轮必须打开 `YOU ARE IN GAMING / LEAVE MATCH` 正常对局菜单，不能直接
弹出靶场的退出确认页；同时确认 `ShowConfirm/ExitRange` 没有出现在该输入窗口。

固定客户端还会在登录前查询已退役的 Unity Multiplay fleet；该接口当前返回
`404 fleet does not exist`，会使冷启动停在 `CONNECTING TO PLATFORM SERVER`。
旧版 PVE 脚本的 `-LaunchClient` 默认临时启动 `local-qos-compat.ps1`，在 loopback 提供最小
Discovery 响应和 Multiplay UDP echo，再通过 `UnityMatchmaker.ChinaDiscoverURL`
的启动期 Frida patch 仅重定向本次本地 PVE 客户端；Shipping 构建不接受
RuntimeOptions、`Engine.ini` ConsoleVariables 或 `ExecCmds` 设置该值。普通
`startgame.ps1` 不启用该兼容层；诊断
原始行为时可给 PVE 启动器传 `-DisableClientQosCompatibility`。
该 patch 校验固定 EXE SHA-256，并在可执行文件入口点运行前等待原生
OverseaDiscoverURL 初始化完成后原位改写；它仅用于对照旧行为，不得作为 Toolbox
集成路径已经通过的证据。

启动器固定校验 EXE SHA-256、拒绝已占用端口与重复 dedicated 进程，并把两个
MetaTunnel 启动日志写到 `%LOCALAPPDATA%\ProjectRebound\local-pve\<timestamp>`。
客户端使用默认认证缓存；local PVE dedicated 使用独立的 `local-pve` 会话缓存，
避免两个长驻 MetaTunnel 轮换同一个 refresh token 并触发整个 token family 撤销。
dedicated 若在 world travel 阶段无 crash dump 地提前退出，启动器默认做一次有界
重试，并在最终错误中返回每次 launcher exit code、日志目录和 stdout 尾部。

## 6. UI 操作与人工验收

- 标准导航为 `NO -> CUSTOMIZATION -> HR & ARMORY`。
- UI 显示 ORLAN 时，在日志和协议中使用内部角色 `PEACE` 做关联。
- 游戏重启后重新绑定窗口；只匹配精确标题 `Boundary`，不要使用宽泛窗口规则。
- 自动化 UI 时，每个动作前获取新截图/可交互状态，一次只执行一个动作，再观察结果。
- 选择一个明显非默认且容易识别的样本，同时覆盖角色装备、角色皮肤、涂装、头饰、臂章、武器、配件、配件皮肤和武器挂件。
- 记录 UI 文本或 ID，不记录帐号、票据和 token。

五点一致性检查：

1. MetaServer 返回的 snapshot/archive。
2. `APBPlayerState` equipping。
3. `UPBFieldModManager` pre-ordering。
4. HR & ARMORY 显示。
5. 对局出生后的实际装备。

## 7. 构建与回归测试

后端快速验证：

```powershell
Set-Location (Join-Path $RepoRoot 'Backend')
gofmt -l .
go vet ./...
go test ./... -count=1
```

Payload 策略测试：

```powershell
Set-Location $RepoRoot
cmake -S Payload\Tests -B build\payload-tests -A x64
cmake --build build\payload-tests --config Release --parallel
ctest --test-dir build\payload-tests -C Release --output-on-failure
```

Frida 语法检查：

```powershell
node --check (Join-Path $RepoRoot 'Tools\Frida\armory_probe.js')
python -m py_compile (Join-Path $RepoRoot 'Tools\Frida\capture_armory.py')
```

对 Payload 主 DLL 使用仓库现有 Visual Studio/x64 Release 配置。不要把旧 build 目录的成功当成本次源码已经重编。

## 8. DLL 部署

只在用户明确要求部署时执行：

1. 完成 Release 构建和相关测试。
2. 用完整路径确认目标游戏进程已经停止；不要按名称批量终止进程。
3. 对现有目标 DLL 建立时间戳备份。
4. 使用显式 source/target 路径复制，不使用 glob。
5. 比较构建产物和目标文件 SHA-256，必须相同。
6. 通过安装目录的 `startgame.ps1` 启动。
7. 报告新 DLL 哈希和备份路径；除非用户要求结束，验收后让游戏保持运行。

参考命令：

```powershell
$sourceDll = '<absolute-release-payload-path>'
$targetDll = $PayloadDll
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$backupDll = Join-Path $GameBin "Payload.pre-$stamp.dll"
Copy-Item -LiteralPath $targetDll -Destination $backupDll
Copy-Item -LiteralPath $sourceDll -Destination $targetDll -Force
Get-FileHash -Algorithm SHA256 -LiteralPath $sourceDll, $targetDll, $backupDll
```

## 9. 冷启动与对局矩阵

每项同时记录“保存响应、native completion、下一次读取、UI 和实际角色状态”。

| 场景 | 最低要求 |
| --- | --- |
| 军械库解锁 | 原生库存 60 秒稳定；代表性角色/武器/配件/皮肤 `HasItem=true` |
| 第一次冷启动 | 非默认样本逐槽恢复；没有客户端容器直接写入 |
| 第二次冷启动 | 与第一次一致，排除只在本进程缓存生效 |
| 游戏内修改 | 保存状态可解释，再读完全一致 |
| 空/无效主武器 | 按原生规则回退，不制造空武器出生 |
| 首次出生 | 与 Meta/UI 五点一致 |
| 复活 | 不被旧 generation 或旧 pawn 覆盖 |
| 换角色 | role ID、大小写和角色专属兼容性正确 |
| 晚加入 | grace/lease 后得到最新配置 |
| 断线重连 | 新 lifecycle 重新读取，旧缓存不泄漏 |

## 10. 失败时的定位顺序

1. 验证 RPC 是否发出、message ID 是否配对、response wrapper error 是否成功。
2. 验证 protobuf wire 字段、大小和角色 ID 大小写。
3. 验证 native consumer 是否进入及 completion 是否被调用。
4. 验证 completion 原始 code 与仅限路径的归一化结果。
5. 验证 Manager/PlayerState 的 native 状态，再看 UI subscriber 是否刷新。
6. 最后检查物品类型、角色/武器兼容性、隐藏/开发资产和等级过滤。
7. 只有证明确有 native 缺口后才增加最小 hook；每次只改变一个假设并保留回滚。

## 11. 严格名单双机重连验收

1. 必须先完成首次双人出生，并确认两端 Pawn 均可移动、射击；仅有名单 `RUNNING`、角色选择页或等待开局画面不算通过。
2. 只关闭远端成员的 Boundary 游戏进程，保留其 `Start-LabClient.ps1`/实验 CLI。不要退出房间、重新 join、手工 `open` 或关闭 P2P 主机。
3. 等待 authority 侧明确记录 `DISCONNECTED` 和旧 connection generation；在此之前请求新授权应被 Meta 拒绝为连接仍存活。
4. 在远端成员原 CLI 输入 `retry`。新 grant 的 generation 必须恰好 `+1`，attempt、team、team slot、logical slot 必须与冻结 roster 完全相同。
5. authority 日志必须依次证明新 generation 的 `PreLogin` 准入、`PostLogin` 原生 Team/Camp 读回一致和 Meta `CONNECTED` 回报；旧 generation/JTI 的再次连接必须失败。
6. 远端成员重新选择角色并 Deploy，确认新 Pawn、HUD、武器、移动和射击；同时确认主机 world 连续存在、旧 Pawn/连接已释放、名单没有新增成员。
7. 可再重复一次，要求 generation 从 2 到 3。若关闭的是 P2P 主机或 authority/world 已丢失，attempt 必须中止，不能执行成员式 retry 或主机迁移。

## 12. LAN 严格名单三服务预检

1. 在启动游戏前分别验证 Control Plane HTTP、Meta HTTP `/health/live` 和 Meta Logic TCP；三项不能用同一个端口，也不能把 MetaTunnel upstream 指回 Control Plane。
2. LAN 明文 Meta 只允许专用 lab 构建同时开启 private HTTP 与 private plain Logic 两个显式门禁；API、Meta HTTP、Meta Logic 必须解析为同一个私网/回环 IP literal。生产测试不得开启这些门禁。
3. 两端必须使用同一构建包并在服务重启后创建全新 lobby/attempt。旧 token、allocation、join grant 或 frozen room 不可跨临时签名密钥复用。
4. 从机若在 `CONNECTING` 收到 `ROOM_NOT_JOINABLE`，先核对 managed-room attach 版本；名单内冻结成员应允许重挂载，名单外账号则必须在权威名单检查中被拒绝。
5. 若游戏显示 Meta 400，先检查 MetaServer stderr 与 `/connectServer` 路由，不要用手工 `open` 掩盖原生 Meta 层失败。

## 13. Listen 主机远端登录 `0x70` 崩溃检查

1. 先从 crash context 核对主机命令行包含精确 `-RoomAuthority`，异常为 read `0x70`，并确认栈包含固定构建 RVA `0x0156193B / 0x01561BB8 / 0x01584801 / 0x015A31E3 / 0x036CE872`。若地址或 EXE SHA 不同，不复用本节 hook。
2. `UWorld::NotifyControlMessage` 外层出现 Payload trampoline 不等于 hook 破坏参数。固定崩溃点的真实数据链是：远端 APBPlayerController -> RVA `0x01584730` -> `GetLocalPlayer` RVA `0x034FB080` 返回 null -> PBGameViewportClient map RVA `0x01561A60` -> RVA `0x0156193B` 读取 null `+0x70`。
3. Listen 启动日志必须同时出现 `server-only-load-overrides=native` 与 `remote-player-viewport-guard=enabled`；Dedicated 必须是 `enabled/disabled`。缺少日志时先核对实际安装目录 Payload 哈希，不要继续用旧 DLL 复测。
4. 第二名玩家连接时主机控制台应只记录一次 `[LISTEN] Suppressed a remote PlayerController client viewport-layer request.`，随后仍出现 `Player Connected!`。不要通过跳过整个 PlayerController 初始化、PostLogin、NotifyControlMessage 或伪造 PBGameViewportClient map value 来避崩溃。
5. 最小动态验收至少要求：第二客户端进入同一战局世界、authority 管道从 `player_count=1` 升为 2、`authority_ready` 持续为 true、两进程稳定 90 秒且不新增 crash。完成本地原生链回归后，仍需用签名 Toolbox 在真实两机 route 上验证 MetaTunnel、carrier、加入和实际出生。

## 14. 运行时更新包出现 `ServerLauncher` 的处理

1. `Project Rebound release ZIP contains an unmanaged path: ServerLauncher` 表示下载到的不是玩家运行时包；不要把 `ServerLauncher` 加入安装白名单，也不要关闭全归档预检。
2. 先从生产 `/v1/downloads` 读取 slug 为 `rebound-release` 的唯一条目，按 `latest_version_id` 选择版本，并记录非敏感的版本、大小和 SHA-256。下载 URL 必须仍是同源 `/v1/downloads/files/<version-id>`。
3. 下载后在解压前核对目录声明的大小与 SHA-256。玩家包顶层只能有 `Payload.dll`、`dxgi.dll`、`DT_ItemType.json`、`steam_appid.txt`、`project_rebound_version.txt`；历史 `BoundaryMetaServer-main` 只允许被验证后跳过，不能写入或覆盖现有用户副本。
4. 若旧 Toolbox 把 `latest` 解析为 GitHub 历史标签并下载旧 `Release.zip`，应先升级 Toolbox 到包含受管目录解析的版本，再重试 Project Rebound 更新；不能通过手工复制旧完整包绕过。
5. 发布前用临时目录解压最终 ZIP，逐项比较源/解压 SHA-256，并验证解压后的 Payload Authenticode 签名、RFC3161 时间戳和 `project_rebound_version.txt`。管理后台的版本标签、文件名、大小和 SHA-256 必须与该最终文件一致。

## 15. Legacy carrier 卡在 `CHECKING_DIRECT` 或选路后被候选重放打断的处理

1. 若错误同时包含 `state=CHECKING_DIRECT`、`candidates>0`、`selected_path=none`，说明候选已经进入 Control Plane，但探测/路径选择没有闭环；不要把它误判成无候选、地址格式错误或 Boundary 自身掉线。
2. 若错误为 `INVALID_CONNECTION_STATE: Connection is not gathering direct candidates.`，先查同一 connection 的权威 check 与 selection。若数据库已存在成功的 `LAN/IPV6/UDP_PUNCH` check，说明选路已经完成，错误可能只是排队中的稳定候选重放被旧客户端误当成全局失败，不能据此宣判数据面不可用。
3. 房主和从机都必须退出旧 Toolbox 并升级到 0.9.10 或更高版本。升级后创建全新房间和 connection；旧房间里的非重放事件、旧 selection 和旧 route generation 不作为复测证据。生产 Control Plane 还必须包含精确候选幂等重放热修复；内容发生变化的候选仍应返回 `INVALID_CONNECTION_STATE`。
4. 若创建房间先报 `no online room route is currently advertised`，但 Meta region 目录实际有在线 Relay，检查 discovery 和 UDP QoS 是否共用一个总 deadline。0.9.10 为发现与探测分别保留有界窗口；已发现 Relay 但 UDP 不通时应返回明确的 UDP route-check 诊断，而不是伪装成没有在线路由。
5. 不要用手工 `open`、直接启动 Boundary、把房间目录地址改成 `127.0.0.1`，也不要随意关闭防火墙或扩大端口白名单来掩盖协调层竞态。先让 Toolbox 的 carrier barrier 自己完成。
6. 正常 direct 闭环应从候选汇合推进到 `LAN` 或 `UDP_PUNCH` 选路；直连不可用但 Relay UDP 可达时应推进到 relay。只有 carrier ready 后才允许启动 Boundary；列表中可见房间或两端游戏进程存在都不算完成。
7. 若 0.9.10 的全新房间仍超时，保留同一次 attempt 的双方持久日志，记录脱敏后的 room/connection ID、Control Plane 状态推进、候选数量和最终选择，不收集 access/refresh token。重点区分“仍停在 `CHECKING_DIRECT`”、“已选路但客户端先消费了旧错误”和“选路完成后 UDP/Relay 数据面失败”。
8. 最低动态通过标准是：双方日志均确认同一 connection 的 carrier ready，主从机随后由 Toolbox 启动 Boundary，进入同一战局并能看到彼此、移动和射击；仅进入等待画面不算通过。

## 16. 权威清单模式的进房、恢复与启动检查

1. 先读取同一生产 origin 的 `/v1/client/config`。只有 `features.strict_roster_v1=true` 才是权威模式；查询失败必须让房间操作 fail closed，不能猜测开启，也不能静默退回 Legacy。若为 `false`，先检查 Control Plane 的严格清单开关、锁定游戏哈希、admission key ID/private seed 是否完整，再决定是否切门。
2. 严格模式的新房日志应依次出现 `create_match_lobby`、owner ready；从机应出现 `get_match_lobby`、`join_match_lobby`、member ready。任何新会话仍出现 `create_room`、`join_room` 或 `launch_room`，都表示 UI 未进入权威模式，优先检查双方版本、服务端 feature 投影和是否残留旧进程。
3. Toolbox 或 React 页面重启后调用 `get_active_match_lobby`。无活动大厅的 `MATCH_LOBBY_NOT_ACTIVE` 应清空本地状态；成员只恢复公开清单；P2P owner 还应在 Rust controller 内恢复受管 host credential。日志、Tauri DTO、前端 state 和诊断导出中都不应出现 host token、join grant、allocation secret 或 authority session secret。
4. owner 只能在 `local.can_start=true` 时冻结清单；成员永远没有手工启动按钮。冻结后观察 `match_lobby_frozen -> match_connection_changed -> match_carrier_ready -> match_auto_launch`。只有 carrier-ready 后出现 Boundary 进程才正确；列表可见、两端进程已启动或进入等待画面都不代表承载闭环。
5. 冻结后的普通 leave 必须被拒绝。从机断线且 `local.can_retry_connection=true` 时只调用 `retry_match_connection`，确认新 connection generation 回收同一冻结席位；不要重新 join、另建 Legacy 房间或让 owner 尝试主机迁移。
6. 若连接/启动失败，保留同一 lobby、attempt、route generation 的脱敏日志，按顺序检查：服务端清单 revision 和 ready 状态、attempt/grant 签发、受管 P2P room 投影、carrier barrier、Payload authority-ready、auto-launch。错误后若日志紧跟旧房间命令，应直接判定 fail-closed 边界被破坏。
7. 发布 ToolBox 时上传签名后的原始 EXE，安装 path 必须逐字为小写 `rebound_toolbox.exe` 且 `compression=none`。同版本 `vnt-runtime-manifest.json` 只作为服务器校验 sidecar；发布后重新读取公开 Manifest，必须恰好一个文件且不能包含 sidecar。否则客户端会返回 `manifest must contain only uncompressed rebound_toolbox.exe`。
8. 最低双机验收：双方退出所有旧 Toolbox 后强制升级到 0.9.12 或更高；创建全新 P2P 大厅；确认两个权威席位和队伍分配；owner 冻结同一 revision；双方 carrier-ready 后自动启动；进入同一战局看到彼此并完成移动、射击。把双方日志和服务端 attempt 状态作为同一次验收证据，未完成实机步骤时不得标记生产通过。
