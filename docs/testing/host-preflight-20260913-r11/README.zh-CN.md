# r11：修复登录凭据轮换导致的启动取消

[English](README.md) | 简体中文

r10 双机测试失败：房主启动续租收到 `401 SESSION_REVOKED`，随后取消原生启动；中止请求也收到 401，从机最终等待到 `P2P_HOST_RECONNECT_TIMEOUT`。后台同一时间窗记录正常 `ROTATED` 后的旧会话请求，没有 refresh reuse。精确请求与会话之间缺少日志关联，撤销原因属于时窗相关性；现有证据不能独立证明游戏自身崩溃。见[失败与源码原因](evidence/native/reported-r10-failure-and-source-cause.json)和[后端只读回执](evidence/backend.json)。

r11 记住当前账号已发放玩家凭据的有限 SHA-256 指纹，使正常轮换后的旧请求仍可执行一次认证重试；账户校验和启动续租返回、使用当前账号的凭据。登出、换号、真实撤销及未知用途凭据继续拒绝，严格名单和原生准入不变。跨进程刷新串行化仍未验证，本轮没有将其实现或验收为通过。

Toolbox `06350349da2b9ef9d2a680f6654888e3f1f74779`；已部署 Backend `8069d5e1126b8a585610b232e721aee45c57ee88`；Payload source `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`。本轮未重部署后端，Payload 字节不变；Main 后续提交不等于实际部署版本。[源码绑定](evidence/committed-source-binding.json)使用真实测试源码哈希核对最终提交，测试时父提交与最终提交不同。

真实执行：Rust 364 项、Tauri 9 项通过，失败 0、忽略 0；两处格式检查通过。[最终 HTTP 回归](evidence/auth-final/final-receipt.json)绑定干净提交，6 个场景 PASS，3 个负向场景为 EXPECTED_REJECTION。两个新缺陷在[旧实现](evidence/auth-before/before-receipt.json)上实际失败。最初的[格式失败](evidence/rust-initial-format/execution-receipt.json)及[测试计数断言修正](evidence/auth-after/after-receipt.json)均保留；未执行的测试没有被记为通过。

生产构建、签名包字节检查、45 项文件预检和[实际桌面启动](evidence/desktop/ui-observation.json)通过。界面仍显示“版本检查失败”，该项不是通过。Toolbox 该提交的 Actions/checks/statuses 数量为 0，CI 记为 [NOT_RUN](evidence/ci-toolbox.json)；Main 文档提交的 CI 在推送后单独核验。

分发包 `rebound-hardware-test-20260913-r11-windows-x64.zip`：23,540,605 字节，SHA-256 `45d581a497e98606ab5e8b3bb688be07adb18f6545f48bfad05d82267c29573a`。测试证书不在系统默认受信根中；本包不修改根证书、不包含私钥。见[包收据](evidence/distribution/package-build-receipt.json)、[52 项追加记录](52-item-r11-append-delta.json)和[证据索引](evidence-index.json)。

所有实机完全退出旧 Toolbox，统一使用 r11 并创建全新大厅，确认队伍后每人（包括房主）准备，房主使用“采集原生准入证据”。仍需至少三台独立 Steam 账号机器验证原生入场、出生与操作、清理及第二局。这些 r11 实机步骤均为 NOT_RUN；`release_ready=false`、`native_authority_admission_verified=false`。禁止用空凭据、关闭严格名单或手工 open 取得通过。
