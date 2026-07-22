# Backup and restore runbook

Use [PostgreSQL and key backup/restore](../backup-and-restore.md) for commands and retention policy.

1. Select an encrypted `.dump.age` and matching SHA-256 from off-host storage. Verify checksum, age decryption, and `pg_restore --list` before touching a target.
2. Restore only into a new isolated PostgreSQL instance. Restore signing/CA material from its separate encrypted recovery package.
3. Start the matching Control Plane image, apply forward migrations, and validate table counts, known players/admin access, an old signed Manifest, and Relay re-enrollment/reconnection.
4. Confirm old in-memory connections/allocations are not expected to recover. Keep the environment isolated until validation is signed off.
5. Record backup ID/hash, RPO, start/end/RTO, schema/image versions, row counts, failures, and operator in `docs/testing/v1.1/restore-test-report.md`. Publish the textfile success timestamp only after every required check passes.
