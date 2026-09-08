param(
    [Parameter(Mandatory = $true)][string]$InputPath,
    [Parameter(Mandatory = $true)][string]$OutputPath,
    [Parameter(Mandatory = $true)][ValidatePattern('^[a-fA-F0-9]{64}$')][string]$ExpectedInputSha256,
    [Parameter(Mandatory = $true)][ValidatePattern('^[a-fA-F0-9]{40}$')][string]$CertificateThumbprint,
    [Parameter(Mandatory = $true)][string]$ReceiptPath
)

$ErrorActionPreference = 'Stop'
$inputFile = (Resolve-Path -LiteralPath $InputPath).Path
$outputFile = [IO.Path]::GetFullPath($OutputPath)
$receiptFile = [IO.Path]::GetFullPath($ReceiptPath)
$started = [DateTime]::UtcNow.ToString('o')
if ([IO.Path]::GetExtension($inputFile).ToLowerInvariant() -notin @('.exe', '.dll')) { throw 'Only PE artifacts may be signed.' }
if ((Get-FileHash -LiteralPath $inputFile -Algorithm SHA256).Hash -ine $ExpectedInputSha256) { throw 'Unsigned input hash mismatch.' }
if ((Test-Path -LiteralPath $outputFile) -or (Test-Path -LiteralPath $receiptFile)) { throw 'Refuse to overwrite an earlier artifact or receipt.' }
$certificate = Get-Item -LiteralPath ('Cert:\CurrentUser\My\' + $CertificateThumbprint)
if (-not $certificate.HasPrivateKey) { throw 'Signing certificate has no accessible private key.' }
if ($certificate.NotBefore.ToUniversalTime() -gt [DateTime]::UtcNow -or $certificate.NotAfter.ToUniversalTime() -le [DateTime]::UtcNow) { throw 'Signing certificate is not currently valid.' }
if (@($certificate.EnhancedKeyUsageList | Where-Object { $_.ObjectId -eq '1.3.6.1.5.5.7.3.3' }).Count -eq 0) { throw 'Certificate does not permit code signing.' }

# Use the same WinVerifyTrust flags as Toolbox security/integrity.rs. A valid
# self-signed test certificate is recorded as untrusted; no root is installed.
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class ReboundTestWinTrust {
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct FileInfo {
        public uint cbStruct;
        public IntPtr filePath;
        public IntPtr hFile;
        public IntPtr knownSubject;
    }
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct TrustData {
        public uint cbStruct;
        public IntPtr policyCallbackData;
        public IntPtr sipClientData;
        public uint uiChoice;
        public uint revocationChecks;
        public uint unionChoice;
        public IntPtr fileInfo;
        public uint stateAction;
        public IntPtr stateData;
        public IntPtr urlReference;
        public uint providerFlags;
        public uint uiContext;
        public IntPtr signatureSettings;
    }
    [DllImport("wintrust.dll", ExactSpelling = true)]
    private static extern int WinVerifyTrust(IntPtr hwnd, ref Guid action, ref TrustData data);
    public static int Verify(string path) {
        IntPtr text = Marshal.StringToCoTaskMemUni(path);
        IntPtr file = IntPtr.Zero;
        var action = new Guid("00AAC56B-CD44-11D0-8CC2-00C04FC295EE");
        var data = new TrustData();
        try {
            var info = new FileInfo { cbStruct=(uint)Marshal.SizeOf(typeof(FileInfo)), filePath=text };
            file = Marshal.AllocHGlobal(Marshal.SizeOf(typeof(FileInfo)));
            Marshal.StructureToPtr(info,file,false);
            data.cbStruct=(uint)Marshal.SizeOf(typeof(TrustData));
            data.uiChoice=2;
            data.unionChoice=1;
            data.fileInfo=file;
            data.stateAction=1;
            return WinVerifyTrust(IntPtr.Zero,ref action,ref data);
        } finally {
            if (data.stateData != IntPtr.Zero) {
                data.stateAction=2;
                WinVerifyTrust(IntPtr.Zero,ref action,ref data);
            }
            if (file != IntPtr.Zero) Marshal.FreeHGlobal(file);
            Marshal.FreeCoTaskMem(text);
        }
    }
}
'@

[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($outputFile)) | Out-Null
[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($receiptFile)) | Out-Null
$status = 'FAILED'
$failureType = $null
$signature = $null
$winTrust = $null
try {
    Copy-Item -LiteralPath $inputFile -Destination $outputFile
    $signature = Set-AuthenticodeSignature -LiteralPath $outputFile -Certificate $certificate -HashAlgorithm SHA256
    $observed = Get-AuthenticodeSignature -LiteralPath $outputFile
    if ($null -eq $observed.SignerCertificate -or $observed.SignerCertificate.Thumbprint -ine $CertificateThumbprint) { throw 'Signed PE does not contain the expected test certificate.' }
    $winTrust = [ReboundTestWinTrust]::Verify($outputFile)
    if ($winTrust -notin @(0, -2146762487)) { throw ('WinVerifyTrust rejected signed bytes: ' + $winTrust) }
    if ((Get-FileHash -LiteralPath $inputFile -Algorithm SHA256).Hash -ine $ExpectedInputSha256) { throw 'Unsigned input changed while signing.' }
    $status = 'PASS_TEST_SIGNATURE_ONLY'
} catch {
    $failureType = $_.Exception.GetType().FullName
    throw
} finally {
    $receipt = [ordered]@{
        schema_version = 1
        status = $status
        started_at = $started
        finished_at = [DateTime]::UtcNow.ToString('o')
        input_path = $inputFile
        input_sha256 = $ExpectedInputSha256.ToLowerInvariant()
        output_path = $outputFile
        output_sha256 = $(if (Test-Path -LiteralPath $outputFile) { (Get-FileHash -LiteralPath $outputFile -Algorithm SHA256).Hash.ToLowerInvariant() } else { $null })
        output_bytes = $(if (Test-Path -LiteralPath $outputFile) { (Get-Item -LiteralPath $outputFile).Length } else { $null })
        signer_subject = $certificate.Subject
        signer_thumbprint = $certificate.Thumbprint
        signer_valid_until = $certificate.NotAfter.ToUniversalTime().ToString('o')
        authenticode_status = $(if ($null -ne $signature) { [string]$signature.Status } else { $null })
        win_verify_trust_hresult = $winTrust
        private_key_exported = $false
        trust_store_modified = $false
        timestamping = 'NOT_RUN'
        release_ready = $false
        failure_type = $failureType
        script_sha256 = (Get-FileHash -LiteralPath $PSCommandPath -Algorithm SHA256).Hash.ToLowerInvariant()
        scope = 'Actual test Authenticode signing and signature verification only; no native gameplay, production signing, updater publication, or full release acceptance.'
    }
    $receipt | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $receiptFile -Encoding UTF8
    $receipt | ConvertTo-Json -Depth 5
}
