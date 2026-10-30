@echo off
rem ESP32 rules API - one click build and start (delegates to the PS script)
rem Usage: double click this file, or run: start.cmd -Port 9090 -Background
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\start.ps1" %*
set RC=%ERRORLEVEL%
if not "%RC%"=="0" (
    echo.
    echo [FAILED] exit code %RC%
    pause
)
exit /b %RC%
