# Launch game only (no Frida) to test stability
$GamePath = "D:\steam\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64\ProjectBoundarySteam-Win64-Shipping.exe"
$GameDir = "D:\steam\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64"

# Start metaserver
$MetaDir = "g:\wksp\boundaries\ProjectRebound\Metaserver"
Start-Process -FilePath "node" -ArgumentList "index.js" -WorkingDirectory $MetaDir -WindowStyle Minimized
Start-Sleep -Seconds 1
Start-Process -FilePath "node" -ArgumentList "proxy.js" -WorkingDirectory $MetaDir -WindowStyle Minimized
Start-Sleep -Seconds 2

Write-Host "Starting game..." -ForegroundColor Yellow
$psi = [System.Diagnostics.ProcessStartInfo]::new()
$psi.FileName = $GamePath
$psi.Arguments = "-LogicServerURL=http://127.0.0.1:8000"
$psi.WorkingDirectory = $GameDir
$psi.UseShellExecute = $true
$proc = [System.Diagnostics.Process]::Start($psi)
Write-Host "Game PID: $($proc.Id)" -ForegroundColor Green
Write-Host "Wait for game to load, then enter armory." -ForegroundColor Yellow
Write-Host "If it crashes, the problem is NOT Frida." -ForegroundColor Red
Write-Host "Press Enter to kill game." -ForegroundColor Gray
Read-Host
Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
Write-Host "Done."
