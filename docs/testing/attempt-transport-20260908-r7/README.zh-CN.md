[English](README.md) | 简体中文

# r7：冻结后的运输启动修复

已修复日志中的直接失败路径：房主取得 allocation 后，旧客户端仍调用已退役的独立房间 GET，服务端返回 410，随后记录 `P2P_AUTHORITY_LAUNCH_FAILED`。r7 改走当前 Attempt 的受权运输接口，旧房间接口继续退役；原生准入门禁未放宽。

另一个房间由房主先关闭，成员稍后退出得到 409 是实际时序；旧客户端继续轮询成员资格造成连续 403。r7 在确切的成员资格失效响应后清除旧大厅，并保留资源清理边界。启动失败现显示脱敏的具体原因及本轮关联。房主准备和换队按钮保留。

源码：Toolbox `43a4c82fe781ed67df6b4f4f82d1ff976ad586be`；已部署 Backend `8069d5e1126b8a585610b232e721aee45c57ee88`；Payload 字节未改，来源 `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`。实际验收输入到提交的绑定见 [回执](evidence/committed-source-binding.json)。

- 前端：57 项通过；Vite 和资源准备退出 0。
- 浏览器模拟：6 项作用域/错误/终止断言通过；不能代替实机。
- Rust/Tauri：rust-lib: 342 passed / 0 failed / 0 ignored; tauri-tests: 9 passed / 0 failed / 0 ignored；格式检查通过。
- 后端：[CI](https://github.com/Dubnium-105/ProjectRebound/actions/runs/34247644433)、[部署](https://github.com/Dubnium-105/ProjectRebound/actions/runs/34250303094)；本地 PostgreSQL 集成曾因缺环境跳过，具体 CI 运行及部署结果见证据回执。
- 本机：`PASS_REAL_DESKTOP_STARTUP_OWNER_READY_TEAM_LEAVE_ONLY`，详细范围见 [实机回执](evidence/distribution/desktop-r7-receipt.json)。
- 分发：ZIP 中每个文件按清单验字节；实验签名；未发布更新渠道或上传外部。

[下载 r7 ZIP](../../../artifacts/hardware-test-20260908-r7/rebound-hardware-test-20260908-r7-windows-x64.zip)，SHA-256：`c70935f5e671e136b190de6093ab6ef1d1474954e412236dd80382ca7bc63eb2`。

三台或更多实机统一使用 r7，按包内中文测试矩阵执行，记录房主具体错误与成员阶段。r7 的三机原生准入、第二次冷启动及完整可玩对局均未验收，`native_authority_admission_verified=false`、`release_ready=false` 不变。用户提供的双机失败记录是真实失败证据，远端 EXE 哈希尚未核验。

[52 项追加台账](52-item-r7-append-delta.json) 保留历史状态，未复核项标为 `NOT_REEVALUATED_R7`；不把跳过、未运行或缺环境写成通过。[证据索引](evidence-index.json) 固定每份回执的原始字节，原始配置、Token、数据库转储和账号资料未纳入。

本轮发生过一次公开误提交：四份含数据段的数据库转储及本地 fixture 源码被加入提交 `45e825f4e3799da460dfedecc9791b123698dffd`。已用精确旧 SHA 租约将公开分支撤回干净提交，原文件保留在本地并忽略。但旧提交对象在核查时仍可访问，未宣称 GitHub 缓存已清除，亦未排除转储内账号或凭据暴露。该提交不是部署源码，也未进入本测试包。详见 [纠正记录](evidence/publication-correction/publication-correction-receipt.json) 与 [尚未发送的清除申请](evidence/publication-correction/github-history-removal-request.md)；GitHub 侧清除按其 [敏感数据移除流程](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository) 另行处理。

2026-09-09 更新：清除申请已提交，两个仓库的公开提交状态见 [后续记录](publication-followup-20260909.md)。
