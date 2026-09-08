# Windows native hardware-test checks

These scripts are a read-only preflight and evidence collector for the strict
roster hardware-test package. They do not start Boundary or Toolbox, open a
match, write a named pipe, copy `Payload.dll`, change a strict/native capability
flag, or handle a grant, ticket, account identifier, or raw client log.

The package is a hardware-test candidate. `package-manifest.json` must contain
`schema_version: 1`, `purpose: "hardware-test-only"`, and
`release_ready: false`. Its `files` array binds every ordinary package file by
relative POSIX path, byte count, and SHA-256. The manifest itself is outside
that array; an optional `SHA256SUMS` sidecar covers the manifest and all listed
files, but does not list itself. A ZIP with one outer directory is accepted as
well as an already extracted package root.

The manifest should also carry `msvc_minimum_version` from the actual x64
build toolchain (for example `14.50.35717`). The preflight compares every
required x64 runtime DLL version with that value; if it is absent, the VC check
is `NOT_RUN` rather than an unverified pass.

The package must carry `Payload.dll` and the manifest must pin the reviewed
Boundary executable:

```text
ProjectBoundarySteam-Win64-Shipping.exe
  SHA-256 181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843
  bytes   102362112
```

The Payload hash is read from the package manifest. It is never hard-coded in
these scripts, so a newly signed distribution can be checked without treating
the earlier unsigned candidate or the restored baseline DLL as the same
artifact. If the installed game `Payload.dll` differs from the package hash,
the report is `BLOCKED`; the script does not repair it.

## Commands

Run from an extracted package, or pass the package ZIP explicitly. The game
path is intentionally explicit because Steam libraries are not fixed to one
drive. `-GameBin` is shorthand for the directory containing the fixed game
executable.

```powershell
$native = Join-Path $PackageRoot 'native'
# Keep evidence beside the package so it cannot change the bytes being verified.
$evidence = Join-Path (Split-Path -Parent $PackageRoot) 'native-evidence'
# -OutputPath is rejected when it resolves inside $PackageRoot.

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File (Join-Path $native 'Preflight-WindowsNative.ps1') `
  -PackageRoot $PackageRoot `
  -GameExePath 'X:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64\ProjectBoundarySteam-Win64-Shipping.exe' `
  -ToolboxExePath 'X:\Tools\rebound_toolbox.exe' `
  -ExpectedToolboxSha256 '<published Toolbox SHA-256>' `
  -OutputPath (Join-Path $evidence 'preflight.json')

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File (Join-Path $native 'Collect-WindowsNative.ps1') `
  -PackageRoot $PackageRoot `
  -GameBin 'X:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64' `
  -OutputPath (Join-Path $evidence 'collect.json')

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File (Join-Path $native 'Test-WindowsNative.ps1') `
  -PackageRoot $PackageRoot `
  -GameBin 'X:\SteamLibrary\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64' `
  -OutputPath (Join-Path $evidence 'test.json')
```

Exit codes are `0` for all requested static checks passing, `2` for a concrete
hash/path/dependency blocker, `3` when a required input or live native stage
was not run, and `4` for a script error. A `0` from the package checks does not
mean that Steam authentication, NMT/PreLogin, Reserve, CONNECTED, Playable,
cleanup, or next-match reuse passed. Those remain the separately coordinated
strict online matrix.

## Host checks

The scripts read and report only structured facts:

- fixed game executable size and SHA-256;
- package manifest and each listed package file size/SHA-256;
- installed `Payload.dll` size/SHA-256, if an explicit game path was supplied;
- the Engine-tree `steam_api64.dll` used by the fixed launch path, at
  `Engine/Binaries/ThirdParty/Steamworks/Steamv157/Win64`, including the
  currently pinned Steam-v157 bytes/SHA-256 when the default pin is used;
- Steam executable/signature presence, Steam process presence, and exact game
  process count;
- WebView2 Evergreen presence/version and the x64 MSVC runtime DLLs;
- optional Toolbox size/SHA-256/signature when an expected hash is supplied.

Missing Steam/WebView2/VC or a running exact game process is recorded as
`BLOCKED`. The scripts never start or terminate a process. A missing Toolbox
argument is `NOT_RUN`, not a guessed success.
The signature summary is evidence only; a local unsigned fixture is not a
distributable Toolbox candidate. The release coordinator must pin the signed
Toolbox hash in the outer package manifest and apply the product's existing
signature policy before a machine is handed the package.

The supported online path remains the signed Toolbox match-lobby flow: use the
managed Project Rebound catalog and let Toolbox verify the download and install
transaction. These scripts never copy a DLL. If the coordinator supplies a
hardware-test candidate outside the published catalog, installation must be a
separate, explicitly controlled step: verify the package and fixed EXE first,
confirm the exact Boundary process is stopped, retain a timestamped backup and
hash of the existing DLL, replace only the explicitly named Payload path, and
verify the post-install hash before the normal Toolbox/Steam/Grant/strict flow.
An installer must roll back only after verifying the backup hash; this package
does not provide an ad-hoc copy shortcut. Do not invoke `startgame.ps1`, use a
console `open`, pass a ticket/grant on a command line, or use a legacy room/server
package. The package's `native/` scripts are diagnostics only and are not a
compatibility layer.

Reports intentionally omit usernames, account/platform IDs, access or refresh
tokens, join grants, tickets, command lines, configuration files, and raw
client logs. Return only the generated JSON after removing any locally added
paths from surrounding notes.
