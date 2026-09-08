[English](TEST-MATRIX.md) | 简体中文

# 三台实机测试记录

本轮使用现有 schema 48 后端，通过测试包采集受管原生准入证据。`native_authority_admission_verified=false` 仍是独立构建门禁；采集不会修改它，也不会发布 Playable。三机执行无回执时保留 `NOT_RUN`。

## 第一轮执行步骤

1. A 为房主，B、C 为成员，各用一台实体 Windows 机器和自己的 Steam 会话。核对相同 package ID、ZIP SHA-256、固定游戏版本，安装后在游戏停止时执行 `Check-ThisMachine.ps1`。
2. 三人分别登录 Toolbox。A 创建权威 P2P 大厅，B、C 刷新列表并加入同一大厅的不同席位。如需换队，在权威战局卡片使用队伍按钮；当前队伍、已满队伍以及冻结后的名单不可切换。先完成换队，再确认准备。只选择界面实际提供的传输方式；当前未提供 VNT 时该场景记录 `BLOCKED: transport_unavailable`。
3. 全部人员加入后，每个人（包括房主）在权威战局卡片点击 **准备**，确认自己的状态为已准备。成员加入、离开或换队会重置准备状态，名单变更后每个人都需重新确认；不要用建房时的历史“owner ready”日志判断当前状态。A 等待三个真实席位就绪且 `local.can_start=true`，点击 **采集原生准入证据**。本轮不要点击普通“冻结清单并启动”。
4. 房主通过正常 API 冻结名单、取得签名 allocation、启动原生权威。成员按受管流程自动启动和连接。游戏提示平台登录时按 SPACE；记录原生登录是否完成，不能以 profile HTTP 成功代替。
5. 记录每台机器最后到达的阶段和错误。若出现 `P2P_AUTHORITY_LAUNCH_FAILED`，同时记录其后的具体原因、lobby/Attempt、operation/run 和时间；只有通用错误码仍不足以定位新故障。房主成功回执必须绑定本轮冻结名单及连接；三人场景须覆盖 B、C 两个唯一远端席位，同一玩家重连不能计为第二人。
6. 采集后由受管流程结束 Attempt 并清理游戏、连接和传输。确认清理完成后再开始新一轮。若报告 `cleanup_pending`，保留本轮关联和错误，不得记为通过或直接复用。
7. 每台机器执行 `Check-ThisMachine.ps1 -Collect`，证据目录放在包外。分别保存 `round_id/A`、`round_id/B`、`round_id/C` 的预检、收集 JSON 和下方结果。

“原生准入证据已采集”只表示本轮诊断回执成功。成员若随后遇到 `native_admission_unverified`，须单独记录；不能写成三人均可玩。只收到一个远端席位、超时或登录失败时如实记录，不补造席位、不修改回执。

## 场景与通过条件

| 场景 | 必需证据 | 本包初始状态 |
|---|---|---|
| 安装、版本、依赖 | 每台机器预检全部必需项符合包清单 | NOT_RUN |
| Toolbox 登录 | 独立 Steam 登录完成，大厅列表可读 | NOT_RUN |
| 游戏原生登录 | 当前包对应游戏进程原生登录完成 | NOT_RUN |
| 三机原生准入采集 | 本轮 B、C 唯一远端席位真实连接，且清理完成 | NOT_RUN |
| 第二次冷启动采集 | 退出本轮进程，新 round/Attempt 再完成采集与清理 | NOT_RUN |
| 两人、三人完整对局 | 每人选角、出生、可操作并具有 Playable 证据 | BLOCKED：构建原生能力尚未验证 |
| 冻结成员延后连接 | 同一冻结身份、队伍、席位合法接入，无额外席位 | BLOCKED：完整对局前置验收未完成 |
| 重连、路由迁移 | 新 generation 生效，旧连接终态，名单不变 | BLOCKED：完整对局前置验收未完成 |
| 主机退出、结束和下一局 | 旧权限失效，清理完成后才复用 | BLOCKED：完整对局前置验收未完成 |

原生错误 audience、过期、重放等拒绝负例由协调员另行组织，普通测试者不手改凭据。CI Docker/Relay 结果不能代替实体游戏验收。不关闭严格名单、不使用空 Token 或控制台 `open`，也不创建旧房间旁路。

## 每台机器的结果

保存 `match-result.json`，填写实际 package ID 和 UTC 时间。不透明关联字段可记录 lobby、Attempt、route generation、operation/run；省略平台 ID、账号名、IP 和凭据。

```json
{
  "round_id": "round-01",
  "machine": "A",
  "package_id": "从包清单读取",
  "scenario": "native_admission_collection_three_machines",
  "result": "NOT_RUN",
  "started_at_utc": null,
  "finished_at_utc": null,
  "last_successful_step": null,
  "observed_error_code": null,
  "unique_remote_seats_observed": null,
  "cleanup_result": "NOT_RUN",
  "playable_result": "NOT_RUN",
  "correlation": {},
  "notes": ""
}
```

结果限 `PASS`、`FAIL`、`BLOCKED`、`NOT_RUN`。未执行为 `NOT_RUN`；缺机器、依赖或服务条件为 `BLOCKED` 并说明；实际执行却不符合通过条件为 `FAIL`。仅部分阶段成功不得整项填 `PASS`。

只回传收集器 JSON 和脱敏观察，不发送原始客户端日志、Steam 资料、Token、Grant、Ticket、配置、备份 DLL 或数据库。脚本不自动上传。安装回执含本机路径，分享前排除该路径。
