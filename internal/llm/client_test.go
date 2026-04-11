// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClient_Validation(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"missing endpoint", Config{Model: "m", APIKey: "k"}},
		{"missing model", Config{Endpoint: "http://x", APIKey: "k"}},
		{"missing api_key", Config{Endpoint: "http://x", Model: "m"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClient(tt.cfg); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestNewClient_Valid(t *testing.T) {
	c, err := NewClient(Config{Endpoint: "http://x", Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("client should not be nil")
	}
}

func TestChatCompletion_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
		}

		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "test-model" {
			t.Errorf("model = %q, want %q", req.Model, "test-model")
		}
		if req.Temperature != 0 {
			t.Errorf("temperature = %f, want 0", req.Temperature)
		}

		resp := Response{
			Choices: []Choice{{Message: Message{Role: "assistant", Content: "hello"}}},
			Usage:   Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Endpoint: srv.URL, Model: "test-model", APIKey: "test-key", MaxTokens: 100})
	resp, err := c.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "hello" {
		t.Errorf("content = %q, want %q", resp.Choices[0].Message.Content, "hello")
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("total_tokens = %d, want 15", resp.Usage.TotalTokens)
	}
}

func TestChatCompletion_RateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("rate limited"))
			return
		}
		resp := Response{Choices: []Choice{{Message: Message{Content: "ok"}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	c.initialBackoff = 1 * time.Millisecond
	resp, err := c.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Choices[0].Message.Content, "ok")
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3 (2 retries + 1 success)", got)
	}
}

func TestChatCompletion_ServerError_Exhausted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	c.initialBackoff = 1 * time.Millisecond
	_, err := c.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("calls = %d, want 4 (1 initial + 3 retries)", got)
	}
}

func TestChatCompletion_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	_, err := c.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestChatCompletion_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Response{Choices: []Choice{}})
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	_, err := c.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestChatCompletion_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	c.initialBackoff = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.ChatCompletion(ctx, []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error on context cancel")
	}
}

func TestChatCompletion_ClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	_, err := c.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for 400")
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"hello world", 3}, // 11 chars → (11+3)/4 = 3
	}
	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
