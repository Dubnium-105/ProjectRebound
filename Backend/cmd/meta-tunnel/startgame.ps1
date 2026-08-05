param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]] $GameArguments
)

$ErrorActionPreference = 'Stop'

$gameDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$gameExecutable = Join-Path $gameDirectory 'ProjectBoundarySteam-Win64-Shipping.exe'
$tunnelExecutable = @(
    (Join-Path $gameDirectory 'meta-tunnel.exe'),
    (Join-Path $gameDirectory 'meta-tunnel-diagnostic.exe')
) | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
$steamHelperExecutable = Join-Path $gameDirectory 'steam_helper.exe'
$signerPublicKey = Join-Path $gameDirectory 'rebound-signer.pem'
$authCache = Join-Path $gameDirectory '.project-rebound-auth.json'
$apiBaseUrl = if ([string]::IsNullOrWhiteSpace($env:PROJECT_REBOUND_API_BASE_URL)) {
    'https://api.project-rebound.space'
}
else {
    $env:PROJECT_REBOUND_API_BASE_URL.TrimEnd('/')
}
$metaBaseUrl = if ([string]::IsNullOrWhiteSpace($env:PROJECT_REBOUND_META_BASE_URL)) {
    'https://meta.project-rebound.space'
}
else {
    $env:PROJECT_REBOUND_META_BASE_URL.TrimEnd('/')
}
$logicAddress = if ([string]::IsNullOrWhiteSpace($env:PROJECT_REBOUND_LOGIC_ADDRESS)) {
    'logic.project-rebound.space:443'
}
else {
    $env:PROJECT_REBOUND_LOGIC_ADDRESS.Trim()
}
$logicServerName = if ([string]::IsNullOrWhiteSpace($env:PROJECT_REBOUND_LOGIC_SERVER_NAME)) {
    'logic.project-rebound.space'
}
else {
    $env:PROJECT_REBOUND_LOGIC_SERVER_NAME.Trim()
}
$tunnelHttpUrl = $null
$startedTunnel = $null
$startedGame = $null

[Net.ServicePointManager]::SecurityProtocol =
    [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

function ConvertTo-PlainText {
    param([Security.SecureString] $SecureValue)

    $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($SecureValue)
    try {
        return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
    }
    finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer)
    }
}

function Invoke-ReboundApi {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [object] $Body,
        [string] $AccessToken,
        [string] $DeviceId
    )

    $headers = @{
        'Accept' = 'application/json'
        'User-Agent' = 'project-rebound-startgame/1.1'
    }
    if (-not [string]::IsNullOrWhiteSpace($AccessToken)) {
        $headers['Authorization'] = "Bearer $AccessToken"
    }
    if (-not [string]::IsNullOrWhiteSpace($DeviceId)) {
        $headers['X-Device-Id'] = $DeviceId
    }

    try {
        return Invoke-RestMethod `
            -Uri "$apiBaseUrl$Path" `
            -Method Post `
            -Headers $headers `
            -ContentType 'application/json' `
            -Body ($Body | ConvertTo-Json -Depth 8 -Compress) `
            -TimeoutSec 20
    }
    catch {
        $message = $_.Exception.Message
        if (-not [string]::IsNullOrWhiteSpace($_.ErrorDetails.Message)) {
            try {
                $errorEnvelope = $_.ErrorDetails.Message | ConvertFrom-Json
                if (-not [string]::IsNullOrWhiteSpace($errorEnvelope.error.message)) {
                    $message = "$($errorEnvelope.error.code): $($errorEnvelope.error.message)"
                }
            }
            catch {
                # Preserve the transport error when the response is not JSON.
            }
        }
        throw $message
    }
}

function Get-ShortFactorHash {
    param(
        [Parameter(Mandatory = $true)] [string] $Prefix,
        [Parameter(Mandatory = $true)] [string] $Value
    )

    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes("$Prefix`:$Value")
        $digest = $sha.ComputeHash($bytes)
        $hex = New-Object Text.StringBuilder
        for ($index = 0; $index -lt 8; $index++) {
            [void] $hex.Append($digest[$index].ToString('x2'))
        }
        return "$Prefix`:$hex"
    }
    finally {
        $sha.Dispose()
    }
}

function Get-DeviceFingerprint {
    $factors = New-Object Collections.Generic.List[string]

    try {
        $uuid = [string] (Get-CimInstance Win32_ComputerSystemProduct -ErrorAction Stop |
            Select-Object -ExpandProperty UUID)
        $uuid = $uuid.Trim().ToUpperInvariant()
        if ($uuid -and $uuid -ne '00000000-0000-0000-0000-000000000000') {
            $factors.Add((Get-ShortFactorHash -Prefix 'uu' -Value $uuid))
        }
    }
    catch {
        Write-Host '[WARN] SMBIOS UUID is unavailable.' -ForegroundColor Yellow
    }

    try {
        $disk = [string] (Get-CimInstance Win32_DiskDrive -ErrorAction Stop |
            Where-Object { $_.Index -eq 0 } |
            Select-Object -First 1 -ExpandProperty SerialNumber)
        if ([string]::IsNullOrWhiteSpace($disk)) {
            $disk = [string] (Get-CimInstance Win32_PhysicalMedia -ErrorAction Stop |
                Select-Object -First 1 -ExpandProperty SerialNumber)
        }
        $disk = $disk.Trim().ToUpperInvariant()
        if ($disk) {
            $factors.Add((Get-ShortFactorHash -Prefix 'ds' -Value $disk))
        }
    }
    catch {
        Write-Host '[WARN] System disk serial is unavailable.' -ForegroundColor Yellow
    }

    return $factors -join '|'
}

function Save-ReboundSession {
    param(
        [Parameter(Mandatory = $true)] [object] $Session,
        [string] $SteamId,
        [string] $PersonaName,
        [bool] $IntegrityTrusted = $false
    )

    if ([string]::IsNullOrWhiteSpace($Session.refresh_token)) {
        throw 'Authentication API did not return a refresh token.'
    }

    $secureRefreshToken = ConvertTo-SecureString ([string] $Session.refresh_token) -AsPlainText -Force
    try {
        $protectedRefreshToken = ConvertFrom-SecureString $secureRefreshToken
    }
    finally {
        $secureRefreshToken.Dispose()
    }

    $cacheDocument = [ordered] @{
        protected_refresh_token = $protectedRefreshToken
        refresh_token_expires_at = [string] $Session.refresh_token_expires_at
        steam_id = $SteamId
        persona_name = $PersonaName
        integrity_trusted = $IntegrityTrusted
    }
    $json = $cacheDocument | ConvertTo-Json -Depth 4
    [IO.File]::WriteAllText($authCache, $json, [Text.UTF8Encoding]::new($false))
}

function Restore-ReboundSession {
    param([string] $DeviceId)

    if (-not (Test-Path -LiteralPath $authCache -PathType Leaf)) {
        return $null
    }

    $refreshToken = $null
    try {
        $cacheDocument = Get-Content -LiteralPath $authCache -Raw -Encoding UTF8 | ConvertFrom-Json
        if ([string]::IsNullOrWhiteSpace($cacheDocument.protected_refresh_token)) {
            return $null
        }
        $secureRefreshToken = ConvertTo-SecureString ([string] $cacheDocument.protected_refresh_token)
        try {
            $refreshToken = ConvertTo-PlainText $secureRefreshToken
        }
        finally {
            $secureRefreshToken.Dispose()
        }

        Write-Host '[AUTH] Restoring the saved Project Rebound session...'
        $response = Invoke-ReboundApi `
            -Path '/v1/auth/refresh' `
            -Body @{ refresh_token = $refreshToken } `
            -DeviceId $DeviceId
        if ([string]::IsNullOrWhiteSpace($response.data.session.access_token)) {
            throw 'Authentication API returned an invalid refresh response.'
        }
        Save-ReboundSession `
            -Session $response.data.session `
            -SteamId ([string] $cacheDocument.steam_id) `
            -PersonaName ([string] $cacheDocument.persona_name) `
            -IntegrityTrusted $true
        Write-Host "[AUTH] Session restored for $($cacheDocument.persona_name)."
        return $response.data.session
    }
    catch {
        Write-Host "[WARN] Saved session could not be restored: $($_.Exception.Message)" -ForegroundColor Yellow
        return $null
    }
    finally {
        $refreshToken = $null
    }
}

function Get-SteamLogin {
    if (-not (Test-Path -LiteralPath $steamHelperExecutable -PathType Leaf)) {
        throw "Steam login helper not found: $steamHelperExecutable"
    }

    Write-Host '[AUTH] Requesting an encrypted ticket from the running Steam client...'
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $steamHelperExecutable
    $startInfo.WorkingDirectory = $gameDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.CreateNoWindow = $true

    $helperProcess = [Diagnostics.Process]::new()
    $helperProcess.StartInfo = $startInfo
    if (-not $helperProcess.Start()) {
        throw 'Steam login helper could not be started.'
    }
    $stdoutTask = $helperProcess.StandardOutput.ReadToEndAsync()
    $stderrTask = $helperProcess.StandardError.ReadToEndAsync()
    $helperProcess.WaitForExit()
    $helperOutput = $stdoutTask.Result
    $helperDiagnostic = $stderrTask.Result
    $helperExitCode = $helperProcess.ExitCode
    $helperProcess.Dispose()

    $helperError = [regex]::Match($helperOutput, '(?m)^ERROR:\s*(.+)$')
    if ($helperExitCode -ne 0 -or $helperError.Success) {
        $detail = if ($helperError.Success) {
            $helperError.Groups[1].Value.Trim()
        }
        elseif (-not [string]::IsNullOrWhiteSpace($helperDiagnostic)) {
            ($helperDiagnostic.Trim() -split "`r?`n")[-1]
        }
        else {
            "exit code $helperExitCode"
        }
        throw "Steam login failed: $detail"
    }

    $steamIdMatch = [regex]::Match($helperOutput, '(?m)^SteamID:\s*(\d+)\s*$')
    $personaMatch = [regex]::Match($helperOutput, '(?m)^PersonaName:\s*(.+?)\s*$')
    $ticketMatch = [regex]::Match($helperOutput, '(?m)^Ticket:\s*([0-9a-fA-F]+)\s*$')
    if (-not $steamIdMatch.Success -or -not $personaMatch.Success -or -not $ticketMatch.Success) {
        throw 'Steam login helper returned an incomplete identity or ticket.'
    }

    return [pscustomobject] @{
        SteamId = $steamIdMatch.Groups[1].Value
        PersonaName = $personaMatch.Groups[1].Value.Trim()
        Ticket = $ticketMatch.Groups[1].Value.ToLowerInvariant()
    }
}

function ConvertFrom-HexString {
    param([Parameter(Mandatory = $true)] [string] $Hex)

    if (($Hex.Length % 2) -ne 0 -or $Hex -notmatch '^[0-9a-fA-F]+$') {
        throw 'Encrypted Steam ticket is not valid hexadecimal data.'
    }
    $bytes = New-Object byte[] ($Hex.Length / 2)
    for ($index = 0; $index -lt $bytes.Length; $index++) {
        $bytes[$index] = [Convert]::ToByte($Hex.Substring($index * 2, 2), 16)
    }
    return ,$bytes
}

function Submit-IntegrityProof {
    param(
        [Parameter(Mandatory = $true)] [string] $AccessToken,
        [Parameter(Mandatory = $true)] [string] $Ticket,
        [Parameter(Mandatory = $true)] [string] $Nonce,
        [string] $DeviceId
    )

    if (-not (Test-Path -LiteralPath $signerPublicKey -PathType Leaf)) {
        Write-Host '[WARN] Integrity public key is unavailable; proof was skipped.' -ForegroundColor Yellow
        return
    }

    $keyBytes = [IO.File]::ReadAllBytes($signerPublicKey)
    $ticketBytes = ConvertFrom-HexString $Ticket
    $nonceBytes = [Text.Encoding]::UTF8.GetBytes($Nonce)
    $proofInput = New-Object byte[] ($keyBytes.Length + $ticketBytes.Length + $nonceBytes.Length)
    [Buffer]::BlockCopy($keyBytes, 0, $proofInput, 0, $keyBytes.Length)
    [Buffer]::BlockCopy($ticketBytes, 0, $proofInput, $keyBytes.Length, $ticketBytes.Length)
    [Buffer]::BlockCopy($nonceBytes, 0, $proofInput, $keyBytes.Length + $ticketBytes.Length, $nonceBytes.Length)

    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $digest = $sha.ComputeHash($proofInput)
        $proof = -join ($digest | ForEach-Object { $_.ToString('x2') })
    }
    finally {
        $sha.Dispose()
        [Array]::Clear($ticketBytes, 0, $ticketBytes.Length)
        [Array]::Clear($proofInput, 0, $proofInput.Length)
    }

    $response = Invoke-ReboundApi `
        -Path '/v1/integrity/proof' `
        -Body @{ nonce = $Nonce; proof = $proof; component = 'toolbox' } `
        -AccessToken $AccessToken `
        -DeviceId $DeviceId
    if ($response.data.ok -ne $true) {
        throw 'Integrity proof was rejected.'
    }
    Write-Host '[AUTH] Integrity proof accepted.'
}

function New-ReboundSession {
    param([string] $DeviceId)

    $steamLogin = Get-SteamLogin
    Write-Host "[AUTH] Steam login OK: $($steamLogin.PersonaName) ($($steamLogin.SteamId))"

    $bindBody = [ordered] @{
        steam_id = $steamLogin.SteamId
        persona_name = $steamLogin.PersonaName
        device_id = $DeviceId
        encrypted_ticket = $steamLogin.Ticket
    }
    if (-not [string]::IsNullOrWhiteSpace($env:PROJECT_REBOUND_INVITE_CODE)) {
        $bindBody.invite_code = $env:PROJECT_REBOUND_INVITE_CODE
    }

    Write-Host '[AUTH] Binding the verified Steam identity...'
    $response = Invoke-ReboundApi -Path '/v1/auth/bind' -Body $bindBody -DeviceId $DeviceId
    if ([string]::IsNullOrWhiteSpace($response.data.session.access_token)) {
        throw 'Authentication API returned an invalid bind response.'
    }
    if ($response.data.steam_verified -ne $true -or $response.data.auth_level -ne 'verified') {
        throw 'The server did not verify the Steam encrypted ticket.'
    }

    $nonce = [string] $response.data.integrity_challenge.nonce
    $integrityTrusted = [string]::IsNullOrWhiteSpace($nonce)
    if (-not [string]::IsNullOrWhiteSpace($nonce)) {
        try {
            Submit-IntegrityProof `
                -AccessToken ([string] $response.data.session.access_token) `
                -Ticket $steamLogin.Ticket `
                -Nonce $nonce `
                -DeviceId $DeviceId
            $integrityTrusted = $true
        }
        catch {
            Write-Host "[WARN] Integrity proof failed: $($_.Exception.Message)" -ForegroundColor Yellow
        }
    }

    Save-ReboundSession `
        -Session $response.data.session `
        -SteamId $steamLogin.SteamId `
        -PersonaName $steamLogin.PersonaName `
        -IntegrityTrusted $integrityTrusted

    return $response.data.session
}

function Get-ReboundSession {
    param(
        [string] $DeviceId,
        [switch] $RestoreOnly
    )

    $session = Restore-ReboundSession -DeviceId $DeviceId
    if (($null -eq $session -or [string]::IsNullOrWhiteSpace($session.access_token)) -and -not $RestoreOnly) {
        $session = New-ReboundSession -DeviceId $DeviceId
    }
    if ($null -eq $session -or [string]::IsNullOrWhiteSpace($session.access_token)) {
        throw 'Project Rebound authentication did not return a valid session.'
    }
    return $session
}

function Get-AccessTokenRefreshAt {
    param([Parameter(Mandatory = $true)] [object] $Session)

    $fallback = [DateTimeOffset]::UtcNow.AddMinutes(10)
    if ([string]::IsNullOrWhiteSpace($Session.access_token_expires_at)) {
        return $fallback
    }
    try {
        $expiresAt = [DateTimeOffset]::Parse(
            [string] $Session.access_token_expires_at,
            [Globalization.CultureInfo]::InvariantCulture,
            [Globalization.DateTimeStyles]::AssumeUniversal
        ).ToUniversalTime()
        $refreshAt = $expiresAt.AddMinutes(-2)
        $minimum = [DateTimeOffset]::UtcNow.AddSeconds(5)
        if ($refreshAt -lt $minimum) {
            return $minimum
        }
        return $refreshAt
    }
    catch {
        Write-Host '[WARN] Access token expiry was invalid; using a 10-minute refresh interval.' -ForegroundColor Yellow
        return $fallback
    }
}

function Send-TunnelAccessToken {
    param([Parameter(Mandatory = $true)] [object] $Session)

    $accessToken = [string] $Session.access_token
    try {
        if ([string]::IsNullOrWhiteSpace($accessToken) -or $accessToken.Length -lt 32) {
            throw 'Project Rebound authentication returned a malformed access token.'
        }
        if ($null -eq $script:startedTunnel -or $script:startedTunnel.HasExited) {
            throw 'MetaTunnel exited before it could receive the access token.'
        }
        $script:startedTunnel.StandardInput.WriteLine($accessToken)
        $script:startedTunnel.StandardInput.Flush()
    }
    finally {
        $accessToken = $null
    }
}

function ConvertTo-NativeArgument {
    param([AllowEmptyString()] [string] $Value)

    if ($Value.Length -eq 0) {
        return '""'
    }
    if ($Value -notmatch '[\s"]') {
        return $Value
    }
    # Windows CommandLineToArgvW escaping: double backslashes before a quote and
    # at the end of a quoted argument.
    $quoted = New-Object Text.StringBuilder
    [void] $quoted.Append('"')
    $backslashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq '\') {
            $backslashes++
            continue
        }
        if ($character -eq '"') {
            [void] $quoted.Append(('\' * (($backslashes * 2) + 1)))
            [void] $quoted.Append('"')
            $backslashes = 0
            continue
        }
        if ($backslashes -gt 0) {
            [void] $quoted.Append(('\' * $backslashes))
            $backslashes = 0
        }
        [void] $quoted.Append($character)
    }
    if ($backslashes -gt 0) {
        [void] $quoted.Append(('\' * ($backslashes * 2)))
    }
    [void] $quoted.Append('"')
    return $quoted.ToString()
}

function Stop-StartedTunnel {
    if ($null -ne $script:startedTunnel -and -not $script:startedTunnel.HasExited) {
        try {
            $script:startedTunnel.StandardInput.Close()
        }
        catch {
            # The tunnel may already have closed its stdin while stopping.
        }
        $script:startedTunnel.Kill()
        $script:startedTunnel.WaitForExit()
    }
}

function Stop-StartedGame {
    if ($null -ne $script:startedGame -and -not $script:startedGame.HasExited) {
        if (-not $script:startedGame.CloseMainWindow() -or -not $script:startedGame.WaitForExit(3000)) {
            $script:startedGame.Kill()
            $script:startedGame.WaitForExit()
        }
    }
}

try {
    if (-not (Test-Path -LiteralPath $gameExecutable -PathType Leaf)) {
        throw "Game executable not found: $gameExecutable"
    }
    if ([string]::IsNullOrWhiteSpace($tunnelExecutable)) {
        throw "MetaTunnel not found in: $gameDirectory"
    }

    $deviceId = Get-DeviceFingerprint
    $session = Get-ReboundSession -DeviceId $deviceId

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $tunnelExecutable
    $startInfo.WorkingDirectory = $gameDirectory
    $tunnelArguments = @(
        '--meta-base-url', $metaBaseUrl,
        '--logic-address', $logicAddress,
        '--logic-server-name', $logicServerName,
        '--http-listen', '127.0.0.1:0',
        '--tcp-listen', '127.0.0.1:0',
        '--token-stdin=true'
    )
    $startInfo.Arguments = ($tunnelArguments | ForEach-Object {
        ConvertTo-NativeArgument ([string] $_)
    }) -join ' '
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.CreateNoWindow = $false

    $startedTunnel = [System.Diagnostics.Process]::new()
    $startedTunnel.StartInfo = $startInfo
    if (-not $startedTunnel.Start()) {
        throw 'Failed to start MetaTunnel.'
    }

    Send-TunnelAccessToken -Session $session

    $readinessTask = $startedTunnel.StandardOutput.ReadLineAsync()
    if (-not $readinessTask.Wait(10000)) {
        throw 'MetaTunnel did not become ready within 10 seconds.'
    }
    $readiness = $readinessTask.Result | ConvertFrom-Json
    if ($readiness.event -ne 'ready' -or [string]::IsNullOrWhiteSpace($readiness.http_url)) {
        throw 'MetaTunnel returned an invalid readiness response.'
    }
    $tunnelHttpUrl = [string] $readiness.http_url
    Write-Host "[READY] MetaTunnel HTTP: $tunnelHttpUrl"
    Write-Host "[READY] MetaTunnel TCP:  $($readiness.logic_endpoint)"

    $health = Invoke-RestMethod -Uri "$tunnelHttpUrl/_meta-tunnel/health/live" -Method Get -TimeoutSec 5
    if ($health.status -ne 'live') {
        throw 'MetaTunnel local health check failed.'
    }

    $forwardedArguments = @($GameArguments | Where-Object {
        $null -ne $_ -and -not [string]::IsNullOrWhiteSpace([string] $_)
    } | Where-Object {
        [string] $_ -notmatch '^-LogicServerURL='
    })
    if ($forwardedArguments -notcontains '-debuglog') {
        $forwardedArguments += '-debuglog'
    }
    $arguments = @("-LogicServerURL=$tunnelHttpUrl") + $forwardedArguments
    Write-Host "[START] $gameExecutable"
    Write-Host "[PARAM] $($arguments -join ' ')"

    $gameStartInfo = [Diagnostics.ProcessStartInfo]::new()
    $gameStartInfo.FileName = $gameExecutable
    $gameStartInfo.WorkingDirectory = $gameDirectory
    $gameStartInfo.UseShellExecute = $false
    $gameStartInfo.Arguments = ($arguments | ForEach-Object { ConvertTo-NativeArgument ([string] $_) }) -join ' '
    $startedGame = [Diagnostics.Process]::new()
    $startedGame.StartInfo = $gameStartInfo
    if (-not $startedGame.Start()) {
        throw 'Failed to start Boundary.'
    }

    $refreshAt = Get-AccessTokenRefreshAt -Session $session
    $session = $null
    while (-not $startedGame.WaitForExit(1000)) {
        if ($startedTunnel.HasExited) {
            throw "MetaTunnel exited unexpectedly with code $($startedTunnel.ExitCode)."
        }
        if ([DateTimeOffset]::UtcNow -ge $refreshAt) {
            try {
                Write-Host '[AUTH] Refreshing the Project Rebound session...'
                $session = Get-ReboundSession -DeviceId $deviceId -RestoreOnly
                Send-TunnelAccessToken -Session $session
                $refreshAt = Get-AccessTokenRefreshAt -Session $session
                Write-Host "[AUTH] MetaTunnel token updated; next refresh before $($refreshAt.ToLocalTime())."
                $session = $null
            }
            catch {
                Write-Host "[WARN] Session refresh failed; retrying in 30 seconds: $($_.Exception.Message)" -ForegroundColor Yellow
                $refreshAt = [DateTimeOffset]::UtcNow.AddSeconds(30)
            }
        }
    }
    exit $startedGame.ExitCode
}
catch {
    Write-Host "[ERROR] $($_.Exception.Message)" -ForegroundColor Red
    Stop-StartedGame
    Stop-StartedTunnel
    exit 1
}
finally {
    Stop-StartedGame
    Stop-StartedTunnel
}
