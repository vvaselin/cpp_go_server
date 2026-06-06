package app

import (
	"net/http"
	"strings"
)

func staticFileHandler() http.Handler {
	fs := http.FileServer(http.Dir(staticDir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, ".go") || strings.Contains(r.URL.Path, ".env") || strings.Contains(r.URL.Path, ".mod") {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodOptions {
			corsMiddleware(fs).ServeHTTP(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowedOrigins := []string{
			"http://localhost:8088",
			"https://lab.wasabi-apple.com",
		}
		origin := r.Header.Get("Origin")
		for _, allowedOrigin := range allowedOrigins {
			if origin == allowedOrigin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				break
			}
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
