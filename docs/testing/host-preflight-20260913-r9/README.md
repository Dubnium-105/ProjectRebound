English | [简体中文](README.zh-CN.md)

# r9 native authority endpoint and diagnostic fix

The reported r8 two-machine test FAILED. The host game window opened and waited for the authority, but the HOST path sent the Legacy proxy target as the native game listen target. Payload rejected it with `authority_port_mismatch`. The user logs do not contain the actual remote proxy port; synthetic test ports are not hardware observations. r9 separates the HOST native authority endpoint from the Member/proxy target. Only the HOST process receives explicit `-port` and `-external` arguments, and its native ready ACK is checked against that endpoint. Members retain their transport targets. Same-world recovery uses the same HOST endpoint configuration and ACK check. A VNT HOST uses its virtual address before the member endpoint is published, removing the circular startup dependency; unavailable VNT state still fails preparation. Diagnostics retain `named pipe: Strict authority startup failed` while redacting pipe paths, structured secret fields, tokens and nonces.

Toolbox source: `87a9fc8869560fe30867aaf98536baaad8217551`. The deployed Backend remains `8069d5e1126b8a585610b232e721aee45c57ee88`, and Payload source remains `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`. No Backend, Payload or deployment change was made. The earlier r7 installed Payload mismatch is historical context, not the current r9 blocker.

The final execution receipt at `r9/rust-final-v2/execution-receipt.json` records 358 Rust and 9 Tauri tests: 367 passed, zero failed and zero ignored. Formatting, production Tauri assets, local preflight and archive byte checks are supported by their receipts. The initial formatting-only failure ran zero tests; its receipt and log remain separate and are not counted as passes.

The GitHub Actions/checks lookup for Toolbox commit `87a9fc8869560fe30867aaf98536baaad8217551` recorded `NOT_RUN`, with no workflow, check or commit status entries. Local tests do not establish CI success; see `evidence/ci-toolbox.json`.

The desktop observation proves only that the signed r9 test client opened and rendered its lobby controls. r9 native authority admission, the three-or-more-machine matrix, playable matches and a second match after cleanup remain `NOT_RUN`; `release_ready=false`. The test certificate has an untrusted root and does not establish production signing.

Package: `rebound-hardware-test-20260913-r9-windows-x64.zip`, 23,535,108 bytes, SHA-256 `7c78c442b09419bef501b38cb6af7ca1ea03627723c75b4d04561d2420e5b6d5`. The 52-item append record is `52-item-r9-append-delta.json`; raw evidence hashes are in `evidence-index.json`.

Three or more physical machines with independent Steam sessions still need to use the same r9 package to collect strict preflight, HOST native ACK, Member admission, Playable, failure cleanup and the next cold-start attempt. Unexecuted checks remain unpassed.
