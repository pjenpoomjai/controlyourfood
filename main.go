package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/line/line-bot-sdk-go/v7/linebot"
)

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
		case *linebot.AudioMessage:
			handleAudio(userID, replyToken, msg.ID)
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

	// Send to AI and reply
	reply := AskText(userID, trimmed, knowledgeBase)
	replyText(replyToken, reply)

	// Background: update profile + detect meal + learn from conversation
	go func() {
		sm := GetSheets()

		// Save profile (new info or re-save existing)
		extracted := ExtractProfileFromMessage(userID, trimmed)
		if extracted != nil {
			if extracted.Name == "" {
				extracted.Name = getUserName(userID)
			}
			sm.SaveUserProfile(extracted)
		} else {
			existing := sm.GetUserProfile(userID)
			if existing.Name == "" {
				existing.Name = getUserName(userID)
			}
			if existing.Name != "" || existing.Weight != "" || existing.Goal != "" {
				sm.SaveUserProfile(existing)
			}
		}

		// Auto-detect and save meal
		food, calories := DetectMealFromText(trimmed)
		if food != "" {
			username := getUserName(userID)
			sm.LogMeal(userID, username, food, calories)
			log.Printf("meal auto-logged for %s: %s (%s kcal)", userID, food, calories)
		}

		// Extract and save new knowledge from this Q&A pair
		topic, knowledge := ExtractAndLearn(trimmed, reply)
		if topic != "" {
			sm.SaveLearned(topic, knowledge, userID)
			log.Printf("learned: [%s] %s", topic, knowledge)
		}
	}()
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

	// Auto-save meal immediately — no confirmation needed (survives server restarts)
	if calories != "" {
		username := getUserName(userID)
		sm := GetSheets()
		if sm.LogMeal(userID, username, "อาหารจากรูปภาพ", calories) {
			reply += "\n\n✅ บันทึกมื้อนี้แล้วค่ะ!"
		} else {
			reply += "\n\n⚠️ บันทึกไม่สำเร็จ กรุณาลองส่งรูปใหม่อีกครั้งนะคะ"
		}
	}

	replyText(replyToken, reply)
}

// ── Audio message handler ─────────────────────────────────────────────────────

func handleAudio(userID, replyToken, messageID string) {
	content, err := bot.GetMessageContent(messageID).Do()
	if err != nil {
		log.Printf("failed to download audio [%s]: %v", userID, err)
		replyText(replyToken, "ขออภัยค่ะ ไม่สามารถดาวน์โหลดเสียงได้ 🙏")
		return
	}
	defer content.Content.Close()

	audioBytes, err := io.ReadAll(content.Content)
	if err != nil {
		log.Printf("failed to read audio [%s]: %v", userID, err)
		replyText(replyToken, "ขออภัยค่ะ ไม่สามารถประมวลผลเสียงได้ 🙏")
		return
	}

	// Transcribe audio → text via Groq Whisper
	text, err := TranscribeAudio(audioBytes)
	if err != nil {
		log.Printf("transcription error [%s]: %v", userID, err)
		replyText(replyToken, "ขออภัยค่ะ ถอดเสียงไม่สำเร็จ ลองพิมพ์แทนได้เลยนะคะ 🙏")
		return
	}
	if text == "" {
		replyText(replyToken, "ขออภัยค่ะ ไม่ได้ยินเสียงชัดเจน กรุณาลองใหม่อีกครั้งนะคะ 🎙️")
		return
	}

	log.Printf("audio transcribed [%s]: %s", userID, text)

	// Process transcribed text exactly like a normal text message
	// Prepend the transcript so user knows what was heard
	reply := AskText(userID, text, knowledgeBase)
	replyText(replyToken, "🎙️ ได้ยิน: "+text+"\n\n"+reply)
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

	// Initialize Google Sheets + load accumulated learned knowledge
	sm := GetSheets()
	if learned := sm.LoadLearnedKnowledge(); learned != "" {
		knowledgeBase += "\n\n" + learned
		log.Printf("loaded learned knowledge from Sheets: %d entries", strings.Count(learned, "\n-"))
	}

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
