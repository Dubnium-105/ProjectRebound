#!/usr/bin/env bash
set -o pipefail
/usr/bin/psql 'postgres://phanthy@127.0.0.1:55439/=disable' -X -v ON_ERROR_STOP=1 -f '/mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/postcheck-full-20260908T000115Z.sql'
rc=\True
printf 'EXIT_CODE=%s\\n' "\"
exit \