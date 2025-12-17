package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

//================================================================
// 初期化関数
//================================================================

// loadEnv は .env ファイルから環境変数を読み込み
func loadEnv() {
	err := godotenv.Load() // .env ファイルを探す
	if err != nil {
		log.Println("警告: .env ファイルの読み込みに失敗しました。")
	}
}

func buildSystemPrompt(charID string, mode string, loveLevel int) string {
	// ベースシステムの読み込み
	baseBytes, err := os.ReadFile("./prompts/base_system.txt")
	if err != nil {
		log.Printf("ERROR: base_system.txt read failed: %v", err)
		return "あなたはAIアシスタントです。"
	}

	// ペルソナの読み込み (デフォルトは mocha)
	if charID == "" {
		charID = "mocha"
	}
	// ディレクトリトラバーサル対策（簡易）
	charID = filepath.Clean(charID)
	personaPath := fmt.Sprintf("./prompts/persona_%s.txt", charID)

	personaBytes, err := os.ReadFile(personaPath)
	if err != nil {
		log.Printf("WARNING: Persona file '%s' not found. Using default.", personaPath)
		// ファイルがない場合はデフォルト(mocha)を試す
		personaBytes, _ = os.ReadFile("./prompts/persona_mocha.txt")
	}

	// 出力フォーマットの読み込み
	formatFile := "format_standard.txt"
	if mode == "thought" || mode == "debug" {
		formatFile = "format_thought.txt"
	}
	formatBytes, err := os.ReadFile("./prompts/" + formatFile)
	if err != nil {
		log.Printf("ERROR: Format file '%s' read failed", formatFile)
	}

	// 結合
	fullPrompt := string(baseBytes) + "\n\n" + string(personaBytes) + "\n\n" + string(formatBytes)

	// レベルを計算して、プロンプトに詳しい情報を埋め込む
	levelInfo := "Lv.1: 警戒と緊張" // デフォルト
	if loveLevel >= 91 {
		levelInfo = "Lv.5: 唯一のパートナー"
	} else if loveLevel >= 71 {
		levelInfo = "Lv.4: 親愛と好意"
	} else if loveLevel >= 51 {
		levelInfo = "Lv.3: 信頼と笑顔"
	} else if loveLevel >= 21 {
		levelInfo = "Lv.2: 慣れと安堵"
	}

	// AIに「数値」だけでなく「レベルの定義」ごと渡す
	loveStatus := fmt.Sprintf("%d (%s)", loveLevel, levelInfo)
	fullPrompt = strings.Replace(fullPrompt, "{{current_love}}", loveStatus, -1)

	return fullPrompt
}

func loadGradeSystemPrompt() {
	content, err := os.ReadFile("./prompts/system/prompt_grade.txt")
	if err != nil {
		log.Println("prompt_grade.txtの読み込み失敗。デフォルトを使用。")
		gradeSystemPrompt = "あなたは採点官です。JSONで採点してください。"
	} else {
		gradeSystemPrompt = string(content)
	}
}

// loadSummarySystemPrompt は .txt から要約用プロンプトを読み込みます
func loadSummarySystemPrompt() {
	content, err := os.ReadFile("./prompts/system/prompt_summary.txt")
	if err != nil {
		log.Println("警告: prompt_summary.txtの読み込み失敗。デフォルトを使用。")
		summarySystemPrompt = "あなたはユーザーの学習状況を記録するメモリーマネージャーです。JSON形式で出力してください。"
	} else {
		summarySystemPrompt = string(content)
		//log.Println("INFO: prompt_summary.txt を読み込みました")
	}
}

// お喋りモード用のシステムプロンプト構築関数
func buildTalkSystemPrompt(charID string, mode string, loveLevel int) (string, error) {
	// 1. ベースシステムの読み込み
	// (base_system.txtには {{user_memory}} 等のプレースホルダがありますが、
	//  今回は単純化のため、それらが残っていてもAIが無視するようにするか、
	//  strings.Replaceですべて空文字に置換して消してしまうのが安全です)
	baseBytes, err := os.ReadFile("./prompts/base_system.txt")
	if err != nil {
		log.Printf("WARNING: base_system.txt not found: %v", err)
		baseBytes = []byte("あなたはAIアシスタントです。")
	}
	basePrompt := string(baseBytes)

	// 不要なプレースホルダを掃除 (base_system.txt用)
	// TalkAPIで使わない変数は空文字にしておく
	replacer := strings.NewReplacer(
		"{{user_memory}}", "特になし",
		"{{user_weaknesses}}", "特になし",
		"{{prev_params}}", "特になし",
		"{{prev_output}}", "特になし",
	)
	basePrompt = replacer.Replace(basePrompt)

	// 2. ペルソナの読み込み
	if charID == "" {
		charID = "mocha"
	}
	// ディレクトリトラバーサル対策
	charID = filepath.Base(charID)
	personaPath := fmt.Sprintf("./prompts/persona_%s.txt", charID)
	personaBytes, err := os.ReadFile(personaPath)
	if err != nil {
		log.Printf("WARNING: Persona file '%s' not found. Using default.", personaPath)
		personaBytes, _ = os.ReadFile("./prompts/persona_mocha.txt")
	}

	// 3. モード別指示の読み込み
	modeFile := "mode_chat.txt" // デフォルト
	if mode == "quiz" {
		modeFile = "mode_quiz.txt"
	}
	modeBytes, err := os.ReadFile(filepath.Join("prompts", modeFile))
	if err != nil {
		log.Printf("WARNING: Mode file '%s' not found.", modeFile)
	}

	// 4. 出力フォーマット (JSON指定) の読み込み
	// ここで format_thought.txt の代わりに format_talk_json.txt を使う
	formatBytes, err := os.ReadFile("./prompts/format_talk_json.txt")
	if err != nil {
		return "", fmt.Errorf("format_talk_json.txt read failed: %v", err)
	}

	// 5. 結合
	var builder strings.Builder
	builder.WriteString(basePrompt)
	builder.WriteString("\n\n")
	builder.WriteString(string(personaBytes))
	builder.WriteString("\n\n")
	builder.WriteString(string(modeBytes))
	builder.WriteString("\n\n")
	builder.WriteString(string(formatBytes))

	fullPrompt := builder.String()

	// 6. 好感度レベルの埋め込み logic
	levelInfo := "Lv.1: 警戒と緊張"
	if loveLevel >= 91 {
		levelInfo = "Lv.5: 唯一のパートナー"
	} else if loveLevel >= 71 {
		levelInfo = "Lv.4: 親愛と好意"
	} else if loveLevel >= 51 {
		levelInfo = "Lv.3: 信頼と笑顔"
	} else if loveLevel >= 21 {
		levelInfo = "Lv.2: 慣れと安堵"
	}

	loveStatus := fmt.Sprintf("%d (%s)", loveLevel, levelInfo)
	fullPrompt = strings.ReplaceAll(fullPrompt, "{{current_love}}", loveStatus)

	return fullPrompt, nil
}

//================================================================
// ヘルパー関数
//================================================================

// callOpenAI は OpenAI API にリクエストを送り、結果の文字列を返します
func callOpenAI(sysPrompt, userMsg string, useJSON bool) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY が設定されていません")
	}

	reqMessages := []OpenAIMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userMsg},
	}

	reqBody := OpenAIRequest{
		Model:    "gpt-4o-mini",
		Messages: reqMessages,
	}

	// JSONモードの切り替えスイッチ
	if useJSON {
		reqBody.ResponseFormat = &ResponseFormat{Type: "json_object"}
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("JSON作成エラー: %v", err)
	}

	// ... (HTTPリクエスト作成部分は変更なし) ...
	// req, err := http.NewRequestWithContext(...) など
	// req.Header.Set(...) など

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBytes))
	if err != nil {
		return "", fmt.Errorf("リクエスト作成エラー: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API通信エラー: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("APIエラー (Status: %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var openAIResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return "", fmt.Errorf("レスポンスデコードエラー: %v", err)
	}

	if len(openAIResp.Choices) == 0 || openAIResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("AIからの応答が空です")
	}

	return openAIResp.Choices[0].Message.Content, nil
}

func callOpenAITalk(messages []OpenAIMessage) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set")
	}

	// リクエストデータの作成
	reqBody := OpenAIRequest{
		Model:    "gpt-3.5-turbo-0125", // または "gpt-4-turbo", "gpt-4o" (JSONモード対応モデル必須)
		Messages: messages,
		ResponseFormat: &ResponseFormat{
			Type: "json_object",
		},
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("JSON marshal error: %v", err)
	}

	// HTTPリクエスト作成
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBytes))
	if err != nil {
		return "", fmt.Errorf("request creation error: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// 送信
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API call error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error (Status: %d): %s", resp.StatusCode, string(bodyBytes))
	}

	// レスポンスのパース
	var openAIResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return "", fmt.Errorf("response decode error: %v", err)
	}

	if len(openAIResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return openAIResp.Choices[0].Message.Content, nil
}

// cleanJSONString は AIが返したマークダウン記法 (```json ... ```) を除去します
func cleanJSONString(s string) string {
	s = strings.TrimSpace(s)

	// Markdownのコードブロック記法があれば削除
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
	}

	return strings.TrimSpace(s)
}
