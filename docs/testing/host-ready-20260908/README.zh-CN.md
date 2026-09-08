[English](README.md) | 简体中文

# r6 实机准备测试记录 — 2026-09-08

本目录是在 r5、r4 记录上的追加记录，不是发布批准。r6 测试包已经构建；一台真实机器上的 r6 桌面回归已通过本轮准备/换队/离开流程。更新通道没有发布签名更新，因此版本检查不通过。

## 当前 r6 来源与测试包绑定

- Toolbox 源提交：3ff5ca2447621cfc018c3c8e65dce28705bd7cb9
- r6 测试执行父提交：995625903685b61770da6e8139731b50a4dfb0eb
- Payload 源提交：98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d
- Payload SHA-256：8d471662e947bcb6baf3b0380e23f90bef614f76ac9e7e8a82e2cfbf48619183
- r6 测试包：C:/wksp/ProjectRebound/artifacts/hardware-test-20260908-r6/rebound-hardware-test-20260908-r6-windows-x64.zip
- ZIP 大小：23,489,707 字节
- ZIP SHA-256：bc6e21d54d35d1408c609c042120ce47109e4e9968c1f4df34c5efc3841e3cd0
- 已签 Toolbox SHA-256：bef5cc75a95f741f70c69ef27281fcb13e44eed6cdbebec2448e5420cfc23058
- Backend schema：48；custom_protocol=true；lab_testing=false
- native_authority_admission_verified=false；release_ready=false

r6 前端测试是在创建 r6 源提交前、9956259 稳定源上对 5 个前端工作树文件执行的。执行回执记录 5 个被测源文件的精确哈希和父提交；3ff5ca 是测试后的来源绑定，不声称测试是在提交之后运行。r6 包、Backend 更新、build、sign、preflight 和桌面回执在 evidence/ 下。Backend 更新回执记录了旧无效发布清单的可逆归档，以及当前诚实的 UPDATE_NOT_FOUND/404 状态；不声称更新可用检查通过。实现提交映射：4af32fe 为 Ready/team，f5338a2 为可信身份，c156a44 为 mock/工具栏，a51fb44 为 scope，9e661ed 为前端回归，9956259 为运行时 DTO/CI，3ff5ca 为本轮 leave 修复。

## r6 产品增量

r6 修复了实机观察到的同 scope 先事件后确认顺序，并增加了过期响应和晚到事件的回归门禁。即使终态快照仍为 local.is_member=true，相同 scope 的离开确认也能正确处理；更新后的 session/lobby scope 不会被旧结果清除。校验仍绑定 session epoch 和大厅标识。

## r6 实际检查

| 范围 | 结果 | 证据与边界 |
|---|---|---|
| 前端 r6 行为测试 | PASS，55 项唯一测试：13 authoritative-room、15 bridge、27 UI | evidence/frontend-r6/execution-receipt.json 与 related-tests.log。较早的 55 项中间运行是重复记录，不计入总数。 |
| 前端生产构建 | PASS，exit 0 | evidence/frontend-r6/frontend-build.log |
| 站点准备 | PASS，exit 0 | evidence/frontend-r6/prepare-sites.log |
| r6 浏览器事件顺序检查 | PASS，仅 mock | evidence/browser-mock-r6/r6-leave-race-browser-receipt-v1.json；不代表 Backend/Native。 |
| Rust library 与 Tauri 测试 | 仅有 r5 证据；r6 未重跑 | r5 记录包含同一父源线的 316 个 Rust library 测试和 9 个 Tauri 测试，不改标为 r6 执行。 |
| r6 包构建/sign/preflight | 包字节和生产候选通过；release_ready 仍为 false | evidence/distribution-r6/package-build-receipt.json。包通过不等于原生游戏或发布通过。 |
| r6 实机桌面 UI | PASS（有界） | 一台真实签名可执行文件通过启动、建房、房主准备/取消准备、队伍 1 → 2 清准备、重新准备和同 scope 终态事件后的离开。版本检查为 NOT_PASSED_UNPUBLISHED_UPDATE_CHANNEL（404），不声称版本检查通过。见 evidence/desktop-r6/desktop-r6-receipt.json。 |
| 原生游戏启动 / 严格准入 | NOT_RUN | native_authority_admission_verified 仍为 false。 |
| 三机在线准入 | NOT_RUN | 没有 r6 三机结果。 |

r5 的离开失败仍作为历史证据保留，不是 r6 结果。r6 的 3ff5ca 源修复和有界桌面回执单独记录；r5 测试包、桌面回执和失败日志保留在原 evidence 路径，未被覆盖。r6 追加台账为 52-item-r6-append-delta.json。
## 历史 r5 测试包与检查

## 测试包与来源绑定

r5 便携测试包：

`C:/wksp/ProjectRebound/artifacts/hardware-test-20260908-r5/rebound-hardware-test-20260908-r5-windows-x64.zip`

- ZIP 大小：23,489,348 字节
- ZIP SHA-256：`77642aa56359133e2975cb8119215fe3052597dbd917188a477073129b38c072`
- Toolbox 源提交：`995625903685b61770da6e8139731b50a4dfb0eb`
- Payload 源提交：`98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`
- Backend 候选提交：`584e000baf024e381c5bdb3417ad7ac879bebdca`
- Backend 实现来源：`5e1953b525cc27e8e209abb877c938c4920c16a7`
- Backend schema：48
- Payload SHA-256：`8d471662e947bcb6baf3b0380e23f90bef614f76ac9e7e8a82e2cfbf48619183`
- 固定游戏 SHA-256：`181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`
- `custom_protocol=true`，`lab_testing=false`
- `native_authority_admission_verified=false`，`release_ready=false`

该包用于已有完整 Rebound 运行文件的测试机，不含游戏本体，也没有发布 updater。测试签名和包字节校验不等于生产签名发布。包清单、Payload 清单、SHA 清单、build/sign/package/preflight 回执和失败 build 回执均在[evidence](evidence/)中。

## r5 产品增量

UI 为房主和成员提供一致的显式“准备/取消准备”和换队操作。操作使用已认证的 `player_id`、当前权威名单 revision，以及服务端的 `local.can_set_ready` / `local.can_switch_team` 能力。换队由权威快照清除准备状态；客户端不会替其他席位本地准备。

Controller、Tauri DTO 与前端增加 session/lobby scope 和单调 `snapshot_sequence` 校验。同 revision 的动态快照按运行时代次排序；旧账号/旧大厅事件不能重新填充新大厅。这是组件和生命周期保护，不是原生准入证明。

## r5 实际检查

| 范围 | 结果 | 证据与边界 |
|---|---|---|
| 前端单元测试 | 74 项唯一测试通过：11 authoritative-room、14 bridge、7 downloads、27 UI、11 status、4 sites | [前端回执](evidence/frontend/r5-final-scope-receipt.json)与[字节核验](evidence/frontend/evidence-byte-validation.json)。额外 14 项取消准备 bridge 复跑是重复验证，不计入 74。 |
| 前端生产构建 | PASS，exit 0 | [前端 build 日志](evidence/frontend/build.log) |
| Rust library | 316 passed，0 failed，0 ignored | [Rust 回执](evidence/rust/r5-final-rust-receipt.json)与[原始 library 日志](evidence/rust/lib-316.log) |
| Tauri 测试 | 9 passed，0 failed，0 ignored；fmt check 通过 | [Rust 回执](evidence/rust/r5-final-rust-receipt.json)与[Tauri 原始日志](evidence/rust/tauri-9.log) |
| 浏览器 mock | 独立 `tauri-mock=1` Edge 上完整 App 准备/取消准备/换队流程通过 | [v3 回执](evidence/browser-mock/receipt-v3.json)与[字节索引](evidence/browser-mock/byte-index.json)。仅 mock；Backend、Steam、Payload 和多人验收均 NOT_RUN。v2 清洁源码前记录保留为历史证据。 |
| 真实桌面 UI | PARTIAL | r5 签名可执行文件在一台机器上通过启动、建房、房主准备/取消准备、队伍 1 → 2 → 准备 → 队伍 1，以及单人开局门禁。离开后后端列表已为空，但客户端仍报告 stale response，属于真实失败，留给 r6 修复；版本检查也失败；脱敏 r5 回执将诊断保留为待定；r6 Backend 更新回执记录了修复后的公开 404 未发布状态。不声称版本检查通过。见[脱敏桌面回执](evidence/desktop-r5/desktop-r5-receipt.json)。 |
| 包/build/sign/preflight | 包字节校验 PASS，最终 Tauri build PASS；仅测试签名 | [分发证据](evidence/distribution-r5/)。首次 wrapper build 和一次普通 build 失败回执均保留，没有覆盖。 |
| 原生游戏启动 | NOT_RUN | r5 桌面运行没有启动游戏。 |
| 三机在线准入 / Playable | NOT_RUN | 只使用了一台实机和一个 Steam 账号；`native_authority_admission_verified` 仍为 false。 |

离开大厅的失败不会被改写为通过。该 r5 失败由 r6 的 3ff5ca 源修复，r6 有界桌面回执另行记录，因此 r5 不是最终实机验收包。

## 52 项追加规则

[52-item-r5-append-delta.json](52-item-r5-append-delta.json) 包含全部 52 个 ID。每项复制 r4 冻结基线状态与 r4 增量引用；只有明确观察到的 r5 范围追加 Ready/team、session/lobby/sequence、IPC 组件证据、包来源和真实 leave 失败。其余均为 `NOT_REEVALUATED_R5`；组件、mock、CI 或单机结果都不会把原生在线验收改成通过。

r4 基线仍为 10 PASS、30 PARTIAL、12 BLOCKED，r5 追加不改写这些数字。

## 证据处理

这里只复制脱敏回执、字节索引、包清单以及单元/build 日志。原始桌面截图、无障碍树、游戏日志、浏览器 profile、Steam 身份材料、凭据和备份均留在 tracked evidence 之外。目录属性将 JSON 与 evidence 文件视作 binary，以保留记录字节。

严格按实际结果使用 `PASS`、`FAIL`、`BLOCKED`、`NOT_RUN`。准备/换队成功不等于离开成功、原生游戏登录成功、三机准入成功或 Playable 对局。
