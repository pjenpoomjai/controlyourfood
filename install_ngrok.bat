@echo off
echo ====================================
echo    ติดตั้ง ngrok สำหรับ tunnel
echo ====================================
echo.

echo กำลังดาวน์โหลด ngrok...
curl -o ngrok.zip https://bin.equinox.io/c/bNyj1mQVY4c/ngrok-v3-stable-windows-amd64.zip

echo แตกไฟล์...
tar -xf ngrok.zip

echo ลบไฟล์ zip...
del ngrok.zip

echo.
echo เสร็จแล้ว! เปิด tunnel ด้วยคำสั่ง:
echo ngrok http 5000
echo.
pause
