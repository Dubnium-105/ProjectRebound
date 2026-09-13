English | [简体中文](README.zh-CN.md)

# r8: Host preflight and error-cause fix

The host-provided r7 preflight had 45 items: 44 PASS, with the only BLOCKED item being Payload in the game directory. The installed file was 1,021,440 bytes, SHA-256 `295cd8835913f1807195ae7a69ae372480451ec1d93f075304886bc289ffbb07`; the matching r7/r8 file was 2,098,488 bytes, `8d471662e947bcb6baf3b0380e23f90bef614f76ac9e7e8a82e2cfbf48619183`. According to the actual source, this difference is strictly rejected before MetaTunnel and game-process creation, consistent with the report that no game window appeared. Remote repair and retry have not yet been observed.

Toolbox commit `dc0cb49e63bfd92247e7b526909676c68b4c6b7a` fixes the HOST path losing the underlying cause through `error.to_string()`, preserving the complete error chain and redacting it before logs, UI, and returned errors; it also checks the local P2P host runtime before the freeze request in normal startup and native evidence collection. When the check fails, the lobby remains open. Dedicated lobby creators remain remote players; the same strict check continues before actually starting a native process. Backend and Payload bytes were unchanged.

Actual acceptance: the final source had 348 Rust tests and 9 Tauri tests, with no failures or ignored tests; formatting checks and the official Tauri asset build passed. The initial targeted test did not compile because the clean repository lacked the fixed-hash aria2 build asset, and it ran 0 tests; the failed record was retained, then a single hash-matching file was supplied and the tests were rerun. Tests used isolated APPDATA/LOCALAPPDATA and no real account configuration.

The r8 test signing, full ZIP byte verification, and this machine's 45 read-only preflight checks passed. The signed client was actually started from the distribution directory and displayed the startup console; Boundary was not started and no multiplayer Attempt was created. The test certificate is not trusted by the system root store; WinVerifyTrust returned `CERT_E_UNTRUSTEDROOT`. No trust-store changes, private-key export, or production signing was performed. `release_ready=false`.

Package: `rebound-hardware-test-20260913-r8-windows-x64.zip`, 23,528,211 bytes, SHA-256 `c618db9609963d70cc51745e6317bdb7bf7d41d286ee022db5953a25a5c4fe82`. Signed Toolbox SHA-256: `720087bd140d7b9d74c54f72a810ec7a7a970736be4210d536c4da7c8374f248`.

On all test machines, close Toolbox and Boundary, run `Install-StrictPayload.ps1 -GameWin64 <actual game Win64 directory>` from the new package directory, then run `Check-ThisMachine.ps1 -GameWin64 <same directory>`. The install script backs up old files first and verifies the replacement. After the preflight passes, use the same r8 package to create a room again, prepare, and start a match. This file check is not native admission or playable acceptance.

Still awaiting real-world testing: host game startup, the named-pipe and native-identity handshake, strict admission on three or more physical machines, an operable match, cleanup, and the next match. The 52 items are listed in the [append ledger](52-item-r8-append-delta.json), and per-file evidence fingerprints are in the [evidence index](evidence-index.json). Existing backend health checks and this round's request records have been sanitized and saved; native success was not inferred from HTTP 200.
