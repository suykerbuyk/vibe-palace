// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestRunDiscoverRooms_NoCandidates(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", cfg, 0, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "No unclassified content") {
		t.Errorf("expected 'No unclassified content' in output: %s", buf.String())
	}
}

func TestRunDiscoverRooms_Estimate(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{}

	for i := range 4 {
		d := storage.Drawer{
			Content:    "some unclassified content " + string(rune('a'+i)),
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-10T10:00:00Z",
		}
		v.AppendDrawer("proj", "proj", "general", d)
	}

	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", cfg, 0, false, true, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Token Estimate") {
		t.Errorf("expected token estimate in output: %s", out)
	}
	if !strings.Contains(out, "Prompt tokens") {
		t.Errorf("expected prompt token count in output: %s", out)
	}
}

func TestRunDiscoverRooms_NoLLMConfig(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{}

	d := storage.Drawer{
		Content:    "some general content",
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	v.AppendDrawer("proj", "proj", "general", d)

	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", cfg, 0, false, false, "", &buf)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (ExitUser)", code, cli.ExitUser)
	}
	if !strings.Contains(buf.String(), "LLM not configured") {
		t.Errorf("expected 'LLM not configured' in output: %s", buf.String())
	}
}

func TestRunDiscoverRooms_MissingAPIKey(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{
		PalaceLLM: storage.LLMConfig{
			Endpoint:  "http://localhost:1234",
			Model:     "test-model",
			APIKeyEnv: "VIBE_PALACE_TEST_NONEXISTENT_KEY_12345",
		},
	}

	d := storage.Drawer{
		Content:    "some general content",
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	v.AppendDrawer("proj", "proj", "general", d)

	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", cfg, 0, false, false, "", &buf)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (ExitUser)", code, cli.ExitUser)
	}
	if !strings.Contains(buf.String(), "API key environment variable") {
		t.Errorf("expected API key error in output: %s", buf.String())
	}
}

func TestRunDiscoverRooms_Export(t *testing.T) {
	v := testVault(t)

	// Mock LLM returns empty array (no keywords found).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "[]"}},
			},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("VP_TEST_API_KEY", "test-key")
	cfg := storage.Config{
		PalaceLLM: storage.LLMConfig{
			Endpoint:  srv.URL,
			Model:     "test-model",
			APIKeyEnv: "VP_TEST_API_KEY",
			MaxTokens: 100,
		},
	}

	d := storage.Drawer{
		Content:    "some unclassified general content",
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	v.AppendDrawer("proj", "proj", "general", d)

	exportPath := filepath.Join(t.TempDir(), "report.json")
	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", cfg, 0, false, false, exportPath, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0; output: %s", code, buf.String())
	}

	// Verify export file exists and is valid JSON.
	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var report palace.DiscoverReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if report.Project != "proj" {
		t.Errorf("project = %q, want %q", report.Project, "proj")
	}
}

func TestRunDiscoverRooms_WithProposals(t *testing.T) {
	v := testVault(t)

	// 3 general drawers with "neural network" content.
	for i := range 3 {
		d := storage.Drawer{
			Content:    "neural network transformer training " + string(rune('a'+i)),
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-10T10:00:00Z",
		}
		v.AppendDrawer("proj", "proj", "general", d)
	}

	// Mock LLM suggests "neural network" keyword for devops.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": `[
					{"drawer_id": "any", "room": "devops", "keywords": [
						{"keyword": "neural network", "weight": "high"}
					]}
				]`}},
			},
			"usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("VP_TEST_API_KEY", "test-key")
	cfg := storage.Config{
		PalaceLLM: storage.LLMConfig{
			Endpoint: srv.URL, Model: "m", APIKeyEnv: "VP_TEST_API_KEY", MaxTokens: 100,
		},
	}

	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", cfg, 0, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0; output: %s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "Results:") {
		t.Errorf("expected Results summary: %s", out)
	}
	// Should have TOML diff or no-proposals message.
	if !strings.Contains(out, "palace.scoring") && !strings.Contains(out, "No viable keyword") {
		t.Errorf("expected TOML diff or no-proposals message: %s", out)
	}
}

func TestRunDiscoverRooms_Apply(t *testing.T) {
	v := testVault(t)

	// 3 general drawers with matching content.
	for i := range 3 {
		d := storage.Drawer{
			Content:    "neural network transformer training " + string(rune('a'+i)),
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-10T10:00:00Z",
		}
		v.AppendDrawer("proj", "proj", "general", d)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": `[
					{"drawer_id": "any", "room": "devops", "keywords": [
						{"keyword": "neural network", "weight": "high"}
					]}
				]`}},
			},
			"usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("VP_TEST_API_KEY", "test-key")
	cfg := storage.Config{
		PalaceLLM: storage.LLMConfig{
			Endpoint: srv.URL, Model: "m", APIKeyEnv: "VP_TEST_API_KEY", MaxTokens: 100,
		},
	}

	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", cfg, 0, true, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0; output: %s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "Applied") && !strings.Contains(out, "No viable keyword") {
		t.Errorf("expected 'Applied' or no-proposals message: %s", out)
	}

	// If applied, verify config was written.
	if strings.Contains(out, "Applied") {
		reloaded, err := v.LoadConfig("proj")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if reloaded.PalaceScoringOverrides == nil {
			t.Error("expected scoring overrides after apply")
		}
	}
}

func TestRunDiscoverRooms_NoProposals(t *testing.T) {
	v := testVault(t)

	// Mock LLM returns empty responses (no keywords).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "[]"}},
			},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("VP_TEST_API_KEY", "test-key")
	cfg := storage.Config{
		PalaceLLM: storage.LLMConfig{
			Endpoint: srv.URL, Model: "m", APIKeyEnv: "VP_TEST_API_KEY", MaxTokens: 100,
		},
	}

	d := storage.Drawer{
		Content:    "some general unclassifiable content",
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	v.AppendDrawer("proj", "proj", "general", d)

	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", cfg, 0, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "No viable keyword") {
		t.Errorf("expected 'No viable keyword' in output: %s", buf.String())
	}
}

func TestDiscoveryProposalsToOverrides(t *testing.T) {
	proposals := []palace.KeywordProposal{
		{Room: "devops", Keyword: "orchestration", Weight: "high"},
		{Room: "devops", Keyword: "pipeline runner", Weight: "medium"},
		{Room: "data", Keyword: "etl", Weight: "low"},
	}

	rooms := discoveryProposalsToOverrides(proposals)
	if len(rooms) != 2 {
		t.Fatalf("expected 2 rooms, got %d", len(rooms))
	}

	devops := rooms["devops"]
	if len(devops.High) != 1 || devops.High[0] != "orchestration" {
		t.Errorf("devops.High = %v, want [orchestration]", devops.High)
	}
	if len(devops.Medium) != 1 || devops.Medium[0] != "pipeline runner" {
		t.Errorf("devops.Medium = %v, want [pipeline runner]", devops.Medium)
	}

	data := rooms["data"]
	if len(data.Low) != 1 || data.Low[0] != "etl" {
		t.Errorf("data.Low = %v, want [etl]", data.Low)
	}
}
