English | [简体中文](TEST-MATRIX.zh-CN.md)

# Three-machine test record

Use the existing schema 48 backend to collect managed native admission evidence. `native_authority_admission_verified=false` remains an independent build gate: collection neither changes it nor publishes Playable. Without an actual three-machine receipt, keep `NOT_RUN`.

## First round

1. A is the owner; B and C are members. Use three physical Windows machines and separate Steam sessions. Verify the same package ID, ZIP SHA-256, and fixed game version. Install, then run `Check-ThisMachine.ps1` while the game is stopped.
2. Each player logs into Toolbox. A creates an authoritative P2P lobby; B and C refresh and join distinct seats in that same lobby. Use the team buttons in the authoritative match card if a team change is needed. The current team, full teams and a frozen roster are unavailable for switching. Finish team changes before confirming readiness. Choose only an offered transport. If VNT is unavailable, record `BLOCKED: transport_unavailable` for that scenario.
3. After everyone joins, each person, including the owner, clicks **Ready** in the authoritative match card and confirms their own ready state. Joining, leaving or changing teams resets readiness; everyone must confirm again after the roster changes. An earlier "owner ready" creation log does not describe current readiness. A waits for three real ready seats and `local.can_start=true`, then selects **Collect native proof**. Do not use ordinary **Freeze roster and start** for this round.
4. The owner freezes the roster through the normal API, receives a signed allocation, and starts native authority. Members launch and connect through the managed flow. Press SPACE if the game requests platform login. Record native login separately from a successful profile HTTP request.
5. Record each machine's last reached stage and error. For `P2P_AUTHORITY_LAUNCH_FAILED`, also record the following cause, lobby/Attempt, operation/run, and time; the generic code alone cannot identify a new failure. A successful owner receipt must bind to this frozen roster and its connections. Three players require both B and C as unique remote seats; reconnecting one player cannot count as another player.
6. Managed cleanup ends the Attempt and reclaims game, connections, and transport. Verify completion before a new round. For `cleanup_pending`, retain correlation and the error; do not record a pass or reuse immediately.
7. Run `Check-ThisMachine.ps1 -Collect` on each machine. Keep evidence outside the package, with separate `round_id/A`, `round_id/B`, and `round_id/C` preflight, collector JSON, and result files.

**Native admission proof captured** describes only this diagnostic receipt. A member subsequently reporting `native_admission_unverified` needs a separate result; do not report three playable players. Record a single remote seat, timeout, or login failure as observed. Do not fabricate seats or edit receipts.

## Scenarios and pass conditions

| Scenario | Required evidence | Initial state |
|---|---|---|
| Installation, versions, dependencies | Every machine meets all required manifest checks | NOT_RUN |
| Toolbox login | Independent Steam authentication completed; lobby list readable | NOT_RUN |
| Native game login | Native login completed in the game process for this package | NOT_RUN |
| Three-machine admission collection | Actual B and C unique remote connections for this round, with completed cleanup | NOT_RUN |
| Second cold-start collection | Exit round processes; collect and clean a new round/Attempt | NOT_RUN |
| Two- and three-player full matches | Every player selects, spawns, controls a character, and has Playable evidence | BLOCKED: native build capability unverified |
| Delayed frozen member | Same frozen identity, team, and seat admitted without an extra seat | BLOCKED: full-match prerequisites incomplete |
| Reconnect and route migration | New generation effective, old connection terminal, roster unchanged | BLOCKED: full-match prerequisites incomplete |
| Owner exit, completion, next match | Old authority revoked; reuse only after cleanup | BLOCKED: full-match prerequisites incomplete |

The coordinator separately organizes native rejection cases such as wrong audience, expiration, and replay. Ordinary testers do not edit credentials. Docker/Relay CI cannot replace physical game acceptance. Do not disable strict rosters, use empty tokens or console `open`, or create retired rooms.

## Result per machine

Save `match-result.json` with the actual package ID and UTC times. Opaque correlation may contain lobby, Attempt, route generation, and operation/run markers. Exclude platform IDs, account names, IP addresses, and credentials.

```json
{
  "round_id": "round-01",
  "machine": "A",
  "package_id": "read from package manifest",
  "scenario": "native_admission_collection_three_machines",
  "result": "NOT_RUN",
  "started_at_utc": null,
  "finished_at_utc": null,
  "last_successful_step": null,
  "observed_error_code": null,
  "unique_remote_seats_observed": null,
  "cleanup_result": "NOT_RUN",
  "playable_result": "NOT_RUN",
  "correlation": {},
  "notes": ""
}
```

Use only `PASS`, `FAIL`, `BLOCKED`, or `NOT_RUN`. Unexecuted work is `NOT_RUN`; missing machines, dependencies, or service conditions are `BLOCKED` with a reason; executed work violating the pass condition is `FAIL`. Success at only some stages cannot make the whole scenario `PASS`.

Return only collector JSON and sanitized observations, excluding raw client logs, Steam data, tokens, Grants, Tickets, configuration, backup DLLs, and databases. Scripts do not upload automatically. Remove local paths from installation receipts before sharing.
