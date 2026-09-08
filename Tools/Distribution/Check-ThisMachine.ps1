param(
    [Parameter(Mandatory = $true)][string]$GameWin64,
    [switch]$Collect,
    [string]$EvidenceDirectory
)
$ErrorActionPreference = 'Stop'
$manifest = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'package-manifest.json') -Raw | ConvertFrom-Json
$entry = @($manifest.files | Where-Object { $_.path -ceq 'rebound_toolbox_tauri.exe' })
if ($entry.Count -ne 1 -or [string]$entry[0].sha256 -cnotmatch '^[a-f0-9]{64}$') { throw 'Package manifest has no unique Toolbox pin.' }
if ([string]::IsNullOrWhiteSpace($EvidenceDirectory)) {
    $stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
    $EvidenceDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) ('Rebound-Test-Evidence-' + $stamp)
}
$mode = if ($Collect) { 'Collect' } else { 'Preflight' }
$scriptPath = Join-Path $PSScriptRoot ('native\' + $mode + '-WindowsNative.ps1')
$outputPath = Join-Path $EvidenceDirectory ($mode.ToLowerInvariant() + '.json')
$shell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
& $shell -NoProfile -ExecutionPolicy Bypass -File $scriptPath `
    -PackageRoot $PSScriptRoot -GameBin $GameWin64 `
    -ToolboxExePath (Join-Path $PSScriptRoot 'rebound_toolbox_tauri.exe') `
    -ExpectedToolboxSha256 ([string]$entry[0].sha256) -OutputPath $outputPath
$result = $LASTEXITCODE
Write-Output ('Evidence file: ' + [IO.Path]::GetFileName($outputPath))
Write-Output ('Evidence directory: ' + (Split-Path -Leaf $EvidenceDirectory) + ' (outside the package; use the directory selected above)')
Write-Output ('Exit code: ' + $result + '; package/host checks do not establish native gameplay acceptance.')
exit $result
