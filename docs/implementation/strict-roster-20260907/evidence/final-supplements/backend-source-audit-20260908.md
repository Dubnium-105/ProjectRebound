# Backend read-only source audit — 2026-09-08

Reviewed source commit: `bb3590adf298ddc9fefde63d0c70c2fc08ad7529`.

No product source was changed or committed in this review. Full structured findings and hashes are in [backend-source-audit-20260908.json](C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/backend-source-audit-20260908.json).

## Findings

- **BACKEND-AUDIT-001 (P2):** `AuthorityAdmissions` (`Backend/internal/matchlobby/service.go:1547-1624`) and `MarkAdmissionDelivered` (`:1636-1672`) do not explicitly compare a grant’s route/generation with the current attempt and roster before reconstructing or acknowledging a grant. `ReserveAdmission` and `ConfirmConnected` reject stale values later, so this is a stale staging risk rather than a demonstrated final connection bypass.
- **BACKEND-AUDIT-002 (P1):** migration 47 permits a null live native nonce; migration 48 marks historical connected HOST rows as preserved; `AuthorityHeartbeat` (`service.go:2414-2421`) does not require a nonempty live nonce. A migrated active HOST can therefore advance route without proof that its live scope can be preserved. The current migration test does not exercise this null-nonce active attempt.
- **BACKEND-AUDIT-003 (P2):** after `cleanup_state=CLEARED`, a repeated `NativeCleared` validates scope but accepts changed evidence metadata without comparing it to the first audited receipt (`service.go:2876-2894`). It does not reopen state or overwrite the audit row, but receipt identity is scope-only.
- **BACKEND-AUDIT-004 (P1):** runtime old P2P routes return explicit retirement errors (`controlplane/server.go:469-489`, `p2proom/http.go:52-60`), while OpenAPI still advertises them as functional 200 room/directory operations (`api/openapi/openapi.yaml:3065-3175`). Runtime is fail-closed; the authoritative API description is stale.

No empty match token, missing admission signing key, strict-off online mode, direct-open, or production old-online route bypass was found in the reviewed Backend source. The current unit evidence for this is `backend-strict-config-unit-20260908T232233Z.log` and `backend-legacy-retirement-unit-20260908T232026Z.log`.

## E2E-13/14/15 Backend-only evidence

The dedicated result is [e2e-13-15-backend-result-20260908.json](C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/e2e131415/e2e-13-15-backend-result-20260908.json). All three cases remain **PARTIAL** because no real process kill/reconciler, native Playable/role confirmation, or WS/IPC old-event ordering was executed.

- MatchLobby PG integration: 6/6 top-level PASS, exit 0, log SHA `35CBA0D69049589F5DBF5315D2759B7AFD23423AA9A4DC8E180E338778DF94D9`.
- Migration/verify PG integration: 2/2 PASS, exit 0, log SHA `34BBDB0F1E7C182AF2AFBE7AA1988965B399C781E057BF74A19EF1ABD9E45737`.
- BattleLog synthetic strict projection component: 3 subtests PASS, exit 0, log SHA `48B44BE34BFFC4B6A125464F82035E7FC49929748CAB2BCDE7060CCBD9BB85C6`; this is not a real game BattleLog acceptance.
- Meta retirement repository: 1/1 PASS, exit 0, log SHA `4BCAF40F5614EF146400F617B26F367FC0B1E2094CCF642A225BE8010F0C5F2B`.
- Retired Meta HTTP/RPC and managed P2P unit checks: exit 0, log SHA `8B85ACA8DBCCDE4986D7628CA37D7A5619D6571AEBE8FC496D9DE721421D0732`.
- Strict config/admission-token unit checks: exit 0, log SHA `0DCAB9B2C01B91A5FC5BA056087C569A1D5845F866297A62E252E46D7B8B6899`.

The earlier combined run with concurrent residue is preserved as an actual failure (exit 1, SHA `0ACF158445A5B16063B9F3CC600B6A4ACC713ECA1BCC99EFE63AEC5EF88FA006`); it was not relabeled as passing evidence.