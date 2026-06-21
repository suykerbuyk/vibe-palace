// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestNewEnricherFromConfig_Disabled(t *testing.T) {
	// A disabled config is not an error: (nil, nil).
	e, err := NewEnricherFromConfig(storage.EnrichmentConfig{Enabled: false}, "")
	if err != nil {
		t.Fatalf("disabled config: unexpected error: %v", err)
	}
	if e != nil {
		t.Errorf("disabled config: expected nil enricher, got %v", e)
	}
}

func TestNewEnricherFromConfig_MissingAPIKeyEnvName(t *testing.T) {
	// Enabled but APIKeyEnv unset (empty name) → error, nil enricher.
	e, err := NewEnricherFromConfig(storage.EnrichmentConfig{
		Enabled:  true,
		Provider: "anthropic",
		Model:    "claude-test",
	}, "")
	if err == nil {
		t.Fatal("expected error when api key env name is empty")
	}
	if e != nil {
		t.Errorf("expected nil enricher on error, got %v", e)
	}
}

func TestNewEnricherFromConfig_APIKeyEnvUnset(t *testing.T) {
	// Enabled, env name given, but the env var is empty → error, nil enricher.
	t.Setenv("VP_TEST_ENRICH_KEY", "")
	e, err := NewEnricherFromConfig(storage.EnrichmentConfig{
		Enabled:   true,
		Provider:  "anthropic",
		Model:     "claude-test",
		APIKeyEnv: "VP_TEST_ENRICH_KEY",
	}, "")
	if err == nil {
		t.Fatal("expected error when api key env is empty")
	}
	if e != nil {
		t.Errorf("expected nil enricher on error, got %v", e)
	}
}

func TestNewEnricherFromConfig_Anthropic(t *testing.T) {
	// Enabled + anthropic + a set key → non-nil enricher, nil error.
	// Anthropic tolerates an empty endpoint (defaults internally) and
	// NewEnricher does not call the API.
	t.Setenv("VP_TEST_ENRICH_KEY", "sk-test-123")
	e, err := NewEnricherFromConfig(storage.EnrichmentConfig{
		Enabled:        true,
		Provider:       "anthropic",
		Model:          "claude-test",
		APIKeyEnv:      "VP_TEST_ENRICH_KEY",
		TimeoutSeconds: 5,
	}, "")
	if err != nil {
		t.Fatalf("anthropic config: unexpected error: %v", err)
	}
	if e == nil {
		t.Fatal("anthropic config: expected non-nil enricher")
	}
	if e.Model() != "claude-test" {
		t.Errorf("Model() = %q, want %q", e.Model(), "claude-test")
	}
}

func TestNewEnricherFromConfig_OpenAICompatibleRequiresBaseURL(t *testing.T) {
	// Enabled, openai-compatible provider (empty provider string), key set,
	// but no BaseURL → the OpenAI-compatible client requires an endpoint, so
	// the completer build fails: (nil, err).
	t.Setenv("VP_TEST_ENRICH_KEY", "sk-test-123")
	e, err := NewEnricherFromConfig(storage.EnrichmentConfig{
		Enabled:   true,
		Provider:  "",
		Model:     "grok-test",
		APIKeyEnv: "VP_TEST_ENRICH_KEY",
		// BaseURL intentionally empty.
	}, "")
	if err == nil {
		t.Fatal("expected error for openai-compatible provider with empty base_url")
	}
	if e != nil {
		t.Errorf("expected nil enricher on error, got %v", e)
	}
}

func TestNewEnricherFromConfig_OpenAICompatibleWithBaseURL(t *testing.T) {
	// Enabled, openai-compatible, key set, BaseURL present → non-nil enricher.
	t.Setenv("VP_TEST_ENRICH_KEY", "sk-test-123")
	e, err := NewEnricherFromConfig(storage.EnrichmentConfig{
		Enabled:   true,
		Provider:  "openai",
		Model:     "grok-test",
		APIKeyEnv: "VP_TEST_ENRICH_KEY",
		BaseURL:   "https://example.invalid/v1",
	}, "")
	if err != nil {
		t.Fatalf("openai-compatible config: unexpected error: %v", err)
	}
	if e == nil {
		t.Fatal("openai-compatible config: expected non-nil enricher")
	}
	if e.Model() != "grok-test" {
		t.Errorf("Model() = %q, want %q", e.Model(), "grok-test")
	}
}
