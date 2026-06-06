package app

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		allowedOrigins := []string{
			"http://localhost:8088",
			"https://lab.wasabi-apple.com",
		}
		for _, allowed := range allowedOrigins {
			if origin == allowed {
				return true
			}
		}
		log.Printf("WARNING(WS): rejected origin: %s", origin)
		return false
	},
}

func chatWSHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ERROR(WS): upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	chatProvider, err := getChatProvider()
	if err != nil {
		log.Printf("ERROR: chat provider setup failed: %v", err)
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "API key not configured"))
		return
	}

	var chatHistory []OpenAIMessage
	const maxHistoryLen = 20

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("ERROR(WS): unexpected close: %v", err)
			} else {
				log.Printf("INFO(WS): connection closed: %s", r.RemoteAddr)
			}
			break
		}

		var payload ChatPayload
		if err := json.Unmarshal(msgBytes, &payload); err != nil {
			log.Printf("ERROR(WS): invalid JSON: %v", err)
			conn.WriteJSON(WSStreamMessage{Type: "done", Text: "リクエストの解析に失敗しました。", Emotion: "sad"})
			continue
		}

		chatRes, err := buildChatResponseStream(payload, chatProvider, chatHistory, conn)
		if err != nil {
			log.Printf("ERROR(WS): AI response generation failed: %v", err)
			conn.WriteJSON(WSStreamMessage{Type: "done", Text: "AIとの通信に失敗しました。", Emotion: "sad"})
			continue
		}

		log.Printf("[Stream] params=%+v emo=%s text=%s", chatRes.Parameters, chatRes.Emotion, chatRes.Text)

		chatHistory = append(chatHistory, OpenAIMessage{Role: "user", Content: payload.Message})
		chatHistory = append(chatHistory, OpenAIMessage{Role: "assistant", Content: chatRes.Text})
		if len(chatHistory) > maxHistoryLen {
			chatHistory = chatHistory[len(chatHistory)-maxHistoryLen:]
		}
	}
}

func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST method only", http.StatusMethodNotAllowed)
		return
	}

	var payload ChatPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Printf("ERROR(/api/chat): invalid JSON: %v", err)
		http.Error(w, "Bad Request: Invalid JSON", http.StatusBadRequest)
		return
	}

	chatProvider, err := getChatProvider()
	if err != nil {
		log.Printf("ERROR(/api/chat): chat provider setup failed: %v", err)
		http.Error(w, "Internal Server Error: API key not configured", http.StatusInternalServerError)
		return
	}

	chatRes, err := buildChatResponse(payload, chatProvider, nil)
	if err != nil {
		log.Printf("ERROR(/api/chat): %v", err)
		http.Error(w, "Failed to communicate with AI", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chatRes)
}
