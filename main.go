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
	"github.com/nedpals/supabase-go"
)

// --- グローバル設定 ---

// systemPrompt はAIチャットで使用するシステムプロンプトです。
var systemPrompt string

// staticDir は配信するティラノスクリプトのプロジェクトディレクトリです。
const staticDir = "../tyranoedu"

var gradeSystemPrompt string

const MEMORY_FILE = "user_memory.json"

var summarySystemPrompt string

var supabaseClient *supabase.Client

//================================================================
// サーバー起動処理 (main)
//================================================================

func main() {
	// --- 初期化処理 ---
	loadEnv()

	supabaseUrl := os.Getenv("SUPABASE_URL")
	supabaseKey := os.Getenv("SUPABASE_KEY")
	if supabaseUrl == "" || supabaseKey == "" {
		log.Println("WARNING: SUPABASE_URL または SUPABASE_KEY が設定されていません。DB機能は無効です。")
	} else {
		supabaseClient = supabase.CreateClient(supabaseUrl, supabaseKey)
		log.Println("INFO: Supabase接続完了")
	}

	// loadSystemPrompt()
	loadGradeSystemPrompt()
	loadSummarySystemPrompt()

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
	compileAndRunScript := "g++ -Wall /usr/src/app/main.cpp -o /usr/src/app/main.out && /usr/src/app/main.out"

	// ホストの一時ディレクトリをコンテナの /usr/src/app にマウントして実行
	log.Printf("INFO: Dockerコンテナを実行...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"--rm", // 実行後にコンテナを削除
		"-i",
		"--net=none",                              // ネットワークを無効化
		"-v", fmt.Sprintf("%s:/usr/src/app", dir), // ボリュームマウント
		"gcc:latest",                    // ベースイメージを直接指定
		"sh", "-c", compileAndRunScript, // コンテナで実行するコマンド
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
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(response)
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

	var userMem UserProfile
	if payload.UserID != "" {
		var profiles []UserProfile
		// エラー処理は省略していますが、実戦ではチェックしてください
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

	currentSystemPrompt := buildSystemPrompt(payload.CharacterID, "thought", payload.LoveLevel)

	currentSystemPrompt = strings.Replace(currentSystemPrompt, "{{user_memory}}", memoryText, -1)
	currentSystemPrompt = strings.Replace(currentSystemPrompt, "{{user_weaknesses}}", weaknessText, -1)

	// OpenAI APIへのリクエストボディを作成
	userContent := fmt.Sprintf(
		"【現在の課題】\n%s\n\n【ユーザーのコード】\n%s\n\n【ユーザーのメッセージ】\n%s",
		payload.Task,
		payload.Code,
		payload.Message,
	)

	reqMessages := []OpenAIMessage{
		{Role: "system", Content: currentSystemPrompt},
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

	if os.Getenv("AI_DEBUG_MODE") == "true" {
		if chatRes.Thought != "" {
			log.Printf("🧠Thought: %s", chatRes.Thought)
			log.Printf("📊Params: %+v", chatRes.Parameters)
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

	// log.Printf("DEBUG: UserID=%s, TaskID=%s, Score=%d", p.UserID, p.TaskID, 0)

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

	// ユーザーIDとタスクIDがある場合のみ実行
	bonusLove := 0
	isNewRecord := false

	if supabaseClient != nil && p.UserID != "" && p.TaskID != "" {
		// DBから現在の記録を取得
		var records []UserTaskProgress
		err := supabaseClient.DB.From("task_progress").
			Select("high_score").
			Eq("user_id", p.UserID).
			Eq("task_id", p.TaskID).
			Execute(&records)

		if err != nil {
			log.Printf("ERROR: Supabase Select failed: %v", err)
		}

		currentScore := gradeRes.Score

		if err == nil && len(records) > 0 {
			// 記録あり: ハイスコア更新チェック
			oldHighScore := records[0].HighScore
			// log.Printf("INFO: Record found. Old HighScore: %d, Current: %d", oldHighScore, currentScore)

			if currentScore > oldHighScore {
				bonusLove = 3 // 更新ボーナス
				isNewRecord = true
				// アップデート
				updateData := map[string]interface{}{"high_score": currentScore, "is_cleared": currentScore >= 80}

				var updateResult interface{}
				upErr := supabaseClient.DB.From("task_progress").
					Update(updateData).
					Eq("user_id", p.UserID).
					Eq("task_id", p.TaskID).
					Execute(&updateResult)

				if upErr != nil {
					log.Printf("ERROR: Supabase Update failed: %v", upErr)
				} else {
					// log.Println("INFO: Supabase Update success")
				}
			}
		} else {
			// 記録なし: 新規作成
			// log.Println("INFO: No record found. Creating new record.")

			if currentScore >= 80 {
				bonusLove = 5 // 初クリアボーナス
			}
			// 新規インサート
			newData := map[string]interface{}{
				"user_id":    p.UserID,
				"task_id":    p.TaskID,
				"high_score": currentScore,
				"is_cleared": currentScore >= 80,
			}

			// ★修正: Executeのエラーを捕捉する
			var insertResult interface{}
			inErr := supabaseClient.DB.From("task_progress").Insert(newData).Execute(&insertResult)

			if inErr != nil {
				log.Printf("ERROR: Supabase Insert failed: %v", inErr)
			} else {
				//log.Println("INFO: Supabase Insert success")
			}
		}
	}

	// レスポンスにボーナス情報を付与
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
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		// IDがない場合は空の初期値を返す（またはエラー）
		json.NewEncoder(w).Encode(UserProfile{
			Summary:   "ユーザーIDが指定されていません。",
			LoveLevel: 0,
		})
		return
	}

	// Supabaseから取得
	var profiles []UserProfile
	err := supabaseClient.DB.From("profiles").Select("*").Eq("id", userID).Execute(&profiles)

	if err != nil {
		log.Printf("ERROR: Fetch profile failed: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if len(profiles) == 0 {
		// データがない場合は初期レコードを作成して返す
		newProfile := UserProfile{
			ID:            userID,
			LoveLevel:     0,
			Summary:       "初めまして。これからよろしくお願いします。",
			LearnedTopics: []string{},
			Weaknesses:    []string{},
			LastUpdated:   time.Now().Format("2006-01-02 15:04:05"),
		}
		// DBに保存
		supabaseClient.DB.From("profiles").Insert(newProfile).Execute(nil)
		json.NewEncoder(w).Encode(newProfile)
	} else {
		// 既存データを返す
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(profiles[0])
	}
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
	content, err := os.ReadFile("./prompts/mocha_cool.txt")
	if err != nil {
		log.Println("prompt.txtの読み込みに失敗しました。デフォルトのプロンプトを使用します。")
		systemPrompt = "あなたは親切なAIアシスタントです。"
	} else {
		systemPrompt = string(content)
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

	var req SummarizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.UserID == "" {
		http.Error(w, "UserID is required", http.StatusBadRequest)
		return
	}

	// 現在の記憶をDBからロード (loadMemory()の代わり)
	var profiles []UserProfile
	supabaseClient.DB.From("profiles").Select("*").Eq("id", req.UserID).Execute(&profiles)

	var currentMem UserProfile
	if len(profiles) > 0 {
		currentMem = profiles[0]
	}

	// プロンプト作成 (ここは変更なし)
	logText := ""
	for _, item := range req.ChatLog {
		logText += fmt.Sprintf("%s: %s\n", item.Username, item.Message)
	}

	// AI呼び出し (currentMemの型が変わったので jsonCurrentMem関数などは適宜調整するか、json.Marshalで直接文字列化)
	currentMemJson, _ := json.Marshal(currentMem)

	userPrompt := fmt.Sprintf(`
[Current Memory JSON]
%s

[Current Status]
Current Love Level: %d

[Recent Chat Log]
%s
`, string(currentMemJson), req.CurrentLoveLevel, logText)

	// AI実行 ... (変更なし)
	newJsonStr, err := callOpenAI(summarySystemPrompt, userPrompt, true)
	if err != nil { /* エラー処理 */
	}

	// 保存処理
	newJsonStr = cleanJSONString(newJsonStr)
	var newProfileData UserProfile
	if err := json.Unmarshal([]byte(newJsonStr), &newProfileData); err != nil {
		/* エラー処理 */
	}

	// AIの結果を信頼しつつ、IDと好感度を確定させる
	newProfileData.ID = req.UserID
	if req.CurrentLoveLevel > 0 {
		newProfileData.LoveLevel = req.CurrentLoveLevel
	}
	newProfileData.LastUpdated = time.Now().Format("2006-01-02 15:04:05")

	// Supabase更新 (Update)
	// JSONBのカラム(learned_topics等)もうまくマッピングされるはずですが、
	// エラーが出る場合は map[string]interface{} に変換して渡してください。
	err = supabaseClient.DB.From("profiles").Update(newProfileData).Eq("id", req.UserID).Execute(nil)

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

//================================================================
// データ構造体 (Structs)
//================================================================

// --- C++実行用 ---

// /execute へのリクエストボディ
type CodePayload struct {
	Code  string `json:"code"`
	Stdin string `json:"stdin"`
}

// /execute からのレスポンスボディ
type ResultPayload struct {
	Result string `json:"result"`
}

// --- AIチャット用 ---

// /api/chat へのリクエストボディ
type ChatPayload struct {
	Message     string `json:"message"`
	Code        string `json:"code"`
	Task        string `json:"task"`
	LoveLevel   int    `json:"love_level"`
	CharacterID string `json:"character_id"`
	UserID      string `json:"user_id"`
}

// /api/chat からのレスポンスボディ
type ChatResponse struct {
	Thought    string   `json:"thought"` // 思考プロセス
	Parameters struct { // 感情パラメータ
		Joy      int `json:"joy"`
		Trust    int `json:"trust"`
		Fear     int `json:"fear"`
		Anger    int `json:"anger"`
		Shy      int `json:"shy"`
		Surprise int `json:"surprise"`
	} `json:"parameters"`
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
	UserID  string `json:"user_id"`
	ChatLog []struct {
		Username string `json:"username"`
		Message  string `json:"message"`
	} `json:"chat_history"`
	CurrentLoveLevel int `json:"current_love_level"`
}

// 採点リクエスト用
type GradePayload struct {
	UserID         string `json:"user_id"`
	TaskID         string `json:"task_id"`
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

// Supabase採点用の構造体
type UserTaskProgress struct {
	UserID    string `json:"user_id"`
	TaskID    string `json:"task_id"`
	HighScore int    `json:"high_score"`
	IsCleared bool   `json:"is_cleared"`
}

// DBの profiles テーブル用構造体
type UserProfile struct {
	ID            string   `json:"id"`
	LoveLevel     int      `json:"love_level"`
	Summary       string   `json:"summary"`
	LearnedTopics []string `json:"learned_topics"`
	Weaknesses    []string `json:"weaknesses"`
	LastUpdated   string   `json:"last_updated"`
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
