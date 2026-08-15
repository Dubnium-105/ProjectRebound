# Boundary Armory Frida Probes

English | [简体中文](README.zh-CN.md)

These scripts correlate the Meta `QueryAssets` response with the native
`UPBArmoryManager::OwnedItems` array and `HasItem` results. The default probe
does not write game memory, replace return values, or record authentication
tokens. Both Python controllers reject any executable whose SHA-256 is not
`181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`.

## Running

Start MetaTunnel and the game, then attach automatically:

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

After entering the game, follow `NO -> customization -> HR&Armory`. Inspect a
default item, an item that should be unlocked but appears locked, and an item
used by the online loadout, then enter one match.

## Important events

- `rpc.query_assets`: the item-row count, `item_count`, and the distributions
  of the three unknown integer fields received by the client.
- `rpc.player_archive`: a summary of the online role loadouts received by the
  client.
- `armory.snapshot` / `armory.changed`: native inventory size, Count
  distribution, and NewItemCounter values.
- `armory.has_item`: array presence, Count, bIsNew, and the native result.
- `persistent_user.snapshot`: Saved and Runtime inventory sizes and set hashes.
- `fieldmod.native_call`: `ClientInitFieldMod`, both native refresh RPCs,
  selection calls, getters, and weapon-spawn boundaries.
- `fieldmod.snapshot`: per-role pre-ordering slots before and after those calls.
- `progression.player_level_table`: runtime PlayerLevelExp row count and highest
  numeric level for this exact executable.
- `unreal.lifecycle`: snapshots around the native armory-entry lifecycle.

Interpret the results as follows:

| Result | Meaning |
| --- | --- |
| `present=true,count=0,return_value=true` | In this build, native `HasItem` matches only the FName; Count is not an ownership gate. |
| `present=true,count=0,return_value=false` | The probe offset or target build changed and must be checked again in IDA. |
| RPC contains the ID but OwnedItems does not | QueryAssets was not committed as inventory state, or it was overwritten. |
| `HasItem=true` but the UI is locked | This is an item-type or UI compatibility filter, not ownership. |
| The armory is correct but changes after entering a match | Match initialization overwrote the inventory or loadout. |

IDA analysis of the current Steam build shows that
`UPBArmoryManager::HasItem` iterates `FPBItem` entries with a 0x10-byte stride
and compares only the `FName` at offset 0x0. It does not read Count at offset
0x8.

Run `run_query_assets_status_ab.py --script query_assets_observe.js` for a
read-only baseline. It reports the stable QueryAssets protobuf prefix without
modifying the receive buffer.

The QueryAssets A/B probes are retained as regression diagnostics. Runtime
inspection of the native consumer at `0x1416DF990` established that the
top-level field named `ItemCount` is actually a result/status value: values
above 299 are rejected before any ItemData row is examined. The captured
success value is `1`; the repeated ItemData array independently contains all
40,462 rows. The `UserAsset` candidates produce only the 268-row fallback and
are not used by MetaServer.

The native completion path was subsequently resolved as
`FOnlineAsyncTaskQueryAssets -> LogicServer delegate -> PBArmoryManager`. The
task has a hard five-second deadline and copies only each row's `ItemId`; it
does not read the three scalar fields. A production-sized 1,615,627-byte
response completed as `result_code=-1` with zero committed rows at 5.03
seconds, even after the heavyweight decoder was removed. MetaServer therefore
keeps every deduplicated ItemId and omits the unused default-valued scalars,
reducing the deterministic payload to 1,372,853 bytes without changing the
ownership set. The immutable serialized response and its observability digest
are cached once, so subsequent armory entries do not rebuild, unmarshal, or
rehash all 40,462 rows on the synchronous response path.

`logic_server_armory_probe.js` is the read-only probe for that final native
path. It records the concrete LogicServer virtual targets, QueryAssets delegate
result/count, subscriber callback, and the armory size before and after the
broadcast. It never changes the receive buffer or game memory.

`query_assets_single_item_ab.js` is the second-stage A/B probe. It preserves
the frame length, exposes only one ItemData row (`PEACE_RU-AKM` by default), and rewrites
the top-level ItemCount to an equal-width encoding of one. This distinguishes
between the entire oversized heterogeneous asset list being rejected and the
three integer fields still having incorrect semantics. The script also checks
the complete 1,615,627-byte QueryAssets payload window so it cannot match
another RPC response accidentally.

Use `run_query_assets_status_ab.py --target-item PEACE_GSW-IDW` with this
script to select a normally locked row for a discriminating ownership test.
Add `--top-level-value 0` to test whether field 1 is a success status rather
than an item count; the default value remains 1.

`query_assets_user_asset_ab.js` tests the runtime-reflected schema candidate
directly. It hides the old top-level count, exposes only the selected row as a
field-1 `UserAsset`, and preserves all buffer and frame lengths. Run it with
`--script query_assets_user_asset_ab.js --target-item PEACE_GSW-IDW`.

`player_archive_level_ab.js` parses the complete native frame and protobuf
wrapper, then rewrites only the one-byte top-level PlayerLevel. Run controlled
low/high arms with the hash-verifying controller:

```powershell
python .\Tools\Frida\run_query_assets_status_ab.py --pid 1234 --output .\level-low.jsonl --script player_archive_level_ab.js --target-player-level 1
$maxLevel = 100 # replace with progression.player_level_table.maximum_numeric_level
python .\Tools\Frida\run_query_assets_status_ab.py --pid 1234 --output .\level-high.jsonl --script player_archive_level_ab.js --target-player-level $maxLevel
```

`persistent_armory_probe.js` is a one-shot read-only comparison of
`PBPersistentUser_BP_C::ArmorySaved`, its runtime `Armorys`, and
`UPBArmoryManager::Armorys`. Run it against an existing game process with:

```powershell
python .\Tools\Frida\run_query_assets_status_ab.py --pid 1234 --output .\persistent.jsonl --script persistent_armory_probe.js
```

After receiving `probe.done`, press `Ctrl+C` to exit. The probe does not write
game memory.
