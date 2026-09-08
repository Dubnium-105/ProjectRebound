# 权威名单唯一在线版本：当前实施与验收状态

已保留历史单客户端严格准入、连接和断开链路的实际执行证据，各次源码范围独立记录。当前候选停在登录完成之前，尚未完成 Pawn/Playable 与多人验收，不能发布为可用的权威在线版本。组件测试和历史单客户端准入不代表完整在线对局通过。

源码构建对：ProjectRebound `1fbc032665497502252f6245f3f1427d13314566`；Toolbox `e360b6f1c0cc6cf6c1e89288b1ef25b306bfe482`。后续证据文档提交不改变该构建源码对。逐项修改提交、实际证据路径和摘要见 [52 项台账](IMPLEMENTED.md) 与 [progress.json](progress.json)。

Backend 已落地唯一 MatchLobby/Attempt 权威、冻结名单、Reserve/Confirm/Release、终态清理与审计，以及 schema 44–48。Toolbox 已落地受管传输、按连接路由、会话代次与凭据存储、IPC/PID 校验、启动取消和账号切换清理。固定 EXE 的静态调用链已证实序列化写入函数同步复制借用字符串；动态探针仍未命中目标，完整生命周期以 [native-report.json](native-report.json) 与 [native-blockers.md](native-blockers.md) 为准。

独立 MinIO 验证先复现 Content-Type 元数据错误，修复后真实单段/分段上传、对象检查和读取通过。首次失败和修复后日志同时保留在 [Backend 报告](backend-report.json)。没有用合成 BattleLog 冒充真实对局产物。

Backend 在 345bde0 源码上实际执行全量 race：406 个顶层测试和 144 个子测试通过，0 失败，4 项 SKIP 保留。升级后的真实 schema 47 快照数据库也通过当前校验器的 4 项复测；迁移行尾修复前的失败日志保留。Toolbox 的 311 项组件测试、当前强类型 ID 的 2 项编译失败测试、Payload 的 24 项组件测试分别保留各自实际源码和日志，不合并冒称一次全系统验收。

52 项组件验收状态：{"PARTIAL": 30, "PASS": 10, "BLOCKED": 12}；22 项 E2E 实际状态：{"partial": 9, "not_run": 13}。组件 PASS 的实际日志判定和 owner 范围差异保存在台账中；不能据此推导整体验收通过。

可复核记录：

- [52 项组件与 22 项 E2E](acceptance-tests.json)：逐项记录返回码、命令、环境和证据，不把未运行或跳过算通过。
- [ROOT 集成测试](root-report.json)、[Backend 测试](backend-report.json)、[Toolbox 测试](toolbox-report.json)、[Native 测试](native-report.json)：保留历史失败、修复验证和各次源码范围。
- [五个本地产物](artifact-inventory.json)、[构建证明](artifacts-provenance.json)、[依赖清单](artifacts-provenance.sbom.json)：记录 SHA-256、源码、签名观察和兼容验收状态。
- [证据一致性审计](final-evidence-consistency-audit.json)：检查 52/22 清单、路径、哈希与源码对；它不运行游戏。

| 本地产物 | 字节 | SHA-256 |
|---|---:|---|
| control-plane | 51228959 | `4d7d96b3c79b1a9ba06fe8bf87cbac2466320d9e1a247947d3bceb099c781b6f` |
| meta-server | 38776886 | `1400c83b32daee19dd139ed804d48e452e8c7be616ec3e4ab24cae7abfb933be` |
| edge-relay | 19305636 | `aaf0df748018c89e59e24930d3a527f3f9df2b7b76a6ebf59328a16d429f5606` |
| Payload | 2094592 | `297c6ea8585c8a606b3ab31a9949bcb153d7469b890ef8603521fb8af2cbfc1b` |
| Tauri Toolbox | 46372352 | `9063474a92eef3d8d7102a0be16d38e5cb30eecc08a134619621cc1c5a1a6687` |

历史单客户端运行曾观察到 RESERVED、NATIVE_ADMITTED、CONNECTED、DISCONNECTED 及 Backend 对应回执。在线模式 MinPlayersToStart=2，单人没有进入正常角色选择和 Pawn/Playable。额外两台可同时运行的 Steam 会话尚不可用，三人真实对局矩阵未执行。完整出生、重连、队伍/席位及清理仍按 native-blockers.md 分项判定，不能仅修改 ready 标志。

最新 297C6 候选与曾成功的 42C72B 历史 DLL 本次都在登录完成之前停止；相同历史 DLL、wrapper 与 driver 的对照也未取得 Grant 或进入 Join。当前启动/认证边界尚未定位，这个有限对照不能认定问题只由新 DLL 引入。原生负例 mutation_executed=false、guard_coverage=0，不能把 harness 返回 0 当成验收通过；Backend finalize 实际返回 1。详见 [对照回执](evidence/native-scope-runtime-20260908/ab-old42-baseline-safe-receipt.json)。

Dedicated authority-only 清理已实跑 PENDING → owned HANDLE 退出 → CLEARED，错误 world、route、PID 均被拒绝。这次 fixture 预置服务器记录，没有执行正式进程注册；另一次独立 service 测试通过注册、签名心跳和能力失效检查，但尚未把真实 owned Payload 进程接入注册 worker。服务器保持 UNHEALTHY，不能算 READY 复用通过。

本次测试的独立 PostgreSQL/Redis 已按目录、端口与进程身份校验后停止，游戏原始 DLL 与 AppID 文件哈希恢复，测试游戏进程数为 0；原有服务和私有数据库资料保留。这仅证明本地测试环境恢复，见 [恢复回执](evidence/local-lab-restoration-20260908T011512Z/receipt.json)，不计作原生清理或 READY 复用验收。

生产签名、完整产物兼容验收和部署尚未执行。本地发布门禁因此保持失败，候选没有被推送或部署。
