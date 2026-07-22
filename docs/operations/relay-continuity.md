# Relay continuity and recovery policy

English | [简体中文](relay-continuity.zh-CN.md)

This document defines Edge Relay production invariants, certificate lifecycle, recovery boundaries, and test methodology. A healthy node should remain continuously online. Releases, recovery, and fault injection must limit disruption to one already-drained node.

## Runtime invariants

- A healthy Relay is not restarted hourly, daily, or on any other fixed schedule.
- Compose uses `restart: unless-stopped` only to recover from an abnormal process exit or Docker/host recovery.
- A monitor performs explicit recovery only after the container is stopped and a second check confirms it has not recovered. A timer is not a health check.
- When the control stream disconnects, the Relay reconnects with backoff. Existing allocations continue during the default grace period; after the grace period the node enters `DRAINING` and rejects new allocations.
- A planned release follows `DRAINING -> active_allocations=0 -> deploy -> CONNECTING -> READY`, one node at a time. Never restart every node in a region together.

Relay allocations and UDP sessions live in process memory. Restarting a Relay that carries an active match breaks the original data path. Control-plane migration and client re-BIND reduce recovery time but cannot turn a stateful process restart into a lossless operation. Drain every planned restart first.

## mTLS certificate lifecycle

After enrollment, a node atomically stores its private key, certificate, control-plane CA, node token, and keyset in `identity.json` with mode `0600`. A production node certificate is valid for 24 hours by default. At 25% remaining lifetime, the Edge Relay:

1. creates a new Ed25519 private key and CSR;
2. requests renewal using the existing node identity;
3. atomically replaces `identity.json`;
4. rebuilds the mTLS gRPC control connection with the new certificate.

If renewal fails while the old certificate remains valid, the node retries with control-stream backoff. An expired certificate cannot establish a new mTLS connection; use controlled re-enrollment or identity recovery instead of repeated restarts.

Early Edge images read a certificate only at startup and do not support runtime renewal. Before upgrading such a node, verify adequate expiry headroom, drain it, deploy a runtime-renewal-capable image, and verify the new certificate. A control-plane restart forces a new connection and is useful as a post-release certificate check, but it is not a routine renewal mechanism.

## Monitoring and alerts

Grafana and Prometheus derive Relay targets from the dynamic control-plane inventory. Never hard-code fixed names such as LAX or HGH. Each node should expose:

- state, `control_connected`, last heartbeat, and heartbeat age;
- `certificate_expires_at` and remaining lifetime;
- software version, protocol version, region, and public UDP endpoint;
- active allocations, capacity, BIND/forwarding errors, and control reconnect count;
- container state and the most recent non-zero exit.

Alert when a certificate enters its renewal window without being replaced, the control stream remains disconnected, heartbeats expire, a node leaves `READY`, or capacity is exhausted. A stale database `READY` row does not prove liveness. Recovery after a control-plane restart requires a new heartbeat newer than that restart.

## Offline recovery

1. Pause releases for the affected node. Inspect dynamic inventory, container state, heartbeat, control connection, certificate expiry, and active allocations.
2. If the process still runs, allow built-in reconnect backoff. Never start a second instance with the same identity.
3. If the container is stopped, wait for the confirmation interval and recover only that container. Stop automated attempts during a persistent crash loop and retain logs.
4. If the certificate expired or identity was revoked, issue a new identity through the node-enrollment flow. Never copy another Relay's `identity.json`.
5. Resume scheduling only after the node reaches `CONNECTING`, emits a new heartbeat, and returns to `READY`.
6. Compare allocations, migrations, and client re-BIND results before and after the fault; confirm that no handle remains.

See the [Relay outage runbook](runbooks/relay-outage.md) for incident steps and [release and rollback](release-and-rollback.md) for planned changes.

## Test methodology and current evidence

Continuous soak and fault injection are separate:

- **Soak:** no planned Relay restart; recover only a confirmed failure. This measures continuity, certificate renewal, resources, and data-plane stability.
- **Fault injection:** SIGKILL, control-plane/Redis/PostgreSQL restart, weak network, and migration run independently to measure recovery windows.

The corrected continuous-online verification on 2026-07-22 ran for 601.2 seconds: HTTP 4,780/4,780 with P95 6.482 ms; UDP 1,170,600/1,170,600 with zero loss; 100 allocations; zero control-disconnect samples; and no residual allocation. An earlier 17.1-hour run forcibly restarted a Relay every hour, causing 14.7928% UDP loss without useful migration evidence. That method was incorrect fault injection, is excluded from soak evidence, and has been removed from policy. A formal 24-hour continuous-online run remains required under the corrected methodology.

See the [V1.1 test report](../testing/v1.1/test-report.md) for complete version evidence.
