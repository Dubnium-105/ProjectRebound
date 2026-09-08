English | [简体中文](windows-README.zh-CN.md)

# Project Rebound hardware-test build

This package connects to the existing `api.project-rebound.space` / `cnapi.project-rebound.space` and `meta.project-rebound.space` services. The existing backend was verified at 2026-09-08 06:51 UTC: database schema 48; control plane, MetaServer, administrator web, and primary/gateway edge nodes use CI-tested images from `584e000baf024e381c5bdb3417ad7ac879bebdca`. No separate test backend is needed.

This round covers installation, login, managed startup, and native admission proof collection on three physical machines. The owner uses the new **Collect native proof** button. It retains real Steam identities, a frozen roster, signed allocation, native Grants, Reserve/Confirm, and managed cleanup.

**A complete playable match remains unverified.** Live connection evidence is separate from build capability: `native_authority_admission_verified=false` remains unchanged, and proof capture does not publish Playable. The ordinary **Freeze roster and start** action remains subject to this gate. Collect actual three-machine evidence first, then use it to fix or verify the remaining playable flow; changing flags, using empty tokens, or console `open` cannot establish a pass.

## Before use

Use Windows 10/11 x64, your own Steam session, and the fixed Boundary version. The game executable must be `ProjectBoundarySteam-Win64-Shipping.exe` with SHA-256 `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`. The game itself is not included.

This is a test update for an existing Rebound runtime. The game directory needs its complete loader and data files. The installer checks the required files and stops when one is missing. Use the managed Toolbox installation to fill missing runtime files; do not mix DLLs from another source.

The target needs WebView2 Runtime. If it is missing, install it from the [Microsoft WebView2 page](https://developer.microsoft.com/en-us/microsoft-edge/webview2); the [official distribution guidance](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution) explains runtime checks and installation. It also needs the x64 Microsoft Visual C++ v14 runtime, with a version no earlier than the build-tool version recorded in the package manifest; the download entry is on the [official Microsoft runtime page](https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist?view=msvc-170).

## Install and start

1. Verify the ZIP SHA-256 supplied by the distributor, then extract it completely. Keep `package-manifest.json`, `SHA256SUMS`, and every script.
2. Fully exit Boundary and all Toolbox windows. Open PowerShell and change to the extracted directory.
3. Run `Install-StrictPayload.ps1` with `-GameWin64` pointing to the actual `ProjectBoundary\Binaries\Win64` directory. The script checks the package, game, and candidate DLL, saves the old DLL and install receipt, then replaces Payload. Record the receipt location.
4. Double-click `Run-Toolbox.cmd`, select the actual game directory in Toolbox, and use Steam login normally. Each tester uses their own account; do not copy configuration or share credentials.
5. Follow `TEST-MATRIX.md`: all three players join the same authoritative P2P lobby and become ready. The owner selects **Collect native proof**; members launch and connect through the managed flow. Press SPACE if the game requests platform login. Record the actual result on every machine and verify cleanup after collection.

For a game installed on drive D:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Install-StrictPayload.ps1 `
  -GameWin64 'D:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64'
```

That `ExecutionPolicy` applies only to this PowerShell process to run the package script; it does not change the game's or Toolbox's native admission checks. Do not use console `open`, an empty Token, a disabled strict roster, or a manually edited ready flag.

## Check and return results

After installation, run the following command with the actual game directory. The entry point reads the Toolbox hash from the package and saves the report in a sibling evidence directory outside the package:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Check-ThisMachine.ps1 `
  -GameWin64 'D:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64'
```

After the test, add `-Collect` to the same command to collect the report. Do not put the output directory inside the package, which would change the verified file set.

`native/Collect-WindowsNative.ps1` collects only structured version, hash, dependency, and process-state facts. It does not upload automatically, read raw client logs, or collect Steam profiles. See `native/README.md` for commands and exit codes. Give the generated JSON and the written test-matrix results to the coordinator; do not send application configuration, Steam data, or backup directories.

A successful package check does not mean that an online match succeeded. Test records must distinguish `PASS`, `FAIL`, `BLOCKED`, and `NOT_RUN`, and must include the last successful step and error code.

## Restore the original version

Fully exit Boundary and Toolbox, then use the receipt generated during installation:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Restore-StrictPayload.ps1 `
  -ReceiptPath '<path to the install receipt>'
```

The restore script restores the old DLL only when the backup hash is correct, the target is still this round's candidate, and the game directory matches. If another installation operation changed the file, it stops without overwriting it.

## Known limitations

Native hardware results must come from execution receipts for this exact package. Historical versions stopped before native login; neither those failures nor a window-opening check establish the result of this build. Three-machine admission, character spawn and control, rejection cases, cleanup, and reuse in a new match need separate evidence. Keep missing executions as `NOT_RUN`, or record a concrete `BLOCKED` condition.

The EXE and Payload are signed with the project's existing test certificate. The certificate is not trusted by the default Windows root store, so its signature may display as untrusted; this package does not install a root certificate and contains no private key. Signature and hash results, certificate validity, and source commits for each artifact are recorded in the package manifest; they do not mean that production release has passed.
