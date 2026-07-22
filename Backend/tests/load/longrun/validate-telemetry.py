#!/usr/bin/env python3
import json
import math
import re
import statistics
import sys


def fail(message: str) -> None:
    print(f"LONGRUN_TELEMETRY_FAIL {message}", file=sys.stderr)
    raise SystemExit(1)


if len(sys.argv) != 3:
    fail("usage: validate-telemetry.py METRICS_TSV MIN_SECONDS")

path = sys.argv[1]
min_seconds = float(sys.argv[2])
series: dict[str, list[float]] = {}
metric_pattern = re.compile(r"^([^\s{]+)(?:\{([^}]*)\})?\s+([^\s]+)$")

with open(path, encoding="utf-8") as handle:
    for line in handle:
        fields = line.rstrip("\n").split("\t", 1)
        if len(fields) != 2:
            continue
        match = metric_pattern.match(fields[1])
        if not match:
            continue
        name, labels, raw_value = match.groups()
        key = name if labels is None else f"{name}{{{labels}}}"
        try:
            value = float(raw_value)
        except ValueError:
            continue
        if math.isfinite(value):
            series.setdefault(key, []).append(value)


def values(name: str) -> list[float]:
    result = series.get(name)
    if not result:
        fail(f"missing metric {name} in {path}")
    return result


def quarter_medians(samples: list[float]) -> tuple[float, float]:
    width = max(1, len(samples) // 4)
    return statistics.median(samples[:width]), statistics.median(samples[-width:])


memory = values("go_memory_alloc_bytes")
goroutines = values("go_goroutines")
db_in_use = values("db_pool_in_use_connections")
db_open = values("db_pool_open_connections")
postgres = values("postgres_available")
redis = values("redis_available")

expected_samples = max(2, int(min_seconds / 60 * 0.75))
if min(len(memory), len(goroutines), len(db_in_use), len(db_open), len(postgres), len(redis)) < expected_samples:
    fail(f"insufficient samples; expected at least {expected_samples}")

relay_control = {
    key: samples
    for key, samples in series.items()
    if key.startswith("relay_node_control_connected{")
}
if len(relay_control) < 2:
    fail("expected telemetry for at least two Relay nodes")
if any(len(samples) < expected_samples for samples in relay_control.values()):
    fail(f"insufficient Relay continuity samples; expected at least {expected_samples} per node")

relay_disconnected_samples = {
    key: sum(value != 1 for value in samples)
    for key, samples in relay_control.items()
}
relay_disconnected_ratio = {
    key: relay_disconnected_samples[key] / len(samples)
    for key, samples in relay_control.items()
}

memory_first, memory_last = quarter_medians(memory)
goroutines_first, goroutines_last = quarter_medians(goroutines)
memory_limit = max(memory_first * 1.5, memory_first + 64 * 1024 * 1024)
goroutine_limit = max(goroutines_first * 2, goroutines_first + 100)

checks = {
    "no sustained memory growth": memory_last <= memory_limit,
    "no sustained goroutine growth": goroutines_last <= goroutine_limit,
    "PostgreSQL availability": sum(value == 0 for value in postgres) / len(postgres) <= 0.05,
    "Redis availability": sum(value == 0 for value in redis) / len(redis) <= 0.05,
    "Relay control continuity": max(relay_disconnected_ratio.values()) <= 0.005,
    "database pool not persistently exhausted": sum(
        open_count > 0 and in_use / open_count >= 0.85
        for in_use, open_count in zip(db_in_use, db_open)
    ) / min(len(db_in_use), len(db_open)) <= 0.05,
}
failed_checks = [name for name, passed in checks.items() if not passed]
if failed_checks:
    fail(", ".join(failed_checks))

summary = {
    "samples": len(memory),
    "memory_first_quarter_median_bytes": memory_first,
    "memory_last_quarter_median_bytes": memory_last,
    "goroutines_first_quarter_median": goroutines_first,
    "goroutines_last_quarter_median": goroutines_last,
    "db_in_use_max": max(db_in_use),
    "db_open_max": max(db_open),
    "postgres_unavailable_samples": sum(value == 0 for value in postgres),
    "redis_unavailable_samples": sum(value == 0 for value in redis),
    "relay_nodes": len(relay_control),
    "relay_disconnected_samples_max": max(relay_disconnected_samples.values()),
}
print("LONGRUN_TELEMETRY_OK " + json.dumps(summary, sort_keys=True))
