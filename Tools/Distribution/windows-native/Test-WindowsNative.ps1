[CmdletBinding()]
param(
    [string]$PackageRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$GameExePath,
    [string]$GameBin,
    [string]$ToolboxExePath,
    [ValidatePattern('^[0-9a-fA-F]{64}$')][string]$ExpectedToolboxSha256,
    [ValidatePattern('^[0-9a-fA-F]{64}$')][string]$ExpectedSteamApiSha256,
    [string]$OutputPath
)

Set-StrictMode -Version 2.0
. (Join-Path $PSScriptRoot 'Common-WindowsNative.ps1')

if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $OutputPath = Get-NativeDefaultEvidencePath -PackagePath $PackageRoot -LeafName 'test.json'
}

if ([string]::IsNullOrWhiteSpace($GameExePath) -and -not [string]::IsNullOrWhiteSpace($GameBin)) {
    $GameExePath = Join-Path $GameBin 'ProjectBoundarySteam-Win64-Shipping.exe'
}
if ([string]::IsNullOrWhiteSpace($ExpectedSteamApiSha256)) {
    $ExpectedSteamApiSha256 = $script:ExpectedSteamApiSha256
}

try {
    Assert-NativeEvidenceOutputOutsidePackage -PackagePath $PackageRoot -OutputPath $OutputPath
    $evidence = New-NativePreflightEvidence -PackagePath $PackageRoot -GameExePath $GameExePath -ToolboxExePath $ToolboxExePath -ExpectedToolboxSha256 $ExpectedToolboxSha256 -ExpectedSteamApiSha256 $ExpectedSteamApiSha256
    $evidence.tool = 'ProjectRebound.WindowsNativeDistribution.Test'
    $staticChecks = @()
    $packageBlocked = @($evidence.checks | Where-Object { $_.status -eq 'BLOCKED' }).Count -gt 0
    $staticChecks += New-NativeCheck 'managed.install_boundary' $(if ($packageBlocked) {'BLOCKED'} else {'PASS'}) ([ordered]@{ package_verified = (-not $packageBlocked); install_mode = 'Toolbox managed catalog or separately controlled package installer' }) 'verified package and fixed target; no legacy server path' 'This script checks the boundary and never installs files. A separate coordinator must enforce backup, process ownership, post-install hash, and rollback checks.'
    $staticChecks += New-NativeCheck 'managed.ipc_status' 'NOT_RUN' ([ordered]@{ connected = $false; writes = $false }) 'owned Payload status snapshot from an actual managed process' 'No pipe is discovered or contacted by this safe test.'
    $staticChecks += New-NativeCheck 'native.online.acceptance' 'NOT_RUN' ([ordered]@{ game_started = $false; grant_sent = $false; connected = $false; playable = $false }) 'real strict native run' 'A package/host check cannot prove Steam authentication, Grant injection, Reserve, CONNECTED, Playable, or cleanup.'
    $evidence.checks = @($evidence.checks) + $staticChecks
    $evidence.test = [ordered]@{
        status = if ($packageBlocked) { 'BLOCKED' } else { 'NOT_RUN' }
        executed = @('package manifest and byte binding', 'fixed game and dependency preflight', 'managed installation boundary')
        not_executed = @('game launch', 'Toolbox launch', 'Payload named-pipe connection', 'Grant/admission', 'native login/PreLogin', 'Playability', 'cleanup/reuse')
        reason = 'The distribution test is intentionally non-invasive. Run the separately coordinated native matrix after this report; do not convert NOT_RUN to PASS.'
    }
    if ($packageBlocked) { $evidence.status = 'BLOCKED' } else { $evidence.status = 'NOT_RUN' }
    Write-NativeEvidence -Evidence $evidence -OutputPath $OutputPath
    $exitCode = if ($packageBlocked) { 2 } else { 3 }
    exit $exitCode
} catch {
    if ($_.Exception.Message -match '(?i)OutputPath') { $OutputPath = $null }
    $errorEvidence = [ordered]@{
        schema_version = 1
        tool = 'ProjectRebound.WindowsNativeDistribution.Test'
        tool_version = '1.0'
        generated_at_utc = Get-NativeUtcNow
        mode = 'read_only_no_launch_no_install'
        status = 'ERROR'
        release_ready = $false
        error = 'test_exception'
        test = [ordered]@{ status = 'ERROR'; game_started = $false; named_pipe_written = $false }
        safety = [ordered]@{ game_started = $false; dll_replaced = $false; named_pipe_written = $false; credentials_collected = $false; raw_logs_collected = $false }
    }
    Write-NativeEvidence -Evidence $errorEvidence -OutputPath $OutputPath
    exit 4
}
