# r14b hardware-test package / 硬件测试包

r14b 打包完成。包内 23 个文件已逐项回读校验，状态为 `PASS_PACKAGE_BYTES_ONLY`。

- 文件：`artifacts/hardware-test-20260913-r14b/rebound-hardware-test-20260913-r14b-windows-x64.zip`
- 大小：23,798,898 字节。
- SHA-256：`cdea5ccd4a2ae77e0487aea63b5c39ff28f76faebb308ac0fd54f6e9ae096d90`
- Payload 来源：`644bd068f37853b0f29c260f4998abb60a480cc5`。
- Toolbox 来源：`33fec346d55ca85836e26f9447a86c05a8b92eab`。

本机预检为 `BLOCKED`：`game.payload.present` 检测到已安装 Payload 的 SHA-256 为 `fe848b3752def0b7d47ad7153e1bb24aa49487be5ebdbb26cf0ae4e2fbf8efdd`，包要求 `69f9190444c7bd2199df944aaa6f20b76c51d2e801094bb7748604b8d7e9151a`。本次只读预检没有替换 DLL 或启动游戏。安装包内新 Payload 后，需重新运行 `Check-ThisMachine.ps1`。

测试签名使用现有证书，未导出私钥或修改信任存储。签名回执为 `PASS_TEST_SIGNATURE_ONLY`；WinVerifyTrust 返回不受信任根证书，不代表生产签名信任通过。

`release_ready=false`；本轮原生多人入场、出生和第二次冷启动验收均未运行。r13 的 `world_ready_timeout` 根因仍未知。r14b 保留严格名单及签名准入，包含首次 PID 管道连接时序调整、InitListen 失败传播和 HOST 诊断。

The archive is complete and all 23 archived files passed byte verification. Host preflight is blocked by the previously installed Payload hash. No DLL was installed and no game was launched. Install the new packaged Payload and rerun preflight before hardware testing. Native multiplayer acceptance has not run; this is not a production-ready release.

Evidence is preserved in [evidence](evidence/); archive and manifest hashes are recorded in the package build receipt.
