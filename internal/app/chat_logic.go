package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func fetchUserProfile(userID string) UserProfile {
	var userMem UserProfile
	if userID == "" || supabaseClient == nil {
		return userMem
	}

	var profiles []UserProfile
	if err := supabaseClient.DB.From("profiles").Select("*").Eq("id", userID).Execute(&profiles); err != nil {
		log.Printf("WARNING: Supabase profile fetch failed: %v", err)
		return userMem
	}
	if len(profiles) > 0 {
		userMem = profiles[0]
	}
	return userMem
}

func profilePromptValues(userMem UserProfile) (string, string, string) {
	userName := strings.TrimSpace(userMem.Name)
	if userName == "" {
		userName = "あなた"
	}

	memoryText := "まだ情報がありません。"
	if userMem.Summary != "" {
		memoryText = userMem.Summary
	}

	weaknessText := "特になし"
	if len(userMem.Weaknesses) > 0 {
		weaknessText = strings.Join(userMem.Weaknesses, ", ")
	}
	return userName, memoryText, weaknessText
}

func buildChatPrompt(payload ChatPayload, history []OpenAIMessage, mode string) (string, []OpenAIMessage) {
	userMem := fetchUserProfile(payload.UserID)
	userName, memoryText, weaknessText := profilePromptValues(userMem)

	systemPrompt := buildSystemPrompt(payload.CharacterID, mode, payload.LoveLevel)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{user_name}}", userName)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{user_memory}}", memoryText)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{user_weaknesses}}", weaknessText)

	prevParamsJSON, _ := json.Marshal(payload.PrevParams)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{prev_params}}", string(prevParamsJSON))
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{prev_output}}", payload.PrevOutput)
	validateTemplateVars(systemPrompt)

	userContent := fmt.Sprintf(
		"[Current Task]\n%s\n\n[User Code]\n%s\n\n[User Message]\n%s",
		payload.Task,
		payload.Code,
		payload.Message,
	)

	messages := make([]OpenAIMessage, 0, len(history)+1)
	messages = append(messages, history...)
	messages = append(messages, OpenAIMessage{Role: "user", Content: userContent})
	return systemPrompt, messages
}

func parseChatAIContent(aiRawContent string, streaming bool) ChatResponse {
	aiCleanContent := cleanJSONString(aiRawContent)
	var chatRes ChatResponse
	if err := json.Unmarshal([]byte(aiCleanContent), &chatRes); err != nil {
		if streaming {
			log.Printf("WARNING: streaming AI response could not be parsed as JSON. raw: %s", aiCleanContent)
		} else {
			log.Printf("WARNING: AI response could not be parsed as JSON. raw: %s", aiCleanContent)
		}
		chatRes = ChatResponse{Text: aiCleanContent, Emotion: "normal", LoveUp: 0}
	}

	if os.Getenv("AI_DEBUG_MODE") == "true" && chatRes.Thought != "" {
		log.Printf("Thought: %s", chatRes.Thought)
		log.Printf("Params: %+v", chatRes.Parameters)
		log.Printf("LoveValue: %d", chatRes.LoveUp)
	}
	return chatRes
}

func buildChatResponse(payload ChatPayload, provider ChatProvider, history []OpenAIMessage) (ChatResponse, error) {
	systemPrompt, messages := buildChatPrompt(payload, history, "thought")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	aiRawContent, err := provider.GenerateChat(ctx, systemPrompt, messages)
	if err != nil {
		return ChatResponse{}, err
	}
	return parseChatAIContent(aiRawContent, false), nil
}

func buildChatResponseStream(payload ChatPayload, provider ChatProvider, history []OpenAIMessage, conn *websocket.Conn) (ChatResponse, error) {
	systemPrompt, messages := buildChatPrompt(payload, history, "stream")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var lastSentTextLen int
	aiRawContent, err := provider.StreamChat(ctx, systemPrompt, messages, func(accumulated string) error {
		currentText := extractPartialTextField(accumulated)
		if len(currentText) <= lastSentTextLen {
			return nil
		}

		delta := currentText[lastSentTextLen:]
		lastSentTextLen = len(currentText)
		if err := conn.WriteJSON(WSStreamMessage{Type: "chunk", Delta: delta}); err != nil {
			log.Printf("ERROR(WS): chunk send failed: %v", err)
			return fmt.Errorf("WebSocket chunk send failed: %w", err)
		}
		return nil
	})
	if err != nil {
		return ChatResponse{}, err
	}

	chatRes := parseChatAIContent(aiRawContent, true)
	if strings.TrimSpace(chatRes.Text) == "" {
		log.Printf("WARNING: AI response text field is empty. raw: %s", cleanJSONString(aiRawContent))
		chatRes.Text = "ごめん、うまく言葉にできなかった... もう一度聞いてくれる？"
	}

	doneMsg := WSStreamMessage{
		Type:       "done",
		Text:       chatRes.Text,
		Emotion:    chatRes.Emotion,
		LoveUp:     chatRes.LoveUp,
		Thought:    chatRes.Thought,
		Parameters: chatRes.Parameters,
	}
	if err := conn.WriteJSON(doneMsg); err != nil {
		log.Printf("ERROR(WS): done send failed: %v", err)
		return chatRes, fmt.Errorf("WebSocket done send failed: %w", err)
	}

	return chatRes, nil
}

func extractPartialTextField(partial string) string {
	searchPatterns := []string{`"text": "`, `"text":"`}
	textStart := -1
	patternLen := 0

	for _, pat := range searchPatterns {
		searchEnd := len(partial)
		for {
			idx := strings.LastIndex(partial[:searchEnd], pat)
			if idx == -1 {
				break
			}
			if idx > 0 && partial[idx-1] == '\\' {
				searchEnd = idx
				continue
			}
			if idx > textStart {
				textStart = idx
				patternLen = len(pat)
			}
			break
		}
	}

	if textStart == -1 {
		return ""
	}

	valueStart := textStart + patternLen
	if valueStart >= len(partial) {
		return ""
	}

	var result strings.Builder
	for i := valueStart; i < len(partial); {
		ch := partial[i]
		if ch == '\\' && i+1 < len(partial) {
			next := partial[i+1]
			switch next {
			case '"':
				result.WriteByte('"')
			case '\\':
				result.WriteByte('\\')
			case 'n':
				result.WriteByte('\n')
			case 'r':
				result.WriteByte('\r')
			case 't':
				result.WriteByte('\t')
			default:
				result.WriteByte('\\')
				result.WriteByte(next)
			}
			i += 2
		} else if ch == '"' {
			break
		} else {
			result.WriteByte(ch)
			i++
		}
	}

	return result.String()
}
