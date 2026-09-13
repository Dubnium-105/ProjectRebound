# r9 原生 Authority 端口目标与错误原因修复

r8 的两台实机结果为 FAILED：房主游戏窗口已经打开并等待 authority，但 Legacy hostproxy 目标被当作原生游戏监听目标发送，Payload 的 authority_port_mismatch 因此终止启动。用户日志没有记录实际远端代理端口，文档不把合成测试夹具端口当作实机事实。r9 将 HOST 原生 authority 目标与 Member/代理房间目标分离；只有 HOST 启动时显式传入 -port 和 -external，并验证原生 ACK，Member 目标保持 endpoint-only；refresh 复用相同的 HOST 目标配置和 ACK 校验。VNT HOST 使用稳定虚拟地址，不等待尚未发布的 Member endpoint，从而去掉启动目标依赖游戏已经就绪的循环依赖；VNT 不可用时仍在准备阶段报错。错误脱敏规则保留 named pipe: Strict authority startup failed 这类首因，同时继续移除真实管道路径、结构化 pipe/pipe_name 字段、Token 与 nonce。

本次源码提交：87a9fc8869560fe30867aaf98536baaad8217551。Backend 仍为 8069d5e1126b8a585610b232e721aee45c57ee88，Payload 源提交仍为 98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d；没有 Backend、Payload 或部署变更。此前 r7 的 Payload 版本差异不再作为 r9 阻断。

最终 r9 自动检查数量由 r9/rust-final-v2/execution-receipt.json 动态读取：Rust 358 项，Tauri 9 项，共 367 项通过；失败 0，忽略 0。格式检查、Tauri 正式资产构建、本机文件预检和包字节校验均以收据为准。首轮仅格式检查失败，运行测试数为 0，原始收据和 fmt.log 已单独保存，未计入通过数量。

精确提交 87a9fc8869560fe30867aaf98536baaad8217551 的 GitHub Actions/checks 查询结果为 NOT_RUN（没有 workflow、check 或 commit status 记录）；本地测试结果不替代 CI 结果，证据见 evidence/ci-toolbox.json。

r9 桌面记录只证明签名测试客户端能够启动并显示大厅控制界面。r9 原生 Authority 准入、三台或更多实机矩阵、可玩战局、清理后的第二局均为 NOT_RUN；release_ready=false。测试签名仍是 test-only，根证书不受系统信任，不能视为生产签名。

包：rebound-hardware-test-20260913-r9-windows-x64.zip，23,535,108 字节，SHA-256 7c78c442b09419bef501b38cb6af7ca1ea03627723c75b4d04561d2420e5b6d5。逐项追加清单见 52-item-r9-append-delta.json，证据字节哈希见 evidence-index.json。

当前仍待三台以上真实 Steam 实机使用同一 r9 包完成：安装并通过严格文件预检、房主原生 ACK、Member 进入、Playable 回执、失败清理和下一局冷启动。未运行的项目不会记录为通过。
