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
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// --- グローバル設定 ---

// systemPrompt はAIチャットで使用するシステムプロンプトです。
var systemPrompt string

// staticDir は配信するティラノスクリプトのプロジェクトディレクトリです。
const staticDir = "../tyranoedu"

var gradeSystemPrompt string

const MEMORY_FILE = "user_memory.json"

var summarySystemPrompt string

//================================================================
// サーバー起動処理 (main)
//================================================================

func main() {
	// --- 初期化処理 ---
	loadEnv()
	loadSystemPrompt()
	loadGradeSystemPrompt()

	// --- ハンドラ（ルーティング）設定 ---
	// APIルート（静的ファイルより先に登録）
	http.Handle("/execute", corsMiddleware(http.HandlerFunc(executeHandler)))
	http.Handle("/api/chat", corsMiddleware(http.HandlerFunc(chatHandler)))

	http.Handle("/api/grade", corsMiddleware(http.HandlerFunc(gradeHandler)))

	// 静的ファイル配信ルート（上記以外のすべてのリクエスト）
	http.Handle("/", staticFileHandler())

	// 記憶ハンドラ
	http.Handle("/api/memory", corsMiddleware(http.HandlerFunc(getMemoryHandler)))
	http.Handle("/api/summarize", corsMiddleware(http.HandlerFunc(summarizeHandler)))

	// --- サーバー起動 ---
	// myIP := os.Getenv("MY_IPV4_ADDRESS")
	log.Println("Goサーバーが待機中:")
	log.Println("  - http://localhost:8088 (ローカル)")

	/*
		if myIP != "" {
			log.Printf("  - http://%s:8088 (ネットワーク)\n", myIP)
		}
	*/

	log.Println("(API配信: /execute, /api/chat, /api/grade, /api/memory, /api/summarize)")
	// log.Println("(静的ファイルの配信元: " + staticDir + ")")

	// ListenAndServe はエラーを返すため、ログに出力する
	if err := http.ListenAndServe(":8088", nil); err != nil {
		log.Fatalf("サーバーの起動に失敗しました: %v", err)
	}
}

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
		log.Printf("ERROR(/execute): 不正なJSONを受信: %v", err)
		http.Error(w, "Bad Request: Invalid JSON", http.StatusBadRequest)
		return
	}

	// 一時ディレクトリを作成
	dir, err := os.MkdirTemp("", "cpp-execution-")
	if err != nil {
		log.Printf("ERROR: 一時ディレクトリの作成に失敗: %v", err)
		http.Error(w, "Failed to create temp dir", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)
	log.Printf("INFO:: 一時ディレクトリを作成: %s", dir)

	// C++コードを一時ディレクトリに書き出す
	if err := os.WriteFile(filepath.Join(dir, "main.cpp"), []byte(payload.Code), 0666); err != nil {
		log.Printf("ERROR: main.cpp書き込みに失敗: %v", err)
		http.Error(w, "Failed to write to temp file", http.StatusInternalServerError)
		return
	}

	// 10秒間のタイムアウトを設定
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// コンテナ内で実行するコマンド
	compileAndRunScript := "g++ /usr/src/app/main.cpp -o /usr/src/app/main.out && /usr/src/app/main.out"

	// ホストの一時ディレクトリをコンテナの /usr/src/app にマウントして実行
	log.Printf("INFO: Dockerコンテナを実行...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"--rm",                                    // 実行後にコンテナを削除
		"--net=none",                              // ネットワークを無効化
		"-v", fmt.Sprintf("%s:/usr/src/app", dir), // ボリュームマウント
		"gcc:latest",                    // ベースイメージを直接指定
		"sh", "-c", compileAndRunScript, // コンテナで実行するコマンド
	)

	var out bytes.Buffer
	var stderr bytes.Buffer
	runCmd.Stdout = &out
	runCmd.Stderr = &stderr
	err = runCmd.Run()

	// タイムアウトの場合
	if ctx.Err() == context.DeadlineExceeded {
		log.Println("ERROR: Docker run timed out")
		http.Error(w, "Execution timed out", http.StatusGatewayTimeout)
		return
	}

	// その他の実行エラー（コンパイルエラーなど）
	if err != nil {
		log.Printf("ERROR: C++実行失敗: %v\n標準エラー: %s", err, stderr.String())
		http.Error(w, "Execution failed: "+stderr.String(), http.StatusInternalServerError)
		return
	}

	// 成功した結果を返す
	log.Printf("INFO: C++実行成功: %s", out.String())
	response := ResultPayload{Result: out.String()}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// --- AIチャットハンドラ ---
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

	// OpenAI APIへのリクエストボディを作成
	userContent := fmt.Sprintf(
		"【現在の課題】\n%s\n\n【ユーザーのコード】\n%s\n\n【ユーザーのメッセージ】\n%s",
		payload.Task,
		payload.Code,
		payload.Message,
	)

	reqMessages := []OpenAIMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userContent},
	}

	reqBody := OpenAIRequest{
		Model:    "gpt-4o-mini",
		Messages: reqMessages,
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("ERROR: OpenAIへのリクエスト送信に失敗: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// OpenAI APIへリクエストを送信 (30秒タイムアウト)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBytes))
	if err != nil {
		log.Printf("ERROR: Failed to create OpenAI request: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("ERROR: OpenAIへのリクエスト送信に失敗: %v", err)
		http.Error(w, "Failed to communicate with AI", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Printf("ERROR: OpenAI APIが200以外のステータスを返答: %d %s", resp.StatusCode, string(bodyBytes))
		http.Error(w, "AI service returned an error", http.StatusBadGateway)
		return
	}

	// レスポンスをパース
	var openAIResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		log.Printf("ERROR: OpenAIレスポンスのJSONデコードに失敗: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	aiRawContent := ""
	if len(openAIResp.Choices) > 0 {
		aiRawContent = openAIResp.Choices[0].Message.Content
	}
	// Markdown記法 (```json ... ```) が含まれている場合に除去する
	aiCleanContent := cleanJSONString(aiRawContent)
	// JSON文字列を構造体にパース
	var chatRes ChatResponse
	if err := json.Unmarshal([]byte(aiCleanContent), &chatRes); err != nil {
		log.Printf("WARNING: AIの応答がJSONとしてパースできませんでした。生テキストを返します。\nRaw: %s\nError: %v", aiCleanContent, err)
		// パース失敗時は、AIの応答全てをテキストとして扱い、感情はデフォルトにする
		chatRes = ChatResponse{
			Text:    aiCleanContent, // 除去後のテキストを入れる
			Emotion: "normal",
			LoveUp:  0,
		}
	}
	// クライアント（ティラノ）にJSONを返す
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

	// AIに送るユーザープロンプトを構築
	userMessage := fmt.Sprintf(
		"【課題】\n%s\n\n【想定出力】\n%s\n\n【提出コード】\n%s\n\n【実際の実行出力】\n%s",
		p.TaskDesc, p.ExpectedOutput, p.Code, p.Output,
	)

	aiResponseStr, err := callOpenAI(gradeSystemPrompt, userMessage, false)
	if err != nil {
		http.Error(w, "AI Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// JSON部分だけ抽出（Markdown記法 ```json ... ``` などを除去する処理が必要な場合あり）
	aiResponseStr = cleanJSONString(aiResponseStr)

	// レスポンスをパースして検証
	var gradeRes GradeResponse
	if err := json.Unmarshal([]byte(aiResponseStr), &gradeRes); err != nil {
		log.Println("JSON Parse Error:", aiResponseStr)
		http.Error(w, "AI Response Parse Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(gradeRes)
}

// --- 静的ファイル配信ハンドラ ---
func staticFileHandler() http.Handler {
	fs := http.FileServer(http.Dir(staticDir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// APIルートがここに到達した場合（通常は発生しない）は 404
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/execute") {
			http.NotFound(w, r)
			return
		}

		// セキュリティ: .env や .go ファイルなど、サーバーの内部ファイルへのアクセスを禁止
		if strings.Contains(r.URL.Path, ".go") || strings.Contains(r.URL.Path, ".env") || strings.Contains(r.URL.Path, ".mod") {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		// CORSのPreflightリクエスト(OPTIONS)に対応
		if r.Method == "OPTIONS" {
			corsMiddleware(fs).ServeHTTP(w, r)
			return
		}

		// ファイルサーバーが処理
		fs.ServeHTTP(w, r)
	})
}

//================================================================
// HTTP ミドルウェア
//================================================================

// 安全のため、許可するアクセス元を .env のIPとlocalhostに限定
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// myIP := os.Getenv("MY_IPV4_ADDRESS")
		// 許可するオリジン（アクセス元）のリスト
		allowedOrigins := []string{
			"http://localhost:8088", // ローカルホスト
		}
		/*
			if myIP != "" {
				allowedOrigins = append(allowedOrigins, "http://"+myIP+":8088") // ネットワークIP
			}
		*/

		// リクエストのオリジンを取得
		origin := r.Header.Get("Origin")

		// 許可リストに存在するオリジンの場合のみヘッダーを設定
		for _, allowedOrigin := range allowedOrigins {
			if origin == allowedOrigin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				break
			}
		}
		// 'Access-Control-Allow-Origin' が設定された場合のみ、他のヘッダーも設定する
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		// OPTIONSメソッド（プリフライトリクエスト）の場合はここで終了
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// 次のハンドラ（API本体）を実行
		next.ServeHTTP(w, r)
	})
}

// GET /api/memory
func getMemoryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	mem, err := loadMemory()
	if err != nil {
		http.Error(w, "Failed to load memory", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(mem)
}

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

// loadSystemPrompt は .txt からシステムプロンプトを読み込み、グローバル変数にセット
func loadSystemPrompt() {
	content, err := os.ReadFile("./prompts/prompt_mocha_distant.txt")
	if err != nil {
		log.Println("prompt.txtの読み込みに失敗しました。デフォルトのプロンプトを使用します。")
		systemPrompt = "あなたは親切なAIアシスタントです。"
	} else {
		systemPrompt = string(content)
	}
}

func loadGradeSystemPrompt() {
	content, err := os.ReadFile("./prompts/prompt_grade.txt")
	if err != nil {
		log.Println("prompt_grade.txtの読み込み失敗。デフォルトを使用。")
		gradeSystemPrompt = "あなたは採点官です。JSONで採点してください。"
	} else {
		gradeSystemPrompt = string(content)
	}
}

// 記憶ファイルを読み込むヘルパー関数
func loadMemory() (UserMemory, error) {
	var mem UserMemory
	// デフォルト値
	mem.Summary = "まだ会話をしていません。"
	mem.LearnedTopics = []string{}
	mem.Weaknesses = []string{}

	file, err := os.ReadFile(MEMORY_FILE)
	if err != nil {
		if os.IsNotExist(err) {
			return mem, nil // ファイルがない場合は初期値を返す
		}
		return mem, err
	}
	err = json.Unmarshal(file, &mem)
	return mem, err
}

// 記憶ファイルを保存するヘルパー関数
func saveMemory(mem UserMemory) error {
	data, err := json.MarshalIndent(mem, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(MEMORY_FILE, data, 0644)
}

// POST /api/summarize
func summarizeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// リクエスト受信 (CurrentLoveLevelが含まれているはず)
	var req SummarizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 現在の記憶をロード
	currentMem, _ := loadMemory()

	// プロンプト作成
	logText := ""
	for _, item := range req.ChatLog {
		logText += fmt.Sprintf("%s: %s\n", item.Username, item.Message)
	}

	// クライアントから来た love_level をプロンプトに明記する
	userPrompt := fmt.Sprintf(`
[Current Memory JSON]
%s

[Current Status]
Current Love Level: %d

[Recent Chat Log]
%s
`, jsonCurrentMem(currentMem), req.CurrentLoveLevel, logText)

	// AI呼び出し (JSONモード有効: true)
	newJsonStr, err := callOpenAI(summarySystemPrompt, userPrompt, true)
	if err != nil {
		log.Printf("Summary generation failed: %v", err)
		http.Error(w, "Summary generation failed", http.StatusInternalServerError)
		return
	}

	// 保存処理
	newJsonStr = cleanJSONString(newJsonStr)
	var newMem UserMemory
	if err := json.Unmarshal([]byte(newJsonStr), &newMem); err != nil {
		log.Printf("JSON Parse Error: %v\nResponse: %s", err, newJsonStr)
		http.Error(w, "Failed to parse summary JSON", http.StatusInternalServerError)
		return
	}

	// 安全策: AIが数値を間違えても、クライアントの値を正とするならここで上書きする
	if req.CurrentLoveLevel > 0 {
		newMem.LoveLevel = req.CurrentLoveLevel
	}

	newMem.LastUpdated = time.Now().Format("2006-01-02 15:04:05")

	if err := saveMemory(newMem); err != nil {
		http.Error(w, "Failed to save memory", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

//================================================================
// データ構造体 (Structs)
//================================================================

// --- C++実行用 ---

// /execute へのリクエストボディ
type CodePayload struct {
	Code string `json:"code"`
}

// /execute からのレスポンスボディ
type ResultPayload struct {
	Result string `json:"result"`
}

// --- AIチャット用 ---

// /api/chat へのリクエストボディ
type ChatPayload struct {
	Message string `json:"message"`
	Code    string `json:"code"`
	Task    string `json:"task"`
}

// /api/chat からのレスポンスボディ
type ChatResponse struct {
	Text    string `json:"text"`
	Emotion string `json:"emotion"`
	LoveUp  int    `json:"love_up"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

// OpenAI API へのリクエストボディ
type OpenAIRequest struct {
	Model          string          `json:"model"`
	Messages       []OpenAIMessage `json:"messages"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// OpenAI API で使用するメッセージ構造体
type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAI API からのレスポンスボディ
type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// 記憶データ構造
type UserMemory struct {
	Summary       string   `json:"summary"`        // ユーザーの学習状況・特徴の要約
	LearnedTopics []string `json:"learned_topics"` // 学んだ項目リスト
	Weaknesses    []string `json:"weaknesses"`     // 苦手な項目リスト
	LoveLevel     int      `json:"love_level"`     // (オプション) 親密度をサーバー側でもバックアップしたい場合
	LastUpdated   string   `json:"last_updated"`   // 最終更新日時
}

// 要約リクエストの構造体
type SummarizeRequest struct {
	ChatLog []struct {
		Username string `json:"username"`
		Message  string `json:"message"`
	} `json:"chat_history"`
	CurrentLoveLevel int `json:"current_love_level"`
}

// 採点リクエスト用
type GradePayload struct {
	Code           string `json:"code"`            // ユーザーのコード
	Output         string `json:"output"`          // 実行結果の出力
	TaskDesc       string `json:"task_desc"`       // 課題文
	ExpectedOutput string `json:"expected_output"` // 想定出力
}

// 採点レスポンス用 (AIからのJSONをマッピング)
type GradeResponse struct {
	Score       int    `json:"score"`
	Reason      string `json:"reason"`
	Improvement string `json:"improvement"`
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

// ヘルパー: 構造体をJSON文字列にする
func jsonCurrentMem(mem UserMemory) string {
	b, _ := json.Marshal(mem)
	return string(b)
}
