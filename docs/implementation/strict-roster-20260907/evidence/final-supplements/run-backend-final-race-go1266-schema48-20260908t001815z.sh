#!/usr/bin/env bash
set -o pipefail
export TEST_DATABASE_URL='postgres://phanthy@127.0.0.1:55439/rebound_final_race_schema48_20260908t001637z?sslmode=disable'
export TEST_REDIS_ADDRESS='127.0.0.1:56380'
export GOCACHE='/tmp/rebound-backend-authority-gocache-final-race-20260908'
export GOMODCACHE='/mnt/c/Users/23587/go/pkg/mod'
cd /mnt/c/wksp/ProjectRebound/Backend
/tmp/rebound-strict-roster-20260907-go1266/go/bin/go test -race -p 1 ./... -count=1 -v
rc=$?
printf 'EXIT_CODE=%s\n' "$rc"
exit $rc