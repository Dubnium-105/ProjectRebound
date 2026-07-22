# Access Token permission matrix

English | [简体中文](auth-permission-matrix.zh-CN.md)

`POST /v1/auth/bind` accepts the SteamID reported by the client. Currently, `auth_provider` is fixed to `steam_client_asserted` and `auth_level` is fixed to `unverified`. This process does not prove that the requester controls the corresponding Steam account.

The Access Token is a short-lived Ed25519 JWT containing only the player ID, session ID, provider, authentication level, issue/expiration times, and token version. `account_status` and `is_vip` are not written to the token; they are always read from PostgreSQL, or from a future short-lived Redis cache, when needed.

| Operation | ACTIVE | BANNED | DELETED |
| --- | --- | --- | --- |
| Bind | Allow | Allow | Reject |
| Refresh | Allow | Allow | Reject and revoke session |
| Logout | Allow | Allow | Issued token is rejected |
| `users/me` | Allow | Allow | Reject |
| Version/update reads | Allow | Allow | Defined by a later milestone |
| Public server/room browsing | Allow | Allow | Defined by a later milestone |
| Online write operations | Allow | Reject | Reject |

The Admin API does not use this matrix and does not accept Player Access Tokens. It requires a separate static Admin Token, a trusted source network, and exclusion from public Caddy routing.

Refresh Tokens rotate on every use. The session row corresponding to the old token remains in a rotated state; reusing the old token revokes every session in the same `token_family_id` and records a `REFRESH_TOKEN_REUSE` audit event.
