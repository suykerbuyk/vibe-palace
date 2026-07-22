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
	vaultRoot := t.TempDir()
	vault := storage.NewVault(vaultRoot)
	tool := IngestCommitMsgTool(vault)

	projDir := t.TempDir() // no .vibe-palace.toml; rely on explicit project
	if err := os.WriteFile(filepath.Join(projDir, "commit.msg"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A markerless but already-registered project: the vault dir exists, which
	// is the "OR exists" half of the write-authorization gate. Without it the
	// tool refuses (see TestIngestCommitMsg_RefusesUnmanagedDir).
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Projects", "explicit-proj"), 0o755); err != nil {
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

// TestIngestCommitMsg_RefusesUnmanagedDir is the write-authorization regression
// guard: a dirty directory with no .vibe-palace.toml marker and no existing
// vault project must be refused, not silently scaffolded into a phantom
// Projects/<slug>/. This is the live incident the commit-write-tools task fixed.
func TestIngestCommitMsg_RefusesUnmanagedDir(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)

	projDir := newGitProjectRepoNoMarker(t, true) // git repo, dirty, NO marker
	writeCommitMsg(t, projDir)

	params, _ := json.Marshal(map[string]string{"project": "junk-project", "project_path": projDir})
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected refusal: an unmanaged dir must not scaffold a vault project")
	}
	if !strings.Contains(err.Error(), "run `vp init`") {
		t.Errorf("refusal message = %q, want it to point at vp init", err)
	}
	// Nothing must have been scaffolded in the vault.
	if _, serr := os.Stat(filepath.Join(vault.Root, "Projects", "junk-project")); !os.IsNotExist(serr) {
		t.Errorf("vault project scaffolded despite refusal (stat err = %v)", serr)
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

// newGitProjectRepo creates a project repo root that is a REAL git repo with a
// .vibe-palace.toml naming it and a committed .gitignore. The gitignore entry
// mirrors the real repo's own `/commit.msg` rule (widened to any depth so the
// subdirectory case below stays clean): it is what keeps `git status
// --porcelain` — which runs without --ignored — from seeing the very file the
// tool is about to ingest, so the clean-repo check cannot self-trip.
//
// The returned repo is CLEAN. Pass dirty=true to leave an untracked file behind.
func newGitProjectRepo(t *testing.T, name string, dirty bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.ConfigFileName),
		[]byte("[project]\nname = \""+name+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("commit.msg\n"), 0o644); err != nil {
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
	run("add", "-A")
	run("commit", "-m", "init")
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "dirt.txt"), []byte("uncommitted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// newGitProjectRepoNoMarker is newGitProjectRepo without the .vibe-palace.toml
// marker: a real git repo that is NOT an enrolled vibe-palace project. Used to
// exercise the write-authorization gate's refusal path.
func newGitProjectRepoNoMarker(t *testing.T, dirty bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("commit.msg\n"), 0o644); err != nil {
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
	run("add", "-A")
	run("commit", "-m", "init")
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "dirt.txt"), []byte("uncommitted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeCommitMsg drops a well-formed commit.msg at dir.
func writeCommitMsg(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "commit.msg"), []byte("feat: something\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestIngestCommitMsg_RefusesOnCleanRepo is the check: a repo with no
// uncommitted work has nothing for a commit message to describe.
func TestIngestCommitMsg_RefusesOnCleanRepo(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)

	projDir := newGitProjectRepo(t, "clean-proj", false)
	writeCommitMsg(t, projDir)

	params, _ := json.Marshal(map[string]string{"project_path": projDir})
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected refusal on a clean repo")
	}
	if !strings.Contains(err.Error(), "no uncommitted changes") {
		t.Errorf("refusal message = %q, want it to explain the empty diff", err)
	}
	// Nothing must have been written to the vault.
	dest, derr := vault.CommitMsgFile("clean-proj")
	if derr != nil {
		t.Fatal(derr)
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Errorf("vault commit.msg written despite refusal (stat err = %v)", serr)
	}
}

// TestIngestCommitMsg_PermitsOnDirtyRepo — untracked/unstaged dirt is exactly
// what the message describes.
func TestIngestCommitMsg_PermitsOnDirtyRepo(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)

	projDir := newGitProjectRepo(t, "dirty-proj", true)
	writeCommitMsg(t, projDir)

	params, _ := json.Marshal(map[string]string{"project_path": projDir})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("dirty repo must permit: %v", err)
	}
	if res.(map[string]any)["project"] != "dirty-proj" {
		t.Errorf("project = %v, want dirty-proj", res.(map[string]any)["project"])
	}
}

// TestIngestCommitMsg_PermitsOnNonRepo — a project that is not a git repo must
// not be blocked; ProjectHasUncommittedWrites cannot tell this case from a
// clean repo, which is why ProjectGitState exists.
func TestIngestCommitMsg_PermitsOnNonRepo(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)

	projDir := newProjectDir(t, "norepo-proj") // t.TempDir(), no git
	writeCommitMsg(t, projDir)

	params, _ := json.Marshal(map[string]string{"project_path": projDir})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("non-repo must permit: %v", err)
	}
}

// TestIngestCommitMsg_CleanRepoSubdir — a subdirectory of a clean repo must
// resolve to the repo ROOT and still refuse. A shallow probe of the raw
// project_path would see no .git and silently permit.
func TestIngestCommitMsg_CleanRepoSubdir(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)

	root := newGitProjectRepo(t, "subdir-proj", false)
	sub := filepath.Join(root, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommitMsg(t, sub)

	params, _ := json.Marshal(map[string]string{"project": "subdir-proj", "project_path": sub})
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected refusal: subdir must resolve to the clean repo root")
	}
	if !strings.Contains(err.Error(), "no uncommitted changes") {
		t.Errorf("refusal message = %q, want it to explain the empty diff", err)
	}
}

// TestIngestCommitMsg_DirtyRepoSubdir — the mirror image: resolving to a dirty
// root permits.
func TestIngestCommitMsg_DirtyRepoSubdir(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := IngestCommitMsgTool(vault)

	root := newGitProjectRepo(t, "subdir-dirty", true)
	sub := filepath.Join(root, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommitMsg(t, sub)

	params, _ := json.Marshal(map[string]string{"project": "subdir-dirty", "project_path": sub})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("dirty root via subdir must permit: %v", err)
	}
}
