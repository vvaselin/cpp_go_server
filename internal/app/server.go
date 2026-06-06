package app

import (
	"log"
	"net/http"
	"os"

	"github.com/nedpals/supabase-go"
)

const staticDir = "../TyranoEdu"

var gradeSystemPrompt string

const MEMORY_FILE = "user_memory.json"

var summarySystemPrompt string

var supabaseClient *supabase.Client

func Run() {
	loadEnv()

	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseKey := os.Getenv("SUPABASE_KEY")
	if supabaseURL == "" || supabaseKey == "" {
		log.Println("WARNING: SUPABASE_URL or SUPABASE_KEY is not configured. DB features are disabled.")
	} else {
		supabaseClient = supabase.CreateClient(supabaseURL, supabaseKey)
		log.Println("INFO: Supabase connection ready")
	}

	loadGradeSystemPrompt()
	loadSummarySystemPrompt()

	http.Handle("/api/execute", corsMiddleware(http.HandlerFunc(executeHandler)))
	http.HandleFunc("/api/chat/ws", chatWSHandler)
	http.Handle("/api/chat", corsMiddleware(http.HandlerFunc(chatHandler)))
	http.Handle("/api/grade", corsMiddleware(http.HandlerFunc(gradeHandler)))
	http.Handle("/api/memory", corsMiddleware(http.HandlerFunc(getMemoryHandler)))
	http.Handle("/api/summarize", corsMiddleware(http.HandlerFunc(summarizeHandler)))
	http.Handle("/", staticFileHandler())

	log.Println("Go server is listening:")
	log.Println("  - http://localhost:8088  (HTTP)")
	log.Println("(API: /api/execute, /api/chat, /api/chat/ws, /api/grade, /api/memory, /api/summarize)")

	if err := http.ListenAndServe(":8088", nil); err != nil {
		log.Fatalf("server startup failed: %v", err)
	}
}
