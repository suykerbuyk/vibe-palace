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

const (
	anthropicDefaultBaseURL   = "https://api.anthropic.com"
	anthropicVersion          = "2023-06-01"
	anthropicDefaultMaxTokens = 4096
)

// anthropicClient is a native Anthropic Messages-API client satisfying
// Completer.
type anthropicClient struct {
	cfg            Config
	http           *http.Client
	initialBackoff time.Duration // default 1s; tests can lower
}

// newAnthropicClient creates a native Anthropic client. The endpoint may
// default; model and api key are required.
func newAnthropicClient(cfg Config) (*anthropicClient, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm: model is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("llm: api_key is required")
	}
	return &anthropicClient{
		cfg:            cfg,
		http:           &http.Client{Timeout: 120 * time.Second},
		initialBackoff: 1 * time.Second,
	}, nil
}

// anthropicRequest is the Messages-API request body.
type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
}

// anthropicMessage is a single message in the Messages-API request.
type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse is the Messages-API response shape.
type anthropicResponse struct {
	Content []anthropicContent `json:"content"`
}

// anthropicContent is a single content block in the response.
type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Name returns a stable identifier for the client.
func (a *anthropicClient) Name() string {
	return "anthropic/" + a.cfg.Model
}

// Complete sends a system/user pair to the Anthropic Messages API and returns
// the first text content block. Handles rate limiting (429) and server errors
// (5xx) with exponential backoff.
func (a *anthropicClient) Complete(ctx context.Context, system, user string) (string, error) {
	maxTokens := a.cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	reqBody := anthropicRequest{
		Model:       a.cfg.Model,
		MaxTokens:   maxTokens,
		Temperature: 0.0,
		System:      system,
		Messages:    []anthropicMessage{{Role: "user", Content: user}},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	base := a.cfg.Endpoint
	if base == "" {
		base = anthropicDefaultBaseURL
	}
	url := base + "/v1/messages"

	resp, err := retryWithBackoff(ctx, a.initialBackoff, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("llm: create request: %w", err)
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-api-key", a.cfg.APIKey)
		req.Header.Set("anthropic-version", anthropicVersion)
		return a.http.Do(req)
	})
	if err != nil {
		return "", err
	}

	respBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return "", fmt.Errorf("llm: read response: %w", readErr)
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", fmt.Errorf("llm: %d after %d retries: %s", resp.StatusCode, maxRetries, truncate(respBody, 200))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm: %d: %s", resp.StatusCode, truncate(respBody, 200))
	}

	var result anthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("llm: unmarshal response: %w", err)
	}
	// Return the first text block. The API may lead with a non-text block
	// (e.g. a thinking block) and carry the answer in a later text block, so
	// scan rather than assuming Content[0] is text.
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("llm: response contains no text content")
}
