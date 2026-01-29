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
	if loveLevel >= 71 {
		levelInfo = "Lv.5: 唯一のパートナー"
	} else if loveLevel >= 51 {
		levelInfo = "Lv.4: 親愛と好意"
	} else if loveLevel >= 31 {
		levelInfo = "Lv.3: 信頼と笑顔"
	} else if loveLevel >= 16 {
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

// クイズモード専用のプロンプト構築
func buildQuizSystemPrompt(req TalkRequest, profile UserProfile) (string, error) {
	// ファイル読み込み
	readFile := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			log.Printf("Warning: %s not found", path)
			return ""
		}
		return string(b)
	}

	basePrompt := readFile("./prompts/base_system.txt")

	// ペルソナ読み込み (デフォルトmocha)
	charID := "mocha"
	personaPrompt := readFile(fmt.Sprintf("./prompts/persona_%s.txt", charID))
	if personaPrompt == "" {
		personaPrompt = readFile("./prompts/persona_mocha.txt")
	}

	modePrompt := readFile("./prompts/mode_quiz.txt")
	formatPrompt := readFile("./prompts/format_talk_json.txt")

	// 変数準備
	learnedStr := strings.Join(profile.LearnedTopics, ", ")
	if learnedStr == "" {
		learnedStr = "C++の基礎"
	}

	weaknessStr := strings.Join(profile.Weaknesses, ", ")
	if weaknessStr == "" {
		weaknessStr = "特になし"
	}

	// 好感度レベルの定義を作成 (ユーザー提供のロジックを適用)
	levelInfo := "Lv.1: 警戒と緊張"
	if req.LoveLevel >= 91 {
		levelInfo = "Lv.5: 唯一のパートナー"
	} else if req.LoveLevel >= 71 {
		levelInfo = "Lv.4: 親愛と好意"
	} else if req.LoveLevel >= 51 {
		levelInfo = "Lv.3: 信頼と笑顔"
	} else if req.LoveLevel >= 21 {
		levelInfo = "Lv.2: 慣れと安堵"
	}
	loveStatus := fmt.Sprintf("%d (%s)", req.LoveLevel, levelInfo)

	// 置換処理

	// mode_quiz.txt の置換
	modeReplacer := strings.NewReplacer(
		"{{love_level}}", loveStatus, // 詳細なレベル情報を渡す
		"{{learned_topics}}", learnedStr,
		"{{weaknesses}}", weaknessStr,
		"{{quiz_count}}", fmt.Sprintf("%d", req.QuizCount),
	)
	modeInstruction := modeReplacer.Replace(modePrompt)

	// base_system.txt の掃除 (不要なタグを消す)
	baseCleaner := strings.NewReplacer(
		"{{user_memory}}", "",
		"{{user_weaknesses}}", "",
		"{{current_love}}", loveStatus,
		"{{prev_params}}", "",
		"{{prev_output}}", "",
	)
	baseSystem := baseCleaner.Replace(basePrompt)

	// 結合
	// ---------------------------------------------------------
	var builder strings.Builder

	// 基本システム (Base)
	builder.WriteString(baseSystem)
	builder.WriteString("\n\n")

	// クイズモード指示 (Mode)
	builder.WriteString(modeInstruction)
	builder.WriteString("\n\n")

	// ペルソナ (Persona) - 優先度高いため後ろに配置
	builder.WriteString("# キャラクター定義\n")
	builder.WriteString(personaPrompt)
	builder.WriteString("\n\n")
	builder.WriteString("※いかなる場合も、上記のペルソナとしての口調と振る舞いを最優先してください。\n")

	// 出力フォーマット (JSON Format)
	builder.WriteString(formatPrompt)

	return builder.String(), nil
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
