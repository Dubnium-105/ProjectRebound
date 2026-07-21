#!/usr/bin/env python3
import json
import sys


def fail(message: str) -> None:
    print(f"LONGRUN_REPORT_FAIL {message}", file=sys.stderr)
    raise SystemExit(1)


if len(sys.argv) != 6:
    fail("usage: validate-report.py REPORT MIN_SECONDS CLIENTS ROOMS RELAY_CONNECTIONS")

path, min_seconds, clients, rooms, relay_connections = sys.argv[1:]
min_seconds = float(min_seconds)
clients = int(clients)
rooms = int(rooms)
relay_connections = int(relay_connections)

with open(path, encoding="utf-8") as handle:
    report = json.load(handle)

successful = int(report.get("successful_requests", 0))
failed = int(report.get("failed_requests", 0))
total = successful + failed
failure_rate = (failed * 100 / total) if total else 100.0
failures = report.get("failures") or {}
server_errors = sum(
    int(value)
    for key, value in failures.items()
    if any(f"status_{status}" in key for status in range(500, 600))
)
server_error_rate = (server_errors * 100 / total) if total else 100.0

checks = {
    "scenario duration": float(report.get("duration_seconds", 0)) >= min_seconds * 0.99,
    "client count": int(report.get("clients", 0)) == clients,
    "rooms created": int(report.get("rooms_created", 0)) == rooms,
    "Relay allocations created": int(report.get("relay_allocations", 0)) >= relay_connections,
    "Relay allocations closed": int(report.get("relay_allocations_closed", 0)) == relay_connections,
    "Relay BIND failures": int(report.get("relay_bind_failures", 0)) == 0,
    "Relay BIND successes": int(report.get("relay_bind_success", 0)) >= relay_connections * 2,
    "API P95 below 200ms": float(report.get("p95_ms", 1e9)) < 200,
    "request failures below 1%": failure_rate < 1,
    "API 5xx below 0.5%": server_error_rate < 0.5,
    "success rate above 99%": float(report.get("success_rate_percent", 0)) > 99,
    "Refresh Token flow": int(report.get("token_refresh_failures", 0)) == 0,
    "UDP traffic sent": int(report.get("packets_sent", 0)) > 0,
    "UDP traffic received": int(report.get("packets_received", 0)) > 0,
    "UDP loss below 5%": float(report.get("packet_loss_percent", 100)) < 5,
}

failed_checks = [name for name, passed in checks.items() if not passed]
if failed_checks:
    fail(
        ", ".join(failed_checks)
        + f"; failure_rate={failure_rate:.6f}% server_error_rate={server_error_rate:.6f}% report={path}"
    )

print(
    "LONGRUN_REPORT_OK"
    f" duration={float(report['duration_seconds']):.1f}s"
    f" success_rate={float(report['success_rate_percent']):.6f}%"
    f" p95_ms={float(report['p95_ms']):.3f}"
    f" failure_rate={failure_rate:.6f}%"
    f" server_error_rate={server_error_rate:.6f}%"
    f" packet_loss={float(report['packet_loss_percent']):.6f}%"
)
