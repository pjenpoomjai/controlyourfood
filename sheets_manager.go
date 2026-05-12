package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

var sheetHeaders = []interface{}{"Date", "Time", "User ID", "Username", "Food", "Calories (kcal)", "Notes"}

// SheetsManager handles Google Sheets read/write operations.
type SheetsManager struct {
	srv           *sheets.Service
	spreadsheetID string
	sheetName     string
}

var sheetsInstance *SheetsManager

// GetSheets returns the singleton SheetsManager instance.
func GetSheets() *SheetsManager {
	if sheetsInstance == nil {
		sheetsInstance = newSheetsManager()
	}
	return sheetsInstance
}

func newSheetsManager() *SheetsManager {
	spreadsheetID := os.Getenv("SPREADSHEET_ID")
	credsPath := os.Getenv("GOOGLE_CREDENTIALS_PATH")
	if credsPath == "" {
		credsPath = "google_credentials.json"
	}

	if spreadsheetID == "" {
		log.Println("SPREADSHEET_ID not set — Google Sheets disabled")
		return &SheetsManager{}
	}

	credsData, err := os.ReadFile(credsPath)
	if err != nil {
		log.Printf("credentials file not found (%s) — Google Sheets disabled", credsPath)
		return &SheetsManager{}
	}

	config, err := google.JWTConfigFromJSON(credsData, sheets.SpreadsheetsScope)
	if err != nil {
		log.Printf("failed to parse credentials: %v", err)
		return &SheetsManager{}
	}

	ctx := context.Background()
	client := config.Client(ctx)
	srv, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Printf("failed to create Sheets service: %v", err)
		return &SheetsManager{}
	}

	sm := &SheetsManager{
		srv:           srv,
		spreadsheetID: spreadsheetID,
		sheetName:     "Meal History",
	}

	sm.ensureSheet()
	log.Println("Google Sheets connected successfully")
	return sm
}

// IsConnected returns true if the Sheets service is available.
func (sm *SheetsManager) IsConnected() bool {
	return sm.srv != nil
}

// ensureSheet creates the sheet tab if it does not already exist.
func (sm *SheetsManager) ensureSheet() {
	if !sm.IsConnected() {
		return
	}

	ss, err := sm.srv.Spreadsheets.Get(sm.spreadsheetID).Do()
	if err != nil {
		log.Printf("failed to get spreadsheet info: %v", err)
		return
	}

	for _, s := range ss.Sheets {
		if s.Properties.Title == sm.sheetName {
			return // already exists
		}
	}

	req := &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{
				AddSheet: &sheets.AddSheetRequest{
					Properties: &sheets.SheetProperties{Title: sm.sheetName},
				},
			},
		},
	}
	if _, err = sm.srv.Spreadsheets.BatchUpdate(sm.spreadsheetID, req).Do(); err != nil {
		log.Printf("failed to create sheet: %v", err)
		return
	}

	_ = sm.appendRow(sheetHeaders)
	log.Printf("created sheet '%s' with headers", sm.sheetName)
}

func (sm *SheetsManager) appendRow(values []interface{}) error {
	vr := &sheets.ValueRange{Values: [][]interface{}{values}}
	_, err := sm.srv.Spreadsheets.Values.
		Append(sm.spreadsheetID, sm.sheetName, vr).
		ValueInputOption("USER_ENTERED").
		Do()
	return err
}

// LogMeal records a meal entry to Google Sheets.
func (sm *SheetsManager) LogMeal(userID, username, food, calories string) bool {
	if !sm.IsConnected() {
		log.Println("Sheets not connected — skipping meal log")
		return false
	}

	now := time.Now()
	row := []interface{}{
		now.Format("2006-01-02"),
		now.Format("15:04:05"),
		userID,
		username,
		food,
		calories,
		"",
	}

	if err := sm.appendRow(row); err != nil {
		log.Printf("failed to log meal to Sheets: %v", err)
		return false
	}

	log.Printf("meal logged: user=%s food=%s", userID, food)
	return true
}

// GetUserHistory retrieves the last `limit` meal records for a user.
func (sm *SheetsManager) GetUserHistory(userID string, limit int) []map[string]string {
	if !sm.IsConnected() {
		return nil
	}

	rangeStr := fmt.Sprintf("%s!A:G", sm.sheetName)
	resp, err := sm.srv.Spreadsheets.Values.Get(sm.spreadsheetID, rangeStr).Do()
	if err != nil {
		log.Printf("failed to fetch history: %v", err)
		return nil
	}

	if len(resp.Values) < 2 {
		return nil
	}

	colHeaders := make([]string, len(resp.Values[0]))
	for i, h := range resp.Values[0] {
		colHeaders[i] = fmt.Sprint(h)
	}

	var userRows []map[string]string
	for _, row := range resp.Values[1:] {
		record := make(map[string]string)
		for i, h := range colHeaders {
			if i < len(row) {
				record[h] = fmt.Sprint(row[i])
			} else {
				record[h] = ""
			}
		}
		if record["User ID"] == userID {
			userRows = append(userRows, record)
		}
	}

	if len(userRows) > limit {
		userRows = userRows[len(userRows)-limit:]
	}
	return userRows
}

// FormatHistory formats meal records into a readable string.
func (sm *SheetsManager) FormatHistory(records []map[string]string) string {
	if len(records) == 0 {
		return "No meal history found."
	}

	var lines []string
	lines = append(lines, "📋 Recent Meal History\n")
	for i, r := range records {
		lines = append(lines, fmt.Sprintf(
			"%d. [%s %s]\n   🍽️ %s\n   🔥 %s kcal",
			i+1, r["Date"], r["Time"], r["Food"], r["Calories (kcal)"],
		))
	}
	return strings.Join(lines, "\n\n")
}
