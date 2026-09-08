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
    $OutputPath = Get-NativeDefaultEvidencePath -PackagePath $PackageRoot -LeafName 'preflight.json'
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
    Write-NativeEvidence -Evidence $evidence -OutputPath $OutputPath
    $exitCode = switch ([string]$evidence.status) {
        'PASS' { 0; break }
        'NOT_RUN' { 3; break }
        'BLOCKED' { 2; break }
        default { 4; break }
    }
    exit $exitCode
} catch {
    if ($_.Exception.Message -match '(?i)OutputPath') { $OutputPath = $null }
    $errorEvidence = [ordered]@{
        schema_version = 1
        tool = 'ProjectRebound.WindowsNativeDistribution'
        tool_version = '1.0'
        generated_at_utc = Get-NativeUtcNow
        mode = 'read_only_no_launch_no_install'
        status = 'ERROR'
        release_ready = $false
        error = 'preflight_exception'
        safety = [ordered]@{ game_started = $false; dll_replaced = $false; named_pipe_written = $false; credentials_collected = $false; raw_logs_collected = $false }
    }
    Write-NativeEvidence -Evidence $errorEvidence -OutputPath $OutputPath
    exit 4
}
