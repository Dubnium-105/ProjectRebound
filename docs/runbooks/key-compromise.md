# Signing key, Relay CA, or node credential compromise

1. Identify the exact credential: administrator token, Access signer, Relay Token signer, Relay CA, node token/certificate, update signer, or backup age identity. Freeze related releases and preserve an audit trail without copying secret material.
2. For one Relay identity, call the node `revoke` API. It is irreversible: the control stream closes, credentials are marked revoked, and active connections migrate. Re-enroll with a new node identity.
3. For a Relay Token signer, stage a new key, wait for every READY node's Keyset acknowledgement, activate it, and keep the old public verification key through maximum Token TTL. If the private key is actively abused, drain/revoke affected allocations and shorten the incident window under operator approval.
4. Access/update signing compromise requires a new key ID and client/operator distribution plan. Relay CA compromise requires a new CA and re-enrollment of every node; ordinary leaf renewal is insufficient.
5. Rotate administrator and storage credentials, invalidate exposed CI secrets, audit logs/commits/artifacts, and verify old credentials are rejected. Update encrypted offline recovery copies and complete a recovery test.

Normal rotation details are in [key rotation](../key-rotation.md).
