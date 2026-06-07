package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/nedpals/supabase-go"
)

const staticDir = "../TyranoEdu"

var gradeSystemPrompt string

const MEMORY_FILE = "user_memory.json"

var summarySystemPrompt string

var supabaseClient *supabase.Client

func Run() {
	loadEnv()

	supabaseURL := cleanEnvValue(os.Getenv("SUPABASE_URL"))
	supabaseKey := cleanEnvValue(os.Getenv("SUPABASE_KEY"))
	//log.Printf("INFO: SUPABASE_URL configured: %t", supabaseURL != "")
	//log.Printf("INFO: SUPABASE_KEY configured: %t", supabaseKey != "")
	if supabaseKey != "" {
		//log.Printf("INFO: Supabase key type: %s", supabaseKeyType(supabaseKey))
	}
	if supabaseURL == "" || supabaseKey == "" {
		log.Println("WARNING: SUPABASE_URL or SUPABASE_KEY is not configured. DB features are disabled.")
	} else if err := validateSupabaseURL(supabaseURL); err != nil {
		log.Printf("ERROR: invalid SUPABASE_URL: %v", err)
		log.Println("WARNING: DB features are disabled until SUPABASE_URL is set to https://<project-ref>.supabase.co")
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

func supabaseKeyType(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) < 2 {
		return "unknown_non_jwt"
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "unknown_jwt"
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "unknown_jwt"
	}

	if role, ok := claims["role"].(string); ok && role != "" {
		return role
	}
	if ref, ok := claims["ref"].(string); ok && ref != "" {
		return "secret_key_ref_" + ref
	}
	return "unknown_jwt"
}

func cleanEnvValue(value string) string {
	return strings.Trim(strings.TrimSpace(value), "\"")
}

func validateSupabaseURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme must be https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("host is empty")
	}
	if !strings.HasSuffix(u.Host, ".supabase.co") {
		return fmt.Errorf("host must end with .supabase.co, got %q", u.Host)
	}
	return nil
}
