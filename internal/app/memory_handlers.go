package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func getMemoryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		json.NewEncoder(w).Encode(UserProfile{
			Summary:   "ユーザーIDが指定されていません。",
			LoveLevel: 0,
		})
		return
	}

	if supabaseClient == nil {
		log.Println("ERROR: Supabase client is not initialized")
		writeJSONError(w, http.StatusInternalServerError, "Database is not configured")
		return
	}

	var profiles []UserProfile
	err := supabaseClient.DB.From("profiles").Select("*").Eq("id", userID).Execute(&profiles)
	if err != nil {
		log.Printf("ERROR: Fetch profile failed: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Database error")
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
		if err := supabaseClient.DB.From("profiles").Insert(newProfile).Execute(nil); err != nil {
			log.Printf("ERROR: Insert profile failed: user_id=%s err=%v", userID, err)
			writeJSONError(w, http.StatusInternalServerError, "Database error")
			return
		}
		json.NewEncoder(w).Encode(newProfile)
	} else {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(profiles[0])
	}
}

func summarizeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req SummarizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.UserID == "" {
		writeJSONError(w, http.StatusBadRequest, "UserID is required")
		return
	}

	if supabaseClient == nil {
		log.Println("ERROR: Supabase client is not initialized")
		writeJSONError(w, http.StatusInternalServerError, "Database is not configured")
		return
	}

	var profiles []UserProfile
	if err := supabaseClient.DB.From("profiles").Select("*").Eq("id", req.UserID).Execute(&profiles); err != nil {
		log.Printf("ERROR: Fetch profile before summarize failed: user_id=%s err=%v", req.UserID, err)
		writeJSONError(w, http.StatusInternalServerError, "Database error")
		return
	}

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
		writeJSONError(w, http.StatusInternalServerError, "AI Error")
		return
	}

	newJSONStr = cleanJSONString(newJSONStr)
	var newProfileData UserProfile
	if err := json.Unmarshal([]byte(newJSONStr), &newProfileData); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "AI parse error")
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
		log.Printf("ERROR: Save profile failed: user_id=%s update=%+v err=%v", req.UserID, updateData, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to save to DB")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(map[string]string{"status": "success"})
}
