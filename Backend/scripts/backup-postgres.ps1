param(
    [string]$DatabaseURL = $env:DATABASE_URL,
    [string]$OutputDirectory = '.backups'
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'postgres-env.ps1')
if ([string]::IsNullOrWhiteSpace($DatabaseURL)) {
    throw 'DATABASE_URL or -DatabaseURL is required.'
}
if (-not (Get-Command pg_dump -ErrorAction SilentlyContinue)) {
    throw 'pg_dump is required.'
}
if (-not (Get-Command pg_restore -ErrorAction SilentlyContinue)) {
    throw 'pg_restore is required for backup verification.'
}

$directory = [System.IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $directory | Out-Null
$timestamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ')
$output = Join-Path $directory "projectrebound-$timestamp.dump"

Invoke-WithPostgresEnvironment -DatabaseURL $DatabaseURL -Action {
    & pg_dump --format custom --compress 9 --no-owner --no-privileges --file $output
    if ($LASTEXITCODE -ne 0) {
        throw "pg_dump failed with exit code $LASTEXITCODE."
    }
    & pg_restore --list $output | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Backup verification failed with exit code $LASTEXITCODE."
    }
}

Get-Item -LiteralPath $output | Select-Object FullName, Length, LastWriteTimeUtc
