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

// TestListProjectsUnionOfBothTrees pins the fix: the tool used to read palace/
// only, so a project whose history was captured but never drawer-indexed was
// invisible to every agent that asked what was in the vault. In the live vault
// that was 5 projects and 73 session notes.
func TestListProjectsUnionOfBothTrees(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	mk := func(parts ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(append([]string{vault.Root}, parts...)...), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	mk("palace", "both")
	mk("Projects", "both")
	mk("palace", "palace-only")
	mk("Projects", "projects-only", "sessions") // the previously-invisible class

	result, err := ListProjectsTool(vault).Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]any)

	projects := m["projects"].([]string)
	want := []string{"both", "palace-only", "projects-only"}
	if len(projects) != len(want) {
		t.Fatalf("projects = %v, want %v", projects, want)
	}
	for i, w := range want {
		if projects[i] != w {
			t.Fatalf("projects = %v, want %v (sorted)", projects, want)
		}
	}

	drift, ok := m["drift"].([]projectDrift)
	if !ok {
		t.Fatal("drift missing: two projects exist in only one tree and the tool did not say so")
	}
	if len(drift) != 2 {
		t.Fatalf("drift = %+v, want the 2 single-tree projects", drift)
	}
	for _, d := range drift {
		switch d.Slug {
		case "palace-only":
			if !d.InPalace || d.InProjects {
				t.Errorf("palace-only drift = %+v", d)
			}
		case "projects-only":
			if d.InPalace || !d.InProjects {
				t.Errorf("projects-only drift = %+v", d)
			}
		default:
			t.Errorf("unexpected drift entry %+v — 'both' is complete and must not be reported", d)
		}
	}
}

// TestListProjectsDriftSilentWhenTreesAgree: drift is ABSENT, not empty, when
// there is none. A signal that is always present is one agents learn to skim —
// the same reasoning that killed the `partial` capture tier and made vp_health
// silent when healthy.
func TestListProjectsDriftSilentWhenTreesAgree(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	for _, tree := range []string{"palace", "Projects"} {
		if err := os.MkdirAll(filepath.Join(vault.Root, tree, "tidy"), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	result, err := ListProjectsTool(vault).Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]any)

	if _, present := m["drift"]; present {
		t.Fatalf("drift must be absent when the trees agree, got %+v", m["drift"])
	}
	if projects := m["projects"].([]string); len(projects) != 1 || projects[0] != "tidy" {
		t.Fatalf("projects = %v, want [tidy]", projects)
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
