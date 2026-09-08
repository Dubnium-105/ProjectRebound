[English](README.md) | 简体中文

# 2026-09-08 多人实机测试交付

现有后端已更新到 schema 48；本机使用同一 r4 包实际进入 PvE 第一人称场景、部署角色并移动，及时部署的复测还取得 Payload 离线 Playable 日志且没有超时，退出后目标进程数为 0。三台实机在线准入和完整对局尚未执行。测试包用于安装、登录和受管原生准入采集，`native_authority_admission_verified=false` 与 `release_ready=false` 保持不变。汇总见[最终交付回执](final-handoff-receipt.json)。

## 交付文件

分发本机 `C:/wksp/ProjectRebound/artifacts/hardware-test-20260908-r4/rebound-hardware-test-20260908-r4-windows-x64.zip`（23,487,655 字节）。ZIP SHA-256：

```text
73bff684e19d0644fe310fa833624150e5b7c9105013482198a90a1ce6651689
```

Toolbox 源提交为 `30148ad96e08ee4e26cd4972c557a4dee2340c21`，Payload 源提交为 `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`。包内包含生产 UI、测试签名的 EXE/DLL、安装/恢复脚本、版本检查与收集器，以及中英文安装说明和三机测试矩阵。它是已有完整 Rebound 运行文件的测试更新，不含游戏本体。测试证书未被系统默认信任，未安装根证书或发布生产更新。完整制品链见[分发回执](distribution-r4-receipt.json)。

## 实际验收

| 项目 | 实际结果与证据 |
|---|---|
| 后端与 CI | 已部署镜像对应 `584e000`；原 CI 11 个作业成功，部署 4 个作业成功，数据库由 schema 43 升到 48。见[部署回执](deployment-receipt.json)。 |
| 服务器空间清理 | 移除 77 个未使用项目镜像，释放 2,045,927,424 字节；数据卷保留。构建缓存清理实际回收 0 字节，未计入收益。 |
| 本机空间清理 | 删除经路径/进程复核的旧 Rust debug 增量缓存，C 盘可用空间实际增加 32,452,939,776 字节，清理后为 34,377,019,392 字节；四份当前制品哈希保持不变。见[清理回执](local-cache-cleanup-receipt.json)。 |
| 原生登录故障 | 现有 Compose 覆盖配置缺少 Redis `EVAL/EVALSHA`，导致游戏票据消费失败。已备份并修复原服务权限；无需重启 Redis/Meta 或更换凭据。见[修复回执](meta-acl-repair-receipt.json)。 |
| 部署防复发 | `0aec1e7`、`b66bc66`、`7c130f3` 在部署前验证最终合并配置，检查完整命令与最小权限，诊断不回显配置值。真实修复配置通过，真实旧配置按预期被拒绝。仅这 5 个修复文件组成的公开提交 `129f1ca`，其[最新 CI](https://github.com/Dubnium-105/ProjectRebound/actions/runs/34205921914) 11 个作业全部成功；见[实际作业回执](evidence/final-ci/ci-sanitized-receipt-34205921914.json)。未据此更换既有已部署镜像。 |
| 本机 r4 启动 | 45 项预检通过；生产 UI 渲染，观察到已有 Steam 关联会话，未强制重新执行登录挑战。 |
| 本机 PvE | 第一轮完成真实 Deploy 与 W 移动，但慢选角触发 `playable_pawn_timeout`。第二轮及时部署后，客户端明确记录 `Offline PvE travel reached Playable`，两类 readiness timeout 均为 0；服务器记录原生出生、占有与 StartMatch。第二轮 W 后截图已处于被击杀状态，不单独声称移动通过。见[第二轮回执](evidence/runtime-r4-fast/r4-fast-runtime-receipt.json)。 |
| 退出清理 | 游戏、服务器、Toolbox 正常关闭，最后目标进程数为 0。见[原始结构化运行回执](evidence/runtime-r4-after-acl/r4-after-acl-final-runtime-receipt.json)。 |
| 三机在线/Playable | `NOT_RUN`；当前构建原生能力门禁仍未验证。单机 PvE、机器人数量和 CI 不能替代多人原生对局证据。 |

早期 r3 和 ACL 修复前 r4 的实际失败，以及两次只读探测失败，均保留在证据链中。后来成功未覆盖这些历史记录。[运行证据字节核验](runtime-evidence-validation.json)核对了 25 份本地原始材料；截图和原始游戏日志留在本机，未纳入公共 CI 分支。

[超时复核](evidence/runtime-r4-after-acl/r4-timeout-review.json)确认 Payload 的出生等待期限为 90 秒；早一轮从 PvE 启动到 Deploy 为 215.844 秒，发生超时后清除 pending scope，后续游戏可操作也不会恢复 Payload readiness。及时部署复测的按钮到 Deploy 间隔为 110.6146 秒，但按钮不是原生计时起点；原始日志没有逐行时间戳，不能声称测得原生耗时小于 90 秒。该轮实际 Playable 日志、零超时与清理证据经[23 份原始材料核验](fast-runtime-evidence-validation.json)确认。慢选角限制仍存在，测试时应尽快完成选角和 Deploy；三机测试需单独记录是否发生同类超时。

[最新 CI 日志摘要](evidence/final-ci/ci-log-summary-34205921914.json)保留了原始日志哈希、一次 `DEPLOY_SOURCE_TEST_OK` 和五个镜像扫描成功的作业证据。日志没有可解析的漏洞数量摘要，因此只记录扫描步骤成功，不推断“零漏洞”。

## 三台机器如何开始

1. A 为房主，B、C 为成员；每台机器安装固定版本 Boundary，使用自己的 Steam 账号。先校验上方 ZIP 哈希并完整解压。
2. 退出游戏和 Toolbox，按包内 `README.zh-CN.md` 执行 `Install-StrictPayload.ps1` 和 `Check-ThisMachine.ps1`。缺少加载器或运行文件时按实际阻塞处理。
3. 各自正常登录 Toolbox，加入同一权威大厅并 ready。房主选择“采集原生准入证据”，使用本轮真实冻结名单、allocation 和 Grant。
4. 按[三机测试矩阵](TEST-MATRIX.zh-CN.md)记录两个唯一远端成员席位、各机最后阶段、错误和清理结果；完整清理后再开始新轮。
5. 仅回传包内收集器 JSON 与脱敏结果。平台身份、凭据、原始配置和备份不随结果分发。包不会自动上传。

采集成功不自动开启普通完整对局；出现 `native_admission_unverified` 时必须按实际记录。缺机器、依赖或服务条件写 `BLOCKED`，未执行写 `NOT_RUN`，不能以空 Token、关闭严格名单或控制台 `open` 补出通过结果。

[说明与源码复核](evidence/runbook-review/r4-runbook-source-review.json)是静态审计。采集按钮支持不同大厅人数，因此两人大厅也可以采集成功；三机项仍须人工核对本轮签名名单里 B、C 两个唯一 MEMBER 的回执，不能仅凭界面成功提示填写 PASS。

## 52 项记录与剩余边界

每项保留原始状态、依赖、改动提交及本轮证据：[后端/CI 增量](backend-ci-delta.json)、[客户端增量](client-native-delta.json)、[r4 修正](client-native-delta-r4.json)、[本次 Meta 与运行增量](runtime-meta-delta.json)。原始快照的 10 PASS / 30 PARTIAL / 12 BLOCKED 是历史范围；本轮未把全部 52 项改报通过。

当前只有本机具备可访问的真实执行环境。三机在线、完整对局、重连/迁移、下一局复用仍需要用户已能安排的三台机器采集证据；现有 HGH 节点 SSH 关闭、未更新，且不在本次 primary/gateway 部署矩阵内。
