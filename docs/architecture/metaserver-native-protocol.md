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
- Runtime packet capture and cold-start replay confirm that `PlayerRoleData`
  consumes the six equipment values in fields 1-7 plus three optional archive
  values: field 8 is the hex-encoded selected-weapon archive bundle, field 9 is
  the operator skin token, and field 10 is its ornament/painting ID.
  `UpdateWeaponArchiveV2` persists each archive by player and weapon ID;
  `GetPlayerArchiveV2` rebuilds field 8 from the role's selected weapon IDs so
  a later process can restore weapon parts and weapon cosmetics.
- Native `PlayerLevel` is configured by `META_NATIVE_PLAYER_LEVEL` (validated
  range `1..127`). The current pinned build reports 70 rows in
  `DT_PlayerLevelExp`, and the armory identifies locked rows as operator-level
  rewards, so production uses the build maximum of `70`.
- `UpdateRoleArchiveV2.Operation` is not a fixed slot number. The server routes
  by pinned item type first, then uses the observed operation only to choose
  between primary/secondary weapons or left/right pods. A skin-only update
  never clears an equipment slot.
- Successful `UpdateRoleArchiveV2` and `UpdateWeaponArchiveV2` responses must
  contain both the outer `ResponseWrapper.ErrorCode=0` bytes `18 00` and the
  inner status field bytes `08 00`. Even with both fields present, the pinned
  client can finish a persisted update with completion 404, or with 9002 on
  the equipment/weapon archive dispatcher. The Payload compatibility hook
  normalizes only those observed path-specific sentinels; every other native
  completion code remains unchanged.

The active Payload constructs the server-authoritative `LoadoutManager` only
in dedicated-server processes. The client never writes OwnedItems,
PersistentUser, or `PBFieldModManager + 0x98`, and it does not poll or maintain
an archive mirror. Runtime tracing established that this build receives a
valid `GetPlayerArchiveV2` response but does not dispatch it into either the
menu `PBCustomizeManager` cache or native `ClientInitFieldMod`. As the narrow
compatibility bridge, Payload performs one authenticated current-user loadout
read for each local-player lifecycle. It feeds each role/slot through the
version-pinned native character-slot completion entry exactly once per
`PBCustomizeManager`, and invokes `ClientInitFieldMod` exactly once per local
`APBPlayerState`. The completion entry performs the game's own cache update and
delegate broadcasts; Payload does not write the manager map or mutate equipment
state directly. Its only completion-code rewrite is the path-specific persisted
sentinel policy described above. Role quotas are read from the running build's
character definition table. `-NativeArchiveOnly` disables both calls and leaves
all client state read-only for diagnostics.

Servers can also use `-NativeArchiveOnly`, or independently disable
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
