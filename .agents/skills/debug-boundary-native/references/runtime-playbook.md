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

无 Frida 时使用已安装启动器：

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

自动连接在主菜单登录稳定后先停用 `PBMainMenuManager` 的顶层前端 widget，再直接执行
`open <target>`，不得先调用 `GoToRange`。首发 UI 回归验收同时检查最新
`clientlogs/clientlog-*.txt` 中依次存在 `Deactivated frontend menu` 与
`Connecting directly to match`，且不存在 `Entering Shooting Range`；Frida 必须确认
`GoToRange` 调用数为 0、`DeactivateWidget` 一次，并在服务器原生 StartMatch 后收到
`ClientStartOnlineGame/ClientMatchHasStarted/ClientRoundHasStarted/ClientSelectRole`。
完成首次出生后按 ESC 必须打开正常 `IN GAME` 角色界面，不能直接弹出靶场的退出确认页；
同时确认 `ShowConfirm/ExitRange` 没有出现在该输入窗口。

固定客户端还会在登录前查询已退役的 Unity Multiplay fleet；该接口当前返回
`404 fleet does not exist`，会使冷启动停在 `CONNECTING TO PLATFORM SERVER`。
`-LaunchClient` 默认临时启动 `local-qos-compat.ps1`，在 loopback 提供最小
Discovery 响应和 Multiplay UDP echo，再通过 `UnityMatchmaker.ChinaDiscoverURL`
的启动期 Frida patch 仅重定向本次本地 PVE 客户端；Shipping 构建不接受
RuntimeOptions、`Engine.ini` ConsoleVariables 或 `ExecCmds` 设置该值。普通
`startgame.ps1` 不启用该兼容层；诊断
原始行为时可给 PVE 启动器传 `-DisableClientQosCompatibility`。
该 patch 校验固定 EXE SHA-256，并在可执行文件入口点运行前等待原生
OverseaDiscoverURL 初始化完成后原位改写；不修改 Payload 或对局内逻辑。

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
