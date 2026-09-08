# Backend final evidence audit

Generated: `2026-09-07T19:05:55+08:00`. Audit generation is read-only; no product source was changed. The backend schema47 follow-up evidence and report entries were completed before this regeneration.

The canonical matrix is still unexecuted: 52/52 component ACs are not_run with result=null, and 22/22 E2E targets are not_run with result=null. The audit marks no whole AC fully proven. Four component-level candidates have owner PASS plus matching runtime evidence: AC-002, AC-003, AC-013, AC-022. Their canonical states remain unchanged.

## Owner report snapshot

| Report | Issues | Acceptance states | Tests | Missing current log paths |
|---|---:|---|---:|---:|
| `backend-report.json` | 15 | BLOCKED=2, PARTIAL=13 | 28 | 0 |
| `native-report.json` | 18 | BLOCKED=8, PARTIAL=9, PASS=1 | 18 | 0 |
| `toolbox-report.json` | 29 | BLOCKED=10, PARTIAL=19 | 19 | 0 |
| `root-report.json` | 25 | PARTIAL=22, PASS=3 | 11 | 0 |

Current evidence path resolution found 0 missing effective paths in issue evidence/test logs.

## Findings

| ID | Severity | Finding | Evidence |
|---|---|---|---|
| E-AUDIT-001 | high | The canonical 52-component matrix remains not_run/result=null and all 22 canonical E2E targets remain not_run/result=null. Owner PASS values were not promoted. | `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\breakpoints.json:131`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\breakpoints.json:134`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\acceptance-tests.json:26`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\acceptance-tests.json:36` |
| E-AUDIT-002 | medium | Backend/toolbox owner snapshots still report the 246-pass Toolbox library run, while the later root report records the current all-features run as 267; the 246 logs remain historical evidence only. Current same-log pass counts were checked and have no direct mismatch. | `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\backend-report.json:171`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\toolbox-report.json:84`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\root-report.json:333`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\toolbox-final-all-features-test.log:273` |
| E-AUDIT-004 | medium | Current owner reports contain 71 explicit source:line references. Six current issue records have only source/static references and no operational log/fixture evidence; none of the four owner PASS records is source-only. Source lines cannot substitute for runtime execution. | `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\native-report.json:402`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\native-report.json:555` |
| E-AUDIT-005 | medium | Owner reports were generated from different source revisions and the current worktrees are dirty; report evidence is snapshot-scoped and is not proof of one integrated final commit. | `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\backend-report.json:28`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\native-report.json:5`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\root-report.json:3` |
| E-AUDIT-007 | medium | The schema47 Go race/full Backend runs retain TestStrictRosterRustRelayClients as an explicit SKIP because the driver is unavailable. Root has an independent Go/Rust relay PASS for BP-013; that result is valid in its own scope and does not turn the Backend skipped test into a pass. | `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\backend-report.json:403`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\evidence\backend-go-test-race-go1266-schema47-20260907-summary.log:10`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\evidence\backend-go-test-race-go1266-schema47-20260907.log:980`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\root-report.json:136` |
| E-AUDIT-008 | low | The earlier implementation-evidence-index records BP-007 and BP-011 as empty issue evidence and says filesystem verification was not performed. The current root report now contains evidence paths for both, and this audit resolved all current root evidence paths; the index entry is stale. | `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\implementation-evidence-index.json:929`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\implementation-evidence-index.json:934`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\root-report.json:87`; `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\root-report.json:109` |

The source-line audit found 71 explicit source:line references; all bases exist after normalization. Six issue records have no operational log/fixture reference, so their source lines cannot be treated as runtime acceptance.

## Schema47 race evidence

The Linux Go1.26.6 race run is verified as EXECUTED_PASS_WITH_EXPLICIT_SKIPS: report exit 0, 386 PASS lines, 0 FAIL lines, and three explicit skips. Summary evidence is `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\evidence\backend-go-test-race-go1266-schema47-20260907-summary.log:5-10`; raw skip lines are `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\evidence\backend-go-test-race-go1266-schema47-20260907.log:474` (TestS3StorageAgainstCompatibleService), `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\evidence\backend-go-test-race-go1266-schema47-20260907.log:589` (TestNormalizeBattleLogFixtureWhenConfigured), `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\evidence\backend-go-test-race-go1266-schema47-20260907.log:980` (TestStrictRosterRustRelayClients).

The Rust relay skip remains a Backend skip. Root's independent BP-013 relay run is separately evidenced and does not change that skip.

## Schema47 dump/restore

Source and restored databases both report migration 47, migrations 45-47 checksums, matching key row counts, 1038 public columns, 559 constraints, and 264 indexes. The authoritative comparison log records IDENTICAL_EXCLUDING_DATABASE_IDENTITY_LINE and rowcount_and_schema_metrics=IDENTICAL_EXCLUDING_DATABASE_IDENTITY_LINE at `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\evidence\backend-schema47-authority-rowcount-compare-20260907.log:8-9`; both source and restore psql logs record exit 0. The exact archive `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\evidence\backend-schema47-source-20260907-authority-final.dump` is 317571 bytes with SHA256 2d2290a14481fb380b7e9057faea45976c18ae4ba84600af4a3b19b8b22ddeb3, and the final restore log records the same path/hash and final_pg_restore_exit_code=0. The earlier two differently hashed archives remain retained as historical. Production ACL/listener, object storage, Docker preflight, and application startup remain outside the evidence.

## Secret scan

Scanned 176 .log files only under `C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907` and `C:\wksp\ProjectRebound\.tmp\strict-roster-20260907`. Credential-shaped value findings: 0. No secret values are emitted. The keyword-only hits (12) are token field names/placeholders in the cargo-format source-diff log; they are listed with line numbers in JSON and were not classified as secret values.

## Component AC review

| AC | BP | Canonical status | Owner PASS | Audit disposition |
|---|---|---|---|---|
| AC-001 | BP-001 | not_run / None | none | not fully proven |
| AC-002 | BP-002 | not_run / None | root-report.json | component candidate; canonical state preserved |
| AC-003 | BP-003 | not_run / None | root-report.json | component candidate; canonical state preserved |
| AC-004 | BP-004 | not_run / None | none | not fully proven |
| AC-005 | BP-005 | not_run / None | none | not fully proven |
| AC-006 | BP-006 | not_run / None | none | not fully proven |
| AC-007 | BP-007 | not_run / None | none | not fully proven |
| AC-008 | BP-008 | not_run / None | none | not fully proven |
| AC-009 | BP-009 | not_run / None | none | not fully proven |
| AC-010 | BP-010 | not_run / None | none | not fully proven |
| AC-011 | BP-011 | not_run / None | none | not fully proven |
| AC-012 | BP-012 | not_run / None | none | not fully proven |
| AC-013 | BP-013 | not_run / None | root-report.json | component candidate; canonical state preserved |
| AC-014 | BP-014 | not_run / None | none | not fully proven |
| AC-015 | BP-015 | not_run / None | none | not fully proven |
| AC-016 | BP-016 | not_run / None | none | not fully proven |
| AC-017 | BP-017 | not_run / None | none | not fully proven |
| AC-018 | BP-018 | not_run / None | none | not fully proven |
| AC-019 | BP-019 | not_run / None | none | not fully proven |
| AC-020 | BP-020 | not_run / None | none | not fully proven |
| AC-021 | BP-021 | not_run / None | none | not fully proven |
| AC-022 | BP-022 | not_run / None | native-report.json | component candidate; canonical state preserved |
| AC-023 | BP-023 | not_run / None | none | not fully proven |
| AC-024 | BP-024 | not_run / None | none | not fully proven |
| AC-025 | BP-025 | not_run / None | none | not fully proven |
| AC-026 | BP-026 | not_run / None | none | not fully proven |
| AC-027 | BP-027 | not_run / None | none | not fully proven |
| AC-028 | BP-028 | not_run / None | none | not fully proven |
| AC-029 | BP-029 | not_run / None | none | not fully proven |
| AC-030 | BP-030 | not_run / None | none | not fully proven |
| AC-031 | BP-031 | not_run / None | none | not fully proven |
| AC-032 | BP-032 | not_run / None | none | not fully proven |
| AC-033 | BP-033 | not_run / None | none | not fully proven |
| AC-034 | BP-034 | not_run / None | none | not fully proven |
| AC-035 | BP-035 | not_run / None | none | not fully proven |
| AC-036 | BP-036 | not_run / None | none | not fully proven |
| AC-037 | BP-037 | not_run / None | none | not fully proven |
| AC-038 | BP-038 | not_run / None | none | not fully proven |
| AC-039 | BP-039 | not_run / None | none | not fully proven |
| AC-040 | BP-040 | not_run / None | none | not fully proven |
| AC-041 | BP-041 | not_run / None | none | not fully proven |
| AC-042 | BP-042 | not_run / None | none | not fully proven |
| AC-043 | BP-043 | not_run / None | none | not fully proven |
| AC-044 | BP-044 | not_run / None | none | not fully proven |
| AC-045 | BP-045 | not_run / None | none | not fully proven |
| AC-046 | BP-046 | not_run / None | none | not fully proven |
| AC-047 | BP-047 | not_run / None | none | not fully proven |
| AC-048 | BP-048 | not_run / None | none | not fully proven |
| AC-049 | BP-049 | not_run / None | none | not fully proven |
| AC-050 | BP-050 | not_run / None | none | not fully proven |
| AC-051 | BP-051 | not_run / None | none | not fully proven |
| AC-052 | BP-052 | not_run / None | none | not fully proven |

Machine-readable per-BP records, line references, skip names, and redacted scan results:

`C:\wksp\ProjectRebound\docs\implementation\strict-roster-20260907\final-evidence-audit-backend.json`


## Follow-up review: hard-coded component mapping and cleanup closure

This follow-up was read-only. The ledger script at `C:\wksp\ProjectRebound\.tmp\strict-roster-20260907\create_acceptance_ledger.py:37` hard-codes `BP-002`, `BP-003`, `BP-013`, and `BP-022` into `whole_component_pass`; line 51 emits `PASS` from that set before evaluating owner status or evidence completeness. The current canonical records remain `not_run` with `result: null` at `acceptance-tests.json:39-68`, `71-100`, `392-421`, and `683-712`.

The four expected strings were checked against existing executed evidence and are sufficient only at component scope:

| AC/BP | Expected wording check | Existing evidence | Audit disposition |
|---|---|---|---|
| AC-002/BP-002 | 20 concurrent 401s -> one refresh; logout and account switch reject late result | `toolbox-auth-http-concurrency-test.log:5-41`; `root-report.json:34-35` | Component candidate; canonical `not_run` retained |
| AC-003/BP-003 | killed writer, concurrent stale preferences, decryptability, no temporary plaintext | `toolbox-final-all-features-test.log:207,222,257,286`; `Toolbox/src/config/config_types.rs:407-505` | Component candidate; canonical `not_run` retained |
| AC-013/BP-013 | distinct HOST/PEER tokens, Edge retagging, bidirectional receive, tamper rejection | `go-rust-live-relay-test.log:37-42`; `toolbox-final-all-features-test.log:212-215`; `rust-relay-fixture-test.log:44-47` | Component candidate; Backend Rust-driver skip remains separate |
| AC-022/BP-022 | actual C++ accepted frame -> Rust; wrong ID, busy, wrong command reject; accepted is not Playable | `cpp-rust-wire-test.log:38-42`; `native-report.json:680-691`; `Toolbox/src/server/pipe_cpp_fixture_tests.rs:13-126` | Component candidate with mixed owner states; native PASS does not erase root/Toolbox PARTIAL |

No one of these records proves the 52-component matrix, 22 E2E cases, native gameplay admission, or release readiness. The hard-coded set should remain an explicitly evidence-backed component exception if regenerated; it must not promote canonical or E2E results.

The Backend report was also checked across all 15 owned issues. It contains 12 `implementation: implemented` and 3 `implementation: partial`, while acceptance is `PARTIAL` for 13 and `BLOCKED` for BP-046/BP-047. Each note records the missing live/native/fault-injection/production evidence. No Backend acceptance is overclaimed. `implemented` is treated as code-level status; BP-046 and BP-047 remain blocked and must not be interpreted as end-to-end readiness. The machine-readable per-issue review is in `final-evidence-audit-backend.json` under `E-AUDIT-010`.

F1/F2 cleanup review is recorded under `E-AUDIT-011`: current Toolbox pre-spawn failures route through the sealed `NativeProcessNotStarted` path and exact empty-world scope; post-spawn failures are typed `AuthorityLaunchFailure`, and missing owned-handle proof remains quarantine (`Option=None`/PID metadata) rather than a fabricated exit receipt. Backend guards still require P2P HOST, terminal exact scope, and no published world/Payload for the not-started marker. Final native teardown and supervisor marker integration remain acceptance blockers.
