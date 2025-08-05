@echo off
REM LIBERTARIAN RSS - Ultra Quick Development
REM Un solo comando para todo

echo 🚀 LIBERTARIAN RSS - Quick Start
echo.

REM Matar procesos anteriores
taskkill /F /IM main.exe 2>nul

REM Build rápido
echo 🔨 Building...
go build -o main.exe main.go
if %errorlevel% neq 0 (
    echo ❌ Build failed
    pause
    exit /b 1
)

REM Launch
echo 🚀 Starting server...
start /b main.exe

REM Wait y check
timeout /t 3 /nobreak >nul

REM Check if running
tasklist /FI "IMAGENAME eq main.exe" 2>nul | find /I /N "main.exe">nul
if %errorlevel% equ 0 (
    echo ✅ Server running at http://localhost:8080
    echo 📊 GZIP enabled, 30 feeds max, 15min cache
    echo.
    echo 💡 Press Ctrl+C to stop or close this window
    echo 📜 Server logs:
    echo.
) else (
    echo ❌ Failed to start server
    pause
    exit /b 1
)

REM Keep window open to show it's running
:loop
timeout /t 5 /nobreak >nul
tasklist /FI "IMAGENAME eq main.exe" 2>nul | find /I /N "main.exe">nul
if %errorlevel% neq 0 (
    echo ⚠️ Server stopped
    pause
    exit /b 0
)
goto loop
