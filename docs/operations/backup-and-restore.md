# PostgreSQL and key backup and recovery

English | [简体中文](backup-and-restore.zh-CN.md)

## Daily database backup

Install the PostgreSQL client, `age`, and optionally `rclone` that matches the server's major version. The backup script first generates a custom-format compressed dump, verifies it with `pg_restore --list`, then encrypts it with the age public key and generates SHA-256. Reject backup when no encryption recipient is configured.

```bash
export DATABASE_URL='postgres://...'
export BACKUP_DIRECTORY=/srv/projectrebound-backups/postgres
export BACKUP_ENCRYPTION_RECIPIENT='age1...'
export BACKUP_RETENTION_DAYS=14
export BACKUP_RCLONE_REMOTE='remote:projectrebound/postgres' # 可选异机/对象存储
scripts/backup/postgres-backup.sh
```

systemd timer/cron is called daily; weekly backups are copied to the 8-week preservation prefix, and monthly backups are copied to the 12-month preservation prefix. Monitor `BACKUP_OK` time, file size, verification results and remote object existence. The private key, CA, Access/Relay/Manifest signing key and administrator recovery credentials must be backed up at least twice offline using another set of age receivers, and at least one copy is not on the control plane host; Git or database dump is not allowed.

## Verification and new environment recovery

```bash
export BACKUP_AGE_IDENTITY_FILE=/secure/offline/backup-age-key.txt
scripts/backup/verify-backup.sh /srv/.../projectrebound-....dump.age

export DATABASE_URL='postgres://.../empty_projectrebound?sslmode=require'
export RESTORE_I_UNDERSTAND=replace-target-database
scripts/backup/postgres-restore.sh /srv/.../projectrebound-....dump.age
```

For alerting, set `BACKUP_METRICS_DIRECTORY=/var/lib/node_exporter/textfile_collector` and enable node-exporter's textfile collector for that directory. Backup, verification, and restore-drill timestamps are written atomically to separate `.prom` files. A failed run updates its status gauge without deleting the previous successful backup timestamp.

Recovery sequence: Empty PostgreSQL → Database dump → Recovery script terminates room/connection/allocation/migration in snapshot within a single transaction and puts node OFFLINE → Access/Relay/Manifest key with Relay CA → Control Plane non-destructive migration → Administrator and player_id verification → Old Manifest verification → Relay reconnect or re-register. The volatile state must be cleared before accessing the public network and cannot wait for ordinary TTL.

`docs/testing/v1.1/restore-test-report.md` is rehearsed and updated every week in a new isolation environment: backup and independent key package hash, start/end time, database recovery time, application RTO/RPO, number of table rows, key identity/Manifest verification, Relay reconnection and failed items. Do not write "recovery successful" without actual evidence of execution.
