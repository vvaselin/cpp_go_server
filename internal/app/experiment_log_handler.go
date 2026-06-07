package app

import (
	"encoding/json"
	"log"
	"net/http"
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
