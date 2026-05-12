package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Free-tier Gemini models — tried in order, first working one is used.
// All models below are free via Google AI Studio (aistudio.google.com).
var modelCandidates = []string{
	"gemini-2.0-flash-lite", // best free tier: 30 RPM, 1500 RPD
	"gemini-2.0-flash",      // 15 RPM, 1500 RPD free
	"gemini-1.5-flash-8b",   // lightweight, 15 RPM free
	"gemini-1.5-flash",      // 15 RPM free
	"gemini-1.5-flash-latest",
}

const maxHistory = 20

// ── Gemini client ─────────────────────────────────────────────────────────────

var (
	geminiClient    *genai.Client
	activeModelName string
)

// InitGemini creates the Gemini client and auto-detects the best available model.
func InitGemini() {
	ctx := context.Background()
	var err error
	geminiClient, err = genai.NewClient(ctx, option.WithAPIKey(os.Getenv("GEMINI_API_KEY")))
	if err != nil {
		log.Fatalf("failed to create Gemini client: %v", err)
	}

	// Auto-detect working model
	activeModelName = detectWorkingModel(ctx)
	if activeModelName == "" {
		log.Fatal("no working Gemini model found — check your API key and quota")
	}
	log.Printf("Gemini ready using model: %s", activeModelName)
}

// detectWorkingModel tries each candidate and returns the first that responds.
func detectWorkingModel(ctx context.Context) string {
	for _, name := range modelCandidates {
		log.Printf("trying model: %s ...", name)
		m := geminiClient.GenerativeModel(name)
		_, err := m.GenerateContent(ctx, genai.Text("hi"))
		if err == nil {
			log.Printf("✅ model works: %s", name)
			return name
		}
		log.Printf("❌ model %s failed: %v", name, err)
	}
	return ""
}

// ── Conversation history ──────────────────────────────────────────────────────

var (
	historyMu sync.Mutex
	history   = make(map[string][]*genai.Content)
)

func getHistory(userID string) []*genai.Content {
	historyMu.Lock()
	defer historyMu.Unlock()
	h := history[userID]
	result := make([]*genai.Content, len(h))
	copy(result, h)
	return result
}

func addHistory(userID string, content *genai.Content) {
	historyMu.Lock()
	defer historyMu.Unlock()
	history[userID] = append(history[userID], content)
	if len(history[userID]) > maxHistory {
		history[userID] = history[userID][len(history[userID])-maxHistory:]
	}
}

// ClearHistory clears the conversation history for a user.
func ClearHistory(userID string) {
	historyMu.Lock()
	defer historyMu.Unlock()
	delete(history, userID)
}

// ── System prompt ─────────────────────────────────────────────────────────────

func buildSystemPrompt(knowledgeBase string) string {
	knowledgeSection := ""
	if knowledgeBase != "" {
		knowledgeSection = fmt.Sprintf(`

## Knowledge Base
Use the following documents as your primary reference when answering questions:

%s

---`, knowledgeBase)
	}

	return fmt.Sprintf(`You are "FitBot" — a friendly AI nutrition and diet assistant. Respond in Thai language.

## Personality
- Friendly, encouraging, and non-judgmental
- Respond primarily in Thai, keep answers concise and easy to understand
- Use emojis appropriately to make conversations engaging
- Provide accurate and helpful information

## Capabilities
1. Answer nutrition questions — calories, macronutrients, weight loss, clean eating
2. Analyze food images — estimate calories and nutritional breakdown
3. Give recommendations — healthy meal ideas, tips for better eating habits
4. Track meals — when users describe what they ate, provide feedback and estimates

## Image Analysis
When receiving a food image:
1. Identify all visible food items
2. Estimate total calories (give a range, e.g. 350-450 kcal)
3. Summarize key macronutrients (protein, carbs, fat)
4. Provide a brief tip
5. Ask if the user wants to log this meal (reply "บันทึก" to save)
%s
## Disclaimers
- Do not replace professional medical advice
- For complex health conditions, recommend consulting a doctor or registered dietitian
- Always mention that calorie estimates from images are approximate`, knowledgeSection)
}

// ── Model builder ─────────────────────────────────────────────────────────────

func newModel(knowledgeBase string) *genai.GenerativeModel {
	model := geminiClient.GenerativeModel(activeModelName)
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(buildSystemPrompt(knowledgeBase))},
	}
	temp := float32(0.7)
	maxTokens := int32(1024)
	model.Temperature = &temp
	model.MaxOutputTokens = &maxTokens
	return model
}

// ── Retry helper ──────────────────────────────────────────────────────────────

func sendWithRetry(cs *genai.ChatSession, parts ...genai.Part) (*genai.GenerateContentResponse, error) {
	ctx := context.Background()
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := cs.SendMessage(ctx, parts...)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt < 3 {
			wait := time.Duration(attempt*3) * time.Second
			log.Printf("Gemini error (attempt %d/3), retrying in %v: %v", attempt, wait, err)
			time.Sleep(wait)
		}
	}
	return nil, lastErr
}

// ── Public functions ──────────────────────────────────────────────────────────

// AskText sends a text message to Gemini and returns the reply.
func AskText(userID, message, knowledgeBase string) string {
	model := newModel(knowledgeBase)
	cs := model.StartChat()
	cs.History = getHistory(userID)

	resp, err := sendWithRetry(cs, genai.Text(message))
	if err != nil {
		log.Printf("Gemini text error [%s]: %v", userID, err)
		return "ขออภัยค่ะ เกิดข้อผิดพลาด กรุณาลองใหม่อีกครั้ง 🙏"
	}

	reply := responseText(resp)
	addHistory(userID, &genai.Content{Role: "user", Parts: []genai.Part{genai.Text(message)}})
	addHistory(userID, &genai.Content{Role: "model", Parts: []genai.Part{genai.Text(reply)}})
	return reply
}

// AskImage sends a food image to Gemini and returns (reply, estimatedCalories).
func AskImage(userID string, imageBytes []byte, mediaType, knowledgeBase string) (string, string) {
	model := newModel(knowledgeBase)
	cs := model.StartChat()
	cs.History = getHistory(userID)

	imgPart := genai.ImageData(mimeToExt(mediaType), imageBytes)
	textPart := genai.Text("This is the food I ate. Please analyze the calories and nutritional content.")

	resp, err := sendWithRetry(cs, imgPart, textPart)
	if err != nil {
		log.Printf("Gemini image error [%s]: %v", userID, err)
		return "ขออภัยค่ะ ไม่สามารถวิเคราะห์รูปได้ในขณะนี้ 🙏", ""
	}

	reply := responseText(resp)
	calories := extractCalories(reply)

	addHistory(userID, &genai.Content{Role: "user", Parts: []genai.Part{genai.Text("I sent a food image for analysis.")}})
	addHistory(userID, &genai.Content{Role: "model", Parts: []genai.Part{genai.Text(reply)}})
	return reply, calories
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func responseText(resp *genai.GenerateContentResponse) string {
	for _, cand := range resp.Candidates {
		if cand.Content != nil {
			for _, part := range cand.Content.Parts {
				if t, ok := part.(genai.Text); ok {
					return string(t)
				}
			}
		}
	}
	return "ขออภัยค่ะ ไม่สามารถสร้างคำตอบได้ กรุณาลองใหม่อีกครั้ง"
}

func mimeToExt(mediaType string) string {
	switch mediaType {
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return "jpeg"
	}
}

func extractCalories(text string) string {
	patterns := []string{
		`(\d+[-–]\d+)\s*(?:kcal|cal)`,
		`(?:approximately|~|≈|about|ประมาณ)?\s*(\d+)\s*(?:kcal|cal|แคลอรี่)`,
	}
	for _, pattern := range patterns {
		re := regexp.MustCompile(`(?i)` + pattern)
		matches := re.FindStringSubmatch(text)
		if len(matches) > 1 {
			return matches[1]
		}
	}
	return ""
}
