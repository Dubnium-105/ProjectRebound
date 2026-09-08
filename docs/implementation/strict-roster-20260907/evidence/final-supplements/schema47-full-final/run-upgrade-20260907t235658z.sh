#!/usr/bin/env bash
set +e
cd /mnt/c/wksp/ProjectRebound/Backend
TEST_DATABASE_URL='postgres://phanthy@127.0.0.1:55439/=disable' GOCACHE=/tmp/rebound-schema47-full-final-gocache GOMODCACHE=/mnt/c/Users/23587/go/pkg/mod /tmp/rebound-strict-roster-20260907-go1266/go/bin/go test ./internal/database -run '^TestMigratorAgainstPostgreSQL$' -count=1 -v > /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/upgrade-20260907t235658z.log 2>&1
ec=$?
printf '\nEXIT_CODE=%s\n' "$ec" >> /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/upgrade-20260907t235658z.log
exit "$ec"