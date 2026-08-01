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

	// A clean git project dir so the dirty probes are deterministic.
	projDir := newGitProjectDir(t, "demo")

	params, _ := json.Marshal(map[string]any{"project": "demo", "project_path": projDir})
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

func TestPreflightWrapTool_RequiresProjectPath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := PreflightWrapTool(vault)

	params, _ := json.Marshal(map[string]any{"project": "demo"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing project_path")
	}
}

// TestPreflightWrapTool_IgnoresServerCwd is the regression pin for the defect
// that made this tool unsafe to build anything on: it derived the repo root
// from os.Getwd() of the long-lived `vp mcp` process — the host's LAUNCH
// directory, not the repo the agent is working in. Under that resolution the
// probe silently reported on whichever repo the server happened to start in.
//
// Both directions are asserted, because only the pair distinguishes "reads
// project_path" from "reads nothing at all".
func TestPreflightWrapTool_IgnoresServerCwd(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := PreflightWrapTool(vault)

	withMsg := newGitProjectDir(t, "with-msg")
	if err := os.WriteFile(filepath.Join(withMsg, "commit.msg"), []byte("stale subject\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// commit.msg must not itself dirty the tree, or the clean-tree branch never
	// runs. Ignore it exactly as a real project does.
	if err := os.WriteFile(filepath.Join(withMsg, ".gitignore"), []byte("/commit.msg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, withMsg)

	clean := newGitProjectDir(t, "clean")

	hasUnconsumed := func(t *testing.T, cwd, projectPath string) bool {
		t.Helper()
		t.Chdir(cwd)
		params, _ := json.Marshal(map[string]any{"project_path": projectPath})
		res, err := tool.Handler(context.Background(), params)
		if err != nil {
			t.Fatalf("handler: %v", err)
		}
		for _, w := range res.(wrapstate.PreflightResult).Warnings {
			if w.Check == "commit_msg_unconsumed" {
				return true
			}
		}
		return false
	}

	// cwd is the repo holding the stale message; project_path is the clean one.
	// The old os.Getwd() resolution would report the cwd's repo here.
	if hasUnconsumed(t, withMsg, clean) {
		t.Error("probe reported the SERVER CWD's commit.msg — it must key off project_path")
	}
	// Inverse: cwd clean, project_path holds the stale message.
	if !hasUnconsumed(t, clean, withMsg) {
		t.Error("probe missed project_path's unconsumed commit.msg")
	}
}

func gitCommitAll(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "ignore commit.msg"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
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
