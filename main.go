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
	if containsAny(lower, []string{"/clear", "clear history", "reset"}) {
		ClearHistory(userID)
		clearPending(userID)
		replyText(replyToken, "🔄 Conversation history cleared! Feel free to start fresh.")
		return
	}

	if containsAny(lower, []string{"/history", "meal history", "my meals"}) {
		sm := GetSheets()
		records := sm.GetUserHistory(userID, 10)
		replyText(replyToken, sm.FormatHistory(records))
		return
	}

	if containsAny(lower, []string{"/help", "help", "commands"}) {
		help := "🤖 FitBot — Your Diet Assistant\n\n" +
			"📌 What I can do:\n" +
			"• Send a food photo → estimate calories\n" +
			"• Ask about nutrition / weight loss\n" +
			"• Tell me what you ate → log it to Google Sheets\n" +
			"• Remember your profile across sessions\n\n" +
			"📌 Commands:\n" +
			"• 'my meals' — view recent meal history\n" +
			"• 'my profile' — view your saved profile\n" +
			"• 'update profile' — update your info\n" +
			"• 'reset' — clear conversation history\n" +
			"• 'help' — show this menu"
		replyText(replyToken, help)
		return
	}

	if containsAny(lower, []string{"my profile", "ข้อมูลของฉัน", "โปรไฟล์"}) {
		sm := GetSheets()
		p := sm.GetUserProfile(userID)
		msg := formatProfile(p)
		replyText(replyToken, msg)
		return
	}

	if containsAny(lower, []string{"update profile", "แก้ไขข้อมูล", "อัปเดตข้อมูล"}) {
		replyText(replyToken,
			"📝 Let's update your profile! Please tell me:\n\n"+
				"1. Your name\n"+
				"2. Weight (kg)\n"+
				"3. Height (cm)\n"+
				"4. Goal (lose weight / maintain / gain muscle)\n"+
				"5. Daily calorie target (if you know it)\n"+
				"6. Any dietary restrictions or allergies\n\n"+
				"You can share all at once or just the parts you want to update 😊")
		return
	}

	// Check for pending meal confirmation
	if pending := getPending(userID); pending != nil {
		if containsAny(lower, []string{"save", "yes", "ok", "confirm", "log", "บันทึก", "ใช่"}) {
			sm := GetSheets()
			username := getUserName(userID)
			ok := sm.LogMeal(userID, username, pending.Food, pending.Calories)
			clearPending(userID)
			if ok {
				replyText(replyToken, fmt.Sprintf(
					"✅ Meal logged!\n🍽️ %s\n🔥 %s kcal",
					pending.Food, pending.Calories,
				))
			} else {
				replyText(replyToken, "⚠️ Could not save the meal right now. Please try again later.")
			}
			return
		}
		if containsAny(lower, []string{"no", "cancel", "skip", "ไม่"}) {
			clearPending(userID)
			replyText(replyToken, "👌 Got it, not saving. Anything else I can help with?")
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
		replyText(replyToken, "Sorry, I couldn't download the image. Please try again. 🙏")
		return
	}
	defer content.Content.Close()

	imageBytes, err := io.ReadAll(content.Content)
	if err != nil {
		log.Printf("failed to read image [%s]: %v", userID, err)
		replyText(replyToken, "Sorry, I couldn't process the image. Please try again. 🙏")
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
		return "📋 No profile saved yet.\n\nSay 'update profile' to set up your info and I'll remember it for every session! 😊"
	}
	lines := "📋 Your Profile\n\n"
	if p.Name != "" { lines += "👤 Name: " + p.Name + "\n" }
	if p.Weight != "" { lines += "⚖️ Weight: " + p.Weight + " kg\n" }
	if p.Height != "" { lines += "📏 Height: " + p.Height + " cm\n" }
	if p.Goal != "" { lines += "🎯 Goal: " + p.Goal + "\n" }
	if p.DailyCalories != "" { lines += "🔥 Daily Calories: " + p.DailyCalories + " kcal\n" }
	if p.Restrictions != "" { lines += "🚫 Restrictions: " + p.Restrictions + "\n" }
	if p.Notes != "" { lines += "📝 Notes: " + p.Notes + "\n" }
	if p.UpdatedAt != "" { lines += "\n🕐 Last updated: " + p.UpdatedAt }
	return lines
}


// ── Gemini test endpoint ──────────────────────────────────────────────────────

func testGeminiHandler(w http.ResponseWriter, r *http.Request) {
	reply := AskText("test-user", "Say 'FitBot is ready!' in Thai.", "")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"model":  activeModelName,
		"reply":  reply,
		"status": "ok",
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

	// Initialize Gemini AI client
	InitGemini()

	// Initialize Google Sheets
	GetSheets()

	// Register HTTP routes
	http.HandleFunc("/webhook", webhookHandler)
	http.HandleFunc("/test-gemini", testGeminiHandler)
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
