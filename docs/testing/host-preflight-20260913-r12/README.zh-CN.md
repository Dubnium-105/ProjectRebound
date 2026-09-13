# r12：修复启动中间状态被误判为协议错误

[English](README.md) | 简体中文

r11 实机失败：房主到达 `authority_ready` 后因 `Payload returned an invalid native match state` 进入清理，成员随后收到 `ROOM_NOT_CONNECTABLE`。后端记录房主 ready 约 4.224 秒后 complete，随后成员 409；最终 attempt 为 ABORTED、transport CLOSED、cleanup CLEARED。这是时序关联，不是逐请求因果证明，也不能独立证明游戏自身崩溃。见[后端只读证据](evidence/backend.json)。

源码确认 Toolbox 漏接 Payload 的 `local_pawn_ready`、`waiting_backend_confirmation`、`local_authority_pending` 三个合法状态。旧用户日志没有实际状态值，不能判定当时具体命中了哪一个。r12 补齐解析；未知状态和过期 request ID 继续拒绝，中间状态不能产生 Playable 证明。服务端 `RoundState=InvalidState` 是首次开局前等待语义，属于不同字段，保留其真实观测。见[源码契约复核](evidence/native/source-contract-receipt.json)。

Toolbox `a9d010fd9cdf9f7cb9c45c00afd9c2072289ab9d`; Backend `8069d5e1126b8a585610b232e721aee45c57ee88`; Payload `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`. 本轮后端未重部署、Payload 字节不变。测试时父提交与最终提交不同，通过[测试源码哈希](evidence/committed-source-binding.json)绑定最终提交。

真实执行：旧解析器的新回归 [1 项失败](evidence/regression-before/receipt.json)，修复后 Rust 366 项与 Tauri 9 项通过，失败和忽略均为 0；格式检查通过。第一次使用未限定模块名的 exact 过滤选中 0 项，记录为 NOT_RUN，不计通过。测试使用合成帧及 Windows 管道组件，不能替代真实 Boundary 验收。

首次完整测试因隔离 TEMP 目录缺失发生 LNK1104，保留[环境失败回执](evidence/rust-initial-environment/execution-receipt.json)；创建目录后重跑取得上述结果，未修改产品源码。

生产构建、测试签名、包字节检查、45 项文件预检和[实际桌面启动](evidence/desktop/ui-observation.json)通过。界面的版本检查结果单独记录，不混作启动成功。Toolbox CI 以[查询回执](evidence/ci-toolbox.json)为准；Main 最终提交 CI 在推送后独立查询。

分发包 `rebound-hardware-test-20260913-r12-windows-x64.zip`：23,540,813 字节，SHA-256 `7166eb6b177e84f0208b0f629d4dffb4c12fc1969ba80dfb5dd464984fd1bfed`。测试证书不在默认受信根，未安装根证书、未导出私钥。见[包回执](evidence/distribution/package-build-receipt.json)、[52 项追加](52-item-r12-append-delta.json)、[证据索引](evidence-index.json)。

所有机器退出旧版、统一使用 r12、新建大厅并确认队伍，房主和成员全部准备后由房主采集原生准入证据。三台独立 Steam 账号的原生入场、出生操作、清理及第二局仍为 NOT_RUN；缺少本轮三机环境回执。`release_ready=false`、`native_authority_admission_verified=false`，不能通过空 Token、关闭严格名单或手工 open 绕过。
