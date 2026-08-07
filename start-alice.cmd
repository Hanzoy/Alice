@echo off
cd /d "%~dp0"
echo Alice Core: http://localhost:8080
start "" "http://localhost:8080"
alice.exe -addr :8080 -data "%~dp0data" -components "%~dp0components"
pause

