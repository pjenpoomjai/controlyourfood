# 🥗 น้องฟิต — LINE Chatbot คุมอาหาร

LINE Chatbot สำหรับติดตามการกินอาหาร วิเคราะห์แคลอรี่จากรูปภาพ และให้คำแนะนำโภชนาการ

---

## ✨ ฟีเจอร์

| ฟีเจอร์ | คำอธิบาย |
|---|---|
| 💬 ถาม-ตอบ | ถามเรื่องโภชนาการ การลดน้ำหนัก การกินอาหาร |
| 📸 วิเคราะห์รูป | ส่งรูปอาหาร → ประเมินแคลอรี่ + สารอาหาร |
| 📋 บันทึกประวัติ | บันทึกมื้ออาหารลง Google Sheets อัตโนมัติ |
| 📚 Knowledge Base | ใช้เอกสาร PDF/TXT ของคุณเป็นฐานความรู้ |

---

## 🚀 ขั้นตอน Setup

### 1. สร้าง LINE Bot

1. ไปที่ [LINE Developers Console](https://developers.line.biz/)
2. สร้าง Provider และ Channel ประเภท **Messaging API**
3. เปิดใช้ **Webhooks** และปิด **Auto-reply messages**
4. คัดลอก **Channel Secret** และ **Channel Access Token**

### 2. ขอ Anthropic API Key

1. ไปที่ [console.anthropic.com](https://console.anthropic.com/)
2. สร้าง API Key

### 3. ตั้งค่า Google Sheets (ถ้าต้องการบันทึกประวัติ)

1. ไปที่ [Google Cloud Console](https://console.cloud.google.com/)
2. สร้าง Project → เปิดใช้ **Google Sheets API** และ **Google Drive API**
3. สร้าง **Service Account** → ดาวน์โหลด JSON credentials
4. สร้าง Google Sheet ใหม่ → Share ให้ email ของ Service Account (Editor)
5. คัดลอก Spreadsheet ID จาก URL

### 4. เพิ่มเอกสารความรู้

วางไฟล์ PDF หรือ .txt ในโฟลเดอร์ `knowledge/`
ระบบจะโหลดอัตโนมัติทุกครั้งที่ bot เริ่มทำงาน

### 5. ตั้งค่า Environment Variables

```bash
cp .env.example .env
# แก้ไขค่าใน .env ให้ครบ
```

### 6. Deploy บน Render

1. Push โค้ดขึ้น GitHub
2. ไปที่ [render.com](https://render.com/) → New Web Service → เชื่อม repo
3. Render จะใช้ `render.yaml` อัตโนมัติ
4. ตั้งค่า Environment Variables ใน Render Dashboard
5. อัปโหลดไฟล์ `google_credentials.json` ผ่าน Render Shell (ถ้าใช้ Google Sheets)

### 7. ตั้งค่า LINE Webhook URL

1. Copy URL จาก Render (เช่น `https://control-your-food-bot.onrender.com`)
2. ไปที่ LINE Developers Console → Messaging API → Webhook URL
3. ใส่ `https://your-render-url.onrender.com/webhook`
4. กด **Verify**

---

## 💬 คำสั่งใน LINE

| คำสั่ง | ผล |
|---|---|
| ส่งข้อความปกติ | ถาม-ตอบเรื่องโภชนาการ |
| ส่งรูปอาหาร | วิเคราะห์แคลอรี่ |
| "บันทึก" | บันทึกมื้อล่าสุดลง Google Sheets |
| "ดูประวัติ" | ดูประวัติการกิน 10 มื้อล่าสุด |
| "เริ่มใหม่" | ล้างประวัติการสนทนา |
| "วิธีใช้" | ดูคำสั่งทั้งหมด |

---

## 🧪 ทดสอบในเครื่อง

```bash
pip install -r requirements.txt
cp .env.example .env  # แก้ค่าใน .env
python main.py
```

เปิด tunnel ด้วย [ngrok](https://ngrok.com/):
```bash
ngrok http 5000
# นำ URL ที่ได้ไปใส่เป็น LINE Webhook URL
```

---

## 📁 โครงสร้างไฟล์

```
controlYourFood/
├── main.py                 # LINE Webhook handler หลัก
├── ai_handler.py           # Claude AI integration
├── sheets_manager.py       # Google Sheets integration
├── document_loader.py      # โหลด PDF/TXT knowledge base
├── requirements.txt        # Python dependencies
├── render.yaml             # Render deployment config
├── .env.example            # ตัวอย่าง environment variables
├── google_credentials.json # Google Service Account (ไม่ commit!)
└── knowledge/              # วางเอกสาร PDF/TXT ที่นี่
    └── (วางไฟล์ PDF หรือ .txt ของคุณ)
```

> ⚠️ อย่า commit `google_credentials.json` และ `.env` ขึ้น GitHub เด็ดขาด!
> เพิ่มในไฟล์ `.gitignore` ก่อนเสมอ
