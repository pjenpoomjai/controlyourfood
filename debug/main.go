package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

func main() {
	_ = godotenv.Load("../.env")

	spreadsheetID := os.Getenv("SPREADSHEET_ID")
	credsPath := os.Getenv("GOOGLE_CREDENTIALS_PATH")
	if credsPath == "" {
		credsPath = "../google_credentials.json"
	}

	fmt.Println("=== Google Sheets Debug ===")
	fmt.Printf("SPREADSHEET_ID : [%s]\n", spreadsheetID)
	fmt.Printf("CREDS FILE     : %s\n\n", credsPath)

	// 1. Check credentials file
	credsData, err := os.ReadFile(credsPath)
	if err != nil {
		fmt.Printf("❌ Cannot read credentials: %v\n", err)
		return
	}
	fmt.Println("✅ Credentials file found")

	// 2. Parse client_email from credentials
	var creds map[string]interface{}
	_ = json.Unmarshal(credsData, &creds)
	if email, ok := creds["client_email"].(string); ok {
		fmt.Printf("   Service Account: %s\n\n", email)
		fmt.Println("👉 Make sure this email is added as EDITOR in your Google Sheet!")
	}

	// 3. Check Spreadsheet ID
	if spreadsheetID == "" {
		fmt.Println("❌ SPREADSHEET_ID is empty in .env")
		return
	}
	if len(spreadsheetID) < 20 {
		fmt.Printf("❌ SPREADSHEET_ID looks too short (%d chars). Check your .env\n", len(spreadsheetID))
		return
	}
	fmt.Printf("✅ SPREADSHEET_ID length: %d chars (looks OK)\n\n", len(spreadsheetID))

	// 4. Try to connect
	config, err := google.JWTConfigFromJSON(credsData, sheets.SpreadsheetsScope)
	if err != nil {
		fmt.Printf("❌ Invalid credentials format: %v\n", err)
		return
	}

	ctx := context.Background()
	client := config.Client(ctx)
	srv, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		fmt.Printf("❌ Failed to create Sheets service: %v\n", err)
		return
	}
	fmt.Println("✅ Sheets service created")

	// 5. Try to GET the spreadsheet
	fmt.Printf("   Trying to access spreadsheet ID: %s\n", spreadsheetID)
	ss, err := srv.Spreadsheets.Get(spreadsheetID).Do()
	if err != nil {
		fmt.Printf("\n❌ Cannot access spreadsheet: %v\n", err)
		fmt.Println("\n--- Possible causes ---")
		fmt.Println("1. Wrong Spreadsheet ID in .env")
		fmt.Println("2. Service account email NOT added as Editor in the Sheet")
		fmt.Println("3. Google Sheets API or Drive API not enabled in Cloud Console")
		return
	}

	fmt.Printf("✅ SUCCESS! Spreadsheet found: \"%s\"\n", ss.Properties.Title)
	fmt.Println("\nExisting sheets:")
	for _, s := range ss.Sheets {
		fmt.Printf("   - %s\n", s.Properties.Title)
	}
	fmt.Println("\n🎉 Google Sheets connection is working!")
}
