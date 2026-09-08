#!/usr/bin/env bash
set -o pipefail
export TEST_DATABASE_URL='postgres://phanthy@127.0.0.1:55439/rebound_audit_schema47full_20260907t235537z?sslmode=disable'
export GOCACHE='/tmp/rebound-backend-authority-gocache-final-20260908'
export GOMODCACHE='/mnt/c/Users/23587/go/pkg/mod'
cd /mnt/c/wksp/ProjectRebound/Backend
/tmp/rebound-strict-roster-20260907-go1266/go/bin/go test -race ./internal/database -run 'TestMigrationChecksumsAreStableAcrossCheckoutLineEndings|TestMigrationFormatChecksumDoesNotAcceptSQLDrift|TestLoadMigrationsSortsAndChecksums|TestMigratorAgainstPostgreSQL' -count=1 -v
rc=$?
printf 'EXIT_CODE=%s\n' "$rc"
exit $rc