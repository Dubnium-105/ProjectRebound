# r10: release the authority pipe between launch phases

English | [简体中文](README.zh-CN.md)

The user-reported r9 two-machine run failed after the host reached `authority_ready`: reopening the named pipe returned Windows error 231, followed by member startup cancellation. Source review found that the previous confirmation handle still owned Payload's single pipe instance while later waits reconnected to it. No raw native handle trace was supplied; the diagnosis correlates the reported timeline with source ownership. See the [failure evidence](evidence/native/reported-r9-failure-and-source-cause.json).

r10 uses short PID-verified pipe transactions that release their handle on success or error before HTTP requests or another native RPC. Native-proof and Playable waits continue pumping admission grants and connection events during synchronous startup. Strict roster, valid credentials, scope, cancellation and cleanup ownership checks remain enforced.

Toolbox `642760be87439cea10f4b57f8b7368197e845d45`; Backend `8069d5e1126b8a585610b232e721aee45c57ee88`; Payload source `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`. Only Toolbox changes in this revision. A read-only backend query found the previous attempt finally `ABORTED/CLEARED`, its P2P transport `CLOSED`, and the member still `RESERVED`. These states do not prove native spawn or gameplay. Later Main dependency/CI fixes have not been deployed to the existing service. See the [backend receipt](evidence/backend.json).

Executed checks: 362 Rust and 9 Tauri tests (371 total), zero failures or ignored tests, and both formatting checks passed. New Windows single-instance fixtures reproduce error 231 and verify reconnect, release after errors, and cancellation. Protocol frames are synthetic and are not Boundary native acceptance. The production asset build, 45 file preflight checks, package byte comparisons, and signed Toolbox [desktop startup](evidence/desktop/ui-observation.json) passed. The desktop still displays a failed version check, as did r9; version availability is not counted as passed. [Test receipts](evidence/rust/execution-receipt.json) are [bound to committed source bytes](evidence/committed-source-binding.json).

The exact Toolbox commit has zero Actions runs, checks or commit statuses: [NOT_RUN](evidence/ci-toolbox.json). Local tests do not substitute for CI. Main CI for the documentation commit is queried separately after push.

Distribution: `rebound-hardware-test-20260913-r10-windows-x64.zip`, 23,538,523 bytes, SHA-256 `98d5e2ab6706fdcbecc0909475be5ef961385277142b437b186499b48631c802`. Signing is test-only and its root is untrusted; `release_ready=false` and `native_authority_admission_verified=false`. See the [package receipt](evidence/distribution/package-build-receipt.json), [52-item append](52-item-r10-append-delta.json), and [raw evidence hash index](evidence-index.json).

For the next run, use at least three machines with the same r10 package, close older Toolbox clients, pass file preflight, create a fresh lobby, verify teams, ready every member, and let the host select the native-admission evidence action. Record host admission, member entry, Playable, actual spawn/movement/shooting and a second match after cleanup. All r10 physical steps remain NOT_RUN. Do not use manual open, empty Token/Grant or disabled strict roster to proceed.
