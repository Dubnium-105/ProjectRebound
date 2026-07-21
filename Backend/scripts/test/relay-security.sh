#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_dir"

go test -race ./internal/relayruntime -run 'Test(Runtime|V2Cookie|TokenVerifier|UDPListener)' -count=1
go test ./internal/relayruntime -run 'TestRuntime(CookieBindingAndAuthorizedForwarding|AllowsShortNATPortRebindButRejectsLateOrCrossIPReplay|RejectsWrongNodeExpiredAndInvalidRoleTokens|RateLimitsTotalBytesAndExpiresInMemoryAllocations|SeparatesUnverifiedLimitsAndBoundsIPState|TemporarilyBansInvalidTokenFlood|LimitsUniqueAllocationsPerIP|OverloadStateRejectsOnlyNewAllocations)' -count=20
