English | [简体中文](windows-README.zh-CN.md)

# Project Rebound hardware-test build

This package connects to the existing `api.project-rebound.space` / `cnapi.project-rebound.space` and `meta.project-rebound.space` services; no alternate test-backend address is required. It is for installation, Toolbox login, and managed-startup blockage diagnosis while retaining the strict roster, real Steam Ticket, native Grant, and managed-launch checks.

**A normal-path multiplayer match cannot currently complete.** This Payload fixes `native_authority_admission_verified` to `false`, so the strict online gate rejects continuation. Another machine cannot remove that gate. First verify installation, Toolbox, and connection to the existing service, then record where normal startup stops. The multiplayer matrix remains `BLOCKED`; do not change a ready flag or bypass the gate.

As of 2026-09-08 03:30 UTC, the existing service still runs an older version with database schema 43; this candidate's online contract requires schema 48. The backend update was not completed in that earlier package because of host disk space and CI failures, and temporary configuration was restored. An existing-service login check does not prove that the new strict online interface is available; an interface mismatch must not be blamed on the test machine.

## Before use

Use Windows 10/11 x64, your own Steam session, and the fixed Boundary version. The game executable must be `ProjectBoundarySteam-Win64-Shipping.exe` with SHA-256 `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`. The game itself is not included.

This is a test update for an existing Rebound runtime. The game directory needs its complete loader and data files. The installer checks the required files and stops when one is missing. Use the managed Toolbox installation to fill missing runtime files; do not mix DLLs from another source.

The target needs WebView2 Runtime. If it is missing, install it from the [Microsoft WebView2 page](https://developer.microsoft.com/en-us/microsoft-edge/webview2); the [official distribution guidance](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution) explains runtime checks and installation. It also needs the x64 Microsoft Visual C++ v14 runtime, with a version no earlier than the build-tool version recorded in the package manifest; the download entry is on the [official Microsoft runtime page](https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist?view=msvc-170).

## Install and start

1. Verify the ZIP SHA-256 supplied by the distributor, then extract it completely. Keep `package-manifest.json`, `SHA256SUMS`, and every script.
2. Fully exit Boundary and all Toolbox windows. Open PowerShell and change to the extracted directory.
3. Run `Install-StrictPayload.ps1` with `-GameWin64` pointing to the actual `ProjectBoundary\Binaries\Win64` directory. The script checks the package, game, and candidate DLL, saves the old DLL and install receipt, then replaces Payload. Record the receipt location.
4. Double-click `Run-Toolbox.cmd`, select the actual game directory in Toolbox, and use Steam login normally. Each tester uses their own account; do not copy configuration or share credentials.
5. Use Toolbox to try to create or join a test room normally and record the last reachable step. If the game shows a platform-login prompt, press SPACE; the gate may also stop startup before that prompt. Stop the round after `native_admission_unverified`. See `TEST-MATRIX.md` for the scenario and blockage record.

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

The development machine's earlier native diagnostics stopped before login completed for both the current and historical comparison DLL. The candidate's fixed unverified admission gate is a separate limitation. This package contains no new successful native-login evidence; complete multiplayer spawning, admission negatives, cleanup, and next-match reuse have not passed acceptance. Do not count Toolbox startup or map loading as a complete match.

The EXE and Payload are signed with the project's existing test certificate. The certificate is not trusted by the default Windows root store, so its signature may display as untrusted; this package does not install a root certificate and contains no private key. Signature and hash results, certificate validity, and source commits for each artifact are recorded in the package manifest; they do not mean that production release has passed.
