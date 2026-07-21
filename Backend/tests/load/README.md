# Load test

Run against an isolated staging environment with production-like PostgreSQL and Redis pool sizes:

```powershell
$env:BASE_URL = 'https://staging-api.example.com'
$env:REALTIME_URL = 'wss://staging-api.example.com/v1/realtime/connect'
$env:ACCESS_TOKENS_JSON = Get-Content -Raw .\staging-access-tokens.json
k6 run .\tests\load\control-plane.js
```

The token file is generated outside Git and must contain at least 100 short-lived staging player access tokens to exercise 100 concurrent WebSockets. Without it, the scenario still tests the health, dedicated-server directory, P2P directory, and client-configuration paths at 100 virtual users.

The repository-native load bot can run without k6 and emits terminal, JSON, and Prometheus text reports:

```bash
go run ./cmd/load-bot -config tests/load/scenario-basic.yaml -report load-report.json -prometheus-report load-report.prom
```

Use `scenario: auth-bind` with a staging invite code to exercise concurrent bind/rate-limit behavior. Generated SteamIDs and Device IDs are deterministic per virtual client, so repeated runs are easy to correlate without logging credentials. Long scenarios must run only against isolated staging infrastructure.

`scenario: full`, `relay`, and `soak` are real end-to-end flows. They bind every virtual client, create and join rooms, establish authenticated WebSockets, publish candidate/check-result events, force direct-path failure, consume participant-specific Relay allocation events, execute UDP protocol-v2 BIND challenge/proof, and exchange authenticated Relay data. The clients maintain room heartbeats, rotate Refresh Tokens, inject configured WebSocket disconnects, reconnect with current credentials, and rebind when migration allocations arrive. No Relay Token is written to the report or logs.

The standard gates are versioned as:

```bash
# 300 clients, 100 rooms/allocations, six hours
go run ./cmd/load-bot -config tests/load/scenario-v1.1-full.yaml \
  -report load-report-v1.1-6h.json -prometheus-report load-report-v1.1-6h.prom

# 100 Relay allocations, 24 hours; restart one Relay each hour using the chaos runner
go run ./cmd/load-bot -config tests/load/scenario-v1.1-relay-soak.yaml \
  -report load-report-v1.1-relay-24h.json -prometheus-report load-report-v1.1-relay-24h.prom
```

Replace the staging URL and invite code before execution. The staging Auth limits must be explicitly sized for controlled setup traffic; do not weaken production limits. Pair the six-hour run with `tests/chaos/run.sh` for the scheduled Control Plane/Redis restart and `tests/netem/run-relay-matrix.sh` for weak-network cases.

JSON output includes request success/failure counts and categories, success rate, P50/P95/P99, elapsed duration, room and Relay allocation counts, migrations, reconnects, bytes, Refresh failures, and load-bot process memory/goroutine deltas. Control Plane and Relay memory/goroutine deltas must be exported separately from Prometheus over the same time window.

Acceptance thresholds are API P95 below 200 ms, HTTP failures below 1%, checks above 99%, and WebSocket upgrade P95 below one second. Watch PostgreSQL connections, Redis latency, goroutine count, and the supplied Prometheus dashboard throughout the five-minute run; extend `DURATION` to `30m` for leak/soak validation.
