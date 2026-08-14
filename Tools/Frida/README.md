# Boundary Armory Frida Probes

English | [简体中文](README.zh-CN.md)

These scripts correlate the Meta `QueryAssets` response with the native
`UPBArmoryManager::OwnedItems` array and `HasItem` results. The default probe
does not write game memory, replace return values, or record authentication
tokens.

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

`query_assets_status_ab.js` is a separate reversible A/B probe. It rewrites
the first field of the current QueryAssets payload from 40462 to an equal-width
encoding of zero to determine whether the field is an item count or a
status/reserved value. This script modifies the local game's receive buffer,
so it is not part of the default read-only probe. Stop the probe and restart
the game to restore the original behavior.

`query_assets_single_item_ab.js` is the second-stage A/B probe. It preserves
the frame length, exposes only the `PEACE_RU-AKM` ItemData row, and rewrites
the top-level ItemCount to an equal-width encoding of one. This distinguishes
between the entire oversized heterogeneous asset list being rejected and the
three integer fields still having incorrect semantics. The script also checks
the complete 1,615,627-byte QueryAssets payload window so it cannot match
another RPC response accidentally.

`persistent_armory_probe.js` is a one-shot read-only comparison of
`PBPersistentUser_BP_C::ArmorySaved`, its runtime `Armorys`, and
`UPBArmoryManager::Armorys`. Run it against an existing game process with:

```powershell
frida -p 1234 -l .\Tools\Frida\persistent_armory_probe.js
```

After receiving `probe.done`, press `Ctrl+C` to exit. The probe does not write
game memory.
