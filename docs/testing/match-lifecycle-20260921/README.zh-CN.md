# r14b 联合流程修复记录

[English](README.md) | 简体中文


本轮实现覆盖 `ProjectRebound` 的 Backend / Payload 与常用 `ProjectReboundToolbox` 仓库。范围是创建和加入大厅、准备和冻结、严格名单准入、原生结算、退出清理及创建新大厅开始下一局。P2P 直连、Relay、VNT 和 Dedicated 使用同一结算约束。BattleLog 上传、经验和奖励结算不在本轮范围内。

## 协议与行为

原有 `strict-roster-v2` 继续负责准入；新增独立能力 `match-lifecycle-v1`。旧 Payload 可以被诊断，但不能被新 Toolbox 提升为 Playable。

```text
OPEN -> FROZEN -> PROVISIONING -> CONNECTING -> RUNNING
                                                  |
                                      RESULT_CONFIRMED (seq=1)
                                                  v
                                                ENDING
                                                  |
                                        RETURN_READY (seq=2)
                                                  v
                                             COMPLETED
                                                  |
                                  native clear + transport close
                                                  v
                                      cleanup_state = CLEARED
                                                  |
                                          创建新大厅 / 下一局
```

| 边界 | 实现约束 |
| --- | --- |
| 创建 / 加入成功，准备请求失败 | 立即保留已确认席位，恢复同一大厅并允许重试准备。 |
| 准入与开始 | 继续使用冻结名单、签名 allocation、原生 Grant、Reserve / Confirm 与角色 quorum；不添加旁路。 |
| 正常结果 | 由当前原生 authority 的结果阶段产生，必须绑定 attempt、authority session、world、roster revision、route generation、match generation。 |
| 结束窗口 | Backend 进入 ENDING，拒绝新增准入和重连，保留已有网络和结束回执通道。默认窗口为 120 秒。 |
| 退场 | RETURN_READY 必须在 RESULT_CONFIRMED 之后产生；原生队列在明确 ACK 前保留回执。 |
| 重试与重启 | Toolbox 将回执写入 DPAPI 加密文件并同步、原子替换；HTTP 可幂等重放，迟到的 seq=1 和已确认结果后的 seq=2 不改变结果。 |
| 异常退出 | 未确认结果时按异常结束处理；Backend 已确认结果后，用户显式退出、进程丢失、fatal transport 或超时仍保留 COMPLETED，另记警告和清理状态。正常轮询不走此提前退出路径。 |
| 清理 | 原生进程 / allocation、房间成员、连接、VNT 与 Relay 资源都必须释放。关闭或撤销失败保留 PENDING 并重试。 |
| 下一局 | 前端本地清理完成不能覆盖 Backend 的 PENDING；旧局尚未清理的玩家不能通过另一大厅绕过门禁。 |
| 延迟事件 | UI 与运行时事件按 session、lobby、attempt、operation、run 约束，旧退出通知不能清除新局。 |

生命周期接口为 `POST /v1/match-attempts/{attempt}/host/lifecycle` 和 `POST /v1/game-servers/{id}/match-attempts/{attempt}/lifecycle`。authority session 只通过认证头传递，不进入公开快照。数据库需先迁移到 **49**；本轮没有迁移现有线上数据库。

## 恢复边界

原生清理记录保存本次启动的 PID 与进程创建时间。重启后仅能重新打开完全匹配的进程句柄；PID 被复用时不连接其管道，也不终止该进程。已获得的真实退出证据可持久化重试。若工具箱与子进程均已消失，且退出证据尚未落盘，记录继续阻塞清理，不能把“查不到 PID”伪造成退出回执。

此恢复机制的本地加密文件不能分发或跨账号复制。包中不包含运行时 `.dpapi` 文件、账号配置、令牌、原始日志或签名私钥。

## 实机验收矩阵

以下每种模式均需三个独立 Steam 账号完成；本轮自动化测试不能替代这些证据。

| 模式 | 建房 / 入房 / 准备 | 真实准入 / 出生 / 操作 | 原生结算 / 返回 | 资源释放 / 新大厅下一局 |
| --- | --- | --- | --- | --- |
| P2P 直连 | NOT_RUN | NOT_RUN | NOT_RUN | NOT_RUN |
| Relay | NOT_RUN | NOT_RUN | NOT_RUN | NOT_RUN |
| VNT | NOT_RUN | NOT_RUN | NOT_RUN | NOT_RUN |
| Dedicated | NOT_RUN | NOT_RUN | NOT_RUN | NOT_RUN |

还应分别执行：准备响应丢失、结算上报暂时断网、seq=1/2 重复和迟到、结束阶段断线、结果后 host 退出、Relay revoke 暂时失败、工具箱重启、旧局通知晚于新局、PID 复用拒绝。每次记录最后成功阶段、失败代码和清理状态，禁止记录凭据或完整玩家身份。

固定游戏 EXE SHA-256：`181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`。本轮不更改已验证的准入能力标志，不执行控制台直连，不把主菜单、管道可读、构建成功或测试 fixture 当作实机准入成功。

## 构建与验证

自动化结果及包哈希见同目录的验证记录和最终包清单。测试包使用新的文件目录，保留旧 r14b 制品；`release_ready=false`。新包需要配套 schema 49 后端，不能沿用旧包“现有后端已经满足要求”的声明。
