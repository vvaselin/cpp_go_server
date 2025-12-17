package main

import (
	"log"
	"net/http"
	"os"

	"github.com/nedpals/supabase-go"
)

// --- グローバル設定 ---

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
	// トークハンドラ
	http.HandleFunc("/api/talk", handleTalk)

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
