<#
.SYNOPSIS
Backs up and deploys the pinned multi-match Payload and console wrapper.

.DESCRIPTION
Run this script from an ordinary user PowerShell outside a restricted Codex
sandbox.  It refuses to deploy over a running Boundary process, validates the
pinned executable, copies exact files without globs, and verifies target hashes.
#>
[CmdletBinding()]
param(
    [string] $GameDirectory =
        'C:\Steam\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64'
)

$ErrorActionPreference = 'Stop'
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$gameDirectoryPath = [IO.Path]::GetFullPath($GameDirectory)
$gameExecutable = Join-Path $gameDirectoryPath 'ProjectBoundarySteam-Win64-Shipping.exe'
$sourcePayload = Join-Path $repoRoot 'x64\Release\Payload.dll'
$targetPayload = Join-Path $gameDirectoryPath 'Payload.dll'
$sourceWrapper = Join-Path $repoRoot (
    'ServerWrapper\ProjectReboundServerWrapper\ProjectReboundServerWrapper\' +
    'x64\Release\ProjectReboundServerWrapper.exe')
$targetWrapper = Join-Path $gameDirectoryPath 'ProjectReboundServerWrapper.exe'
$expectedExeSha256 =
    '181C49FFB522B3EB01014C84FD9D3A2A5C0B66AE80A6A6ADDFF4BDD6F8125843'

foreach ($requiredPath in @(
    $gameExecutable, $sourcePayload, $targetPayload, $sourceWrapper, $targetWrapper)) {
    if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
        throw "Required deployment file is missing: $requiredPath"
    }
}

$exeHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $gameExecutable).Hash
if ($exeHash -ne $expectedExeSha256) {
    throw "Unsupported Boundary executable SHA-256: $exeHash"
}

$running = Get-Process -Name 'ProjectBoundarySteam-Win64-Shipping' `
    -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -eq $gameExecutable } catch { $false }
    }
if ($running) {
    throw "Boundary is running; stop exact PID(s) $($running.Id -join ',') before deployment."
}

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$backupDirectory = Join-Path $repoRoot ".tmp\runtime-deploy\$stamp-multimatch"
New-Item -ItemType Directory -Path $backupDirectory -Force | Out-Null
$backupPayload = Join-Path $backupDirectory 'Payload.before-multimatch.dll'
$backupWrapper = Join-Path $backupDirectory 'ProjectReboundServerWrapper.before-multimatch.exe'

Copy-Item -LiteralPath $targetPayload -Destination $backupPayload
Copy-Item -LiteralPath $targetWrapper -Destination $backupWrapper
Copy-Item -LiteralPath $sourcePayload -Destination $targetPayload -Force
Copy-Item -LiteralPath $sourceWrapper -Destination $targetWrapper -Force

$sourcePayloadHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourcePayload).Hash
$targetPayloadHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $targetPayload).Hash
$sourceWrapperHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourceWrapper).Hash
$targetWrapperHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $targetWrapper).Hash
if ($sourcePayloadHash -ne $targetPayloadHash) {
    throw 'Payload deployment hash mismatch.'
}
if ($sourceWrapperHash -ne $targetWrapperHash) {
    throw 'Wrapper deployment hash mismatch.'
}

[pscustomobject]@{
    ExecutableSha256 = $exeHash
    PayloadSha256 = $targetPayloadHash
    WrapperSha256 = $targetWrapperHash
    BackupDirectory = $backupDirectory
    BackupPayloadSha256 =
        (Get-FileHash -Algorithm SHA256 -LiteralPath $backupPayload).Hash
    BackupWrapperSha256 =
        (Get-FileHash -Algorithm SHA256 -LiteralPath $backupWrapper).Hash
}
