# Weak network and fault testing

English | [简体中文](chaos-testing.zh-CN.md)

Execution is only allowed in an isolated Linux network namespace, veth/dummy test interface, or a one-time test VM. The script also requires root, an explicit interface, and a confirmation string, and refuses to run on `PROJECTREBOUND_ENVIRONMENT=production`.

```bash
sudo env NETEM_I_UNDERSTAND=isolated-test NETEM_INTERFACE=veth-relay \
  scripts/netem/profile.sh moderate
sudo env NETEM_I_UNDERSTAND=isolated-test NETEM_INTERFACE=veth-relay \
  scripts/netem/reset.sh
```

Default: Mild is 50ms/10ms jitter/1% loss; Moderate is 120ms/30ms/5%/2Mbps; Severe is 250ms/80ms/15%/3% reorder/256Kbps. Single scripts and corresponding `NETEM_*` environment variables can also be used.

Each test should call `reset.sh` in `trap` and record the start/end time, interface, preset, load-bot report, Relay migration success rate, memory and goroutine. Acceptance includes control flow/WebSocket reconnection, Relay BIND retries, no room closure after one heartbeat loss, migration after SIGKILL, and migration without infinite retries.

The Compose bug uses `scripts/chaos/compose-fault.sh` and requires the project name to start with `project-rebound-chaos`. Override restart, pause and SIGKILL of Control Plane/Redis/PostgreSQL. Object storage unavailability is injected through the test-dedicated invalid endpoint; DNS failure is injected through the isolated network namespace/netem; disk shortage and clock drift are only allowed to be executed in a temporary volume or time namespace with an upper capacity limit, and the host is not directly modified.

## Automated short-duration gate

A Linux Docker host can run the secure wrapper within the repository:

```bash
cd Backend/tests/integration
sudo env \
  V11_INTEGRATION_I_UNDERSTAND=disposable-docker-stack \
  TESTCONTAINERS_RYUK_DISABLED=true \
  ./run-gate.sh
```

This gate operates only on disposable Compose projects whose labels begin with `project-rebound-v11-` and explicitly builds the image from the current source. It covers a dual-Relay baseline, three netem profiles, 100-client synchronized reconnection, active-Relay `SIGKILL` migration, Redis/PostgreSQL/control-plane restarts, and post-recovery smoke tests. After a control-plane restart, the test must observe a new Relay heartbeat later than the restart time; the stale `READY` state in the database is not sufficient.
