# MetaServer 内部与管理 API

[English](metaserver-internal.md) | 简体中文

这些不是客户端 API。Dedicated Server 内部路由只允许通过私有 Meta origin；管理路由
还同时受反向代理和应用中间件的可信管理员网段限制。

## Dedicated Server 认证

每个请求同时发送：

```http
Authorization: Bearer gst_<opaque-token>
X-Game-Server-Id: <server-id>
```

数据库只保存 token 哈希。Token 必须未过期、与 header 的 server 一致、包含所需
Meta match scope，且服务器不能处于 DRAINING、UNHEALTHY 或 OFFLINE。有效凭据也不
提供全局玩家访问：Repository 会检查对局仍活动、确实分配给该服务器并包含目标玩家。

| 方法和路径 | 所需 scope | 行为 |
| --- | --- | --- |
| `GET /internal/v1/meta/matches/{match_id}/players/{player_id}/loadout` | `meta.loadouts.read` | 返回经 definitions 校验的对局配装快照 |
| `POST /internal/v1/meta/matches/{match_id}/players/{player_id}/connected` | `meta.matches.connect` | 仅标记该名单玩家连接；必要时启动 RESERVED 对局 |
| `POST /internal/v1/meta/matches/{match_id}/completed` | `meta.matches.complete` | 接收 `{"result":{...}}`，完成对局并将服务器释放为 READY |
| `PUT /internal/v1/meta/battlelog/reports/{report_id}` | `meta.battlelog.write` | 校验并幂等保存 schema v2 服务端快照，然后完成已关联对局 |

BattleLog 身份与完整性判断复用现有安全体系。对局预留时固化名单玩家的
`unverified`、`verified` 或 `trusted` 等级；只有 `verified` 和 `trusted`
名单成员可成为官方战绩参与者，原始上报不能提供或覆盖该等级。校验异常沿用
`LOW`、`MEDIUM`、`HIGH`、`CRITICAL` 严重度。

相同 `report_id` 与规范化 SHA-256 返回 `200`，可安全重试；相同 ID 对应不同
内容返回 `409`。快照缺少 match ID 时，后端自动关联该游戏服唯一的活动分配；
不存在活动分配时，记录作为非官方 standalone 证据保存。

404/403 语义会刻意避免泄露其他服务器的玩家和对局。遇到 DRAINING/OFFLINE 或分配不
匹配时，不得更换 player ID 重试。

## 管理员认证

所有 `/v1/admin/meta/*` 请求要求：

1. 来源位于配置的可信管理员 CIDR；
2. 当前有效的人类管理员 Access Token，玩家/static token 无效；
3. 端点对应权限；
4. 写操作提供绑定同一会话的 `X-Admin-Step-Up`；
5. `reason` 为 8–1000 字符且不含凭据。

成功写操作会在现有审计日志记录管理员、动作、目标、脱敏 old/new value、原因、
request ID、客户端地址、User-Agent、结果和时间。

| 方法和路径 | 权限 | Step-up | Body/结果 |
| --- | --- | --- | --- |
| `GET /v1/admin/meta/overview` | `meta.read` | 否 | 档案、活动 Party、队列 Ticket、活动对局计数 |
| `GET /v1/admin/meta/players/{player_id}/loadouts` | `meta.loadouts.read` | 否 | 玩家配装 |
| `PUT /v1/admin/meta/players/{player_id}/loadouts/{role_id}` | `meta.loadouts.update` | 是 | `snapshot`、当前 `revision`、`reason` |
| `GET /v1/admin/meta/matches` | `meta.read` | 否 | 最近 100 个对局 |
| `POST /v1/admin/meta/matches/{match_id}/cancel` | `meta.matches.manage` | 是 | `reason`；原子取消并释放预留 |
| `PUT /v1/admin/meta/playlists/{slug}` | `meta.content.manage` | 是 | 展示字段、mode、definition、enabled、顺序、原因 |
| `PUT /v1/admin/meta/notifications/{id}` | `meta.content.manage` | 是 | 本地化内容、时间窗、enabled、优先级、原因；ID `new` 表示创建 |

管理员配装更新与玩家接口使用相同的乐观 revision 规则。权限与 Step-up 在 Go 服务
内部执行，直接访问 Meta HTTP origin 也不能绕过。

## 指标

`GET /internal/metrics` 只向私有监控网暴露 Prometheus 文本。主要指标包括：

- HTTP 请求总数/延迟和 readiness；
- 原生 TCP 活动/总连接、RPC 数量/延迟、畸形帧；
- Gate 签发、消费和重放；
- 配装 revision 冲突；
- 匹配队列深度、分配结果和耗时；
- 按 PvE/PvP 类型、校验状态和幂等重试统计的 BattleLog 上报；
- Relay `0x59` QoS 请求、畸形包和限流计数。

Grafana 通过 `service="project-rebound-meta-server"` 动态发现服务；新增副本不应要求
硬编码 Dashboard panel。

## 导入工具

`meta-import` 是离线运维工具，不是 HTTP endpoint；生产服务不挂载旧 JSON。默认
执行 dry-run：

```bash
meta-import --source /secure/export --database-url "$DATABASE_URL"
meta-import --source /secure/export --database-url "$DATABASE_URL" --apply
```

Dry-run 校验 definitions，以当前 player ID 或 Steam ID 映射玩家，并输出冲突/错误
报告但不写入。`--apply` 使用一个 serializable 事务，只能在备份和报告审查后执行；
源目录始终保持只读。

权威字段、security scheme 与错误 envelope 见
[`openapi.yaml`](../../Backend/api/openapi/openapi.yaml)。
