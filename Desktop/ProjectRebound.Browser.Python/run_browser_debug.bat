@echo off
setlocal
cd /d "%~dp0"
set PR_LAUNCH_MODE=debug
python project_rebound_browser.py
