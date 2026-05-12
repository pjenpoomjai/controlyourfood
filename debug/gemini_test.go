package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/generative-ai-go/genai"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
)

var modelsToTry = []string{
	"gemini-1.5-flash",
	"gemini-1.5-flash-latest",
	"gemini-1.0-pro",
}

func main() {
	_ = godotenv.Load("../.env")

	apiKey := os.Getenv("GEMINI_API_KEY")

	fmt.Println("=== Gemini API Debug ===")
	fmt.Printf("API Key: %s...%s\n\n", apiKey[:8], apiKey[len(apiKey)-4:])

	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		fmt.Printf("❌ Failed to create client: %v\n", err)
		return
	}
	defer client.Close()
	fmt.Println("✅ Client created successfully")

	// Try each model
	for _, modelName := range modelsToTry {
		fmt.Printf("\n🔄 Testing model: %s ... ", modelName)
		model := client.GenerativeModel(modelName)
		resp, err := model.GenerateContent(ctx, genai.Text("Say 'OK' in one word."))
		if err != nil {
			fmt.Printf("❌ Error: %v\n", err)
			continue
		}
		for _, cand := range resp.Candidates {
			if cand.Content != nil {
				for _, part := range cand.Content.Parts {
					if t, ok := part.(genai.Text); ok {
						fmt.Printf("✅ Works! Response: %s\n", string(t))
					}
				}
			}
		}
	}
}
