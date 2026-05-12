@echo off
echo ====================================
echo    FitBot - LINE Diet Assistant (Go)
echo ====================================
echo.

cd /d D:\controlYourFood\controlYourFood

echo [1/3] Downloading dependencies...
go mod tidy

echo.
echo [2/3] Building...
go build -o bot.exe .
if errorlevel 1 (
    echo Build failed! See errors above.
    pause
    exit /b 1
)

echo.
echo [3/3] Starting bot...
echo Bot is running at http://localhost:5000
echo Press Ctrl+C to stop.
echo.
bot.exe

pause
