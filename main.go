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
	"time"

	"github.com/joho/godotenv"
)

// Chat用構造体
type ChatPayload struct {
	Message string `json:"message"`
}

type OpenAIRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type ChatResponse struct {
	Text string `json:"text"`
}

// --- システムプロンプトをグローバル変数として読み込む ---
var systemPrompt string

func loadSystemPrompt() {
	// main.go と同じ階層に prompt.txt を置く想定
	// --- 修正: ioutil.ReadFile -> os.ReadFile ---
	content, err := os.ReadFile("./prompt.txt")
	if err != nil {
		log.Println("prompt.txtの読み込みに失敗しました。デフォルトのプロンプトを使用します。")
		systemPrompt = "あなたは親切なAIアシスタントです。"
	} else {
		systemPrompt = string(content)
		log.Println("prompt.txtを読み込みました。")
	}
}

// C++用構造体
type CodePayload struct {
	Code string `json:"code"`
}
type ResultPayload struct {
	Result string `json:"result"`
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// main.go の executeHandler関数をこれに置き換える
func executeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST method only", http.StatusMethodNotAllowed)
		return
	}

	var payload CodePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Printf("ERROR: Invalid JSON received: %v", err)
		http.Error(w, "Bad Request: Invalid JSON", http.StatusBadRequest)
		return
	}

	// 一時ディレクトリを作成
	dir, err := os.MkdirTemp("", "cpp-execution-")
	if err != nil {
		log.Printf("ERROR: Failed to create temp dir: %v", err)
		http.Error(w, "Failed to create temp dir", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir) // 処理終了時に一時ディレクトリを削除
	log.Printf("INFO: Created temp directory: %s", dir)

	// C++コードを一時ディレクトリに書き出す
	if err := os.WriteFile(filepath.Join(dir, "main.cpp"), []byte(payload.Code), 0666); err != nil {
		log.Printf("ERROR: Failed to write main.cpp: %v", err)
		http.Error(w, "Failed to write to temp file", http.StatusInternalServerError)
		return
	}

	// --- Docker Run を修正 ---
	// 10秒間のタイムアウトを設定
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// コンテナ内で実行するコマンド
	// /usr/src/app はマウントされたディレクトリ
	compileAndRunScript := "g++ /usr/src/app/main.cpp -o /usr/src/app/main.out && /usr/src/app/main.out"

	// ホストの一時ディレクトリをコンテナの /usr/src/app にマウントして実行
	log.Printf("INFO: Running Docker container using volume mount...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"--rm",                                    // 実行後にコンテナを削除
		"--net=none",                              // ネットワークを無効化 (セキュリティ向上)
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
		log.Printf("ERROR: Docker run failed: %v\nStderr: %s", err, stderr.String())
		// コンパイルエラーなども stderr に入るので、それをクライアントに返す
		http.Error(w, "Execution failed: "+stderr.String(), http.StatusInternalServerError)
		return
	}

	// 成功した結果を返す
	log.Printf("INFO: Docker execution successful. Output: %s", out.String())
	response := ResultPayload{Result: out.String()}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// --- AIチャット用のハンドラを新しく追加 ---
func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST method only", http.StatusMethodNotAllowed)
		return
	}

	var payload ChatPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Printf("ERROR: Invalid JSON received (chat): %v", err)
		http.Error(w, "Bad Request: Invalid JSON", http.StatusBadRequest)
		return
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Println("ERROR: OPENAI_API_KEY is not set")
		http.Error(w, "Internal Server Error: API key not configured", http.StatusInternalServerError)
		return
	}

	// OpenAI APIへのリクエストボディを作成
	reqMessages := []OpenAIMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: payload.Message},
	}
	reqBody := OpenAIRequest{
		Model:    "gpt-4o-mini", // server.js と同じモデルを指定
		Messages: reqMessages,
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("ERROR: Failed to marshal OpenAI request: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// OpenAI APIへリクエストを送信
	// タイムアウトを設定 (例: 30秒)
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
		log.Printf("ERROR: Failed to send request to OpenAI: %v", err)
		http.Error(w, "Failed to communicate with AI", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// --- 修正: ioutil.ReadAll -> io.ReadAll ---
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Printf("ERROR: OpenAI API returned non-200 status: %d %s", resp.StatusCode, string(bodyBytes))
		http.Error(w, "AI service returned an error", http.StatusBadGateway)
		return
	}

	// レスポンスをパース
	var openAIResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		log.Printf("ERROR: Failed to decode OpenAI response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// --- 修正: 「varresponseText」 -> 「responseText」 ---
	responseText := "（応答なし）"
	if len(openAIResp.Choices) > 0 && openAIResp.Choices[0].Message.Content != "" {
		responseText = openAIResp.Choices[0].Message.Content
	}

	response := ChatResponse{Text: responseText}

	// --- 修正: 「w.Header.Set」 -> 「w.Header().Set」 ---
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("警告: .env ファイルの読み込みに失敗しました。")
	} else {
		log.Println(".env ファイルを読み込みました。")
	}

	loadSystemPrompt()

	executeHandlerFunc := http.HandlerFunc(executeHandler)
	chatHandlerFunc := http.HandlerFunc(chatHandler)

	http.Handle("/execute", corsMiddleware(executeHandlerFunc))
	http.Handle("/api/chat", corsMiddleware(chatHandlerFunc))

	fmt.Println("Go server listening on http://localhost:8088 (serving /execute and /api/chat)")
	log.Fatal(http.ListenAndServe(":8088", nil))
}
