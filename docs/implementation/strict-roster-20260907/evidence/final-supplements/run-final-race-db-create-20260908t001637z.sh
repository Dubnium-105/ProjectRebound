#!/usr/bin/env bash
set -o pipefail
/usr/bin/createdb -h 127.0.0.1 -p 55439 -U phanthy 'rebound_final_race_schema48_20260908t001637z'
rc=$?
printf 'DB_NAME=rebound_final_race_schema48_20260908t001637z\nCREATEDB_EXIT_CODE=%s\n' "$rc"
exit $rc