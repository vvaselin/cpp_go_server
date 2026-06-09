package app

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
	"strings"
	"time"
)

func executeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST method only", http.StatusMethodNotAllowed)
		return
	}

	var payload CodePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Printf("ERROR(/api/execute): invalid JSON: %v", err)
		sendErrorJSON(w, "invalid request")
		return
	}

	dir, err := os.MkdirTemp("", "cpp-execution-")
	if err != nil {
		log.Printf("ERROR: temp dir creation failed: %v", err)
		sendErrorJSON(w, "server error: failed to create temp directory")
		return
	}
	defer os.RemoveAll(dir)

	if err := os.WriteFile(filepath.Join(dir, "main.cpp"), []byte(payload.Code), 0666); err != nil {
		log.Printf("ERROR: main.cpp write failed: %v", err)
		sendErrorJSON(w, "server error: failed to write source file")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	compileAndRunScript := "g++ -Wall /usr/src/app/main.cpp -o /usr/src/app/main.out && /usr/src/app/main.out"
	//log.Printf("INFO: running Docker C++ execution")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"--rm",
		"-i",
		"--net=none",
		"-v", fmt.Sprintf("%s:/usr/src/app", dir),
		"gcc:latest",
		"sh", "-c", compileAndRunScript,
	)

	if payload.Stdin != "" {
		runCmd.Stdin = strings.NewReader(payload.Stdin)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	runCmd.Stdout = &out
	runCmd.Stderr = &stderr
	err = runCmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		log.Println("ERROR: Docker run timed out")
		sendErrorJSON(w, "execution timed out (10 seconds)")
		return
	}

	if err != nil {
		sendErrorJSON(w, stderr.String())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(ResultPayload{Result: out.String()})
}

func sendErrorJSON(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(ResultPayload{Result: "エラー:\n" + errMsg})
}
