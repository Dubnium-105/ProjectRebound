param(
    [Parameter(Mandatory = $true)][string]$GameWin64,
    [string]$CandidatePayload,
    [string]$BackupRoot
)
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class StrictPackageWinTrust {
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct WinTrustFileInfo { public uint cbStruct; public IntPtr pcwszFilePath; public IntPtr hFile; public IntPtr pgKnownSubject; }
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct WinTrustData { public uint cbStruct; public IntPtr pPolicyCallbackData; public IntPtr pSIPClientData; public uint dwUIChoice; public uint fdwRevocationChecks; public uint dwUnionChoice; public IntPtr pFile; public uint dwStateAction; public IntPtr hWVTStateData; public IntPtr pwszURLReference; public uint dwProvFlags; public uint dwUIContext; }
    [DllImport("wintrust.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern int WinVerifyTrust(IntPtr hwnd, ref Guid actionId, ref WinTrustData data);
    public static int Verify(string path) {
        IntPtr pathPtr = Marshal.StringToCoTaskMemUni(path); IntPtr filePtr = IntPtr.Zero;
        try {
            var file = new WinTrustFileInfo { cbStruct = (uint)Marshal.SizeOf<WinTrustFileInfo>(), pcwszFilePath = pathPtr };
            filePtr = Marshal.AllocCoTaskMem(Marshal.SizeOf<WinTrustFileInfo>()); Marshal.StructureToPtr(file, filePtr, false);
            var data = new WinTrustData { cbStruct = (uint)Marshal.SizeOf<WinTrustData>(), dwUIChoice = 2, dwUnionChoice = 1, pFile = filePtr, dwStateAction = 1 };
            Guid action = new Guid("00AAC56B-CD44-11d0-8CC2-00C04FC295EE"); return WinVerifyTrust(IntPtr.Zero, ref action, ref data);
        } finally { if (filePtr != IntPtr.Zero) Marshal.FreeCoTaskMem(filePtr); Marshal.FreeCoTaskMem(pathPtr); }
    }
}
"@
$ErrorActionPreference = 'Stop'
$packageDefaultPayload = Join-Path $PSScriptRoot 'Payload.dll'
if ([string]::IsNullOrWhiteSpace($CandidatePayload)) { $CandidatePayload = $packageDefaultPayload }
if ([string]::IsNullOrWhiteSpace($BackupRoot)) { $BackupRoot = Join-Path $env:LOCALAPPDATA 'ProjectRebound\payload-backups\distribution-test' }
$CERT_E_UNTRUSTEDROOT = -2146762487
$EXPECTED_SIGNER_THUMBPRINT = 'B041917B2322ED509435B72356BA5AD9EA053378'
function Get-Sha256([string]$Path) { (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }
function Fail([string]$Message) { throw "strict payload install refused: $Message" }
function Assert-WinTrust([string]$Path, [string]$Label) { $hresult = [StrictPackageWinTrust]::Verify($Path); if ($hresult -ne 0 -and $hresult -ne $CERT_E_UNTRUSTEDROOT) { Fail "$Label WinVerifyTrust rejected with HRESULT $hresult" }; return $hresult }
function Assert-SignedArtifact([string]$Path, [string]$Label) {
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    $thumbprint = if ($signature.SignerCertificate) { ([string]$signature.SignerCertificate.Thumbprint).ToUpperInvariant() } else { '' }
    if ($thumbprint -cne $EXPECTED_SIGNER_THUMBPRINT) { Fail "$Label signer thumbprint is missing or unexpected" }
    $hresult = Assert-WinTrust $Path $Label
    return [ordered]@{ hresult = $hresult; thumbprint = $thumbprint }
}
function Assert-ToolboxStopped([string]$Path) { $name = [IO.Path]::GetFileNameWithoutExtension($Path); if (Get-Process -Name $name -ErrorAction SilentlyContinue) { Fail "Toolbox process $name is running; close it and retry (no process is terminated by this script)" } }
function Assert-PackageManifest([string]$ManifestPath) {
    if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) { Fail 'package-manifest.json is missing' }
    $package = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
    if ([int]$package.schema_version -ne 1 -or [string]$package.purpose -cne 'hardware-test-only' -or [bool]$package.release_ready) { Fail 'package manifest is not a non-release hardware-test manifest' }
    $root = [IO.Path]::GetFullPath($PSScriptRoot).TrimEnd('\') + '\'
    $entries = @($package.files)
    if ($entries.Count -eq 0) { Fail 'package manifest has no file entries' }
    foreach ($entry in $entries) {
        $relative = [string]$entry.path
        if ([string]::IsNullOrWhiteSpace($relative) -or $relative -match '(^[\\/]|^[A-Za-z]:|(^|[\\/])\.\.([\\/]|$))') { Fail "package manifest contains an unsafe path: $relative" }
        $full = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot $relative))
        if (-not $full.StartsWith($root, [StringComparison]::OrdinalIgnoreCase)) { Fail "package manifest path escapes package root: $relative" }
        if (-not (Test-Path -LiteralPath $full -PathType Leaf)) { Fail "package file is missing: $relative" }
        if ([long]$entry.bytes -ne (Get-Item -LiteralPath $full).Length -or [string]$entry.sha256 -cne (Get-Sha256 $full)) { Fail "package file bytes do not match manifest: $relative" }
    }
    foreach ($required in @('Payload.dll', 'rebound_toolbox_tauri.exe', 'Install-StrictPayload.ps1', 'Restore-StrictPayload.ps1', 'payload-manifest.json')) {
        if (-not @($entries | Where-Object { [string]$_.path -ceq $required }).Count) { Fail "package manifest omits required file: $required" }
    }
    return $package
}
$manifestPath = Join-Path $PSScriptRoot 'payload-manifest.json'; $toolboxPath = Join-Path $PSScriptRoot 'rebound_toolbox_tauri.exe'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf) -or -not (Test-Path -LiteralPath $toolboxPath -PathType Leaf) -or -not (Test-Path -LiteralPath $packageDefaultPayload -PathType Leaf)) { Fail 'package manifest, Toolbox, or Payload.dll is missing' }
$packageManifest = Assert-PackageManifest (Join-Path $PSScriptRoot 'package-manifest.json')
$packagePayload = (Resolve-Path -LiteralPath $CandidatePayload -ErrorAction Stop).Path
if ([IO.Path]::GetFullPath($packagePayload) -cne [IO.Path]::GetFullPath($packageDefaultPayload)) { Fail 'CandidatePayload must be the package-root Payload.dll' }
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
if ([string]$manifest.payload_filename -cne 'Payload.dll' -or [string]$manifest.toolbox_filename -cne 'rebound_toolbox_tauri.exe') { Fail 'manifest filenames are not the fixed package names' }
if ([string]$manifest.signer_thumbprint -cne $EXPECTED_SIGNER_THUMBPRINT -or [string]$manifest.toolbox_signer_thumbprint -cne $EXPECTED_SIGNER_THUMBPRINT) { Fail 'manifest signer thumbprint is not the pinned test signer' }
$expectedPayload = [string]$manifest.payload_sha256; $expectedGame = [string]$manifest.game_sha256; $expectedToolbox = [string]$manifest.toolbox_sha256
 $expectedSteam = [string]$manifest.steam_api64_sha256
if ($expectedPayload -notmatch '^[a-f0-9]{64}$' -or $expectedGame -notmatch '^[a-f0-9]{64}$' -or $expectedToolbox -notmatch '^[a-f0-9]{64}$' -or $expectedSteam -notmatch '^[a-f0-9]{64}$') { Fail 'package manifest hashes are incomplete' }
if ((Get-Sha256 $packagePayload) -ne $expectedPayload -or (Get-Sha256 $toolboxPath) -ne $expectedToolbox) { Fail 'package bytes do not match manifest' }
$payloadEntry = @($packageManifest.files | Where-Object { [string]$_.path -ceq 'Payload.dll' }); $toolboxEntry = @($packageManifest.files | Where-Object { [string]$_.path -ceq 'rebound_toolbox_tauri.exe' })
if ($payloadEntry.Count -ne 1 -or [string]$payloadEntry[0].sha256 -cne $expectedPayload -or $toolboxEntry.Count -ne 1 -or [string]$toolboxEntry[0].sha256 -cne $expectedToolbox) { Fail 'package-manifest and payload-manifest disagree about signed artifact bytes' }
if ([string]$packageManifest.pinned_game.sha256 -cne $expectedGame) { Fail 'package-manifest and payload-manifest disagree about game pin' }
$toolboxSignature = Assert-SignedArtifact $toolboxPath 'Toolbox'; $payloadSignature = Assert-SignedArtifact $packagePayload 'Payload'
$gameRoot = (Resolve-Path -LiteralPath $GameWin64 -ErrorAction Stop).Path
if ((Split-Path -Leaf $gameRoot) -cne 'Win64') { Fail 'GameWin64 must resolve to ProjectBoundary\Binaries\Win64' }
$gameExe = Join-Path $gameRoot 'ProjectBoundarySteam-Win64-Shipping.exe'; $destination = Join-Path $gameRoot 'Payload.dll'
if (-not (Test-Path -LiteralPath $gameExe -PathType Leaf)) { Fail "game executable is missing: $gameExe" }; if ((Get-Sha256 $gameExe) -ne $expectedGame) { Fail 'game executable hash does not match the strict manifest' }
foreach ($required in @('dxgi.dll', 'DT_ItemType.json')) { if (-not (Test-Path -LiteralPath (Join-Path $gameRoot $required) -PathType Leaf)) { Fail "required Rebound runtime file is missing: $required" } }
$steamDll = Join-Path $gameRoot '..\..\..\Engine\Binaries\ThirdParty\Steamworks\Steamv157\Win64\steam_api64.dll'; if (-not (Test-Path -LiteralPath $steamDll -PathType Leaf)) { Fail "Steam runtime is missing: $steamDll" }; if ((Get-Sha256 $steamDll) -cne $expectedSteam) { Fail 'Steam runtime hash does not match the package path pin' }
Assert-ToolboxStopped $toolboxPath; if (Get-Process -Name 'ProjectBoundarySteam-Win64-Shipping' -ErrorAction SilentlyContinue) { Fail 'the game process is running; close it and retry (no process is terminated by this script)' }
if (-not (Test-Path -LiteralPath $destination -PathType Leaf)) { Fail 'an existing Payload.dll is required so this test package can restore the previous runtime' }
$existingHash = Get-Sha256 $destination; New-Item -ItemType Directory -Force -Path $BackupRoot | Out-Null
$stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ'); $backupDir = Join-Path $BackupRoot ("$stamp-" + [Guid]::NewGuid().ToString('N')); New-Item -ItemType Directory -Force -Path $backupDir | Out-Null
$backupPath = Join-Path $backupDir 'Payload.dll.before-install'; $tempPath = Join-Path $gameRoot ('.Payload.dll.strict-install-' + [Guid]::NewGuid().ToString('N') + '.tmp')
 $receiptPath = Join-Path $backupDir 'payload-install-receipt.json'
function Write-AtomicJson([string]$Path, [object]$Value) {
    $temporary = "$Path.$([Guid]::NewGuid().ToString('N')).tmp"
    try {
        $encoding = [Text.UTF8Encoding]::new($false); [IO.File]::WriteAllText($temporary, ($Value | ConvertTo-Json -Depth 8), $encoding)
        $stream = [IO.File]::Open($temporary, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read); try { $stream.Flush($true) } finally { $stream.Dispose() }
        Move-Item -LiteralPath $temporary -Destination $Path -Force -ErrorAction Stop
    } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue } }
}
$intent = [ordered]@{ schema_version = 1; operation = 'install'; status = 'preparing'; game_win64 = $gameRoot; game_executable_sha256 = $expectedGame; candidate_payload_sha256 = $expectedPayload; previous_payload_sha256 = $existingHash; backup_path = $backupPath; toolbox_path = $toolboxPath; completed_at = $null }
Write-AtomicJson $receiptPath $intent
try {
    Copy-Item -LiteralPath $destination -Destination $backupPath -ErrorAction Stop; if ((Get-Sha256 $backupPath) -ne $existingHash) { Fail 'backup bytes failed verification' }
    Copy-Item -LiteralPath $packagePayload -Destination $tempPath -ErrorAction Stop; if ((Get-Sha256 $tempPath) -ne $expectedPayload) { Fail 'temporary installed copy failed hash verification' }
    $intent.status = 'prepared'; $intent.backup_sha256 = Get-Sha256 $backupPath; Write-AtomicJson $receiptPath $intent
    Move-Item -LiteralPath $tempPath -Destination $destination -Force -ErrorAction Stop; if ((Get-Sha256 $destination) -ne $expectedPayload) { Fail 'installed Payload.dll failed final hash verification' }
    $receipt = [ordered]@{ schema_version = 1; operation = 'install'; status = 'completed'; game_win64 = $gameRoot; game_executable_sha256 = Get-Sha256 $gameExe; candidate_payload_sha256 = $expectedPayload; installed_payload_sha256 = Get-Sha256 $destination; previous_payload_sha256 = $existingHash; backup_path = $backupPath; backup_sha256 = Get-Sha256 $backupPath; toolbox_path = $toolboxPath; toolbox_sha256 = Get-Sha256 $toolboxPath; toolbox_winverifytrust_hresult = $toolboxSignature.hresult; toolbox_signer_thumbprint = $toolboxSignature.thumbprint; payload_winverifytrust_hresult = $payloadSignature.hresult; payload_signer_thumbprint = $payloadSignature.thumbprint; steam_api64_sha256 = $expectedSteam; completed_at = [DateTime]::UtcNow.ToString('o') }
    Write-AtomicJson $receiptPath $receipt; Get-Content -LiteralPath $receiptPath -Raw
} catch {
    if (Test-Path -LiteralPath $tempPath) { Remove-Item -LiteralPath $tempPath -Force -ErrorAction SilentlyContinue }
    $currentHash = $null; if (Test-Path -LiteralPath $destination -PathType Leaf) { try { $currentHash = Get-Sha256 $destination } catch {} }
    $rollbackStatus = 'not_applied'
    if ($currentHash -eq $expectedPayload) {
        $rollbackTemp = Join-Path $gameRoot ('.Payload.dll.strict-rollback-' + [Guid]::NewGuid().ToString('N') + '.tmp')
        try { Copy-Item -LiteralPath $backupPath -Destination $rollbackTemp -ErrorAction Stop; if ((Get-Sha256 $rollbackTemp) -ne $existingHash) { throw 'rollback backup hash mismatch' }; Move-Item -LiteralPath $rollbackTemp -Destination $destination -Force -ErrorAction Stop; if ((Get-Sha256 $destination) -ne $existingHash) { throw 'rollback final hash mismatch' }; $rollbackStatus = 'rolled_back' } catch { $rollbackStatus = 'rollback_failed: ' + $_.Exception.Message; if (Test-Path -LiteralPath $rollbackTemp) { Remove-Item -LiteralPath $rollbackTemp -Force -ErrorAction SilentlyContinue } }
    } elseif ($null -ne $currentHash -and $currentHash -ne $existingHash) { $rollbackStatus = 'quarantined_third_party_change' }
    $failure = [ordered]@{ schema_version = 1; operation = 'install'; status = 'failed'; failure = $_.Exception.Message; game_win64 = $gameRoot; destination = $destination; current_payload_sha256 = $currentHash; expected_candidate_sha256 = $expectedPayload; previous_payload_sha256 = $existingHash; backup_path = $backupPath; backup_sha256 = if (Test-Path -LiteralPath $backupPath) { Get-Sha256 $backupPath } else { $null }; recovery = $rollbackStatus; completed_at = [DateTime]::UtcNow.ToString('o') }
    try { Write-AtomicJson (Join-Path $backupDir 'payload-install-failure.json') $failure } catch {}
    throw
}
