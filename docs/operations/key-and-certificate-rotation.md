# Relay key rotation

English | [简体中文](key-and-certificate-rotation.zh-CN.md)

Relay Token uses an independent Ed25519 key set. The database only saves the public key, status and `env:` private key reference, but does not save the private key content. The active private key comes from `RELAY_TOKEN_PRIVATE_KEY_BASE64`; the private key to be rotated is provided by `RELAY_TOKEN_ROTATION_KEYS` injected into the Control Plane only:

```dotenv
RELAY_TOKEN_KEY_ID=relay-2026-07
RELAY_TOKEN_PRIVATE_KEY_BASE64=...
RELAY_TOKEN_ROTATION_KEYS=relay-2026-08=...;relay-2026-09=...
```

At startup, the current key record is `ACTIVE`, and the additional key record is `PENDING`. The Control Plane issues a Keyset containing the version, generation time, all public keys, signer, and Ed25519 signature. Edge only accepts updates signed by trusted keys and without version rollback, and reports `KeysetLoaded(keyset_version)` through the mTLS control flow.

After confirming that all `READY` Relay has confirmed the current staged Keyset, the administrator calls:

```http
POST /internal/v1/relay-signing-keys/{key_id}/activate
Authorization: Bearer <admin-token>
```

The new key becomes `ACTIVE`, and the old key becomes `VERIFY_ONLY`, and is retained at least until the end of the Relay Token maximum TTL. The new Keyset version is then pushed to all online nodes; the new Token uses the new `kid`, and the old Token continues to be verified during the grace period. Unknown `kid` Always rejects and does not bypass signatures.

Old private key material must not be removed from the environment during rotation; it cannot be removed from the running configuration and offline key inventory until all old tokens have expired and subsequent `RETIRED` operations have been completed. Private key backups must be separated from database backups and stored encrypted.

## Node mTLS certificate

Each registration and renewal will write the certificate serial, SHA-256 fingerprint, issuance/validity/expiration time and rotation time to `relay_node_credentials`. The renewal transaction will mark the old certificate as `ROTATED`, update the current fingerprint of the node, and then write the new credentials. Therefore, even if the old certificate can still pass the CA chain verification, it cannot pass the node binding check of the Control Plane.

Edge calculates the full validity period from certificate `NotBefore/NotAfter`, automatically generates a new Ed25519 private key and CSR when 25% remains, requests renewal through Node Token, atomically replaces `identity.json` with permission 0600, updates the Keyset cache and rebuilds the mTLS control connection. If the renewal fails but the old certificate still has time, it will back off and try again according to the control connection; after the certificate expires, a new control connection cannot be established.

Before production release, you must confirm that the image contains runtime renewal implementation, and check that each node `certificate_expires_at` has sufficient upgrade window. Early Edge images that only read certificates when the process starts cannot rely on restarting for certificate renewal: they should drain to zero allocation node by node, upgrade to an immutable image that supports online renewal, and then verify the new fingerprint, expiration time, new heartbeat and `READY`. A control plane restart will force Relay to re-handshake, which can be used as a post-release verification, but not as a certificate rotation mechanism.

When the administrator calls node `revoke`, the node enters irreversible `REVOKED`, all unrevoked credentials are recorded as `ADMIN_REVOKED`, the online control flow receives `Shutdown`, and the existing connection enters failover. Any subsequent reconnections using this Node Token, fingerprint, or certificate will be rejected.

Edge persists the certificate, Control Plane CA, last signed valid Keyset, and configuration. When the control flow is disconnected, the existing allocation and the unexpired Token of the known `kid` continue to work; after the default grace period of 600 seconds (`control_disconnect_grace_seconds`), the node locally enters `DRAINING` and stops accepting new allocations. Exit this protection state after the control connection is restored and the `READY` configuration snapshot is received.

Monitoring must display `control_connected`, heartbeat age, certificate expiration time and remaining validity period according to the dynamic node list, and alarm when the certificate has not been updated after entering the renewal window. Certificate expiration, identity revocation and ordinary network disconnection are different failures: only ordinary disconnection should wait for the built-in reconnection first; expiration or revocation must use a controlled new identity process. For continuous operation and recovery boundaries, see [Relay continuous online and recovery](relay-continuity.md).
