function Invoke-WithPostgresEnvironment {
    param(
        [Parameter(Mandatory = $true)]
        [string]$DatabaseURL,
        [Parameter(Mandatory = $true)]
        [scriptblock]$Action
    )

    $uri = [Uri]$DatabaseURL
    if ($uri.Scheme -notin @('postgres', 'postgresql') -or [string]::IsNullOrWhiteSpace($uri.Host)) {
        throw 'Database URL must use postgres:// or postgresql:// and include a host.'
    }
    $userInfo = $uri.UserInfo.Split(':', 2)
    if ($userInfo.Count -lt 1 -or [string]::IsNullOrWhiteSpace($userInfo[0])) {
        throw 'Database URL must include a username.'
    }

    $values = @{
        PGHOST = $uri.Host
        PGPORT = if ($uri.IsDefaultPort) { '5432' } else { [string]$uri.Port }
        PGUSER = [Uri]::UnescapeDataString($userInfo[0])
        PGPASSWORD = if ($userInfo.Count -eq 2) { [Uri]::UnescapeDataString($userInfo[1]) } else { '' }
        PGDATABASE = [Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart('/'))
        PGSSLMODE = 'prefer'
    }
    if ([string]::IsNullOrWhiteSpace($values.PGDATABASE)) {
        throw 'Database URL must include a database name.'
    }
    foreach ($part in $uri.Query.TrimStart('?').Split('&', [System.StringSplitOptions]::RemoveEmptyEntries)) {
        $pair = $part.Split('=', 2)
        if ($pair.Count -eq 2 -and $pair[0] -eq 'sslmode') {
            $values.PGSSLMODE = [Uri]::UnescapeDataString($pair[1])
        }
    }

    $previous = @{}
    foreach ($name in $values.Keys) {
        $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $values[$name], 'Process')
    }
    try {
        & $Action
    }
    finally {
        foreach ($name in $values.Keys) {
            [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
        }
    }
}
