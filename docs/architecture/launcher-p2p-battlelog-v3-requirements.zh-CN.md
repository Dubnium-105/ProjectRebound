# Launcher 开发转交清单：P2P BattleLog v3

<!-- bilingual-doc: chinese-only -->

状态：首版实现需求  
适用范围：玩家 Launcher，不适用于 Dedicated Server Launcher  
服务端契约：`Backend/api/openapi/openapi.yaml`  
DLL 契约：`Payload/BattleLog/README.zh-CN.md`  
安全补充：`docs/architecture/p2p-battlelog-launcher-contract.zh-CN.md`

## 1. 目标与边界

Launcher 负责取得服务端创建的 P2P Match 上下文、保管上传凭据、向游戏进程传入非秘密上下文、监控 DLL 封存的 v3 文件，并幂等上传战绩证据。

首版不得：

- 根据本地时间、房间名或游戏内 `AssignedMatchID` 自行生成 P2P Match ID。
- 修改 DLL 生成的 JSON、补写玩家数据、重算统计数据或把 `PARTIAL` 改成 `FINAL`。
- 把 `report_token`、Player Access/Refresh Token 写入命令行、环境变量、日志、崩溃转储或战绩文件。
- 将 P2P 报告上传到 Dedicated Server 的 `/internal/v1/meta/battlelog/...` 接口。
- 将服务端返回的 `SELF_REPORTED` 或 `INCOMPLETE` 在客户端显示为交叉验证通过。
- 因房主离开而在客户端直接判定胜负；最终状态以服务端 `/result` 为准。

## 2. 必须实现的状态机

```text
NO_MATCH
  -> DISCOVER_MATCH
  -> ISSUE_CAPABILITY
  -> LAUNCH_WITH_CONTEXT
  -> CONNECTING
  -> ACTIVE
  -> RESULT_SCREEN | EXITING | DISCONNECTED
  -> UPLOAD_FINAL | UPLOAD_LATEST_PARTIAL
  -> WAIT_SERVER_DECISION
  -> ACKNOWLEDGED
```

要求：

- [ ] 状态机、队列和 ACK 持久化不得包含任何 Bearer Token 或明文 `report_token`；崩溃恢复所需的 `report_token` 只能进入 Windows 当前用户绑定的受保护凭据库。
- [ ] 同一 `match_id` 同时只能有一个上传协调器。
- [ ] 游戏重启或重连可以产生新的 `timeline_session_id`，但必须继续使用同一个服务端 `match_id`。
- [ ] 每次 Presence 更新使用单调递增的 `presence_seq`；不得复用其他 Match 的序号。
- [ ] Access Token 正常刷新后继续使用原 Capability，不重新申请。
- [ ] 只有在启动游戏前且尚未产生 v3 文件时，才允许主动轮换 Capability。

## 3. API 调用顺序

所有玩家接口均要求：

```http
Authorization: Bearer <player-access-token>
```

该 Access Token 必须属于 Active、Steam 已验证的玩家。

### 3.1 获取活动 Match

房主成功调用房间 `start` 后，每位房间成员调用：

```http
GET /v1/p2p-rooms/{room_id}/matches/active
```

Launcher 必须保存并校验：

- [ ] `match_id`
- [ ] `room_id`
- [ ] `state`
- [ ] `match_type`
- [ ] `roster_revision`
- [ ] `expected_reporter_count`
- [ ] `policy_version`
- [ ] `hard_expires_at`

处理规则：

- 404：Match 尚未创建或玩家不属于冻结名单；短暂退避重试，不得本地创建 ID。
- 403：停止启动带战绩上报的对局并提示重新认证/重新加入房间。
- `state` 已是终态：不得申请新 Capability；仅恢复本地未完成 ACK。

### 3.2 申请报告 Capability

```http
POST /v1/p2p-matches/{match_id}/report-capability
```

成功响应包含：

- `capability_id`
- `report_token`
- `server_nonce`
- `expires_at`

要求：

- [ ] `report_token` 平时只存在于 Launcher 进程内存；为支持 Launcher 崩溃恢复，唯一允许的落盘形式是 Windows Credential Manager，或使用 Windows DPAPI `CurrentUser` 保护的独立密文记录。
- [ ] 不得把明文或可逆的自制加密 Token 写入本地配置、注册表、日志、Telemetry、ACK/队列文件或 DLL 环境；不得把加密密钥与密文一同保存。
- [ ] 受保护凭据必须同时绑定 `match_id`、`capability_id`、当前 Windows 用户和服务端 Origin，并在服务端终态、硬过期或用户登出后删除。
- [ ] `capability_id` 与 `server_nonce` 可以进入游戏环境和本地 ACK 元数据。
- [ ] 申请新 Capability 会撤销此前同玩家、同 Match 的 Capability；生成 v3 文件后禁止随意轮换。
- [ ] Capability 与玩家及 Refresh Token Family 绑定，正常 Access Token 刷新不要求重新申请。

### 3.3 Presence 上报

```http
PUT /v1/p2p-matches/{match_id}/presence/me
Content-Type: application/json
```

请求体：

```json
{
  "presence_seq": 1,
  "status": "CONNECTING",
  "timeline_session_id": "p2tl_...",
  "last_checkpoint_seq": 0,
  "game_process_alive": true,
  "game_connected": false
}
```

状态映射：

| Launcher/游戏事件 | `status` | `game_process_alive` | `game_connected` |
| --- | --- | ---: | ---: |
| 游戏已启动，正在连接 | `CONNECTING` | true | false |
| 已进入对局 | `ACTIVE` | true | true |
| 网络中断，进程仍在 | `DISCONNECTED` | true | false |
| 重连成功 | `ACTIVE` | true | true |
| 已显示结果页 | `RESULT_SCREEN` | true | 视实际连接 |
| 玩家主动退出意图 | `EXIT_INTENT` | true | 视实际连接 |
| 游戏进程已经结束 | `LEFT` | false | false |

要求：

- [ ] `timeline_session_id` 必须取自对应 v3 `.ready` 文件，Launcher 不得自行生成。
- [ ] 首个 `.ready` 文件出现前只维护本地 `CONNECTING` 状态，暂不调用 Presence API；读取到 Timeline Session 后再按当前真实状态发送第一次 Presence。
- [ ] `last_checkpoint_seq` 使用当前已观察到的 DLL 时间线末序号，不是 Presence 序号。
- [ ] 同一状态的网络重试使用同一个 `presence_seq`；只有产生新的逻辑状态更新时才递增。
- [ ] 收到 `was_duplicate=true` 视为成功。
- [ ] 新游戏进程或新的时间线会由服务端创建 Reconnect Segment；单局最多允许 32 段。
- [ ] 单个玩家离开不得让 Launcher 宣布整局结束；全员结果页/离开或房间关闭会由服务端开启收集截止窗口。

### 3.4 上传报告

```http
PUT /v1/p2p-matches/{match_id}/reports/{report_id}
Authorization: Bearer <player-access-token>
X-P2P-Report-Token: <report-token>
Content-Type: application/json

<DLL 生成的完整 v3 JSON，不能再包一层 snapshot>
```

`report_id` 必须稳定且满足：

```text
^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$
```

推荐：

```text
r_<SHA256(原始 .ready 文件字节) 的小写十六进制>
```

要求：

- [ ] 相同文件的所有重试使用同一个 `report_id`。
- [ ] 请求体直接发送文件内容，不得反序列化后重新序列化。
- [ ] 每位玩家、每个 Match 只能有一份不可变 `FINAL`。
- [ ] 200 且 `duplicate=true` 视为成功 ACK。
- [ ] `QUARANTINED` 表示证据已保存但不参与正常共识，不得自动伪装为成功验证。
- [ ] 上传成功后记录服务端响应、文件 SHA-256、Match ID 和 ACK 时间，但不记录 Token。

### 3.5 查询服务端裁决

```http
GET /v1/p2p-matches/{match_id}/result
```

Launcher/UI 必须区分：

| 状态 | 含义 |
| --- | --- |
| `STARTING` / `RUNNING` | 对局进行中 |
| `COLLECTING` | 等待其他玩家报告或截止时间 |
| `PEER_CONFIRMED` | 达到交叉验证法定人数和队伍覆盖 |
| `SELF_REPORTED` | 仅有单方证据，不能视为 Peer Confirmed |
| `DISPUTED` | 终局数据互相冲突 |
| `INCOMPLETE` | 报告、法定人数或队伍覆盖不足 |
| `ABORTED` | 对局被服务端判定中止 |
| `EXPIRED` | 硬过期前没有形成有效结果 |

要求：

- [ ] UI 同时显示 `trust_tier`、`risk_severity`、`reasons`。
- [ ] 玩家个人数据必须显示 `stats_status`：`CONSENSUS`、`SELF_ONLY`、`UNVERIFIED` 或 `CONFLICTED`。
- [ ] `CONFLICTED` 或 `UNVERIFIED` 数据不得进入本地排行榜、奖励或成就计算。
- [ ] Shadow Mode 阶段所有结果只用于观察和诊断。

## 4. DLL 启动上下文

Launcher 必须在创建游戏进程时设置：

```text
PROJECT_REBOUND_P2P_MATCH_ID=<match_id>
PROJECT_REBOUND_P2P_CAPABILITY_ID=<capability_id>
PROJECT_REBOUND_P2P_SERVER_NONCE=<server_nonce>
PROJECT_REBOUND_CLIENT_VERSION=<launcher/client version>
PROJECT_REBOUND_P2P_AUTHORITY_KIND=CLIENT_OBSERVER
```

房主监听服务器使用：

```text
PROJECT_REBOUND_P2P_AUTHORITY_KIND=LISTEN_HOST_OBSERVER
```

要求：

- [ ] 不设置 `report_token`、Access Token 或 Refresh Token 环境变量。
- [ ] 不通过游戏命令行传递任何 P2P 上下文，避免被普通进程列表读取。
- [ ] Match、Capability、Nonce 必须来自同一次服务端 Capability 响应。
- [ ] 上下文只对本次游戏子进程生效，不修改用户级或系统级环境变量。
- [ ] Dedicated Server 启动流程不得设置这些玩家 P2P 环境变量。

## 5. 文件监控与本地队列

扫描根目录固定为：

```text
%LOCALAPPDATA%\ProjectRebound\battlelog-dumps
```

DLL 输出：

```text
<pve|pvp|unknown>\<timestamp>_..._<trigger>.json.ready
```

要求：

- [ ] 只处理 `*.json.ready`；忽略 `.tmp` 和普通 `.json`。
- [ ] 拒绝解析后逃出固定扫描根目录的符号链接、Junction 或重解析点。
- [ ] 文件大小必须大于 0 且不超过服务端配置上限，当前默认 524288 字节。
- [ ] 打开文件时使用只读共享模式，并在读取前后确认长度、最后写入时间未变化。
- [ ] 本地预检 `schema == project-rebound.p2p-battlelog.raw` 且 `schema_version == 3`。
- [ ] 本地预检 `p2p_match_id`、`capability_id`、`server_nonce` 与当前上下文逐字相同。
- [ ] 本地预检 `report_completeness` 仅为 `PARTIAL` 或 `FINAL`。
- [ ] 本地预检失败时隔离文件并记录无敏感数据的诊断，不尝试修复 JSON。

上传选择规则：

- [ ] 出现 `FINAL` 时立即优先上传该文件。
- [ ] 多个 `PARTIAL` 只保留时间线序号最大的一个作为恢复候选，默认不逐轮上传全部检查点。
- [ ] 游戏进程结束且没有 `FINAL` 时，上传最新的有效 `PARTIAL`。
- [ ] 发现两份不同 SHA-256 的 `FINAL` 时停止自动选择，保留两份证据并上报诊断。
- [ ] 已 ACK 文件可移至 `uploaded` 子目录；不得立刻永久删除。
- [ ] 建议原始证据保留 7 天或由服务端下发策略控制，并设置总磁盘配额。

建议的非秘密 ACK 索引字段：

```json
{
  "match_id": "p2pm_...",
  "capability_id": "p2rc_...",
  "report_id": "r_...",
  "file_sha256": "...",
  "completeness": "FINAL",
  "server_validation_status": "ACCEPTED",
  "acknowledged_at": "2026-08-01T00:00:00Z"
}
```

## 6. 提前离开、崩溃和重连

### 玩家中途主动退出

- [ ] 退出确认后先发送 `EXIT_INTENT`。
- [ ] 给 DLL/文件监控一个短暂封存窗口，但不得为了等待文件无限阻止用户退出。
- [ ] 进程结束后发送 `LEFT`。
- [ ] 没有 `FINAL` 时上传最新 `PARTIAL`。

### 游戏崩溃

- [ ] Launcher 检测异常退出，发送 `LEFT`，但本地原因标记为 crash。
- [ ] 扫描并上传最后一个已原子封存的 `PARTIAL`。
- [ ] 不上传仍为 `.tmp` 的文件。

### Launcher 自身崩溃

- [ ] 下次启动先恢复 ACK 索引、`.ready` 队列和当前用户受保护的 Capability 凭据，再开始新的 Match。
- [ ] 成功解封原 Capability 凭据后，未 ACK 的报告保持原 `report_id` 重试。
- [ ] 无法解封 `report_token` 时不得申请新 Capability 上传旧文件；保留证据和非秘密诊断状态，并明确提示该报告无法自动补传。
- [ ] 已产生文件后不得通过申请新 Capability 尝试上传旧文件。

### 网络断开与重连

- [ ] 发送 `DISCONNECTED`，保留 Match/Capability。
- [ ] 同一游戏进程重连后发送新的 `ACTIVE` Presence。
- [ ] 游戏进程重启会产生新 `timeline_session_id`，服务端记录新 Reconnect Segment。
- [ ] 不得把两个 Timeline 的 JSON 合并成一份报告。

### 房主提前离开

- [ ] 房主 Launcher 按普通玩家规则上传可用的 FINAL/PARTIAL 并发送 `LEFT`。
- [ ] 其他玩家继续上传自身观察结果。
- [ ] 不在客户端选举“权威报告”；服务端根据法定人数、PVP 队伍覆盖和截止窗口裁决。
- [ ] 房间关闭后进入服务端收集窗口：服务端终态前可提交新报告；终态后只能接受服务端明确支持的相同内容幂等重试，不得一直重试到 `hard_expires_at`。

## 7. 重试与错误处理矩阵

| 场景 | Launcher 行为 |
| --- | --- |
| DNS、连接超时、网络中断 | 保持同一文件和 `report_id`，指数退避加抖动 |
| HTTP 408、429、5xx | 可重试；遵守 `Retry-After`，最大退避建议 60 秒 |
| Access Token 401 | 正常 Refresh 一次，再用原 Capability 重试 |
| `P2P_REPORT_TOKEN_INVALID` | 停止自动重试并保留证据；不得申请新 Capability 上传旧文件 |
| `P2P_MATCH_EXPIRED` | 标记永久失败，保留诊断元数据 |
| `P2P_REPORT_ID_CONFLICT` | 永久失败；说明同一 ID 对应了不同字节 |
| `P2P_FINAL_REPORT_CONFLICT` | 永久失败；保留冲突的两份 FINAL |
| `P2P_MATCH_FINALIZED` | 查询 `/result`；仅相同内容的幂等 ACK 可以视为完成 |
| HTTP 413 | 永久失败并隔离；不得截断 JSON |
| HTTP 415 | 修正请求 Header，不修改文件 |
| HTTP 422 | 永久隔离并记录服务端错误码，不修改或重签报告 |
| HTTP 403 | 停止上报并要求重新认证/检查冻结名单 |

重试不得超过 `hard_expires_at`。所有重试任务必须可取消，并在退出 Launcher 时安全持久化非秘密队列状态。

## 8. 安全要求

- [ ] 所有公网请求仅连接配置的 Project Rebound HTTPS Origin。
- [ ] 禁止自动跟随到不同 Origin 的 30x 重定向并继续携带 Authorization/Header Token。
- [ ] 校验证书链和主机名；不得提供“忽略 TLS 错误”的生产选项。
- [ ] HTTP 日志必须过滤 `Authorization`、`X-P2P-Report-Token`、Cookie 和完整请求体。
- [ ] 崩溃上报必须对 Token、Nonce、SteamID、玩家名和本地绝对路径做分级脱敏。
- [ ] `report_token` 生命周期结束后及时清零可控内存、删除 Windows 受保护凭据并释放引用。
- [ ] 禁止用跨 Windows 用户、跨设备同步或随安装包漫游的凭据存储保存 `report_token`。
- [ ] 不加载扫描根目录外的文件；不接受网页、插件或 IPC 任意指定上传路径。
- [ ] 不允许另一 Windows 用户会话控制当前上传队列。
- [ ] 本地 ACK/队列文件采用当前用户 ACL；不得给予 Everyone 写权限。
- [ ] 不把 P2P Match 结果用于客户端可篡改的奖励结算。

## 9. Telemetry 与日志

允许记录：

- Match ID 的短后缀或不可逆摘要。
- 报告状态、HTTP 状态、服务端错误码、重试次数和延迟。
- 文件大小、事件数量、Completeness、Validation Status。
- Presence 状态迁移和 Reconnect Segment 数量。

禁止记录：

- Access Token、Refresh Token、`report_token`。
- 完整原始 v3 JSON。
- `server_nonce` 全值。
- 未脱敏 SteamID、玩家名、IP 和本地用户目录。

建议指标：

- `p2p_battlelog_capability_issue_total`
- `p2p_battlelog_ready_file_total{completeness}`
- `p2p_battlelog_upload_total{result}`
- `p2p_battlelog_upload_retry_total{reason}`
- `p2p_battlelog_upload_latency_ms`
- `p2p_battlelog_pending_files`
- `p2p_battlelog_quarantined_local_total{reason}`
- `p2p_presence_update_total{status,result}`

## 10. 验收测试

### 正常流程

- [ ] 单人 PVE：FINAL 上传成功，最终状态为 `SELF_REPORTED`。
- [ ] 双人 PVE：两份一致报告按服务端策略形成交叉确认。
- [ ] 1v1 PVP：必须收到双方一致报告且覆盖双方队伍才可 `PEER_CONFIRMED`。
- [ ] 多人 PVP：满足 2/3 法定人数且覆盖所有人类队伍。
- [ ] 相同报告网络重试返回 `duplicate=true`，本地只形成一个 ACK。
- [ ] Access Token 在对局中刷新，原 Capability 仍可上传。

### 退出与恢复

- [ ] 玩家中途退出：发送 EXIT/LEFT，并上传最新 PARTIAL。
- [ ] 房主中途退出：其他玩家仍可完成上传并查询服务端裁决。
- [ ] 游戏崩溃：不读取 `.tmp`，只上传最后一个 `.ready`。
- [ ] Launcher 在上传请求发送后、收到响应前崩溃：同一 Windows 用户重启后从受保护凭据库恢复原 Capability，以同一 ID 重试并得到幂等结果。
- [ ] 复制受保护凭据到另一 Windows 用户或设备后无法解封或使用。
- [ ] 网络断开后同进程重连：Presence 序号递增，不新建 Timeline。
- [ ] 游戏进程重启后重连：使用新 Timeline Session，服务端生成新 Presence Segment。

### 冲突与攻击面

- [ ] 修改 v3 任一 Timeline Payload 后上传，服务端返回 422。
- [ ] 使用其他玩家的 `report_token`，服务端拒绝。
- [ ] 使用同玩家其他设备会话和窃取的 Token 组合，服务端拒绝会话族不匹配。
- [ ] 同一 `report_id` 上传不同内容，服务端返回冲突。
- [ ] 同一玩家上传第二份不同 FINAL，服务端返回冲突。
- [ ] 伪造名单外真人 SteamID，报告被隔离。
- [ ] 超过 512 KiB、事件超限或非法 Schema 的文件不会被 Launcher 自动修改后重试。
- [ ] 30x 跳转到非配置 Origin 时不会泄露 Authorization 或报告 Token。
- [ ] 日志与崩溃报告扫描确认没有任何 Token。

## 11. 交付物

Launcher 团队需要提交：

- [ ] Match/Capability API 客户端。
- [ ] Presence 状态机及持久化的单调序号管理。
- [ ] 子进程级环境变量注入，不污染用户/系统环境。
- [ ] `.json.ready` 安全文件监控与恢复队列。
- [ ] FINAL 优先、最新 PARTIAL 回退的选择器。
- [ ] 幂等上传器、Access Token 刷新和错误矩阵实现。
- [ ] 基于 Windows Credential Manager 或 DPAPI `CurrentUser` 的 Capability 凭据库及完整的创建、恢复、过期和删除生命周期。
- [ ] 非秘密 ACK 索引及磁盘配额/保留策略。
- [ ] `/result` 状态及 `stats_status` 的 UI 映射。
- [ ] Token/PII 日志过滤测试。
- [ ] 本文第 10 节的自动化测试报告。
- [ ] 一份端到端 Shadow Mode 测试产物：两名玩家的本地 ACK、服务端 Match ID、最终裁决和脱敏日志。

## 12. 完成定义

只有同时满足以下条件才算完成：

- 全部 MUST/勾选项实现并通过测试。
- Launcher 源码、安装包和自动更新流程均未包含固定 Token 或测试凭据。
- Dedicated Server BattleLog 流程无回归。
- P2P 与 Dedicated Server 报告不会进入对方接口或本地队列。
- Shadow Mode 端到端验证至少覆盖正常结束、玩家退出、房主退出、断网重连和冲突报告。
- 安全评审确认游戏进程内不存在 `report_token`，HTTP/崩溃日志不存在凭据泄露。
