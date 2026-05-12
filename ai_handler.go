package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"sync"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

const (
	geminiModel = "gemini-2.0-flash"
	maxHistory  = 20
)

// ── Gemini client ─────────────────────────────────────────────────────────────

var geminiClient *genai.Client

// InitGemini creates the shared Gemini client. Call once at startup.
func InitGemini() {
	ctx := context.Background()
	var err error
	geminiClient, err = genai.NewClient(ctx, option.WithAPIKey(os.Getenv("GEMINI_API_KEY")))
	if err != nil {
		log.Fatalf("failed to create Gemini client: %v", err)
	}
	log.Println("Gemini client initialized")
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

func popHistory(userID string) {
	historyMu.Lock()
	defer historyMu.Unlock()
	if len(history[userID]) > 0 {
		history[userID] = history[userID][:len(history[userID])-1]
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
2. Estimate total calories (give a range, e.g. 350–450 kcal)
3. Summarize key macronutrients (protein, carbs, fat)
4. Provide a brief tip
5. Ask if the user wants to log this meal (reply "บันทึก" to save)
%s
## Disclaimers
- Do not replace professional medical advice
- For complex health conditions, recommend consulting a doctor or registered dietitian
- Always mention that calorie estimates from images are approximate`, knowledgeSection)
}

// ── Shared model builder ──────────────────────────────────────────────────────

func newModel(knowledgeBase string) *genai.GenerativeModel {
	model := geminiClient.GenerativeModel(geminiModel)
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(buildSystemPrompt(knowledgeBase))},
	}
	temp := float32(0.7)
	maxTokens := int32(1024)
	model.Temperature = &temp
	model.MaxOutputTokens = &maxTokens
	return model
}

// ── Public functions ──────────────────────────────────────────────────────────

// AskText sends a text message to Gemini and returns the reply.
func AskText(userID, message, knowledgeBase string) string {
	ctx := context.Background()
	model := newModel(knowledgeBase)

	cs := model.StartChat()
	cs.History = getHistory(userID)

	resp, err := cs.SendMessage(ctx, genai.Text(message))
	if err != nil {
		log.Printf("Gemini text error [%s]: %v", userID, err)
		return "Sorry, something went wrong. Please try again. 🙏"
	}

	reply := responseText(resp)

	// Save both turns to history
	addHistory(userID, &genai.Content{Role: "user", Parts: []genai.Part{genai.Text(message)}})
	addHistory(userID, &genai.Content{Role: "model", Parts: []genai.Part{genai.Text(reply)}})

	return reply
}

// AskImage sends a food image to Gemini and returns (reply, estimatedCalories).
func AskImage(userID string, imageBytes []byte, mediaType, knowledgeBase string) (string, string) {
	ctx := context.Background()
	model := newModel(knowledgeBase)

	cs := model.StartChat()
	cs.History = getHistory(userID)

	imgPart := genai.ImageData(mimeToExt(mediaType), imageBytes)
	textPart := genai.Text("This is the food I ate. Please analyze the calories and nutritional content.")

	resp, err := cs.SendMessage(ctx, imgPart, textPart)
	if err != nil {
		log.Printf("Gemini image error [%s]: %v", userID, err)
		return "Sorry, I could not analyze the image at this time. Please try again. 🙏", ""
	}

	reply := responseText(resp)
	calories := extractCalories(reply)

	// Store text placeholder in history instead of raw image bytes (save memory)
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
	return "Sorry, I could not generate a response. Please try again."
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
