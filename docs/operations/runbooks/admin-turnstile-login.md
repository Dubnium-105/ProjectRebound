# Runbook: Admin Turnstile login failures

English | [简体中文](admin-turnstile-login.zh-CN.md)

## Trigger

Trigger on broad administrator login failure, rising `turnstile_verify_latency_ms`, Siteverify unavailable or hostname/action mismatch audit events, or loss of outbound HTTPS access to `challenges.cloudflare.com`.

Remain fail closed. Never restore access by bypassing Turnstile, trusting browser-only verification, logging tokens or secrets, borrowing another environment’s credentials, or broadening the production hostname allowlist.

## Initial classification

1. Confirm the admin origin, VPN/zero-trust proxy, and Control Plane health.
2. Use login audit records to compare non-secret error codes, hostname, action, latency, UTC time, and request ID.
3. Decide whether the failure affects one source, every administrator, or only one environment.

## Widget and proxy checks

Check the exact CSP script/frame allowance, Managed interaction-only widget action, token reset behavior, same-origin secure cookie deployment, and trusted-proxy client address handling.

## Control Plane checks

Verify Control Plane-only secret injection, expected hostname/action, DNS/time/CA health, outbound 443, bounded Siteverify timeout, and pre-Siteverify login limiting. Retry network/429/5xx failures once with the same idempotency key; never retry 4xx. Use Cloudflare test credentials only in an isolated non-production environment.

## Recovery

Repair configuration or egress. If compromise or invalidation is suspected, rotate the environment-specific Secret through the secret manager and roll the Control Plane. Complete a controlled Turnstile → password → TOTP login, verify the audit outcome and absence of secret logging, then observe failure rate and latency for at least 15 minutes.

If every administrator is locked out, use the pre-established backup administrator from an approved network; that does not authorize bypassing Turnstile. Roll back the recent proxy or Control Plane configuration if needed while retaining fail-closed behavior.

## Attack or credential compromise

Disable affected administrators, revoke sessions, rotate compromised credentials, preserve non-secret logs and the incident timeline, and document root cause, impact, verification, and prevention.
