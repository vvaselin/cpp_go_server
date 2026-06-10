package app

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

func experimentLogHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	var req ExperimentLogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}

	if req.EventType == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "event_type is required"})
		return
	}

	if supabaseClient == nil {
		log.Println("WARNING: experiment log skipped: Supabase client is not initialized")
		json.NewEncoder(w).Encode(map[string]string{"status": "skipped"})
		return
	}

	eventData := req.EventData
	if eventData == nil {
		eventData = map[string]interface{}{}
	}

	insertData := map[string]interface{}{
		"participant_id": req.ParticipantID,
		"role":           req.Role,
		"session_id":     req.SessionID,
		"task_id":        req.TaskID,
		"event_type":     req.EventType,
		"event_data":     eventData,
	}
	if req.UserID != "" {
		insertData["user_id"] = req.UserID
	}

	var result interface{}
	if err := supabaseClient.DB.From("experiment_events").Insert(insertData).Execute(&result); err != nil {
		log.Printf("ERROR: experiment log insert failed: event_type=%s participant_id=%s err=%v", req.EventType, req.ParticipantID, err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to save experiment log"})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

type lectureViewEventRow struct {
	EventData map[string]interface{} `json:"event_data"`
}

func lectureViewsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	watched := map[string]bool{}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" || supabaseClient == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"watched_lectures": watched})
		return
	}

	var rows []lectureViewEventRow
	err := supabaseClient.DB.From("experiment_events").
		Select("event_data").
		Limit(5000).
		Eq("user_id", userID).
		Eq("event_type", "lecture_view").
		Execute(&rows)
	if err != nil {
		log.Printf("ERROR: lecture views fetch failed: user_id=%s err=%v", userID, err)
		json.NewEncoder(w).Encode(map[string]interface{}{"watched_lectures": watched})
		return
	}

	for _, row := range rows {
		if row.EventData == nil {
			continue
		}
		if lectureNum, ok := parseLectureNum(row.EventData["lecture_num"]); ok {
			watched[strconv.Itoa(lectureNum)] = true
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"watched_lectures": watched})
}

func parseLectureNum(value interface{}) (int, bool) {
	switch v := value.(type) {
	case float64:
		if v >= 1 {
			return int(v), true
		}
	case int:
		if v >= 1 {
			return v, true
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil && n >= 1 {
			return n, true
		}
	}
	return 0, false
}
