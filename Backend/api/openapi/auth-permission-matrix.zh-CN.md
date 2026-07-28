# Access Token 权限矩阵

[English](auth-permission-matrix.md) | 简体中文

`POST /v1/auth/bind` 接受客户端自述的 SteamID，当前 `auth_provider` 固定为 `steam_client_asserted`，`auth_level` 固定为 `unverified`。此流程不证明请求者控制对应 Steam 账号。

Access Token 是短期 Ed25519 JWT，只包含玩家 ID、session ID、provider、auth level、签发/过期时间和 token version。`account_status` 与 `is_vip` 不写入 Token；需要时始终从 PostgreSQL（或后续的短期 Redis 缓存）读取。

| 操作 | ACTIVE | BANNED | DELETED |
| --- | --- | --- | --- |
| bind | 允许 | 允许 | 拒绝 |
| refresh | 允许 | 允许 | 拒绝并撤销 session |
| logout | 允许 | 允许 | 已签发 Token 拒绝 |
| users/me | 允许 | 允许 | 拒绝 |
| 版本/更新读取 | 允许 | 允许 | 后续 Milestone 定义 |
| 公开服务器/房间浏览 | 允许 | 可允许 | 后续 Milestone 定义 |
| Meta 档案/内容读取 | 允许 | 拒绝 | 拒绝 |
| Meta 配装、Party、Gate 和匹配写操作 | 允许 | 拒绝 | 拒绝 |
| 联机写操作 | 允许 | 拒绝 | 拒绝 |

Admin API 不使用玩家矩阵，也绝不接受 Player Access Token。`/v1/admin/*` 人类管理接口要求可信来源网段，并使用只有在 Turnstile、密码和 TOTP/恢复码全部通过后才建立的独立管理员 Session。现有运维机器接口使用单独配置的静态 Admin Token；`/internal/v1/meta/*` Dedicated Server 路由改用绑定 server ID、有效期、scope、活动状态、已分配对局和名单的不透明 Game Server Token。两类凭据都不建立浏览器 Session。

## 人类管理员 RBAC

角色只是权限组合；后端 Handler 校验权限 Key，不把角色名直接当作授权判断。默认角色为 `SUPER_ADMIN`、`OPERATIONS`、`PLAYER_SUPPORT`、`RELEASE_MANAGER`、`INFRA_OPERATOR`、`AUDITOR` 和 `VIEWER`。

| 资源 | 读取 | 写入或生命周期权限 |
| --- | --- | --- |
| Dashboard | `dashboard.read` | — |
| 玩家 | `players.read` | `players.update_status`, `players.update_vip`, `players.revoke_sessions` |
| 风险 | `risk_events.read` | `risk_events.resolve` |
| 邀请码 | `invite_codes.read` | `invite_codes.create`, `invite_codes.update`, `invite_codes.revoke` |
| 房间 | `rooms.read` | `rooms.close`, `rooms.remove_member` |
| Dedicated Server | `game_servers.read` | `game_servers.drain`, `game_servers.disable` |
| 中继节点 | `relay_nodes.read` | `relay_nodes.drain`, `relay_nodes.resume`, `relay_nodes.revoke`, `relay_nodes.rotate_certificate` |
| Connection | `connections.read` | `connections.migrate`, `connections.close` |
| 客户端发布 | `updates.read` | `updates.create`, `updates.publish`, `updates.rollback`（也用于归档非发布版本） |
| 管理员 | `admins.read` | `admins.create`, `admins.update` |
| 角色 | — | `roles.manage` |
| 审计 | `audit_logs.read` | — |
| 设置 | `settings.read` | `settings.update` |
| MetaServer | `meta.read`, `meta.loadouts.read` | `meta.content.manage`, `meta.matches.manage`, `meta.loadouts.update` |

撤销中继节点、正式发布/回滚/归档、管理员创建/更新/MFA 重置、角色改权、系统设置更新及所有 MetaServer 管理写操作还必须提供与当前管理员 Session 绑定的短时 `X-Admin-Step-Up` 凭证。每个写操作必须填写原因并由后端写入审计。最后一个有效 `SUPER_ADMIN` 与 `SUPER_ADMIN` 权限集另有服务端不变量保护。

Refresh Token 每次使用都会 rotation。旧 Token 对应的 session 行保留为已轮换状态；再次使用旧 Token 会撤销整个 `token_family_id` 下的 session，并记录 `REFRESH_TOKEN_REUSE` 审计事件。
