# Authentication abuse runbook

Trigger on `AuthBindRateLimitSpike`, `RefreshTokenReplayDetected`, `MultiAccountRiskSpike`, or `InviteCodeFailureSpike`.

1. Record alert time, affected dimension, deployment digest, and request IDs; never copy credentials into the incident.
2. Inspect `/v1/admin/auth/risk-events` through the trusted internal admin endpoint. Correlate masked IP, SteamID, Device hash signal, batch, and failure code.
3. Revoke a compromised invite code or player sessions through the documented admin API. Refresh reuse has already revoked its token family.
4. Keep production limits in place. Apply a temporary upstream IP block only with an expiry and evidence that it will not block shared NAT users.
5. Confirm bind failure/limit rates return to baseline and active sessions do not grow unexpectedly.
6. Preserve sanitized audit rows and write the incident outcome. Rotate an exposed administrator token using the key-compromise runbook.
