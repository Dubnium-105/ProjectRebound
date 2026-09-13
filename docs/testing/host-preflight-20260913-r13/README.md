# r13: normalize empty scope diagnostics and review candidate address classes

English | [简体中文](README.zh-CN.md)

The r12 physical native result remains `FAILED_USER_REPORTED`. R13 records source contracts, component checks, read-only backend aggregates and package evidence only. Native multiplayer admission, spawn, control, cleanup and a second match remain `NOT_RUN`; `release_ready=false`.

The source contract confirms that Payload `match.scope` and `match.playable_scope` may be `{}` before a scope exists or after cleanup. R13 maps `{}` or `null` to absence only for these passive diagnostic fields. Nonempty objects with missing fields or wrong types remain strict errors. An absent scope can never produce Playable proof. See the [native contract](evidence/native/source-contract-receipt.json).

The old client classified every local IPv4 as LAN, but the backend accepts only private IPv4 LAN candidates. R13 aligns publication with the deployed `LAN`, `IPV6` and `SRFLX` rules, prefers a valid STUN SRFLX when available and otherwise retains a valid local candidate, publishing at most one per class. Stored aggregates are HOST:LAN=2, HOST:SRFLX=2, MEMBER:SRFLX=2. They are consistent with this contract gap, but the original rejected frame and specific refusal field were not captured, so the individual request cause remains unproven. Published evidence omits identities and addresses. See the [carrier contract](evidence/carrier/source-contract-receipt.json) and [role aggregate](evidence/backend/candidate-role-aggregate.json).

The old parser actually failed one new regression with `missing field attempt_id`. After repair, 372 Rust and 9 Tauri tests passed with zero failed or ignored, including a real Windows named-pipe component regression; formatting checks passed. The 23 passing targeted controller tests are a subset of the full run, not additional unique coverage. Their first compilation failed with E0658 for an unstable API; the failure is retained and the corrected run passed. See the [before regression](evidence/regression-before/receipt.json), [final checks](evidence/rust-final/execution-receipt.json) and [targeted checks](evidence/rust-targeted/controller-tests-receipt.json).

The read-only Backend receipt records attempt=ABORTED and cleanup=CLEARED; candidate diagnostics are `NOT_OBSERVED_IN_TARGET_SCOPED_OR_WINDOW_GENERIC_MARKERS`. These are lifecycle and aggregate observations, not per-request causal proof. See the [Backend receipt](evidence/backend/r13-attempt-evidence.json).

Toolbox commit: `4cdefa78925d4e83a2cb8d1509c7baec16cf1473`. The backend remains `8069d5e1126b8a585610b232e721aee45c57ee88`, and Payload build source remains `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`. No backend redeployment occurred and Payload bytes are unchanged.

Build, test signing, package byte checks, 45 file preflight checks and real desktop startup passed. The UI still displays a failed version check, separately recorded as `FAILED_DISPLAY_OBSERVED` and not fixed in r13. The packaging helper initially read the wrong receipt field; that failure is retained. The corrected retry passed without product-source changes. Toolbox has no CI execution for this commit and is `NOT_RUN`; Main CI is checked separately after the documentation commit is pushed. See [desktop](evidence/desktop/ui-observation.json), [distribution](evidence/distribution) and [Toolbox CI](evidence/ci-toolbox.json).

Package `rebound-hardware-test-20260913-r13-windows-x64.zip`: 23,539,490 bytes, SHA-256 `c91ac4ca14401863ba93f352d989dbb4d8e6de73c67d2bf167d38adbb4da0127`. Signed Toolbox: 49,494,840 bytes, SHA-256 `d1cc82942c702fe1152f19296218749ac15427530756b18b749ed896b21a8ebb`. The test certificate is not a default trusted root; no roots were installed or private keys exported.

Exit old versions on every machine, use r13, create a fresh lobby and have all members including HOST become ready before collecting native admission evidence. No r13 execution receipt from three independent Steam-account machines exists. Component and desktop checks cannot substitute for native acceptance; strict roster, Token, native Grant and Playable proof requirements remain enforced.

The 52-item append preserves the r12 baseline. R13 marks only BP-001, 009, 011, 017, 018, 019, 023, 047, 050 and 051; all other items remain `NOT_REEVALUATED_R13`. BP-047 contains evidence only and makes no cleanup-code change claim. See the [52-item append](52-item-r13-append-delta.json) and [source binding](evidence/committed-source-binding.json).
