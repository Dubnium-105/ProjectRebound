@echo off
setlocal
title Boundary Proxy + MetaServer
cd /d "%~dp0"

echo ========================================
echo   Boundary TCP Proxy + MetaServer
echo ========================================
echo.

where node >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    echo [ERROR] Node.js not found. Please install Node.js from https://nodejs.org/
    pause
    exit /b 1
)

if not exist "node_modules\" (
    echo [INFO] Installing dependencies...
    call npm install
    if %ERRORLEVEL% NEQ 0 (
        echo [ERROR] npm install failed.
        pause
        exit /b 1
    )
    echo.
)

echo [INFO] Starting MetaServer on port 6968 in a new window...
start "Boundary-MetaServer" cmd /c "cd /d %~dp0 && node index.js"
echo [INFO] Waiting for MetaServer to start...
timeout /t 3 >nul

echo [INFO] Starting TCP Proxy on port 6969...
echo.
echo   Game Client  --TCP-->  Proxy (:6969)  --TCP-->  MetaServer (:6968)
echo.
echo   HTTP API:  http://127.0.0.1:8000
echo   TCP Proxy: 127.0.0.1:6969
echo   UDP QoS:   127.0.0.1:9000
echo   Logs:      %~dp0logs\
echo.
echo Press Ctrl+C to stop the proxy.
echo (Close the MetaServer window separately when done.)
echo ========================================
echo.

node proxy.js

pause
