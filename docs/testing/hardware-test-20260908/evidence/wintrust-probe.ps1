param(
    [Parameter(Mandatory = $true)][string]$SignedToolbox,
    [Parameter(Mandatory = $true)][string]$SignedPayload,
    [Parameter(Mandatory = $true)][string]$UnsignedPayload
)
$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class DistributionWinTrustProbe {
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)] private struct FileInfo { public uint cbStruct; public IntPtr path; public IntPtr hFile; public IntPtr subject; }
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)] private struct Data { public uint cbStruct; public IntPtr callback; public IntPtr sip; public uint ui; public uint revocation; public uint unionChoice; public IntPtr file; public uint stateAction; public IntPtr state; public IntPtr url; public uint flags; public uint context; }
    [DllImport("wintrust.dll", CharSet = CharSet.Unicode, SetLastError = true)] private static extern int WinVerifyTrust(IntPtr hwnd, ref Guid action, ref Data data);
    public static int Verify(string path) { IntPtr pathPtr = Marshal.StringToCoTaskMemUni(path); IntPtr filePtr = IntPtr.Zero; try { var file = new FileInfo { cbStruct = (uint)Marshal.SizeOf<FileInfo>(), path = pathPtr }; filePtr = Marshal.AllocCoTaskMem(Marshal.SizeOf<FileInfo>()); Marshal.StructureToPtr(file, filePtr, false); var data = new Data { cbStruct = (uint)Marshal.SizeOf<Data>(), ui = 2, unionChoice = 1, file = filePtr, stateAction = 1 }; Guid action = new Guid("00AAC56B-CD44-11d0-8CC2-00C04FC295EE"); return WinVerifyTrust(IntPtr.Zero, ref action, ref data); } finally { if (filePtr != IntPtr.Zero) Marshal.FreeCoTaskMem(filePtr); Marshal.FreeCoTaskMem(pathPtr); } }
}
"@
$trustedRoot = -2146762487
function Record([string]$Path) {
    $h = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    $hr = [DistributionWinTrustProbe]::Verify($Path)
    [ordered]@{ path = $Path; sha256 = $h; winverifytrust_hresult = $hr; accepted_by_gate = ($hr -eq 0 -or $hr -eq $trustedRoot) }
}
@(
    (Record $SignedToolbox)
    (Record $SignedPayload)
    (Record $UnsignedPayload)
) | ConvertTo-Json -Depth 4
