// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestListProjectsEmpty(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ListProjectsTool(vault)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]any)
	projects := m["projects"].([]string)
	if len(projects) != 0 {
		t.Errorf("projects = %v", projects)
	}
}

func TestListProjectsPopulated(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	// Create palace dirs for two projects.
	for _, proj := range []string{"alpha", "beta"} {
		dir := filepath.Join(vault.Root, "palace", proj)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	tool := ListProjectsTool(vault)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]any)
	projects := m["projects"].([]string)
	if len(projects) != 2 {
		t.Fatalf("projects = %v, want 2", projects)
	}
}

func TestAppendIterationTool(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := AppendIterationTool(vault)

	params, _ := json.Marshal(appendIterationParams{
		Project: "test-proj",
		Content: "## Iteration 1\nDid some work.",
	})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "appended" {
		t.Errorf("status = %q", m["status"])
	}

	// Verify content on disk.
	path, _ := vault.IterationsFile("test-proj")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "Iteration 1") {
		t.Errorf("missing content: %q", string(data))
	}
}

func TestAppendIterationValidation(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := AppendIterationTool(vault)

	tests := []struct {
		name   string
		params appendIterationParams
	}{
		{"empty project", appendIterationParams{Project: "", Content: "x"}},
		{"empty content", appendIterationParams{Project: "proj", Content: ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := json.Marshal(tt.params)
			if _, err := tool.Handler(context.Background(), p); err == nil {
				t.Error("expected error")
			}
		})
	}
}
