#!/usr/bin/env bash
set +e
psql -h 127.0.0.1 -p 55439 -U phanthy -d rebound_audit_schema47full_20260907t235537z -v ON_ERROR_STOP=1 -f /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/precheck.sql > /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/precheck-final.log 2>&1
ec=$?
printf '\nEXIT_CODE=%s\n' "$ec" >> /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/precheck-final.log
exit "$ec"