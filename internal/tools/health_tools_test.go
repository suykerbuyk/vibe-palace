// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestHealthToolHealthy(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := HealthTool(vault)

	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if hr.Status != "healthy" {
		t.Errorf("status = %q, want healthy", hr.Status)
	}
}

func TestHealthToolWarnings(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entries := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"entity extraction: add entity failed","err":"disk full"}`, now),
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"entity extraction: duplicate entity skipped","entity":"test"}`, now),
		fmt.Sprintf(`{"time":"%s","level":"INFO","msg":"normal operation"}`, now),
	}

	logPath := filepath.Join(logDir, "vp.log")
	var content string
	for _, e := range entries {
		content += e + "\n"
	}
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if hr.Status != "warnings" {
		t.Errorf("status = %q, want warnings", hr.Status)
	}
	if len(hr.RecentWarns) != 2 {
		t.Errorf("recent warns = %d, want 2", len(hr.RecentWarns))
	}
	if hr.WarnCounts["entity extraction"] != 2 {
		t.Errorf("warn count for entity extraction = %d, want 2", hr.WarnCounts["entity extraction"])
	}
}

func TestHealthToolErrors(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entry := fmt.Sprintf(`{"time":"%s","level":"ERROR","msg":"critical failure"}`, now)
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(entry+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if hr.Status != "errors" {
		t.Errorf("status = %q, want errors", hr.Status)
	}
}

func TestHealthToolMalformedLines(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	content := "this is not json\n" +
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"valid warn"}`, now) + "\n" +
		"{broken json\n"
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if len(hr.RecentWarns) != 1 {
		t.Errorf("recent warns = %d, want 1 (malformed lines skipped)", len(hr.RecentWarns))
	}
}

func TestHealthToolOldEntries(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	entry := fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"old warning"}`, old)
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(entry+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if hr.Status != "healthy" {
		t.Errorf("status = %q, want healthy (old entries excluded)", hr.Status)
	}
	if len(hr.RecentWarns) != 0 {
		t.Errorf("recent warns = %d, want 0", len(hr.RecentWarns))
	}
}
