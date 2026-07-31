# BattleLog raw match extraction

English | [简体中文](README.zh-CN.md)

`BattleLogExtractor` runs synchronously on the Unreal game thread after selected
`ProcessEvent` calls. It copies post-match SDK state into detached JSON and then
writes that JSON to disk. No `UObject` is read from a worker thread.

## Output

The default directory is:

```text
%LOCALAPPDATA%\ProjectRebound\battlelog-dumps
```

If `LOCALAPPDATA` is unavailable, the fallback is
`<process-current-directory>\battlelog-dumps`.

Each ending stage gets a separate file:

```text
<pve|pvp|unknown>\<timestamp>_<sequence>_<pve|pvp|unknown>_<server|client>_<match-id>_<trigger>.json
```

The game/server console prints the absolute path with a `[BATTLELOG]` prefix.
Repeated calls to the same stage are de-duplicated until the next match-start
event or `UWorld` change.

## P2P v3 sealing

Launcher enables client-observer v3 output by setting these non-secret process
environment values before injection:

```text
PROJECT_REBOUND_P2P_MATCH_ID=p2pm_...
PROJECT_REBOUND_P2P_CAPABILITY_ID=p2rc_...
PROJECT_REBOUND_P2P_SERVER_NONCE=p2n_...
PROJECT_REBOUND_CLIENT_VERSION=<launcher-version>
PROJECT_REBOUND_P2P_AUTHORITY_KIND=CLIENT_OBSERVER
```

Use `LISTEN_HOST_OBSERVER` only for the listen host. The report token is
deliberately absent from this contract and must remain in Launcher memory.

When the three context IDs are valid, the extractor emits schema v3. It creates
an initial `PARTIAL` at match start, another bounded checkpoint after each
round, and one `FINAL` at the result-screen trigger. Events form a SHA-256 chain
rooted in `match_id|capability_id|server_nonce|timeline_session_id`. Files are
written to a `.tmp` sibling and atomically renamed to `*.json.ready`; Launcher
must scan only `.ready` files. Dedicated-server extraction without these
variables remains schema v2 and keeps the existing `.json` behavior.

## PvE and PvP classification

Schema version 2 emits `match_classification.type` as `pve`, `pvp`, or
`unknown`. Dedicated-server snapshots use `Config.IsPvE`, populated from the
server's `-pve` launch flag, as the authoritative source when set. Explicit
runtime metadata such as `Rush_PVE_Normal` can identify PvE even if a launcher
provided the PvE mode path but omitted `-pve`; the override and both pieces of
evidence are retained in JSON. Client snapshots use the same SDK mode metadata,
while ambiguous client metadata is not treated as authoritative.

Files are separated into `pve`, `pvp`, and `unknown` subdirectories. Each JSON
also contains:

- `participant_summary`: combined, human, AI, and per-team aggregates.
- `pve_record`: result and participant summary for a PvE match, otherwise null.
- `pvp_record`: result and participant summary for a PvP match, otherwise null.

This keeps the full raw `players` array unchanged while giving downstream
battlelog persistence an explicit discriminator and separate PvE/PvP records.

## MetaServer ingestion

The server-side ingestion contract accepts the complete schema-v2 object as the
`snapshot` property of:

```text
PUT /internal/v1/meta/battlelog/reports/<report-id>
```

The caller supplies its scoped Game Server Token and `X-Game-Server-Id`. The
report ID is stable across retries; the current dump filename without the
`.json` suffix is a valid transitional report ID. The MetaServer hashes the
canonical JSON, so an identical retry is safe while different content under the
same ID is rejected.

The snapshot never determines a player's authentication level. The backend
uses the `unverified`, `verified`, or `trusted` roster snapshot captured when a
managed match was reserved. Reports without a managed match remain non-official.

## Captured sources

- `APBPlayerState`: replicated/raw counters, identity fields, role/team
  assignment, derived SDK getter values, outcome flags, role/character score
  maps, and `FPBInGameData`.
- `APBGameState`: map/mode/timing/round/team state, `FPBMatchResult`, and the
  last `FPBRoundResult`.
- Server `APBGameMode::GetMatchResultInfo(PlayerState)`: the personalized
  `FMatchResultInfo` for every player.
- `ClientMatchHasEnded` parameters: the exact `FPBMatchResult` delivered by
  the RPC.
- `UPBGameInstance::SaveMatchResultInfo` parameters and
  `LocalMatchResultInfo`: the client UI-facing `FMatchResultInfo`.
- `UPBCareerManager::GetLastPostMatchSettlementData`: team/member settlement
  and medal data when available.

Raw enum numbers are always emitted together with their known SDK names.
Pointer addresses and Unreal object paths are diagnostic correlation fields;
they are not stable battlelog identifiers.

## First validation pass

Run one complete match with the payload injected into the dedicated server and
one client. Keep all files from both processes. Compare these stages first:

1. `ClientMatchHasEnded`
2. `SaveMatchResultInfo`
3. `K2_StartShowingMatchResult`
4. `GetLastPostMatchSettlementData`

The later battlelog persistence schema should be based on values observed in
these dumps, especially the exact identity field, score semantics, match-time
unit, team numbering, and whether accuracy/KDA are already normalized.
