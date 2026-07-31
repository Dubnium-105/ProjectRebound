# Access Token permission matrix

English | [简体中文](auth-permission-matrix.zh-CN.md)

`POST /v1/auth/bind` keeps legacy compatibility: omitting `encrypted_ticket` creates an `auth_provider=steam_client_asserted`, `auth_level=unverified` session. A valid Encrypted App Ticket creates an `auth_provider=steam_ticket`, `auth_level=verified`, `steam_verified=true` session. The decrypted ticket SteamID is authoritative. A supplied invalid ticket is rejected and never downgraded.

The bind response includes an integrity nonce for verified sessions. A valid ToolBox PE/ticket proof promotes only that database session, and the player identity, to `auth_level=trusted`. The promotion is one-way: an already-issued verified Access Token remains accepted until refresh, while an unverified token can never inherit trusted privileges. Three consecutive proof failures revoke the session.

The Access Token is a short-lived Ed25519 JWT containing the player/user ID, session ID, provider, authentication level, Steam verification flag, issue/expiration times, and token version. Authentication level is stored per session and preserved by refresh. `account_status` and `is_vip` are not written to the token; they are always read from PostgreSQL, or from a future short-lived Redis cache, when needed.

| Operation | ACTIVE | BANNED | DELETED |
| --- | --- | --- | --- |
| Bind | Allow | Allow | Reject |
| Refresh | Allow | Allow | Reject and revoke session |
| Logout | Allow | Allow | Issued token is rejected |
| `users/me` | Allow | Allow | Reject |
| Version/update reads | Allow | Allow | Defined by a later milestone |
| Public server/room browsing | Allow | Allow | Defined by a later milestone |
| Meta profile/content reads | Allow | Reject | Reject |
| Meta loadout, Party, Gate, and matchmaking writes | Allow | Reject | Reject |
| Online write operations | Allow | Reject | Reject |

Room, connection, MetaServer session, party, loadout, and matchmaking operations additionally require a session with `steam_verified=true` and `auth_level` of `verified` or `trusted`. Unverified legacy sessions retain bind, refresh, logout, personal/session management, public directory, and update-read compatibility.

The Admin API does not use the player matrix and never accepts Player Access Tokens. Human routes under `/v1/admin/*` require a trusted source network plus a dedicated administrator session created only after Turnstile, password, and TOTP/recovery-code verification. Existing operational machine routes use separately configured static Admin Tokens. Dedicated Server routes under `/internal/v1/meta/*` instead require an opaque Game Server Token bound to its server ID, expiry, scopes, active state, assigned match, and roster; neither credential creates a browser session.

## Human administrator RBAC

Roles are permission bundles. Backend handlers check permission keys; they do not treat a role name as authorization. The default bundles are `SUPER_ADMIN`, `OPERATIONS`, `PLAYER_SUPPORT`, `RELEASE_MANAGER`, `INFRA_OPERATOR`, `AUDITOR`, and `VIEWER`.

| Resource | Read | Write or lifecycle permissions |
| --- | --- | --- |
| Dashboard | `dashboard.read` | — |
| Players | `players.read` | `players.update_status`, `players.update_vip`, `players.revoke_sessions` |
| Risk | `risk_events.read` | `risk_events.resolve` |
| Invitations | `invite_codes.read` | `invite_codes.create`, `invite_codes.update`, `invite_codes.revoke` |
| Rooms | `rooms.read` | `rooms.close`, `rooms.remove_member` |
| Dedicated servers | `game_servers.read` | `game_servers.drain`, `game_servers.disable` |
| Relay nodes | `relay_nodes.read` | `relay_nodes.drain`, `relay_nodes.resume`, `relay_nodes.revoke`, `relay_nodes.rotate_certificate` |
| Connections | `connections.read` | `connections.migrate`, `connections.close` |
| Releases | `updates.read` | `updates.create`, `updates.publish`, `updates.rollback` (also archives non-published releases) |
| Administrators | `admins.read` | `admins.create`, `admins.update` |
| Roles | — | `roles.manage` |
| Audit | `audit_logs.read` | — |
| Settings | `settings.read` | `settings.update` |
| MetaServer | `meta.read`, `meta.loadouts.read`, `meta.battlelog.read`, `meta.battlelog.raw.read` | `meta.content.manage`, `meta.matches.manage`, `meta.loadouts.update`, `meta.battlelog.manage` |

Dedicated Server BattleLog submission is not an administrator permission. It
uses the machine-only `meta.battlelog.write` Game Server Token scope. Player
eligibility reuses the existing `unverified`, `verified`, and `trusted`
authentication levels captured when the roster is reserved; the raw game
snapshot cannot assert or promote an authentication level.

Relay revoke, release publish/rollback/archive, administrator create/update/MFA reset, role changes, settings changes, and every MetaServer administrative write also require a short-lived `X-Admin-Step-Up` proof bound to the current administrator session. Every write requires a reason and a backend audit record. The final active `SUPER_ADMIN` and the `SUPER_ADMIN` permission bundle have additional server-side invariants.

Refresh Tokens rotate on every use. The session row corresponding to the old token remains in a rotated state; reusing the old token revokes every session in the same `token_family_id` and records a `REFRESH_TOKEN_REUSE` audit event.
