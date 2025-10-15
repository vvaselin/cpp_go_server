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
	defer os.RemoveAll(dir)
	log.Printf("INFO: Created temp directory: %s", dir)

	// 必要なファイルを一時ディレクトリに書き出す
	// main.cpp
	if err := os.WriteFile(filepath.Join(dir, "main.cpp"), []byte(payload.Code), 0666); err != nil {
		log.Printf("ERROR: Failed to write main.cpp: %v", err)
		http.Error(w, "Failed to write to temp file", http.StatusInternalServerError)
		return
	}
	// run.sh
	scriptContent := `#!/bin/sh
g++ main.cpp -o main.out && ./main.out`
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(scriptContent), 0755); err != nil {
		log.Printf("ERROR: Failed to write run.sh: %v", err)
		http.Error(w, "Failed to write to script file", http.StatusInternalServerError)
		return
	}
	// Dockerfile
	dockerfileContent := `FROM gcc:latest
WORKDIR /usr/src/app
COPY . .
RUN chmod +x run.sh
CMD ["./run.sh"]`
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfileContent), 0666); err != nil {
		log.Printf("ERROR: Failed to write Dockerfile: %v", err)
		http.Error(w, "Failed to write to dockerfile", http.StatusInternalServerError)
		return
	}

	// Dockerイメージをビルドする
	imageName := fmt.Sprintf("exec-image-%d", time.Now().UnixNano())
	log.Printf("INFO: Building Docker image: %s", imageName)
	buildCmd := exec.Command("docker", "build", "-t", imageName, ".")
	buildCmd.Dir = dir // コマンドの実行ディレクトリを一時ディレクトリに設定
	if buildOutput, err := buildCmd.CombinedOutput(); err != nil {
		log.Printf("ERROR: Docker build failed: %v\nOutput: %s", err, string(buildOutput))
		http.Error(w, "Docker build failed: "+string(buildOutput), http.StatusInternalServerError)
		return
	}
	// 実行後、イメージを削除するように予約
	defer exec.Command("docker", "rmi", imageName).Run()

	// ビルドしたイメージからコンテナを実行する
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log.Printf("INFO: Running Docker container from image: %s", imageName)
	runCmd := exec.CommandContext(ctx, "docker", "run", "--rm", imageName)

	var out bytes.Buffer
	var stderr bytes.Buffer
	runCmd.Stdout = &out
	runCmd.Stderr = &stderr
	err = runCmd.Run()

	if err != nil {
		log.Printf("ERROR: Docker run failed: %v\nStderr: %s", err, stderr.String())
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
