[English](README.md) | 简体中文

# Windows 原生实机测试检查

这些脚本用于严格名单实机测试包的只读预检和证据收集。它们不会启动 Boundary 或 Toolbox、打开房间、写命名管道、复制 `Payload.dll`、修改严格/原生能力标志，也不会处理 Grant、Ticket、账号标识或原始客户端日志。

该包是实机测试候选包。`package-manifest.json` 必须包含 `schema_version: 1`、`purpose: "hardware-test-only"` 和 `release_ready: false`。其中 `files` 数组按 POSIX 相对路径、字节数和 SHA-256 绑定每个普通包文件；清单自身不在该数组中，可选的 `SHA256SUMS` sidecar 覆盖清单及所有列出的文件，但不列出自身。包含一个外层目录的 ZIP 与已经解压的包根目录都可以使用。

清单还应记录实际 x64 构建工具链的 `msvc_minimum_version`，例如 `14.50.35717`。预检会将每个必需 x64 运行库 DLL 的版本与此值比较；如果字段缺失，VC 检查记为 `NOT_RUN`，不会在未验证时算作通过。

包必须带有 `Payload.dll`，清单必须固定经过复核的 Boundary 可执行文件：

```text
ProjectBoundarySteam-Win64-Shipping.exe
  SHA-256 181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843
  bytes   102362112
```

Payload 哈希从包清单读取，不在这些脚本中硬编码，因此新的签名分发物可以独立检查，不会把更早的未签名候选或已恢复的基线 DLL 当成同一制品。如果安装的游戏 `Payload.dll` 与包哈希不同，报告记为 `BLOCKED`；脚本不会自行修复。

## 命令

从解压后的包运行，或显式传入包 ZIP。游戏路径必须明确，因为 Steam 库不固定在某个盘符。`-GameBin` 是包含固定游戏可执行文件的目录的简写。

```powershell
$native = Join-Path $PackageRoot 'native'
# 将证据放在包旁边，避免改变正在验证的字节。
$evidence = Join-Path (Split-Path -Parent $PackageRoot) 'native-evidence'
# 如果 -OutputPath 解析到 $PackageRoot 内部，脚本会拒绝。

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File (Join-Path $native 'Preflight-WindowsNative.ps1') `
  -PackageRoot $PackageRoot `
  -GameExePath 'X:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64\ProjectBoundarySteam-Win64-Shipping.exe' `
  -ToolboxExePath 'X:\Tools\rebound_toolbox.exe' `
  -ExpectedToolboxSha256 '<已发布的 Toolbox SHA-256>' `
  -OutputPath (Join-Path $evidence 'preflight.json')

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File (Join-Path $native 'Collect-WindowsNative.ps1') `
  -PackageRoot $PackageRoot `
  -GameBin 'X:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64' `
  -OutputPath (Join-Path $evidence 'collect.json')

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File (Join-Path $native 'Test-WindowsNative.ps1') `
  -PackageRoot $PackageRoot `
  -GameBin 'X:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64' `
  -OutputPath (Join-Path $evidence 'test.json')
```

退出码为：`0` 表示所有请求的静态检查通过；`2` 表示发现具体哈希、路径或依赖阻塞；`3` 表示缺少必需输入或原生阶段未运行；`4` 表示脚本错误。包检查返回 `0` 不表示 Steam 认证、NMT/PreLogin、Reserve、CONNECTED、Playable、清理或下一局复用通过。这些仍属于单独协调的严格在线矩阵。

## 主机检查

脚本只读取并报告结构化事实：

- 固定游戏可执行文件的大小和 SHA-256；
- 包清单以及每个列出包文件的大小和 SHA-256；
- 如果提供明确游戏路径，已安装 `Payload.dll` 的大小和 SHA-256；
- 固定启动路径使用的 Engine 树 `steam_api64.dll`，位置为 `Engine/Binaries/ThirdParty/Steamworks/Steamv157/Win64`，使用默认 pin 时还检查当前固定 Steam-v157 字节和 SHA-256；
- Steam 可执行文件/签名存在性、Steam 进程存在性和精确游戏进程数；
- WebView2 Evergreen 存在性/版本和 x64 MSVC 运行库 DLL；
- 如果提供预期哈希，Toolbox 的大小、SHA-256 和签名。

缺少 Steam、WebView2、VC，或检测到精确游戏进程正在运行，会记录为 `BLOCKED`。脚本不会启动或终止进程。缺少 Toolbox 参数记为 `NOT_RUN`，不会猜测成功。

签名摘要只是证据；本地未签名 fixture 不是可分发的 Toolbox 候选。发布协调员必须在外层包清单中固定已签名 Toolbox 的哈希，并应用产品现有签名策略后才能把包交给测试机。

受支持的在线路径仍是已签名 Toolbox match-lobby 流程：使用受管 Project Rebound 清单，由 Toolbox 验证下载和安装事务。这些脚本绝不会复制 DLL。如果协调员在已发布清单之外提供实机测试候选，安装必须是单独、明确受控的步骤：先核对包和固定 EXE，确认精确 Boundary 进程已停止，保留现有 DLL 的带时间戳备份和哈希，只替换明确指定的 Payload 路径，并在正常 Toolbox/Steam/Grant/严格流程前核对安装后哈希。安装器只有在备份哈希核对正确时才可回滚；本包不提供临时复制快捷方式。不要调用 `startgame.ps1`，不要使用控制台 `open`，不要在命令行传递 ticket/grant，也不要使用旧房间/服务器包。包内 `native/` 脚本只用于诊断，不是兼容层。

报告有意省略用户名、账号/平台 ID、访问或刷新 Token、join Grant、Ticket、命令行、配置文件和原始客户端日志。只回传生成的 JSON，并从附带说明中删除本地新增路径。
