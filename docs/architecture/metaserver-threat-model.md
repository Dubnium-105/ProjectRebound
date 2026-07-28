# MetaServer threat model

English | [简体中文](metaserver-threat-model.zh-CN.md)

## Assets and actors

Protected assets are player identity and sessions, loadouts and weapon archives,
Party membership, match reservations, Game Server credentials, administrator
authority, protocol/definition integrity, and service availability. Actors are
normal and malicious clients, enrolled Dedicated Servers, Relay operators,
administrators, Cloudflare, the public gateway, and an attacker able to observe
or inject ordinary Internet traffic.

The client machine is not trusted. Hardware fingerprints, `playerId`,
`loginToken`, protobuf identity fields, QoS source addresses, and all loadout
JSON are untrusted input. PostgreSQL, Redis, the control-plane signing key, and
root-owned gateway/FRP configuration are inside the trusted operations boundary.

## Controls

| Threat | Control |
| --- | --- |
| Client impersonates another player | Existing signed Access Token and active-session check; all player IDs come from the principal |
| Gate credential theft/replay | 256-bit entropy, 60-second TTL, Redis hash key, atomic `GETDEL`, TLS, no credential logs |
| IDOR on Party, ticket, or loadout | Repository queries include authenticated player ownership/membership |
| Dedicated Server reads another match | token hash + server ID + scope + READY/active state + assigned match/player checks |
| Duplicate multi-instance allocation | PostgreSQL advisory leader lock, row locks, `SKIP LOCKED`, unique active reservation |
| Lost update or malicious loadout | pinned-definition validation, JSON object/size limits, optimistic revision lock |
| Slowloris/frame or connection flood | TLS/handshake/read/idle deadlines, frame cap, per-IP connection and rate limits |
| Forged address bypasses per-IP limits | both HAProxy Logic hops preserve the source with PROXY v1; MetaServer accepts it only on the explicitly enabled private FRP listener |
| QoS reflection/amplification | exact `0x59` recognition, request bounds, response no larger than request, per-IP PPS, silent malformed drop |
| Admin account or CSRF abuse | trusted network, administrator-only session, permission, step-up, explicit reason, audit |
| Supply-chain/protocol drift | pinned commit and hashes, static protobuf, generated-code diff gate, SBOM, provenance, vulnerability/image scans |
| Secret or tenant-data leakage | dedicated DB/Redis roles, isolated FRP credentials, structured redaction, no legacy JSON mount |
| Compromised MetaServer container | non-root, read-only root, no capabilities, NNP, tmpfs and resource limits |

## Residual risks

- The gateway terminates Logic TLS; its root account and HAProxy memory can
  observe native traffic. Harden and patch the gateway and rotate isolated FRP
  tokens after suspected compromise.
- A valid compromised Game Server can access its assigned roster and snapshots
  until its token or match is revoked. Token expiry, scope minimization, audit,
  and rapid OFFLINE/disable response limit the window.
- Native mappings not confirmed by real-client captures remain compatibility
  stubs. They must not be promoted by guesswork.
- Redis loss invalidates unconsumed Gate tickets but does not corrupt persistent
  player state. PostgreSQL loss is handled by the normal backup/restore runbook.

## Security verification

Release gates cover missing/forged tokens, banned players, IDOR, cross-server
access, concurrent Gate consumption, replay, malformed protobuf, Slowloris,
connection flood, scheduler concurrency, QoS amplification, race tests, fuzzing,
`govulncheck`, image scanning, SBOM, and build provenance. Alert on replay or
malformed-frame spikes, queue backlog, FRP disconnects, readiness failures, and
certificate expiry.

Report a suspected incident using the private operator channel; never attach raw
bearer tokens, Gate tickets, full protobuf frames, or loadout archives to a
public issue. Operational response is in the
[MetaServer runbook](../operations/runbooks/metaserver.md).
