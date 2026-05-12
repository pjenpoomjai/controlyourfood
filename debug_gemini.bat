@echo off
echo === Gemini API Debug Tool ===
echo.
cd /d D:\controlYourFood\controlYourFood\debug
go mod tidy
go run gemini_test.go
echo.
pause
