# Repeatable chaos scenarios

English | [简体中文](README.zh-CN.md)

Create a disposable Compose deployment using project name `project-rebound-chaos-*`, then run:

```bash
export PROJECTREBOUND_ENVIRONMENT=test
export CHAOS_I_UNDERSTAND=disposable-staging
export CHAOS_PROJECT=project-rebound-chaos-ci
export CHAOS_TEST_COMMAND='go run ./cmd/load-bot -config tests/load/scenario-basic.yaml'
scripts/chaos/run-matrix.sh
```

`compose-fault.sh` supports restart, bounded pause, and SIGKILL/recreate for the explicit service allowlist. Pause always installs an unpause trap. It cannot target the production Compose project name. Use the netem scripts for control-link/DNS-style packet loss. Disk-low and clock-skew tests require a disposable VM or container with a bounded test filesystem/time namespace; they are intentionally not automated against a host OS.

Record recovery time, request failure window, WebSocket/control reconnects, migration attempts, residual allocations, DB pool recovery, Redis fallback metrics, memory, goroutines and disk use. Re-run the same request IDs/players after each fault to verify idempotency.
