Set-StrictMode -Version 2.0

# Read-only helpers shared by the Windows native hardware-test checks.
# This file deliberately contains no process launch, DLL copy, named-pipe write,
# registry write, or network operation.

$script:NativePackageSchemaVersion = 1
$script:NativePackagePurpose = 'hardware-test-only'
$script:FixedGameSha256 = '181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843'
$script:FixedGameBytes = 102362112
$script:ExpectedSteamApiSha256 = 'a44e5537939ae4ee bc69000589aa9b2437a667813a1657cc779198bae9b815a9'.Replace(' ', '')
$script:ExpectedSteamApiBytes = 298856
$script:RuntimeFileNames = @(
    'Payload.dll',
    'dxgi.dll',
    'DT_ItemType.json',
    'steam_appid.txt',
    'project_rebound_version.txt'
)
$script:SupportFileNames = @(
    'native/Common-WindowsNative.ps1',
    'native/Preflight-WindowsNative.ps1',
    'native/Collect-WindowsNative.ps1',
    'native/Test-WindowsNative.ps1',
    'native/README.md',
    'native/package-manifest.schema.json'
)

function Get-NativeSafePath {
    param([AllowNull()][string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    $value = $Path
    try {
        $profile = [Environment]::GetFolderPath('UserProfile')
        if (-not [string]::IsNullOrWhiteSpace($profile)) {
            $value = $value.Replace($profile, '%USERPROFILE%')
        }
    } catch { }
    # A caller may pass an unexpanded user path. Keep evidence free of the
    # Windows account name even when the profile folder was not resolvable.
    $value = [regex]::Replace($value, '(?i)^[A-Z]:\\Users\\[^\\]+', '$0'.Replace('$0', '%USERPROFILE%'))
    return $value
}

function Get-NativeUtcNow {
    return [DateTime]::UtcNow.ToString('o')
}

function Get-NativeDefaultEvidencePath {
    param(
        [Parameter(Mandatory = $true)][string]$PackagePath,
        [Parameter(Mandatory = $true)][string]$LeafName
    )
    # Evidence is deliberately a sibling of the package, never a file that
    # can become part of the manifest being verified.
    $packageFull = [IO.Path]::GetFullPath($PackagePath)
    $parent = Split-Path -Parent $packageFull
    return (Join-Path (Join-Path $parent 'native-evidence') $LeafName)
}

function Assert-NativeEvidenceOutputOutsidePackage {
    param(
        [Parameter(Mandatory = $true)][string]$PackagePath,
        [AllowNull()][string]$OutputPath
    )
    if ([string]::IsNullOrWhiteSpace($OutputPath)) { return }
    $packageFull = [IO.Path]::GetFullPath($PackagePath).TrimEnd('\', '/')
    $outputFull = [IO.Path]::GetFullPath($OutputPath).TrimEnd('\', '/')
    $item = Get-Item -LiteralPath $PackagePath -ErrorAction SilentlyContinue
    if ($null -ne $item -and $item.PSIsContainer) {
        $prefix = $packageFull + [IO.Path]::DirectorySeparatorChar
        if ($outputFull.Equals($packageFull, [StringComparison]::OrdinalIgnoreCase) -or
            $outputFull.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw 'OutputPath must be outside the package root so evidence cannot alter the verified package.'
        }
    } elseif ($outputFull.Equals($packageFull, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'OutputPath cannot overwrite the package ZIP.'
    }
}

function Test-NativeSha256 {
    param([AllowNull()][string]$Value)
    return (-not [string]::IsNullOrWhiteSpace($Value)) -and ($Value -match '^[0-9a-fA-F]{64}$')
}

function Normalize-NativeRelativePath {
    param([AllowNull()][string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    $value = $Path.Replace('\', '/')
    if ($value.StartsWith('/') -or $value.Contains("`0")) { return $null }
    $parts = $value.Split('/')
    if ($parts.Count -eq 0) { return $null }
    foreach ($part in $parts) {
        if ([string]::IsNullOrWhiteSpace($part) -or $part -eq '.' -or $part -eq '..' -or $part.Contains(':')) {
            return $null
        }
    }
    return ($parts -join '/')
}

function New-NativeCheck {
    param(
        [Parameter(Mandatory = $true)][string]$Id,
        [Parameter(Mandatory = $true)][ValidateSet('PASS', 'BLOCKED', 'NOT_RUN', 'ERROR')][string]$Status,
        [AllowNull()][object]$Observed,
        [AllowNull()][object]$Expected,
        [AllowNull()][string]$Note
    )
    return [ordered]@{
        id = $Id
        status = $Status
        observed = $Observed
        expected = $Expected
        note = $Note
    }
}

function Get-NativeFileDigest {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    $item = Get-Item -LiteralPath $Path -ErrorAction Stop
    $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
    return [ordered]@{
        path = Get-NativeSafePath $item.FullName
        bytes = [int64]$item.Length
        sha256 = $hash
        file_version = [string]$item.VersionInfo.FileVersion
        product_version = [string]$item.VersionInfo.ProductVersion
    }
}

function Get-NativeSignatureSummary {
    param([Parameter(Mandatory = $true)][string]$Path)
    try {
        $signature = Get-AuthenticodeSignature -LiteralPath $Path -ErrorAction Stop
        $subject = $null
        if ($null -ne $signature.SignerCertificate) {
            $subject = [string]$signature.SignerCertificate.Subject
        }
        $signer = if ($subject -match '(?i)CN=([^,]+)') { $Matches[1] } else { $subject }
        return [ordered]@{
            status = [string]$signature.Status
            signer = $signer
            has_signature = ($null -ne $signature.SignerCertificate)
        }
    } catch {
        return [ordered]@{
            status = 'UNAVAILABLE'
            signer = $null
            has_signature = $false
        }
    }
}

function Get-NativeTextSha256 {
    param([Parameter(Mandatory = $true)][string]$Text)
    $bytes = [Text.Encoding]::UTF8.GetBytes($Text)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
}

function Get-NativeDirectoryEntries {
    param([Parameter(Mandatory = $true)][string]$Root)
    $rootItem = Get-Item -LiteralPath $Root -ErrorAction Stop
    if (-not $rootItem.PSIsContainer) { throw 'PackageRoot is not a directory.' }
    $entries = @()
    foreach ($file in @(Get-ChildItem -LiteralPath $Root -File -Recurse -Force -ErrorAction Stop)) {
        $relative = $file.FullName.Substring($rootItem.FullName.Length).TrimStart('\', '/')
        $normalized = Normalize-NativeRelativePath $relative
        if ($null -eq $normalized) { throw 'Package contains an unsafe relative path.' }
        $entries += [ordered]@{
            path = $normalized
            bytes = [int64]$file.Length
            sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
            reparse_point = (($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)
        }
    }
    return $entries
}

function Get-NativeZipEntries {
    param([Parameter(Mandatory = $true)][string]$ZipPath)
    Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction SilentlyContinue
    $archive = [IO.Compression.ZipFile]::OpenRead($ZipPath)
    try {
        $rawEntries = @()
        foreach ($entry in $archive.Entries) {
            $rawName = [string]$entry.FullName
            if ($rawName.EndsWith('/')) { throw 'ZIP contains a directory entry; distribution files must be flat.' }
            $normalized = Normalize-NativeRelativePath $rawName
            if ($null -eq $normalized) { throw 'ZIP contains an unsafe relative path.' }
            $stream = $entry.Open()
            $sha = [Security.Cryptography.SHA256]::Create()
            try {
                $digest = ([BitConverter]::ToString($sha.ComputeHash($stream))).Replace('-', '').ToLowerInvariant()
            } finally {
                $sha.Dispose()
                $stream.Dispose()
            }
            $rawEntries += [ordered]@{
                raw_path = $normalized
                bytes = [int64]$entry.Length
                sha256 = $digest
                reparse_point = $false
            }
        }
        $roots = @($rawEntries | Where-Object { $_.raw_path.Contains('/') } | ForEach-Object { $_.raw_path.Split('/')[0] } | Sort-Object -Unique)
        $stripRoot = ($roots.Count -eq 1 -and @($rawEntries | Where-Object { -not $_.raw_path.Contains('/') }).Count -eq 0)
        $entries = @()
        foreach ($raw in $rawEntries) {
            $relative = if ($stripRoot) {
                $prefix = $roots[0] + '/'
                if (-not $raw.raw_path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'ZIP contains multiple top-level roots.' }
                $raw.raw_path.Substring($prefix.Length)
            } else { $raw.raw_path }
            if ([string]::IsNullOrWhiteSpace($relative)) { throw 'ZIP root contains an empty relative path.' }
            $entries += [ordered]@{
                path = $relative
                bytes = $raw.bytes
                sha256 = $raw.sha256
                reparse_point = $raw.reparse_point
            }
        }
        return $entries
    } finally {
        $archive.Dispose()
    }
}

function Get-NativeZipEntry {
    param(
        [Parameter(Mandatory = $true)][object]$Archive,
        [Parameter(Mandatory = $true)][string]$RelativePath
    )
    $normalized = Normalize-NativeRelativePath $RelativePath
    $matches = @()
    foreach ($entry in $Archive.Entries) {
        if ($entry.FullName.EndsWith('/')) { continue }
        $raw = Normalize-NativeRelativePath $entry.FullName
        if ($raw -ieq $normalized) { $matches += $entry; continue }
        $parts = $raw.Split('/')
        if ($parts.Count -gt 1 -and (($parts[1..($parts.Count - 1)] -join '/') -ieq $normalized)) { $matches += $entry }
    }
    if ($matches.Count -gt 1) { throw 'ZIP contains duplicate package paths.' }
    if ($matches.Count -eq 1) { return $matches[0] }
    return $null
}

function Read-NativePackageFileText {
    param(
        [Parameter(Mandatory = $true)][string]$PackagePath,
        [Parameter(Mandatory = $true)][string]$RelativePath
    )
    $normalized = Normalize-NativeRelativePath $RelativePath
    if ($null -eq $normalized) { throw 'Invalid package-relative path.' }
    if ((Get-Item -LiteralPath $PackagePath).PSIsContainer) {
        $path = Join-Path $PackagePath ($normalized.Replace('/', '\'))
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return $null }
        return Get-Content -LiteralPath $path -Raw -ErrorAction Stop
    }
    Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction SilentlyContinue
    $archive = [IO.Compression.ZipFile]::OpenRead($PackagePath)
    try {
        $entry = Get-NativeZipEntry -Archive $archive -RelativePath $normalized
        if ($null -eq $entry) { return $null }
        $stream = $entry.Open()
        $reader = New-Object IO.StreamReader($stream, [Text.Encoding]::UTF8, $true)
        try { return $reader.ReadToEnd() } finally { $reader.Dispose(); $stream.Dispose() }
    } finally { $archive.Dispose() }
}

function Get-NativePackageFileSha256 {
    param(
        [Parameter(Mandatory = $true)][string]$PackagePath,
        [Parameter(Mandatory = $true)][string]$RelativePath
    )
    $normalized = Normalize-NativeRelativePath $RelativePath
    if ($null -eq $normalized) { throw 'Invalid package-relative path.' }
    $item = Get-Item -LiteralPath $PackagePath -ErrorAction Stop
    if ($item.PSIsContainer) {
        $path = Join-Path $item.FullName ($normalized.Replace('/', '\'))
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return $null }
        return (Get-FileHash -LiteralPath $path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
    }
    Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction SilentlyContinue
    $archive = [IO.Compression.ZipFile]::OpenRead($item.FullName)
    try {
        $entry = Get-NativeZipEntry -Archive $archive -RelativePath $normalized
        if ($null -eq $entry) { return $null }
        $stream = $entry.Open()
        $sha = [Security.Cryptography.SHA256]::Create()
        try { return ([BitConverter]::ToString($sha.ComputeHash($stream))).Replace('-', '').ToLowerInvariant() }
        finally { $sha.Dispose(); $stream.Dispose() }
    } finally { $archive.Dispose() }
}

function Get-NativePackageEntries {
    param([Parameter(Mandatory = $true)][string]$PackagePath)
    $item = Get-Item -LiteralPath $PackagePath -ErrorAction Stop
    if ($item.PSIsContainer) { return Get-NativeDirectoryEntries $item.FullName }
    if ($item.Extension -ine '.zip') { throw 'PackagePath must be a directory or .zip file.' }
    return Get-NativeZipEntries $item.FullName
}

function Get-NativePackageManifest {
    param([Parameter(Mandatory = $true)][string]$PackagePath)
    $text = Read-NativePackageFileText -PackagePath $PackagePath -RelativePath 'package-manifest.json'
    if ([string]::IsNullOrWhiteSpace($text)) { throw 'package-manifest.json is missing from package root.' }
    try { $manifest = $text | ConvertFrom-Json -ErrorAction Stop }
    catch { throw 'package-manifest.json is not valid JSON.' }
    return [pscustomobject]@{
        object = $manifest
        sha256 = Get-NativePackageFileSha256 -PackagePath $PackagePath -RelativePath 'package-manifest.json'
    }
}

function Test-NativeManifestEntryShape {
    param(
        [Parameter(Mandatory = $true)][object]$Manifest,
        [Parameter(Mandatory = $true)][object[]]$PackageEntries
    )
    $checks = @()
    $schemaOk = ([int]$Manifest.schema_version -eq $script:NativePackageSchemaVersion)
    $checks += New-NativeCheck 'manifest.schema_version' $(if ($schemaOk) { 'PASS' } else { 'BLOCKED' }) ([int]$Manifest.schema_version) $script:NativePackageSchemaVersion 'Manifest schema is pinned.'
    $purposeOk = ([string]$Manifest.purpose -ceq $script:NativePackagePurpose)
    $checks += New-NativeCheck 'manifest.purpose' $(if ($purposeOk) { 'PASS' } else { 'BLOCKED' }) ([string]$Manifest.purpose) $script:NativePackagePurpose 'Only hardware-test packages are accepted by this tool.'
    $releaseOk = ($Manifest.release_ready -eq $false)
    $checks += New-NativeCheck 'manifest.release_ready' $(if ($releaseOk) { 'PASS' } else { 'BLOCKED' }) $Manifest.release_ready $false 'A hardware-test package is not a release claim.'

    $listed = @()
    if ($null -ne $Manifest.files) {
        foreach ($entry in @($Manifest.files)) {
            $path = Normalize-NativeRelativePath ([string]$entry.path)
            if ($null -eq $path) { $checks += New-NativeCheck 'manifest.files.path' 'BLOCKED' ([string]$entry.path) 'relative POSIX path' 'Manifest contains an invalid file path.'; continue }
            $listed += $path
            $entryBytes = 0L
            try { $entryBytes = [int64]$entry.bytes } catch { }
            $entryHash = ([string]$entry.sha256).ToLowerInvariant()
            $shape = ($entryBytes -gt 0 -and (Test-NativeSha256 $entryHash))
            $checks += New-NativeCheck ("manifest.file.shape:" + $path) $(if ($shape) { 'PASS' } else { 'BLOCKED' }) ([ordered]@{ bytes = $entryBytes; sha256 = $entryHash }) 'positive byte count and SHA-256' 'Manifest file entry shape.'
        }
    } else {
        $checks += New-NativeCheck 'manifest.files' 'BLOCKED' $null 'array' 'Manifest files array is missing.'
    }
    $duplicate = @($listed | Group-Object { $_.ToLowerInvariant() } | Where-Object { $_.Count -gt 1 })
    $checks += New-NativeCheck 'manifest.files.unique' $(if ($duplicate.Count -eq 0) { 'PASS' } else { 'BLOCKED' }) $duplicate.Count 0 'File paths must be unique case-insensitively.'

    $required = @($script:RuntimeFileNames | ForEach-Object { $_.ToLowerInvariant() })
    $listedLower = @($listed | ForEach-Object { $_.ToLowerInvariant() })
    $missingRuntime = @($required | Where-Object { $listedLower -notcontains $_ })
    $unexpectedTop = @($listed | Where-Object {
        $_ -notmatch '/' -and
        $required -notcontains $_.ToLowerInvariant() -and
        $_.ToLowerInvariant() -ne 'payload.dll' -and
        $_ -notmatch '(?i)^rebound[_-]?toolbox.*\.exe$'
    })
    $payloadListed = ($listedLower -contains 'payload.dll')
    $fullPlayerRuntime = ($missingRuntime.Count -eq 0)
    # The hardware-test ZIP intentionally does not ship the game executable or
    # a full player runtime. Payload.dll is mandatory; the other four files are
    # optional when Toolbox installs them through its managed catalog.
    # Support files may be at the package root in a full runtime package. The
    # manifest and sidecar are handled separately; legacy server/game/log
    # paths are rejected by Test-NativePackageContent below.
    $runtimeShapeOk = $payloadListed
    $runtimeNote = if ($fullPlayerRuntime) { 'Full managed player-file set is present at package root.' } else { 'Hardware-test package carries Payload.dll; remaining player files stay under Toolbox managed installation.' }
    $checks += New-NativeCheck 'manifest.runtime_files' $(if ($runtimeShapeOk) { 'PASS' } else { 'BLOCKED' }) ([ordered]@{ payload_present = $payloadListed; missing_optional_player_files = $missingRuntime; unexpected_top_level = $unexpectedTop }) 'Payload.dll plus an optional full five-file player runtime' $runtimeNote

    $entryMap = @{}
    foreach ($entry in @($PackageEntries)) { $entryMap[$entry.path.ToLowerInvariant()] = $entry }
    $allListedPresent = $true
    foreach ($path in $listed) {
        if (-not $entryMap.ContainsKey($path.ToLowerInvariant())) { $allListedPresent = $false }
    }
    $allPackageListed = $true
    foreach ($entry in @($PackageEntries)) {
        $name = $entry.path.ToLowerInvariant()
        if ($name -eq 'package-manifest.json' -or $name -eq 'sha256sums') { continue }
        if ($listedLower -notcontains $name) { $allPackageListed = $false }
    }
    $checks += New-NativeCheck 'manifest.files.complete' $(if ($allListedPresent -and $allPackageListed) { 'PASS' } else { 'BLOCKED' }) ([ordered]@{ listed = $listed.Count; package_entries = $PackageEntries.Count }) 'all package files except manifest/SHA256SUMS' 'Manifest must bind every distributable file.'

    $pinnedSha = if ($null -ne $Manifest.pinned_game) { ([string]$Manifest.pinned_game.sha256).ToLowerInvariant() } else { $null }
    $pinnedBytes = if ($null -ne $Manifest.pinned_game) { [int64]$Manifest.pinned_game.bytes } else { 0L }
    $pinnedOk = ($pinnedSha -eq $script:FixedGameSha256 -and $pinnedBytes -eq $script:FixedGameBytes)
    $checks += New-NativeCheck 'manifest.pinned_game' $(if ($pinnedOk) { 'PASS' } else { 'BLOCKED' }) ([ordered]@{ sha256 = $pinnedSha; bytes = $pinnedBytes }) ([ordered]@{ sha256 = $script:FixedGameSha256; bytes = $script:FixedGameBytes }) 'The package must target the reviewed fixed Boundary executable.'

    $commitOk = $false
    $commitObserved = @{}
    if ($null -ne $Manifest.source_commits) {
        foreach ($property in $Manifest.source_commits.PSObject.Properties) {
            $value = [string]$property.Value
            $commitObserved[$property.Name] = if ($value -match '^[0-9a-fA-F]{40}$') { 'present' } else { 'invalid' }
        }
        $commitOk = ($commitObserved.Count -ge 2 -and @($commitObserved.Values | Where-Object { $_ -ne 'present' }).Count -eq 0)
    }
    $checks += New-NativeCheck 'manifest.source_commits' $(if ($commitOk) { 'PASS' } else { 'BLOCKED' }) $commitObserved 'at least two 40-hex source commits' 'Source provenance is metadata only; it does not establish runtime admission.'
    return $checks
}

function Test-NativePackageContent {
    param(
        [Parameter(Mandatory = $true)][object]$Manifest,
        [Parameter(Mandatory = $true)][object[]]$PackageEntries
    )
    $checks = @()
    $expected = @{}
    foreach ($entry in @($Manifest.files)) {
        $path = Normalize-NativeRelativePath ([string]$entry.path)
        if ($null -eq $path) { continue }
        $expected[$path.ToLowerInvariant()] = [ordered]@{
            path = $path
            bytes = [int64]$entry.bytes
            sha256 = ([string]$entry.sha256).ToLowerInvariant()
        }
    }
    $checked = 0
    $mismatches = @()
    foreach ($entry in @($PackageEntries)) {
        $key = $entry.path.ToLowerInvariant()
        if ($key -eq 'package-manifest.json' -or $key -eq 'sha256sums') { continue }
        if (-not $expected.ContainsKey($key)) {
            $mismatches += [ordered]@{ path = $entry.path; reason = 'unlisted' }
            continue
        }
        $want = $expected[$key]
        if ([int64]$entry.bytes -ne [int64]$want.bytes -or ([string]$entry.sha256).ToLowerInvariant() -ne $want.sha256) {
            $mismatches += [ordered]@{ path = $entry.path; reason = 'bytes_or_sha256_mismatch' }
        }
        $checked++
    }
    foreach ($key in $expected.Keys) {
        if (-not @($PackageEntries | Where-Object { $_.path.ToLowerInvariant() -eq $key }).Count) {
            $mismatches += [ordered]@{ path = $expected[$key].path; reason = 'missing_from_package' }
        }
    }
    $checks += New-NativeCheck 'package.file_hashes' $(if ($mismatches.Count -eq 0) {'PASS'} else {'BLOCKED'}) ([ordered]@{ checked = $checked; mismatches = $mismatches }) 'manifest-listed file bytes and SHA-256' 'Every listed package byte is verified before any install step.'

    $forbidden = @($PackageEntries | Where-Object {
        $p = $_.path.ToLowerInvariant()
        $p -match '(^|/)(serverlauncher|boundarymetaserver-main|startgame\.ps1|start-local-pve\.ps1|projectboundary.*\.exe|clientlogs|saved|\.log$)'
    } | ForEach-Object { $_.path })
    $checks += New-NativeCheck 'package.no_legacy_or_runtime_extras' $(if ($forbidden.Count -eq 0) {'PASS'} else {'BLOCKED'}) $forbidden 'no ServerLauncher/legacy server/game executable/log paths' 'Legacy online compatibility and raw runtime logs are outside the distribution boundary.'
    return $checks
}

function Test-NativeSha256SumsFile {
    param(
        [Parameter(Mandatory = $true)][string]$PackagePath,
        [Parameter(Mandatory = $true)][object[]]$PackageEntries,
        [Parameter(Mandatory = $true)][string]$ManifestSha256
    )
    $text = Read-NativePackageFileText -PackagePath $PackagePath -RelativePath 'SHA256SUMS'
    if ([string]::IsNullOrWhiteSpace($text)) {
        return New-NativeCheck 'package.sha256sums' 'NOT_RUN' $null 'optional SHA256SUMS' 'Package has no SHA256SUMS sidecar; package-manifest.json remains authoritative.'
    }
    $rows = @{}
    foreach ($line in ($text -split "`r?`n")) {
        $trimmed = $line.Trim()
        if ([string]::IsNullOrWhiteSpace($trimmed)) { continue }
        if ($trimmed -notmatch '^([0-9a-fA-F]{64})\s+\*?(.+)$') {
            return New-NativeCheck 'package.sha256sums' 'BLOCKED' 'invalid_line' 'sha256 path lines' 'SHA256SUMS contains an invalid line.'
        }
        $path = Normalize-NativeRelativePath $Matches[2].Trim()
        if ($null -eq $path) { return New-NativeCheck 'package.sha256sums' 'BLOCKED' 'unsafe_path' 'relative POSIX path' 'SHA256SUMS contains an unsafe path.' }
        $rows[$path.ToLowerInvariant()] = $Matches[1].ToLowerInvariant()
    }
    $expected = @('package-manifest.json') + @($PackageEntries | Where-Object { $_.path.ToLowerInvariant() -ne 'sha256sums' } | ForEach-Object { $_.path })
    $expectedLower = @($expected | ForEach-Object { $_.ToLowerInvariant() } | Sort-Object -Unique)
    $actualLower = @($rows.Keys | Sort-Object -Unique)
    $difference = @(Compare-Object -ReferenceObject $expectedLower -DifferenceObject $actualLower)
    $shapeOk = ($difference.Count -eq 0)
    $hashOk = ($rows['package-manifest.json'] -eq $ManifestSha256.ToLowerInvariant())
    $check = New-NativeCheck 'package.sha256sums' $(if ($shapeOk -and $hashOk) { 'PASS' } else { 'BLOCKED' }) ([ordered]@{ entries = $rows.Count; manifest_sha256_matches = $hashOk }) ([ordered]@{ entries = $expectedLower.Count; manifest_sha256 = $ManifestSha256 }) 'Optional sidecar must bind the manifest and every package file; its own hash is excluded.'
    return $check
}

function Invoke-NativePackageChecks {
    param([Parameter(Mandatory = $true)][string]$PackagePath)
    $checks = @()
    if (-not (Test-Path -LiteralPath $PackagePath)) {
        return [pscustomobject]@{ checks = @(New-NativeCheck 'package.path' 'BLOCKED' (Get-NativeSafePath $PackagePath) 'existing package path' 'PackagePath does not exist.'); manifest = $null; entries = @(); kind = $null; manifest_sha256 = $null }
    }
    try {
        $item = Get-Item -LiteralPath $PackagePath -ErrorAction Stop
        $kind = if ($item.PSIsContainer) { 'directory' } else { 'zip' }
        $manifestInfo = Get-NativePackageManifest -PackagePath $item.FullName
        $entries = @(Get-NativePackageEntries -PackagePath $item.FullName)
        $checks += Test-NativeManifestEntryShape -Manifest $manifestInfo.object -PackageEntries $entries
        $checks += Test-NativePackageContent -Manifest $manifestInfo.object -PackageEntries $entries
        $manifestEntry = @($entries | Where-Object { $_.path.ToLowerInvariant() -eq 'package-manifest.json' })
        $manifestHash = $manifestInfo.sha256.ToLowerInvariant()
        if ($manifestEntry.Count -eq 1 -and $manifestEntry[0].sha256 -ne $manifestHash) {
            $checks += New-NativeCheck 'package.manifest.hash' 'BLOCKED' $manifestEntry[0].sha256 $manifestHash 'Manifest hash differs between package bytes and parsed text.'
        } else {
            $checks += New-NativeCheck 'package.manifest.hash' 'PASS' $manifestHash $manifestHash 'Manifest bytes are bound to the evidence.'
        }
        $checks += Test-NativeSha256SumsFile -PackagePath $item.FullName -PackageEntries $entries -ManifestSha256 $manifestHash
        $checks += New-NativeCheck 'package.reparse_points' $(if (@($entries | Where-Object {$_.reparse_point}).Count -eq 0) {'PASS'} else {'BLOCKED'}) (@($entries | Where-Object {$_.reparse_point}).Count) 0 'Reparse-point files are not accepted in the package.'
        return [pscustomobject]@{ checks = $checks; manifest = $manifestInfo.object; entries = $entries; kind = $kind; manifest_sha256 = $manifestHash }
    } catch {
        $checks += New-NativeCheck 'package.read' 'BLOCKED' $null 'readable package manifest' 'Package could not be read or validated.'
        return [pscustomobject]@{ checks = $checks; manifest = $null; entries = @(); kind = $null; manifest_sha256 = $null }
    }
}

function Find-NativeSteamRoot {
    param([AllowNull()][string]$GameExePath)
    $candidates = @()
    if (-not [string]::IsNullOrWhiteSpace($GameExePath)) {
        $bin = Split-Path -Parent $GameExePath
        $candidates += (Join-Path $bin '..\..\..\..\..')
    }
    try {
        foreach ($key in @('HKCU:\Software\Valve\Steam', 'HKLM:\SOFTWARE\WOW6432Node\Valve\Steam', 'HKLM:\SOFTWARE\Valve\Steam')) {
            $props = Get-ItemProperty -LiteralPath $key -ErrorAction SilentlyContinue
            if ($null -ne $props) {
                foreach ($name in @('SteamPath', 'InstallPath')) {
                    if ($props.PSObject.Properties.Name -contains $name -and -not [string]::IsNullOrWhiteSpace([string]$props.$name)) { $candidates += [string]$props.$name }
                }
            }
        }
    } catch { }
    foreach ($candidate in $candidates) {
        try {
            $resolved = (Resolve-Path -LiteralPath $candidate -ErrorAction Stop).Path
            if (Test-Path -LiteralPath (Join-Path $resolved 'steam.exe') -PathType Leaf) { return $resolved }
        } catch { }
    }
    return $null
}

function Get-NativeWebView2Summary {
    $versions = @()
    foreach ($base in @(
        (Join-Path ${env:ProgramFiles(x86)} 'Microsoft\EdgeWebView\Application'),
        (Join-Path $env:ProgramFiles 'Microsoft\EdgeWebView\Application')
    )) {
        if ([string]::IsNullOrWhiteSpace($base) -or -not (Test-Path -LiteralPath $base)) { continue }
        foreach ($exe in @(Get-ChildItem -LiteralPath $base -Filter 'msedgewebview2.exe' -File -Recurse -ErrorAction SilentlyContinue)) {
            $versions += [string]$exe.VersionInfo.ProductVersion
        }
    }
    foreach ($root in @(
        'HKLM:\SOFTWARE\Microsoft\EdgeUpdate\Clients',
        'HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients',
        'HKCU:\SOFTWARE\Microsoft\EdgeUpdate\Clients'
    )) {
        try {
            foreach ($key in @(Get-ChildItem -LiteralPath $root -ErrorAction SilentlyContinue)) {
                $props = Get-ItemProperty -LiteralPath $key.PSPath -ErrorAction SilentlyContinue
                if ($null -ne $props -and (([string]$props.name) -match '(?i)WebView2' -or ([string]$key.PSChildName) -match '(?i)WebView2')) {
                    if (-not [string]::IsNullOrWhiteSpace([string]$props.pv)) { $versions += [string]$props.pv }
                }
            }
        } catch { }
    }
    $versions = @($versions | Where-Object { $_ -match '^[0-9]+(\.[0-9]+){1,3}' } | Sort-Object -Unique)
    return [ordered]@{ present = ($versions.Count -gt 0); versions = $versions; count = $versions.Count }
}

function Get-NativeVcRuntimeSummary {
    param([AllowNull()][string]$MinimumVersion)
    $names = @('vcruntime140.dll', 'vcruntime140_1.dll', 'msvcp140.dll')
    $items = @()
    $missing = @()
    foreach ($name in $names) {
        $path = Join-Path $env:windir ('System32\' + $name)
        if (Test-Path -LiteralPath $path -PathType Leaf) {
            $i = Get-Item -LiteralPath $path
            $items += [ordered]@{ name = $name; bytes = [int64]$i.Length; file_version = [string]$i.VersionInfo.FileVersion }
        } else { $missing += $name }
    }
    $minimumDeclared = $false
    $minimum = $null
    try {
        if (-not [string]::IsNullOrWhiteSpace($MinimumVersion)) {
            $minimum = [version]$MinimumVersion
            $minimumDeclared = $true
        }
    } catch { $minimumDeclared = $false }
    $versionFailures = @()
    $belowMinimum = @()
    if ($minimumDeclared) {
        foreach ($item in $items) {
            try {
                $actual = [version]$item.file_version
                if ($actual -lt $minimum) { $belowMinimum += $item.name }
            } catch { $versionFailures += $item.name }
        }
    }
    return [ordered]@{
        required = $names
        present = $items
        missing = $missing
        complete = ($missing.Count -eq 0)
        minimum_version = if ($minimumDeclared) { $minimum.ToString() } else { $MinimumVersion }
        minimum_declared = $minimumDeclared
        below_minimum = $belowMinimum
        version_failures = $versionFailures
        compatible = ($minimumDeclared -and $missing.Count -eq 0 -and $belowMinimum.Count -eq 0 -and $versionFailures.Count -eq 0)
    }
}

function Get-NativeProcessSummary {
    param([Parameter(Mandatory = $true)][string]$GameExePath)
    $gameCount = 0
    try {
        foreach ($process in @(Get-CimInstance Win32_Process -ErrorAction Stop)) {
            if (-not [string]::IsNullOrWhiteSpace([string]$process.ExecutablePath)) {
                try {
                    if ((Resolve-Path -LiteralPath $process.ExecutablePath -ErrorAction Stop).Path -ieq (Resolve-Path -LiteralPath $GameExePath -ErrorAction Stop).Path) { $gameCount++ }
                } catch { }
            }
        }
    } catch {
        try {
            foreach ($process in @(Get-Process -ErrorAction Stop)) {
                if ($process.ProcessName -ieq 'ProjectBoundarySteam-Win64-Shipping') { $gameCount++ }
            }
        } catch { }
    }
    $steamCount = 0
    try { $steamCount = @(Get-Process -Name steam -ErrorAction SilentlyContinue).Count } catch { }
    return [ordered]@{ exact_game_process_count = $gameCount; steam_process_count = $steamCount }
}

function Invoke-NativeEnvironmentChecks {
    param(
        [AllowNull()][string]$GameExePath,
        [AllowNull()][string]$ToolboxExePath,
        [AllowNull()][string]$ExpectedToolboxSha256,
        [AllowNull()][string]$ExpectedPayloadSha256,
        [AllowNull()][string]$MinimumVcRuntimeVersion,
        [AllowNull()][string]$ExpectedSteamApiSha256 = $script:ExpectedSteamApiSha256
    )
    $checks = @()
    $os = [Environment]::OSVersion.Version
    $osOk = ([Environment]::Is64BitOperatingSystem -and $os.Major -ge 10)
    $checks += New-NativeCheck 'host.windows_x64' $(if ($osOk) {'PASS'} else {'BLOCKED'}) ([ordered]@{ is_64bit = [Environment]::Is64BitOperatingSystem; version = $os.ToString(); architecture = $env:PROCESSOR_ARCHITECTURE }) 'Windows 10/11 x64' 'Fixed Boundary build requires a 64-bit Windows host.'

    if ([string]::IsNullOrWhiteSpace($GameExePath)) {
        $checks += New-NativeCheck 'game.exe.input' 'NOT_RUN' $null 'explicit -GameExePath' 'No game path was supplied; this tool never guesses or launches the game.'
        return $checks
    }
    try { $gamePath = (Resolve-Path -LiteralPath $GameExePath -ErrorAction Stop).Path } catch { $checks += New-NativeCheck 'game.exe.path' 'BLOCKED' (Get-NativeSafePath $GameExePath) 'existing fixed game executable' 'Game executable path does not exist.'; return $checks }
    $game = Get-NativeFileDigest $gamePath
    $gameOk = ($game.sha256 -eq $script:FixedGameSha256 -and $game.bytes -eq $script:FixedGameBytes)
    $checks += New-NativeCheck 'game.exe.fixed_hash' $(if ($gameOk) {'PASS'} else {'BLOCKED'}) ([ordered]@{ bytes = $game.bytes; sha256 = $game.sha256 }) ([ordered]@{ bytes = $script:FixedGameBytes; sha256 = $script:FixedGameSha256 }) 'The running target must be the fixed reviewed Boundary image.'

    $gameBin = Split-Path -Parent $gamePath
    $payloadPath = Join-Path $gameBin 'Payload.dll'
    $manifestExpectedPayload = $null
    $checks += New-NativeCheck 'game.path.resolved' 'PASS' (Get-NativeSafePath $gamePath) 'resolved path' 'Read-only path resolution.'
    if (Test-Path -LiteralPath $payloadPath -PathType Leaf) {
        $payload = Get-NativeFileDigest $payloadPath
        $payloadHashOk = ([string]::IsNullOrWhiteSpace($ExpectedPayloadSha256) -or $payload.sha256 -eq $ExpectedPayloadSha256.ToLowerInvariant())
        $checks += New-NativeCheck 'game.payload.present' $(if ($payloadHashOk) {'PASS'} else {'BLOCKED'}) ([ordered]@{ bytes = $payload.bytes; sha256 = $payload.sha256 }) ([ordered]@{ sha256 = if ($ExpectedPayloadSha256) {$ExpectedPayloadSha256.ToLowerInvariant()} else {'package Payload SHA-256'} }) 'Installed Payload was read only; no replacement was attempted.'
        $manifestExpectedPayload = $payload.sha256
    } else {
        $checks += New-NativeCheck 'game.payload.present' 'BLOCKED' $null 'Payload.dll' 'Installed Payload.dll is missing.'
    }

    $projectRoot = Split-Path -Parent (Split-Path -Parent $gameBin)
    # Boundary's Steam runtime and Toolbox's Steam helper both load the
    # reviewed Steamv157 module from the sibling Engine tree. Do not make a
    # copied steam_api64.dll beside the game executable an alternate trust
    # root: a package must validate the module the real launch path uses.
    $steamApi = Join-Path (Split-Path -Parent $projectRoot) 'Engine\Binaries\ThirdParty\Steamworks\Steamv157\Win64\steam_api64.dll'
    if (Test-Path -LiteralPath $steamApi -PathType Leaf) {
        $steamDigest = Get-NativeFileDigest $steamApi
        $steamHashOk = ([string]::IsNullOrWhiteSpace($ExpectedSteamApiSha256) -or $steamDigest.sha256 -eq $ExpectedSteamApiSha256.ToLowerInvariant())
        $checks += New-NativeCheck 'steam.api64' $(if ($steamHashOk) {'PASS'} else {'BLOCKED'}) ([ordered]@{ path = $steamDigest.path; bytes = $steamDigest.bytes; sha256 = $steamDigest.sha256; signature = (Get-NativeSignatureSummary $steamApi) }) ([ordered]@{ path = 'Engine/Binaries/ThirdParty/Steamworks/Steamv157/Win64/steam_api64.dll'; bytes = $script:ExpectedSteamApiBytes; sha256 = $ExpectedSteamApiSha256.ToLowerInvariant() }) 'Steam API must exist in the fixed Engine Steamv157 tree and match the pinned Steam build when supplied.'
    } else {
        $checks += New-NativeCheck 'steam.api64' 'BLOCKED' $null 'Engine/Binaries/ThirdParty/Steamworks/Steamv157/Win64/steam_api64.dll' 'The fixed launch path cannot authenticate without its Engine Steam API module.'
    }
    $steamRoot = Find-NativeSteamRoot $gamePath
    if ($null -ne $steamRoot) {
        $steamExe = Join-Path $steamRoot 'steam.exe'
        $checks += New-NativeCheck 'steam.client' 'PASS' ([ordered]@{ present = $true; signature = (Get-NativeSignatureSummary $steamExe) }) 'Steam installed' 'Steam executable found without exposing registry configuration.'
    } else {
        $checks += New-NativeCheck 'steam.client' 'BLOCKED' ([ordered]@{ present = $false }) 'Steam installed' 'Steam installation was not found from the game path/registry.'
    }
    $processes = Get-NativeProcessSummary $gamePath
    $checks += New-NativeCheck 'steam.process' $(if ($processes.steam_process_count -gt 0) {'PASS'} else {'BLOCKED'}) ([ordered]@{ running = ($processes.steam_process_count -gt 0); count = $processes.steam_process_count }) 'running Steam process' 'This check never launches Steam.'
    $checks += New-NativeCheck 'game.process.absent' $(if ($processes.exact_game_process_count -eq 0) {'PASS'} else {'BLOCKED'}) ([ordered]@{ exact_process_count = $processes.exact_game_process_count }) 0 'No Boundary process may be running during installation or package verification; no process is terminated by this tool.'

    $webview = Get-NativeWebView2Summary
    $checks += New-NativeCheck 'webview2.runtime' $(if ($webview.present) {'PASS'} else {'BLOCKED'}) $webview 'installed WebView2 Evergreen runtime' 'Toolbox Tauri requires WebView2; no installer is run.'
    $vc = Get-NativeVcRuntimeSummary -MinimumVersion $MinimumVcRuntimeVersion
    $vcStatus = if (-not $vc.complete -or $vc.below_minimum.Count -gt 0 -or $vc.version_failures.Count -gt 0) { 'BLOCKED' } elseif (-not $vc.minimum_declared) { 'NOT_RUN' } else { 'PASS' }
    $checks += New-NativeCheck 'vc.runtime' $vcStatus $vc ([ordered]@{ files = $vc.required; minimum_version = $MinimumVcRuntimeVersion }) 'MSVC runtime DLLs and the manifest minimum version are checked read only.'

    if ([string]::IsNullOrWhiteSpace($ToolboxExePath)) {
        $checks += New-NativeCheck 'toolbox.exe' 'NOT_RUN' $null 'explicit -ToolboxExePath and expected SHA' 'Toolbox executable was not supplied; this native preflight never downloads or starts it.'
    } elseif (-not (Test-Path -LiteralPath $ToolboxExePath -PathType Leaf)) {
        $checks += New-NativeCheck 'toolbox.exe' 'BLOCKED' (Get-NativeSafePath $ToolboxExePath) 'existing Toolbox executable' 'Toolbox executable path does not exist.'
    } else {
        $toolbox = Get-NativeFileDigest $ToolboxExePath
        $toolboxHashOk = ([string]::IsNullOrWhiteSpace($ExpectedToolboxSha256) -or $toolbox.sha256 -eq $ExpectedToolboxSha256.ToLowerInvariant())
        $checks += New-NativeCheck 'toolbox.exe' $(if ($toolboxHashOk) {'PASS'} else {'BLOCKED'}) ([ordered]@{ bytes = $toolbox.bytes; sha256 = $toolbox.sha256; signature = (Get-NativeSignatureSummary $ToolboxExePath) }) ([ordered]@{ sha256 = if ($ExpectedToolboxSha256) {$ExpectedToolboxSha256.ToLowerInvariant()} else {'explicit expected hash not supplied'} }) 'Toolbox is measured only; this tool never starts it.'
    }
    return $checks
}

function Get-NativeOverallStatus {
    param([Parameter(Mandatory = $true)][object[]]$Checks)
    if (@($Checks | Where-Object { $_.status -eq 'ERROR' }).Count -gt 0) { return 'ERROR' }
    if (@($Checks | Where-Object { $_.status -eq 'BLOCKED' }).Count -gt 0) { return 'BLOCKED' }
    if (@($Checks | Where-Object { $_.status -eq 'NOT_RUN' }).Count -gt 0) { return 'NOT_RUN' }
    return 'PASS'
}

function New-NativePreflightEvidence {
    param(
        [Parameter(Mandatory = $true)][string]$PackagePath,
        [AllowNull()][string]$GameExePath,
        [AllowNull()][string]$ToolboxExePath,
        [AllowNull()][string]$ExpectedToolboxSha256,
        [AllowNull()][string]$ExpectedSteamApiSha256 = $script:ExpectedSteamApiSha256
    )
    $package = Invoke-NativePackageChecks -PackagePath $PackagePath
    $expectedPayload = $null
    $minimumVcRuntimeVersion = $null
    $listedFiles = @()
    if ($null -ne $package.manifest) {
        foreach ($entry in @($package.manifest.files)) {
            $path = Normalize-NativeRelativePath ([string]$entry.path)
            if ($null -eq $path) { continue }
            $hash = ([string]$entry.sha256).ToLowerInvariant()
            $listedFiles += [ordered]@{ path = $path; bytes = [int64]$entry.bytes; sha256 = $hash }
            if ($path -ieq 'Payload.dll') { $expectedPayload = $hash }
        }
        if ($null -ne $package.manifest.msvc_minimum_version) {
            $minimumVcRuntimeVersion = [string]$package.manifest.msvc_minimum_version
        }
    }
    $environment = Invoke-NativeEnvironmentChecks -GameExePath $GameExePath -ToolboxExePath $ToolboxExePath -ExpectedToolboxSha256 $ExpectedToolboxSha256 -ExpectedPayloadSha256 $expectedPayload -MinimumVcRuntimeVersion $minimumVcRuntimeVersion -ExpectedSteamApiSha256 $ExpectedSteamApiSha256
    $checks = @($package.checks) + @($environment)
    $preflightStatus = Get-NativeOverallStatus -Checks $checks
    $item = Get-Item -LiteralPath $PackagePath -ErrorAction SilentlyContinue
    $packagePathSafe = Get-NativeSafePath $PackagePath
    $sourceCommits = @{}
    if ($null -ne $package.manifest -and $null -ne $package.manifest.source_commits) {
        foreach ($property in $package.manifest.source_commits.PSObject.Properties) { $sourceCommits[$property.Name] = [string]$property.Value }
    }
    return [ordered]@{
        schema_version = 1
        tool = 'ProjectRebound.WindowsNativeDistribution'
        tool_version = '1.0'
        generated_at_utc = Get-NativeUtcNow
        mode = 'read_only_no_launch_no_install'
        status = $preflightStatus
        release_ready = $false
        package = [ordered]@{
            path = $packagePathSafe
            kind = $package.kind
            manifest_sha256 = $package.manifest_sha256
            files = $listedFiles
            source_commits = $sourceCommits
            msvc_minimum_version = $minimumVcRuntimeVersion
            pinned_game = if ($null -ne $package.manifest) { $package.manifest.pinned_game } else { $null }
        }
        game = [ordered]@{ supplied_path = Get-NativeSafePath $GameExePath; fixed_sha256 = $script:FixedGameSha256; fixed_bytes = $script:FixedGameBytes }
        toolbox = [ordered]@{ supplied_path = Get-NativeSafePath $ToolboxExePath; expected_sha256 = if ($ExpectedToolboxSha256) {$ExpectedToolboxSha256.ToLowerInvariant()} else {$null} }
        checks = $checks
        native_online_admission = [ordered]@{
            status = 'NOT_RUN'
            reason = 'This distribution preflight never launches Boundary, connects a Payload pipe, sends a grant, or changes strict/native_verified state.'
        }
        safety = [ordered]@{
            game_started = $false
            dll_replaced = $false
            named_pipe_written = $false
            credentials_collected = $false
            raw_logs_collected = $false
        }
    }
}

function Write-NativeEvidence {
    param(
        [Parameter(Mandatory = $true)][object]$Evidence,
        [AllowNull()][string]$OutputPath
    )
    $json = $Evidence | ConvertTo-Json -Depth 14
    if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
        $parent = Split-Path -Parent $OutputPath
        if (-not [string]::IsNullOrWhiteSpace($parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
        Set-Content -LiteralPath $OutputPath -Value $json -Encoding UTF8
    }
    Write-Output $json
}
