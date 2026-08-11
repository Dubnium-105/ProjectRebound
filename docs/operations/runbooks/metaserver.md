# MetaServer incident runbook

English | [简体中文](metaserver.zh-CN.md)

## First response

1. Record alert time, environment, deployed image digest, request IDs, and
   affected player/match/server IDs. Do not copy credentials or protobuf bodies.
2. Check `meta-server` readiness, container state, PostgreSQL/Redis dependency
   checks, Meta FRPC/FRPS, HAProxy, and certificate validity in that order.
3. Compare Gate replay, malformed-frame, queue-depth, scheduler, HTTP/RPC
   latency, Relay readiness, and Game Server availability metrics.
4. Preserve logs and audit rows before restarting anything. MetaServer can be
   restarted independently; Relay nodes with active allocations must not be.

## Readiness failure

- PostgreSQL: confirm migration 40 exists and the restricted Meta role retains
  grants; do not grant schema-wide write privileges as a shortcut.
- Redis: confirm the `projectrebound-meta` ACL user is enabled and limited to
  `meta:*`; a Redis restart invalidates outstanding Gate tickets, so clients
  must request new sessions.
- Schema/config: compare the image release metadata to protocol/DB/definitions
  versions and inspect startup hash verification.
- Roll back only the MetaServer image if the new image caused failure. Additive
  migrations stay applied.

## HTTP works but Logic fails

1. Verify `logic.dubnium.top` is DNS-only and resolves to the gateway.
2. Run `openssl s_client` with SNI and certificate verification.
3. Check HAProxy SNI route, loopback 16969, Meta FRPS 7002, control Meta FRPC,
   and loopback 16968.
4. Confirm no other FRP service reused the Meta user, token, config directory,
   port, or unit.
5. Do not expose 6968/6969 directly as a workaround.

## Gate replay or malformed-frame spike

Block only confirmed abusive source ranges at the gateway after checking for a
client rollout defect. Capture counters and short metadata, not tickets or full
frames. Revoke affected auth sessions if credential theft is suspected. Rotate
FRP tokens only when the tunnel identity may be compromised; FRP rotation does
not invalidate player sessions.

## Matchmaking queue backlog

Check scheduler leader metrics and PostgreSQL locks, then list Game Servers by
state, mode, region, version, capacity, token expiry, and heartbeat age. A queue
with no compatible READY server is expected to wait; do not enable P2P fallback.
For stuck reservations, use the audited admin match cancel route so the match
and `RESERVED -> READY` release are transactional.

## QoS or Relay discovery failure

Verify Relay Registry state, heartbeat freshness, load state, and public UDP
endpoint. DRAINING, REJECT_NEW, UNHEALTHY, or OFFLINE nodes are intentionally
excluded. Test an exact bounded `0x59` request; never use an oversized probe.
QoS throttling must not alter normal authenticated Relay traffic. Roll one Relay
at a time only after draining allocations.

## Credential or data exposure

Revoke exposed player/admin/Game Server credentials in their owning system.
Rotate the isolated Meta FRP token if exposed. Gate tickets need no database
cleanup after their 60-second TTL, but inspect replay evidence. If logs contain
credentials or full snapshots, restrict the log store, preserve evidence, and
open a security incident before redaction/retention cleanup.

## Recovery acceptance

Require ready health, valid HTTP and Logic TLS routes, a fresh Gate issue and
single consumption, loadout read/write conflict test, one solo/Party match,
Dedicated Server authorization test, dynamic Relay/QoS test, stable error rates,
and no new FRP reconnects. Run canary traffic before resuming the normal soak
gate.
