# Dedicated server multi-match

English | [简体中文](dedicated-server-multimatch.zh-CN.md)

This opt-in feature runs an ordered sequence of dedicated matches in one process. Defaults remain unchanged: without a valid opt-in configuration, the server notifies clients, performs native final cleanup, exits, and lets the wrapper start a new process. P2P/listen servers always retain the native single-match boundary.

## Fixed-build static evidence

- RVA `0x036D61B0` has the Win64 `UWorld*, FString*, bool, bool` ABI, reads AuthorityGameMode from `UWorld +0x118`, copies the URL to `UWorld +0x5F0`, and returns a bool. This identifies it as `UWorld::ServerTravel` from behavior rather than from a string-only address guess.
- Its two direct callers in the pinned `.text` image are `0x032948EC` and `0x0350B0F4`; the first is inside the native `AGameMode::RestartGame` body at `0x03294830`. Same-map restart and cross-map travel therefore share the native Engine boundary.
- The PB post-match state machine still has no next-match caller. By default, `WaitingToEndGame 0x0162B1C0` reaches final cleanup `0x0163EFD0` and `RequestExit(false) 0x019EFEE0`. Multi-match adds an explicit, minimal Engine travel branch only when opted in; it does not claim that a dormant PB rotation flow was recovered.

## Configuration

Add this section to the wrapper's `serverconfig.json`:

```json
{
  "map": "Warehouse",
  "mode": "pve",
  "multiMatch": {
    "enabled": true,
    "playlist": ["Warehouse", "OSS", "DataCenter"],
    "travelTimeoutSeconds": 45,
    "vote": {
      "enabled": true,
      "durationSeconds": 15,
      "candidateCount": 3
    }
  }
}
```

The console `ProjectReboundServerWrapper` canonicalizes map names and rejects unknown, duplicate, or PVE-incompatible entries. Supported values are:

- `travelTimeoutSeconds`: 10–180;
- `vote.durationSeconds`: 0–60;
- `vote.candidateCount`: 1–3;
- PVE maps: `OSS`, `MiniFarm`, `Warehouse`, `DataCenter`, and `CircularX`.

Only a valid enabled configuration adds `-DedicatedMultiMatch` and the absolute `-multimatchconfig` path to the child process. An invalid configuration safely falls back to the existing one-match-per-process mode.

## Between-match flow

The server preserves the native result freeze, result screen, and `MatchEnding` presentation. At `WaitingToEndGame`, only an explicitly enabled Dedicated NetMode 1 instance branches:

1. Candidate maps follow playlist order and skip the current map by default.
2. Players vote with `/vote 1`, `/vote 2`, or `/vote 3`; ties select the earliest playlist candidate.
3. A same-map transition calls native `RestartGame`; a cross-map transition calls the pinned-build native `UWorld::ServerTravel` with seamless travel enabled.
4. New connections are rejected during the travel window. A new match generation is committed only after a new Dedicated GameMode/GameState exists, the original NetDriver belongs to the new World, and the exact pre-travel connection set remains continuous.
5. The payload clears per-match pointer caches and rebinds persistent PlayerController, loadout, and late-join flows.

Travel failure, timeout, a disconnect during migration, or a World/NetDriver/connection continuity mismatch restores the native client return-to-menu notification and final exit. The wrapper then resumes from the next playlist map in a new process. If the authoritative GameMode is already unavailable, the last-resort fallback can only request process exit and may still appear as a disconnect to clients; live fault injection must validate this degraded path separately.

Runtime status JSON exposes `lifecycleState`, `activeMap`, `nextMap`, `matchGeneration`, and `vote`. Common states are `Running`, `Voting`, `Traveling`, `LoadingNext`, and `FallbackExit`.

## Acceptance

The deployed implementation requires live validation; a successful build does not prove connection continuity. Attach the read-only probe to the server and at least one client:

```powershell
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1 -ProcessId <PID>
```

Inspect `events.jsonl` for `match.lifecycle`, `match.native_boundary`, and `match.server_travel`. Success requires:

- one unchanged server PID across at least three matches;
- a true `ServerTravel` result with a continuous NetDriver pointer and original connection set;
- a new World or GameMode/GameState and an incremented `matchGeneration`;
- no client MainMenu, Inactive, `HandleNetworkError`, or `HandleTravelError` transition;
- working role selection, spawn, loadout, respawn, late join, and reconnect during each `Running` phase, with new joins rejected during travel and migration disconnects triggering safe fallback;
- a travel-failure injection that falls back to native exit and wrapper recovery in a new process.

All RVAs and layouts are pinned to the repository's supported executable SHA-256 and must be revalidated for any other game build. This delivery has static/build/test validation only: no DLL deployment, game launch, Frida attachment, or live server match was performed. In particular, native ReplicationGraph/channel cleanup after seamless travel, destination-world recognition, and connection continuity remain runtime proof gates. The duplicated GUI launcher wrapper is not yet a multi-match configuration consumer; only its extra empty map-table entry was corrected.
