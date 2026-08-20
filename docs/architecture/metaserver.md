# MetaServer architecture

English | [简体中文](metaserver.zh-CN.md)

## Scope and provenance

Project Rebound implements the Boundary MetaServer protocol in Go. The upstream
reference is `Dubnium-105/BoundaryMetaServer master@d68e717`; Node.js is not a
production dependency. The 41 source protobuf files, 13 content definition
files, upstream commit, per-file hashes, aggregate definitions hash, and AGPL
notice are pinned in `Backend/api/proto/metaserver` and
`Backend/internal/metaserver/assets/definitions`.

Production uses statically generated Go protobuf types. Definitions are embedded
and hash-checked at startup. Fields marked tentative by the upstream project are
excluded from state-changing matching logic until a sanitized real-client
capture confirms them.

## Components and trust boundaries

```text
Browser/launcher -- Access Token via anonymous pipe --> MetaTunnel
MetaTunnel -- HTTPS --> meta.project-rebound.space -- FRP --> meta-server HTTP :8081
game -- loopback TCP --> MetaTunnel -- verified TLS --> logic.project-rebound.space:443
logic gateway -- terminated TLS + isolated authenticated FRP --> meta-server :6968

meta-server --> PostgreSQL meta_* tables and selected read-only control data
meta-server --> Redis meta:* Gate tickets
meta-server --> READY Game Server and Relay registry rows
Dedicated Server -- scoped token --> /internal/v1/meta/*
Administrator -- trusted network + session + permission + step-up --> /v1/admin/meta/*
```

`meta-server` is a separate process and image. It does not share a listener,
database role, Redis ACL user, FRP user, token, systemd unit, or rollback action
with the control plane. Public port 443 is the only client ingress. Ports 6968,
6969, 8000, 8081, and 9000 remain private or loopback-only. `dubnium.top` is
retired and is not part of the production trust boundary or fallback path.

## Identity flow

The launcher authenticates against the existing control plane and passes the
initial Access Token to MetaTunnel through stdin. It keeps the pipe open and
writes each replacement token before the 15-minute Access Token expires.
MetaTunnel never accepts tokens in a command line, environment variable, URL,
or log. It binds random loopback-only HTTP and TCP ports.

MetaTunnel is a fixed-origin reverse proxy: it preserves the HTTP method, path,
query, body, response, streaming, and upgrade semantics for every MetaServer
path while replacing any client-supplied Authorization header with the current
launcher token. `/_meta-tunnel/health/live` is reserved for local tunnel health;
MetaServer's `/health/live` continues upstream. Native TCP frames are bridged
unchanged over certificate-verified TLS.

For `/connectServer`, the tunnel additionally enforces the legacy body limit
and rewrites the successful Logic endpoint to its local TCP listener. Shipped
builds disagree on the legacy body's encoding, field names, and types, so MetaServer
drains the size-limited body without decoding or trusting it. It derives player
ID, auth session, account state, compatibility client label, and protocol
version server-side. It stores a SHA-256-keyed Gate record in Redis for 60
seconds and returns the opaque 256-bit ticket. The native Gate handshake
consumes it with Redis `GETDEL`; a concurrent or repeated use fails and emits a
replay metric/security event.

## Persistence and consistency

- Profiles and all player-owned rows reference `players.id`.
- `(player_id, role_id)` uniquely identifies a role loadout. JSON is validated
  against pinned definitions and updated with compare-and-swap `revision`.
- Weapon archives retain the original protobuf bytes, decoded JSON, and
  SHA-256 for forensic and migration use.
- Partial unique indexes enforce one active Party membership and one active
  match ticket per player. Party mutation locks the affected rows.
- The scheduler takes a PostgreSQL advisory transaction lock. It claims queued
  tickets and READY Game Servers with `FOR UPDATE SKIP LOCKED`, then writes the
  match, roster, ticket transition, and `READY -> RESERVED` transition in one
  transaction.
- Image rollback never rolls migrations back. Current MetaServer readiness requires migration 40; migrations 25–40 remain applied during an ordinary image rollback.

## Availability and discovery

Matchmaking selects registered Dedicated Servers only; there is no implicit P2P
host fallback. A ticket remains queued until assigned or expired. A cancelled,
expired, failed, offline, or unconnected reservation releases the server.

Region and QoS discovery is built from Relay Registry rows on every request.
Only READY nodes with fresh heartbeats and a load state that accepts new traffic
are returned. New nodes need registry enrollment, not a new DNS name or a
MetaServer configuration edit.

## Process security and observability

The container runs as a non-root numeric user with a read-only root filesystem,
all capabilities dropped, `no-new-privileges`, tmpfs, and CPU, memory, PID
limits. TCP applies handshake/read/write/idle deadlines, a 2 MiB maximum frame,
bounded write queues, per-IP connection and rate limits, per-player/RPC limits,
and panic isolation. Logs omit bearer tokens, Gate tickets, complete protobuf
frames, and loadout snapshots.

Metrics cover HTTP, native connections, RPC latency, malformed frames, Gate
issue/consume/replay, loadout conflicts, queue depth, matching latency, and Relay
QoS. See the [threat model](metaserver-threat-model.md), [native protocol](metaserver-native-protocol.md),
and [deployment guide](../operations/metaserver-deployment.md).
