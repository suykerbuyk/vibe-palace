// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package llm

import (
	"context"
	"fmt"
)

// Completer is the interface for single-shot system/user completions.
// Both *Client (OpenAI-compatible) and the native Anthropic client satisfy it.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
	Name() string
}

// Complete sends a system/user pair as a chat completion and returns the
// assistant's text content.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	resp, err := c.ChatCompletion(ctx, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("llm: response contains no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

// Name returns a stable identifier for the client (the configured model).
func (c *Client) Name() string {
	if c.cfg.Model != "" {
		return c.cfg.Model
	}
	return "openai-compatible"
}

// NewCompleter builds a Completer for the given provider. When provider is
// "anthropic" it returns the native Anthropic client; otherwise (default,
// empty, "openai", etc.) it returns an OpenAI-compatible *Client.
func NewCompleter(provider string, cfg Config) (Completer, error) {
	if provider == "anthropic" {
		return newAnthropicClient(cfg)
	}
	return NewClient(cfg)
}
