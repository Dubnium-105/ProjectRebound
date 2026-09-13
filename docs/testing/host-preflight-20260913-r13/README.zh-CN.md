# r13：规范化空 scope 诊断并复核候选地址类别

[English](README.md) | 简体中文

r12 的多人原生结果保留为 `FAILED_USER_REPORTED`。本轮 r13 只记录源码契约、组件回归、后端只读聚合和测试包证据；原生多人入场、出生、控制、清理与第二局仍为 `NOT_RUN`，`release_ready=false`。

源码契约确认 Payload 的 `match.scope` 与 `match.playable_scope` 在未建立或清理后可以是 `{}`。r13 只把这两个被动诊断字段的 `{}` 或 `null` 解释为缺失；非空但缺字段或类型错误的对象继续严格拒绝。缺失 scope 永远不能生成 Playable 证明。见[原生契约](evidence/native/source-contract-receipt.json)。

原客户端把所有本机 IPv4 标为 LAN，后端却只接受私网 IPv4 的 LAN。r13 按现有 `LAN`、`IPV6`、`SRFLX` 规则分类，优先使用有效 STUN SRFLX；无有效 STUN 时保留有效本地候选，每类最多发布一个。后端聚合为 HOST:LAN=2、HOST:SRFLX=2、MEMBER:SRFLX=2，与该契约缺口相符，但原始拒绝帧及具体拒绝字段未采集，不能断言逐请求根因。公开证据省略身份和地址。见[承载契约](evidence/carrier/source-contract-receipt.json)和[角色聚合](evidence/backend/candidate-role-aggregate.json)。

旧解析器实际重现 1 项回归失败：`missing field attempt_id`。修复后 Rust 372 项、Tauri 9 项通过，失败和忽略均为 0，包含实际 Windows 命名管道组件回归；格式检查通过。控制器定向 23 项通过，属于全量测试的子集，不累加为新的覆盖数量。定向初次编译因不稳定 API 报 E0658，修正后通过，失败尝试保留在回执。见[修复前回归](evidence/regression-before/receipt.json)、[最终检查](evidence/rust-final/execution-receipt.json)和[定向检查](evidence/rust-targeted/controller-tests-receipt.json)。

后端只读回执显示 attempt=ABORTED、cleanup=CLEARED；候选诊断为 `NOT_OBSERVED_IN_TARGET_SCOPED_OR_WINDOW_GENERIC_MARKERS`。这是生命周期和聚合观察，不是逐请求因果证明。见[后端回执](evidence/backend/r13-attempt-evidence.json)。

Toolbox 提交为 `4cdefa78925d4e83a2cb8d1509c7baec16cf1473`；后端保持 `8069d5e1126b8a585610b232e721aee45c57ee88`，Payload 构建源保持 `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`。本轮未重部署后端，Payload 字节不变。

构建、测试签名、包字节检查、45 项文件预检和实际桌面启动通过。界面仍显示“版本: 检查失败”，单独记录为 `FAILED_DISPLAY_OBSERVED`，本轮未修复。打包辅助脚本首次读错回执字段，保留失败记录；修正后重跑成功，未改产品源码。Toolbox 该提交没有 CI 运行，记为 `NOT_RUN`；Main 文档提交的 CI 在推送后独立核对。见[桌面](evidence/desktop/ui-observation.json)、[分发](evidence/distribution)和[Toolbox CI](evidence/ci-toolbox.json)。

分发包 `rebound-hardware-test-20260913-r13-windows-x64.zip` 为 23,539,490 字节，SHA-256 `c91ac4ca14401863ba93f352d989dbb4d8e6de73c67d2bf167d38adbb4da0127`。签名 Toolbox 为 49,494,840 字节，SHA-256 `d1cc82942c702fe1152f19296218749ac15427530756b18b749ed896b21a8ebb`。测试证书不属于默认受信根，未安装根证书或导出私钥。

各机退出旧版、统一使用 r13 并新建大厅，全部成员（包括房主）准备后采集原生准入证据。本轮尚无三台独立 Steam 账号实机执行回执，不能将组件和桌面检查当成原生通过；严格名单、Token、原生 Grant 和 Playable 证明要求保持不变。

52 项以 r12 追加表为基线；本轮只标记 BP-001、009、011、017、018、019、023、047、050、051，其余保持 `NOT_REEVALUATED_R13`。BP-047 只有后端证据，未声明清理代码修改。见[52 项追加](52-item-r13-append-delta.json)和[源码绑定](evidence/committed-source-binding.json)。
