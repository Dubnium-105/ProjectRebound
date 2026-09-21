# r14b 流程修复测试包

[English](PACKAGE.md) | 简体中文


本包是联合验证候选，`release_ready=false`。包含配套 Toolbox 与 Payload，新增原生结算、结束窗口及清理重试。三账号完整战局和四种网络模式尚未实机验收。

**必须先由协调员部署本包对应的 Backend 源码并完成 schema 49 迁移。** 本次打包没有部署后端。客户端仍使用既有 api/cnapi/meta 服务；旧 schema 48 后端不能作为本包完整流程的验收环境。服务端压缩包仅供协调员使用，不包含服务器凭据。

1. 核对外部提供的压缩包 SHA-256，完整解压。
2. 完全退出 Boundary 和旧 Toolbox。使用自己的 Steam 账号及固定游戏版本。
3. 执行 `Install-StrictPayload.ps1 -GameWin64 '<游戏 Win64 目录>'`。脚本验证哈希并备份原 DLL。仅启动新 Toolbox 不会替换游戏 Payload。
4. 执行 `Check-ThisMachine.ps1 -GameWin64 '<同一目录>'`，保留检查结果。此检查不代表原生对局通过。
5. 双击 `Run-Toolbox.cmd`。所有测试者使用同一包、各自账号，创建新大厅；房主和成员分别准备后，使用现有严格准入流程。
6. 按 `TEST-MATRIX.zh-CN.md` 记录每种模式的准入、出生、结算、退出、清理与下一局结果。

固定 EXE：`ProjectBoundarySteam-Win64-Shipping.exe`，SHA-256 `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`。需要既有游戏加载器、数据文件、WebView2 和清单指定版本以上的 x64 VC++ Runtime；本包不含游戏本体。

结果页期间显示“正在结束”是正常状态。清理失败时保留房间卡片，使用“重试清理”；只有本地进程和后端网络资源都释放后才开始下一局。不要删除加密恢复记录来绕过屏障，不要改准入标志或使用控制台直连。

原生准入能力门禁保持现有要求。无法通过时应记录 BLOCKED，不能把证据采集、主菜单或构建成功当作 Playable。

测试后可用 `Check-ThisMachine.ps1 -GameWin64 '<目录>' -Collect` 收集结构化检查。不要发送账号配置、令牌、原始日志、`.dpapi` 文件或备份目录。安装回退使用 `Restore-StrictPayload.ps1 -ReceiptPath '<安装回执>'`，此前先退出游戏和工具箱。

EXE 与 DLL 使用已有测试证书签名，证书不属于 Windows 默认受信根；本包不安装根证书、不包含私钥。签名不代表生产发布或实机验收通过。
