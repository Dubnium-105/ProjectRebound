<#
.SYNOPSIS
Starts a pinned-build local Boundary PVE dedicated server through MetaTunnel.

.DESCRIPTION
The default invocation starts only the dedicated server. Use -LaunchClient for
a one-command smoke test when no Boundary client is already running. Launcher
logs are written below %LOCALAPPDATA%\ProjectRebound\local-pve.

.EXAMPLE
powershell -ExecutionPolicy Bypass -File .\start-local-pve.ps1

.EXAMPLE
powershell -ExecutionPolicy Bypass -File .\start-local-pve.ps1 `
    -Map Warehouse -Difficulty normal -Port 7777 -LaunchClient

.EXAMPLE
powershell -ExecutionPolicy Bypass -File .\start-local-pve.ps1 `
    -Map Warehouse -Difficulty normal -Port 7777 -LaunchClient `
    -MultiMatchPlaylist 'Warehouse,OSS,DataCenter' `
    -MultiMatchVoteDurationSeconds 10
#>
[CmdletBinding()]
param(
    [ValidateSet('Warehouse', 'OSS', 'MiniFarm', 'DataCenter', 'CircularX')]
    [string] $Map = 'Warehouse',

    [ValidateSet('easy', 'normal', 'hard')]
    [string] $Difficulty = 'normal',

    [ValidateRange(1024, 65535)]
    [int] $Port = 7777,

    [ValidateRange(1, 10)]
    [int] $MaxPlayers = 4,

    [string] $GameDirectory =
        'C:\Steam\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64',

    [switch] $LaunchClient,

    [switch] $DisableClientQosCompatibility,

    [ValidateRange(15, 300)]
    [int] $ReadyTimeoutSeconds = 120,

    [ValidateRange(1, 3)]
    [int] $ServerStartAttempts = 2,

    [string] $MultiMatchPlaylist = '',

    [ValidateRange(10, 180)]
    [int] $MultiMatchTravelTimeoutSeconds = 45,

    [ValidateRange(0, 60)]
    [int] $MultiMatchVoteDurationSeconds = 15,

    [ValidateRange(1, 3)]
    [int] $MultiMatchVoteCandidateCount = 3,

    [switch] $DisableMultiMatchVote,

    [switch] $DryRun
)

$ErrorActionPreference = 'Stop'

$expectedExeSha256 =
    '181C49FFB522B3EB01014C84FD9D3A2A5C0B66AE80A6A6ADDFF4BDD6F8125843'
$expectedSteamAppId = '1364020'
$gameDirectoryPath = [IO.Path]::GetFullPath($GameDirectory)
$gameExecutable = Join-Path $gameDirectoryPath 'ProjectBoundarySteam-Win64-Shipping.exe'
$startGameScript = Join-Path $gameDirectoryPath 'startgame.ps1'
$localQosScript = Join-Path $gameDirectoryPath 'local-qos-compat.ps1'
$payloadDll = Join-Path $gameDirectoryPath 'Payload.dll'
$steamAppIdFile = Join-Path $gameDirectoryPath 'steam_appid.txt'
$authCache = Join-Path $gameDirectoryPath '.project-rebound-auth.json'
$powershellExecutable = (Get-Command powershell.exe -ErrorAction Stop).Source

$modeByDifficulty = @{
    easy = '/Game/Online/GameMode/BP_PBGameMode_Rush_PVE_Easy.BP_PBGameMode_Rush_PVE_Easy_C'
    normal = '/Game/Online/GameMode/BP_PBGameMode_Rush_PVE_Normal.BP_PBGameMode_Rush_PVE_Normal_C'
    hard = '/Game/Online/GameMode/BP_PBGameMode_Rush_PVE_Hard.BP_PBGameMode_Rush_PVE_Hard_C'
}
$modePath = $modeByDifficulty[$Difficulty]
$pveMaps = @('Warehouse', 'OSS', 'MiniFarm', 'DataCenter', 'CircularX')
$canonicalPveMaps = @{}
foreach ($pveMap in $pveMaps) {
    $canonicalPveMaps[$pveMap.ToLowerInvariant()] = $pveMap
}

$multiMatchMaps = [Collections.Generic.List[string]]::new()
$seenMultiMatchMaps = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::OrdinalIgnoreCase)
if (-not [string]::IsNullOrWhiteSpace($MultiMatchPlaylist)) {
    foreach ($requestedMap in $MultiMatchPlaylist.Split(',')) {
        $mapAlias = $requestedMap.Trim()
        $mapKey = $mapAlias.ToLowerInvariant()
        if (-not $canonicalPveMaps.ContainsKey($mapKey)) {
            throw "Unknown or PVE-incompatible multi-match map: '$mapAlias'."
        }
        $canonicalMap = [string] $canonicalPveMaps[$mapKey]
        if (-not $seenMultiMatchMaps.Add($canonicalMap)) {
            throw "Duplicate multi-match map: '$canonicalMap'."
        }
        $multiMatchMaps.Add($canonicalMap)
    }
    if (-not $seenMultiMatchMaps.Contains($Map)) {
        throw "The initial map '$Map' must be present in -MultiMatchPlaylist."
    }
}

function ConvertTo-NativeArgument {
    param([Parameter(Mandatory = $true)] [string] $Value)

    if ($Value.Length -gt 0 -and $Value -notmatch '[\s"]') {
        return $Value
    }

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

function Get-BoundaryProcesses {
    try {
        return @(Get-CimInstance Win32_Process -ErrorAction Stop | Where-Object {
            $_.ExecutablePath -eq $gameExecutable
        } | ForEach-Object {
            [pscustomobject]@{
                ProcessId = [int] $_.ProcessId
                ParentProcessId = [int] $_.ParentProcessId
                ExecutablePath = [string] $_.ExecutablePath
                CommandLine = [string] $_.CommandLine
                InspectionComplete = $true
                CreationDate = $_.CreationDate
            }
        })
    }
    catch {
        return @(Get-Process -Name 'ProjectBoundarySteam-Win64-Shipping' `
            -ErrorAction SilentlyContinue | Where-Object {
                try { $_.Path -eq $gameExecutable } catch { $false }
            } | ForEach-Object {
                [pscustomobject]@{
                    ProcessId = [int] $_.Id
                    ParentProcessId = 0
                    ExecutablePath = [string] $_.Path
                    CommandLine = $null
                    InspectionComplete = $false
                    CreationDate = $_.StartTime
                }
            })
    }
}

function Test-ExactServerSwitch {
    param([string] $CommandLine)

    if ([string]::IsNullOrWhiteSpace($CommandLine)) {
        return $false
    }
    return $CommandLine -match '(?i)(?:^|\s)-server(?:\s|$)'
}

function Start-ReboundLauncher {
    param(
        [Parameter(Mandatory = $true)] [string[]] $GameArguments,
        [Parameter(Mandatory = $true)] [string] $AuthSessionScope,
        [Parameter(Mandatory = $true)] [string] $StdoutPath,
        [Parameter(Mandatory = $true)] [string] $StderrPath
    )

    $launcherArguments = @(
        '-NoProfile',
        '-ExecutionPolicy', 'Bypass',
        '-File', $startGameScript,
        '-AuthSessionScope', $AuthSessionScope
    ) + $GameArguments

    Start-Process `
        -FilePath $powershellExecutable `
        -ArgumentList (($launcherArguments | ForEach-Object {
            ConvertTo-NativeArgument ([string] $_)
        }) -join ' ') `
        -WorkingDirectory $gameDirectoryPath `
        -WindowStyle Hidden `
        -RedirectStandardOutput $StdoutPath `
        -RedirectStandardError $StderrPath `
        -PassThru
}

function Wait-NewBoundaryProcess {
    param(
        [Parameter(Mandatory = $true)]
        [AllowEmptyCollection()]
        [Collections.Generic.HashSet[int]] $ExistingPids,
        [Parameter(Mandatory = $true)] [Diagnostics.Process] $Launcher,
        [Parameter(Mandatory = $true)] [bool] $ExpectServer,
        [Parameter(Mandatory = $true)] [datetime] $Deadline,
        [Parameter(Mandatory = $true)] [string] $StdoutPath,
        [Parameter(Mandatory = $true)] [string] $StderrPath,
        [switch] $AllowIndirectChild
    )

    $stableCandidatePid = 0
    $stableCandidateSince = [datetime]::MinValue
    while ([datetime]::UtcNow -lt $Deadline) {
        $candidate = Get-BoundaryProcesses | Where-Object {
            -not $ExistingPids.Contains([int] $_.ProcessId) -and
            ($AllowIndirectChild -or -not $_.InspectionComplete -or
                [int] $_.ParentProcessId -eq $Launcher.Id) -and
            (-not $_.InspectionComplete -or
                (Test-ExactServerSwitch $_.CommandLine) -eq $ExpectServer)
        } | Sort-Object CreationDate -Descending | Select-Object -First 1
        if ($null -ne $candidate) {
            $candidatePid = [int] $candidate.ProcessId
            if ($stableCandidatePid -ne $candidatePid) {
                $stableCandidatePid = $candidatePid
                $stableCandidateSince = [datetime]::UtcNow
            }
            elseif (([datetime]::UtcNow - $stableCandidateSince).TotalMilliseconds -ge 1000 -and
                $null -ne (Get-Process -Id $candidatePid -ErrorAction SilentlyContinue)) {
                return $candidate
            }
        }
        else {
            $stableCandidatePid = 0
            $stableCandidateSince = [datetime]::MinValue
        }

        $Launcher.Refresh()
        if ($Launcher.HasExited) {
            $stdoutTail = if (Test-Path -LiteralPath $StdoutPath) {
                (Get-Content -LiteralPath $StdoutPath -Tail 20) -join [Environment]::NewLine
            }
            else { '' }
            $stderrTail = if (Test-Path -LiteralPath $StderrPath) {
                (Get-Content -LiteralPath $StderrPath -Tail 20) -join [Environment]::NewLine
            }
            else { '' }
            throw "startgame.ps1 exited before Boundary started. stdout=$stdoutTail stderr=$stderrTail"
        }
        Start-Sleep -Milliseconds 250
    }

    throw "Boundary process did not appear within $ReadyTimeoutSeconds seconds."
}

function Wait-ServerUdpEndpoint {
    param(
        [Parameter(Mandatory = $true)] [int] $ProcessId,
        [Parameter(Mandatory = $true)] [datetime] $Deadline
    )

    while ([datetime]::UtcNow -lt $Deadline) {
        if ($null -eq (Get-Process -Id $ProcessId -ErrorAction SilentlyContinue)) {
            throw "Dedicated server process $ProcessId exited before it became ready."
        }

        $endpoint = Get-NetUDPEndpoint -ErrorAction SilentlyContinue | Where-Object {
            $_.OwningProcess -eq $ProcessId -and $_.LocalPort -eq $Port
        } | Select-Object -First 1
        if ($null -ne $endpoint) {
            return $endpoint
        }
        Start-Sleep -Milliseconds 500
    }

    throw "Dedicated server process $ProcessId did not bind UDP port $Port within $ReadyTimeoutSeconds seconds."
}

foreach ($requiredPath in @($gameExecutable, $startGameScript, $payloadDll, $steamAppIdFile)) {
    if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
        throw "Required file is missing: $requiredPath"
    }
}

$actualExeSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $gameExecutable).Hash
if ($actualExeSha256 -ne $expectedExeSha256) {
    throw "Unsupported Boundary executable SHA-256: $actualExeSha256"
}
$actualSteamAppId = (Get-Content -LiteralPath $steamAppIdFile -Raw -Encoding UTF8).Trim()
if ($actualSteamAppId -ne $expectedSteamAppId) {
    throw (
        "Boundary platform AppID must be $expectedSteamAppId for client login; " +
        "found '$actualSteamAppId' in $steamAppIdFile. AppID 480 stalls before MetaTunnel."
    )
}
if (-not (Test-Path -LiteralPath $authCache -PathType Leaf)) {
    throw "MetaTunnel auth cache is missing. Run startgame.ps1 interactively once before local PVE."
}

$serverArguments = @(
    '-server',
    '-pve',
    '-LocalPveLoadout',
    '-log',
    '-nullrhi',
    '-nosplash',
    '-NoWindow',
    "-map=$Map",
    "-mode=$modePath",
    "-port=$Port",
    "-external=$Port",
    '-servername=LocalPVE',
    '-serverregion=local',
    "-maxplayers=$MaxPlayers",
    '-debuglog'
)
$clientArguments = @("-match=127.0.0.1:$Port", '-debuglog')

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$sessionBaseDirectory = if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
    [IO.Path]::GetTempPath()
}
else {
    Join-Path $env:LOCALAPPDATA 'ProjectRebound\local-pve'
}
$sessionDirectory = Join-Path $sessionBaseDirectory $stamp
$multiMatchConfigPath = Join-Path $sessionDirectory 'serverconfig.multimatch.json'
$multiMatchConfig = $null
if ($multiMatchMaps.Count -gt 0) {
    $multiMatchConfig = [ordered]@{
        map = $Map
        mode = 'pve'
        multiMatch = [ordered]@{
            enabled = $true
            playlist = @($multiMatchMaps)
            travelTimeoutSeconds = $MultiMatchTravelTimeoutSeconds
            vote = [ordered]@{
                enabled = -not $DisableMultiMatchVote
                durationSeconds = $MultiMatchVoteDurationSeconds
                candidateCount = $MultiMatchVoteCandidateCount
            }
        }
    }
    $serverArguments += @(
        '-DedicatedMultiMatch',
        "-multimatchconfig=$multiMatchConfigPath"
    )
}
if ($DryRun) {
    [pscustomobject]@{
        DryRun = $true
        GameExecutable = $gameExecutable
        ExecutableSha256 = $actualExeSha256
        PayloadSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $payloadDll).Hash
        SteamAppId = $actualSteamAppId
        ServerAuthSessionScope = 'local-pve'
        ServerStartAttempts = $ServerStartAttempts
        ServerArguments = $serverArguments
        ClientArguments = if ($LaunchClient) {
            if ($DisableClientQosCompatibility) {
                $clientArguments
            }
            else {
                $clientArguments + @(
                    '-LocalPveQosDiscoveryUrl',
                    '<dynamic-local-qos-url>'
                )
            }
        }
        else { @() }
        ClientQosCompatibility = $LaunchClient -and -not $DisableClientQosCompatibility
        MultiMatchConfigPath = if ($null -ne $multiMatchConfig) {
            $multiMatchConfigPath
        }
        else { $null }
        MultiMatchConfig = $multiMatchConfig
        SessionDirectory = $sessionDirectory
    }
    return
}

$existingProcesses = Get-BoundaryProcesses
$incompletelyInspectedProcess = $existingProcesses | Where-Object {
    -not $_.InspectionComplete
} | Select-Object -First 1
if ($null -ne $incompletelyInspectedProcess) {
    throw (
        "Boundary PID $($incompletelyInspectedProcess.ProcessId) is already running, " +
        'but its command line is unavailable; refusing to guess whether it is a client or server.')
}
$existingServer = $existingProcesses | Where-Object {
    Test-ExactServerSwitch $_.CommandLine
} | Select-Object -First 1
if ($null -ne $existingServer) {
    throw "A Boundary dedicated server is already running (PID $($existingServer.ProcessId))."
}
if ($LaunchClient) {
    $existingClient = $existingProcesses | Where-Object {
        -not (Test-ExactServerSwitch $_.CommandLine)
    } | Select-Object -First 1
    if ($null -ne $existingClient) {
        throw "-LaunchClient cannot be used while a Boundary client is already running (PID $($existingClient.ProcessId))."
    }
}

$occupiedEndpoint = Get-NetUDPEndpoint -ErrorAction SilentlyContinue | Where-Object {
    $_.LocalPort -eq $Port
} | Select-Object -First 1
if ($null -ne $occupiedEndpoint) {
    throw "UDP port $Port is already owned by PID $($occupiedEndpoint.OwningProcess)."
}

New-Item -ItemType Directory -Path $sessionDirectory -Force | Out-Null
if ($null -ne $multiMatchConfig) {
    $multiMatchJson = $multiMatchConfig | ConvertTo-Json -Depth 6
    [IO.File]::WriteAllText(
        $multiMatchConfigPath,
        $multiMatchJson,
        [Text.UTF8Encoding]::new($false))
}
$existingPids = [Collections.Generic.HashSet[int]]::new()
foreach ($process in $existingProcesses) {
    [void] $existingPids.Add([int] $process.ProcessId)
}

$serverLauncher = $null
$serverProcess = $null
$serverEndpoint = $null
$serverAttempt = 0
$serverFailures = [Collections.Generic.List[string]]::new()

while ($serverAttempt -lt $ServerStartAttempts -and $null -eq $serverEndpoint) {
    $serverAttempt++
    $logSuffix = if ($serverAttempt -eq 1) { '' } else { ".attempt-$serverAttempt" }
    $serverStdout = Join-Path $sessionDirectory "server-launcher$logSuffix.stdout.log"
    $serverStderr = Join-Path $sessionDirectory "server-launcher$logSuffix.stderr.log"

    $serverLauncher = Start-ReboundLauncher `
        -GameArguments $serverArguments `
        -AuthSessionScope 'local-pve' `
        -StdoutPath $serverStdout `
        -StderrPath $serverStderr
    $serverDeadline = [datetime]::UtcNow.AddSeconds($ReadyTimeoutSeconds)

    try {
        $serverProcess = Wait-NewBoundaryProcess `
            -ExistingPids $existingPids `
            -Launcher $serverLauncher `
            -ExpectServer $true `
            -Deadline $serverDeadline `
            -StdoutPath $serverStdout `
            -StderrPath $serverStderr
        $serverEndpoint = Wait-ServerUdpEndpoint `
            -ProcessId ([int] $serverProcess.ProcessId) `
            -Deadline $serverDeadline
    }
    catch {
        $failureMessage = $_.Exception.Message
        $serverStillRunning = $null -ne $serverProcess -and
            $null -ne (Get-Process -Id ([int] $serverProcess.ProcessId) -ErrorAction SilentlyContinue)
        if ($serverStillRunning) {
            throw "$failureMessage Logs: $serverStdout ; $serverStderr"
        }

        $launcherDeadline = [datetime]::UtcNow.AddSeconds(5)
        while ([datetime]::UtcNow -lt $launcherDeadline) {
            $serverLauncher.Refresh()
            if ($serverLauncher.HasExited) { break }
            Start-Sleep -Milliseconds 200
        }
        $launcherExit = if ($serverLauncher.HasExited) {
            [string] $serverLauncher.ExitCode
        }
        else { 'pending' }
        $stdoutTail = if (Test-Path -LiteralPath $serverStdout) {
            (Get-Content -LiteralPath $serverStdout -Tail 12) -join ' | '
        }
        else { '' }
        $serverFailures.Add(
            "attempt=$serverAttempt launcher_exit=$launcherExit error=$failureMessage stdout_tail=$stdoutTail")

        if ($serverAttempt -ge $ServerStartAttempts) {
            throw "Dedicated server failed after $ServerStartAttempts attempt(s). " +
                "Session logs: $sessionDirectory. " + ($serverFailures -join ' || ')
        }

        Write-Warning "Dedicated server attempt $serverAttempt exited during world travel; retrying once. Logs: $serverStdout"
        Start-Sleep -Seconds 2
    }
}

$result = [ordered]@{
    SessionDirectory = $sessionDirectory
    ServerLauncherPid = $serverLauncher.Id
    ServerPid = [int] $serverProcess.ProcessId
    ServerAttempts = $serverAttempt
    ServerEndpoint = "$($serverEndpoint.LocalAddress):$($serverEndpoint.LocalPort)"
    ServerArguments = $serverArguments
    MultiMatchConfigPath = if ($null -ne $multiMatchConfig) {
        $multiMatchConfigPath
    }
    else { $null }
    ClientLauncherPid = $null
    ClientPid = $null
    QosCompatibilityPid = $null
    QosDiscoveryUrl = $null
    QosUdpEndpoint = $null
}

if ($LaunchClient) {
    if (-not $DisableClientQosCompatibility -and
        -not (Test-Path -LiteralPath $localQosScript -PathType Leaf)) {
        throw "Local QoS compatibility helper is missing: $localQosScript"
    }

    Start-Sleep -Seconds 2
    $beforeClient = Get-BoundaryProcesses
    $beforeClientPids = [Collections.Generic.HashSet[int]]::new()
    foreach ($process in $beforeClient) {
        [void] $beforeClientPids.Add([int] $process.ProcessId)
    }

    $qosProcess = $null
    try {
        if (-not $DisableClientQosCompatibility) {
            $qosLog = Join-Path $sessionDirectory 'local-qos-compat.log'
            $qosArguments = @(
                '-NoProfile',
                '-ExecutionPolicy', 'Bypass',
                '-File', $localQosScript,
                '-ParentProcessId', [string] $serverProcess.ProcessId,
                '-LogPath', $qosLog
            )
            $qosStartInfo = [Diagnostics.ProcessStartInfo]::new()
            $qosStartInfo.FileName = $powershellExecutable
            $qosStartInfo.WorkingDirectory = $gameDirectoryPath
            $qosStartInfo.UseShellExecute = $false
            $qosStartInfo.CreateNoWindow = $true
            $qosStartInfo.RedirectStandardOutput = $true
            $qosStartInfo.RedirectStandardError = $true
            $qosStartInfo.Arguments = ($qosArguments | ForEach-Object {
                ConvertTo-NativeArgument ([string] $_)
            }) -join ' '
            $qosProcess = [Diagnostics.Process]::new()
            $qosProcess.StartInfo = $qosStartInfo
            if (-not $qosProcess.Start()) {
                throw 'Failed to start the local QoS compatibility helper.'
            }
            $qosReadyTask = $qosProcess.StandardOutput.ReadLineAsync()
            if (-not $qosReadyTask.Wait(10000)) {
                throw 'Local QoS compatibility helper did not become ready within 10 seconds.'
            }
            $qosReady = $qosReadyTask.Result | ConvertFrom-Json
            if ($qosReady.event -ne 'ready' -or
                [string]::IsNullOrWhiteSpace([string] $qosReady.http_url) -or
                [string] $qosReady.http_url -notmatch '^http://127\.0\.0\.1:\d+/servers$') {
                throw 'Local QoS compatibility helper returned invalid readiness data.'
            }
            $clientArguments += @(
                '-LocalPveQosDiscoveryUrl',
                [string] $qosReady.http_url
            )
            $result.QosCompatibilityPid = $qosProcess.Id
            $result.QosDiscoveryUrl = [string] $qosReady.http_url
            $result.QosUdpEndpoint = [string] $qosReady.udp_endpoint
        }

        $clientStdout = Join-Path $sessionDirectory 'client-launcher.stdout.log'
        $clientStderr = Join-Path $sessionDirectory 'client-launcher.stderr.log'
        $clientLauncher = Start-ReboundLauncher `
            -GameArguments $clientArguments `
            -AuthSessionScope 'default' `
            -StdoutPath $clientStdout `
            -StderrPath $clientStderr
        $clientProcess = Wait-NewBoundaryProcess `
            -ExistingPids $beforeClientPids `
            -Launcher $clientLauncher `
            -ExpectServer $false `
            -Deadline ([datetime]::UtcNow.AddSeconds($ReadyTimeoutSeconds)) `
            -StdoutPath $clientStdout `
            -StderrPath $clientStderr `
            -AllowIndirectChild:$($null -ne $qosProcess)
        $result.ClientLauncherPid = $clientLauncher.Id
        $result.ClientPid = [int] $clientProcess.ProcessId
    }
    catch {
        if ($null -ne $qosProcess -and -not $qosProcess.HasExited) {
            Stop-Process -Id $qosProcess.Id -ErrorAction SilentlyContinue
        }
        throw
    }
}

[pscustomobject] $result
