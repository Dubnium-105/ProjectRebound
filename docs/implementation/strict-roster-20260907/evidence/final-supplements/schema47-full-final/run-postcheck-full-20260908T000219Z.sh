#!/usr/bin/env bash
set -o pipefail
/usr/bin/psql 'postgres://phanthy@127.0.0.1:55439/rebound_audit_schema47full_20260907t235537z?sslmode=disable' -X -v ON_ERROR_STOP=1 -f '/mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/postcheck-full-20260908T000219Z.sql'
rc=$?
printf 'EXIT_CODE=%s\n' "$rc"
exit $rc