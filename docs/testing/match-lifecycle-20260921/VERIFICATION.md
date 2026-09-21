# Joint flow-fix verification record

English | [简体中文](VERIFICATION.zh-CN.md)

Scope: ProjectRebound / ProjectReboundToolbox, r14b-flowfix. This record separates source, component, database, and distribution-file verification; it is not acceptance of a real match in all four modes.

## Automated verification

| Item | Command / method | Result |
| --- | --- | --- |
| Rust core, product vnt feature | `cargo test --lib --no-default-features --features vnt --quiet` | 391 passed, 0 failed, 0 ignored |
| Tauri commands and DTOs | `cargo test --manifest-path src-tauri/Cargo.toml --quiet` | 9 passed, 0 failed, 0 ignored |
| React frontend | Project frontend test script | 84 passed, 0 failed, 0 skipped |
| React production assets | Project frontend build script | Passed |
| Payload C++ | CTest Release | 27 passed, 0 failed |
| Payload DLL | MSBuild Release x64 | Passed, 0 warnings, 0 errors |
| Backend ordinary tests | `go test ./...` | Passed; this command had no database configured and is not treated as PostgreSQL acceptance |
| MatchLobby PostgreSQL | Linux test binary connected to a newly created, independent PostgreSQL database | 26 passed, 0 failed, 0 skipped |
| Relay registry PostgreSQL | Linux test binary connected to an independent PostgreSQL database | 15 passed, 0 failed, 0 skipped |
| Connection / P2P room / Game server PostgreSQL | Linux test binaries connected to independent test databases and run in sequence | All 3 packages passed, 0 skipped; includes real database lifecycle cases for connection, P2P room, VNT room, and game server |
| Distribution PowerShell scripts | PowerShell Parser static analysis | 7 scripts, 0 syntax errors; installation not executed |

Rust verification includes real Windows named pipes, rejection of an incorrect PID, DPAPI receipt restart recovery, exact-handle termination and waiting for an independent child process, the Dedicated HTTP outbox before cleanup fallback, and consumption of the native C++ output wire fixture. The child-process test does not start the game.

Frontend verification covers retaining membership after a successful create/join followed by a failed ready request, stale retry and exit callbacks, preventing local completion from overriding Backend PENDING, and rejecting cleanup receipts from an old operation/run for the same attempt. Browser visual inspection was not completed.

C++ verification covers both real result-callback orders, preventing a single signal from producing a result, complete scope, idempotent ACK, RETURN_READY ordering, and NetDriver binding. The common SHA-256 of the C++ wire and Rust fixture is `a061145083d8745311083bee6cf6ead28a6569ac7f9d97b88fdd38da967ebdf6`.

PostgreSQL tests applied the schema 49 migration through the project Migrator. New database regressions cover normal completion, rejecting premature completion, rejecting new admission during ENDING, replay after a completion transaction fails after RETURN_READY was saved, retaining COMPLETED after process exit / timeout once the result is confirmed, late receipts, retaining the native cleanup record when network release fails, cleanup succeeding after retry, and rejecting VNT bootstrap during ENDING while continuing to accept presence / heartbeat. Relay regressions require remote AllocationClosed confirmation before revocation can complete; native Relay runtime tests cover repeated revocation and idempotent confirmation after a lost receipt.

The initial database test rounds exposed and corrected an old scope snapshot, an error-code contract issue, an uncommitted preflight cleanup receipt, and a SQL parameter type issue in the new VNT test. The final passing logs are `matchlobby-db-verified.txt` and `relayregistry-db-tests-final.txt`; failed rounds are not evidence of passing.

## Build inputs and evidence scope

- Toolbox source commit: `8a641dc`. Built from a clean commit by `scripts/build-strict-candidate.ps1`; production default features are empty, only `vnt` is enabled as a dependency feature, and `lab-testing=false`.
- Payload unsigned DLL SHA-256: `fbaeda4058a8e24e115879c16e326bc0e1c5e8f5dcd9d137deaee1f82be05c47`.
- Payload signed DLL SHA-256: `e73c84f215c67e1cdf6da611157a99caada9c4e1fd2d7bd4f7abb69fdd58bcd0`.
- Toolbox unsigned EXE SHA-256: `e351277928aa1a3b777ad0d972e68805e7dfc0ceecf9832750e0d35c8a01ac10`.
- Toolbox signed EXE SHA-256: `af29cb9cce666ef3ec41ecdef1ee461e9777254d56fe390145450e3dcbb2b370`; 49,742,648 bytes. The Tauri CLI release build passed, with existing unused-code warnings retained.
- Signing used the existing test certificate `B041917B2322ED509435B72356BA5AD9EA053378`; the trust store was not modified and no private key was exported. The test root is not trusted by default Windows trust and is not a production-signing statement.
- The corresponding build logs, test logs, and signing receipt are stored locally in `.tmp/match-lifecycle-20260921/`. The raw logs are not distributed in the public package.

The final Backend / Payload source commits, Linux binary, and archive hashes are listed in the delivery manifest and the in-package `package-manifest.json`. The package builder read back every archive file byte; its pass status only means that the distribution files are consistent.

## Not executed

Online Backend deployment and database migration, replacement of the installed game DLL, game / Frida startup, two cold starts, and a complete match, cleanup, and next lobby with three independent Steam accounts in P2P direct, Relay, VNT, and Dedicated are all **NOT_RUN**. `release_ready=false`; the admission capability gate is unchanged.
