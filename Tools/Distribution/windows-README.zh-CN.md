[English](windows-README.md) | 简体中文

# Project Rebound 实机测试版本

本包连接现有 `api.project-rebound.space` / `cnapi.project-rebound.space` 与 `meta.project-rebound.space`，无需填写另一个测试后端地址。它用于安装、Toolbox 登录与启动阻塞诊断，保留严格名单、真实 Steam Ticket、原生 Grant 和受管启动检查。

**当前不能完成普通路径的多人对局测试。** 此 Payload 的 `native_authority_admission_verified` 固定为 `false`，严格在线门禁会拒绝继续。换一台机器不会消除此门禁。请先验证安装、工具箱与现有服务连接，记录正常启动停在哪一步；多人矩阵暂记 `BLOCKED`，不得修改 ready 标志或绕过门禁。

截至 2026-09-08 03:30 UTC，现有服务仍运行旧版本、数据库 schema 43；本候选在线契约要求 schema 48。此次后端更新因主机磁盘不足与 CI 失败未完成，临时配置已恢复。现有服务登录检查不代表新的严格在线接口已可用，接口不匹配也不能归因于测试机。

## 使用前

使用 Windows 10/11 x64、自己的 Steam 会话及固定版本 Boundary。游戏 EXE 必须为 `ProjectBoundarySteam-Win64-Shipping.exe`，SHA-256 为 `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`。本包不含游戏本体。

本次为现有 Rebound 运行文件的测试更新；游戏目录需要完整的加载器和数据文件。安装脚本会检查必要文件，缺项时停止。请先通过 Toolbox 受管安装补齐运行文件，不要混用其他来源的 DLL。

目标机器需要 WebView2 Runtime。缺失时从 [Microsoft 官方 WebView2 页面](https://developer.microsoft.com/en-us/microsoft-edge/webview2) 安装；[官方分发说明](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution) 解释了运行时检查与安装方式。还需 x64 Microsoft Visual C++ v14 运行库，版本不得早于本包清单记录的构建工具版本；下载入口见 [Microsoft 官方运行库页面](https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist?view=msvc-170)。

## 安装与启动

1. 核对分发者提供的 ZIP SHA-256，再完整解压。保留 `package-manifest.json`、`SHA256SUMS` 和所有脚本。
2. 完全退出 Boundary 与所有 Toolbox 窗口。打开 PowerShell，切换到解压目录。
3. 执行 `Install-StrictPayload.ps1`，参数 `-GameWin64` 指向实际游戏的 `ProjectBoundary\Binaries\Win64` 目录。脚本校验包、游戏和候选 DLL，保存旧 DLL 与安装回执，再替换 Payload。记下回执位置。
4. 双击 `Run-Toolbox.cmd`，在工具箱选择实际游戏目录并正常使用 Steam 登录。各测试者使用自己的账号，不复制配置或共享凭据。
5. 通过工具箱尝试正常创建或加入测试房间，记录最后可达步骤。若游戏显示平台登录提示，可按 SPACE；当前门禁也可能在提示前结束启动。看到 `native_admission_unverified` 后停止该轮。具体场景与阻塞记录见 `TEST-MATRIX.zh-CN.md`。

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

当前开发机此前的原生诊断流程曾在登录完成前停止，新旧 DLL 对照尚未定位原因。这与当前候选固定返回准入未验证的门禁是两个独立限制。此包没有新的原生登录成功证据；完整多人出生、准入负例、清理与下一局复用尚未验收通过。不要把工具箱启动或地图加载记成完整对局通过。

EXE 与 Payload 使用项目已有测试证书签名。证书不属于 Windows 默认受信根，签名状态可能显示为不受信任；本包不会安装根证书，也不包含私钥。签名和哈希检查结果、证书有效期及各制品源提交保存在包清单中；它们不代表生产发布已通过。
