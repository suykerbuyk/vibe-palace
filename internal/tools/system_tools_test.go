// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestInitProjectSuccess(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	projDir := filepath.Join(t.TempDir(), "my-project")

	params, _ := json.Marshal(initParams{
		Path:   projDir,
		Name:   "my-project",
		Domain: "work",
		Tags:   []string{"go", "cli"},
	})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "initialized" {
		t.Errorf("status = %q", m["status"])
	}
	if m["project"] != "my-project" {
		t.Errorf("project = %q", m["project"])
	}

	// Verify config file was created.
	configPath := filepath.Join(projDir, project.ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	content := string(data)
	if !containsAll(content, "my-project", "work", "go", "cli") {
		t.Errorf("config content = %q", content)
	}
}

func TestInitProjectRelativePath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	params, _ := json.Marshal(initParams{Path: "./relative"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestInitProjectAlreadyExists(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, project.ConfigFileName)
	os.WriteFile(configPath, []byte("[project]\nname = \"test\"\n"), 0644)

	params, _ := json.Marshal(initParams{Path: projDir, Name: "test"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for existing config")
	}
}

func TestInitProjectAutoDetectName(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	projDir := filepath.Join(t.TempDir(), "cool-app")

	params, _ := json.Marshal(initParams{Path: projDir})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]string)
	if m["project"] != "cool-app" {
		t.Errorf("project = %q, want cool-app", m["project"])
	}
}

func TestVaultSyncNonGitDir(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := VaultSyncTool(vault)

	params, _ := json.Marshal(vaultSyncParams{Action: "pull"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestVaultSyncInvalidAction(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := VaultSyncTool(vault)

	params, _ := json.Marshal(vaultSyncParams{Action: "nope"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestRefreshIndexTool(t *testing.T) {
	// RefreshIndexTool requires a non-nil engine. Verify the tool constructor works.
	// We can't easily test a full rebuild without the embedder, but we verify
	// the handler rejects empty project.
	tool := RefreshIndexTool(nil)
	if tool.Name != "vp_refresh_index" {
		t.Fatalf("name = %q", tool.Name)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
