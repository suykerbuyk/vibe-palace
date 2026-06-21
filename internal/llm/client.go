// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Caller is the interface for LLM chat completions.
// *Client satisfies this; tests inject a mock.
type Caller interface {
	ChatCompletion(ctx context.Context, messages []Message) (*Response, error)
}

// Config holds resolved LLM runtime configuration.
// Compare with storage.LLMConfig which holds the TOML-level settings
// (APIKeyEnv rather than APIKey).
type Config struct {
	Endpoint  string // base URL, e.g. "https://api.x.ai/v1"
	Model     string
	APIKey    string // resolved key value (not env var name)
	MaxTokens int
}

// Message is a chat completion message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is the chat completion request body.
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature"`
}

// Response is the chat completion response.
type Response struct {
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice is a single response choice.
type Choice struct {
	Message Message `json:"message"`
}

// Usage holds token usage from the API response.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Client is a reusable OpenAI-compatible LLM HTTP client.
type Client struct {
	cfg            Config
	http           *http.Client
	initialBackoff time.Duration // default 1s; tests can lower
}

// NewClient creates a new LLM client. Returns an error if config is invalid.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("llm: endpoint is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm: model is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("llm: api_key is required")
	}
	return &Client{
		cfg:            cfg,
		http:           &http.Client{Timeout: 120 * time.Second},
		initialBackoff: 1 * time.Second,
	}, nil
}

const maxRetries = 3

// ChatCompletion sends a chat completion request and returns the response.
// Handles rate limiting (429) and server errors (5xx) with exponential backoff.
func (c *Client) ChatCompletion(ctx context.Context, messages []Message) (*Response, error) {
	reqBody := Request{
		Model:       c.cfg.Model,
		Messages:    messages,
		MaxTokens:   c.cfg.MaxTokens,
		Temperature: 0.0,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}

	url := c.cfg.Endpoint + "/chat/completions"

	resp, err := retryWithBackoff(ctx, c.initialBackoff, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("llm: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		return c.http.Do(req)
	})
	if err != nil {
		return nil, err
	}

	respBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("llm: read response: %w", readErr)
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, fmt.Errorf("llm: %d after %d retries: %s", resp.StatusCode, maxRetries, truncate(respBody, 200))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: %d: %s", resp.StatusCode, truncate(respBody, 200))
	}

	var result Response
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("llm: unmarshal response: %w", err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("llm: response contains no choices")
	}

	return &result, nil
}

// EstimateTokens returns a rough token count using the 4-chars-per-token heuristic.
func EstimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// truncate returns the first n bytes of b as a string.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
