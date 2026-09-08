#!/usr/bin/env bash
set +e
psql -h 127.0.0.1 -p 55439 -U phanthy -d postgres -v ON_ERROR_STOP=1 -c 'CREATE DATABASE rebound_audit_schema47full_20260907T235456Z' > /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/restore-20260907T235456Z.log 2>&1
ec=$?
printf 'CREATE_DB_EXIT_CODE=%s\n' "$ec" >> /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/restore-20260907T235456Z.log
if [ "$ec" -ne 0 ]; then exit "$ec"; fi
pg_restore --host=127.0.0.1 --port=55439 --username=phanthy --dbname=rebound_audit_schema47full_20260907T235456Z --no-owner --no-privileges --exit-on-error /mnt/c/wksp/ProjectRebound/docs/implementation/strict-roster-20260907/evidence/backend-schema47-source-20260907-authority-final.dump >> /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/restore-20260907T235456Z.log 2>&1
ec=$?
printf 'RESTORE_EXIT_CODE=%s\n' "$ec" >> /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/restore-20260907T235456Z.log
if [ "$ec" -ne 0 ]; then exit "$ec"; fi
psql -h 127.0.0.1 -p 55439 -U phanthy -d rebound_audit_schema47full_20260907T235456Z -v ON_ERROR_STOP=1 -f /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/schema47-active-hosts.sql >> /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/restore-20260907T235456Z.log 2>&1
ec=$?
printf 'SEED_EXIT_CODE=%s\n' "$ec" >> /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/restore-20260907T235456Z.log
exit "$ec"