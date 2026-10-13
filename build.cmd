@echo off
rem Runs build.ps1 without needing to change the PowerShell execution policy.
rem   build.cmd          all platforms
rem   build.cmd host     only the packer for this machine
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0build.ps1" %*
exit /b %ERRORLEVEL%
