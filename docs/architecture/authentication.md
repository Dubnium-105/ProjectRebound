# V1.1 authentication and sessions

English | [简体中文](authentication.zh-CN.md)

`POST /v1/auth/bind` remains a client-asserted SteamID bootstrap and does not prove Steam account ownership. V1.1 compensates with independent IP, SteamID, Device ID, and IP+Device limits; optional invite batches; append-only login/risk observations; rotating Refresh Tokens; family-wide reuse revocation; and user/admin session revocation. `device_id` is an untrusted risk signal and never grants identity.

Structured device fingerprints use `v1|uu:<16hex>|ds:<16hex>|cp:<16hex>`. The parser accepts the prior unversioned form and normalizes version, factor order, and hexadecimal case before hashing. PostgreSQL stores no raw SMBIOS, disk, CPU, or composite fingerprint text. `auth_device_fingerprints` contains a domain-separated HMAC-SHA-256 composite digest and independent factor digests, identified by a stable key ID; sessions and login/risk events reference that record. Independent factor indexes make future ban correlation possible even when one hardware factor changes. Legacy opaque Device IDs remain accepted and continue to use the existing SHA-256 session/risk hash and four-character display suffix, but cannot be split retroactively.

`DEVICE_FINGERPRINT_HMAC_KEY_BASE64` is an independent, long-lived secret containing at least 32 random bytes. Production refuses to start without it. The corresponding `DEVICE_FINGERPRINT_KEY_ID` is stored beside each digest. Because raw factor values are deliberately discarded, old rows cannot be recomputed after losing or changing this key; back it up and design an explicit multi-key transition before rotation. Do not reuse JWT, Relay, update-signing, database, or administrator secrets for this purpose.

The Access Token is a short-lived Ed25519 JWT bound to a database session. Refresh Tokens are opaque random secrets stored as hashes. A successful refresh atomically revokes the old token and issues a replacement in the same family. Reuse of any rotated token revokes the family, records a high-severity event, increments `auth_refresh_reuse_total`, and requires a new bind. Logout and session-management APIs revoke database state, so previously signed Access Tokens stop authenticating.

Invite codes are generated outside logs, stored as hashes, bounded by expiry and quota, and consumed in the same PostgreSQL transaction as player/session creation. Concurrent use cannot exceed `max_uses`. Administrator responses may return plaintext only once at creation; list/revoke responses do not.

Operational details:

- public request/response contracts: [external API](../api/external.md#32-authentication-and-players);
- permissions and credential boundaries: [auth matrix](../../Backend/api/openapi/auth-permission-matrix.md);
- risk/session/invite administration: [internal API](../api/internal.md);
- abuse response: [Auth abuse runbook](../operations/runbooks/auth-abuse.md).

Never log Authorization, Access/Refresh Tokens, invite plaintext, private keys, full Device IDs, device factor digests, or unmasked client IPs in administrative responses. Steamworks Auth Ticket verification is deliberately outside V1.1.
