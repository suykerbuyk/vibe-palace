// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/memory"
	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// renamedAwayVault is a git vault whose Projects/old/ existed and was renamed
// to Projects/new/: the state a project-slug migration leaves, and the one a
// stale checkout still names.
func renamedAwayVault(t *testing.T) *storage.Vault {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	if err := os.MkdirAll(filepath.Join(root, "Projects", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Projects", "old", "resume.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "seed")
	run("mv", "Projects/old", "Projects/new")
	run("commit", "-q", "-m", "migrate: old -> new (1/2 rename)")
	return storage.NewVault(root)
}

// The two MCP writers an agent in a stale checkout runs at wrap time. Neither
// passes through RequireKnownProject, and both lazily create Projects/<slug>/.
func TestRemovedSlugIsRefusedByTheCaptureAndHarvestTools(t *testing.T) {
	// A real native memory for the stale checkout, so that without the gate the
	// harvest has something to route into Projects/old/memory/.
	t.Setenv("CLAUDE_HOME", t.TempDir())
	staleCwd := t.TempDir()
	nativeDir, err := memory.NativeDirFromCwd(staleCwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		params map[string]any
		tool   func(*storage.Vault) func(context.Context, json.RawMessage) (any, error)
	}{
		{"vp_capture_session", map[string]any{"project": "old", "summary": "a session in a stale checkout"},
			func(v *storage.Vault) func(context.Context, json.RawMessage) (any, error) {
				return CaptureSessionTool(v, nil).Handler
			}},
		{"vp_memory_harvest", map[string]any{"project": "old", "cwd": staleCwd},
			func(v *storage.Vault) func(context.Context, json.RawMessage) (any, error) {
				return MemoryHarvestTool(v).Handler
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := renamedAwayVault(t)
			p, _ := json.Marshal(tc.params)
			_, err := tc.tool(vault)(context.Background(), p)
			if err == nil {
				t.Fatal("a removed slug must be refused")
			}
			if !strings.Contains(err.Error(), "Projects/old/ was removed") {
				t.Errorf("refusal must say the project was removed, got %q", err)
			}
			if _, statErr := os.Stat(filepath.Join(vault.Root, "Projects", "old")); statErr == nil {
				t.Error("Projects/old/ was resurrected")
			}
			if ents, _ := os.ReadDir(nativeDir); len(ents) == 0 {
				t.Error("the native memory must not be harvested or deleted")
			}
		})
	}
}
