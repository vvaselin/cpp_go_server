package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

//================================================================
// HTTP ハンドラ (各URLの処理本体)
//================================================================

// --- C++実行ハンドラ ---
func executeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST method only", http.StatusMethodNotAllowed)
		return
	}

	var payload CodePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Printf("ERROR(/api/execute): 不正なJSONを受信: %v", err)
		sendErrorJSON(w, "不正なリクエストです")
		return
	}

	// 一時ディレクトリを作成
	dir, err := os.MkdirTemp("", "cpp-execution-")
	if err != nil {
		log.Printf("ERROR: 一時ディレクトリの作成に失敗: %v", err)
		sendErrorJSON(w, "サーバー内部エラー: 一時ディレクトリ作成失敗")
		return
	}
	defer os.RemoveAll(dir)
	// log.Printf("INFO:: 一時ディレクトリを作成: %s", dir)

	// C++コードを一時ディレクトリに書き出す
	if err := os.WriteFile(filepath.Join(dir, "main.cpp"), []byte(payload.Code), 0666); err != nil {
		log.Printf("ERROR: main.cpp書き込みに失敗: %v", err)
		sendErrorJSON(w, "サーバー内部エラー: ファイル書き込み失敗")
		return
	}

	// 10秒間のタイムアウトを設定
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// コンテナ内で実行するコマンド
	compileAndRunScript := "g++ -Wall /usr/src/app/main.cpp -o /usr/src/app/main.out && /usr/src/app/main.out"

	// ホストの一時ディレクトリをコンテナの /usr/src/app にマウントして実行
	log.Printf("INFO: Dockerコンテナを実行...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"--rm",
		"-i",
		"--net=none",
		"-v", fmt.Sprintf("%s:/usr/src/app", dir),
		"gcc:latest",
		"sh", "-c", compileAndRunScript,
	)

	if payload.Stdin != "" {
		runCmd.Stdin = strings.NewReader(payload.Stdin)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	runCmd.Stdout = &out
	runCmd.Stderr = &stderr
	err = runCmd.Run()

	// タイムアウトの場合
	if ctx.Err() == context.DeadlineExceeded {
		log.Println("ERROR: Docker run timed out")
		sendErrorJSON(w, "実行がタイムアウトしました（10秒超過）")
		return
	}

	// その他の実行エラー（コンパイルエラーなど）
	if err != nil {
		// log.Printf("ERROR: C++実行失敗: %v\n標準エラー: %s", err, stderr.String())
		sendErrorJSON(w, stderr.String())
		return
	}

	// 成功した結果を返す
	// log.Printf("INFO: C++実行成功: %s", out.String())
	response := ResultPayload{Result: out.String()}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(response)
}

// エラーでも200 + JSONで返すヘルパー関数
func sendErrorJSON(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(ResultPayload{Result: "エラー:\n" + errMsg})
}

//================================================================
// WebSocket チャットハンドラ
//================================================================

// wsUpgrader: HTTPをWebSocketにアップグレードする設定
// CheckOriginで許可するオリジンをcorsMiddlewareと合わせる
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		// corsMiddlewareと同じ許可リスト
		allowedOrigins := []string{
			"http://localhost:8088",
			"https://lab.wasabi-apple.com",
		}
		for _, allowed := range allowedOrigins {
			if origin == allowed {
				return true
			}
		}
		log.Printf("WARNING(WS): 許可されていないオリジンからの接続: %s", origin)
		return false
	},
}

// chatWSHandler: /api/chat/ws へのWebSocket接続を処理する (ストリーミング対応版)
//
// 接続フロー:
//  1. クライアントがWSを開く (一度だけ)
//  2. クライアントがChatPayload JSONをテキストフレームで送信
//  3. サーバーがOpenAIをストリーミングで呼び出し:
//     - テキストが生成されるたびに {type:"chunk", delta:"..."} を送信
//     - 完了時に {type:"done", text:"...", emotion:"...", ...} を送信
//  4. 2〜3を繰り返す（接続は維持）
func chatWSHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ERROR(WS): アップグレード失敗: %v", err)
		return
	}
	defer conn.Close()

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Println("ERROR: 'OPENAI_API_KEY'が設定されていません")
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "API key not configured"))
		return
	}

	// このWebSocket接続の会話履歴（コネクション単位で保持）
	var chatHistory []OpenAIMessage
	const maxHistoryLen = 20 // 最大保持数（10往復分）

	for {
		// クライアントからメッセージを受信
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("ERROR(WS): 予期しない切断: %v", err)
			} else {
				log.Printf("INFO(WS): 接続終了: %s", r.RemoteAddr)
			}
			break
		}

		// ペイロードをデコード
		var payload ChatPayload
		if err := json.Unmarshal(msgBytes, &payload); err != nil {
			log.Printf("ERROR(WS): 不正なJSONを受信: %v", err)
			errMsg := WSStreamMessage{Type: "done", Text: "リクエストの解析に失敗しました。", Emotion: "sad"}
			conn.WriteJSON(errMsg)
			continue
		}

		// ストリーミングでAIレスポンスを生成
		// ※ 切り替え: useStreaming を false にすると従来の一括レスポンスになる
		useStreaming := true

		var chatRes ChatResponse
		if useStreaming {
			chatRes, err = buildChatResponseStream(payload, apiKey, chatHistory, conn)
		} else {
			chatRes, err = buildChatResponse(payload, apiKey, chatHistory)
			if err == nil {
				// 非ストリーミング時は自分でWSに送信する
				conn.WriteJSON(chatRes)
			}
		}
		if err != nil {
			log.Printf("ERROR(WS): AIレスポンス生成失敗: %v", err)
			errMsg := WSStreamMessage{Type: "done", Text: "AIとの通信に失敗しました。", Emotion: "sad"}
			conn.WriteJSON(errMsg)
			continue
		}

		log.Printf("[Stream] params=%+v emo=%s", chatRes.Parameters, chatRes.Emotion)

		// 会話履歴に今回のやり取りを追加
		chatHistory = append(chatHistory, OpenAIMessage{
			Role:    "user",
			Content: payload.Message,
		})
		chatHistory = append(chatHistory, OpenAIMessage{
			Role:    "assistant",
			Content: chatRes.Text,
		})

		// 履歴が上限を超えたら古い方から削除
		if len(chatHistory) > maxHistoryLen {
			chatHistory = chatHistory[len(chatHistory)-maxHistoryLen:]
		}
	}
}

// buildChatResponse: ChatPayloadからAIレスポンスを構築する共通ロジック
// chatWSHandlerから呼び出される（旧chatHandlerのロジックを切り出したもの）
func buildChatResponse(payload ChatPayload, apiKey string, history []OpenAIMessage) (ChatResponse, error) {
	// Supabaseからユーザーメモリを取得
	var userMem UserProfile
	if payload.UserID != "" && supabaseClient != nil {
		var profiles []UserProfile
		supabaseClient.DB.From("profiles").Select("*").Eq("id", payload.UserID).Execute(&profiles)
		if len(profiles) > 0 {
			userMem = profiles[0]
		}
	}

	memoryText := "まだ情報がありません。"
	if userMem.Summary != "" {
		memoryText = userMem.Summary
	}
	weaknessText := "特になし"
	if len(userMem.Weaknesses) > 0 {
		weaknessText = strings.Join(userMem.Weaknesses, ", ")
	}

	// システムプロンプトを構築
	currentSystemPrompt := buildSystemPrompt(payload.CharacterID, "thought", payload.LoveLevel)
	currentSystemPrompt = strings.ReplaceAll(currentSystemPrompt, "{{user_memory}}", memoryText)
	currentSystemPrompt = strings.ReplaceAll(currentSystemPrompt, "{{user_weaknesses}}", weaknessText)

	prevParamsJSON, _ := json.Marshal(payload.PrevParams)
	currentSystemPrompt = strings.ReplaceAll(currentSystemPrompt, "{{prev_params}}", string(prevParamsJSON))
	currentSystemPrompt = strings.ReplaceAll(currentSystemPrompt, "{{prev_output}}", payload.PrevOutput)

	// テンプレート変数の未解決チェック
	validateTemplateVars(currentSystemPrompt)

	// ユーザーコンテンツを構築
	userContent := fmt.Sprintf(
		"【現在の課題】\n%s\n\n【ユーザーのコード】\n%s\n\n【ユーザーのメッセージ】\n%s",
		payload.Task,
		payload.Code,
		payload.Message,
	)

	// メッセージ配列を構築: system → 過去の会話履歴 → 今回のユーザー入力
	reqMessages := []OpenAIMessage{
		{Role: "system", Content: currentSystemPrompt},
	}
	reqMessages = append(reqMessages, history...)
	reqMessages = append(reqMessages, OpenAIMessage{Role: "user", Content: userContent})

	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini" // デフォルト
	}

	reqBody := OpenAIRequest{
		Model:          model,
		Messages:       reqMessages,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("OpenAIリクエストのMarshal失敗: %w", err)
	}

	// OpenAI APIへリクエスト送信 (30秒タイムアウト)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBytes))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("OpenAIリクエスト作成失敗: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("OpenAIへの送信失敗: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return ChatResponse{}, fmt.Errorf("OpenAI APIエラー: %d %s", resp.StatusCode, string(bodyBytes))
	}

	// レスポンスをパース
	var openAIResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return ChatResponse{}, fmt.Errorf("OpenAIレスポンスのデコード失敗: %w", err)
	}

	aiRawContent := ""
	if len(openAIResp.Choices) > 0 {
		aiRawContent = openAIResp.Choices[0].Message.Content
	}

	aiCleanContent := cleanJSONString(aiRawContent)
	var chatRes ChatResponse
	if err := json.Unmarshal([]byte(aiCleanContent), &chatRes); err != nil {
		log.Printf("WARNING: AIの応答がJSONとしてパースできませんでした。Raw: %s", aiCleanContent)
		chatRes = ChatResponse{
			Text:    aiCleanContent,
			Emotion: "normal",
			LoveUp:  0,
		}
	}

	if os.Getenv("AI_DEBUG_MODE") == "true" {
		if chatRes.Thought != "" {
			log.Printf("Thought: %s", chatRes.Thought)
			log.Printf("Params: %+v", chatRes.Parameters)
			log.Printf("LoveValue: %d", chatRes.LoveUp)
		}
	}

	return chatRes, nil
}

// buildChatResponseStream: OpenAI Streaming APIを使い、テキストを逐次WebSocket送信する
// チャンク送信中は {type:"chunk", delta:"..."} を送り、
// 完了時に {type:"done", ...fullResponse} を送る
func buildChatResponseStream(payload ChatPayload, apiKey string, history []OpenAIMessage, conn *websocket.Conn) (ChatResponse, error) {
	// --- システムプロンプト構築 (buildChatResponseと同じ) ---
	var userMem UserProfile
	if payload.UserID != "" && supabaseClient != nil {
		var profiles []UserProfile
		supabaseClient.DB.From("profiles").Select("*").Eq("id", payload.UserID).Execute(&profiles)
		if len(profiles) > 0 {
			userMem = profiles[0]
		}
	}

	memoryText := "まだ情報がありません。"
	if userMem.Summary != "" {
		memoryText = userMem.Summary
	}
	weaknessText := "特になし"
	if len(userMem.Weaknesses) > 0 {
		weaknessText = strings.Join(userMem.Weaknesses, ", ")
	}

	currentSystemPrompt := buildSystemPrompt(payload.CharacterID, "stream", payload.LoveLevel)
	currentSystemPrompt = strings.ReplaceAll(currentSystemPrompt, "{{user_memory}}", memoryText)
	currentSystemPrompt = strings.ReplaceAll(currentSystemPrompt, "{{user_weaknesses}}", weaknessText)

	prevParamsJSON, _ := json.Marshal(payload.PrevParams)
	currentSystemPrompt = strings.ReplaceAll(currentSystemPrompt, "{{prev_params}}", string(prevParamsJSON))
	currentSystemPrompt = strings.ReplaceAll(currentSystemPrompt, "{{prev_output}}", payload.PrevOutput)
	validateTemplateVars(currentSystemPrompt)

	userContent := fmt.Sprintf(
		"【現在の課題】\n%s\n\n【ユーザーのコード】\n%s\n\n【ユーザーのメッセージ】\n%s",
		payload.Task,
		payload.Code,
		payload.Message,
	)

	reqMessages := []OpenAIMessage{
		{Role: "system", Content: currentSystemPrompt},
	}
	reqMessages = append(reqMessages, history...)
	reqMessages = append(reqMessages, OpenAIMessage{Role: "user", Content: userContent})

	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini" // デフォルト
	}

	// --- ストリーミングリクエスト送信 ---
	reqBody := OpenAIStreamRequest{
		Model:          model,
		Messages:       reqMessages,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
		Stream:         true,
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("OpenAIリクエストのMarshal失敗: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBytes))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("OpenAIリクエスト作成失敗: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("OpenAIへの送信失敗: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return ChatResponse{}, fmt.Errorf("OpenAI APIエラー: %d %s", resp.StatusCode, string(bodyBytes))
	}

	// --- SSEストリームの読み取りとチャンク送信 ---
	var accumulated strings.Builder
	var lastSentTextLen int

	scanner := bufio.NewScanner(resp.Body)
	// SSEラインの最大サイズを拡大
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// SSE形式: "data: {...}" のみ処理
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		// ストリーム終了マーカー
		if data == "[DONE]" {
			break
		}

		// チャンクをパース
		var chunk OpenAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // パース失敗は無視
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		content := chunk.Choices[0].Delta.Content
		if content == "" {
			continue
		}

		accumulated.WriteString(content)

		// 蓄積されたJSONから "text" フィールドの値を部分的に抽出
		currentText := extractPartialTextField(accumulated.String())
		if len(currentText) > lastSentTextLen {
			// 新しい差分のみ送信
			delta := currentText[lastSentTextLen:]
			lastSentTextLen = len(currentText)

			chunkMsg := WSStreamMessage{
				Type:  "chunk",
				Delta: delta,
			}
			if err := conn.WriteJSON(chunkMsg); err != nil {
				log.Printf("ERROR(WS): チャンク送信失敗: %v", err)
				return ChatResponse{}, fmt.Errorf("WebSocketチャンク送信失敗: %w", err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("SSEストリーム読み取りエラー: %w", err)
	}

	// --- 完了: フルJSONをパースしてメタデータ込みで送信 ---
	aiRawContent := accumulated.String()
	aiCleanContent := cleanJSONString(aiRawContent)

	var chatRes ChatResponse
	if err := json.Unmarshal([]byte(aiCleanContent), &chatRes); err != nil {
		log.Printf("WARNING: ストリーミング応答のJSONパース失敗。Raw: %s", aiCleanContent)
		chatRes = ChatResponse{
			Text:    aiCleanContent,
			Emotion: "normal",
			LoveUp:  0,
		}
	}

	if os.Getenv("AI_DEBUG_MODE") == "true" && chatRes.Thought != "" {
		log.Printf("Thought: %s", chatRes.Thought)
		log.Printf("Params: %+v", chatRes.Parameters)
		log.Printf("LoveValue: %d", chatRes.LoveUp)
	}

	// テキストが空の場合はフォールバック（gpt-4oで稀に発生）
	if strings.TrimSpace(chatRes.Text) == "" {
		log.Printf("WARNING: AIレスポンスのtextフィールドが空です。Raw: %s", aiCleanContent)
		chatRes.Text = "（ごめん、うまく言葉にできなかった…もう一度聞いてくれる？）"
	}

	// done メッセージを送信
	doneMsg := WSStreamMessage{
		Type:       "done",
		Text:       chatRes.Text,
		Emotion:    chatRes.Emotion,
		LoveUp:     chatRes.LoveUp,
		Thought:    chatRes.Thought,
		Parameters: chatRes.Parameters,
	}
	if err := conn.WriteJSON(doneMsg); err != nil {
		log.Printf("ERROR(WS): done送信失敗: %v", err)
		return chatRes, fmt.Errorf("WebSocket done送信失敗: %w", err)
	}

	return chatRes, nil
}

// extractPartialTextField は蓄積中のJSON文字列から "text" フィールドの値を
// 部分的に抽出する。JSONがまだ不完全でも、"text" キーの値が始まっていれば
// そこまでの内容を返す。
func extractPartialTextField(partial string) string {
	// "text" キーの位置を探す
	// パターン: "text": " または "text":"
	searchPatterns := []string{`"text": "`, `"text":"`}

	textStart := -1
	patternLen := 0
	for _, pat := range searchPatterns {
		// 末尾から逆順に探し、エスケープされていないものを見つける
		searchEnd := len(partial)
		for {
			idx := strings.LastIndex(partial[:searchEnd], pat)
			if idx == -1 {
				break
			}
			// idx の直前が \ ならエスケープ内部なのでスキップ
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

	// "text": " の直後から値を読み取る
	valueStart := textStart + patternLen
	if valueStart >= len(partial) {
		return ""
	}

	// JSON文字列の値をデコード（エスケープ対応）
	var result strings.Builder
	i := valueStart
	for i < len(partial) {
		ch := partial[i]
		if ch == '\\' && i+1 < len(partial) {
			// エスケープシーケンス
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
			// 閉じクォート = テキスト値の終端
			break
		} else {
			result.WriteByte(ch)
			i++
		}
	}

	return result.String()
}

// --- AIチャットハンドラ (HTTP版 / 後方互換のため残す) ---
func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST method only", http.StatusMethodNotAllowed)
		return
	}

	var payload ChatPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Printf("ERROR(/api/chat): 不正なJSONを受信: %v", err)
		http.Error(w, "Bad Request: Invalid JSON", http.StatusBadRequest)
		return
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Println("ERROR: 'OPENAI_API_KEY'が設定されていません")
		http.Error(w, "Internal Server Error: API key not configured", http.StatusInternalServerError)
		return
	}

	chatRes, err := buildChatResponse(payload, apiKey, nil)
	if err != nil {
		log.Printf("ERROR(/api/chat): %v", err)
		http.Error(w, "Failed to communicate with AI", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chatRes)
}

// --- 採点ハンドラ ---
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
		"【課題】\n%s\n\n【想定出力】\n%s\n\n【提出コード】\n%s\n\n【実際の実行出力】\n%s",
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
		log.Printf("ERROR: 採点JSONパース失敗: %v\nRaw: %s", err, aiResponseStr)
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
				if currentScore >= 80 && !prev.IsCleared {
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

// --- 静的ファイル配信ハンドラ ---
func staticFileHandler() http.Handler {
	fs := http.FileServer(http.Dir(staticDir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, ".go") || strings.Contains(r.URL.Path, ".env") || strings.Contains(r.URL.Path, ".mod") {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if r.Method == "OPTIONS" {
			corsMiddleware(fs).ServeHTTP(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

//================================================================
// HTTP ミドルウェア
//================================================================

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowedOrigins := []string{
			"http://localhost:8088",
			"https://lab.wasabi-apple.com",
		}
		origin := r.Header.Get("Origin")
		for _, allowedOrigin := range allowedOrigins {
			if origin == allowedOrigin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				break
			}
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GET /api/memory
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

// POST /api/summarize
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

	currentMemJson, _ := json.Marshal(currentMem)
	userPrompt := fmt.Sprintf(`
[Current Memory JSON]
%s

[Recent Chat Log]
%s
`, string(currentMemJson), logText)

	newJsonStr, err := callOpenAI(summarySystemPrompt, userPrompt, true)
	if err != nil {
		http.Error(w, "AI Error", http.StatusInternalServerError)
		return
	}

	newJsonStr = cleanJSONString(newJsonStr)
	var newProfileData UserProfile
	if err := json.Unmarshal([]byte(newJsonStr), &newProfileData); err != nil {
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

func advisorHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST method only", http.StatusMethodNotAllowed)
		return
	}

	var payload ChatPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Printf("ERROR(/api/advisor): %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	systemPrompt, err := os.ReadFile("./prompts/system/prompt_advisor.txt")
	if err != nil {
		systemPrompt = []byte("あなたはプロフェッショナルなC++プログラミング講師です。簡潔かつ技術的に正確なアドバイスをしてください。")
	}

	userContent := fmt.Sprintf(
		"【現在の課題】\n%s\n\n【ユーザーのコード】\n%s\n\n【状況・メッセージ】\n%s",
		payload.Task, payload.Code, payload.Message,
	)

	aiResponseStr, err := callOpenAI(string(systemPrompt), userContent, true)
	if err != nil {
		http.Error(w, "AI Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(cleanJSONString(aiResponseStr)))
}
