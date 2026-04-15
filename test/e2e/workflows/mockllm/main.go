// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// mockllm is a minimal OpenAI-compatible mock server for e2e workflow rigs.
//
// On POST /chat/completions it returns the contents of $MOCK_RESPONSE_FILE
// as the assistant message's .content string (the tune code parses it as
// JSON). Request events are appended to $MOCK_LOG as JSONL.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	port := flag.Int("port", 0, "listen port (0 = ephemeral)")
	flag.Parse()

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatalf("mockllm: listen: %v", err)
	}
	// Line 1 of stdout: chosen port (parsed by start_mock_llm bash helper).
	fmt.Println(ln.Addr().(*net.TCPAddr).Port)

	srv := &http.Server{Handler: newHandler()}
	log.Printf("mockllm: serving on %s", ln.Addr())
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("mockllm: serve: %v", err)
	}
}

func newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", handleChat)
	return mux
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	logRequest(len(body))

	content := ""
	if p := os.Getenv("MOCK_RESPONSE_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			log.Printf("mockllm: read MOCK_RESPONSE_FILE %s: %v", p, err)
		} else {
			content = string(b)
		}
	}

	promptTok := intEnv("MOCK_PROMPT_TOKENS", 10)
	complTok := intEnv("MOCK_COMPLETION_TOKENS", 10)

	resp := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTok,
			"completion_tokens": complTok,
			"total_tokens":      promptTok + complTok,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func logRequest(nBytes int) {
	p := os.Getenv("MOCK_LOG")
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("mockllm: open MOCK_LOG: %v", err)
		return
	}
	defer f.Close()
	row := map[string]any{
		"ts":     time.Now().UnixMilli(),
		"method": "POST",
		"path":   "/chat/completions",
		"bytes":  nBytes,
	}
	b, _ := json.Marshal(row)
	if _, err := f.Write(append(b, '\n')); err != nil {
		log.Printf("mockllm: write MOCK_LOG: %v", err)
	}
}

func intEnv(name string, def int) int {
	if s := os.Getenv(name); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}
