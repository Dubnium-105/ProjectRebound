# Access Token 权限矩阵

[English](auth-permission-matrix.md) | 简体中文

`POST /v1/auth/bind` 保持旧客户端兼容：省略 `encrypted_ticket` 时创建 `auth_provider=steam_client_asserted`、`auth_level=unverified` 会话。有效 Encrypted App Ticket 创建 `auth_provider=steam_ticket`、`auth_level=verified`、`steam_verified=true` 会话，且以解密出的 ticket SteamID 为权威身份。提交无效 ticket 时直接拒绝，绝不降级。

verified 会话的 bind 响应会携带完整性 nonce。有效的 ToolBox PE/ticket proof 会把当前数据库会话以及玩家身份提升为 `auth_level=trusted`。该提升是单向的：此前签发的 verified Access Token 在 refresh 前仍可使用，但 unverified Token 绝不能继承 trusted 权限。连续三次 proof 失败会撤销会话。

Access Token 是短期 Ed25519 JWT，包含玩家/用户 ID、session ID、provider、auth level、Steam 验证标记、签发/过期时间和 token version。认证等级按会话保存并由 refresh 继承。`account_status` 与 `is_vip` 不写入 Token；需要时始终从 PostgreSQL（或后续的短期 Redis 缓存）读取。

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

房间、连接、MetaServer session、Party、配装和匹配操作还要求会话满足 `steam_verified=true`，且 `auth_level` 为 `verified` 或 `trusted`。旧客户端的 unverified 会话仍可 bind、refresh、logout、管理个人/会话、读取公共目录和更新信息。

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
