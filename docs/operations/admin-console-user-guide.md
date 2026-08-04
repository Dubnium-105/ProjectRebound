# Admin Web operator guide

English | [简体中文](admin-console-user-guide.zh-CN.md)

Admin Web is for operations, support, release, infrastructure, and audit staff. It calls the Go Control Plane over HTTPS and never connects directly to PostgreSQL, Redis, game servers, or relay nodes.

## Sign in

Enter through the approved VPN or zero-trust proxy, submit administrator credentials after the Cloudflare Turnstile Managed interaction, and complete TOTP or a one-time recovery code. The access token stays only in page memory; the rotating refresh token is an `HttpOnly; Secure; SameSite=Strict` cookie. A Turnstile outage remains fail closed; follow the [Turnstile runbook](runbooks/admin-turnstile-login.md).

## Common tasks

### Players and risk

Search and filter players, change account status or VIP, revoke sessions, and resolve risk events from their resource pages. Every write needs a ticket-quality reason. Never put tokens, passwords, cookies, private network data, or game payloads in a reason or note.

### Invitations

- The creation wizard generates 1–100 codes and offers separate checkboxes for account creation, P2P room registration, Dedicated Server registration, and VNT node registration. Select only the capabilities intended for that batch.
- Invitation plaintext is a one-time response. Store or export it to an approved location before closing the result; the database retains only SHA-256.
- A player submits their code through ToolBox during Steam bind. Both a new-player bind and an existing-player bind consume one use when a code is supplied.
- Granted capabilities expire at the invitation's deadline. A code without an expiry grants non-expiring capabilities. Later edits, disablement, or revocation affect future uses only; an existing grant keeps the deadline and permission snapshot captured at redemption.
- Redeeming another qualifying code may extend an existing capability but cannot shorten it. Keep the batch expiry aligned with the intended access period rather than treating invitation expiry as only a redemption window.

### Online resources

Drain servers or relays before disruptive work. Connection relay migration never accepts an operator-supplied address; the backend scheduler selects an eligible `READY` node. Relay revoke requires fresh MFA step-up.

To enroll a Dedicated Server, use **Online / Dedicated Servers / Add server**. The operator needs `game_servers.register`, must complete a fresh MFA step-up, and must provide a stable instance ID, a 1–168 hour expiry, and an audit reason. The plaintext Registration Token is displayed once. Transfer it to only the matching server, confirm safe receipt before closing the dialog, and never put it in Git, logs, chat, or the reason field. Issuing another token for the same instance immediately revokes the previous unconsumed token. Continue with the [Dedicated Server registration guide](dedicated-server-registration.md).

The player-facing registration actions are gated independently: creating a P2P room needs `p2p_room_registration`, requesting a Dedicated Server Registration Token needs `game_server_registration`, and requesting a VNT node enrollment needs `vnt_node_registration`. Administrator RBAC permissions such as `game_servers.register` do not grant these player capabilities.

### Client releases

Create a `DRAFT`, run every manifest, server-side object `HEAD`, and file check, and publish only from `READY`. Publish and rollback require a reason and MFA; rollback preserves history. `DRAFT`, `READY`, and `ROLLED_BACK` releases can be archived with the rollback permission and MFA, while `PUBLISHED` must be rolled back first.

### Administrators, roles, and settings

New-administrator TOTP provisioning data and recovery codes are one-time responses. Administrator and role governance and settings changes require fresh MFA step-up.

The final active `SUPER_ADMIN` cannot be disabled or stripped of that role. The `SUPER_ADMIN` permission bundle is immutable.

## Audit and error escalation

Use operation audit filters and export only the current redacted result. Preserve the request ID, resource ID, operation, and local/UTC time when escalating an error. Use secure logout when finished and revoke other sessions after device loss or suspected cookie exposure.

## Sign out

Use secure logout when finished. Revoke other sessions after device loss or suspected cookie exposure; administrator disablement and MFA reset revoke all of that administrator’s sessions.
