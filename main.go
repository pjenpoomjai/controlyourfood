package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/joho/godotenv"
	"github.com/line/line-bot-sdk-go/v7/linebot"
)

// ── Pending meal save state ───────────────────────────────────────────────────

type pendingMeal struct {
	Food     string
	Calories string
}

var (
	pendingMu   sync.Mutex
	pendingSave = make(map[string]*pendingMeal)
)

func setPending(userID string, meal *pendingMeal) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	pendingSave[userID] = meal
}

func getPending(userID string) *pendingMeal {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	return pendingSave[userID]
}

func clearPending(userID string) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	delete(pendingSave, userID)
}

// ── Global state ──────────────────────────────────────────────────────────────

var (
	bot           *linebot.Client
	knowledgeBase string
)

// ── Helpers ───────────────────────────────────────────────────────────────────

func replyText(replyToken, text string) {
	if _, err := bot.ReplyMessage(replyToken, linebot.NewTextMessage(text)).Do(); err != nil {
		log.Printf("reply error: %v", err)
	}
}

func getUserName(userID string) string {
	profile, err := bot.GetProfile(userID).Do()
	if err != nil {
		return userID
	}
	return profile.DisplayName
}

func containsAny(s string, keywords []string) bool {
	s = strings.ToLower(s)
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// ── Webhook handler ───────────────────────────────────────────────────────────

func webhookHandler(w http.ResponseWriter, r *http.Request) {
	events, err := bot.ParseRequest(r)
	if err != nil {
		if err == linebot.ErrInvalidSignature {
			w.WriteHeader(400)
		} else {
			w.WriteHeader(500)
		}
		return
	}

	for _, event := range events {
		if event.Type != linebot.EventTypeMessage {
			continue
		}

		userID := event.Source.UserID
		replyToken := event.ReplyToken

		switch msg := event.Message.(type) {
		case *linebot.TextMessage:
			handleText(userID, replyToken, msg.Text)
		case *linebot.ImageMessage:
			handleImage(userID, replyToken, msg.ID)
		}
	}

	w.WriteHeader(200)
	fmt.Fprint(w, "OK")
}

// ── Text message handler ──────────────────────────────────────────────────────

func handleText(userID, replyToken, text string) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)

	// Special commands
	if containsAny(lower, []string{"/clear", "clear history", "reset", "ล้างประวัติ"}) {
		ClearHistory(userID)
		clearPending(userID)
		replyText(replyToken, "🔄 ล้างประวัติการสนทนาแล้วค่ะ เริ่มต้นใหม่ได้เลยนะคะ 😊")
		return
	}

	if containsAny(lower, []string{"/history", "meal history", "my meals", "ประวัติมื้ออาหาร", "ดูประวัติ"}) {
		sm := GetSheets()
		records := sm.GetUserHistory(userID, 10)
		replyText(replyToken, sm.FormatHistory(records))
		return
	}

	if containsAny(lower, []string{"/help", "help", "commands", "ช่วยเหลือ", "วิธีใช้"}) {
		help := "🤖 น้องฟิต — ผู้ช่วยคุมอาหารของคุณ\n\n" +
			"📌 ฉันทำอะไรได้บ้าง:\n" +
			"• ส่งรูปอาหาร → ประเมินแคลอรี่\n" +
			"• ถามเรื่องโภชนาการ / การลดน้ำหนัก\n" +
			"• บอกว่ากินอะไร → บันทึกลง Google Sheets\n" +
			"• จำข้อมูลโปรไฟล์ข้ามเซสชัน\n\n" +
			"📌 คำสั่ง:\n" +
			"• 'ดูประวัติ' — ดูประวัติมื้ออาหารล่าสุด\n" +
			"• 'โปรไฟล์ของฉัน' — ดูข้อมูลที่บันทึกไว้\n" +
			"• 'อัปเดตข้อมูล' — แก้ไขข้อมูลส่วนตัว\n" +
			"• 'ล้างประวัติ' — ล้างประวัติการสนทนา\n" +
			"• 'ช่วยเหลือ' — แสดงเมนูนี้"
		replyText(replyToken, help)
		return
	}

	if containsAny(lower, []string{"my profile", "โปรไฟล์ของฉัน", "ข้อมูลของฉัน", "โปรไฟล์"}) {
		sm := GetSheets()
		p := sm.GetUserProfile(userID)
		msg := formatProfile(p)
		replyText(replyToken, msg)
		return
	}

	if containsAny(lower, []string{"update profile", "แก้ไขข้อมูล", "อัปเดตข้อมูล", "แก้ไขโปรไฟล์"}) {
		replyText(replyToken,
			"📝 มาอัปเดตข้อมูลกันเลยค่ะ! บอกฉันได้เลยว่า:\n\n"+
				"1. ชื่อ\n"+
				"2. น้ำหนัก (กก.)\n"+
				"3. ส่วนสูง (ซม.)\n"+
				"4. เป้าหมาย (ลดน้ำหนัก / รักษาน้ำหนัก / เพิ่มกล้ามเนื้อ)\n"+
				"5. เป้าหมายแคลอรี่ต่อวัน (ถ้าทราบ)\n"+
				"6. อาหารที่แพ้หรือข้อจำกัดด้านอาหาร\n\n"+
				"บอกทีเดียวหรือแค่บางส่วนที่ต้องการอัปเดตก็ได้เลยนะคะ 😊")
		return
	}

	// Check for pending meal confirmation
	if pending := getPending(userID); pending != nil {
		if containsAny(lower, []string{"save", "yes", "ok", "confirm", "log", "บันทึก", "ใช่", "ตกลง"}) {
			sm := GetSheets()
			username := getUserName(userID)
			ok := sm.LogMeal(userID, username, pending.Food, pending.Calories)
			clearPending(userID)
			if ok {
				replyText(replyToken, fmt.Sprintf(
					"✅ บันทึกแล้วค่ะ!\n🍽️ %s\n🔥 %s kcal",
					pending.Food, pending.Calories,
				))
			} else {
				replyText(replyToken, "⚠️ ขออภัยค่ะ บันทึกไม่สำเร็จ กรุณาลองใหม่อีกครั้งนะคะ")
			}
			return
		}
		if containsAny(lower, []string{"no", "cancel", "skip", "ไม่", "ยกเลิก", "ข้าม"}) {
			clearPending(userID)
			replyText(replyToken, "👌 ไม่เป็นไรค่ะ ไม่บันทึก มีอะไรให้ช่วยเพิ่มเติมไหมคะ?")
			return
		}
	}

	// Auto-extract and save profile info using AI (runs in background)
	go func() {
		extracted := ExtractProfileFromMessage(userID, trimmed)
		if extracted != nil {
			if extracted.Name == "" {
				extracted.Name = getUserName(userID)
			}
			GetSheets().SaveUserProfile(extracted)
			log.Printf("profile auto-saved for user: %s", userID)
		}
	}()

	// Send to Gemini
	reply := AskText(userID, trimmed, knowledgeBase)
	replyText(replyToken, reply)
}

// ── Image message handler ─────────────────────────────────────────────────────

func handleImage(userID, replyToken, messageID string) {
	content, err := bot.GetMessageContent(messageID).Do()
	if err != nil {
		log.Printf("failed to download image [%s]: %v", userID, err)
		replyText(replyToken, "ขออภัยค่ะ ไม่สามารถดาวน์โหลดรูปได้ กรุณาลองใหม่อีกครั้งนะคะ 🙏")
		return
	}
	defer content.Content.Close()

	imageBytes, err := io.ReadAll(content.Content)
	if err != nil {
		log.Printf("failed to read image [%s]: %v", userID, err)
		replyText(replyToken, "ขออภัยค่ะ ไม่สามารถประมวลผลรูปได้ กรุณาลองใหม่อีกครั้งนะคะ 🙏")
		return
	}

	reply, calories := AskImage(userID, imageBytes, "image/jpeg", knowledgeBase)

	if calories != "" {
		setPending(userID, &pendingMeal{Food: "Food from image", Calories: calories})
	}

	replyText(replyToken, reply)
}

// ── Profile helpers ───────────────────────────────────────────────────────────

func formatProfile(p *UserProfile) string {
	if p.Name == "" && p.Weight == "" && p.Goal == "" {
		return "📋 ยังไม่มีข้อมูลโปรไฟล์ค่ะ\n\nพิมพ์ 'อัปเดตข้อมูล' เพื่อบันทึกข้อมูลของคุณ แล้วฉันจะจำไว้ทุกครั้งเลยนะคะ 😊"
	}
	lines := "📋 โปรไฟล์ของคุณ\n\n"
	if p.Name != "" { lines += "👤 ชื่อ: " + p.Name + "\n" }
	if p.Weight != "" { lines += "⚖️ น้ำหนัก: " + p.Weight + " กก.\n" }
	if p.Height != "" { lines += "📏 ส่วนสูง: " + p.Height + " ซม.\n" }
	if p.Goal != "" { lines += "🎯 เป้าหมาย: " + p.Goal + "\n" }
	if p.DailyCalories != "" { lines += "🔥 แคลอรี่ต่อวัน: " + p.DailyCalories + " kcal\n" }
	if p.Restrictions != "" { lines += "🚫 ข้อจำกัดอาหาร: " + p.Restrictions + "\n" }
	if p.Notes != "" { lines += "📝 หมายเหตุ: " + p.Notes + "\n" }
	if p.UpdatedAt != "" { lines += "\n🕐 อัปเดตล่าสุด: " + p.UpdatedAt }
	return lines
}


// ── Groq test endpoint ────────────────────────────────────────────────────────

func testGroqHandler(w http.ResponseWriter, r *http.Request) {
	reply := AskText("test-user", "Say 'FitBot is ready!' in Thai.", "")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"text_model":   textModel,
		"vision_model": visionModel,
		"reply":        reply,
		"status":       "ok",
	})
}

// ── Health check ──────────────────────────────────────────────────────────────

func healthHandler(w http.ResponseWriter, r *http.Request) {
	sm := GetSheets()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "ok",
		"bot":              "FitBot",
		"knowledge_chars":  len(knowledgeBase),
		"sheets_connected": sm.IsConnected(),
	})
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, using environment variables")
	}

	// Initialize LINE bot client
	var err error
	bot, err = linebot.New(
		os.Getenv("LINE_CHANNEL_SECRET"),
		os.Getenv("LINE_CHANNEL_ACCESS_TOKEN"),
	)
	if err != nil {
		log.Fatalf("failed to create LINE bot client: %v", err)
	}

	// Load knowledge base documents
	log.Println("loading knowledge base documents...")
	knowledgeBase = LoadAllDocuments("knowledge")
	log.Printf("knowledge base loaded: %d chars", len(knowledgeBase))

	// Initialize Groq AI client
	InitGroq()

	// Initialize Google Sheets
	GetSheets()

	// Register HTTP routes
	http.HandleFunc("/webhook", webhookHandler)
	http.HandleFunc("/test-groq", testGroqHandler)
	http.HandleFunc("/", healthHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "5000"
	}

	log.Printf("🚀 FitBot is running on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
