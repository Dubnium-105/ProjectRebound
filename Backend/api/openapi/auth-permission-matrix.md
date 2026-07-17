# Access Token 权限矩阵

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
| 联机写操作 | 允许 | 拒绝 | 拒绝 |

Refresh Token 每次使用都会 rotation。旧 Token 对应的 session 行保留为已轮换状态；再次使用旧 Token 会撤销整个 `token_family_id` 下的 session，并记录 `REFRESH_TOKEN_REUSE` 审计事件。
