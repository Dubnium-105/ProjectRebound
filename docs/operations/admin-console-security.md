# Admin Web security guide

English | [简体中文](admin-console-security.zh-CN.md)

Production access is layered: VPN or zero-trust proxy, dedicated HTTPS admin origin, Cloudflare Turnstile, administrator password, TOTP/recovery code, backend RBAC, MFA step-up for high-risk operations, and backend audit. Turnstile reduces automated login abuse; it replaces none of the other layers.

## Security boundary

```text
VPN / zero-trust proxy
→ dedicated HTTPS admin origin
→ Cloudflare Turnstile
→ administrator password
→ TOTP / recovery code
→ backend RBAC
→ high-risk MFA step-up
→ backend audit
```

Admin Web is an unprivileged read-only static container on the edge network. It must never receive database, Redis, relay-control, signing, MFA-encryption, JWT, or Turnstile secret credentials. The Turnstile site key is public; the secret key is injected only into the Go Control Plane. Access and step-up tokens stay in page memory, while the rotating refresh token is an HttpOnly cookie.

## Secret boundary

Keep the Turnstile secret, MFA encryption key, administrator JWT key, database credentials, and signing keys only in Control Plane secrets. Passwords, TOTP values, and recovery codes exist only during input or one-time delivery. Never place them in localStorage, audit values, tickets, or chat.

## Administrator lifecycle

Use individual MFA-enabled administrator accounts, least-privilege roles, periodic reviews, immediate disablement on departure or device loss, and a second verified administrator for MFA resets. Keep at least two independently controlled `SUPER_ADMIN` accounts. Never put passwords, TOTP values, recovery codes, cookies, Authorization headers, Turnstile tokens, private keys, node identity files, or game payloads in reasons, audit values, tickets, or chat.

## High-risk operations

High-risk actions require a session-bound MFA step-up and a ticket-quality reason. Frontend button visibility is not an authorization boundary; backend permission checks remain authoritative. Audit-write failure must fail closed for critical writes.

## Logging and privacy

Audit retains actor, action, target, before/after values, reason, request ID, source summary, User-Agent, outcome, and time. Credential-like keys are recursively redacted.

## Periodic checks

Periodically verify Turnstile hostname/action restrictions, login limiter and alerts, active administrators and sessions, secret rotation, Caddy CSP/frame denial, frontend artifact secret scanning, and backup/restore coverage for administrator and audit tables. See the [Turnstile runbook](runbooks/admin-turnstile-login.md) for login incidents.
