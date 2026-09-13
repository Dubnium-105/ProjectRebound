# r8 房主启动前校验与错误原因修复

房主提供的 r7 预检共 45 项，44 项 PASS，唯一 BLOCKED 是游戏目录中的 Payload。已安装文件为 1,021,440 字节、SHA-256 `295cd8835913f1807195ae7a69ae372480451ec1d93f075304886bc289ffbb07`；r7/r8 配套文件为 2,098,488 字节、`8d471662e947bcb6baf3b0380e23f90bef614f76ac9e7e8a82e2cfbf48619183`。按实际源码，这个差异会在 MetaTunnel 和游戏进程创建前被严格校验拒绝，与“没有游戏窗口”的报告一致。远端修复和重试尚未观测。

Toolbox 提交 `dc0cb49e63bfd92247e7b526909676c68b4c6b7a` 修复 HOST 路径中 `error.to_string()` 丢失底层原因的问题，在日志、UI 和返回错误前保留完整错误链并脱敏；同时在普通开局和原生证据采集的冻结请求前检查 P2P 房主本机运行时。检查失败时保持大厅开放。Dedicated 大厅创建者仍是远程玩家；真正启动原生进程前继续执行相同的严格检查。后端与 Payload 字节未修改。

实际验收：最终源码 348 项 Rust 测试、9 项 Tauri 测试，均无失败或忽略；格式检查与 Tauri 正式资产构建通过。初次定向测试因干净仓库缺少固定哈希的 aria2 构建资产而未编译成功，运行测试数为 0；已保留失败记录，补齐单个哈希匹配文件后重跑。测试使用隔离的 APPDATA/LOCALAPPDATA，未使用真实账户配置。

r8 测试签名、ZIP 全文件字节校验及本机 45 项只读预检通过。签名客户端已从分发目录真实启动并显示启动控制台；没有启动 Boundary 或创建多人 Attempt。测试证书不受系统根信任，WinVerifyTrust 返回 `CERT_E_UNTRUSTEDROOT`，没有修改信任库、导出私钥或进行生产签名。`release_ready=false`。

包：`rebound-hardware-test-20260913-r8-windows-x64.zip`，23,528,211 字节，SHA-256 `c618db9609963d70cc51745e6317bdb7bf7d41d286ee022db5953a25a5c4fe82`。签名 Toolbox SHA-256 `720087bd140d7b9d74c54f72a810ec7a7a970736be4210d536c4da7c8374f248`。

所有测试机关闭 Toolbox 和 Boundary，在新包目录运行 `Install-StrictPayload.ps1 -GameWin64 <实际游戏 Win64 目录>`，再运行 `Check-ThisMachine.ps1 -GameWin64 <同一目录>`。安装脚本先备份旧文件并验证替换结果。预检 PASS 后使用同一 r8 包重新建房、准备并开局。这个文件检查不等于原生准入或可玩验收。

仍待实测：房主游戏启动、命名管道与原生身份握手、三台以上实机严格准入、可操作战局、清理与下一局。52 项记录见 [追加清单](52-item-r8-append-delta.json)，逐文件证据指纹见 [证据索引](evidence-index.json)。现有后端健康检查和本轮请求记录已脱敏保存；没有以 HTTP 200 推断原生成功。
