# Dedicated server multi-match

English | [简体中文](dedicated-server-multimatch.zh-CN.md)

This opt-in feature runs an ordered sequence of dedicated matches in one process. Defaults remain unchanged: without a valid opt-in configuration, the server notifies clients, performs native final cleanup, exits, and lets the wrapper start a new process. P2P/listen servers always retain the native single-match boundary.

Static analysis of the pinned image confirms that RVA `0x036D61B0` has the Win64 `UWorld*, FString*, bool, bool` ABI, reads `UWorld +0x118`, copies the URL to `UWorld +0x5F0`, and returns a bool. Its two direct callers are `0x032948EC` and `0x0350B0F4`; the first is inside the native `AGameMode::RestartGame` body at `0x03294830`. The PB post-match state machine still has no next-match caller: by default it reaches final cleanup and `RequestExit(false)`. Multi-match therefore adds an explicit, minimal Engine travel branch; it does not claim that a dormant PB rotation flow was recovered.

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

The wrapper canonicalizes map names and rejects unknown, duplicate, or PVE-incompatible entries. Supported ranges are 10–180 seconds for travel timeout, 0–60 seconds for vote duration, and 1–3 candidates. PVE supports `OSS`, `MiniFarm`, `Warehouse`, `DataCenter`, and `CircularX`.

Only a valid enabled configuration adds `-DedicatedMultiMatch` and the absolute `-multimatchconfig` path to the child. Players vote with `/vote <number>`. Ties deterministically choose the earliest playlist candidate.

The payload preserves the native result and match-ending presentation, then branches at `WaitingToEndGame` only for Dedicated NetMode 1. It uses native `RestartGame` for the same map and the pinned-build `UWorld::ServerTravel` for a different map. New connections are rejected during the travel window. A new match generation is committed only after a new dedicated GameMode/GameState exists and the original NetDriver and exact pre-travel connection set remain continuous. Travel failure, timeout, a disconnect during migration, or continuity mismatch restores native return-to-menu/final exit so the wrapper can recover on the next playlist map in a new process. If the authoritative GameMode is already unavailable, the last-resort fallback can only request process exit and may still appear as a disconnect to clients.

Runtime status exposes `lifecycleState`, `activeMap`, `nextMap`, `matchGeneration`, and `vote`. Deployment validation must cover at least three matches with unchanged server PID and connections, no client MainMenu/Inactive/network or travel error, working role selection/spawn/loadout/respawn/late join and reconnect while `Running`, rejected new joins during travel, and a tested process-restart fallback for a migration disconnect. The read-only Frida probe emits `match.lifecycle`, `match.native_boundary`, and `match.server_travel` evidence.

All RVAs and layouts are pinned to the repository's supported executable SHA-256 and must be revalidated for any other game build. This delivery has static/build/test validation only: no DLL deployment, game launch, Frida attachment, or live server match was performed. In particular, native ReplicationGraph/channel cleanup after seamless travel, destination-world recognition, and connection continuity remain runtime proof gates. The duplicated GUI launcher wrapper is not yet a multi-match configuration consumer; only its extra empty map-table entry was corrected.
