# r14b joint flow-fix record

English | [简体中文](README.zh-CN.md)

This iteration covers the `ProjectRebound` Backend / Payload and the regular `ProjectReboundToolbox` repositories. The scope runs from creating and joining a lobby, ready and freeze, and strict-roster admission through native result handling, exit cleanup, and creating a new lobby for the next match. P2P direct, Relay, VNT, and Dedicated use the same completion constraints. BattleLog upload, experience, and reward settlement are outside this iteration.

## Protocol and behavior

The existing `strict-roster-v2` continues to own admission; the independent `match-lifecycle-v1` capability is added. An old Payload may be diagnosed, but a new Toolbox must not promote it to Playable.

```text
OPEN -> FROZEN -> PROVISIONING -> CONNECTING -> RUNNING
                                                  |
                                      RESULT_CONFIRMED (seq=1)
                                                  v
                                                ENDING
                                                  |
                                        RETURN_READY (seq=2)
                                                  v
                                             COMPLETED
                                                  |
                                  native clear + transport close
                                                  v
                                      cleanup_state = CLEARED
                                                  |
                                          Create new lobby / next match
```

| Boundary | Implementation constraint |
| --- | --- |
| Create / join succeeds, ready request fails | Keep the confirmed seat immediately, restore the same lobby, and allow ready to be retried. |
| Admission and start | Continue using the frozen roster, signed allocation, native Grant, Reserve / Confirm, and role quorum; do not add a bypass. |
| Normal result | The current native authority's result phase produces it, bound to the attempt, authority session, world, roster revision, route generation, and match generation. |
| Ending window | Backend enters ENDING, rejects new admission and reconnect, and keeps the existing network and ending-receipt channels. The default window is 120 seconds. |
| Exit | RETURN_READY must follow RESULT_CONFIRMED; the native queue keeps the receipt until an explicit ACK. |
| Retry and restart | Toolbox writes receipts to a DPAPI-encrypted file with flush and atomic replacement; HTTP replay is idempotent, and late seq=1 or seq=2 after a confirmed result does not change the result. |
| Abnormal exit | An unconfirmed result follows the abnormal-end path; after Backend confirms a result, explicit user exit, process loss, fatal transport, or timeout still keeps COMPLETED and records a warning and cleanup state. Normal polling does not use this early-exit path. |
| Cleanup | Native process / allocation, room members, connections, VNT, and Relay resources must all be released. A failed close or revoke stays PENDING and is retried. |
| Next match | Local frontend cleanup must not overwrite Backend PENDING; a player from an uncleared old match cannot bypass the gate through another lobby. |
| Delayed events | UI and runtime events are constrained by session, lobby, attempt, operation, and run; an old exit notice cannot clear a new match. |

The lifecycle endpoints are `POST /v1/match-attempts/{attempt}/host/lifecycle` and `POST /v1/game-servers/{id}/match-attempts/{attempt}/lifecycle`. The authority session is carried only in authenticated headers and is not placed in the public snapshot. The database must be migrated to **49** first; this iteration did not migrate an existing online database.

## Recovery boundaries

The native cleanup record stores the PID and process creation time from this launch. After restart, only a process handle with an exact match may be reopened; if the PID was reused, its pipe is not connected and the process is not terminated. Real exit evidence that was obtained may be persisted for retry. If both Toolbox and the child process have disappeared before exit evidence was durably recorded, cleanup remains blocked; a missing PID lookup cannot be fabricated into an exit receipt.

The local encrypted recovery file must not be distributed or copied across accounts. The package contains no runtime `.dpapi` files, account configuration, tokens, raw logs, or signing private keys.

## Physical acceptance matrix

Each mode requires three independent Steam accounts; automated tests in this iteration cannot replace that evidence.

| Mode | Create / join / ready | Real admission / spawn / control | Native result / return | Resource release / next lobby |
| --- | --- | --- | --- | --- |
| P2P direct | NOT_RUN | NOT_RUN | NOT_RUN | NOT_RUN |
| Relay | NOT_RUN | NOT_RUN | NOT_RUN | NOT_RUN |
| VNT | NOT_RUN | NOT_RUN | NOT_RUN | NOT_RUN |
| Dedicated | NOT_RUN | NOT_RUN | NOT_RUN | NOT_RUN |

Also run these separately: lost ready responses, temporary network loss during result reporting, duplicate and late seq=1/2, disconnect during the ending phase, host exit after the result, temporary Relay revoke failure, Toolbox restart, an old-lobby notice arriving after a new match, and PID-reuse rejection. Record the last successful phase, failure code, and cleanup state each time; do not record credentials or complete player identities.

The fixed game EXE SHA-256 is `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`. This iteration does not change verified admission capability flags, use console direct-connect, or treat the main menu, a readable pipe, a successful build, or a test fixture as native admission success.

## Build and verification

Automated results and package hashes are in the verification record and final package manifest in this directory. The test package uses a new file directory and retains the old r14b artifact; `release_ready=false`. The new package requires the matching schema 49 Backend and cannot rely on the old claim that the existing Backend already satisfies the requirements.
