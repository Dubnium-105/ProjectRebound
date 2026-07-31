# P2P BattleLog v3 Launcher 实现契约

<!-- bilingual-doc: chinese-only -->

## 安全边界

- Launcher 持有 Player Access/Refresh Token 和 `report_token`；不得写入日志、普通配置/状态文件、命令行或游戏进程环境。为支持 Launcher 崩溃恢复，`report_token` 唯一允许的落盘形式是 Windows Credential Manager，或由 Windows DPAPI `CurrentUser` 保护并绑定 Match、Capability 与服务端 Origin 的独立密文记录。
- DLL 只取得 `match_id`、`capability_id`、`server_nonce`、客户端版本与观察者类型。这些字段用于绑定证据，不是上传凭据。
- `report_token` 与玩家及 refresh-token family 绑定，因此正常 Access Token 刷新不会中断长对局；另一个设备的会话不能复用。
- 只读取 `*.json.ready`。`.tmp` 表示尚未封存，永不上传。

## 启动与上报状态机

1. 房主调用房间 `start` 后，每位玩家调用 `GET /v1/p2p-rooms/{room_id}/matches/active`。
2. 调用 `POST /v1/p2p-matches/{match_id}/report-capability`，把 `report_token` 保存在内存及上述 Windows 当前用户受保护凭据库；把其余上下文作为 DLL 文档所列环境变量传入游戏。
3. 首个 `.ready` 文件生成前只在本地跟踪 `CONNECTING`。从 DLL 文件取得 `timeline_session_id` 后，按当时真实状态发送第一次 Presence。每次状态变化递增 `presence_seq`；网络断开、重连、结果页、主动退出分别发送 `DISCONNECTED`、`ACTIVE`、`RESULT_SCREEN`、`EXIT_INTENT`。同一游戏进程保持相同 `timeline_session_id`，重启游戏后使用新文件中的值。
4. 监视 `%LOCALAPPDATA%\ProjectRebound\battlelog-dumps\**\*.json.ready`。先验证文件不超过服务端限制，且 v3 上下文与当前能力完全相同。
5. `report_id` 使用稳定值，例如 `r_<SHA256(文件内容)>`。以文件本体为请求体，携带 Bearer Access Token、`Content-Type: application/json` 和 `X-P2P-Report-Token`，调用 `PUT /v1/p2p-matches/{match_id}/reports/{report_id}`。
6. 收到 200 后把响应和文件摘要写入本地 ACK 索引，再把文件移到已上传目录；不要删除原始证据，直至满足保留策略。
7. 对局结束后优先上传 `FINAL`；如果进程提前结束且没有 `FINAL`，上传时间最新且上下文匹配的 `PARTIAL`，再发送 `LEFT`。`PARTIAL` 仅用于退出/重连审计，不计入结果法定人数。

## 重试规则

- 网络错误、408、429、5xx：保持同一 `report_id` 与文件内容，指数退避并加入抖动；退避上限建议 60 秒，但不得超过 `hard_expires_at`。
- Access Token 401：正常刷新一次，再以同一个报告能力重试。不要重新申请能力，因为新能力会生成新的 Capability ID/nonce，与已经封存的文件不符。
- `P2P_REPORT_TOKEN_INVALID`：停止自动重试并保留证据；这通常表示能力被主动轮换、会话族撤销或超过有效期。
- 409 的 ID/FINAL 冲突、422 的时间线/名单错误：停止自动重试，记录错误码、文件摘要和 Match ID；不要通过修改 JSON 绕过校验。
- 200 且 `duplicate=true` 视为成功 ACK。

## 哈希链字节契约

所有摘要均为小写十六进制 SHA-256：

```text
root = SHA256(match_id + "|" + capability_id + "|" + server_nonce + "|" + timeline_session_id)
payload_hash = SHA256(compact_json(payload))
event_hash = SHA256(lower(previous_event_hash) + "|" + seq + "|" + type + "|" + local_monotonic_ms + "|" + payload_hash)
```

`seq` 从 1 开始且连续；首事件必须为 `MATCH_STARTED`。`FINAL` 的末事件必须是唯一的 `MATCH_ENDED`，`events_digest` 必须等于最后一个 `event_hash`。

## 本地恢复

- Launcher 启动时先加载 ACK 索引，再扫描 `.ready`；已 ACK 的同摘要文件不重复排队。
- 如果本地存在未 ACK 的 `FINAL`，且同一 Windows 用户能从受保护凭据库恢复原 Capability，即使上次 Launcher 在上传中崩溃，也用原 `report_id` 重试。
- Capability 终止、硬过期或玩家登出后删除受保护凭据；无法解封原凭据时不得申请新 Capability 上传旧文件。
- 同一玩家只选择一份 `FINAL`。发现两份不同摘要的 `FINAL` 时停止自动选择并上报诊断，服务端不会接受第二份。
- 不允许从其他 Windows 用户目录、符号链接或不受信路径上传文件；扫描根目录应解析为固定的 `%LOCALAPPDATA%\ProjectRebound\battlelog-dumps`。
