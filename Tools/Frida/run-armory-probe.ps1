[CmdletBinding()]
param(
    [switch] $AttachOnly,
    [int] $ProcessId,
    [string] $OutputDirectory,
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]] $GameArguments
)

$ErrorActionPreference = 'Stop'

$python = (Get-Command python.exe -ErrorAction Stop).Source
$controller = Join-Path $PSScriptRoot 'capture_armory.py'
$arguments = @($controller)

if ($ProcessId -gt 0) {
    $arguments += @('--pid', [string] $ProcessId)
}
elseif (-not $AttachOnly) {
    $arguments += '--launch'
}

if (-not [string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $arguments += @('--output', $OutputDirectory)
}

$forwarded = @($GameArguments | Where-Object {
    $null -ne $_ -and -not [string]::IsNullOrWhiteSpace([string] $_)
})
if ($forwarded.Count -gt 0) {
    $arguments += '--'
    $arguments += $forwarded
}

& $python @arguments
exit $LASTEXITCODE
