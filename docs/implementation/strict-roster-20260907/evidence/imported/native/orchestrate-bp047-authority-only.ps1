<#!
Dedicated component diagnostic for one locked Dedicated authority and one real
Steam client.  This file intentionally lives under .tmp and is not a product
launcher.  It never opens a game connection directly: the only connection
entry point is the Payload named-pipe `join` request carrying the signed grant.

The script is fail-closed around the control-plane handoff.  A fixture is only
accepted when it is an explicitly marked component fixture and contains a
signed allocation.  The authority is started first; a separate backend
 authority-ready marker must match the actual world/endpoint returned by the
 Payload.  The real client is then cold-started and its owned Payload pipe must
 report match.login_completed and match.login_ready for the exact attempt,
 session, world, roster, and route before the runner writes the client-ready
 marker that releases the short-lived real-seat grant.  Static grants or a
 world copied from the initial fixture are never used.  Every connected pipe
 is checked with GetNamedPipeServerProcessId against the captured game PID,
 creation time, executable path, and process handle before any frame is
 written.  The script does not manufacture that evidence and does not call
 Reserve/Confirm itself; the normal signed Dedicated registration worker or a
 separately audited test-driver must produce it.  The runner sends Payload
 receipt commands only after matching private backend receipt files prove the
 service-side operation; it never invokes Backend Reserve/Confirm itself.
 Therefore a run cannot be reported positive merely because a game process or
 a pipe is alive.

Secrets (allocation, grants, keys, credentials and tickets) are held only in
memory.  Evidence contains hashes and scope metadata, never token contents or
raw Steam IDs.  The install is restored in `finally`, including after a
launcher failure.
#>

param(
    [Parameter(Mandatory = $true)]
    [ValidateScript({ Test-Path -LiteralPath $_ -PathType Leaf })]
    [string] $FixturePath,

    [string] $EvidenceRoot = 'C:\wksp\ProjectRebound\.tmp\strict-roster-20260907\native-evidence',
    [string] $GameWin64 = 'C:\Steam\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64',
    [string] $CandidatePayload = 'C:\wksp\ProjectRebound\Payload\x64\Release\Payload.dll',
    [string] $OriginalPayloadBackup = 'C:\Users\23587\AppData\Local\ProjectRebound\payload-backups\strict-roster-20260907-20260907-094638-Payload.dll',
    [int] $AuthorityPort = 47777,
    [int] $ClientPort = 47778,
    [int] $TimeoutSeconds = 120,
    [string] $AuthorityAuthSessionScope = 'dedicated-authority',
    [string] $ClientAuthSessionScope = 'dedicated-client',
    [string] $BackendAuthorityReadyEvidencePath,
    [string] $BackendGrantEvidencePath,
    [string] $BackendScopedEventPath,
    [string] $BackendReceiptDir,
    [string] $BackendAdmissionEvidencePath,
    [string] $ClientReadyMarkerPath,
    [switch] $Execute,

    # After a real scoped DISCONNECTED/release chain, stop only the captured
    # Dedicated authority handle and write the handle-bound exit proof consumed
    # by the isolated BP047 cleanup driver.
    [switch] $Bp047Cleanup,

    # Bounded BP047 cleanup-only diagnostic.  It stops the captured authority
    # after a real allocation/world/listen boundary, without inventing a
    # client login or DISCONNECTED event.  The resulting handle proof is
    # consumed by the isolated Backend cleanup fixture.
    [switch] $Bp047AuthorityOnly
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$ExpectedExecutableSha256 = '181C49FFB522B3EB01014C84FD9D3A2A5C0B66AE80A6A6ADDFF4BDD6F8125843'
$ExpectedOriginalPayloadSha256 = '6C7B5E05540AC72A6D7A9FA78F867917285FCE00081156EE13D237AA4D6C24A3'
$ExpectedCandidatePayloadSha256 = '297C6EA8585C8A606B3AB31A9949BCB153D7469B890EF8603521FB8AF2CBFC1B'
$ExpectedPayloadProtocol = 'strict-roster-v2'

function Initialize-NativePipeApi {
    if ($null -eq ('StrictRosterNativePipe' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class StrictRosterNativePipe
{
    [DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool GetNamedPipeServerProcessId(
        IntPtr pipe,
        out uint serverProcessId);

    [DllImport("kernel32.dll", SetLastError = true)]
    public static extern uint GetProcessId(IntPtr processHandle);
}
'@
    }
}

function Get-Sha256 {
    param([Parameter(Mandatory = $true)] [string] $Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToUpperInvariant()
}

function Get-TextSha256 {
    param([AllowEmptyString()] [string] $Value)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes([string] $Value)
        return (($sha.ComputeHash($bytes) | ForEach-Object { $_.ToString('x2') }) -join '')
    }
    finally {
        $sha.Dispose()
    }
}

function Get-OptionalValue {
    param(
        [Parameter(Mandatory = $true)] [object] $Object,
        [Parameter(Mandatory = $true)] [string] $Name,
        [AllowNull()] [object] $Default = $null
    )
    if ($Object -is [Collections.IDictionary]) {
        if ($Object.Contains($Name)) { return $Object[$Name] }
        return $Default
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $Default }
    return $property.Value
}

function Require-String {
    param(
        [Parameter(Mandatory = $true)] [object] $Object,
        [Parameter(Mandatory = $true)] [string] $Name,
        [int] $MaxLength = 0
    )
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property -or $property.Value -isnot [string] -or [string]::IsNullOrWhiteSpace([string] $property.Value)) {
        throw "fixture field '$Name' is required"
    }
    $value = [string] $property.Value
    if ($MaxLength -gt 0 -and $value.Length -gt $MaxLength) {
        throw "fixture field '$Name' is too long"
    }
    return $value
}

function Require-Int {
    param([Parameter(Mandatory = $true)] [object] $Object, [Parameter(Mandatory = $true)] [string] $Name)
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property -or $property.Value -isnot [int] -and $property.Value -isnot [long] -and $property.Value -isnot [double]) {
        throw "fixture field '$Name' is required"
    }
    $value = [int] $property.Value
    if ($value -le 0) { throw "fixture field '$Name' must be positive" }
    return $value
}

function Get-GameProcesses {
    param([Parameter(Mandatory = $true)] [string] $PipeName)
    $needle = "-pipe=$PipeName"
    return @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object {
            $_.Name -ieq 'ProjectBoundarySteam-Win64-Shipping.exe' -and
            ([string] $_.CommandLine).IndexOf($needle, [StringComparison]::OrdinalIgnoreCase) -ge 0
        })
}

function Assert-NamedPipeServerOwnership {
    param(
        [Parameter(Mandatory = $true)] [IO.Pipes.NamedPipeClientStream] $Client,
        [Parameter(Mandatory = $true)] [Diagnostics.Process] $ExpectedServerProcess,
        [Parameter(Mandatory = $true)] [string] $PipeName
    )
    Initialize-NativePipeApi
    try {
        $ExpectedServerProcess.Refresh()
        if ($ExpectedServerProcess.HasExited) {
            throw "expected Boundary process exited while validating pipe '$PipeName'"
        }
        $bindingProperty = $ExpectedServerProcess.PSObject.Properties['OwnedCreationTimeUtc']
        if ($null -eq $bindingProperty -or $null -eq $bindingProperty.Value) {
            throw "expected Boundary process has no captured creation-time binding"
        }
        $capturedCreation = [DateTime] $bindingProperty.Value
        $currentCreation = $ExpectedServerProcess.StartTime.ToUniversalTime()
        if ($currentCreation.Ticks -ne $capturedCreation.Ticks) {
            throw "expected Boundary PID was reused while validating pipe '$PipeName'"
        }
        $expectedPathProperty = $ExpectedServerProcess.PSObject.Properties['OwnedExecutablePath']
        $expectedPath = if ($null -ne $expectedPathProperty) { [string] $expectedPathProperty.Value } else { '' }
        if ([string]::IsNullOrWhiteSpace($expectedPath) -or
            [IO.Path]::GetFullPath([string] $ExpectedServerProcess.MainModule.FileName) -ne $expectedPath) {
            throw "expected Boundary process executable changed while validating pipe '$PipeName'"
        }
        $serverProcessId = [uint32] 0
        $pipeHandle = $Client.SafePipeHandle.DangerousGetHandle()
        if ($Client.SafePipeHandle.IsInvalid -or $Client.SafePipeHandle.IsClosed -or
            -not [StrictRosterNativePipe]::GetNamedPipeServerProcessId($pipeHandle, [ref] $serverProcessId)) {
            $errorCode = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
            throw "GetNamedPipeServerProcessId failed for '$PipeName' (win32=$errorCode)"
        }
        if ($serverProcessId -ne [uint32] $ExpectedServerProcess.Id) {
            throw "named pipe '$PipeName' is owned by PID $serverProcessId, expected PID $($ExpectedServerProcess.Id)"
        }
        $handleProcessId = [StrictRosterNativePipe]::GetProcessId([IntPtr] $ExpectedServerProcess.Handle)
        if ($handleProcessId -eq 0 -or $handleProcessId -ne $serverProcessId) {
            throw "owned process handle does not resolve to pipe server PID $serverProcessId"
        }
        return [pscustomobject]@{
            PipeName = $PipeName
            ServerProcessId = [int] $serverProcessId
            CreationTimeUtc = $currentCreation
            HandleProcessId = [int] $handleProcessId
        }
    }
    catch {
        throw "owned pipe server verification failed for '$PipeName': $($_.Exception.Message)"
    }
}

function Get-OwnedGameProcess {
    param(
        [Parameter(Mandatory = $true)] [string] $PipeName,
        [Parameter(Mandatory = $true)] [string] $ExpectedExecutablePath,
        [int] $TimeoutSeconds = 30
    )
    Initialize-NativePipeApi
    $expectedPath = [IO.Path]::GetFullPath($ExpectedExecutablePath)
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        $candidates = @(Get-GameProcesses -PipeName $PipeName)
        if ($candidates.Count -gt 1) {
            throw "multiple Boundary processes claim owned pipe '$PipeName'"
        }
        if ($candidates.Count -eq 1) {
            try {
                $process = [Diagnostics.Process]::GetProcessById([int] $candidates[0].ProcessId)
                $process.Refresh()
                if ($process.HasExited) { throw 'candidate Boundary process exited before ownership capture' }
                $path = [string] $process.MainModule.FileName
                if ([IO.Path]::GetFullPath($path) -ne $expectedPath) {
                    throw "owned pipe '$PipeName' belongs to an unexpected executable"
                }
                $creationTimeUtc = $process.StartTime.ToUniversalTime()
                $handleProcessId = [StrictRosterNativePipe]::GetProcessId([IntPtr] $process.Handle)
                if ($handleProcessId -eq 0 -or $handleProcessId -ne [uint32] $process.Id) {
                    throw "owned Boundary process handle does not resolve to PID $($process.Id)"
                }
                $process | Add-Member -MemberType NoteProperty -Name OwnedCreationTimeUtc -Value $creationTimeUtc -Force
                $process | Add-Member -MemberType NoteProperty -Name OwnedExecutablePath -Value $expectedPath -Force
                $process | Add-Member -MemberType NoteProperty -Name OwnedHandleProcessId -Value ([int] $handleProcessId) -Force
                return $process
            }
            catch [ComponentModel.Win32Exception] {
                # The process may be between creation and module initialization.
            }
            catch [InvalidOperationException] {
                # The process may exit while its module path is being read.
            }
        }
        Start-Sleep -Milliseconds 250
    }
    throw "timed out locating the exact Boundary process for owned pipe '$PipeName'"
}

function Assert-NoBoundaryProcess {
    $gameProcesses = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -ieq 'ProjectBoundarySteam-Win64-Shipping.exe' })
    if ($gameProcesses.Count -ne 0) {
        $ids = ($gameProcesses | ForEach-Object { $_.ProcessId }) -join ','
        throw "a Boundary process is already running (pid=$ids); refusing cross-run ownership"
    }
}

function New-RequestId {
    param([Parameter(Mandatory = $true)] [string] $Prefix)
    return "$Prefix-$([guid]::NewGuid().ToString('N'))"
}

function Send-PayloadFrame {
    param(
        [Parameter(Mandatory = $true)] [string] $PipeName,
        [Parameter(Mandatory = $true)] [string] $Command,
        [Parameter(Mandatory = $true)] [hashtable] $Arguments,
        [Parameter(Mandatory = $true)] [Diagnostics.Process] $ExpectedServerProcess,
        [int] $ConnectTimeoutMs = 1500,
        [int] $ResponseTimeoutMs = 10000
    )
    $client = [IO.Pipes.NamedPipeClientStream]::new(
        '.', $PipeName, [IO.Pipes.PipeDirection]::InOut, [IO.Pipes.PipeOptions]::None)
    try {
        $client.Connect($ConnectTimeoutMs)
        # Resolve the server PID from the connected pipe handle before writing
        # even a status frame. This binds every frame, including allocation
        # and grant delivery, to the exact process captured for this run.
        [void] (Assert-NamedPipeServerOwnership -Client $client -ExpectedServerProcess $ExpectedServerProcess -PipeName $PipeName)
        $json = $Arguments | ConvertTo-Json -Compress -Depth 8
        $frame = "$Command`t$json`n"
        $bytes = [Text.Encoding]::UTF8.GetBytes($frame)
        $client.Write($bytes, 0, $bytes.Length)
        $client.Flush()
        $reader = [IO.StreamReader]::new($client, [Text.Encoding]::UTF8, $false, 4096, $true)
        try {
            # NamedPipeClientStream reports CanTimeout=false on Windows; its
            # ReadTimeout setter throws and used to dispose the pipe before
            # the Payload could write the ACK (ERROR_NO_DATA/232).  Keep the
            # client alive while the async read is bounded by a Task wait.
            $readTask = $reader.ReadLineAsync()
            if (-not $readTask.Wait($ResponseTimeoutMs)) {
                throw "timed out waiting for a response for $Command"
            }
            $line = $readTask.Result
        }
        finally {
            $reader.Dispose()
        }
        if ([string]::IsNullOrWhiteSpace($line)) { throw "empty response for $Command" }
        $parts = $line.Split("`t", 2)
        if ($parts.Count -ne 2) { throw "malformed response for $Command" }
        $data = $parts[1] | ConvertFrom-Json
        if ([string] $parts[0] -eq 'error') {
            $errorRequestId = [string] (Get-OptionalValue $data 'request_id' '')
            if ($errorRequestId -ne [string] $Arguments['request_id']) { throw "response request_id mismatch for $Command error" }
            throw "Payload rejected ${Command}: code=$([string] (Get-OptionalValue $data 'code' 'unknown')) message=$([string] (Get-OptionalValue $data 'message' ''))"
        }
        $expectedResponseCommand = "${Command}_ack"
        if ([string] $parts[0] -ne $expectedResponseCommand) {
            throw "unexpected response command '$($parts[0])' for $Command; expected '$expectedResponseCommand'"
        }
        $expectedRequestId = [string] (Get-OptionalValue $Arguments 'request_id' '')
        $actualRequestId = [string] (Get-OptionalValue $data 'request_id' '')
        if ([string]::IsNullOrWhiteSpace($expectedRequestId) -or $actualRequestId -ne $expectedRequestId) {
            throw "response request_id mismatch for $Command (expected '$expectedRequestId', got '$actualRequestId')"
        }
        return [pscustomobject]@{
            Command = [string] $parts[0]
            Data = $data
        }
    }
    finally {
        $client.Dispose()
    }
}

function Wait-PayloadResponse {
    param(
        [Parameter(Mandatory = $true)] [string] $PipeName,
        [Parameter(Mandatory = $true)] [string] $Command,
        [Parameter(Mandatory = $true)] [hashtable] $Arguments,
        [Parameter(Mandatory = $true)] [System.Diagnostics.Process] $Wrapper,
        [Parameter(Mandatory = $true)] [System.Diagnostics.Process] $ExpectedServerProcess,
        [int] $TimeoutSeconds = 30,
        [int] $ResponseTimeoutMs = 10000
    )
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastFailure = 'no connection attempt completed'
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        if ($Wrapper.HasExited) {
            throw "$Command wrapper exited before the Payload pipe was ready (exit=$($Wrapper.ExitCode))"
        }
        try {
            return Send-PayloadFrame -PipeName $PipeName -Command $Command -Arguments $Arguments -ExpectedServerProcess $ExpectedServerProcess -ResponseTimeoutMs $ResponseTimeoutMs
        }
        catch {
            if ($_.Exception.Message -like '*owned pipe server verification failed*') { throw }
            if ($_.Exception.Message -like 'Payload rejected *') { throw }
            $lastFailure = $_.Exception.Message
            Start-Sleep -Milliseconds 250
        }
    }
    throw "timed out waiting for $Command on Payload pipe ${PipeName}: $lastFailure"
}

function Get-SafeFrameRecord {
    param(
        [Parameter(Mandatory = $true)] [string] $Label,
        [Parameter(Mandatory = $true)] [object] $Frame,
        [Parameter(Mandatory = $true)] [string] $RequestId
    )
    $data = $Frame.Data
    $match = Get-OptionalValue $data 'match' $null
    $record = [ordered]@{
        label = $Label
        command = [string] $Frame.Command
        request_id = $RequestId
        status = [string] (Get-OptionalValue $data 'status' '')
        code = [string] (Get-OptionalValue $data 'code' '')
        message = [string] (Get-OptionalValue $data 'message' '')
        payload_version = [string] (Get-OptionalValue $data 'payload_version' '')
        game_binary_sha256 = [string] (Get-OptionalValue $data 'game_binary_sha256' '')
        endpoint_host = [string] (Get-OptionalValue $data 'endpoint_host' '')
        endpoint_port = [int] (Get-OptionalValue $data 'endpoint_port' 0)
        world_instance_id = [string] (Get-OptionalValue $data 'world_instance_id' '')
        strict_online_ready = [bool] (Get-OptionalValue $data 'strict_online_ready' $false)
        native_authority_path_ready = [bool] (Get-OptionalValue $data 'native_authority_path_ready' $false)
        native_client_grant_injection_ready = [bool] (Get-OptionalValue $data 'native_client_grant_injection_ready' $false)
        native_authority_admission_verified = [bool] (Get-OptionalValue $data 'native_authority_admission_verified' $false)
        match_login_completed = if ($null -ne $match) { [bool] (Get-OptionalValue $match 'login_completed' $false) } else { $false }
        match_login_ready = if ($null -ne $match) { [bool] (Get-OptionalValue $match 'login_ready' $false) } else { $false }
        match_state = if ($null -ne $match) { [string] (Get-OptionalValue $match 'state' '') } else { '' }
        match_last_error = if ($null -ne $match) { [string] (Get-OptionalValue $match 'last_error' '') } else { '' }
        operation_sequence = [uint64] (Get-OptionalValue $data 'operation_sequence' 0)
        match_operation_sequence = if ($null -ne $match) { [uint64] (Get-OptionalValue $match 'operation_sequence' 0) } else { 0 }
        match_scope_verified = if ($null -ne $match) { [bool] (Get-OptionalValue $match 'scope_verified' $false) } else { $false }
        match_native_grant_staged = if ($null -ne $match) { [bool] (Get-OptionalValue $match 'native_grant_staged' $false) } else { $false }
        match_native_grant_injected = if ($null -ne $match) { [bool] (Get-OptionalValue $match 'native_grant_injected' $false) } else { $false }
        match_local_pawn_ready = if ($null -ne $match) { [bool] (Get-OptionalValue $match 'local_pawn_ready' $false) } else { $false }
        match_native_net_ready = if ($null -ne $match) { [bool] (Get-OptionalValue $match 'native_net_ready' $false) } else { $false }
        match_local_world_instance_id = if ($null -ne $match) { [string] (Get-OptionalValue $match 'local_world_instance_id' '') } else { '' }
        observed_utc = [DateTimeOffset]::UtcNow.ToString('o')
    }
    if ($null -ne $match) {
        foreach ($scopeName in @('scope', 'playable_scope')) {
            $scopeValue = Get-OptionalValue $match $scopeName $null
            if ($null -eq $scopeValue) { continue }
            $safeScope = [ordered]@{}
            foreach ($scopeKey in @('attempt_id', 'authority_session_id', 'world_instance_id', 'roster_revision', 'route_generation', 'connection_generation')) {
                $safeScope[$scopeKey] = Get-OptionalValue $scopeValue $scopeKey $null
            }
            foreach ($secretKey in @('player_id', 'grant_jti')) {
                $rawValue = [string] (Get-OptionalValue $scopeValue $secretKey '')
                $safeScope[$secretKey + '_sha256'] = if ($rawValue) { Get-TextSha256 $rawValue } else { '' }
            }
            $record['match_' + $scopeName] = $safeScope
        }
        $matchNonce = [string] (Get-OptionalValue $match 'native_connection_nonce' '')
        $record.match_native_connection_nonce_sha256 = if ($matchNonce) { Get-TextSha256 $matchNonce } else { '' }
    }
    $eventList = Get-OptionalValue $data 'events' $null
    if ($null -ne $eventList) {
        $safeEvents = @()
        foreach ($event in @($eventList)) {
            $safeEvents += [ordered]@{
                sequence = [int64] $event.sequence
                state = [string] $event.state
                attempt_id = [string] $event.attempt_id
                authority_session_id = [string] $event.authority_session_id
                world_instance_id = [string] $event.world_instance_id
                roster_revision = [int64] $event.roster_revision
                route_generation = [int] $event.route_generation
                player_id_sha256 = Get-TextSha256 ([string] $event.player_id)
                grant_jti_sha256 = Get-TextSha256 ([string] $event.grant_jti)
                native_connection_nonce_sha256 = Get-TextSha256 ([string] $event.native_connection_nonce)
            }
        }
        $record.events = $safeEvents
        $record.next_sequence = [int64] (Get-OptionalValue $data 'next_sequence' 0)
    }
    return $record
}

function Wait-ClientLoginReady {
    param(
        [Parameter(Mandatory = $true)] [string] $PipeName,
        [Parameter(Mandatory = $true)] [System.Diagnostics.Process] $Wrapper,
        [Parameter(Mandatory = $true)] [System.Diagnostics.Process] $ExpectedServerProcess,
        [Parameter(Mandatory = $true)] [string] $FramesPath,
        [int] $TimeoutSeconds = 60
    )
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        if ($Wrapper.HasExited) {
            throw "client wrapper exited before match.login_completed && login_ready (exit=$($Wrapper.ExitCode))"
        }
        try {
            $requestId = New-RequestId 'client-login-ready'
            $frame = Send-PayloadFrame -PipeName $PipeName -Command 'payload_status' -Arguments @{ request_id = $requestId } -ExpectedServerProcess $ExpectedServerProcess -ResponseTimeoutMs 5000
            (Get-SafeFrameRecord -Label 'client_login_ready_poll' -Frame $frame -RequestId $requestId) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $FramesPath -Encoding UTF8
            $match = Get-OptionalValue $frame.Data 'match' $null
            $matchState = if ($null -ne $match) { [string] (Get-OptionalValue $match 'state' '') } else { '' }
            if ($matchState -in @('failed', 'cancelled')) {
                throw "client login entered terminal state '$matchState' before grant delivery"
            }
            if ($null -ne $match -and
                [bool] (Get-OptionalValue $match 'login_completed' $false) -and
                [bool] (Get-OptionalValue $match 'login_ready' $false)) {
                return $frame
            }
        }
        catch {
            if ($_.Exception.Message -like '*owned pipe server verification failed*' -or
                $_.Exception.Message -like "client login entered terminal state '*'") { throw }
        }
        Start-Sleep -Milliseconds 250
    }
    throw 'timed out waiting for the real client pipe to report match.login_completed && login_ready'
}

function Start-ManagedLauncher {
    param(
        [Parameter(Mandatory = $true)] [string] $Label,
        [Parameter(Mandatory = $true)] [string] $Scope,
        [Parameter(Mandatory = $true)] [string[]] $GameArguments,
        [Parameter(Mandatory = $true)] [string] $StartGamePath,
        [Parameter(Mandatory = $true)] [string] $LogRoot
    )
    $rawOut = Join-Path $LogRoot "$Label.stdout.raw.log"
    $rawErr = Join-Path $LogRoot "$Label.stderr.raw.log"
    $safeOut = Join-Path $LogRoot "$Label.stdout.log"
    $safeErr = Join-Path $LogRoot "$Label.stderr.log"
    $specPath = Join-Path $LogRoot "$Label.launch-spec.json"
    $wrapperPath = Join-Path $LogRoot "$Label.launch-wrapper.ps1"
    Remove-Item -LiteralPath $rawOut,$rawErr,$safeOut,$safeErr -Force -ErrorAction SilentlyContinue
    $spec = [ordered]@{
        startgame = $StartGamePath
        auth_session_scope = $Scope
        game_arguments = @($GameArguments)
    }
    $spec | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $specPath -Encoding UTF8
    @'
param(
    [Parameter(Mandatory = $true)] [string] $SpecPath
)
$ErrorActionPreference = 'Stop'
$spec = Get-Content -LiteralPath $SpecPath -Raw -Encoding UTF8 | ConvertFrom-Json
$gameArguments = [string[]] @($spec.game_arguments)
$launchParameters = @{
    AuthSessionScope = [string] $spec.auth_session_scope
    GameArguments = $gameArguments
}
& ([string] $spec.startgame) @launchParameters
exit 0
'@ | Set-Content -LiteralPath $wrapperPath -Encoding UTF8
    # Do not pass game flags through the wrapper's -File command line.  The
    # wrapper loads a JSON string[] and splats it into startgame.ps1, which
    # preserves flags such as -StrictRosterAuthority as game arguments.
    $arguments = @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $wrapperPath,
        '-SpecPath', $specPath
    )
    $process = Start-Process -FilePath 'powershell.exe' -ArgumentList $arguments -WorkingDirectory (Split-Path -Parent $StartGamePath) -WindowStyle Hidden -RedirectStandardOutput $rawOut -RedirectStandardError $rawErr -PassThru
    return [pscustomobject]@{
        Label = $Label
        Scope = $Scope
        Process = $process
        RawStdout = $rawOut
        RawStderr = $rawErr
        Stdout = $safeOut
        Stderr = $safeErr
        LaunchSpec = $specPath
        LaunchWrapper = $wrapperPath
        Arguments = $GameArguments
        GameProcess = $null
        StartedUtc = [DateTimeOffset]::UtcNow.ToString('o')
    }
}

function Sanitize-LauncherLog {
    param([Parameter(Mandatory = $true)] [string] $RawPath, [Parameter(Mandatory = $true)] [string] $SafePath)
    $privateRawPath = if ($RawPath -match '\.raw\.log$') {
        $RawPath -replace '\.raw\.log$', '.private.raw.log'
    }
    else { "$RawPath.private" }
    if (Test-Path -LiteralPath $RawPath -PathType Leaf) {
        Copy-Item -LiteralPath $RawPath -Destination $privateRawPath -Force
    }
    $text = if (Test-Path -LiteralPath $RawPath -PathType Leaf) {
        Get-Content -LiteralPath $RawPath -Raw -Encoding UTF8
    }
    else { '' }
    # Preserve useful startup/error context while removing Steam IDs, ticket
    # values, bearer material and URLs carrying a token.
    $text = [regex]::Replace($text, '(?<!\d)7656119\d{10}(?!\d)', '<steam-id-redacted>')
    $text = [regex]::Replace($text, '(?i)SteamID:\s*\d{17}', 'SteamID:<redacted>')
    $text = [regex]::Replace($text, '(?i)Ticket:\s*[0-9a-f]{16,}', 'Ticket:<redacted>')
    $text = [regex]::Replace($text, '(?i)(Bearer\s+)[A-Za-z0-9._~-]+', '$1<redacted>')
    $text = [regex]::Replace($text, '(?i)(ReboundGrant=)[^&\s]+', '$1<redacted>')
    [IO.File]::WriteAllText($SafePath, $text, [Text.UTF8Encoding]::new($false))
    Remove-Item -LiteralPath $RawPath -Force -ErrorAction SilentlyContinue
}

function Stop-ProcessHandle {
    param([AllowNull()] [System.Diagnostics.Process] $Process)
    if ($null -eq $Process) { return }
    try {
        $Process.Refresh()
        if (-not $Process.HasExited) {
            $Process.Kill()
            $Process.WaitForExit(10000)
        }
    }
    catch { }
}


function Write-OwnedAuthorityExitReceipt {
    param(
        [Parameter(Mandatory = $true)] [object] $Launcher,
        [Parameter(Mandatory = $true)] [string] $OutputPath,
        [Parameter(Mandatory = $true)] [string] $AttemptId,
        [Parameter(Mandatory = $true)] [string] $AuthorityId,
        [Parameter(Mandatory = $true)] [string] $AuthoritySessionId,
        [Parameter(Mandatory = $true)] [string] $WorldInstanceId,
        [Parameter(Mandatory = $true)] [int] $RosterRevision,
        [Parameter(Mandatory = $true)] [int] $RouteGeneration
    )
    $process = $Launcher.GameProcess
    if ($null -eq $process) { throw 'BP047 exit proof requires the captured authority process handle' }
    $process.Refresh()
    if ($process.HasExited) { throw 'BP047 exit proof cannot bind an already-exited authority handle' }
    $processId = [uint32] $process.Id
    $ownedHandleProcessId = [uint32] $Launcher.GameProcess.OwnedHandleProcessId
    $ownedCreation = [DateTime] $Launcher.GameProcess.OwnedCreationTimeUtc
    $ownedPath = [string] $Launcher.GameProcess.OwnedExecutablePath
    if ($ownedHandleProcessId -ne $processId -or
        [string]::IsNullOrWhiteSpace($ownedPath) -or
        [IO.Path]::GetFullPath($ownedPath) -ne [IO.Path]::GetFullPath((Join-Path $GameWin64 'ProjectBoundarySteam-Win64-Shipping.exe'))) {
        throw 'BP047 authority handle binding is not exact'
    }
    $receipt = [ordered]@{
        evidence_kind = 'owned_process_exited'
        owned_process_id = $processId
        process_start_fingerprint = ('win-filetime:{0:x16}' -f [UInt64] $ownedCreation.ToFileTimeUtc())
        process_exited = $false
        handle_verified = $true
        handle_process_id = $ownedHandleProcessId
        pipe_server_process_id = $processId
        attempt_id = $AttemptId
        authority_id = $AuthorityId
        authority_session_id = $AuthoritySessionId
        world_instance_id = $WorldInstanceId
        roster_revision = $RosterRevision
        route_generation = $RouteGeneration
        observed_exit_utc = $null
    }
    Stop-ProcessHandle -Process $process
    $process.Refresh()
    if (-not $process.HasExited) { throw "owned authority process $processId did not exit within the bounded wait" }
    $receipt.process_exited = $true
    $receipt.observed_exit_utc = [DateTimeOffset]::UtcNow.ToString('o')
    Write-SafeJson -Path $OutputPath -Value $receipt
    return [pscustomobject] $receipt
}function Stop-OwnedLauncherChildren {
    param([Parameter(Mandatory = $true)] [object] $Launcher)
    $allowedPaths = @(
        [IO.Path]::GetFullPath((Join-Path $GameWin64 'meta-tunnel.exe')),
        [IO.Path]::GetFullPath((Join-Path $GameWin64 '..\..\..\Engine\Binaries\Win64\CrashReportClient.exe'))
    )
    foreach ($parent in @($Launcher.GameProcess, $Launcher.Process)) {
        if ($null -eq $parent) { continue }
        # These are retained handles captured by this run. A child must have
        # been created during this exact parent's lifetime, not after PID reuse.
        try { $parentStart = $parent.StartTime.ToUniversalTime() } catch { continue }
        $parentEnd = if ($parent.HasExited) {
            try { $parent.ExitTime.ToUniversalTime() } catch { $parentStart }
        }
        else { [DateTime]::UtcNow }
        foreach ($child in @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$($parent.Id)")) {
            if ($child.ExecutablePath -notin $allowedPaths) { continue }
            if ($null -eq $child.CreationDate) { continue }
            try { $created = ([Management.ManagementDateTimeConverter]::ToDateTime([string] $child.CreationDate)).ToUniversalTime() } catch { continue }
            if ($created -lt $parentStart -or $created -gt $parentEnd) { continue }
            try {
                $ownedChild = [Diagnostics.Process]::GetProcessById([int] $child.ProcessId)
                if ($ownedChild.HasExited) { continue }
                if ($ownedChild.MainModule.FileName -ne $child.ExecutablePath -or
                    [Math]::Abs(($ownedChild.StartTime.ToUniversalTime() - $created).TotalMilliseconds) -gt 2) {
                    throw 'owned launcher child identity changed'
                }
                $ownedChild.Kill()
                if (-not $ownedChild.WaitForExit(10000)) { throw 'owned launcher child did not exit' }
                [pscustomobject]@{pid=[int]$child.ProcessId; parent_pid=$parent.Id; executable=$child.ExecutablePath; creation_utc=$created.ToString('o'); exited=$true} |
                    ConvertTo-Json -Compress | Add-Content -LiteralPath (Join-Path $runRoot 'owned-child-cleanup.jsonl') -Encoding UTF8
            }
            catch [ArgumentException] { }
        }
    }
}

function Read-BackendMarker {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [string] $AttemptId,
        [Parameter(Mandatory = $true)] [string] $AuthoritySessionId,
        [Parameter(Mandatory = $true)] [string] $WorldInstanceId,
        [Parameter(Mandatory = $true)] [int] $EndpointPort,
        [Parameter(Mandatory = $true)] [string] $NativeConnectionNonce
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    $marker = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([bool] $marker.accepted -ne $true -or
        [string] $marker.attempt_id -ne $AttemptId -or
        [string] $marker.authority_session_id -ne $AuthoritySessionId -or
        [string] $marker.world_instance_id -ne $WorldInstanceId -or
        [int] $marker.endpoint_port -ne $EndpointPort -or
        [string] $marker.native_connection_nonce -ne $NativeConnectionNonce -or
        [string]::IsNullOrWhiteSpace([string] $marker.native_connection_nonce) -or
        ([string] $marker.native_connection_nonce).Length -lt 16) {
        throw 'backend evidence scope does not match the live Payload authority'
    }
    return $marker
}

function Wait-BackendAuthorityMarker {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [string] $AttemptId,
        [Parameter(Mandatory = $true)] [string] $AuthoritySessionId,
        [Parameter(Mandatory = $true)] [string] $WorldInstanceId,
        [Parameter(Mandatory = $true)] [int] $EndpointPort,
        [Parameter(Mandatory = $true)] [string] $NativeConnectionNonce,
        [int] $TimeoutSeconds = 60
    )
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        if (Test-Path -LiteralPath $Path -PathType Leaf) {
            try {
                $marker = Read-BackendMarker -Path $Path -AttemptId $AttemptId -AuthoritySessionId $AuthoritySessionId -WorldInstanceId $WorldInstanceId -EndpointPort $EndpointPort -NativeConnectionNonce $NativeConnectionNonce
                if ($null -ne $marker) { return $marker }
            }
            catch {
                if ($_.Exception.Message -like '*scope does not match*') { throw }
                # An atomic writer may be between replacement and completion.
            }
        }
        Start-Sleep -Milliseconds 250
    }
    throw 'timed out waiting for the scoped backend authority-ready marker'
}

function Read-BackendGrantMarker {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [string] $AttemptId,
        [Parameter(Mandatory = $true)] [string] $AuthoritySessionId,
        [Parameter(Mandatory = $true)] [string] $WorldInstanceId,
        [Parameter(Mandatory = $true)] [int] $RosterRevision,
        [Parameter(Mandatory = $true)] [int] $RouteGeneration
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    $marker = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([bool] $marker.accepted -ne $true -or
        [string] $marker.attempt_id -ne $AttemptId -or
        [string] $marker.authority_session_id -ne $AuthoritySessionId -or
        [string] $marker.world_instance_id -ne $WorldInstanceId -or
        [int] $marker.roster_revision -ne $RosterRevision -or
        [int] $marker.route_generation -ne $RouteGeneration) {
        throw 'backend grant evidence scope does not match the live Payload authority'
    }
    $grant = Require-String $marker 'join_grant' 49152
    $jti = Require-String $marker 'grant_jti' 128
    [void] (Require-String $marker 'player_id' 256)
    if ((Require-Int $marker 'connection_generation') -lt 1) { throw 'backend grant connection generation must be positive' }
    return [pscustomobject]@{ Marker = $marker; Grant = $grant; GrantJti = $jti }
}

function Wait-BackendGrantMarker {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [string] $AttemptId,
        [Parameter(Mandatory = $true)] [string] $AuthoritySessionId,
        [Parameter(Mandatory = $true)] [string] $WorldInstanceId,
        [Parameter(Mandatory = $true)] [int] $RosterRevision,
        [Parameter(Mandatory = $true)] [int] $RouteGeneration,
        [int] $TimeoutSeconds = 60
    )
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        if (Test-Path -LiteralPath $Path -PathType Leaf) {
            try {
                $grant = Read-BackendGrantMarker -Path $Path -AttemptId $AttemptId -AuthoritySessionId $AuthoritySessionId -WorldInstanceId $WorldInstanceId -RosterRevision $RosterRevision -RouteGeneration $RouteGeneration
                if ($null -ne $grant) { return $grant }
            }
            catch {
                if ($_.Exception.Message -like '*scope does not match*' -or $_.Exception.Message -like "*field 'join_grant'*") { throw }
                # Retry a partially written private JSON marker.
            }
        }
        Start-Sleep -Milliseconds 250
    }
    throw 'timed out waiting for the scoped backend grant marker'
}

function Get-PrivateScopedEvents {
    param(
        [Parameter(Mandatory = $true)] [string] $Path
    )
    if (Test-Path -LiteralPath $Path -PathType Leaf) {
        $readDeadline = [DateTimeOffset]::UtcNow.AddSeconds(1)
        while ($true) {
        try {
            $existing = [IO.File]::ReadAllText($Path) | ConvertFrom-Json
            if ($null -eq $existing.events -or $null -eq $existing.next_sequence) {
                throw 'existing native event envelope is incomplete; refusing to reset its cursor'
            }
            return @($existing.events)
        }
        catch [IO.IOException] {
            $nativeError = $_.Exception.HResult -band 0xffff
            if ($nativeError -notin @(32, 33) -or [DateTimeOffset]::UtcNow -ge $readDeadline) { throw }
            Start-Sleep -Milliseconds 20
        }
        }
    }
    return @()
}

function Write-PrivateScopedEnvelope {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [AllowEmptyCollection()] [object[]] $Events,
        [Parameter(Mandatory = $true)] [int64] $NextSequence
    )
    $payload = [ordered]@{
        events = @($Events)
        next_sequence = [int64] $NextSequence
    }
    # Replace a same-directory temporary file so readers see either the old
    # complete envelope or the new complete envelope, never a partial JSON
    # document.  The unique name also prevents stale temp files from racing a
    # later poll.
    $temporary = "$Path.tmp.$PID.$([Guid]::NewGuid().ToString('N'))"
    try {
        [IO.File]::WriteAllText($temporary, ($payload | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
        $replaceDeadline = [DateTimeOffset]::UtcNow.AddSeconds(1)
        while ($true) {
            try {
                [IO.File]::Move($temporary, $Path, $true)
                break
            }
            catch [IO.IOException] {
                $nativeError = $_.Exception.HResult -band 0xffff
                if ($nativeError -notin @(32, 33) -or [DateTimeOffset]::UtcNow -ge $replaceDeadline) { throw }
                Start-Sleep -Milliseconds 20
            }
        }
    }
    finally {
        if (Test-Path -LiteralPath $temporary -PathType Leaf) {
            Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue
        }
    }
}

function Write-PrivateScopedEvent {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [object] $Event
    )
    $record = [ordered]@{
        observed_utc = [DateTimeOffset]::UtcNow.ToString('o')
        sequence = [int64] $Event.sequence
        state = [string] $Event.state
        attempt_id = [string] $Event.attempt_id
        authority_session_id = [string] $Event.authority_session_id
        world_instance_id = [string] $Event.world_instance_id
        roster_revision = [int64] $Event.roster_revision
        route_generation = [int] $Event.route_generation
        player_id = [string] $Event.player_id
        grant_jti = [string] $Event.grant_jti
        native_connection_nonce = [string] $Event.native_connection_nonce
        connection_generation = [int] $Event.connection_generation
    }
    $events = @(Get-PrivateScopedEvents -Path $Path)
    $events += [pscustomobject] $record
    Write-PrivateScopedEnvelope -Path $Path -Events $events -NextSequence ([int64] $Event.sequence)
}

function Write-PrivateScopedCursor {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [int64] $NextSequence
    )
    # Empty event polls still publish the current cursor.  This gives the Go
    # driver a complete, atomic envelope without inventing a native event.
    $events = @(Get-PrivateScopedEvents -Path $Path)
    Write-PrivateScopedEnvelope -Path $Path -Events $events -NextSequence $NextSequence
}

function Read-BackendReceipt {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [string] $ExpectedKind,
        [Parameter(Mandatory = $true)] [object] $Event
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    try {
        $receipt = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
    }
    catch {
        return $null
    }
    if ([bool] $receipt.accepted -ne $true -or
        [int64] $receipt.sequence -ne [int64] $Event.sequence -or
        [string] $receipt.kind -ne $ExpectedKind -or
        [string] $receipt.attempt_id -ne [string] $Event.attempt_id -or
        [string] $receipt.authority_session_id -ne [string] $Event.authority_session_id -or
        [string] $receipt.world_instance_id -ne [string] $Event.world_instance_id -or
        [int64] $receipt.roster_revision -ne [int64] $Event.roster_revision -or
        [int] $receipt.route_generation -ne [int] $Event.route_generation -or
        [string] $receipt.player_id -ne [string] $Event.player_id -or
        [string] $receipt.grant_jti -ne [string] $Event.grant_jti -or
        [string] $receipt.native_connection_nonce -ne [string] $Event.native_connection_nonce -or
        [int] $receipt.connection_generation -ne [int] $Event.connection_generation) {
        throw "backend receipt '$Path' is outside the exact native event scope"
    }
    return $receipt
}

function Wait-BackendReceipt {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $true)] [string] $ExpectedKind,
        [Parameter(Mandatory = $true)] [object] $Event,
        [int] $TimeoutSeconds = 60
    )
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        $receipt = Read-BackendReceipt -Path $Path -ExpectedKind $ExpectedKind -Event $Event
        if ($null -ne $receipt) { return $receipt }
        Start-Sleep -Milliseconds 250
    }
    throw "timed out waiting for backend receipt '$Path' after $ExpectedKind"
}

function Get-ScopedReceiptArguments {
    param(
        [Parameter(Mandatory = $true)] [object] $Event,
        [Parameter(Mandatory = $true)] [string] $RequestId
    )
    return @{
        request_id = $RequestId
        attempt_id = Require-String $Event 'attempt_id' 128
        authority_session_id = Require-String $Event 'authority_session_id' 128
        world_instance_id = Require-String $Event 'world_instance_id' 128
        roster_revision = Require-Int $Event 'roster_revision'
        route_generation = Require-Int $Event 'route_generation'
        player_id = Require-String $Event 'player_id' 256
        grant_jti = Require-String $Event 'grant_jti' 128
        native_connection_nonce = Require-String $Event 'native_connection_nonce' 256
        connection_generation = Require-Int $Event 'connection_generation'
    }
}

function Send-ScopedReceipt {
    param(
        [Parameter(Mandatory = $true)] [string] $PipeName,
        [Parameter(Mandatory = $true)] [string] $Command,
        [Parameter(Mandatory = $true)] [object] $Event,
        [Parameter(Mandatory = $true)] [System.Diagnostics.Process] $ExpectedServerProcess,
        [Parameter(Mandatory = $true)] [string] $FramesPath
    )
    $requestId = New-RequestId $Command
    $arguments = Get-ScopedReceiptArguments -Event $Event -RequestId $requestId
    $frame = Send-PayloadFrame -PipeName $PipeName -Command $Command -Arguments $arguments -ExpectedServerProcess $ExpectedServerProcess -ResponseTimeoutMs 10000
    $expectedCommand = switch ($Command) {
        'confirm_match_admission' { 'confirm_match_admission_ack'; break }
        'confirm_match_connection' { 'confirm_match_connection_ack'; break }
        'release_match_admission' { 'release_match_admission_ack'; break }
        default { throw "unsupported scoped receipt command '$Command'" }
    }
    $ack = $frame.Data
    if ($frame.Command -ne $expectedCommand -or
        [bool] $ack.accepted -ne $true -or
        [string] (Get-OptionalValue $ack 'code' '') -ne 'accepted' -or
        [string] (Get-OptionalValue $ack 'status' '') -ne 'queued' -or
        [string] (Get-OptionalValue $ack 'request_id' '') -ne $requestId) {
        throw "$Command was rejected (code=$($frame.Data.code))"
    }
    foreach ($scopeKey in @(
        'attempt_id', 'authority_session_id', 'world_instance_id',
        'roster_revision', 'route_generation', 'player_id', 'grant_jti',
        'native_connection_nonce', 'connection_generation')) {
        $expectedScopeValue = $arguments[$scopeKey]
        $actualScopeValue = Get-OptionalValue $ack $scopeKey $null
        if ([string] $actualScopeValue -ne [string] $expectedScopeValue) {
            throw "$Command ACK did not echo the exact scoped field '$scopeKey'"
        }
    }
    $safe = Get-SafeFrameRecord -Label $Command -Frame $frame -RequestId $requestId
    $safe.player_id_sha256 = Get-TextSha256 ([string] $Event.player_id)
    $safe.grant_jti_sha256 = Get-TextSha256 ([string] $Event.grant_jti)
    $safe.native_connection_nonce_sha256 = Get-TextSha256 ([string] $Event.native_connection_nonce)
    $safe.connection_generation = [int] $Event.connection_generation
    $safe | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $FramesPath -Encoding UTF8
    return $frame
}

function Write-SafeJson {
    param([Parameter(Mandatory = $true)] [string] $Path, [Parameter(Mandatory = $true)] [object] $Value)
    $parent = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($parent)) {
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
    }
    $temporary = "$Path.tmp"
    [IO.File]::WriteAllText($temporary, ($Value | ConvertTo-Json -Depth 12), [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $temporary -Destination $Path -Force
}

$started = @()
$authorityLauncher = $null
$clientLauncher = $null
$installedPayload = Join-Path $GameWin64 'Payload.dll'
$startGame = Join-Path $GameWin64 'startgame.ps1'
$runId = "dedicated-component-$([DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss'))-$([guid]::NewGuid().ToString('N').Substring(0,8))"
$runRoot = Join-Path $EvidenceRoot $runId
$authorityPipe = "ProjectRebound_DedicatedAuthority_$([guid]::NewGuid().ToString('N'))"
$clientPipe = "ProjectRebound_DedicatedClient_$([guid]::NewGuid().ToString('N'))"
$restorePath = Join-Path $runRoot 'Payload.dll.before-run'
$summaryPath = Join-Path $runRoot 'summary.json'
$ownedExitReceiptPath = Join-Path $runRoot 'owned-exit-receipt.json'
$framesPath = Join-Path $runRoot 'payload-frames.jsonl'
$fixtureHash = $null
$fixture = $null
$authorityAck = $null
$authorityWorld = $null
$authorityEndpointPort = $null
$joinGrant = $null
$joinGrantJti = $null
$grantEvidenceHash = $null
$clientReadyMarkerHash = $null
$connected = $false
$playable = $false
$released = $false
$teardownRequested = $false
$playableTimeout = $false
$playableDeadline = $null
$closureDeadline = $null
$connectedEvent = $null
$disconnectedEvent = $null
$admissionEvidence = $null
$admissionEvidenceHash = $null
$outcome = 'BLOCKED'
$failure = $null
$authorityExitRecorded = $false

New-Item -ItemType Directory -Path $runRoot -Force | Out-Null

try {
    if ($AuthorityPort -lt 1024 -or $AuthorityPort -gt 65535 -or $ClientPort -lt 1024 -or $ClientPort -gt 65535 -or $AuthorityPort -eq $ClientPort) {
        throw 'authority and client ports must be distinct non-privileged ports'
    }
    if (-not (Test-Path -LiteralPath $GameWin64 -PathType Container) -or
        -not (Test-Path -LiteralPath $installedPayload -PathType Leaf) -or
        -not (Test-Path -LiteralPath $startGame -PathType Leaf) -or
        -not (Test-Path -LiteralPath (Join-Path $GameWin64 'ProjectBoundarySteam-Win64-Shipping.exe') -PathType Leaf)) {
        throw 'locked Boundary Win64 runtime or managed startgame.ps1 is missing'
    }
    if (-not (Test-Path -LiteralPath $CandidatePayload -PathType Leaf)) { throw 'candidate Payload.dll is missing' }
    if (-not (Test-Path -LiteralPath $OriginalPayloadBackup -PathType Leaf)) { throw 'verified original Payload backup is missing' }
    if ((Get-Sha256 $CandidatePayload) -ne $ExpectedCandidatePayloadSha256) { throw 'candidate Payload hash is not the frozen game-thread queue candidate' }
    if ((Get-Sha256 $OriginalPayloadBackup) -ne $ExpectedOriginalPayloadSha256) { throw 'original Payload backup hash is not 6C7B' }
    if ((Get-Sha256 $installedPayload) -ne $ExpectedOriginalPayloadSha256) { throw 'installed Payload is not the expected pre-run 6C7B build' }
    if ((Get-Sha256 (Join-Path $GameWin64 'ProjectBoundarySteam-Win64-Shipping.exe')) -ne $ExpectedExecutableSha256) { throw 'installed game executable hash is not locked' }

    $fixtureHash = Get-Sha256 $FixturePath
    $fixture = Get-Content -LiteralPath $FixturePath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([string] $fixture.fixture_kind -ne 'backend_strict_roster_v2_component') { throw 'fixture kind is not the backend strict v2 component fixture' }
    if ([string] $fixture.hosting_kind -ne 'DEDICATED') { throw 'fixture hosting kind is not DEDICATED' }
    if ([string] $fixture.online_acceptance_scope -ne 'component_only_not_three_player_e2e') { throw 'fixture is not explicitly component-only' }
    if ([bool] $fixture.native_callback_observed) { throw 'fixture incorrectly claims a native callback before this run' }
    $attemptId = Require-String $fixture 'attempt_id' 128
    $authoritySessionId = Require-String $fixture 'authority_session_id' 128
    $authorityId = Require-String $fixture 'authority_id' 128
    $allocation = Require-String $fixture 'allocation' 49152
    $allocationKeyId = Require-String $fixture 'allocation_key_id' 128
    $allocationPublicKey = Require-String $fixture 'allocation_public_key_base64' 256
    $fixtureRosterRevision = Require-Int $fixture 'roster_revision'
    $fixtureRouteGeneration = Require-Int $fixture 'route_generation'
    $transportHost = if ($fixture.PSObject.Properties['transport_endpoint']) { [string] $fixture.transport_endpoint } else { '' }
    if ($transportHost -and $transportHost -notmatch '^127\.0\.0\.1:\d{1,5}$') { throw 'fixture transport endpoint is not loopback' }
    if ($transportHost -and [int] $transportHost.Split(':')[-1] -ne $AuthorityPort) { throw 'fixture transport endpoint does not match AuthorityPort' }
    if (-not [string]::IsNullOrWhiteSpace($BackendScopedEventPath)) {
        $eventParent = Split-Path -Parent $BackendScopedEventPath
        if (-not [string]::IsNullOrWhiteSpace($eventParent)) {
            New-Item -ItemType Directory -Path $eventParent -Force | Out-Null
        }
        Remove-Item -LiteralPath $BackendScopedEventPath -Force -ErrorAction SilentlyContinue
    }
    if (-not [string]::IsNullOrWhiteSpace($BackendReceiptDir)) {
        New-Item -ItemType Directory -Path $BackendReceiptDir -Force | Out-Null
        foreach ($receiptName in @('01-reserve.json', '02-confirm-connection.json', '03-connected-observed.json', '04-release.json')) {
            Remove-Item -LiteralPath (Join-Path $BackendReceiptDir $receiptName) -Force -ErrorAction SilentlyContinue
        }
    }
    if ([string]::IsNullOrWhiteSpace($ClientReadyMarkerPath)) {
        $ClientReadyMarkerPath = Join-Path $EvidenceRoot 'backend-client-ready.json'
    }
    if ([string]::IsNullOrWhiteSpace($BackendAuthorityReadyEvidencePath) -or
        [string]::IsNullOrWhiteSpace($BackendGrantEvidencePath) -or
        [string]::IsNullOrWhiteSpace($BackendReceiptDir)) {
        throw 'backend authority-ready, grant, and receipt paths are required for the gated component run'
    }
    $markerPaths = @(@(
        $BackendAuthorityReadyEvidencePath,
        $BackendGrantEvidencePath,
        $ClientReadyMarkerPath,
        $BackendAdmissionEvidencePath
    ) | Where-Object { -not [string]::IsNullOrWhiteSpace([string] $_) })
    if (($markerPaths | Sort-Object -Unique).Count -ne $markerPaths.Count) {
        throw 'backend marker paths must be distinct'
    }
    foreach ($markerPath in $markerPaths) {
        $markerParent = Split-Path -Parent ([string] $markerPath)
        if (-not [string]::IsNullOrWhiteSpace($markerParent)) {
            New-Item -ItemType Directory -Path $markerParent -Force | Out-Null
        }
        Remove-Item -LiteralPath ([string] $markerPath) -Force -ErrorAction SilentlyContinue
    }

    Write-SafeJson -Path (Join-Path $runRoot 'preflight.json') -Value ([ordered]@{
        run_id = $runId
        execute = [bool] $Execute
        fixture_path = $FixturePath
        fixture_sha256 = $fixtureHash
        authority_id = $authorityId
        attempt_id = $attemptId
        authority_session_id = $authoritySessionId
        roster_revision = $fixtureRosterRevision
        route_generation = $fixtureRouteGeneration
        authority_port = $AuthorityPort
        client_port = $ClientPort
        client_ready_marker_path = $ClientReadyMarkerPath
        expected_executable_sha256 = $ExpectedExecutableSha256
        installed_payload_sha256 = Get-Sha256 $installedPayload
        original_backup_sha256 = Get-Sha256 $OriginalPayloadBackup
        candidate_payload_sha256 = Get-Sha256 $CandidatePayload
        observed_utc = [DateTimeOffset]::UtcNow.ToString('o')
    })
    if (-not $Execute) {
        $outcome = 'NOT_RUN'
        return
    }

    Assert-NoBoundaryProcess
    Copy-Item -LiteralPath $installedPayload -Destination $restorePath -Force
    if ((Get-Sha256 $restorePath) -ne $ExpectedOriginalPayloadSha256) { throw 'run restore copy did not preserve the original hash' }
    Copy-Item -LiteralPath $CandidatePayload -Destination $installedPayload -Force
    if ((Get-Sha256 $installedPayload) -ne $ExpectedCandidatePayloadSha256) { throw 'candidate deployment hash verification failed' }

    $authorityArgs = @('-server','-StrictRosterAuthority','-log','-nullrhi','-nosplash','-NoWindow',
        '-map=Warehouse','-mode=/Game/Online/GameMode/PBGameMode_Rush_BP.PBGameMode_Rush_BP_C',
        "-port=$AuthorityPort", "-external=$AuthorityPort", '-servername=StrictDedicatedComponent',
        '-serverregion=local', "-pipe=$authorityPipe", '-debuglog')
    # Member clients do not bootstrap a listening authority. Their mandatory
    # signed Grant is delivered by the guarded join command after login.
    $clientArgs = @("-port=$ClientPort", "-external=$ClientPort",
        "-pipe=$clientPipe", '-debuglog', '-clientdebuglog')
    $authorityLauncher = Start-ManagedLauncher -Label 'authority' -Scope $AuthorityAuthSessionScope -GameArguments $authorityArgs -StartGamePath $startGame -LogRoot $runRoot
    $started += $authorityLauncher
    $authorityLauncher.GameProcess = Get-OwnedGameProcess -PipeName $authorityPipe -ExpectedExecutablePath (Join-Path $GameWin64 'ProjectBoundarySteam-Win64-Shipping.exe') -TimeoutSeconds 20
    $authorityStatusRequest = New-RequestId 'authority-status'
    $authorityStatus = Wait-PayloadResponse -PipeName $authorityPipe -Command 'payload_status' -Arguments @{ request_id = $authorityStatusRequest } -Wrapper $authorityLauncher.Process -ExpectedServerProcess $authorityLauncher.GameProcess -TimeoutSeconds 45
    (Get-SafeFrameRecord -Label 'authority_payload_status' -Frame $authorityStatus -RequestId $authorityStatusRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
    if ([string] $authorityStatus.Data.game_binary_sha256 -ne $ExpectedExecutableSha256.ToLowerInvariant()) { throw 'authority Payload reports a mismatched game hash' }
    if ([bool] $authorityStatus.Data.native_authority_path_ready -ne $true) { throw 'authority native hook path is not ready' }
    # A Dedicated authority owns the server-side gate; the client-side NMT
    # serializer capability is checked only after the real client starts.

    $allocationRequest = New-RequestId 'authority-allocation'
    $allocationFrame = Wait-PayloadResponse -PipeName $authorityPipe -Command 'install_match_allocation' -Arguments @{
        request_id = $allocationRequest
        allocation = $allocation
        admission_key_id = $allocationKeyId
        admission_public_key_base64 = $allocationPublicKey
    } -Wrapper $authorityLauncher.Process -ExpectedServerProcess $authorityLauncher.GameProcess -TimeoutSeconds 20
    (Get-SafeFrameRecord -Label 'authority_allocation' -Frame $allocationFrame -RequestId $allocationRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
    if ($allocationFrame.Command -ne 'install_match_allocation_ack' -or [string] $allocationFrame.Data.status -ne 'accepted') { throw 'Payload did not accept the signed allocation' }
    if ([string] $allocationFrame.Data.payload_version -ne $ExpectedPayloadProtocol) { throw 'Payload allocation ACK protocol version mismatch' }

    $authorityRequest = New-RequestId 'authority-start'
    $authorityAck = Wait-PayloadResponse -PipeName $authorityPipe -Command 'start_match_authority' -Arguments @{ request_id = $authorityRequest; transport_target = "127.0.0.1:$AuthorityPort" } -Wrapper $authorityLauncher.Process -ExpectedServerProcess $authorityLauncher.GameProcess -TimeoutSeconds 45 -ResponseTimeoutMs 35000
    (Get-SafeFrameRecord -Label 'authority_ready' -Frame $authorityAck -RequestId $authorityRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
    if ($authorityAck.Command -ne 'start_match_authority_ack' -or [string] $authorityAck.Data.status -ne 'ready') { throw 'Payload did not make Dedicated authority ready' }
    $authorityWorld = Require-String $authorityAck.Data 'world_instance_id' 128
    $authorityEndpointPort = [int] $authorityAck.Data.endpoint_port
    if ($authorityEndpointPort -ne $AuthorityPort) { throw 'Payload authority endpoint port differs from configured port' }
    $authorityNativeConnectionNonce = Require-String $authorityAck.Data 'native_connection_nonce' 256
    if ($authorityNativeConnectionNonce.Length -lt 16) { throw 'Payload authority nonce is too short' }

    $authorityRequestRecord = [ordered]@{
        # The wire ACK publishes status=ready only after the native callback
        # accepted. It does not carry a separate accepted boolean.
        accepted = ([string] $authorityAck.Data.status -eq 'ready')
        accepted_evidence = 'validated start_match_authority_ack status=ready and exact world/nonce/endpoint'
        attempt_id = $attemptId
        authority_id = $authorityId
        authority_session_id = $authoritySessionId
        world_instance_id = $authorityWorld
        native_connection_nonce = $authorityNativeConnectionNonce
        endpoint_host = [string] $authorityAck.Data.endpoint_host
        endpoint_port = $authorityEndpointPort
        roster_revision = $fixtureRosterRevision
        route_generation = $fixtureRouteGeneration
        allocation_file_sha256 = $fixtureHash
        backend_authority_ready_evidence_path = $BackendAuthorityReadyEvidencePath
        observed_utc = [DateTimeOffset]::UtcNow.ToString('o')
    }
    Write-SafeJson -Path (Join-Path $runRoot 'authority-ready-request.json') -Value $authorityRequestRecord

    if ($Bp047AuthorityOnly) {
        Write-SafeJson -Path (Join-Path $runRoot 'authority-only-ready.json') -Value ([ordered]@{
            kind = 'bp047_authority_only_ready'
            accepted = $true
            authority_ready = $true
            allocation_installed = $true
            world_listening = $true
            attempt_id = $attemptId
            authority_id = $authorityId
            authority_session_id = $authoritySessionId
            world_instance_id = $authorityWorld
            roster_revision = $fixtureRosterRevision
            route_generation = $fixtureRouteGeneration
            endpoint_port = $authorityEndpointPort
            native_connection_nonce_present = (-not [string]::IsNullOrWhiteSpace($authorityNativeConnectionNonce))
            observed_utc = [DateTimeOffset]::UtcNow.ToString('o')
        })
        [void] (Write-OwnedAuthorityExitReceipt -Launcher $authorityLauncher -OutputPath $ownedExitReceiptPath -AttemptId $attemptId -AuthorityId $authorityId -AuthoritySessionId $authoritySessionId -WorldInstanceId $authorityWorld -RosterRevision $fixtureRosterRevision -RouteGeneration $fixtureRouteGeneration)
        $authorityExitRecorded = $true
        $outcome = 'PASS_AUTHORITY_EXIT_PROOF_ONLY'
        return
    }
    if ([string]::IsNullOrWhiteSpace($BackendAuthorityReadyEvidencePath)) { throw 'backend authority-ready evidence path is required before client launch' }
    $backendReady = Wait-BackendAuthorityMarker -Path $BackendAuthorityReadyEvidencePath -AttemptId $attemptId -AuthoritySessionId $authoritySessionId -WorldInstanceId $authorityWorld -EndpointPort $authorityEndpointPort -NativeConnectionNonce $authorityNativeConnectionNonce -TimeoutSeconds ([Math]::Min(60, $TimeoutSeconds))
    if ($null -eq $backendReady) { throw 'backend authority-ready evidence was not produced' }

    # The client is cold-started only after the actual DedicatedReady scope is
    # persisted. The frozen Go driver waits for the marker below before minting
    # the short-lived member grant, so no grant can be staged for a cold client.
    $clientLauncher = Start-ManagedLauncher -Label 'client' -Scope $ClientAuthSessionScope -GameArguments $clientArgs -StartGamePath $startGame -LogRoot $runRoot
    $started += $clientLauncher
    $clientLauncher.GameProcess = Get-OwnedGameProcess -PipeName $clientPipe -ExpectedExecutablePath (Join-Path $GameWin64 'ProjectBoundarySteam-Win64-Shipping.exe') -TimeoutSeconds 20
    $clientStatusRequest = New-RequestId 'client-status'
    $clientStatus = Wait-PayloadResponse -PipeName $clientPipe -Command 'payload_status' -Arguments @{ request_id = $clientStatusRequest } -Wrapper $clientLauncher.Process -ExpectedServerProcess $clientLauncher.GameProcess -TimeoutSeconds 45
    (Get-SafeFrameRecord -Label 'client_payload_status' -Frame $clientStatus -RequestId $clientStatusRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
    if ([string] $clientStatus.Data.game_binary_sha256 -ne $ExpectedExecutableSha256.ToLowerInvariant()) { throw 'client Payload reports a mismatched game hash' }
    if ([bool] $clientStatus.Data.native_client_grant_injection_ready -ne $true) { throw 'client grant injection capability is not ready' }

    $clientReadyFrame = Wait-ClientLoginReady -PipeName $clientPipe -Wrapper $clientLauncher.Process -ExpectedServerProcess $clientLauncher.GameProcess -FramesPath $framesPath -TimeoutSeconds ([Math]::Min(60, $TimeoutSeconds))
    $authorityHeartbeatRequest = New-RequestId 'authority-heartbeat'
    $authorityHeartbeat = Send-PayloadFrame -PipeName $authorityPipe -Command 'start_match_authority' -Arguments @{ request_id = $authorityHeartbeatRequest; transport_target = "127.0.0.1:$AuthorityPort" } -ExpectedServerProcess $authorityLauncher.GameProcess -ResponseTimeoutMs 10000
    if ($authorityHeartbeat.Command -ne 'start_match_authority_ack' -or
        [string] $authorityHeartbeat.Data.status -ne 'ready' -or
        [string] $authorityHeartbeat.Data.world_instance_id -ne $authorityWorld -or
        [string] $authorityHeartbeat.Data.native_connection_nonce -ne $authorityNativeConnectionNonce -or
        [int] $authorityHeartbeat.Data.endpoint_port -ne $AuthorityPort) {
        throw 'authority readiness replay did not match the owned native scope after client login'
    }
    (Get-SafeFrameRecord -Label 'authority_heartbeat_observed' -Frame $authorityHeartbeat -RequestId $authorityHeartbeatRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
    $clientReadyMarker = [ordered]@{
        accepted = $true
        kind = 'client_ready_for_grant'
        client_pipe_ready = $true
        client_login_ready = $true
        authority_pipe_ready = $true
        authority_native_nonce = $authorityNativeConnectionNonce
        attempt_id = $attemptId
        authority_session_id = $authoritySessionId
        world_instance_id = $authorityWorld
        roster_revision = $fixtureRosterRevision
        route_generation = $fixtureRouteGeneration
        observed_utc = [DateTimeOffset]::UtcNow.ToString('o')
    }
    Write-SafeJson -Path $ClientReadyMarkerPath -Value $clientReadyMarker
    $clientReadyMarkerHash = Get-Sha256 $ClientReadyMarkerPath

    # The grant is deliberately fetched only after the live authority marker
    # and the exact real-pipe/login-ready marker. Optional fixture grant/world
    # fields are never a fallback.
    $backendGrant = Wait-BackendGrantMarker -Path $BackendGrantEvidencePath -AttemptId $attemptId -AuthoritySessionId $authoritySessionId -WorldInstanceId $authorityWorld -RosterRevision $fixtureRosterRevision -RouteGeneration $fixtureRouteGeneration -TimeoutSeconds ([Math]::Min(60, $TimeoutSeconds))
    $joinGrant = $backendGrant.Grant
    $joinGrantJti = $backendGrant.GrantJti
    $grantEvidenceHash = Get-Sha256 $BackendGrantEvidencePath
    $expectedClientScope = @{}
    foreach ($scopeKey in @('attempt_id', 'authority_session_id', 'world_instance_id',
        'roster_revision', 'route_generation', 'player_id', 'grant_jti', 'connection_generation')) {
        $expectedClientScope[$scopeKey] = $backendGrant.Marker.$scopeKey
    }

    # Mirror the production authority admission pump: the same Backend Grant
    # must reach the owned authority pipe before the client presents it in
    # NMT_Login. A client-only staged Grant is never authority authorization.
    $stageRequest = New-RequestId 'authority-stage-grant'
    $stageFrame = Wait-PayloadResponse -PipeName $authorityPipe -Command 'install_match_join_grant' -Arguments @{
        request_id = $stageRequest
        join_grant = $joinGrant
    } -Wrapper $authorityLauncher.Process -ExpectedServerProcess $authorityLauncher.GameProcess -TimeoutSeconds 20
    (Get-SafeFrameRecord -Label 'authority_stage_grant' -Frame $stageFrame -RequestId $stageRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
    if ($stageFrame.Command -ne 'install_match_join_grant_ack' -or [string] $stageFrame.Data.status -ne 'staged') {
        throw "authority rejected the exact Backend Grant (code=$($stageFrame.Data.code))"
    }

    $joinRequest = New-RequestId 'client-join'
    $joinFrame = Wait-PayloadResponse -PipeName $clientPipe -Command 'join' -Arguments @{
        request_id = $joinRequest
        ip = "127.0.0.1:$authorityEndpointPort"
        token = $joinGrant
        expected_scope = $expectedClientScope
    } -Wrapper $clientLauncher.Process -ExpectedServerProcess $clientLauncher.GameProcess -TimeoutSeconds 20
    (Get-SafeFrameRecord -Label 'client_join' -Frame $joinFrame -RequestId $joinRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
    if ($joinFrame.Command -ne 'join_ack' -or [string] $joinFrame.Data.status -ne 'accepted') { throw "client native join was not accepted (code=$($joinFrame.Data.code))" }
    $clientOperationSequence = [uint64] (Get-OptionalValue $joinFrame.Data 'operation_sequence' 0)
    if ($clientOperationSequence -eq 0) { throw 'client join ACK lacks a nonzero operation sequence' }

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    $eventSequence = 0
    $receiptKeys = @{}
    while ($true) {
        $loopDeadline = if ($null -ne $closureDeadline) { $closureDeadline } else { $deadline }
        if ([DateTimeOffset]::UtcNow -ge $loopDeadline) { break }
        if ($authorityLauncher.Process.HasExited) { throw "authority exited during client admission (exit=$($authorityLauncher.Process.ExitCode))" }
        if (-not $teardownRequested -and $clientLauncher.Process.HasExited) {
            if ($connected -and -not $playable) {
                $teardownRequested = $true
                $playableTimeout = $true
                $closureDeadline = [DateTimeOffset]::UtcNow.AddSeconds([Math]::Min(30, [Math]::Max(1, $TimeoutSeconds)))
            }
            else {
                throw "client exited during native admission (exit=$($clientLauncher.Process.ExitCode))"
            }
        }
        $eventsFrame = $null
        try {
            $eventRequest = New-RequestId 'authority-events'
            $eventsFrame = Send-PayloadFrame -PipeName $authorityPipe -Command 'match_connection_events' -Arguments @{ request_id = $eventRequest; after_sequence = $eventSequence } -ExpectedServerProcess $authorityLauncher.GameProcess -ResponseTimeoutMs 5000
            (Get-SafeFrameRecord -Label 'authority_connection_events' -Frame $eventsFrame -RequestId $eventRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
        }
        catch {
            if ($_.Exception.Message -like '*owned pipe server verification failed*') { throw }
        }
        if ($null -ne $eventsFrame) {
            $frameEvents = @($eventsFrame.Data.events)
            if (-not [string]::IsNullOrWhiteSpace($BackendScopedEventPath) -and $frameEvents.Count -eq 0) {
                Write-PrivateScopedCursor -Path $BackendScopedEventPath -NextSequence $eventSequence
            }
            foreach ($event in $frameEvents) {
                $eventSequence = [int64] $event.sequence
                if ([string] $event.world_instance_id -ne $authorityWorld -or [string] $event.attempt_id -ne $attemptId -or [string] $event.authority_session_id -ne $authoritySessionId -or [int] $event.roster_revision -ne $fixtureRosterRevision -or [int] $event.route_generation -ne $fixtureRouteGeneration) {
                    throw 'Payload emitted a connection event outside the live Dedicated scope'
                }
                if ([string] $event.grant_jti -ne $joinGrantJti) { throw 'Payload emitted an unexpected grant JTI for the real Steam seat' }
                if (-not [string]::IsNullOrWhiteSpace($BackendScopedEventPath)) {
                    Write-PrivateScopedEvent -Path $BackendScopedEventPath -Event $event
                }
                $state = [string] $event.state
                $receiptKey = "$state|$([string] $event.player_id)|$([int] $event.connection_generation)|$([string] $event.native_connection_nonce)"
                if ($state -eq 'RESERVED' -and -not $receiptKeys.ContainsKey($receiptKey)) {
                    [void] (Wait-BackendReceipt -Path (Join-Path $BackendReceiptDir '01-reserve.json') -ExpectedKind 'confirm_match_admission' -Event $event -TimeoutSeconds ([Math]::Min(60, $TimeoutSeconds)))
                    [void] (Send-ScopedReceipt -PipeName $authorityPipe -Command 'confirm_match_admission' -Event $event -ExpectedServerProcess $authorityLauncher.GameProcess -FramesPath $framesPath)
                    $receiptKeys[$receiptKey] = $true
                }
                elseif ($state -eq 'NATIVE_ADMITTED' -and -not $receiptKeys.ContainsKey($receiptKey)) {
                    # The driver must consume the grant in Backend
                    # ConfirmConnected before the Payload is told to promote
                    # its native seat. This prevents event-only promotion.
                    [void] (Wait-BackendReceipt -Path (Join-Path $BackendReceiptDir '02-confirm-connection.json') -ExpectedKind 'confirm_match_connection' -Event $event -TimeoutSeconds ([Math]::Min(60, $TimeoutSeconds)))
                    [void] (Send-ScopedReceipt -PipeName $authorityPipe -Command 'confirm_match_connection' -Event $event -ExpectedServerProcess $authorityLauncher.GameProcess -FramesPath $framesPath)
                    $receiptKeys[$receiptKey] = $true
                }
                elseif ($state -eq 'DISCONNECTED' -and -not $receiptKeys.ContainsKey($receiptKey)) {
                    [void] (Wait-BackendReceipt -Path (Join-Path $BackendReceiptDir '04-release.json') -ExpectedKind 'release_match_admission' -Event $event -TimeoutSeconds ([Math]::Min(60, $TimeoutSeconds)))
                    [void] (Send-ScopedReceipt -PipeName $authorityPipe -Command 'release_match_admission' -Event $event -ExpectedServerProcess $authorityLauncher.GameProcess -FramesPath $framesPath)
                    $receiptKeys[$receiptKey] = $true
                    $released = $true
                    if (-not $playable) { $playableTimeout = $true }
                }
                if ($state -eq 'CONNECTED' -and -not $connected) {
                    [void] (Wait-BackendReceipt -Path (Join-Path $BackendReceiptDir '03-connected-observed.json') -ExpectedKind 'connected_observed' -Event $event -TimeoutSeconds ([Math]::Min(60, $TimeoutSeconds)))
                    foreach ($scopeKey in $expectedClientScope.Keys) {
                        if ([string] $event.$scopeKey -ne [string] $expectedClientScope[$scopeKey]) {
                            throw "backend CONNECTED receipt differs from staged client scope '$scopeKey'"
                        }
                    }
                    $clientConfirmRequest = New-RequestId 'confirm-client-connection'
                    $clientConfirmArguments = Get-ScopedReceiptArguments -Event $event -RequestId $clientConfirmRequest
                    $clientConfirmArguments.operation_sequence = $clientOperationSequence
                    $clientConfirmFrame = Send-PayloadFrame -PipeName $clientPipe -Command 'confirm_client_match_connection' -Arguments $clientConfirmArguments -ExpectedServerProcess $clientLauncher.GameProcess -ResponseTimeoutMs 10000
                    if ($clientConfirmFrame.Command -ne 'confirm_client_match_connection_ack' -or
                        -not [bool] (Get-OptionalValue $clientConfirmFrame.Data 'accepted' $false) -or
                        [uint64] (Get-OptionalValue $clientConfirmFrame.Data 'operation_sequence' 0) -ne $clientOperationSequence) {
                        throw 'client rejected the exact backend CONNECTED confirmation'
                    }
                    (Get-SafeFrameRecord -Label 'client_connection_confirmed' -Frame $clientConfirmFrame -RequestId $clientConfirmRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
                    $connected = $true
                    $connectedEvent = $event
                    $playableDeadline = [DateTimeOffset]::UtcNow.AddSeconds([Math]::Min(30, [Math]::Max(1, $TimeoutSeconds)))
                    if ($playableDeadline -gt $deadline) { $deadline = $playableDeadline }
                }
                if ($state -eq 'DISCONNECTED' -and $released) {
                    $disconnectedEvent = $event
                }
            }
        }
        if (-not $teardownRequested) {
            try {
                $statusRequest = New-RequestId 'client-status'
                $statusFrame = Send-PayloadFrame -PipeName $clientPipe -Command 'payload_status' -Arguments @{ request_id = $statusRequest } -ExpectedServerProcess $clientLauncher.GameProcess -ResponseTimeoutMs 5000
                (Get-SafeFrameRecord -Label 'client_payload_status_poll' -Frame $statusFrame -RequestId $statusRequest) | ConvertTo-Json -Compress -Depth 12 | Add-Content -LiteralPath $framesPath -Encoding UTF8
                $match = $statusFrame.Data.match
                if ($null -ne $match -and [string] $match.state -in @('failed', 'cancelled')) {
                    throw "native client transition terminated: $([string] (Get-OptionalValue $match 'last_error' 'unknown'))"
                }
                if ($null -ne $match -and [string] $match.state -eq 'playable') {
                    if (-not $connected -or
                        [uint64] (Get-OptionalValue $match 'operation_sequence' 0) -ne $clientOperationSequence -or
                        -not [bool] (Get-OptionalValue $match 'scope_verified' $false) -or
                        -not [bool] (Get-OptionalValue $match 'local_pawn_ready' $false) -or
                        -not [bool] (Get-OptionalValue $match 'native_net_ready' $false) -or
                        [string] (Get-OptionalValue $match 'native_connection_nonce' '') -ne [string] $connectedEvent.native_connection_nonce -or
                        [string]::IsNullOrWhiteSpace([string] (Get-OptionalValue $match 'local_world_instance_id' ''))) {
                        throw 'client Playable lacks the exact confirmed operation or local native readiness'
                    }
                    foreach ($scopeKey in $expectedClientScope.Keys) {
                        if ([string] $match.playable_scope.$scopeKey -ne [string] $expectedClientScope[$scopeKey]) {
                            throw "client Playable scope mismatch '$scopeKey'"
                        }
                    }
                    $playable = $true
                }
            }
            catch {
                if ($_.Exception.Message -like '*owned pipe server verification failed*' -or $_.Exception.Message -like '*client Playable*' -or $_.Exception.Message -like '*native client transition terminated*') { throw }
            }
        }
        if ($connected -and $playable -and -not $teardownRequested) {
            # End the one-seat component run through the native disconnect
            # boundary. The authority remains alive so its DISCONNECTED event
            # can be scoped, released, and observed by the backend driver.
            Stop-ProcessHandle -Process $clientLauncher.GameProcess
            $teardownRequested = $true
            $closureDeadline = [DateTimeOffset]::UtcNow.AddSeconds([Math]::Min(30, [Math]::Max(1, $TimeoutSeconds)))
        }
        elseif ($connected -and -not $playable -and -not $teardownRequested -and
            $null -ne $playableDeadline -and [DateTimeOffset]::UtcNow -ge $playableDeadline) {
            # A CONNECTED seat without a bounded playable transition is not a
            # positive result. Stop only the captured owned client and keep
            # polling the authority until DISCONNECTED and the exact release
            # receipt close the admission chain.
            Stop-ProcessHandle -Process $clientLauncher.GameProcess
            $teardownRequested = $true
            $playableTimeout = $true
            $closureDeadline = [DateTimeOffset]::UtcNow.AddSeconds([Math]::Min(30, [Math]::Max(1, $TimeoutSeconds)))
        }
        if ($released -and ($playable -or $playableTimeout)) { break }
        Start-Sleep -Milliseconds 750
    }

    if ($released -and -not [string]::IsNullOrWhiteSpace($BackendAdmissionEvidencePath)) {
        $admissionDeadline = [DateTimeOffset]::UtcNow.AddSeconds([Math]::Min(30, $TimeoutSeconds))
        while ([DateTimeOffset]::UtcNow -lt $admissionDeadline -and $null -eq $admissionEvidence) {
            if (Test-Path -LiteralPath $BackendAdmissionEvidencePath -PathType Leaf) {
                try {
                    $candidateEvidence = Get-Content -LiteralPath $BackendAdmissionEvidencePath -Raw -Encoding UTF8 | ConvertFrom-Json
                    $evidenceRosterRevision = [int] (Get-OptionalValue $candidateEvidence 'roster_revision' 0)
                    $evidenceRouteGeneration = [int] (Get-OptionalValue $candidateEvidence 'route_generation' 0)
                    $evidencePlayerId = [string] (Get-OptionalValue $candidateEvidence 'player_id' '')
                    $evidenceGrantJti = [string] (Get-OptionalValue $candidateEvidence 'grant_jti' '')
                    $evidenceNativeNonce = [string] (Get-OptionalValue $candidateEvidence 'native_connection_nonce' '')
                    $evidenceConnectionGeneration = [int] (Get-OptionalValue $candidateEvidence 'connection_generation' 0)
                    $evidenceSequence = [int64] (Get-OptionalValue $candidateEvidence 'last_sequence' 0)
                    $evidenceReservedSequence = [int64] (Get-OptionalValue $candidateEvidence 'reserved_sequence' 0)
                    $evidenceNativeAdmittedSequence = [int64] (Get-OptionalValue $candidateEvidence 'native_admitted_sequence' 0)
                    $evidenceConnectedSequence = [int64] (Get-OptionalValue $candidateEvidence 'connected_sequence' 0)
                    $evidenceDisconnectedSequence = [int64] (Get-OptionalValue $candidateEvidence 'disconnected_sequence' 0)
                    if ($null -ne $disconnectedEvent -and
                        [string] (Get-OptionalValue $candidateEvidence 'attempt_id' '') -eq $attemptId -and
                        [string] (Get-OptionalValue $candidateEvidence 'authority_session_id' '') -eq $authoritySessionId -and
                        [string] (Get-OptionalValue $candidateEvidence 'world_instance_id' '') -eq $authorityWorld -and
                        $evidenceRosterRevision -eq [int] $disconnectedEvent.roster_revision -and
                        $evidenceRouteGeneration -eq [int] $disconnectedEvent.route_generation -and
                        $evidencePlayerId -eq [string] $disconnectedEvent.player_id -and
                        $evidenceGrantJti -eq [string] $disconnectedEvent.grant_jti -and
                        $evidenceNativeNonce -eq [string] $disconnectedEvent.native_connection_nonce -and
                        $evidenceConnectionGeneration -eq [int] $disconnectedEvent.connection_generation -and
                        $evidenceSequence -eq [int64] $disconnectedEvent.sequence -and
                        $evidenceReservedSequence -gt 0 -and
                        $evidenceNativeAdmittedSequence -gt $evidenceReservedSequence -and
                        $evidenceConnectedSequence -eq [int64] $connectedEvent.sequence -and
                        $evidenceConnectedSequence -gt $evidenceNativeAdmittedSequence -and
                        $evidenceDisconnectedSequence -eq $evidenceSequence -and
                        $evidenceDisconnectedSequence -gt $evidenceConnectedSequence -and
                        [bool] (Get-OptionalValue $candidateEvidence 'reserve_confirm_release_chain' $false) -eq $true) {
                        $admissionEvidence = $candidateEvidence
                    }
                }
                catch {
                    # An atomic writer may be between replacement and completion.
                }
            }
            if ($null -eq $admissionEvidence) { Start-Sleep -Milliseconds 250 }
        }
    }
    if ($null -ne $admissionEvidence -and (Test-Path -LiteralPath $BackendAdmissionEvidencePath -PathType Leaf)) {
        $admissionEvidenceHash = Get-Sha256 $BackendAdmissionEvidencePath
    }
    if ($Bp047Cleanup) {
        if (-not $released -or $null -eq $disconnectedEvent -or $null -eq $admissionEvidence) {
            throw 'BP047 cleanup requires the exact native DISCONNECTED event and Backend release evidence'
        }
        [void] (Write-OwnedAuthorityExitReceipt -Launcher $authorityLauncher -OutputPath $ownedExitReceiptPath -AttemptId $attemptId -AuthorityId $authorityId -AuthoritySessionId $authoritySessionId -WorldInstanceId $authorityWorld -RosterRevision $fixtureRosterRevision -RouteGeneration $fixtureRouteGeneration)
        $authorityExitRecorded = $true
    }
    if ($playableTimeout) {
        $outcome = 'PARTIAL_ADMISSION_CHAIN_PLAYABLE_NOT_REACHED'
        $failure = if (-not $released) {
            'client did not reach playable within the bounded window and DISCONNECTED/04 release was not observed before cleanup deadline'
        }
        elseif ($null -eq $admissionEvidence) {
            'client did not reach playable within the bounded window; exact DISCONNECTED/04 release closed the chain but matching final admission evidence was not observed'
        }
        else {
            'client did not reach playable within the bounded window; exact admission chain was closed and recorded as partial'
        }
    }
    elseif ($connected -and $playable -and $released -and $null -ne $admissionEvidence) {
        $outcome = 'PASS_COMPONENT_ONLY'
    }
    elseif ($connected -or $playable) {
        $outcome = 'PARTIAL_NATIVE_EVENT_WITHOUT_BACKEND_CHAIN'
    }
    else {
        $outcome = 'BLOCKED_NATIVE_ADMISSION_NOT_CONNECTED'
    }
}
catch {
    $failure = $_.Exception.Message
    if ($outcome -eq 'BLOCKED') { $outcome = 'BLOCKED' }
}
finally {
    foreach ($launcher in @($clientLauncher, $authorityLauncher)) {
        if ($null -eq $launcher) { continue }
        # Kill only process handles captured after the exact owned pipe and
        # executable were verified.  Never re-resolve a stale PID during
        # cleanup: a reused PID must not terminate an unrelated process.
        if ($null -eq $launcher.GameProcess) {
            $pipeArgument = @($launcher.Arguments | Where-Object { $_ -like '-pipe=*' } | Select-Object -First 1)
            if ($pipeArgument.Count -eq 1) {
                try {
                    $launcher.GameProcess = Get-OwnedGameProcess -PipeName ([string] $pipeArgument[0]).Substring(6) -ExpectedExecutablePath (Join-Path $GameWin64 'ProjectBoundarySteam-Win64-Shipping.exe') -TimeoutSeconds 1
                }
                catch { }
            }
        }
        Stop-ProcessHandle -Process $launcher.GameProcess
        try { Stop-OwnedLauncherChildren -Launcher $launcher }
        catch {
            $failure = "$failure; owned child cleanup failed: $($_.Exception.Message)"
            $outcome = 'BLOCKED_OWNED_CHILD_CLEANUP_FAILED'
        }
        Stop-ProcessHandle -Process $launcher.Process
        try { Sanitize-LauncherLog -RawPath $launcher.RawStdout -SafePath $launcher.Stdout } catch { }
        try { Sanitize-LauncherLog -RawPath $launcher.RawStderr -SafePath $launcher.Stderr } catch { }
    }
    if ($Execute -and (Test-Path -LiteralPath $restorePath -PathType Leaf)) {
        try {
            Copy-Item -LiteralPath $restorePath -Destination $installedPayload -Force
            $restoredHash = Get-Sha256 $installedPayload
            if ($restoredHash -ne $ExpectedOriginalPayloadSha256) {
                $failure = if ($failure) { "$failure; restore hash=$restoredHash" } else { "restore hash=$restoredHash" }
                $outcome = 'BLOCKED_RESTORE_HASH_MISMATCH'
            }
        }
        catch {
            $failure = if ($failure) { "$failure; restore failed: $($_.Exception.Message)" } else { "restore failed: $($_.Exception.Message)" }
            $outcome = 'BLOCKED_RESTORE_FAILED'
        }
    }
    $summary = [ordered]@{
        run_id = $runId
        outcome = $outcome
        failure = $failure
        execute = [bool] $Execute
        fixture_path = $FixturePath
        fixture_sha256 = $fixtureHash
        authority_pipe = $authorityPipe
        client_pipe = $clientPipe
        authority_port = $AuthorityPort
        client_port = $ClientPort
        authority_world_instance_id = $authorityWorld
        backend_authority_ready_evidence_path = $BackendAuthorityReadyEvidencePath
        backend_grant_evidence_path = $BackendGrantEvidencePath
        backend_grant_evidence_sha256 = $grantEvidenceHash
        client_ready_marker_path = $ClientReadyMarkerPath
        client_ready_marker_sha256 = $clientReadyMarkerHash
        playable_timeout = [bool] $playableTimeout
        playable_wait_deadline_utc = if ($null -ne $playableDeadline) { ([DateTimeOffset] $playableDeadline).ToString('o') } else { $null }
        closure_deadline_utc = if ($null -ne $closureDeadline) { ([DateTimeOffset] $closureDeadline).ToString('o') } else { $null }
        connected_event_sequence = if ($null -ne $connectedEvent) { [int64] $connectedEvent.sequence } else { $null }
        disconnected_event_sequence = if ($null -ne $disconnectedEvent) { [int64] $disconnectedEvent.sequence } else { $null }
        backend_admission_evidence_sha256 = $admissionEvidenceHash
        backend_scoped_event_path = $BackendScopedEventPath
        backend_scoped_event_sha256 = if (-not [string]::IsNullOrWhiteSpace($BackendScopedEventPath) -and (Test-Path -LiteralPath $BackendScopedEventPath -PathType Leaf)) { Get-Sha256 $BackendScopedEventPath } else { $null }
        backend_receipt_dir = $BackendReceiptDir
        candidate_payload_sha256 = $ExpectedCandidatePayloadSha256
        restored_payload_sha256 = if (Test-Path -LiteralPath $installedPayload -PathType Leaf) { Get-Sha256 $installedPayload } else { $null }
        bp047_authority_exit_recorded = [bool] $authorityExitRecorded
        owned_exit_receipt_path = if (Test-Path -LiteralPath $ownedExitReceiptPath -PathType Leaf) { $ownedExitReceiptPath } else { $null }
        owned_exit_receipt_sha256 = if (Test-Path -LiteralPath $ownedExitReceiptPath -PathType Leaf) { Get-Sha256 $ownedExitReceiptPath } else { $null }
        authority_wrapper_pid = if ($null -ne $authorityLauncher) { $authorityLauncher.Process.Id } else { $null }
        client_wrapper_pid = if ($null -ne $clientLauncher) { $clientLauncher.Process.Id } else { $null }
        authority_game_pid = if ($null -ne $authorityLauncher -and $null -ne $authorityLauncher.GameProcess) { $authorityLauncher.GameProcess.Id } else { $null }
        client_game_pid = if ($null -ne $clientLauncher -and $null -ne $clientLauncher.GameProcess) { $clientLauncher.GameProcess.Id } else { $null }
        authority_game_handle_pid = if ($null -ne $authorityLauncher -and $null -ne $authorityLauncher.GameProcess -and $null -ne $authorityLauncher.GameProcess.OwnedHandleProcessId) { $authorityLauncher.GameProcess.OwnedHandleProcessId } else { $null }
        client_game_handle_pid = if ($null -ne $clientLauncher -and $null -ne $clientLauncher.GameProcess -and $null -ne $clientLauncher.GameProcess.OwnedHandleProcessId) { $clientLauncher.GameProcess.OwnedHandleProcessId } else { $null }
        authority_game_creation_time_utc = if ($null -ne $authorityLauncher -and $null -ne $authorityLauncher.GameProcess -and $null -ne $authorityLauncher.GameProcess.OwnedCreationTimeUtc) { ([DateTime] $authorityLauncher.GameProcess.OwnedCreationTimeUtc).ToString('o') } else { $null }
        client_game_creation_time_utc = if ($null -ne $clientLauncher -and $null -ne $clientLauncher.GameProcess -and $null -ne $clientLauncher.GameProcess.OwnedCreationTimeUtc) { ([DateTime] $clientLauncher.GameProcess.OwnedCreationTimeUtc).ToString('o') } else { $null }
        evidence_root = $runRoot
        frames_path = $framesPath
        observed_utc = [DateTimeOffset]::UtcNow.ToString('o')
    }
    Write-SafeJson -Path $summaryPath -Value $summary
    Write-Output ($summary | ConvertTo-Json -Depth 12)
}



