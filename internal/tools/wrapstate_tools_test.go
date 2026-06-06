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

	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// newGitProjectDir creates a project repo root that is a real git repo (so the
// stat-guarded probes proceed) and carries a .vibe-palace.toml naming it.
func newGitProjectDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.ConfigFileName),
		[]byte("[project]\nname = \""+name+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	// An initial commit so HEAD exists (rev-list / branch probes need it).
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

func TestStampIterTool_RoundTrip(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := StampIterTool(vault)

	projDir := t.TempDir() // explicit project, no detection needed

	// Seed a vault task so the snapshot has content.
	tasksDir, err := vault.TasksDir("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "t1.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]any{
		"project":      "demo",
		"project_path": projDir,
		"iter":         12,
	})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	sr := res.(wrapstate.StampResult)
	if sr.Iter != 12 {
		t.Errorf("iter = %d, want 12", sr.Iter)
	}

	data, err := os.ReadFile(filepath.Join(projDir, wrapstate.AnchorDir, wrapstate.AnchorFile))
	if err != nil {
		t.Fatalf("read last-iter: %v", err)
	}
	if string(data) != "12\n" {
		t.Errorf("last-iter = %q, want 12", string(data))
	}

	snap, err := wrapstate.ReadSnapshot(projDir)
	if err != nil {
		t.Fatal(err)
	}
	if snap.IterN != 12 || len(snap.Active) != 1 || snap.Active[0] != "t1" {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestStampIterTool_MissingProjectPath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := StampIterTool(vault)
	params, _ := json.Marshal(map[string]any{"project": "demo", "iter": 1})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing project_path")
	}
}

func TestPreflightWrapTool_CleanVault(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := PreflightWrapTool(vault)

	// Run from a clean git project dir so the dirty probes are deterministic.
	projDir := newGitProjectDir(t, "demo")
	t.Chdir(projDir)

	params, _ := json.Marshal(map[string]any{"project": "demo"})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	pr := res.(wrapstate.PreflightResult)
	if !pr.OK {
		t.Errorf("clean vault should be ok, got %+v", pr)
	}
	if len(pr.Errors) != 0 {
		t.Errorf("expected no errors, got %+v", pr.Errors)
	}
}

func TestCollectWrapStateTool_Smoke(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := CollectWrapStateTool(vault)

	projDir := newGitProjectDir(t, "demo")
	t.Chdir(projDir)

	// Seed iterations.md in the vault.
	iterPath, err := vault.IterationsFile("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(iterPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iterPath, []byte("### Iteration 3 — prior\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]any{"project": "demo"})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	r := res.(wrapstate.Result)
	if r.IterN != 4 {
		t.Errorf("IterN = %d, want 4", r.IterN)
	}
	// Empty repo (no commits) ⇒ bookkeeping or fresh depending on root walk;
	// just assert the shape is one of the known values.
	switch r.Shape {
	case wrapstate.ShapeFreshFeature, wrapstate.ShapePlanning, wrapstate.ShapeBookkeeping:
	default:
		t.Errorf("unexpected shape %q", r.Shape)
	}
}
