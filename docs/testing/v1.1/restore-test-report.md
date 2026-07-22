# V1.1 Recovery Drill Report

English | [简体中文](restore-test-report.zh-CN.md)

Status: `PASS`

Exercise time: 2026-07-21 09:46:29Z ~ 09:46:42Z

Exercise environment: Debian 13 isolated Docker network, two new PostgreSQL 17 containers, Redis 7, Control Plane and Edge Relay built from current source code. All containers, networks, volumes, temporary image labels, clear text recovery files, and walkthrough directories are removed by the exit trap; the production database is not connected or modified.

Execute command:

```bash
cd Backend/tests/integration
sudo env \
  RESTORE_DRILL_I_UNDERSTAND=disposable-postgres-containers \
  ./run-restore-drill.sh
```

## result

| Item | Result |
| --- | --- |
|PostgreSQL version/Schema|PostgreSQL 17; schema version 16; all 22 required tables exist|
|Database backup|`pg_dump` custom format, compression, `age` encryption, SHA-256, `pg_restore --list` verification PASS|
|Database backup SHA-256| `5df3db6eb238881f37da473db2e9c4922052ce9296542fe21404fededb8017a1` |
|independent key package|Access Token, Relay Token, Manifest, Relay CA and recovery credentials are independent `age` Encryption; SHA-256 `bfb6415301afe9a78495920ac588edbfef8412858be429dca5d8bdfb2f974837`|
|Database recovery time| 515 ms |
|Apply RTO|4417 ms (recovery from start to Control Plane READY, Administrator Authentication, Manifest Continuity, and Relay READY)|
|Total exercise time|13 seconds (including stack building, backup, verification, recovery and application verification after the image cache is hit, the image build is not included in RTO)|
| player_id |`restore-player-0001` Double verification of database and administrator API after recovery|
|Administrator recovery credentials|Control Plane administrator API authentication and player query PASS after restoration|
|Old Manifest|Use the same manifest private key recovered before and after recovery; after normalizing the request ID, the response is consistent byte by byte, and the signature and hash fields exist.|
| Relay |New environment Relay uses the recovered CA/Relay Token key to register with the recovered Control Plane and enter READY|
|old activity status|Room `CLOSED`, connection/allocation `FAILED`, old Relay `OFFLINE`, active member/allocation are all 0|
|migration idempotent|After recovery run schema migrator PASS again|
|Backup/verification/restore indicators|The three indicators of backup success, verification success, and restore drill timestamp are all generated and verified.|

## Restore semantic correction

`postgres-restore.sh` Execute a single transaction after the database recovery is completed, prohibiting the resurrection of volatile state in the snapshot: unfinished Relay migration, allocation, connection, room and member are terminated, non-revoked nodes are set to OFFLINE, capacity, bandwidth and lease are cleared. Players, authentication sessions, invitation codes, auditing and publishing data are not within the scope of this cleanup.

This exercise demonstrates that single database backups and independent key packages can be restored in a new environment; production operations must still meet off-site multiple copies, retention periods, access approvals, and formal change windows.
