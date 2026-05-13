package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

var sheetHeaders = []interface{}{"Date", "Time", "User ID", "Username", "Food", "Calories (kcal)", "Notes"}

var profileHeaders = []interface{}{"User ID", "Name", "Weight (kg)", "Height (cm)", "Goal", "Daily Calories", "Restrictions", "Notes", "Last Updated"}

// ── In-memory profile cache ───────────────────────────────────────────────────
// Prevents repeated Sheets API calls and survives transient connection errors.

var (
	profileCacheMu sync.RWMutex
	profileCache   = make(map[string]*UserProfile)
)

func cacheGet(userID string) (*UserProfile, bool) {
	profileCacheMu.RLock()
	defer profileCacheMu.RUnlock()
	p, ok := profileCache[userID]
	return p, ok
}

func cacheSet(p *UserProfile) {
	profileCacheMu.Lock()
	defer profileCacheMu.Unlock()
	profileCache[p.UserID] = p
}

// UserProfile holds persistent information about a user.
type UserProfile struct {
	UserID        string
	Name          string
	Weight        string
	Height        string
	Goal          string
	DailyCalories string
	Restrictions  string
	Notes         string
	UpdatedAt     string
}

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
	sm.ensureProfileSheet()
	sm.ensureKnowledgeSheet()
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

// ensureProfileSheet creates the User Profiles sheet if it doesn't exist.
func (sm *SheetsManager) ensureProfileSheet() {
	if !sm.IsConnected() {
		return
	}
	ss, err := sm.srv.Spreadsheets.Get(sm.spreadsheetID).Do()
	if err != nil {
		return
	}
	for _, s := range ss.Sheets {
		if s.Properties.Title == "User Profiles" {
			return
		}
	}
	req := &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{AddSheet: &sheets.AddSheetRequest{
				Properties: &sheets.SheetProperties{Title: "User Profiles"},
			}},
		},
	}
	if _, err = sm.srv.Spreadsheets.BatchUpdate(sm.spreadsheetID, req).Do(); err != nil {
		log.Printf("failed to create User Profiles sheet: %v", err)
		return
	}
	vr := &sheets.ValueRange{Values: [][]interface{}{profileHeaders}}
	_, _ = sm.srv.Spreadsheets.Values.
		Append(sm.spreadsheetID, "User Profiles", vr).
		ValueInputOption("USER_ENTERED").Do()
	log.Println("created 'User Profiles' sheet")
}

// GetUserProfile retrieves the profile for a user.
// Returns from in-memory cache if available; otherwise loads from Sheets and caches it.
func (sm *SheetsManager) GetUserProfile(userID string) *UserProfile {
	// 1. Check memory cache first (fast path — no Sheets API call)
	if p, ok := cacheGet(userID); ok {
		return p
	}

	// 2. Load from Sheets
	empty := &UserProfile{UserID: userID}
	if !sm.IsConnected() {
		cacheSet(empty)
		return empty
	}

	resp, err := sm.srv.Spreadsheets.Values.Get(sm.spreadsheetID, "User Profiles!A:I").Do()
	if err != nil {
		log.Printf("GetUserProfile: sheets error for %s: %v", userID, err)
		cacheSet(empty)
		return empty
	}

	for _, row := range resp.Values[1:] {
		if len(row) > 0 && fmt.Sprint(row[0]) == userID {
			p := &UserProfile{UserID: userID}
			if len(row) > 1 { p.Name = fmt.Sprint(row[1]) }
			if len(row) > 2 { p.Weight = fmt.Sprint(row[2]) }
			if len(row) > 3 { p.Height = fmt.Sprint(row[3]) }
			if len(row) > 4 { p.Goal = fmt.Sprint(row[4]) }
			if len(row) > 5 { p.DailyCalories = fmt.Sprint(row[5]) }
			if len(row) > 6 { p.Restrictions = fmt.Sprint(row[6]) }
			if len(row) > 7 { p.Notes = fmt.Sprint(row[7]) }
			if len(row) > 8 { p.UpdatedAt = fmt.Sprint(row[8]) }
			log.Printf("GetUserProfile: loaded from Sheets for %s (name=%s)", userID, p.Name)
			cacheSet(p)
			return p
		}
	}

	log.Printf("GetUserProfile: no profile found in Sheets for %s", userID)
	cacheSet(empty)
	return empty
}

// SaveUserProfile creates or updates a user's profile row.
// Updates the in-memory cache immediately, then writes to Sheets.
func (sm *SheetsManager) SaveUserProfile(p *UserProfile) bool {
	// Always update cache immediately so in-session reads are instant
	p.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	cacheSet(p)

	if !sm.IsConnected() {
		log.Printf("SaveUserProfile: Sheets not connected, saved to cache only for %s", p.UserID)
		return false
	}

	newRow := []interface{}{
		p.UserID, p.Name, p.Weight, p.Height,
		p.Goal, p.DailyCalories, p.Restrictions, p.Notes, p.UpdatedAt,
	}

	// Find existing row to update
	resp, err := sm.srv.Spreadsheets.Values.Get(sm.spreadsheetID, "User Profiles!A:A").Do()
	if err == nil {
		for i, row := range resp.Values {
			if len(row) > 0 && fmt.Sprint(row[0]) == p.UserID {
				rowNum := i + 1
				rangeStr := fmt.Sprintf("User Profiles!A%d:I%d", rowNum, rowNum)
				vr := &sheets.ValueRange{Values: [][]interface{}{newRow}}
				_, err = sm.srv.Spreadsheets.Values.Update(sm.spreadsheetID, rangeStr, vr).
					ValueInputOption("USER_ENTERED").Do()
				if err != nil {
					log.Printf("SaveUserProfile: update failed for %s: %v", p.UserID, err)
					return false
				}
				log.Printf("SaveUserProfile: updated Sheets for %s (name=%s weight=%s)", p.UserID, p.Name, p.Weight)
				return true
			}
		}
	}

	// New user — append row
	vr := &sheets.ValueRange{Values: [][]interface{}{newRow}}
	_, err = sm.srv.Spreadsheets.Values.
		Append(sm.spreadsheetID, "User Profiles", vr).
		ValueInputOption("USER_ENTERED").Do()
	if err != nil {
		log.Printf("SaveUserProfile: append failed for %s: %v", p.UserID, err)
		return false
	}
	log.Printf("SaveUserProfile: created new row in Sheets for %s", p.UserID)
	return true
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

// ── Learned Knowledge ────────────────────────────────────────────────────────

// ensureKnowledgeSheet creates the "Learned Knowledge" sheet if it doesn't exist.
func (sm *SheetsManager) ensureKnowledgeSheet() {
	if !sm.IsConnected() {
		return
	}
	ss, err := sm.srv.Spreadsheets.Get(sm.spreadsheetID).Do()
	if err != nil {
		return
	}
	for _, s := range ss.Sheets {
		if s.Properties.Title == "Learned Knowledge" {
			return
		}
	}
	req := &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{AddSheet: &sheets.AddSheetRequest{
				Properties: &sheets.SheetProperties{Title: "Learned Knowledge"},
			}},
		},
	}
	if _, err = sm.srv.Spreadsheets.BatchUpdate(sm.spreadsheetID, req).Do(); err != nil {
		return
	}
	headers := &sheets.ValueRange{Values: [][]interface{}{{"Date", "Topic", "Knowledge", "Source UserID"}}}
	_, _ = sm.srv.Spreadsheets.Values.
		Append(sm.spreadsheetID, "Learned Knowledge", headers).
		ValueInputOption("USER_ENTERED").Do()
	log.Println("created 'Learned Knowledge' sheet")
}

// SaveLearned appends a new knowledge entry to the Learned Knowledge sheet.
func (sm *SheetsManager) SaveLearned(topic, knowledge, sourceUserID string) bool {
	if !sm.IsConnected() {
		return false
	}
	row := &sheets.ValueRange{Values: [][]interface{}{{
		time.Now().Format("2006-01-02 15:04:05"),
		topic,
		knowledge,
		sourceUserID,
	}}}
	_, err := sm.srv.Spreadsheets.Values.
		Append(sm.spreadsheetID, "Learned Knowledge", row).
		ValueInputOption("USER_ENTERED").Do()
	if err != nil {
		log.Printf("SaveLearned: failed: %v", err)
		return false
	}
	log.Printf("SaveLearned: saved topic=%s", topic)
	return true
}

// LoadLearnedKnowledge returns all learned knowledge as a single string for the knowledge base.
func (sm *SheetsManager) LoadLearnedKnowledge() string {
	if !sm.IsConnected() {
		return ""
	}
	resp, err := sm.srv.Spreadsheets.Values.Get(sm.spreadsheetID, "Learned Knowledge!A:D").Do()
	if err != nil || len(resp.Values) < 2 {
		return ""
	}
	var lines []string
	for _, row := range resp.Values[1:] {
		if len(row) >= 3 {
			topic := fmt.Sprint(row[1])
			knowledge := fmt.Sprint(row[2])
			if topic != "" && knowledge != "" {
				lines = append(lines, fmt.Sprintf("- [%s] %s", topic, knowledge))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "## ความรู้ที่สะสมจากการสนทนา\n" + strings.Join(lines, "\n")
}

// FormatHistory formats meal records into a readable string.
func (sm *SheetsManager) FormatHistory(records []map[string]string) string {
	if len(records) == 0 {
		return "📋 ยังไม่มีประวัติมื้ออาหารค่ะ\n\nลองส่งรูปอาหารหรือบอกว่ากินอะไรเพื่อเริ่มบันทึกได้เลยนะคะ 😊"
	}

	var lines []string
	lines = append(lines, "📋 ประวัติมื้ออาหารล่าสุด\n")
	for i, r := range records {
		lines = append(lines, fmt.Sprintf(
			"%d. [%s %s]\n   🍽️ %s\n   🔥 %s kcal",
			i+1, r["Date"], r["Time"], r["Food"], r["Calories (kcal)"],
		))
	}
	return strings.Join(lines, "\n\n")
}
