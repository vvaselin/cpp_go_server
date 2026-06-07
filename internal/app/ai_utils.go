package app

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
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

//================================================================
// 初期化関数
//================================================================

// loadEnv は .env ファイルから環境変数を読み込み
func loadEnv() {
	_, file, _, ok := runtime.Caller(0)
	if ok {
		rootEnv := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".env"))
		if err := godotenv.Load(rootEnv); err == nil {
			log.Printf("INFO: loaded env file: %s", rootEnv)
			return
		} else {
			log.Printf("WARNING: failed to load go_server .env at %s: %v", rootEnv, err)
		}
	}

	if err := godotenv.Load(); err != nil {
		log.Printf("WARNING: failed to load .env from current directory: %v", err)
	} else {
		log.Println("INFO: loaded env file from current directory")
	}
}

func buildSystemPrompt(charID string, mode string, loveLevel int) string {
	// ベースシステムの読み込み
	baseBytes, err := os.ReadFile("./prompts/base_system.txt")
	if err != nil {
		log.Printf("ERROR: base_system.txt read failed: %v", err)
		return "あなたはAIアシスタントです。"
	}

	// ペルソナの読み込み
	if charID == "" {
		charID = "mocha"
	}
	if strings.Contains(charID, "..") {
		return "invalid charID"
	}
	personaPath := filepath.Join("prompts", "persona", charID+".txt")

	personaBytes, err := os.ReadFile(personaPath)
	if err != nil {
		log.Printf("WARNING: Persona file '%s' not found. Using default.", personaPath)
		personaBytes, err = os.ReadFile("./prompts/persona/mocha.txt")
		if err != nil {
			log.Printf("ERROR: Default persona (mocha.txt) も読み込めません: %v", err)
			personaBytes = []byte("あなたは人見知りなプログラミング学習支援キャラクターです。丁寧に指導してください。")
		}
	}

	// 親密度レベルに応じた振る舞い定義の選択
	levelFile := "lv1.txt"
	if loveLevel >= 100 {
		levelFile = "lv5.txt"
	} else if loveLevel >= 65 {
		levelFile = "lv4.txt"
	} else if loveLevel >= 30 {
		levelFile = "lv3.txt"
	} else if loveLevel >= 15 {
		levelFile = "lv2.txt"
	}

	levelPath := filepath.Join("prompts", "level", charID, levelFile)
	levelBytes, err := os.ReadFile(levelPath)
	if err != nil {
		log.Printf("WARNING: Level file '%s' read failed: %v. lv1.txt にフォールバック", levelFile, err)
		// 指定レベルが読めない場合、最も制限的な lv1 を試す
		fallbackPath := filepath.Join("prompts", "level", charID, "lv1.txt")
		levelBytes, err = os.ReadFile(fallbackPath)
		if err != nil {
			log.Printf("ERROR: lv1.txt も読み込めません: %v. 最小限の定義を使用", err)
			levelBytes = []byte("現在のレベル: Lv.1（初対面）。敬語で、必要最低限の会話のみ行ってください。心理的距離を保つこと。")
		}
	}

	// 出力フォーマットの読み込み
	formatFile := "format_standard.txt"
	if mode == "thought" || mode == "debug" {
		formatFile = "format_thought.txt"
	} else if mode == "stream" {
		formatFile = "format_stream.txt"
	}
	formatPath := filepath.Join("prompts", formatFile)
	formatBytes, err := os.ReadFile(formatPath)
	if err != nil {
		log.Printf("WARNING: Format file '%s' read failed: %v. format_standard.txt にフォールバック", formatFile, err)
		// thought/debug 用が読めない場合、standard を試す
		if formatFile != "format_standard.txt" {
			formatBytes, err = os.ReadFile("./prompts/format_standard.txt")
		}
		if err != nil {
			log.Printf("ERROR: format_standard.txt も読み込めません: %v. 最小限の定義を使用", err)
			formatBytes = []byte(`JSON形式のみで出力してください。
{
  "thought": "思考プロセス",
  "parameters": {"joy":0,"trust":0,"fear":0,"anger":0,"shy":0,"surprise":0},
  "text": "回答テキスト",
  "emotion": "normal",
  "love_up": 0
}`)
		}
	}

	// 結合
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

	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini" // デフォルト
	}

	reqBody := OpenAIRequest{
		Model:    model,
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

// テンプレート変数の正規表現（ {{variable_name}} 形式）
var templateVarRegex = regexp.MustCompile(`\{\{(\w+)\}\}`)

// validateTemplateVars は未解決のテンプレート変数をチェックし、
// 見つかった場合はログに警告を出力します。
// 戻り値: 未解決の変数名リスト（なければ空スライス）
func validateTemplateVars(prompt string) []string {
	matches := templateVarRegex.FindAllStringSubmatch(prompt, -1)
	if len(matches) == 0 {
		return nil
	}

	// 重複除去
	seen := make(map[string]bool)
	var unresolved []string
	for _, m := range matches {
		varName := m[1]
		if !seen[varName] {
			seen[varName] = true
			unresolved = append(unresolved, varName)
		}
	}

	log.Printf("WARNING: 未解決のテンプレート変数が %d 件あります: %v", len(unresolved), unresolved)
	return unresolved
}
