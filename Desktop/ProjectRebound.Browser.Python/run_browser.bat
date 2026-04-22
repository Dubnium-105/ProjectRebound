@echo off
setlocal
cd /d "%~dp0"
set PR_LAUNCH_MODE=quiet
where pythonw >nul 2>nul
if %errorlevel%==0 (
	start "" /B pythonw project_rebound_browser.py
) else (
	start "" /B python project_rebound_browser.py
)
