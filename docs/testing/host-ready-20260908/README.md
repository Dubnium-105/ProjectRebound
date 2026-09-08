English | [简体中文](README.zh-CN.md)

# r6 host-ready hardware test record — 2026-09-08

This directory is an append-only r6 record over the r5 and r4 records. It is a test-package and evidence record, not release approval. The r6 package is built and the scoped one-machine desktop regression passed for the tested Ready/team/leave flow. The update-channel version check is not passed because no signed update is published.

## Current r6 source and package binding

- Toolbox source commit: 3ff5ca2447621cfc018c3c8e65dce28705bd7cb9
- r6 test execution parent: 995625903685b61770da6e8139731b50a4dfb0eb
- Payload source: 98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d
- Payload SHA-256: 8d471662e947bcb6baf3b0380e23f90bef614f76ac9e7e8a82e2cfbf48619183
- r6 package: C:/wksp/ProjectRebound/artifacts/hardware-test-20260908-r6/rebound-hardware-test-20260908-r6-windows-x64.zip
- ZIP size: 23,489,707 bytes
- ZIP SHA-256: bc6e21d54d35d1408c609c042120ce47109e4e9968c1f4df34c5efc3841e3cd0
- signed Toolbox SHA-256: bef5cc75a95f741f70c69ef27281fcb13e44eed6cdbebec2448e5420cfc23058
- Backend schema: 48; custom_protocol=true; lab_testing=false
- native_authority_admission_verified=false; release_ready=false

The r6 frontend tests ran against the stable 9956259 source with five frontend files changed in the worktree before the r6 source commit was created. The execution receipt records the exact five tested source-file hashes and the parent commit; 3ff5ca is the post-test source binding. This preserves execution provenance and does not claim post-commit test execution. The byte-to-commit check is recorded in evidence/frontend-r6/committed-source-binding.json.

The Backend update receipt records reversible archival of four incompatible old Toolbox releases, with eight audit records. Both regions now return 200 for health, valid stable update checks and downloads; the unpublished Toolbox channel truthfully returns UPDATE_NOT_FOUND/404. Backend source and signing checks were unchanged, and CI was not rerun for this data operation.

Implementation commits: 4af32fe adds Ready/team controls; f5338a2 supplies authenticated identity; c156a44 fixes the mock and toolbar; a51fb44 adds snapshot scope checks; 9e661ed adds frontend regression coverage; 9956259 scopes runtime DTO/events and fixes the Tauri command-count assertion; 3ff5ca fixes leave acknowledgement ordering.

## r6 product delta

The r6 frontend fixes the observed event-before-ack leave race and adds regression coverage for superseded sessions and lobbies. A terminal snapshot with local.is_member=true can accept the matching leave acknowledgement for the retired scope. Old results cannot clear a newer session or lobby; the checks remain bound to both identifiers.

## Actual r6 checks

| Area | Result | Evidence and limit |
|---|---|---|
| Frontend r6 behavior tests | PASS, 55 unique tests: 13 authoritative-room, 15 bridge, 27 UI | evidence/frontend-r6/execution-receipt.json and related-tests.log. The earlier 55-test intermediate run is duplicate and is not counted. |
| Frontend production build | PASS, exit 0 | evidence/frontend-r6/frontend-build.log |
| Site preparation | PASS, exit 0 | evidence/frontend-r6/prepare-sites.log |
| r6 browser event-order check | PASS, mock-only | evidence/browser-mock-r6/r6-leave-race-browser-receipt-v1.json; no backend/native result. |
| Rust library and Tauri tests | r5 evidence only; not rerun for r6 | r5 records contain 316 Rust library tests and 9 Tauri tests from the same parent source line. They are not relabeled as r6 executions. |
| r6 package build/sign/preflight | PASS for package bytes and production candidate; release_ready remains false | evidence/distribution-r6/package-build-receipt.json. A package pass does not imply native-game or release acceptance. |
| r6 real desktop UI | PASS (scoped) | One real signed executable passed startup, create, owner Ready/Unready, team 1 → 2 with readiness reset, re-ready and leave after same-scope terminal ordering. The update check is NOT_PASSED_UNPUBLISHED_UPDATE_CHANNEL (404); no version-check pass is claimed. See evidence/desktop-r6/desktop-r6-receipt.json. |
| Native game launch / strict admission | NOT_RUN | native_authority_admission_verified remains false. |
| Three-machine online admission | NOT_RUN | No r6 three-machine result is recorded. |

The r5 leave failure remains historical evidence, not an r6 result. The r6 3ff5ca source fix and scoped desktop receipt are recorded separately; the r5 package, desktop receipt and failure logs remain in their original evidence paths and are not overwritten. The r6 append ledger is 52-item-r6-append-delta.json.
## Historical r5 package and checks

## Package and source binding

The r5 portable package is:

`C:/wksp/ProjectRebound/artifacts/hardware-test-20260908-r5/rebound-hardware-test-20260908-r5-windows-x64.zip`

- ZIP size: 23,489,348 bytes
- ZIP SHA-256: `77642aa56359133e2975cb8119215fe3052597dbd917188a477073129b38c072`
- Toolbox source: `995625903685b61770da6e8139731b50a4dfb0eb`
- Payload source: `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`
- Backend candidate: `584e000baf024e381c5bdb3417ad7ac879bebdca`
- Backend implementation source: `5e1953b525cc27e8e209abb877c938c4920c16a7`
- Backend schema: 48
- Payload SHA-256: `8d471662e947bcb6baf3b0380e23f90bef614f76ac9e7e8a82e2cfbf48619183`
- Pinned game SHA-256: `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`
- `custom_protocol=true`, `lab_testing=false`
- `native_authority_admission_verified=false`, `release_ready=false`

The package updates an existing complete Rebound runtime. It does not contain the game or publish an updater release. Test signatures and package-byte validation do not make it a production-signed release. The package manifest, payload manifest, SHA list, build/sign/package/preflight receipts and retained failed build receipts are under [evidence](evidence/).

## r5 product delta

The UI now exposes the same explicit Ready/Unready and team-switch actions to the owner and members. Actions use the authenticated `player_id`, the current authoritative roster revision and the server's `local.can_set_ready` / `local.can_switch_team` capabilities. A team change clears readiness through the authoritative snapshot; the client does not locally mark another seat ready.

The controller, Tauri DTOs and frontend apply session/lobby scope and monotonic `snapshot_sequence` checks. Same-revision dynamic snapshots are ordered by the runtime sequence; old account/lobby events cannot repopulate a later lobby. This is component and lifecycle protection, not native admission proof.

## Actual r5 checks

| Area | Result | Evidence and limit |
|---|---|---|
| Frontend unit groups | 74 unique tests passed: 11 authoritative-room, 14 bridge, 7 downloads, 27 UI, 11 status, 4 sites | [frontend receipt](evidence/frontend/r5-final-scope-receipt.json) and [byte validation](evidence/frontend/evidence-byte-validation.json). The extra 14-test unready bridge rerun is a duplicate and is not added to 74. |
| Frontend production build | PASS, exit 0 | [frontend build log](evidence/frontend/build.log) |
| Rust library | 316 passed, 0 failed, 0 ignored | [Rust receipt](evidence/rust/r5-final-rust-receipt.json) and [raw library log](evidence/rust/lib-316.log) |
| Tauri tests | 9 passed, 0 failed, 0 ignored; format check passed | [Rust receipt](evidence/rust/r5-final-rust-receipt.json) and [raw Tauri log](evidence/rust/tauri-9.log) |
| Browser mock | Complete App Ready/Unready/team flow passed in an isolated explicit `tauri-mock=1` Edge context | [v3 receipt](evidence/browser-mock/receipt-v3.json) and [byte index](evidence/browser-mock/byte-index.json). It is mock-only; backend, Steam, Payload and multiplayer are NOT_RUN. The v2 pre-clean-head record is retained as historical evidence. |
| Real desktop UI | PARTIAL | The signed r5 executable on one machine passed startup, create, owner Ready/Unready, team 1 → 2 → Ready → team 1, and the one-player start gate. Leave then returned a stale client acknowledgement after the backend list was empty; this is a real failure and remains open for r6. Version check also failed; the sanitized r5 receipt leaves the diagnosis pending. The r6 backend update receipt records the corrected public 404 not-published state; no version-check pass is claimed. See [sanitized desktop receipt](evidence/desktop-r5/desktop-r5-receipt.json). |
| Package/build/sign/preflight | Package byte check PASS and final Tauri build PASS; test signatures only | [distribution evidence](evidence/distribution-r5/). The first wrapper build and one generic build attempt remain failed receipts; they are not overwritten. |
| Native game launch | NOT_RUN | No game was launched in the r5 desktop run. |
| Three-machine online admission / Playable | NOT_RUN | Only one physical machine and one Steam account were used. `native_authority_admission_verified` remains false. |

The leave failure is deliberately not converted into a pass. Native/runtime work for the leave acknowledgement is an r6 follow-up; r5 is not the final hardware acceptance package.

## 52-item append rule

[52-item-r5-append-delta.json](52-item-r5-append-delta.json) contains all 52 IDs. It copies the r4 frozen baseline status and r4 delta reference for every item. Only explicitly observed r5 areas receive an append observation: source/package binding, Ready/team UI, session/lobby/sequence scope, IPC component coverage, and the real leave failure. All other items are `NOT_REEVALUATED_R5`; no component, mock, CI or one-machine result promotes native online acceptance.

The r4 baseline counts remain 10 PASS, 30 PARTIAL and 12 BLOCKED. The r5 append does not rewrite those counts.

## Evidence handling

Only sanitized receipts, byte indexes, package manifests and unit/build logs are copied here. Raw desktop screenshots, accessibility trees, game logs, browser profiles, Steam identity material, credentials and backups remain outside tracked evidence. `evidence/.gitattributes` behavior is defined by the directory-level attributes: JSON and evidence files are treated as binary so their recorded bytes are preserved.

Use `PASS`, `FAIL`, `BLOCKED` and `NOT_RUN` exactly as observed. A successful Ready/team sequence is not a successful leave, native game login, three-machine admission or Playable match.
