package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

func getMemoryHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		json.NewEncoder(w).Encode(UserProfile{
			Summary:   "ユーザーIDが指定されていません。",
			LoveLevel: 0,
		})
		return
	}

	var profiles []UserProfile
	err := supabaseClient.DB.From("profiles").Select("*").Eq("id", userID).Execute(&profiles)
	if err != nil {
		log.Printf("ERROR: Fetch profile failed: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if len(profiles) == 0 {
		newProfile := UserProfile{
			ID:            userID,
			LoveLevel:     0,
			Summary:       "初めまして。これからよろしくお願いします。",
			LearnedTopics: []string{},
			Weaknesses:    []string{},
			LastUpdated:   time.Now().Format("2006-01-02 15:04:05"),
		}
		supabaseClient.DB.From("profiles").Insert(newProfile).Execute(nil)
		json.NewEncoder(w).Encode(newProfile)
	} else {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(profiles[0])
	}
}

func summarizeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SummarizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.UserID == "" {
		http.Error(w, "UserID is required", http.StatusBadRequest)
		return
	}

	var profiles []UserProfile
	supabaseClient.DB.From("profiles").Select("*").Eq("id", req.UserID).Execute(&profiles)

	var currentMem UserProfile
	if len(profiles) > 0 {
		currentMem = profiles[0]
	}

	logText := ""
	for _, item := range req.ChatLog {
		logText += fmt.Sprintf("%s: %s\n", item.Username, item.Message)
	}

	currentMemJSON, _ := json.Marshal(currentMem)
	userPrompt := fmt.Sprintf(`
[Current Memory JSON]
%s

[Recent Chat Log]
%s
`, string(currentMemJSON), logText)

	newJSONStr, err := callOpenAI(summarySystemPrompt, userPrompt, true)
	if err != nil {
		http.Error(w, "AI Error", http.StatusInternalServerError)
		return
	}

	newJSONStr = cleanJSONString(newJSONStr)
	var newProfileData UserProfile
	if err := json.Unmarshal([]byte(newJSONStr), &newProfileData); err != nil {
		http.Error(w, "AI parse error", http.StatusInternalServerError)
		return
	}

	newProfileData.ID = req.UserID
	newProfileData.LastUpdated = time.Now().Format("2006-01-02 15:04:05")

	updateData := map[string]interface{}{
		"summary":        newProfileData.Summary,
		"learned_topics": newProfileData.LearnedTopics,
		"weaknesses":     newProfileData.Weaknesses,
		"last_updated":   time.Now().Format("2006-01-02 15:04:05"),
	}

	err = supabaseClient.DB.From("profiles").Update(updateData).Eq("id", req.UserID).Execute(nil)
	if err != nil {
		log.Printf("ERROR: Save profile failed: %v", err)
		http.Error(w, "Failed to save to DB", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(map[string]string{"status": "success"})
}
