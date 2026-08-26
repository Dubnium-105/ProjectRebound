# Boundary Armory Frida Probes

English | [简体中文](README.zh-CN.md)

These tools correlate the Meta `QueryAssets` response with native armory, archive, FieldMod, PlayerState, and match-spawn state for the pinned Boundary executable. The normal workflow is read-only: it does not write game memory, replace return values, or record authentication tokens. Controllers must reject an executable whose SHA-256 is not the pinned value documented in `.agents/skills/debug-boundary-native/references/current-findings.md`.

## Current workflow

Use the unified probe for normal diagnosis:

```powershell
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1
```

Attach to an already running game:

```powershell
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1 -AttachOnly
```

Attach to a specific process:

```powershell
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1 -ProcessId 1234
```

The default log directory is:

```text
%LOCALAPPDATA%\ProjectRebound\frida-captures\YYYYMMDD-HHMMSS\events.jsonl
```

After entering the game, follow `NO -> customization -> HR&Armory`, inspect representative items and loadouts, then enter a match when spawn-path validation is required.

## Important events

- `rpc.query_assets`: QueryAssets status and item-row summary received by the client.
- `rpc.player_archive`: online role-loadout summary received by the client.
- `armory.snapshot` / `armory.changed`: native inventory size and state changes.
- `armory.has_item`: native ownership lookup evidence for selected IDs.
- `persistent_user.snapshot`: saved/runtime inventory comparison hashes.
- `http.message`: loopback HTTP method/path or response status plus body size/hash; authorization, cookies, query credentials, and bodies are not recorded.
- `player_state.snapshot` / `player_state.changed`: selected/possessed role IDs and native pre-ordering/equipping maps.
- `fieldmod.native_call`: `ClientInitFieldMod`, native refresh calls, selection/getter boundaries, weapon-spawn boundaries, and reflected match-loadout RPCs.
- `fieldmod.snapshot`: per-role pre-ordering state around those calls.
- `match.native_boundary`: native server pre-order, role confirmation, and possession-time promotion boundaries.
- `match.server_travel`: read-only ServerTravel URL/argument/result evidence with World, GameMode, GameState, NetDriver, and connection snapshots.
- `progression.player_level_table`: runtime level-table summary for the exact executable.
- `unreal.lifecycle`: armory lifecycle boundaries.

Interpret ownership evidence as follows:

| Result | Meaning |
| --- | --- |
| `present=true,count=0,return_value=true` | Native `HasItem` matches the item FName; Count is not an ownership gate in the pinned build. |
| `present=true,count=0,return_value=false` | The target build or probe layout changed; re-validate in IDA before changing code. |
| RPC contains the ID but OwnedItems does not | QueryAssets was not committed as native inventory state, or it was later overwritten. |
| `HasItem=true` but the UI is locked | Investigate item-type, progression, compatibility, or UI filtering rather than ownership. |
| Armory state is correct before a match but diverges during spawn | Investigate FieldMod/PlayerState/spawn application rather than QueryAssets. |

The current evidence for the pinned build is maintained in `current-findings.md`: QueryAssets field 1 is a result/status value, repeated field 2 contains the owned item rows, the native completion path is `FOnlineAsyncTaskQueryAssets -> LogicServer delegate -> PBArmoryManager`, and the deterministic full-ownership response is 1,372,853 bytes. The client frame patch raises the four linked 1 MiB constants atomically to 2 MiB only for the exact supported executable hash.

## Historical and A/B probes

`query_assets_*_ab.js`, `player_archive_level_ab.js`, `fieldmod_native_probe.js`, `persistent_armory_probe.js`, and similar one-off scripts are retained only as historical regression evidence. They are **not** part of the default diagnostic or production workflow.

Some of those scripts intentionally rewrite buffers, return values, frame metadata, or selected fields. Their hard-coded payload lengths, offsets, and experimental assumptions describe the capture they were created for and must not be treated as current production constants. In particular, old references to a 1,615,627-byte QueryAssets window are historical and do not describe the current deterministic response.

Only reuse an A/B probe when all of the following are true:

1. the executable SHA matches the probe's pinned build;
2. `current-findings.md` still identifies the same hypothesis as unresolved or worth re-testing;
3. the experiment changes one variable at a time and can be reverted immediately;
4. results are compared against a read-only `armory_probe.js` baseline;
5. any newly confirmed address, layout, or protocol semantic is moved into `current-findings.md` instead of leaving the A/B script as the source of truth.

`logic_server_armory_probe.js` remains useful as a read-only focused view of the native QueryAssets completion path, but new general observability should still be added to `armory_probe.js` first.
