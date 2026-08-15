# MetaServer native protocol

English | [简体中文](metaserver-native-protocol.zh-CN.md)

## Transport

Clients connect only through MetaTunnel to `logic.dubnium.top:443`. MetaTunnel
validates the public certificate with the Windows system trust store and TLS
server name `logic.dubnium.top`. The gateway terminates TLS and forwards the
byte stream through the isolated, TLS-enabled and token-authenticated Meta FRP
channel. No plaintext native listener is public.

Each frame is:

```text
uint32_be payload_length | protobuf RequestWrapper payload
```

The parser accepts fragmented and coalesced TCP reads. Payload length must be
between 1 and 2 MiB. A connection has handshake, frame-read, frame-write, and
idle deadlines. Responses are serialized through one bounded write queue.

## Gate

The first state-changing request must carry the single-use Gate Ticket returned
by the authenticated HTTP session endpoint. The server atomically consumes the
ticket and binds the connection to its player ID, auth session ID, client
version, protocol version, and issue time. The following terminate the
connection:

- expired, unknown, concurrent, or replayed ticket;
- protocol-version mismatch;
- identity mismatch in a legacy message;
- repeated malformed frames or protobuf;
- rate-limit or write-queue abuse.

## Confirmed RPC behavior

The production wrapper maps only reviewed RPC identifiers. Gate/status,
Party creation/readiness/presence, region discovery, playlist discovery,
matchmaking start, and matchmaking status/stop compatibility responses use
statically generated messages. Unknown RPCs receive a compatibility error
without crashing the connection.

Asset and loadout RPCs preserve the captured wire contract:

- `QueryAssets` uses the captured top-level success value `ItemCount=1`. The
  default `META_NATIVE_OWNERSHIP_MODE=full` returns all 40,462 rows from the
  pinned `DT_ItemType`, covering base items and generated per-slot,
  weapon-suite, and character-suite painting applications. Controlled
  diagnostics can use `compact` to exclude the 37,721 generated painting
  applications and reduce the response from about 1.31 MiB to 52,854 bytes.
  Per-row scalar metadata stays at its protobuf default because native
  ownership compares only the item ID.
- The pinned executable's embedded descriptor defines `RoleArchiveDataV2`
  with exactly fields 1-7: role ID, left/right pylon, mobility, melee,
  primary weapon, and secondary weapon. `GetPlayerArchiveV2` emits no fields
  above 7. Weapon-part archives remain independently persisted by
  `UpdateWeaponArchiveV2`; they are not embedded in a role row.
- Native `PlayerLevel` is configured by `META_NATIVE_PLAYER_LEVEL` (validated
  range `1..127`). The current pinned build reports 70 rows in
  `DT_PlayerLevelExp`, and the armory identifies locked rows as operator-level
  rewards, so production uses the build maximum of `70`.
- `UpdateRoleArchiveV2.Operation` is not a fixed slot number. The server routes
  by pinned item type first, then uses the observed operation only to choose
  between primary/secondary weapons or left/right pods. A skin-only update
  never clears an equipment slot.

The active Payload build contains only the server-authoritative
`LoadoutManager` bridge. Its client entry points are no-ops, and the former
OwnedItems/PersistentUser/FieldMod polling and write modules are not compiled.
Servers can use `-NativeArchiveOnly`, or independently disable
`-LoadoutBaselineBridge`, `-LoadoutPreOrderIntercept`,
`-LoadoutConfirmDeferral`, and `-LoadoutSpawnBridge` with `=0`, to isolate the
smallest bridge behavior still required after native-flow verification.

`QueryUnityMatchmakingRes` fields whose upstream numbers remain tentative are
not used to publish a match endpoint. The authoritative endpoint is available
through the authenticated HTTP ticket resource until a sanitized capture fixes
the native field mapping. This prevents an unverified field guess from entering
the production state machine.

## Packet handling rules

- Never log a complete frame or decoded loadout.
- Never echo unknown fields into a different player session.
- A keepalive does not extend Access Token or Gate Ticket validity.
- Frame and RPC limits apply before expensive protobuf or database work.
- Golden samples in `Backend/internal/metaserver/testdata/golden` are sanitized
  and contain no production identity or credentials.

Protocol provenance and the exact upstream commit are recorded in
`Backend/api/proto/metaserver/UPSTREAM.md`.
