package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type AdminProfileRow struct {
	ID            string `json:"id"`
	ParticipantID string `json:"participant_id"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	LoveLevel     int    `json:"love_level"`
	LastUpdated   string `json:"last_updated"`
	LogCount      int    `json:"log_count"`
	LastEventAt   string `json:"last_event_at"`
}

type AdminEventRow struct {
	ID            string                 `json:"id"`
	CreatedAt     string                 `json:"created_at"`
	UserID        string                 `json:"user_id"`
	ParticipantID string                 `json:"participant_id"`
	Role          string                 `json:"role"`
	SessionID     string                 `json:"session_id"`
	TaskID        string                 `json:"task_id"`
	EventType     string                 `json:"event_type"`
	EventData     map[string]interface{} `json:"event_data"`
}

type AdminTaskProgressRow struct {
	UserID    string `json:"user_id"`
	TaskID    string `json:"task_id"`
	HighScore int    `json:"high_score"`
	IsCleared bool   `json:"is_cleared"`
}

type adminEventSummary struct {
	ParticipantID string `json:"participant_id"`
	CreatedAt     string `json:"created_at"`
}

type adminProfileUpdateRequest struct {
	UserID        string `json:"user_id"`
	ParticipantID string `json:"participant_id"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	LoveLevel     *int   `json:"love_level"`
}

type adminTaskProgressUpdateRequest struct {
	UserID    string `json:"user_id"`
	TaskID    string `json:"task_id"`
	HighScore int    `json:"high_score"`
	IsCleared bool   `json:"is_cleared"`
}

type adminDeleteUserRequest struct {
	UserID        string `json:"user_id"`
	ParticipantID string `json:"participant_id"`
	Confirm       string `json:"confirm"`
}

type adminResetRequest struct {
	Confirm string `json:"confirm"`
}

func adminPassword() string {
	return cleanEnvValue(os.Getenv("ADMIN_PASSWORD"))
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	password := adminPassword()
	if password == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "ADMIN_PASSWORD is not configured")
		return false
	}
	if r.Header.Get("X-Admin-Password") != password {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return false
	}
	if supabaseClient == nil {
		writeJSONError(w, http.StatusInternalServerError, "Database is not configured")
		return false
	}
	return true
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return false
	}
	return true
}

func adminProfilesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !requireMethod(w, r, http.MethodGet) || !requireAdmin(w, r) {
		return
	}

	var profiles []UserProfile
	if err := supabaseClient.DB.From("profiles").Select("id,participant_id,name,role,love_level,last_updated").OrderBy("participant_id", "asc").Execute(&profiles); err != nil {
		log.Printf("ERROR: admin profiles fetch failed: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to fetch profiles")
		return
	}

	var summaries []adminEventSummary
	if err := supabaseClient.DB.From("experiment_events").Select("participant_id,created_at").OrderBy("created_at", "desc").Limit(200000).Execute(&summaries); err != nil {
		log.Printf("ERROR: admin event summary fetch failed: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to fetch event summaries")
		return
	}

	counts := map[string]int{}
	lastAt := map[string]string{}
	for _, item := range summaries {
		pid := item.ParticipantID
		if pid == "" {
			continue
		}
		counts[pid]++
		if lastAt[pid] == "" {
			lastAt[pid] = item.CreatedAt
		}
	}

	rows := make([]AdminProfileRow, 0, len(profiles))
	for _, p := range profiles {
		rows = append(rows, AdminProfileRow{
			ID:            p.ID,
			ParticipantID: p.ParticipantID,
			Name:          p.Name,
			Role:          p.Role,
			LoveLevel:     p.LoveLevel,
			LastUpdated:   p.LastUpdated,
			LogCount:      counts[p.ParticipantID],
			LastEventAt:   lastAt[p.ParticipantID],
		})
	}

	writeJSON(w, map[string]interface{}{"profiles": rows})
}

func adminEventsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !requireMethod(w, r, http.MethodGet) || !requireAdmin(w, r) {
		return
	}

	q := r.URL.Query()
	participantID := strings.TrimSpace(q.Get("participant_id"))
	userID := strings.TrimSpace(q.Get("user_id"))
	limit := 500
	if rawLimit := q.Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err == nil && parsed > 0 {
			limit = parsed
		}
	}
	maxLimit := 5000
	if participantID == "" && userID == "" {
		maxLimit = 200000
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	builder := supabaseClient.DB.From("experiment_events").Select("*").OrderBy("created_at", "desc").Limit(limit)
	if participantID != "" {
		builder.Eq("participant_id", participantID)
	} else if userID != "" {
		builder.Eq("user_id", userID)
	}
	if eventType := strings.TrimSpace(q.Get("event_type")); eventType != "" {
		builder.Eq("event_type", eventType)
	}
	if sessionID := strings.TrimSpace(q.Get("session_id")); sessionID != "" {
		builder.Eq("session_id", sessionID)
	}
	if taskID := strings.TrimSpace(q.Get("task_id")); taskID != "" {
		builder.Eq("task_id", taskID)
	}

	var events []AdminEventRow
	if err := builder.Execute(&events); err != nil {
		log.Printf("ERROR: admin events fetch failed: participant_id=%s err=%v", participantID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to fetch events")
		return
	}

	writeJSON(w, map[string]interface{}{"events": events})
}

func adminTaskProgressHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !requireMethod(w, r, http.MethodGet) || !requireAdmin(w, r) {
		return
	}

	participantID := strings.TrimSpace(r.URL.Query().Get("participant_id"))
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if participantID != "" && userID == "" {
		profile, err := fetchAdminProfileByParticipantID(participantID)
		if err != nil {
			log.Printf("ERROR: admin profile lookup failed: participant_id=%s err=%v", participantID, err)
			writeJSONError(w, http.StatusInternalServerError, "Failed to find profile")
			return
		}
		userID = profile.ID
	}

	var rows []AdminTaskProgressRow
	builder := supabaseClient.DB.From("task_progress").Select("user_id,task_id,high_score,is_cleared").OrderBy("task_id", "asc")
	if userID != "" {
		builder.Eq("user_id", userID)
	}
	if err := builder.Execute(&rows); err != nil {
		log.Printf("ERROR: admin task_progress fetch failed: user_id=%s err=%v", userID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to fetch task progress")
		return
	}
	writeJSON(w, map[string]interface{}{"task_progress": rows})
}

func adminProfileUpdateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !requireMethod(w, r, http.MethodPost) || !requireAdmin(w, r) {
		return
	}

	var req adminProfileUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		writeJSONError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	updateData := map[string]interface{}{}
	if strings.TrimSpace(req.Role) != "" {
		role := strings.TrimSpace(req.Role)
		if role != "experimental" && role != "control" {
			writeJSONError(w, http.StatusBadRequest, "role must be experimental or control")
			return
		}
		updateData["role"] = role
	}
	if req.Name != "" {
		updateData["name"] = strings.TrimSpace(req.Name)
	}
	if req.ParticipantID != "" {
		updateData["participant_id"] = strings.ToUpper(strings.TrimSpace(req.ParticipantID))
	}
	if req.LoveLevel != nil {
		if *req.LoveLevel < 0 {
			writeJSONError(w, http.StatusBadRequest, "love_level must be >= 0")
			return
		}
		updateData["love_level"] = *req.LoveLevel
	}
	if len(updateData) == 0 {
		writeJSONError(w, http.StatusBadRequest, "No update fields")
		return
	}

	var result []UserProfile
	if err := supabaseClient.DB.From("profiles").Update(updateData).Eq("id", userID).Execute(&result); err != nil {
		log.Printf("ERROR: admin profile update failed: user_id=%s err=%v", userID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to update profile")
		return
	}
	writeJSON(w, map[string]interface{}{"status": "success", "profile": firstOrNil(result)})
}

func adminTaskProgressUpdateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !requireMethod(w, r, http.MethodPost) || !requireAdmin(w, r) {
		return
	}

	var req adminTaskProgressUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	userID := strings.TrimSpace(req.UserID)
	taskID := strings.TrimSpace(req.TaskID)
	if userID == "" || taskID == "" {
		writeJSONError(w, http.StatusBadRequest, "user_id and task_id are required")
		return
	}
	if req.HighScore < 0 || req.HighScore > 100 {
		writeJSONError(w, http.StatusBadRequest, "high_score must be 0..100")
		return
	}

	updateData := map[string]interface{}{
		"high_score": req.HighScore,
		"is_cleared": req.IsCleared,
	}
	var result []AdminTaskProgressRow
	if err := supabaseClient.DB.From("task_progress").Update(updateData).Eq("user_id", userID).Eq("task_id", taskID).Execute(&result); err != nil {
		log.Printf("ERROR: admin task_progress update failed: user_id=%s task_id=%s err=%v", userID, taskID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to update task progress")
		return
	}
	writeJSON(w, map[string]interface{}{"status": "success", "task_progress": result})
}

func adminDeleteUserHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !requireMethod(w, r, http.MethodPost) || !requireAdmin(w, r) {
		return
	}

	var req adminDeleteUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	userID := strings.TrimSpace(req.UserID)
	participantID := strings.ToUpper(strings.TrimSpace(req.ParticipantID))
	confirm := strings.ToUpper(strings.TrimSpace(req.Confirm))
	if userID == "" {
		writeJSONError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if participantID == "" {
		if confirm != "DELETE_INCOMPLETE" {
			writeJSONError(w, http.StatusBadRequest, "Confirmation must be DELETE_INCOMPLETE for incomplete profiles")
			return
		}
	} else if confirm != participantID {
		writeJSONError(w, http.StatusBadRequest, "Confirmation does not match participant_id")
		return
	}

	if err := deleteByFilter("task_progress", "user_id", userID); err != nil {
		log.Printf("ERROR: delete user failed at task_progress: user_id=%s err=%v", userID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed at step: task_progress")
		return
	}
	if err := deleteByFilter("experiment_events", "user_id", userID); err != nil {
		log.Printf("ERROR: delete user failed at experiment_events user_id: user_id=%s err=%v", userID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed at step: experiment_events by user_id")
		return
	}
	if participantID != "" {
		if err := deleteByFilter("experiment_events", "participant_id", participantID); err != nil {
			log.Printf("ERROR: delete user failed at experiment_events participant_id: participant_id=%s err=%v", participantID, err)
			writeJSONError(w, http.StatusInternalServerError, "Failed at step: experiment_events by participant_id")
			return
		}
	}
	if err := deleteByFilter("profiles", "id", userID); err != nil {
		log.Printf("ERROR: delete user failed at profiles: user_id=%s err=%v", userID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed at step: profiles")
		return
	}
	if err := deleteSupabaseAuthUser(userID); err != nil {
		log.Printf("ERROR: delete user failed at Supabase Auth: user_id=%s err=%v", userID, err)
		writeJSONError(w, http.StatusInternalServerError, "App data deleted, but failed at step: auth user")
		return
	}

	writeJSON(w, map[string]interface{}{"status": "success"})
}

func adminResetTaskProgressHandler(w http.ResponseWriter, r *http.Request) {
	adminResetByRPC(w, r, "RESET_TASK_PROGRESS", "admin_truncate_task_progress")
}

func adminResetExperimentEventsHandler(w http.ResponseWriter, r *http.Request) {
	adminResetByRPC(w, r, "RESET_EXPERIMENT_EVENTS", "admin_truncate_experiment_events")
}

func adminResetByRPC(w http.ResponseWriter, r *http.Request, confirmText string, rpcName string) {
	w.Header().Set("Content-Type", "application/json")
	if !requireMethod(w, r, http.MethodPost) || !requireAdmin(w, r) {
		return
	}
	var req adminResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if strings.TrimSpace(req.Confirm) != confirmText {
		writeJSONError(w, http.StatusBadRequest, "Confirmation text is invalid")
		return
	}
	var result interface{}
	if err := supabaseClient.DB.Rpc(rpcName, map[string]interface{}{}).Execute(&result); err != nil {
		log.Printf("ERROR: admin reset failed: rpc=%s err=%v", rpcName, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to reset data. Did you run supabase/admin_maintenance.sql?")
		return
	}
	writeJSON(w, map[string]interface{}{"status": "success", "result": result})
}

func fetchAdminProfileByParticipantID(participantID string) (*UserProfile, error) {
	var profile UserProfile
	if err := supabaseClient.DB.From("profiles").Select("id,participant_id,name,role,love_level,last_updated").Single().Eq("participant_id", strings.ToUpper(strings.TrimSpace(participantID))).Execute(&profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

func deleteByFilter(table string, column string, value string) error {
	var result interface{}
	return supabaseClient.DB.From(table).Delete().Eq(column, value).Execute(&result)
}

func deleteSupabaseAuthUser(userID string) error {
	supabaseURL := strings.TrimRight(cleanEnvValue(os.Getenv("SUPABASE_URL")), "/")
	supabaseKey := cleanEnvValue(os.Getenv("SUPABASE_KEY"))
	if supabaseURL == "" || supabaseKey == "" {
		return fmt.Errorf("SUPABASE_URL or SUPABASE_KEY is not configured")
	}
	endpoint := fmt.Sprintf("%s/auth/v1/admin/users/%s", supabaseURL, userID)
	req, err := http.NewRequest(http.MethodDelete, endpoint, bytes.NewReader(nil))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		log.Printf("WARNING: Supabase Auth user already missing: user_id=%s body=%s", userID, string(body))
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("auth delete failed: status=%d body=%s", resp.StatusCode, string(body))
	}
	return nil
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(payload)
}

func firstOrNil[T any](items []T) interface{} {
	if len(items) == 0 {
		return nil
	}
	return items[0]
}
