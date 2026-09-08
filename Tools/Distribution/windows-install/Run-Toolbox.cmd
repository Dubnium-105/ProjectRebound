@echo off
setlocal
set "APP=%~dp0rebound_toolbox_tauri.exe"
if not exist "%APP%" (
  echo Missing rebound_toolbox_tauri.exe in this directory.
  exit /b 2
)
start "ProjectRebound ToolBox" "%APP%"
exit /b 0
