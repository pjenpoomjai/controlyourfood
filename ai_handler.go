package main

import (
	"bytes"
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
	audioModel = "whisper-large-v3-turbo"  // fast Thai speech-to-text
	maxHistory = 20
)

// textModels are tried in order — if the first hits quota (429), the next is used.
// Each model has its own 14,400 req/day quota on Groq free tier.
var textModels = []string{
	"llama-3.3-70b-versatile",  // best quality
	"llama-3.1-70b-versatile",  // same size, separate quota
	"mixtral-8x7b-32768",       // strong multilingual
	"llama-3.1-8b-instant",     // fastest fallback
	"gemma2-9b-it",             // last resort
}

// visionModels fallback list for image analysis.
var visionModels = []string{
	"llama-3.2-11b-vision-preview",
	"llama-3.2-90b-vision-preview",
}

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
	log.Printf("✅ Groq client ready (text: %s, vision: %s)", textModels[0], visionModels[0])
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

	return fmt.Sprintf(`CRITICAL INSTRUCTION: You MUST respond in Thai language ONLY. Never use English in your response under any circumstances. Even if the user writes in English, always reply in Thai.

คุณคือ "น้องฟิต" — ผู้ช่วย AI ด้านโภชนาการและการคุมอาหารที่เป็นมิตร

## กฎเหล็ก (ห้ามฝ่าฝืน)
- ตอบเป็นภาษาไทยเท่านั้น 100%% ห้ามมีคำภาษาอังกฤษในคำตอบ
- ถ้าผู้ใช้ถามภาษาอังกฤษ → ตอบเป็นภาษาไทยเสมอ
- ใช้ภาษาพูดที่เป็นกันเอง เข้าใจง่าย ไม่เป็นทางการเกินไป

## สไตล์การตอบ (สำคัญมาก)
- ตอบสั้น กระชับ ตรงประเด็น — ไม่เกิน 3-5 บรรทัด
- ห้ามพูดอ้อมค้อม ห้ามขึ้นต้นด้วยการทวนคำถาม
- ห้ามอธิบายยืดยาว ให้ข้อมูลสำคัญอย่างเดียว
- ใช้ emoji 1-2 ตัวพอ ไม่ใช้เยอะ

## ความสามารถ
- ตอบเรื่องโภชนาการ แคลอรี่ การลดน้ำหนัก
- วิเคราะห์รูปอาหาร ประเมินแคลอรี่
- จำข้อมูลผู้ใช้ข้ามเซสชัน

## วิเคราะห์รูปอาหาร
ระบุอาหาร → แคลอรี่ (ช่วง kcal) → โปรตีน/คาร์บ/ไขมัน → คำแนะนำ 1 ประโยค
%s%s
## ข้อควรระวัง
- ไม่ให้คำแนะนำทางการแพทย์
- แจ้งว่าแคลอรี่จากรูปอาจคลาดเคลื่อน

Remember: THAI LANGUAGE ONLY. Keep responses SHORT and CONCISE.`,
		knowledgeSection, buildProfileContext(profile))
}

// ── API call helpers ──────────────────────────────────────────────────────────

// isQuotaError returns true when Groq responds with 429 (rate limit / quota).
func isQuotaError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "429") ||
		strings.Contains(s, "rate_limit") ||
		strings.Contains(s, "quota")
}

// callGroqWithFallback tries each model in the list until one succeeds.
// Returns (reply, usage, modelUsed, error).
func callGroqWithFallback(models []string, systemPrompt string, messages []openai.ChatCompletionMessage) (string, openai.Usage, string, error) {
	ctx := context.Background()

	allMessages := append([]openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
	}, messages...)

	for i, model := range models {
		resp, err := groqClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:     model,
			Messages:  allMessages,
			MaxTokens: 400,
		})
		if err != nil {
			if isQuotaError(err) && i < len(models)-1 {
				log.Printf("quota hit on %s, switching to %s", model, models[i+1])
				continue
			}
			return "", openai.Usage{}, "", err
		}
		if len(resp.Choices) == 0 {
			return "", openai.Usage{}, "", fmt.Errorf("empty response from Groq")
		}
		if i > 0 {
			log.Printf("used fallback model: %s", model)
		}
		return resp.Choices[0].Message.Content, resp.Usage, model, nil
	}
	return "", openai.Usage{}, "", fmt.Errorf("all models exhausted quota")
}

// callGroq tries a specific model first, then falls back to the full text model list.
// Returns (reply, usage, modelUsed, error).
func callGroq(model, systemPrompt string, messages []openai.ChatCompletionMessage) (string, openai.Usage, string, error) {
	list := []string{model}
	for _, m := range textModels {
		if m != model {
			list = append(list, m)
		}
	}
	return callGroqWithFallback(list, systemPrompt, messages)
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

	reply, usage, modelUsed, err := callGroqWithFallback(textModels, system, msgs)
	if err != nil {
		log.Printf("Groq text error [%s]: %v", userID, err)
		return "ขออภัยค่ะ ระบบ AI ถึง quota แล้วค่ะ กรุณาลองใหม่ในอีกสักครู่ 🙏"
	}

	addHistory(userID, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: message})
	addHistory(userID, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: reply})

	// Log token usage in background (non-blocking)
	go GetSheets().LogTokenUsage(userID, modelUsed, "text", message,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	log.Printf("tokens [%s] text: prompt=%d completion=%d total=%d model=%s",
		userID, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, modelUsed)

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

	reply := ""
	for i, vm := range visionModels {
		resp, err := groqClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:     vm,
			Messages:  allMessages,
			MaxTokens: 400,
		})
		if err != nil {
			if isQuotaError(err) && i < len(visionModels)-1 {
				log.Printf("vision quota hit on %s, trying %s", vm, visionModels[i+1])
				continue
			}
			log.Printf("Groq vision error [%s]: %v", userID, err)
			return "ขออภัยค่ะ ไม่สามารถวิเคราะห์รูปได้ในขณะนี้ 🙏", ""
		}
		if len(resp.Choices) > 0 {
			reply = resp.Choices[0].Message.Content
		}
		// Log token usage in background
		go GetSheets().LogTokenUsage(userID, vm, "image", "[food image]",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
		log.Printf("tokens [%s] image: prompt=%d completion=%d total=%d model=%s",
			userID, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, vm)
		break
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

	reply, _, _, err := callGroq(textModels[0], "", []openai.ChatCompletionMessage{
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

// DetectMealFromText uses AI to detect if a message is a user reporting what they ate.
// Returns (food, calories) if a meal is detected, or ("", "") otherwise.
func DetectMealFromText(message string) (string, string) {
	if groqClient == nil {
		return "", ""
	}

	prompt := fmt.Sprintf(`Analyze if this message is a user reporting what they just ate or drank.
Return JSON only, no explanation.
{"food":"","calories":"","is_meal":false}

Rules:
- food: food/drink name in Thai (what they ate), empty if not a meal report
- calories: estimated calories as a number string (e.g. "450"), empty if unclear
- is_meal: true ONLY if user clearly states they ate/drank something (e.g. "กินข้าวมาแล้ว", "เพิ่งทาน", "ดื่มกาแฟ", "มื้อเที่ยงกิน")
- is_meal: false for questions, general chat, or commands

Message: "%s"`, message)

	reply, _, _, err := callGroq(textModels[0], "", []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: prompt},
	})
	if err != nil {
		return "", ""
	}

	raw := strings.TrimSpace(reply)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var data struct {
		Food     string `json:"food"`
		Calories string `json:"calories"`
		IsMeal   bool   `json:"is_meal"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", ""
	}
	if !data.IsMeal || strings.TrimSpace(data.Food) == "" {
		return "", ""
	}
	return strings.TrimSpace(data.Food), strings.TrimSpace(data.Calories)
}

// ExtractAndLearn extracts useful nutritional knowledge from a Q&A pair and
// returns (topic, knowledge) if something worth saving was found.
func ExtractAndLearn(userMessage, botReply string) (string, string) {
	if groqClient == nil {
		return "", ""
	}

	prompt := fmt.Sprintf(`Did this conversation contain any specific, reusable nutritional fact worth remembering?
Examples worth saving: calorie counts of specific foods, nutrition tips, Thai food data, dietary advice.
Examples NOT worth saving: greetings, general chat, profile updates, meal logs.

Return JSON only: {"topic":"","knowledge":"","should_save":false}
- topic: short category in Thai (e.g. "แคลอรี่อาหาร", "เคล็ดลับลดน้ำหนัก")
- knowledge: the specific fact in Thai, 1 concise sentence
- should_save: true only if there is a clear, reusable nutritional fact

User: "%s"
Bot: "%s"`, userMessage, botReply)

	reply, _, _, err := callGroq(textModels[0], "", []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: prompt},
	})
	if err != nil {
		return "", ""
	}

	raw := strings.TrimSpace(reply)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var data struct {
		Topic      string `json:"topic"`
		Knowledge  string `json:"knowledge"`
		ShouldSave bool   `json:"should_save"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", ""
	}
	if !data.ShouldSave || data.Topic == "" || data.Knowledge == "" {
		return "", ""
	}
	return strings.TrimSpace(data.Topic), strings.TrimSpace(data.Knowledge)
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

// TranscribeAudio converts LINE audio (M4A) to text using Groq Whisper.
func TranscribeAudio(audioBytes []byte) (string, error) {
	ctx := context.Background()
	resp, err := groqClient.CreateTranscription(ctx, openai.AudioRequest{
		Model:    audioModel,
		FilePath: "audio.m4a", // extension tells Groq the format
		Reader:   bytes.NewReader(audioBytes),
		Language: "th",
		Format:   openai.AudioResponseFormatText,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text), nil
}
