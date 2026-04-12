package main

import (
	"log"
	"net/http"
	"os"

	"github.com/nedpals/supabase-go"
)

// --- グローバル設定 ---

const staticDir = "../TyranoEdu"

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

	loadGradeSystemPrompt()
	loadSummarySystemPrompt()

	// --- ハンドラ（ルーティング）設定 ---

	// C++実行API (HTTP)
	http.Handle("/api/execute", corsMiddleware(http.HandlerFunc(executeHandler)))

	// AIチャットAPI:
	//   /api/chat/ws  ... WebSocket版（mascot_chat/init.ks から使用）
	//   /api/chat     ... HTTP版（後方互換のため残す）
	http.HandleFunc("/api/chat/ws", chatWSHandler) // WebSocketはcorsMiddleware不要（CheckOriginで制御）
	http.Handle("/api/chat", corsMiddleware(http.HandlerFunc(chatHandler)))

	// 採点API (HTTP)
	http.Handle("/api/grade", corsMiddleware(http.HandlerFunc(gradeHandler)))

	// 統制群用 (HTTP)
	http.Handle("/api/advisor", corsMiddleware(http.HandlerFunc(advisorHandler)))

	// 記憶・要約API (HTTP)
	http.Handle("/api/memory", corsMiddleware(http.HandlerFunc(getMemoryHandler)))
	http.Handle("/api/summarize", corsMiddleware(http.HandlerFunc(summarizeHandler)))

	// 静的ファイル配信（上記以外のすべてのリクエスト）
	http.Handle("/", staticFileHandler())

	// --- サーバー起動 ---
	log.Println("Goサーバーが待機中:")
	log.Println("  - http://localhost:8088  (HTTP)")
	log.Println("(API: /api/execute, /api/chat, /api/chat/ws, /api/grade, /api/memory, /api/summarize)")

	if err := http.ListenAndServe(":8088", nil); err != nil {
		log.Fatalf("サーバーの起動に失敗しました: %v", err)
	}
}
