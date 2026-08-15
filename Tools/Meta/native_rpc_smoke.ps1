param(
    [string] $TunnelHttpUrl = 'http://127.0.0.1:52252',
    [int] $TimeoutSeconds = 45,
    [string] $RpcPath = '/assets.Assets/QueryAssets'
)

$ErrorActionPreference = 'Stop'

function Add-Varint {
    param(
        [Parameter(Mandatory = $true)]
        [System.Collections.Generic.List[byte]] $Buffer,
        [Parameter(Mandatory = $true)]
        [UInt64] $Value
    )

    do {
        $next = [byte] ($Value -band 0x7f)
        $Value = $Value -shr 7
        if ($Value -ne 0) {
            $next = $next -bor 0x80
        }
        $Buffer.Add($next)
    } while ($Value -ne 0)
}

function New-RequestWrapper {
    param(
        [int] $MessageId,
        [Parameter(Mandatory = $true)]
        [string] $RpcPath,
        [byte[]] $Message = @()
    )

    $buffer = [System.Collections.Generic.List[byte]]::new()
    $buffer.Add(0x08)
    Add-Varint -Buffer $buffer -Value ([UInt64] $MessageId)

    $pathBytes = [Text.Encoding]::UTF8.GetBytes($RpcPath)
    $buffer.Add(0x12)
    Add-Varint -Buffer $buffer -Value ([UInt64] $pathBytes.Length)
    $buffer.AddRange($pathBytes)

    if ($Message.Length -gt 0) {
        $buffer.Add(0x1a)
        Add-Varint -Buffer $buffer -Value ([UInt64] $Message.Length)
        $buffer.AddRange($Message)
    }
    return $buffer.ToArray()
}

function Read-Exact {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.Stream] $Stream,
        [Parameter(Mandatory = $true)]
        [int] $Length
    )

    $buffer = [byte[]]::new($Length)
    $offset = 0
    while ($offset -lt $Length) {
        $read = $Stream.Read($buffer, $offset, $Length - $offset)
        if ($read -eq 0) {
            throw "native stream closed after $offset of $Length bytes"
        }
        $offset += $read
    }
    return $buffer
}

function Write-Frame {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.Stream] $Stream,
        [Parameter(Mandatory = $true)]
        [byte[]] $Payload
    )

    $header = [BitConverter]::GetBytes([UInt32] $Payload.Length)
    if ([BitConverter]::IsLittleEndian) {
        [Array]::Reverse($header)
    }
    $Stream.Write($header, 0, $header.Length)
    $Stream.Write($Payload, 0, $Payload.Length)
    $Stream.Flush()
}

function Read-Frame {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.Stream] $Stream
    )

    $header = Read-Exact -Stream $Stream -Length 4
    if ([BitConverter]::IsLittleEndian) {
        [Array]::Reverse($header)
    }
    $length = [BitConverter]::ToUInt32($header, 0)
    if ($length -eq 0 -or $length -gt 16MB) {
        throw "invalid native frame length: $length"
    }
    return Read-Exact -Stream $Stream -Length ([int] $length)
}

function Read-Varint {
    param(
        [Parameter(Mandatory = $true)]
        [byte[]] $Buffer,
        [Parameter(Mandatory = $true)]
        [ref] $Offset
    )

    [UInt64] $value = 0
    for ($shift = 0; $shift -lt 64; $shift += 7) {
        if ($Offset.Value -ge $Buffer.Length) {
            throw 'truncated protobuf varint'
        }
        $next = $Buffer[$Offset.Value]
        $Offset.Value++
        $value = $value -bor (([UInt64] ($next -band 0x7f)) -shl $shift)
        if (($next -band 0x80) -eq 0) {
            return $value
        }
    }
    throw 'protobuf varint is too long'
}

function Read-ResponseSummary {
    param([byte[]] $Payload)

    $offset = 0
    $summary = [ordered]@{
        message_id = 0
        rpc_path = ''
        error_code = 0
        message_bytes = 0
    }
    while ($offset -lt $Payload.Length) {
        $key = Read-Varint -Buffer $Payload -Offset ([ref] $offset)
        $field = [int] ($key -shr 3)
        $wire = [int] ($key -band 7)
        if ($wire -eq 0) {
            $value = Read-Varint -Buffer $Payload -Offset ([ref] $offset)
            if ($field -eq 1) { $summary.message_id = [int] $value }
            if ($field -eq 3) { $summary.error_code = [int] $value }
            continue
        }
        if ($wire -ne 2) {
            throw "unsupported protobuf wire type: $wire"
        }
        $length = [int] (Read-Varint -Buffer $Payload -Offset ([ref] $offset))
        if ($length -lt 0 -or $offset + $length -gt $Payload.Length) {
            throw 'truncated protobuf field'
        }
        if ($field -eq 2) {
            $summary.rpc_path = [Text.Encoding]::UTF8.GetString($Payload, $offset, $length)
        }
        if ($field -eq 4) {
            $summary.message_bytes = $length
        }
        $offset += $length
    }
    return [pscustomobject] $summary
}

$connectUri = "$($TunnelHttpUrl.TrimEnd('/'))/connectServer"
$connect = Invoke-RestMethod `
    -Uri $connectUri `
    -Method Post `
    -ContentType 'application/json' `
    -Body '{}' `
    -TimeoutSec $TimeoutSeconds

if ([string]::IsNullOrWhiteSpace([string] $connect.gateToken) -or
    [string]::IsNullOrWhiteSpace([string] $connect.endpoint)) {
    throw 'connectServer did not return a gate token and endpoint'
}

$endpoint = [string] $connect.endpoint
$separator = $endpoint.LastIndexOf(':')
if ($separator -lt 1) {
    throw 'connectServer returned an invalid endpoint'
}
$hostName = $endpoint.Substring(0, $separator)
$port = [int] $endpoint.Substring($separator + 1)

$client = [Net.Sockets.TcpClient]::new()
$stage = 'connect'
try {
    $connectTask = $client.ConnectAsync($hostName, $port)
    if (-not $connectTask.Wait([TimeSpan]::FromSeconds($TimeoutSeconds))) {
        throw 'timed out connecting to the local native endpoint'
    }
    $client.ReceiveTimeout = $TimeoutSeconds * 1000
    $client.SendTimeout = $TimeoutSeconds * 1000
    $stream = $client.GetStream()

    $stage = 'gate handshake'
    $gateFrame = New-RequestWrapper -MessageId 1 -RpcPath ([string] $connect.gateToken)
    $gateWatch = [Diagnostics.Stopwatch]::StartNew()
    Write-Frame -Stream $stream -Payload $gateFrame
    $gateEcho = Read-Frame -Stream $stream
    $gateWatch.Stop()
    if (-not [Collections.StructuralComparisons]::StructuralEqualityComparer.Equals(
        $gateFrame,
        $gateEcho
    )) {
        throw 'native Gate handshake did not echo the authenticated request'
    }

    $stage = $RpcPath
    $queryFrame = New-RequestWrapper -MessageId 2 -RpcPath $RpcPath
    $queryWatch = [Diagnostics.Stopwatch]::StartNew()
    Write-Frame -Stream $stream -Payload $queryFrame
    $queryResponse = Read-Frame -Stream $stream
    $queryWatch.Stop()
    $summary = Read-ResponseSummary -Payload $queryResponse
    if ($summary.message_id -ne 2 -or $summary.rpc_path -ne $RpcPath) {
        throw 'native RPC response wrapper did not match the request'
    }

    [pscustomobject]@{
        gate_handshake_ms = $gateWatch.ElapsedMilliseconds
        rpc_path = $RpcPath
        rpc_ms = $queryWatch.ElapsedMilliseconds
        response_frame_bytes = $queryResponse.Length
        response_message_bytes = $summary.message_bytes
        error_code = $summary.error_code
    } | ConvertTo-Json -Compress
}
catch {
    throw "$stage failed: $($_.Exception.Message)"
}
finally {
    $client.Dispose()
}
