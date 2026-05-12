package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"

	openai "github.com/sashabaranov/go-openai"
)

const (
	textModel   = "llama-3.3-70b-versatile"      // best Thai + nutrition understanding
	visionModel = "llama-3.2-11b-vision-preview"  // for food image analysis
	maxHistory  = 20
)

// ── Groq client ───────────────────────────────────────────────────────────────

var groqClient *openai.Client

// InitGroq creates the shared Groq client. Call once at startup.
func InitGroq() {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		log.Fatal("GROQ_API_KEY is not set — please add it in your environment variables")
	}
	masked := apiKey[:8] + "..." + apiKey[len(apiKey)-4:]
	log.Printf("GROQ_API_KEY found: %s", masked)

	config := openai.DefaultConfig(apiKey)
	config.BaseURL = "https://api.groq.com/openai/v1"
	groqClient = openai.NewClientWithConfig(config)
	log.Printf("✅ Groq client ready (text: %s, vision: %s)", textModel, visionModel)
}

// ── Conversation history ──────────────────────────────────────────────────────

var (
	historyMu sync.Mutex
	history   = make(map[string][]openai.ChatCompletionMessage)
)

func getHistory(userID string) []openai.ChatCompletionMessage {
	historyMu.Lock()
	defer historyMu.Unlock()
	h := history[userID]
	result := make([]openai.ChatCompletionMessage, len(h))
	copy(result, h)
	return result
}

func addHistory(userID string, msg openai.ChatCompletionMessage) {
	historyMu.Lock()
	defer historyMu.Unlock()
	history[userID] = append(history[userID], msg)
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

func buildSystemPrompt(knowledgeBase string, profile *UserProfile) string {
	knowledgeSection := ""
	if knowledgeBase != "" {
		knowledgeSection = fmt.Sprintf(`

## Knowledge Base
Use the following documents as your primary reference:

%s

---`, knowledgeBase)
	}

	return fmt.Sprintf(`You are "FitBot" — a friendly AI nutrition and diet assistant. Respond in Thai language.

## Personality
- Friendly, encouraging, and non-judgmental
- Respond primarily in Thai, keep answers concise and easy to understand
- Use emojis to make conversations engaging
- Provide accurate and helpful information

## Capabilities
1. Answer nutrition questions — calories, macronutrients, weight loss, clean eating
2. Analyze food images — estimate calories and nutritional breakdown
3. Give recommendations — healthy meal ideas, tips for better eating habits
4. Track meals — when users describe what they ate, provide feedback
5. Remember user info across sessions (weight, height, goals, dietary needs)

## User Profile Memory
- Use profile info to personalize advice
- If no profile exists, naturally ask for basic info during conversation

## Image Analysis
When receiving a food image:
1. Identify all visible food items
2. Estimate total calories (give a range, e.g. 350-450 kcal)
3. Summarize key macronutrients (protein, carbs, fat)
4. Provide a brief tip
5. Ask if user wants to log this meal (reply "บันทึก" to save)
%s%s
## Disclaimers
- Do not replace professional medical advice
- For complex health conditions, recommend consulting a doctor or dietitian
- Always note that calorie estimates from images are approximate`,
		knowledgeSection, buildProfileContext(profile))
}

// ── API call helpers ──────────────────────────────────────────────────────────

func callGroq(model, systemPrompt string, messages []openai.ChatCompletionMessage) (string, error) {
	ctx := context.Background()

	allMessages := append([]openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
	}, messages...)

	resp, err := groqClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:     model,
		Messages:  allMessages,
		MaxTokens: 1024,
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("empty response from Groq")
	}
	return resp.Choices[0].Message.Content, nil
}

// ── Public functions ──────────────────────────────────────────────────────────

// AskText sends a text message to Groq and returns the reply.
func AskText(userID, message, knowledgeBase string) string {
	profile := GetSheets().GetUserProfile(userID)
	system := buildSystemPrompt(knowledgeBase, profile)

	msgs := getHistory(userID)
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: message,
	})

	reply, err := callGroq(textModel, system, msgs)
	if err != nil {
		log.Printf("Groq text error [%s]: %v", userID, err)
		return "ขออภัยค่ะ เกิดข้อผิดพลาด กรุณาลองใหม่อีกครั้ง 🙏"
	}

	addHistory(userID, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: message})
	addHistory(userID, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: reply})
	return reply
}

// AskImage sends a food image to Groq Vision and returns (reply, estimatedCalories).
func AskImage(userID string, imageBytes []byte, mediaType, knowledgeBase string) (string, string) {
	profile := GetSheets().GetUserProfile(userID)
	system := buildSystemPrompt(knowledgeBase, profile)

	imgData := base64.StdEncoding.EncodeToString(imageBytes)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mediaType, imgData)

	msgs := getHistory(userID)
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		MultiContent: []openai.ChatMessagePart{
			{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL: dataURL,
				},
			},
			{
				Type: openai.ChatMessagePartTypeText,
				Text: "This is the food I ate. Please analyze calories and nutritional content.",
			},
		},
	})

	ctx := context.Background()
	allMessages := append([]openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: system},
	}, msgs...)

	resp, err := groqClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:     visionModel,
		Messages:  allMessages,
		MaxTokens: 1024,
	})
	if err != nil {
		log.Printf("Groq vision error [%s]: %v", userID, err)
		return "ขออภัยค่ะ ไม่สามารถวิเคราะห์รูปได้ในขณะนี้ 🙏", ""
	}

	reply := ""
	if len(resp.Choices) > 0 {
		reply = resp.Choices[0].Message.Content
	}

	// Store text placeholder in history (not raw image bytes)
	addHistory(userID, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "I sent a food image for analysis."})
	addHistory(userID, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: reply})

	return reply, extractCalories(reply)
}

// ExtractProfileFromMessage uses Groq to extract profile info from a user message.
func ExtractProfileFromMessage(userID, message string) *UserProfile {
	if groqClient == nil {
		return nil
	}

	prompt := fmt.Sprintf(`Extract personal health/diet info from this message. Return JSON only, no explanation.
If nothing relevant, return: {}

Message: "%s"

JSON fields (leave blank if not mentioned):
{"name":"","weight_kg":"","height_cm":"","goal":"","daily_calories":"","restrictions":"","notes":""}

goal must be one of: "Lose weight", "Maintain weight", "Gain muscle", or blank.`, message)

	reply, err := callGroq(textModel, "", []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: prompt},
	})
	if err != nil {
		return nil
	}

	raw := strings.TrimSpace(reply)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	if raw == "{}" || raw == "" {
		return nil
	}

	var data map[string]string
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
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
	if v := strings.TrimSpace(data["name"]); v != "" { existing.Name = v }
	if v := strings.TrimSpace(data["weight_kg"]); v != "" { existing.Weight = v }
	if v := strings.TrimSpace(data["height_cm"]); v != "" { existing.Height = v }
	if v := strings.TrimSpace(data["goal"]); v != "" { existing.Goal = v }
	if v := strings.TrimSpace(data["daily_calories"]); v != "" { existing.DailyCalories = v }
	if v := strings.TrimSpace(data["restrictions"]); v != "" { existing.Restrictions = v }
	if v := strings.TrimSpace(data["notes"]); v != "" { existing.Notes = v }

	log.Printf("profile extracted for %s: weight=%s goal=%s", userID, existing.Weight, existing.Goal)
	return existing
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func extractCalories(text string) string {
	patterns := []string{
		`(\d+[-–]\d+)\s*(?:kcal|cal)`,
		`(?:approximately|~|≈|about|ประมาณ)?\s*(\d+)\s*(?:kcal|cal|แคลอรี่)`,
	}
	for _, pattern := range patterns {
		re := regexp.MustCompile(`(?i)` + pattern)
		if m := re.FindStringSubmatch(text); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}
