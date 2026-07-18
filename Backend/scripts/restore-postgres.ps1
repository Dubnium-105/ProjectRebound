param(
    [Parameter(Mandatory = $true)]
    [string]$BackupFile,
    [string]$DatabaseURL = $env:DATABASE_URL,
    [switch]$ConfirmRestore
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'postgres-env.ps1')
if (-not $ConfirmRestore) {
    throw 'Restore replaces database objects. Re-run with -ConfirmRestore after verifying the target.'
}
if ([string]::IsNullOrWhiteSpace($DatabaseURL)) {
    throw 'DATABASE_URL or -DatabaseURL is required.'
}
if (-not (Get-Command pg_restore -ErrorAction SilentlyContinue)) {
    throw 'pg_restore is required.'
}
$backup = [System.IO.Path]::GetFullPath($BackupFile)
if (-not (Test-Path -LiteralPath $backup -PathType Leaf)) {
    throw "Backup file does not exist: $backup"
}

Invoke-WithPostgresEnvironment -DatabaseURL $DatabaseURL -Action {
    & pg_restore --clean --if-exists --no-owner --no-privileges --single-transaction $backup
    if ($LASTEXITCODE -ne 0) {
        throw "pg_restore failed with exit code $LASTEXITCODE."
    }
}
Write-Output 'Restore completed. Run migration and application smoke tests before admitting traffic.'
