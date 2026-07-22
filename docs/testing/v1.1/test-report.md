# V1.1 Testing and Release Gate Report

English | [简体中文](test-report.zh-CN.md)

Report date: 2026-07-22

Production candidate: `66cde85a7528781b18e4302289d5d6087364076d`

Room TTL fix: `447e27a`; continuous online policy: `66cde85a`

Overall status: `PRODUCTION_DEPLOYED_24H_SOAK_PENDING`

V1.1 has been deployed to the production control plane and the LAX and HGH Relays; all seven CI jobs passed. Short-duration functional, security, impaired-network, migration, fault-recovery, alerting, and restore gates passed, as did the 1-hour and 6-hour capacity soak tests. The official 24-hour continuous-online Relay soak has not yet been completed under the revised "no planned restart" policy, so production observation may continue, but the current evidence must not be presented as a completed 24-hour release gate.

## Automated quality gates

| Command | Scenario | Result | Actual observation |
| --- | --- | --- | --- |
| `gofmt -l .` |All Go source code formats| PASS |No output|
| `go vet ./...` |All Go packages static checks| PASS |No diagnosis|
| `go test ./... -count=1` |Unit and in-process integration testing| PASS |About 15 seconds on this machine; all running packages pass|
| `go test ./internal/loadbot -count=20` |load-bot short-term stability return| PASS |Fix the occasional empty report in short tests caused by the first request waiting for the ticker|
| `TEST_DATABASE_URL=… TEST_REDIS_ADDRESS=… go test -race ./... -count=1` |Linux, PostgreSQL 17, Redis 7, full package Race Detector| PASS |~14.3 seconds; temporary container deleted|
| `go test -tags=integration ./... -run '^$'` |Standalone Testcontainers module compilation| PASS |No test execution, only verification that integrated access compiles|
| `promtool test rules v1.1.rules.test.yml` |V1.1 alarm firing/resolved unit test| PASS |PostgreSQL/Redis, Auth/Relay replay, no available Relay, backup and recovery alarms passed|
| `staticcheck ./...` / `golangci-lint run` | Additional static checks | NOT_RUN | The tools are not installed in the current environment and are not configured as repository CI gates |

CI now gates Testcontainers as a pre-requisite for image releases and explicitly builds the current source code before each run, avoiding fixed local image tags that obscure code modifications. CI simultaneously runs Go races, PostgreSQL/Redis integration, OpenAPI/deployment configurations, Prometheus rules, shell syntax, documentation links, deployment/rollback scripts and image provenance.

## Real container joint testing

Environment: Debian 13, PostgreSQL 17, Redis 7, current source code Control Plane (including integrated Worker), two Edge Relay, isolated network segment `198.18.11.0/24`. The final rerun of the combination without netem took 330.2 seconds and passed; the three subtests in the complete netem combination of the same code all passed.

| Scenario | Clients/rooms/allocations | Result |
| --- | --- | --- |
| Baseline full path | 4 / 2 / 2, 20 s | API 32/32, P50 6.8 ms, P95 13.5 ms, P99 18.0 ms; BIND 4/4; UDP 196/196; allocations recycled 2/2; memory +1,256,736 B; goroutines +4 |
| Synchronized reconnection short test | 100 / 50 / 50, 90.6 s | API 900/900, P50 4.4 ms, P95 8.5 ms, P99 77.5 ms; BIND 100/100; UDP 8200/8200; 200 WebSocket reconnections; allocations recycled 50/50; memory +2,476,080 B; goroutines +4 |
| Relay A `SIGKILL` | 4 / 2 / 2, 45.2 s | API 54/54; migration 1/1; BIND 6/6; UDP 415/418; failure-window packet loss 0.718%; no infinite retries; allocation cleanup completed |
|Redis restart|short fault|2.791s recovery READY|
|PostgreSQL restart|short fault|3.608s restore READY, the connection pool then completes the real Auth/room/Relay process|
|Control Plane Restart|short fault|Within 8.648s, new heartbeats of two Relays were recovered and observed; then API 32/32, BIND 4/4, UDP 196/196|

The Relay judgment after Control Plane restart no longer only trusts the old READY status in the database, but requires `last_heartbeat_at` to be later than this restart to prevent the test from misjudging the recovery completion when the lease is about to expire.

## Weak network matrix

`tc netem` is only injected into the container network namespace of one Relay and forces a reset after each subtest. Each scenario lasts 20 seconds, of which approximately 8 seconds are in the injection state; therefore the reported effective packet loss rate for the entire scenario is lower than the configured instantaneous packet loss rate.

| Profile |Inject parameters| API / BIND |UDP results|determination|
| --- | --- | --- | --- | --- |
| Mild | 50 ms, 10 ms jitter, 1% loss | 32/32; 4/4 | 181/182, 0.549% | PASS |
| Moderate | 120 ms, 30 ms jitter, 5% loss, 2 Mbps | 32/32; 4/4 | 196/196, 0% in this random window | PASS |
| Severe | 250 ms, 80 ms jitter, 15% loss, 3% reorder, 256 Kbps | 32/32; 4/4 | 163/165, 1.212% | PASS |

## Backup, recovery and alerting

| Gate | State | Evidence |
| --- | --- | --- |
|Prometheus alert rules| PASS |`promtool test rules` simultaneously verifies firing and resolved|
|Encrypted PostgreSQL backup/verification| PASS |custom dump, compression, age, SHA-256, `pg_restore --list`|
|Independent key recovery| PASS |Access, Relay, Manifest, Relay CA independent encryption package recovery|
|New Control Plane| PASS |After recovery, `/health/ready` successfully queried the administrator player.|
|Old Manifest Continuity| PASS |The normalized request ID before and after recovery is consistent byte by byte.|
|Relay recovery| PASS |The new Relay uses the recovered CA/key to re-register and enter READY|
|Volatile state cannot be revived| PASS |room CLOSED, connection/allocation FAILED, old Relay OFFLINE, activity count reset to zero|

See [Recovery Exercise Report](restore-test-report.md) for complete values ​​and SHA-256.

## Long-term stability and formal capacity gates

| Scenario | Actual results | State |
| --- | --- | --- |
|Preflight|600.3 seconds; HTTP success rate 100%; UDP zero packet loss| PASS |
|Basic concurrency|3,600.3 seconds; HTTP success rate 100%; P95 5.733 ms; UDP zero packet loss| PASS |
|Design upper limit|21,601.0 seconds; 155,345 HTTP requests, 100% success rate, P95 5.520 ms; UDP 43,170,300 sent, 43,170,297 received, packet loss rate 0.00000695%; includes Redis and Control Plane recovery; no residual allocation| PASS |
|Relay continuous online verification after correction|601.2 seconds; HTTP 4,780/4,780, P95 6.482 ms; UDP 1,170,600/1,170,600; 100 allocations; control broken chain sample 0; no residue|PASS (10 minutes)|
| Relay Soak |100~300 allocation, 24 hours; Relay does not plan to restart and will only resume after confirming the disconnection.| PENDING_24H |
| Official reconnection storm | 100 clients, 10 minutes | SHORT_PASS; the 90.6-second functional gate passed, but it did not meet the formal duration requirement |
|Formal Relay failure|50 allocation, 30 minutes|SHORT_PASS; double Relay real SIGKILL/migration passed, does not meet the official scale and duration|

A 17.1-hour test forced the Relay to restart every hour. Final UDP packet loss reached 14.7928%, and the run produced no evidence of effective migration. This demonstrates that periodic restarts actively destroy in-memory relay state and do not constitute a continuous-online soak. The strategy now avoids explicit restarts of healthy nodes and attempts recovery only after confirming that a node is offline; fault injection is tested separately. The failed run is retained as a counterexample and excluded from the 24-hour gate.

## Release Gate Conclusion

| Gate | State |
| --- | --- |
| Auth Gate | PASS |
| Relay Security Gate |SHORT_PASS; function/security and 6h capacity passed, 24h continuous online items to be completed|
| Migration Gate | SHORT_PASS; real failover migration, idempotency, and recovery to READY passed |
| Key Gate |PASS; automatic rotation/certificate test and recovery drill passed|
| Operations Gate | PASS; backup/restore, alerts, migration idempotency, and deployment/rollback scripts all have automated gates |
| Performance Gate |PARTIAL_PASS; passed in 1h/6h, passed online continuously for 10 minutes after correction, 24h to be completed|

Conclusion: The current production version has no known short-term release blockers, and the 1-hour and 6-hour gates passed. The official 24-hour continuous-online evidence remains incomplete, so version status remains `PRODUCTION_DEPLOYED_24H_SOAK_PENDING`. For the authoritative Relay operating policy, see [Continuous Online and Recovery Strategy](../../operations/relay-continuity.md).
