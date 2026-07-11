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

func TestRunTuneRooms_NoSamples(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runTuneRooms(v, "proj", cfg, 0, false, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "No samples found") {
		t.Errorf("expected 'No samples found' in output: %s", buf.String())
	}
}

func TestRunTuneRooms_Estimate(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{}

	// Add some drawers so we have samples.
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
	code := runTuneRooms(v, "proj", cfg, 0, false, true, false, "", &buf)
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

func TestRunTuneRooms_NoLLMConfig(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{}

	d := storage.Drawer{
		Content:    "some content for testing",
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	v.AppendDrawer("proj", "proj", "general", d)

	var buf bytes.Buffer
	code := runTuneRooms(v, "proj", cfg, 0, false, false, false, "", &buf)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (ExitUser)", code, cli.ExitUser)
	}
	if !strings.Contains(buf.String(), "LLM not configured") {
		t.Errorf("expected 'LLM not configured' in output: %s", buf.String())
	}
}

func TestRunTuneRooms_MissingAPIKey(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{
		PalaceLLM: storage.LLMConfig{
			Endpoint:  "http://localhost:1234",
			Model:     "test-model",
			APIKeyEnv: "VIBE_PALACE_TEST_NONEXISTENT_KEY_12345",
		},
	}

	d := storage.Drawer{
		Content:    "some content for testing",
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	v.AppendDrawer("proj", "proj", "general", d)

	var buf bytes.Buffer
	code := runTuneRooms(v, "proj", cfg, 0, false, false, false, "", &buf)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (ExitUser)", code, cli.ExitUser)
	}
	if !strings.Contains(buf.String(), "API key environment variable") {
		t.Errorf("expected API key error in output: %s", buf.String())
	}
}

func TestRunTuneRooms_Export(t *testing.T) {
	v := testVault(t)

	// Set up a mock LLM server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return empty judgments — all drawers stay as-is.
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
		Content:    "some unclassifiable content",
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	v.AppendDrawer("proj", "proj", "general", d)

	exportPath := filepath.Join(t.TempDir(), "report.json")
	var buf bytes.Buffer
	code := runTuneRooms(v, "proj", cfg, 0, false, false, false, exportPath, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0; output: %s", code, buf.String())
	}

	// Verify export file exists and is valid JSON.
	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var report palace.TuneReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if report.Project != "proj" {
		t.Errorf("project = %q, want %q", report.Project, "proj")
	}
}

func TestRunTuneRooms_Verbose(t *testing.T) {
	v := testVault(t)

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

	d := storage.Drawer{Content: "some general content", Hall: "facts", SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z"}
	v.AppendDrawer("proj", "proj", "general", d)

	var buf bytes.Buffer
	code := runTuneRooms(v, "proj", cfg, 0, false, false, true, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "Results:") {
		t.Errorf("expected Results summary: %s", buf.String())
	}
}

func TestRunTuneRooms_ApplyNoProposals(t *testing.T) {
	v := testVault(t)

	// Mock LLM agrees with everything.
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

	d := storage.Drawer{Content: "unclassifiable stuff", Hall: "facts", SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z"}
	v.AppendDrawer("proj", "proj", "general", d)

	var buf bytes.Buffer
	code := runTuneRooms(v, "proj", cfg, 0, true, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0; output: %s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "No proposals to apply") && !strings.Contains(out, "No weight changes proposed") {
		t.Errorf("expected no-change message in output: %s", out)
	}
}

func TestRunTuneRooms_Apply(t *testing.T) {
	v := testVault(t)

	// Create 4 general drawers with "test" keyword content.
	for i := range 4 {
		d := storage.Drawer{
			Content:    "this is a test of subsystem " + string(rune('a'+i)),
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-10T10:00:00Z",
		}
		v.AppendDrawer("proj", "proj", "general", d)
	}

	// Get drawer IDs for mock response.
	drawers, _ := v.ListDrawers("proj", "proj", "general")
	var judgments []map[string]any
	for _, d := range drawers {
		judgments = append(judgments, map[string]any{
			"drawer_id":  d.ID,
			"room":       "testing",
			"confidence": 0.9,
			"reasoning":  "test content",
		})
	}
	respJSON, _ := json.Marshal(judgments)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": string(respJSON)}},
			},
			"usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
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

	var buf bytes.Buffer
	code := runTuneRooms(v, "proj", cfg, 0, true, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0; output: %s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "Applied") {
		t.Errorf("expected 'Applied' in output: %s", out)
	}

	// Verify config was written.
	reloaded, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if reloaded.PalaceScoringOverrides == nil {
		t.Fatal("expected scoring overrides after apply")
	}
}

func TestRunTuneRooms_WithUnmatchedFlags(t *testing.T) {
	v := testVault(t)

	// Add drawers with content that has no keywords for any room.
	for i := range 4 {
		d := storage.Drawer{
			Content:    "quantum computing research paper " + string(rune('a'+i)),
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-10T10:00:00Z",
		}
		v.AppendDrawer("proj", "proj", "general", d)
	}

	// Get drawer IDs.
	drawers, _ := v.ListDrawers("proj", "proj", "general")
	var judgments []map[string]any
	for _, d := range drawers {
		judgments = append(judgments, map[string]any{
			"drawer_id":  d.ID,
			"room":       "testing",
			"confidence": 0.8,
			"reasoning":  "LLM thinks testing",
		})
	}
	respJSON, _ := json.Marshal(judgments)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": string(respJSON)}},
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
	code := runTuneRooms(v, "proj", cfg, 0, false, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0; output: %s", code, buf.String())
	}
	out := buf.String()
	// Should show unmatched flags (content has no testing keywords).
	if !strings.Contains(out, "Unmatched content") && !strings.Contains(out, "No weight changes") {
		t.Errorf("expected unmatched or no-change output: %s", out)
	}
}
