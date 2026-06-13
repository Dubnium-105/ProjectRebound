# =============================================================================
# Cold Launch Automation — VSCode Terminal Compatible
# Usage: powershell -File tools/cold_launch.ps1
# =============================================================================
$ErrorActionPreference = "Continue"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$MetaDir = Split-Path -Parent $ScriptDir
$GameExe = "ProjectBoundarySteam-Win64-Shipping.exe"
$GamePath = "D:\steam\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64\$GameExe"
$GameDir = "D:\steam\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64"
$FridaScript = "$ScriptDir\cold_start_scan.js"
$LogDir = "$MetaDir\logs"
$Now = Get-Date -Format 'yyyyMMdd_HHmmss'
$LogFile = "$LogDir\cold_scan_$Now.log"

# Ensure log dir
New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

Write-Host "" -ForegroundColor Cyan
Write-Host "============================================" -ForegroundColor Cyan
Write-Host "  Cold Start Launcher" -ForegroundColor Cyan
Write-Host "============================================" -ForegroundColor Cyan
Write-Host ""

# --- Step 1: Kill existing processes ---
Write-Host "[0] Cleaning up old processes..." -ForegroundColor Gray
Get-Process -Name "node" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Get-Process -Name $GameExe.Replace('.exe','') -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2

# --- Step 2: Start metaserver ---
Write-Host "[1/4] Starting MetaServer..." -ForegroundColor Yellow
$MetaPsi = [System.Diagnostics.ProcessStartInfo]::new()
$MetaPsi.FileName = "node"
$MetaPsi.Arguments = "index.js"
$MetaPsi.WorkingDirectory = $MetaDir
$MetaPsi.UseShellExecute = $true
$MetaPsi.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Minimized
$MetaProc = [System.Diagnostics.Process]::Start($MetaPsi)

# --- Step 3: Start proxy ---
Write-Host "[2/4] Starting Proxy..." -ForegroundColor Yellow
Start-Sleep -Seconds 2
$ProxyPsi = [System.Diagnostics.ProcessStartInfo]::new()
$ProxyPsi.FileName = "node"
$ProxyPsi.Arguments = "proxy.js"
$ProxyPsi.WorkingDirectory = $MetaDir
$ProxyPsi.UseShellExecute = $true
$ProxyPsi.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Minimized
$ProxyProc = [System.Diagnostics.Process]::Start($ProxyPsi)
Write-Host "  MetaServer + Proxy launched." -ForegroundColor Green

# --- Step 4: Start game ---
Write-Host "[3/4] Launching game..." -ForegroundColor Yellow
$GamePsi = [System.Diagnostics.ProcessStartInfo]::new()
$GamePsi.FileName = $GamePath
$GamePsi.Arguments = "-LogicServerURL=http://127.0.0.1:8000"
$GamePsi.WorkingDirectory = $GameDir
$GamePsi.UseShellExecute = $true
$GameProc = [System.Diagnostics.Process]::Start($GamePsi)
Write-Host "  Game launched (PID: $($GameProc.Id))" -ForegroundColor Green

# Wait for game to load
Write-Host "  Waiting for game process to fully initialize..." -ForegroundColor Gray
$GamePid = $null
for ($i = 0; $i -lt 120; $i++) {
    Start-Sleep -Seconds 2
    try {
        $p = Get-Process -Name $GameExe.Replace('.exe','') -ErrorAction Stop | Select-Object -First 1
        if ($p -and $p.Modules.Count -gt 50) {
            $GamePid = $p.Id
            Write-Host "  Game ready (PID: $GamePid, modules: $($p.Modules.Count))" -ForegroundColor Green
            break
        }
    } catch {}
    if ($i % 15 -eq 0) { Write-Host "  Still waiting... ($($i*2)s)" -ForegroundColor Gray }
}

if (-not $GamePid) {
    Write-Host "  ERROR: Game not found or modules not loaded." -ForegroundColor Red
    Write-Host "  You can manually attach: frida -p <PID> -l `"$FridaScript`"" -ForegroundColor Yellow
    Read-Host "  Press Enter to exit"
    exit 1
}

# --- Step 5: Attach Frida ---
Write-Host "[4/4] Attaching Frida to PID $GamePid..." -ForegroundColor Yellow
Write-Host "  Script: $FridaScript" -ForegroundColor Gray
Write-Host "  Log:    $LogFile" -ForegroundColor Gray

$FridaPsi = [System.Diagnostics.ProcessStartInfo]::new()
$FridaPsi.FileName = "frida"
$FridaPsi.Arguments = "-p $GamePid -l `"$FridaScript`""
$FridaPsi.WorkingDirectory = $ScriptDir
$FridaPsi.UseShellExecute = $true
$FridaPsi.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Normal
$FridaProc = [System.Diagnostics.Process]::Start($FridaPsi)

Write-Host ""
Write-Host "============================================" -ForegroundColor Cyan
Write-Host "  ALL SYSTEMS GO" -ForegroundColor Cyan
Write-Host "============================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "  Frida window: watch for check function hits" -ForegroundColor White
Write-Host "  Game: enter armory to trigger loadout RPCs" -ForegroundColor Yellow
Write-Host ""
Write-Host "  Press Enter to STOP all processes" -ForegroundColor Red
Read-Host

# Cleanup
Write-Host "Shutting down..." -ForegroundColor Gray
Get-Process -Name $GameExe.Replace('.exe','') -ErrorAction SilentlyContinue | Stop-Process -Force
Get-Process -Name "node" -ErrorAction SilentlyContinue | Stop-Process -Force
Get-Process -Name "frida" -ErrorAction SilentlyContinue | Stop-Process -Force
Write-Host "Done." -ForegroundColor Gray
