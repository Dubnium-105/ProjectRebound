#!/usr/bin/env bash
set +e
cd /mnt/c/wksp/ProjectRebound/Backend
TEST_DATABASE_URL='postgres://phanthy@127.0.0.1:55439/rebound_audit_schema47full_20260907t235537z?sslmode=disable' GOCACHE=/tmp/rebound-schema47-full-final-gocache2 GOMODCACHE=/mnt/c/Users/23587/go/pkg/mod /tmp/rebound-strict-roster-20260907-go1266/go/bin/go test ./internal/database -run '^TestMigratorAgainstPostgreSQL$' -count=1 -v > /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/upgrade-rerun-20260907t235811z.log 2>&1
ec=$?
printf '\nEXIT_CODE=%s\n' "$ec" >> /mnt/c/wksp/ProjectRebound/.tmp/strict-roster-20260907/schema47-full-final/upgrade-rerun-20260907t235811z.log
exit "$ec"