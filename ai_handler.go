package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
)

const (
	anthropicAPI = "https://api.anthropic.com/v1/messages"
	claudeModel  = "claude-opus-4-6"
	maxHistory   = 20
)

// ── Anthropic API structs ─────────────────────────────────────────────────────

type anthropicRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []chatMsg `json:"messages"`
}

type chatMsg struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []contentBlock
}

type contentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ── Conversation history ──────────────────────────────────────────────────────

var (
	historyMu sync.Mutex
	history   = make(map[string][]chatMsg)
)

func getHistory(userID string) []chatMsg {
	historyMu.Lock()
	defer historyMu.Unlock()
	msgs := history[userID]
	result := make([]chatMsg, len(msgs))
	copy(result, msgs)
	return result
}

func addHistory(userID, role string, content interface{}) {
	historyMu.Lock()
	defer historyMu.Unlock()
	history[userID] = append(history[userID], chatMsg{Role: role, Content: content})
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

// ── Claude API call ───────────────────────────────────────────────────────────

func callClaude(msgs []chatMsg, system string) (string, error) {
	reqBody := anthropicRequest{
		Model:     claudeModel,
		MaxTokens: 1024,
		System:    system,
		Messages:  msgs,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequest("POST", anthropicAPI, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("request error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", os.Getenv("ANTHROPIC_API_KEY"))
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http error: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read error: %w", err)
	}

	var ar anthropicResponse
	if err := json.Unmarshal(respBytes, &ar); err != nil {
		return "", fmt.Errorf("unmarshal error: %w", err)
	}

	if ar.Error != nil {
		return "", fmt.Errorf("anthropic API error: %s", ar.Error.Message)
	}

	if len(ar.Content) == 0 {
		return "", fmt.Errorf("empty response from API")
	}

	return ar.Content[0].Text, nil
}

// ── Public functions ──────────────────────────────────────────────────────────

// AskText sends a text message to Claude and returns the reply.
func AskText(userID, message, knowledgeBase string) string {
	addHistory(userID, "user", message)
	msgs := getHistory(userID)

	reply, err := callClaude(msgs, buildSystemPrompt(knowledgeBase))
	if err != nil {
		log.Printf("Claude text error [%s]: %v", userID, err)
		popHistory(userID)
		return "Sorry, something went wrong. Please try again. 🙏"
	}

	addHistory(userID, "assistant", reply)
	return reply
}

// AskImage sends a food image to Claude and returns (reply, estimatedCalories).
func AskImage(userID string, imageBytes []byte, mediaType, knowledgeBase string) (string, string) {
	imgData := base64.StdEncoding.EncodeToString(imageBytes)

	userContent := []contentBlock{
		{
			Type: "image",
			Source: &imageSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      imgData,
			},
		},
		{
			Type: "text",
			Text: "This is the food I ate. Please analyze the calories and nutritional content.",
		},
	}

	addHistory(userID, "user", userContent)
	msgs := getHistory(userID)

	reply, err := callClaude(msgs, buildSystemPrompt(knowledgeBase))
	if err != nil {
		log.Printf("Claude image error [%s]: %v", userID, err)
		popHistory(userID)
		return "Sorry, I could not analyze the image at this time. Please try again. 🙏", ""
	}

	// Replace the image message in history with a text placeholder to save memory.
	popHistory(userID)
	addHistory(userID, "user", "I sent a food image for analysis.")
	addHistory(userID, "assistant", reply)

	calories := extractCalories(reply)
	return reply, calories
}

// extractCalories attempts to parse a calorie estimate from the reply text.
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
