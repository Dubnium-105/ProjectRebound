# 2026-09-08 实机诊断分发记录

已生成 Windows x64 便携测试更新包，连接现有 API/cnAPI/Meta 服务，未编入 `lab-testing`。包可用于安装、依赖检查、Toolbox 登录与正常启动阻塞观察。**当前不能完成普通路径的多人在线测试**：Payload 仍固定返回 `native_authority_admission_verified=false`。没有为了分发修改该标志、使用空 Ticket 或加入旧在线兼容层。

## 分发物

- [Windows ZIP](C:/wksp/ProjectRebound/artifacts/hardware-test-20260908-r2/rebound-hardware-test-20260908-r2-windows-x64.zip)，23,450,176 字节，20 个文件。
- ZIP SHA-256：`13858766eebd026ccbc14fc67ba41c958fb5f62beac9bdad26f4879c910a21f1`。
- [测试者安装说明](C:/wksp/ProjectRebound/artifacts/hardware-test-20260908-r2/rebound-hardware-test-20260908-r2-windows-x64/README.zh-CN.md)；[完整文件清单](evidence/r2/package-manifest.json)；[外部校验文件](evidence/r2/distribution-SHA256SUMS)。
- 此包是完整 Rebound 运行目录上的更新。目标需要合法固定版本游戏、既有加载器和数据、WebView2 及清单指定的 VC 运行库。包不包含游戏本体、配置、Steam 会话或私钥。
- MSI/NSIS 构建输出未分发：其内部 EXE 是签名前版本。本次仅分发最终签名便携 EXE 与配套 Payload。

## 源提交与制品绑定

| 制品或工作 | 实际来源 | 结果 |
|---|---|---|
| Payload 源 | `dc49017e488e8b5bf6f6544b4428c46d230c0abd` | 使用此前冻结的 `297c6e…fc1b` 构建产物副本做测试签名；本轮未重新编译 C++ |
| 后端候选源 | `1fbc032665497502252f6245f3f1427d13314566` | Payload 子树与上述构建源 `git diff --quiet` 实际为 0；部署状态另记，不由客户端包证明 |
| Toolbox 源 | `e360b6f1c0cc6cf6c1e89288b1ef25b306bfe482`，clean | Tauri CLI 生产构建实际 exit 0，默认产品 feature，不含 `lab-testing` |
| Toolbox 严格 pin | `77e255b833e2bf953e67f1b199c731a35ffe76e75415d7b65830a5dd656e0f53` | 先签 Payload，再把签后哈希编入 Toolbox；实际 EXE 字节中包含该 pin |
| 签后 Toolbox | `5b6be61380a7bd26f0ff203b1117731d1c0786221ca3e45147dae9c49d126b75` | 49,286,456 字节 |
| 签后 Payload | `77e255b833e2bf953e67f1b199c731a35ffe76e75415d7b65830a5dd656e0f53` | 2,096,440 字节 |

本轮分发工具与证据形成独立提交；它不改变以上制品源提交。测试证书 thumbprint 为 `B041917B2322ED509435B72356BA5AD9EA053378`，有效至 2027-08-26 UTC。实际 WinVerifyTrust 返回 `CERT_E_UNTRUSTEDROOT`（`-2146762487`），是当前产品接受的测试签名状态，不能称为 Windows 默认受信任的生产签名。未安装根证书、导出私钥或执行时间戳签名。

## 真实验证

| 检查 | 实际结果 | 证据 |
|---|---|---|
| Tauri 构建 | exit 0；早期失败和中断日志单独保留 | [构建回执](evidence/toolbox/build-receipt.json) |
| 测试签名 | 两个签后文件的 SHA、证书、WinVerifyTrust 已核对；未签名 DLL 被拒绝 | [Payload](evidence/signed/payload-signature-receipt.json)、[Toolbox](evidence/signed/toolbox-signature-receipt.json)、[WinTrust 探针](evidence/wintrust-probe-output.json) |
| 安装与恢复 | **隔离复制目录**中安装 exit 0、恢复 exit 0；`6c7b5e…c24a3 → 77e255…0f53 → 6c7b5e…c24a3` | 构建回执中的 `isolated_install_observation` 及原始日志 |
| 安装负例 | 缺游戏、缺恢复回执、清单或 DLL 篡改均拒绝；首次正向安装暴露的字典 Clone 错误已修复后重跑成功 | 构建回执列明每次执行与失败日志 |
| ZIP 字节 | 重新读取完整 ZIP，20 文件集合和逐文件字节一致 | [打包回执](evidence/r2/package-build-receipt.json) |
| 最终 ZIP 解压后预检 | exit **2 / BLOCKED**；42 项中 41 PASS、1 BLOCKED。阻塞是开发机仍装旧 DLL，未替换真实游戏 | [预检](evidence/r2/preflight.json) |
| 最终 ZIP 解压后收集 | exit **2 / BLOCKED**；报告在包外生成，无自动上传、原始日志或凭据收集 | [收集](evidence/r2/collect.json) |
| Rust `strict_build` 独立探针 | **NOT_RUN**；C# WinTrust 探针不代替 Rust 函数执行 | 构建回执 |
| 新签名 EXE 图形启动、原生登录和多人对局 | **NOT_RUN**；严格在线流程另外有已知源码门禁阻塞 | [验证汇总](validation-summary.json) |

打包扫描首轮误把加密库内的 PEM 分隔符字串视为私钥，exit 1。实际 EXE 含三个分隔符常量，均未跟随换行或编码私钥正文；扫描器已区分分隔符与完整私钥块，随后打包 exit 0。此失败保留在验证汇总，不算首次通过。

## 在线门禁的准确边界

`Payload/dllmain.cpp:389` 的固定 `false` 使在线 Payload 状态返回 `blocked/native_admission_unverified`。Member 可尝试初始化 MetaTunnel 并生成游戏进程，但 `Toolbox/src/launching/launch.rs:1287` 会在 blocked ACK 处结束等待，先于 Grant 获取及 `join_authorized`。Host 的动作资格不直接读取此常量，但严格 Playable 仍被阻断。SPACE 或 UMG 登录回调若可观察到，只能记录该步骤到达。

因此，本包的多人矩阵明确标为 BLOCKED；换硬件不代表该常量会变为 ready。原 52 项断点台账、此前原生对照失败与历史成功片段均保持原样，未将旧制品的验收结果移到新签名组合上。

所有保留文件的原始路径、大小与 SHA 见 [证据索引](evidence-index.json)。ZIP 留在本地供用户分发；本轮未发布 updater、上传客户端包或联系测试者。

## 现有后端更新状态

实际尝试的是现有目标的源码更新，没有另建测试后端。候选 `1fbc0326` 已推送到 `codex/rebound-deploy-1fbc`；[CI 34182380378](https://github.com/Dubnium-105/ProjectRebound/actions/runs/34182380378) 实际为 **FAILURE**，镜像工作跳过，不能报告构建镜像成功。失败包括部署源码脚本测试、集成容器缺少严格模式签名配置、两个 C++ 警告被视为错误、两个 Go 测试文件格式，以及管理网页依赖审计。具体失败和跳过步骤见 [CI 状态](evidence/ci-deploy-candidate-run.json) 与 [失败日志](evidence/ci-deploy-candidate-failures.log)。本轮 Windows ZIP 的 Tauri 构建成功与此 CI 失败分开记录。

现有目标源码构建因磁盘不足中止；尚未迁移数据库、切换容器或完成严格版本的公网验收。远端尝试与恢复的最终状态由单独的部署收尾回执记录，不把客户端包的地址配置当作服务端已更新。

最终分发版本为 **r2**，加入上述后端真实状态说明。早期内部打包证据保留用于审计，未对外分发。二进制和安装脚本均未随 r2 文案更新而改变。

[部署收尾回执](evidence/backend-deployment-safe-receipt.json) 确认临时 env 已恢复原始 SHA，当前 control/meta/Postgres/Redis 均 healthy、schema 仍为 43；只清理了本轮 source 目录与上传 tar，保留数据库备份及失败日志。收尾后可用空间约 1.2 GB，未继续尝试部署。
