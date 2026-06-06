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

// newProjectDir creates a project repo root with a .vibe-palace.toml naming it.
func newProjectDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, project.ConfigFileName)
	if err := os.WriteFile(cfg, []byte("[project]\nname = \""+name+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIngestCommitMsg_Success(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)

	projDir := newProjectDir(t, "my-proj")
	msg := "feat: do the thing\n\nbody line\n"
	if err := os.WriteFile(filepath.Join(projDir, "commit.msg"), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]string{"project_path": projDir})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := res.(map[string]any)
	if m["project"] != "my-proj" {
		t.Errorf("project = %v, want my-proj", m["project"])
	}

	// Verify the stamped vault copy exists with identical content.
	dest, err := vault.CommitMsgFile("my-proj")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("vault commit.msg not written: %v", err)
	}
	if string(got) != msg {
		t.Errorf("vault commit.msg = %q, want %q", got, msg)
	}
}

func TestIngestCommitMsg_ExplicitProject(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)

	projDir := t.TempDir() // no .vibe-palace.toml; rely on explicit project
	if err := os.WriteFile(filepath.Join(projDir, "commit.msg"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(map[string]string{"project": "explicit-proj", "project_path": projDir})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.(map[string]any)["project"] != "explicit-proj" {
		t.Errorf("project = %v, want explicit-proj", res.(map[string]any)["project"])
	}
}

func TestIngestCommitMsg_MissingFile(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)
	projDir := newProjectDir(t, "p")
	params, _ := json.Marshal(map[string]string{"project_path": projDir})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing commit.msg")
	}
}

func TestIngestCommitMsg_EmptyFile(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)
	projDir := newProjectDir(t, "p")
	if err := os.WriteFile(filepath.Join(projDir, "commit.msg"), []byte("   \n\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(map[string]string{"project_path": projDir})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for empty commit.msg")
	}
}

func TestIngestCommitMsg_MissingProjectPath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)
	params, _ := json.Marshal(map[string]string{})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing project_path")
	}
}
