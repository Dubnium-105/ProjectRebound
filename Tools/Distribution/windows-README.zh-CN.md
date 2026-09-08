[English](windows-README.md) | 简体中文

# Project Rebound 实机测试版本

本包连接现有 `api.project-rebound.space` / `cnapi.project-rebound.space` 与 `meta.project-rebound.space`。现有后端已于 2026-09-08 06:51 UTC 核验更新：数据库 schema 48，控制面、MetaServer、管理网页与 primary/gateway 边缘节点使用通过 CI 的 `584e000baf024e381c5bdb3417ad7ac879bebdca` 镜像。无需另建测试后端。

本轮用于三台实机的安装、登录、受管启动和原生准入证据采集。房主使用新增的“采集原生准入证据”按钮；它仍走真实 Steam 身份、冻结名单、签名 allocation、原生 Grant、Reserve/Confirm 和受管清理。

**完整可玩对局仍未验收。** 原生连接证据与构建能力分开记录；`native_authority_admission_verified=false` 保持不变，采集成功也不会发布 Playable。普通“冻结清单并启动”仍受这一门禁约束。先按测试矩阵完成真实三机采集，再依据证据修复或验证后续可玩流程；不能通过改标志、空 Token 或控制台 `open` 取得通过。

## 使用前

使用 Windows 10/11 x64、自己的 Steam 会话及固定版本 Boundary。游戏 EXE 必须为 `ProjectBoundarySteam-Win64-Shipping.exe`，SHA-256 为 `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`。本包不含游戏本体。

本次为现有 Rebound 运行文件的测试更新；游戏目录需要完整的加载器和数据文件。安装脚本会检查必要文件，缺项时停止。请先通过 Toolbox 受管安装补齐运行文件，不要混用其他来源的 DLL。

网络需允许访问上述 API/Meta HTTPS 服务，以及 MetaTunnel 使用的 `logic.project-rebound.space:443`（TLS TCP）。Logic 通道不是另一个测试后端；它与 Meta HTTP 是同一现有服务的不同入口。TLS 握手成功不等于 Steam 或游戏登录完成。

目标机器需要 WebView2 Runtime。缺失时从 [Microsoft 官方 WebView2 页面](https://developer.microsoft.com/en-us/microsoft-edge/webview2) 安装；[官方分发说明](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution) 解释了运行时检查与安装方式。还需 x64 Microsoft Visual C++ v14 运行库，版本不得早于本包清单记录的构建工具版本；下载入口见 [Microsoft 官方运行库页面](https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist?view=msvc-170)。

## 安装与启动

1. 核对分发者提供的 ZIP SHA-256，再完整解压。保留 `package-manifest.json`、`SHA256SUMS` 和所有脚本。
2. 完全退出 Boundary 与所有 Toolbox 窗口。打开 PowerShell，切换到解压目录。
3. 执行 `Install-StrictPayload.ps1`，参数 `-GameWin64` 指向实际游戏的 `ProjectBoundary\Binaries\Win64` 目录。脚本校验包、游戏和候选 DLL，保存旧 DLL 与安装回执，再替换 Payload。记下回执位置。
4. 双击 `Run-Toolbox.cmd`，在工具箱选择实际游戏目录并正常使用 Steam 登录。各测试者使用自己的账号，不复制配置或共享凭据。
5. 三人按 `TEST-MATRIX.zh-CN.md` 进入同一权威 P2P 大厅。全部人员到齐后，包括房主在内各自点击“准备”；名单变更会重置准备状态，需再次确认。房主选择“采集原生准入证据”，成员按受管流程自动启动和连接。游戏若提示平台登录，按 SPACE。记录每台机器的实际结果，采集结束后确认清理完成。

例如游戏位于 D 盘时：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Install-StrictPayload.ps1 `
  -GameWin64 'D:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64'
```

这里的 `ExecutionPolicy` 仅作用于该 PowerShell 进程，用于运行包内脚本；不会更改游戏或工具箱的原生准入检查。不要使用控制台 `open`、空 Token、关闭严格名单或手改 ready 标志。

## 检查和回传

安装后执行下列命令，使用实际游戏目录。入口会自动读取包中的 Toolbox 哈希，将报告保存到包外的同级证据目录：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Check-ThisMachine.ps1 `
  -GameWin64 'D:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64'
```

测试后在同一命令末尾添加 `-Collect` 收集报告。不要将输出目录设在包内，避免改变已验证的文件集合。

`native/Collect-WindowsNative.ps1` 只收集结构化版本、哈希、依赖和进程状态，不自动上传、不读取原始客户端日志。详细命令和退出码见 `native/README.md`。将生成的 JSON 和测试矩阵中的文字结果交给协调员，不发送应用配置、Steam 资料或备份目录。

包检查成功不代表在线对局成功。测试记录必须区分 `PASS`、`FAIL`、`BLOCKED`、`NOT_RUN`，并写下最后成功的步骤和错误代码。

## 恢复原版本

完全退出 Boundary 与 Toolbox，使用安装时生成的回执执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Restore-StrictPayload.ps1 `
  -ReceiptPath '<安装时生成的回执文件路径>'
```

恢复脚本只在备份哈希正确、目标仍是本轮候选且游戏目录匹配时恢复旧 DLL；如果文件已被其他安装操作更新，会停止而不覆盖。

## 当前已知限制

原生实机结果以本包对应的实际执行回执为准。历史版本曾在原生登录前停止，不能把旧版本的失败或单次窗口启动结果套用到本包。三机原生准入、角色出生与操作、准入负例、清理和下一局复用必须分别记录；缺少对应执行证据时保留 `NOT_RUN` 或具体 `BLOCKED`。

EXE 与 Payload 使用项目已有测试证书签名。证书不属于 Windows 默认受信根，签名状态可能显示为不受信任；本包不会安装根证书，也不包含私钥。签名和哈希检查结果、证书有效期及各制品源提交保存在包清单中；它们不代表生产发布已通过。
