package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

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

// --- メインの処理を修正 ---
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

	// --- Docker Build を削除 ---
	// docker build と docker rmi の処理を削除

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

func main() {
	// (main関数は変更なし)
	executeHandlerFunc := http.HandlerFunc(executeHandler)
	http.Handle("/execute", corsMiddleware(executeHandlerFunc))
	fmt.Println("Go server listening on http://localhost:8088")
	log.Fatal(http.ListenAndServe(":8088", nil))
}
