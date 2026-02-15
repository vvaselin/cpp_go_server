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
	// 1. ベースシステムの読み込み
	baseBytes, err := os.ReadFile("./prompts/base_system.txt")
	if err != nil {
		log.Printf("ERROR: base_system.txt read failed: %v", err)
		return "あなたはAIアシスタントです。"
	}

	// 2. ペルソナの読み込み (ディレクトリ構造に合わせて調整)
	if charID == "" {
		charID = "mocha"
	}
	charID = filepath.Clean(charID)
	personaPath := fmt.Sprintf("./prompts/persona/%s.txt", charID)
	personaBytes, err := os.ReadFile(personaPath)
	if err != nil {
		log.Printf("WARNING: Persona file '%s' not found. Using default.", personaPath)
		personaBytes, _ = os.ReadFile("./prompts/persona/mocha.txt")
	}

	// 3. 親密度レベルに応じた振る舞い定義の選択 [追加]
	levelFile := "lv1.txt"
	if loveLevel >= 71 {
		levelFile = "lv5.txt"
	} else if loveLevel >= 41 {
		levelFile = "lv4.txt"
	} else if loveLevel >= 26 {
		levelFile = "lv3.txt"
	} else if loveLevel >= 11 {
		levelFile = "lv2.txt"
	}
	levelBytes, err := os.ReadFile("./prompts/level/" + levelFile)
	if err != nil {
		log.Printf("ERROR: Level file %s read failed", levelFile)
	}

	// 4. 出力フォーマットの読み込み
	formatFile := "format_standard.txt"
	if mode == "thought" || mode == "debug" {
		formatFile = "format_thought.txt"
	}
	formatBytes, err := os.ReadFile("./prompts/" + formatFile)
	if err != nil {
		log.Printf("ERROR: Format file '%s' read failed", formatFile)
	}

	// 5. 結合 (数値としての {{current_love}} は渡さず、具体的な定義を埋め込む)
	fullPrompt := string(baseBytes) + "\n\n" +
		"# 【キャラクター設定】\n" + string(personaBytes) + "\n\n" +
		"# 【現在の関係性と振る舞いルール】\n" + string(levelBytes) + "\n\n" +
		"# 【出力形式】\n" + string(formatBytes)

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
