package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Free-tier Gemini models — tried in order, first working one is used.
var modelCandidates = []string{
	"gemini-2.5-flash-preview-04-17", // Gemini 2.5 Flash (latest)
	"gemini-2.5-flash",               // Gemini 2.5 Flash (stable alias)
	"gemini-2.0-flash",               // fallback
	"gemini-1.5-flash",               // fallback
	"gemini-1.5-flash-8b",            // fallback
}

const maxHistory = 20

// ── Gemini client ─────────────────────────────────────────────────────────────

var (
	geminiClient    *genai.Client
	activeModelName string
)

// InitGemini creates the Gemini client and auto-detects the best available model.
func InitGemini() {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("GEMINI_API_KEY is not set — please add it to your environment variables")
	}
	// Log first 8 chars for verification (never log full key)
	masked := apiKey[:8] + "..." + apiKey[len(apiKey)-4:]
	log.Printf("GEMINI_API_KEY found: %s", masked)

	ctx := context.Background()
	var err error
	geminiClient, err = genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Fatalf("failed to create Gemini client: %v", err)
	}

	// Auto-detect working model
	activeModelName = detectWorkingModel(ctx)
	if activeModelName == "" {
		log.Fatal("no working Gemini model found — check your API key and quota at aistudio.google.com")
	}
	log.Printf("✅ Gemini ready using model: %s", activeModelName)
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

// buildProfileContext formats a user profile into a context string for the AI.
func buildProfileContext(p *UserProfile) string {
	if p == nil {
		return ""
	}
	var parts []string
	if p.Name != "" { parts = append(parts, "Name: "+p.Name) }
	if p.Weight != "" { parts = append(parts, "Weight: "+p.Weight+" kg") }
	if p.Height != "" { parts = append(parts, "Height: "+p.Height+" cm") }
	if p.Goal != "" { parts = append(parts, "Goal: "+p.Goal) }
	if p.DailyCalories != "" { parts = append(parts, "Daily calorie target: "+p.DailyCalories+" kcal") }
	if p.Restrictions != "" { parts = append(parts, "Dietary restrictions: "+p.Restrictions) }
	if p.Notes != "" { parts = append(parts, "Additional notes: "+p.Notes) }
	if len(parts) == 0 {
		return ""
	}
	return "\n## User Profile (remembered from previous sessions)\n" + strings.Join(parts, "\n") + "\n"
}

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
5. Remember user information across sessions (weight, height, goals, dietary needs)

## User Profile Memory
- When users share personal info (weight, height, goals, allergies), acknowledge it warmly
- Use profile info to personalize advice (e.g. adjust calorie recommendations to their goal)
- If no profile exists yet, naturally ask for basic info during conversation

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

func newModel(knowledgeBase string, profile *UserProfile) *genai.GenerativeModel {
	model := geminiClient.GenerativeModel(activeModelName)
	systemPrompt := buildSystemPrompt(knowledgeBase) + buildProfileContext(profile)
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(systemPrompt)},
	}
	temp := float32(0.7)
	maxTokens := int32(1024)
	model.Temperature = &temp
	model.MaxOutputTokens = &maxTokens
	return model
}

// ── Retry helper ──────────────────────────────────────────────────────────────

// isRateLimit checks if the error is a 429 rate-limit error worth retrying.
func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "429") ||
		strings.Contains(err.Error(), "quota") ||
		strings.Contains(err.Error(), "RESOURCE_EXHAUSTED")
}

func sendWithRetry(cs *genai.ChatSession, parts ...genai.Part) (*genai.GenerateContentResponse, error) {
	ctx := context.Background()
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := cs.SendMessage(ctx, parts...)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// Only retry on rate limit (429) — not on 404 or other errors
		if !isRateLimit(err) {
			log.Printf("Gemini non-retryable error: %v", err)
			return nil, err
		}
		if attempt < 3 {
			wait := time.Duration(attempt*3) * time.Second
			log.Printf("Gemini rate limit (attempt %d/3), retrying in %v...", attempt, wait)
			time.Sleep(wait)
		}
	}
	return nil, lastErr
}

// ── Public functions ──────────────────────────────────────────────────────────

// AskText sends a text message to Gemini and returns the reply.
func AskText(userID, message, knowledgeBase string) string {
	profile := GetSheets().GetUserProfile(userID)
	model := newModel(knowledgeBase, profile)
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
	profile := GetSheets().GetUserProfile(userID)
	model := newModel(knowledgeBase, profile)
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

// ExtractProfileFromMessage uses Gemini to extract profile info from a user message.
func ExtractProfileFromMessage(userID, message string) *UserProfile {
	if geminiClient == nil {
		return nil
	}
	ctx := context.Background()
	model := geminiClient.GenerativeModel(activeModelName)
	temp := float32(0.1)
	model.Temperature = &temp

	prompt := fmt.Sprintf(`Extract personal health/diet info from this message. Return JSON only, no explanation.
If nothing relevant, return: {}

Message: "%s"

JSON fields (leave blank if not mentioned):
{
  "name": "",
  "weight_kg": "",
  "height_cm": "",
  "goal": "",
  "daily_calories": "",
  "restrictions": "",
  "notes": ""
}

goal must be one of: "Lose weight", "Maintain weight", "Gain muscle", or blank.`, message)

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		log.Printf("profile extraction error: %v", err)
		return nil
	}

	raw := responseText(resp)
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	if raw == "{}" || raw == "" {
		return nil
	}

	var data map[string]string
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		log.Printf("profile JSON parse error: %v (raw: %s)", err, raw)
		return nil
	}

	hasData := false
	for _, v := range data {
		if strings.TrimSpace(v) != "" {
			hasData = true
			break
		}
	}
	if !hasData {
		return nil
	}

	existing := GetSheets().GetUserProfile(userID)
	// Merge — only overwrite non-empty fields
	if v := strings.TrimSpace(data["name"]); v != "" { existing.Name = v }
	if v := strings.TrimSpace(data["weight_kg"]); v != "" { existing.Weight = v }
	if v := strings.TrimSpace(data["height_cm"]); v != "" { existing.Height = v }
	if v := strings.TrimSpace(data["goal"]); v != "" { existing.Goal = v }
	if v := strings.TrimSpace(data["daily_calories"]); v != "" { existing.DailyCalories = v }
	if v := strings.TrimSpace(data["restrictions"]); v != "" { existing.Restrictions = v }
	if v := strings.TrimSpace(data["notes"]); v != "" { existing.Notes = v }

	log.Printf("extracted profile for %s: weight=%s height=%s goal=%s", userID, existing.Weight, existing.Height, existing.Goal)
	return existing
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
