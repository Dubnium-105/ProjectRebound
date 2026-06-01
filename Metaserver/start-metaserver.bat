@echo off
setlocal
title Project Rebound MetaServer
cd /d "%~dp0"

echo ========================================
echo   Project Rebound MetaServer Launcher
echo ========================================
echo.

where node >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    echo [ERROR] Node.js not found. Please install Node.js from https://nodejs.org/
    pause
    exit /b 1
)

echo Node.js version:
node -v
echo.

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

echo [INFO] Checking port 6968...
set "PID="
for /f "tokens=5" %%a in ('netstat -ano ^| findstr /r /c:":6968 "') do set "PID=%%a"
if not "%PID%"=="" (
    echo [WARN] Port 6968 is in use by PID %PID%.
    echo [INFO] Terminating PID %PID%...
    taskkill /PID %PID% /F >nul 2>&1
    timeout /t 2 >nul
)

echo [INFO] Starting MetaServer...
echo   HTTP API:  http://127.0.0.1:8000
echo   TCP Game:  127.0.0.1:6968
echo   UDP QoS:   127.0.0.1:9000
echo.
echo Press Ctrl+C to stop the server.
echo ========================================
echo.

node index.js

pause
