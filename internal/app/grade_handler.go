package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

func gradeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var p GradePayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	userMessage := fmt.Sprintf(
		"縲占ｪｲ鬘後曾n%s\n\n縲先Φ螳壼・蜉帙曾n%s\n\n縲先署蜃ｺ繧ｳ繝ｼ繝峨曾n%s\n\n縲仙ｮ滄圀縺ｮ螳溯｡悟・蜉帙曾n%s",
		p.TaskDesc, p.ExpectedOutput, p.Code, p.Output,
	)

	aiResponseStr, err := callOpenAI(gradeSystemPrompt, userMessage, false)
	if err != nil {
		http.Error(w, "AI Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	aiResponseStr = cleanJSONString(aiResponseStr)
	var gradeRes GradeResponse
	if err := json.Unmarshal([]byte(aiResponseStr), &gradeRes); err != nil {
		log.Printf("ERROR: grade JSON parse failed: %v\nRaw: %s", err, aiResponseStr)
		http.Error(w, "AI response parse error", http.StatusInternalServerError)
		return
	}

	currentScore := gradeRes.Score
	bonusLove := 0
	isNewRecord := false

	if supabaseClient != nil && p.UserID != "" && p.TaskID != "" {
		var existing []UserTaskProgress
		supabaseClient.DB.From("task_progress").Select("*").
			Eq("user_id", p.UserID).Eq("task_id", p.TaskID).Execute(&existing)

		if len(existing) > 0 {
			prev := existing[0]
			if currentScore > prev.HighScore {
				isNewRecord = true
				if currentScore >= 80 {
					bonusLove = 5
				}
				updateData := map[string]interface{}{
					"high_score": currentScore,
					"is_cleared": currentScore >= 80,
				}
				var updateResult interface{}
				supabaseClient.DB.From("task_progress").Update(updateData).
					Eq("user_id", p.UserID).Eq("task_id", p.TaskID).Execute(&updateResult)
			}
		} else {
			if currentScore >= 80 {
				bonusLove = 5
			}
			newData := map[string]interface{}{
				"user_id":    p.UserID,
				"task_id":    p.TaskID,
				"high_score": currentScore,
				"is_cleared": currentScore >= 80,
			}
			var insertResult interface{}
			inErr := supabaseClient.DB.From("task_progress").Insert(newData).Execute(&insertResult)
			if inErr != nil {
				log.Printf("ERROR: Supabase Insert failed: %v", inErr)
			}
		}
	}

	responseMap := map[string]interface{}{
		"score":         gradeRes.Score,
		"reason":        gradeRes.Reason,
		"improvement":   gradeRes.Improvement,
		"bonus_love":    bonusLove,
		"is_new_record": isNewRecord,
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(responseMap)
}
