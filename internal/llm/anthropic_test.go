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

func TestAnthropicComplete_RequestShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want %q", got, "test-key")
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
		}
		if got := r.Header.Get("content-type"); got != "application/json" {
			t.Errorf("content-type = %q, want %q", got, "application/json")
		}

		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "claude-test" {
			t.Errorf("model = %q, want %q", req.Model, "claude-test")
		}
		if req.MaxTokens != anthropicDefaultMaxTokens {
			t.Errorf("max_tokens = %d, want %d (default)", req.MaxTokens, anthropicDefaultMaxTokens)
		}
		if req.System != "be helpful" {
			t.Errorf("system = %q, want %q", req.System, "be helpful")
		}
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "hi" {
			t.Errorf("messages = %+v, want single user 'hi'", req.Messages)
		}

		w.Write([]byte(`{"content":[{"type":"text","text":"hello there"}]}`))
	}))
	defer srv.Close()

	a, _ := newAnthropicClient(Config{Endpoint: srv.URL, Model: "claude-test", APIKey: "test-key"})
	got, err := a.Complete(context.Background(), "be helpful", "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello there" {
		t.Errorf("content = %q, want %q", got, "hello there")
	}
}

func TestAnthropicComplete_MaxTokensOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropicRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.MaxTokens != 256 {
			t.Errorf("max_tokens = %d, want 256", req.MaxTokens)
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	a, _ := newAnthropicClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k", MaxTokens: 256})
	if _, err := a.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnthropicComplete_RetryOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("rate limited"))
			return
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"recovered"}]}`))
	}))
	defer srv.Close()

	a, _ := newAnthropicClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	a.initialBackoff = time.Millisecond
	got, err := a.Complete(context.Background(), "s", "u")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "recovered" {
		t.Errorf("content = %q, want %q", got, "recovered")
	}
	if c := calls.Load(); c != 3 {
		t.Errorf("calls = %d, want 3", c)
	}
}

func TestAnthropicComplete_NonOKError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid request"))
	}))
	defer srv.Close()

	a, _ := newAnthropicClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	if _, err := a.Complete(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error for 400")
	}
}

func TestAnthropicComplete_EmptyContentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[]}`))
	}))
	defer srv.Close()

	a, _ := newAnthropicClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	if _, err := a.Complete(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestAnthropicComplete_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	a, _ := newAnthropicClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	if _, err := a.Complete(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestAnthropicComplete_FirstTextBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[{"type":"thinking","text":"let me reason about this"},{"type":"text","text":"REAL"}]}`))
	}))
	defer srv.Close()

	a, _ := newAnthropicClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	got, err := a.Complete(context.Background(), "s", "u")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "REAL" {
		t.Errorf("content = %q, want %q (first text block, not Content[0])", got, "REAL")
	}
}

func TestAnthropicComplete_NoTextBlockError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[{"type":"thinking","text":"x"}]}`))
	}))
	defer srv.Close()

	a, _ := newAnthropicClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	if _, err := a.Complete(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error when response carries no text content block")
	}
}

func TestNewAnthropicClient_DefaultEndpoint(t *testing.T) {
	a, err := newAnthropicClient(Config{Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.cfg.Endpoint != "" {
		t.Errorf("endpoint = %q, want empty (defaulted at call time)", a.cfg.Endpoint)
	}
}
