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

//================================================================
// サーバー起動処理 (main)
//================================================================

func main() {
	// --- 初期化処理 ---
	loadEnv()
	loadSystemPrompt()

	// --- ハンドラ（ルーティング）設定 ---
	// APIルート（静的ファイルより先に登録）
	http.Handle("/execute", corsMiddleware(http.HandlerFunc(executeHandler)))
	http.Handle("/api/chat", corsMiddleware(http.HandlerFunc(chatHandler)))

	// 静的ファイル配信ルート（上記以外のすべてのリクエスト）
	http.Handle("/", staticFileHandler())

	// --- サーバー起動 ---
	// myIP := os.Getenv("MY_IPV4_ADDRESS")
	log.Println("Goサーバーが待機中:")
	log.Println("  - http://localhost:8088 (ローカル)")

	/*
		if myIP != "" {
			log.Printf("  - http://%s:8088 (ネットワーク)\n", myIP)
		}
	*/

	log.Println("(API配信: /execute, /api/chat)")
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
	reqMessages := []OpenAIMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: fmt.Sprintf(
			payload.Task,
			payload.Code,
			payload.Message,
		)},
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

	// クライアント（ティラノ）に返すレスポンス
	responseText := "（応答なし）"
	if len(openAIResp.Choices) > 0 && openAIResp.Choices[0].Message.Content != "" {
		responseText = openAIResp.Choices[0].Message.Content
	}

	response := ChatResponse{Text: responseText}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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
	content, err := os.ReadFile("./prompt_mocha.txt") // main.go と同じ階層
	if err != nil {
		log.Println("prompt.txtの読み込みに失敗しました。デフォルトのプロンプトを使用します。")
		systemPrompt = "あなたは親切なAIアシスタントです。"
	} else {
		systemPrompt = string(content)
	}
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
	Text string `json:"text"`
}

// OpenAI API へのリクエストボディ
type OpenAIRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`
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
