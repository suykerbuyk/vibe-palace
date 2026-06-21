// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientComplete_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) != 2 {
			t.Errorf("messages = %d, want 2", len(req.Messages))
		}
		if req.Messages[0].Role != "system" || req.Messages[0].Content != "sys" {
			t.Errorf("system message = %+v", req.Messages[0])
		}
		if req.Messages[1].Role != "user" || req.Messages[1].Content != "usr" {
			t.Errorf("user message = %+v", req.Messages[1])
		}
		resp := Response{Choices: []Choice{{Message: Message{Role: "assistant", Content: "answer"}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	got, err := c.Complete(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "answer" {
		t.Errorf("content = %q, want %q", got, "answer")
	}
}

func TestClientComplete_PropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad"))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	if _, err := c.Complete(context.Background(), "sys", "usr"); err == nil {
		t.Fatal("expected error")
	}
}

func TestClientName(t *testing.T) {
	c, _ := NewClient(Config{Endpoint: "http://x", Model: "gpt-test", APIKey: "k"})
	if got := c.Name(); got != "gpt-test" {
		t.Errorf("Name() = %q, want %q", got, "gpt-test")
	}
}

func TestNewCompleter_OpenAIDefault(t *testing.T) {
	cfg := Config{Endpoint: "http://x", Model: "m", APIKey: "k"}
	for _, provider := range []string{"", "openai", "xai", "anything-else"} {
		comp, err := NewCompleter(provider, cfg)
		if err != nil {
			t.Fatalf("provider %q: unexpected error: %v", provider, err)
		}
		if _, ok := comp.(*Client); !ok {
			t.Errorf("provider %q: got %T, want *Client", provider, comp)
		}
	}
}

func TestNewCompleter_Anthropic(t *testing.T) {
	comp, err := NewCompleter("anthropic", Config{Model: "claude-test", APIKey: "k"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ac, ok := comp.(*anthropicClient)
	if !ok {
		t.Fatalf("got %T, want *anthropicClient", comp)
	}
	if got := ac.Name(); got != "anthropic/claude-test" {
		t.Errorf("Name() = %q, want %q", got, "anthropic/claude-test")
	}
}

func TestNewCompleter_AnthropicValidation(t *testing.T) {
	if _, err := NewCompleter("anthropic", Config{APIKey: "k"}); err == nil {
		t.Error("expected error for missing model")
	}
	if _, err := NewCompleter("anthropic", Config{Model: "m"}); err == nil {
		t.Error("expected error for missing api key")
	}
}
