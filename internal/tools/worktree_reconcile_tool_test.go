// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/worktree"
)

func TestWorktreeReconcileTool_NotMutating(t *testing.T) {
	if got := WorktreeReconcileTool().Mutating; got {
		t.Fatalf("vp_worktree_reconcile must be read-only (Mutating=false), got %v", got)
	}
}

func TestWorktreeReconcileTool_RequiresProjectPath(t *testing.T) {
	tool := WorktreeReconcileTool()
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("handler must reject a missing project_path")
	}
}

// TestWorktreeReconcileTool_Reports drives the handler against a temp repo with
// one on-convention worktree and asserts a well-formed report. CheckRemote is
// off (no remote in the fixture) so the call touches no network.
func TestWorktreeReconcileTool_Reports(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.email=test@example.com", "-c", "user.name=test"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(root, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(root, "add", "-A")
	git(root, "commit", "-m", "seed")
	if _, err := worktree.New(root).Create("solo", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tool := WorktreeReconcileTool()
	params, perr := json.Marshal(map[string]any{"project_path": root, "check_remote": false})
	if perr != nil {
		t.Fatal(perr)
	}
	res, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rep, ok := res.(worktree.ReconcileReport)
	if !ok {
		t.Fatalf("result type = %T, want worktree.ReconcileReport", res)
	}
	if rep.MainOnRemote != "unverified" {
		t.Errorf("MainOnRemote = %q, want unverified (check_remote=false)", rep.MainOnRemote)
	}
	var solo *worktree.ReconcileEntry
	for i := range rep.Entries {
		if rep.Entries[i].Name == "solo" {
			solo = &rep.Entries[i]
		}
	}
	if solo == nil {
		t.Fatalf("report missing the 'solo' worktree: %+v", rep.Entries)
	}
	// check_remote=false ⇒ MainOnRemote unverified ⇒ never reclaimable.
	if solo.Verdict == "reclaimable" {
		t.Errorf("solo must not be reclaimable with check_remote=false: %+v", solo)
	}
}
