<#
.SYNOPSIS
Provides the retired Unity Multiplay discovery/echo surface needed by the
pinned Boundary client while running a local PVE smoke test.

.DESCRIPTION
The helper binds HTTP and UDP only on 127.0.0.1. The HTTP endpoint returns the
region identity used by the pinned Boundary client. The UDP endpoint follows
the pinned client's receive layout: response bytes 2 and 3 must contain the
probe sequence and per-run random ID, so a 0x95/0x00 header is followed by
request bytes from offset 11 onward.
#>
[CmdletBinding()]
param(
    [ValidateRange(0, 65535)]
    [int] $HttpPort = 0,

    [ValidateRange(0, 65535)]
    [int] $UdpPort = 0,

    [ValidateRange(0, 2147483647)]
    [int] $ParentProcessId = 0,

    [ValidateRange(30, 3600)]
    [int] $LifetimeSeconds = 900,

    [string] $LogPath
)

$ErrorActionPreference = 'Stop'

function Write-QosLog {
    param([Parameter(Mandatory = $true)] [string] $Message)

    if ([string]::IsNullOrWhiteSpace($LogPath)) {
        return
    }
    $timestamp = [DateTimeOffset]::Now.ToString('o')
    Add-Content -LiteralPath $LogPath -Value "$timestamp $Message" -Encoding UTF8
}

function Read-HttpHeader {
    param([Parameter(Mandatory = $true)] [IO.Stream] $Stream)

    $buffer = New-Object byte[] 16384
    $length = 0
    while ($length -lt $buffer.Length) {
        $read = $Stream.Read($buffer, $length, $buffer.Length - $length)
        if ($read -le 0) {
            break
        }
        $length += $read
        if ($length -ge 4) {
            for ($index = [Math]::Max(0, $length - $read - 3); $index -le $length - 4; $index++) {
                if ($buffer[$index] -eq 13 -and $buffer[$index + 1] -eq 10 -and
                    $buffer[$index + 2] -eq 13 -and $buffer[$index + 3] -eq 10) {
                    return [Text.Encoding]::ASCII.GetString($buffer, 0, $index + 4)
                }
            }
        }
    }
    return ''
}

$loopback = [Net.IPAddress]::Loopback
$httpListener = [Net.Sockets.TcpListener]::new($loopback, $HttpPort)
$udpListener = [Net.Sockets.UdpClient]::new(
    [Net.IPEndPoint]::new($loopback, $UdpPort))

try {
    $httpListener.Start()
    $effectiveHttpPort = ([Net.IPEndPoint] $httpListener.LocalEndpoint).Port
    $effectiveUdpPort = ([Net.IPEndPoint] $udpListener.Client.LocalEndPoint).Port
    $discoveryBody = @{
        servers = @(
            @{
                location_id = 6
                region_id = '336d1f3e-3ecb-11eb-a7dc-3b7705f20f56'
                ipv4 = '127.0.0.1'
                ipv6 = ''
                port = $effectiveUdpPort
            }
        )
    } | ConvertTo-Json -Compress
    $discoveryBytes = [Text.Encoding]::UTF8.GetBytes($discoveryBody)

    [pscustomobject]@{
        event = 'ready'
        http_url = "http://127.0.0.1:$effectiveHttpPort/servers"
        udp_endpoint = "127.0.0.1:$effectiveUdpPort"
        pid = $PID
    } | ConvertTo-Json -Compress
    [Console]::Out.Flush()
    Write-QosLog "ready http_port=$effectiveHttpPort udp_port=$effectiveUdpPort"

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($LifetimeSeconds)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        if ($ParentProcessId -gt 0 -and
            $null -eq (Get-Process -Id $ParentProcessId -ErrorAction SilentlyContinue)) {
            Write-QosLog 'parent process exited'
            break
        }

        while ($httpListener.Pending()) {
            $client = $httpListener.AcceptTcpClient()
            try {
                $client.ReceiveTimeout = 2000
                $client.SendTimeout = 2000
                $stream = $client.GetStream()
                $header = Read-HttpHeader -Stream $stream
                $requestLine = if ([string]::IsNullOrWhiteSpace($header)) {
                    ''
                }
                else {
                    ($header -split "`r`n", 2)[0]
                }
                $parts = @($requestLine -split '\s+')
                $method = if ($parts.Count -gt 0) { $parts[0].ToUpperInvariant() } else { '' }
                $target = if ($parts.Count -gt 1) { $parts[1] } else { '' }
                $isSupported = $method -eq 'GET' -or $method -eq 'HEAD'
                $status = if ($isSupported) { '200 OK' } else { '405 Method Not Allowed' }
                $body = if ($isSupported) { $discoveryBytes } else {
                    [Text.Encoding]::UTF8.GetBytes('{"error":"method not allowed"}')
                }
                $responseHeader = "HTTP/1.1 $status`r`n" +
                    "Content-Type: application/json`r`n" +
                    "Content-Length: $($body.Length)`r`n" +
                    "Cache-Control: no-store`r`n" +
                    "ETag: `"project-rebound-local-qos-v1`"`r`n" +
                    "Connection: close`r`n`r`n"
                $headerBytes = [Text.Encoding]::ASCII.GetBytes($responseHeader)
                $stream.Write($headerBytes, 0, $headerBytes.Length)
                if ($method -ne 'HEAD') {
                    $stream.Write($body, 0, $body.Length)
                }
                $stream.Flush()
                Write-QosLog "http method=$method status=$($status.Substring(0, 3)) target_chars=$($target.Length)"
            }
            catch {
                Write-QosLog "http_error=$($_.Exception.GetType().Name)"
            }
            finally {
                $client.Dispose()
            }
        }

        while ($udpListener.Available -gt 0) {
            $remoteEndpoint = [Net.IPEndPoint]::new([Net.IPAddress]::Any, 0)
            $request = $udpListener.Receive([ref] $remoteEndpoint)
            if ($request.Length -lt 3 -or $request[0] -ne 0x59) {
                Write-QosLog "udp_ignored bytes=$($request.Length)"
                continue
            }

            $customOffset = 11
            if ($request.Length -lt $customOffset) {
                Write-QosLog "udp_invalid_boundary_packet bytes=$($request.Length)"
                continue
            }

            $customLength = $request.Length - $customOffset
            $response = New-Object byte[] (2 + $customLength)
            $response[0] = 0x95
            $response[1] = 0x00
            if ($customLength -gt 0) {
                [Buffer]::BlockCopy($request, $customOffset, $response, 2, $customLength)
            }
            [void] $udpListener.Send($response, $response.Length, $remoteEndpoint)
            $sequence = if ($customLength -gt 0) { [int] $request[$customOffset] } else { -1 }
            Write-QosLog "udp_echo sequence=$sequence request_bytes=$($request.Length) response_bytes=$($response.Length)"
        }

        Start-Sleep -Milliseconds 10
    }
}
finally {
    $httpListener.Stop()
    $udpListener.Dispose()
    Write-QosLog 'stopped'
}
