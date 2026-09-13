# r12: accept legitimate native startup observations

English | [简体中文](README.zh-CN.md)

The r11 physical run failed after HOST authority_ready with `Payload returned an invalid native match state`; MEMBER subsequently received `ROOM_NOT_CONNECTABLE`. Backend observations show HOST complete about 4.224 seconds after ready, followed by the member 409. The attempt is ABORTED, transport CLOSED and cleanup CLEARED. This is temporal correlation, not a per-request causal proof or independent proof of a game crash. See [backend evidence](evidence/backend.json).

Toolbox omitted three Payload states: `local_pawn_ready`, `waiting_backend_confirmation` and `local_authority_pending`. The old user log does not identify which value triggered rejection. R12 accepts those existing observations while retaining unknown-state and stale-request rejection. Transitional states cannot produce Playable evidence. Native server `RoundState=InvalidState` is first-round idle data in a separate field and remains unchanged. See [source contract review](evidence/native/source-contract-receipt.json).

Toolbox `a9d010fd9cdf9f7cb9c45c00afd9c2072289ab9d`; Backend `8069d5e1126b8a585610b232e721aee45c57ee88`; Payload `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`. No backend redeployment or Payload byte change occurred. [Tested source hashes](evidence/committed-source-binding.json) bind the test parent to final committed bytes.

Actual execution: [one new regression failed against the old parser](evidence/regression-before/receipt.json); after repair 366 Rust and 9 Tauri tests passed, zero failed or ignored, and format checks passed. An initial unqualified exact filter selected zero tests and is NOT_RUN, not a pass. Synthetic protocol frames and Windows pipe component tests do not establish Boundary native acceptance.

The first full run failed with LNK1104 because the isolated TEMP directory did not exist; its [environment failure receipt](evidence/rust-initial-environment/execution-receipt.json) is retained. Creating the directory and rerunning produced the results above, with no product-source change.

Production build, test signing, package byte verification, 45 preflight checks and [real desktop startup](evidence/desktop/ui-observation.json) passed. The version-check UI result is recorded independently. See [Toolbox CI query](evidence/ci-toolbox.json); final Main CI is checked separately after push.

Package `rebound-hardware-test-20260913-r12-windows-x64.zip`: 23,540,813 bytes; SHA-256 `7166eb6b177e84f0208b0f629d4dffb4c12fc1969ba80dfb5dd464984fd1bfed`. Test certificate trust is not default system trust; no roots were installed or private keys exported. See [package receipt](evidence/distribution/package-build-receipt.json), [52-item append](52-item-r12-append-delta.json) and [evidence index](evidence-index.json).

All machines must exit old versions, use r12, create a fresh lobby, confirm teams and have every player including HOST become ready, then collect native admission evidence. Native admission, spawn/control, cleanup and a second match on three independent Steam-account machines are NOT_RUN because no r12 physical execution receipt exists. `release_ready=false` and `native_authority_admission_verified=false`; no empty tokens, disabled strict roster or manual open bypasses.
