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

Acceptance thresholds are API P95 below 200 ms, HTTP failures below 1%, checks above 99%, and WebSocket upgrade P95 below one second. Watch PostgreSQL connections, Redis latency, goroutine count, and the supplied Prometheus dashboard throughout the five-minute run; extend `DURATION` to `30m` for leak/soak validation.
