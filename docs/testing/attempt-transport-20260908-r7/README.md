English | [简体中文](README.zh-CN.md)

# r7: transport startup after roster freeze

The reported host acquired allocation, then called the retired standalone room GET and received HTTP 410 before completing the Attempt with `P2P_AUTHORITY_LAUNCH_FAILED`. r7 uses authenticated transport operations scoped to the current Attempt, frozen roster and route. Standalone room APIs remain retired; native admission gates remain enforced.

In the earlier lobby, the owner closed the room before the member's leave request. The resulting 409 was valid; repeated 403 polling was stale client state. r7 retires that exact lost membership and keeps cleanup ownership separate from replacement lobbies. Launch failures retain their sanitized cause and session/lobby/Attempt/operation/run scope. Owner Ready and team controls remain available.

Toolbox `43a4c82fe781ed67df6b4f4f82d1ff976ad586be`; deployed Backend `8069d5e1126b8a585610b232e721aee45c57ee88`; unchanged Payload source `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`. See the [committed source binding](evidence/committed-source-binding.json).

- Frontend: 57 unique tests, Vite and asset preparation passed.
- Browser mock: 6 scoped error/retirement assertions passed, no console errors.
- Rust/Tauri: rust-lib: 342 passed / 0 failed / 0 ignored; tauri-tests: 9 passed / 0 failed / 0 ignored; formatting passed.
- Backend: [CI](https://github.com/Dubnium-105/ProjectRebound/actions/runs/34247644433) and [existing-service deployment](https://github.com/Dubnium-105/ProjectRebound/actions/runs/34250303094). The local PostgreSQL integration skip is recorded separately from actual CI execution.
- Local desktop: `PASS_REAL_DESKTOP_STARTUP_OWNER_READY_TEAM_LEAVE_ONLY`; exact checks are in the [desktop receipt](evidence/distribution/desktop-r7-receipt.json).
- Distribution: every archive entry byte-checked, lab signed, no updater publication or external upload.

[r7 ZIP](../../../artifacts/hardware-test-20260908-r7/rebound-hardware-test-20260908-r7-windows-x64.zip); SHA-256 `c70935f5e671e136b190de6093ab6ef1d1474954e412236dd80382ca7bc63eb2`.

Use this same package on all three or more physical machines and follow the included test matrix. r7 native admission, a second cold start, and a complete playable match remain unverified. `native_authority_admission_verified=false` and `release_ready=false`. The user's two-machine failure is actual failure evidence; remote executable provenance was not verified.

The [52-item append ledger](52-item-r7-append-delta.json) preserves historical status and labels unaffected work `NOT_REEVALUATED_R7`. The [evidence index](evidence-index.json) binds exact receipt bytes. No raw credentials, account records or database dumps are included.

An accidental public commit in this round included four database dumps with data sections and local fixture sources: `45e825f4e3799da460dfedecc9791b123698dffd`. The public branch was restored to its clean parent using an exact old-SHA lease; local files were retained and ignored. The old object remained accessible at the recorded check. GitHub cache removal and absence of exposed account/credential values are not claimed. This commit was not the deployed source and these files are not in the package. See the [correction receipt](evidence/publication-correction/publication-correction-receipt.json) and [unsent removal request](evidence/publication-correction/github-history-removal-request.md). Remote removal requires the separate [GitHub sensitive-data removal procedure](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository).

2026-09-09 update: the removal request has been submitted; see the [publication follow-up](publication-followup-20260909.md) for both repositories.
