@echo off
echo === Google Sheets Debug Tool ===
echo.
cd /d D:\controlYourFood\controlYourFood\debug
go mod tidy
go run main.go
echo.
pause
