// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestRepoFreshness_MissingProjectPathRefused(t *testing.T) {
	tool := RepoFreshnessTool()
	params, _ := json.Marshal(map[string]any{})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected an error when project_path is omitted")
	}
}

func TestRepoFreshness_NonexistentPathRefused(t *testing.T) {
	tool := RepoFreshnessTool()
	params, _ := json.Marshal(map[string]any{"project_path": filepath.Join(t.TempDir(), "does-not-exist")})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected an error for a nonexistent project_path")
	}
}

func TestRepoFreshness_NonRepoDirRefused(t *testing.T) {
	tool := RepoFreshnessTool()
	params, _ := json.Marshal(map[string]any{"project_path": t.TempDir()})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected an error for a directory that is not a git repository")
	}
}

// TestRepoFreshness_HappyPathUpToDate drives the handler end to end against a
// real repo pushed to a real bare remote — the tool's own thin wrapper around
// storage.CheckRepoFreshness, proven live rather than only at the storage
// layer.
func TestRepoFreshness_HappyPathUpToDate(t *testing.T) {
	sandboxHostEnv(t)
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, dir, "remote", "add", "origin", bare)
	gitT(t, dir, "push", "origin", "main")

	tool := RepoFreshnessTool()
	params, _ := json.Marshal(map[string]any{"project_path": dir})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rf, ok := res.(storage.RepoFreshness)
	if !ok {
		t.Fatalf("result is %T, want storage.RepoFreshness", res)
	}
	if rf.Status != storage.RepoUpToDate {
		t.Errorf("Status = %q, want %q", rf.Status, storage.RepoUpToDate)
	}
	if len(rf.Remotes) != 1 || rf.Remotes[0].Remote != "origin" {
		t.Errorf("Remotes = %#v, want one origin entry", rf.Remotes)
	}
}

// TestRepoFreshness_BehindReportsSubjects confirms the tool surfaces the
// newest upstream subject when the caller ends up behind, and that it never
// reports up_to_date for a stale checkout.
func TestRepoFreshness_BehindReportsSubjects(t *testing.T) {
	sandboxHostEnv(t)
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, dir, "remote", "add", "origin", bare)
	gitT(t, dir, "push", "origin", "main")

	// Advance the remote via a second clone, exactly as the storage-layer
	// tests do, so dir is genuinely behind without being touched directly.
	other := t.TempDir()
	gitT(t, other, "clone", "-b", "main", bare, ".")
	gitT(t, other, "config", "user.email", "other@example.com")
	gitT(t, other, "config", "user.name", "Other")
	if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("advanced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, other, "add", "-A")
	gitT(t, other, "commit", "-m", "advance remote.txt")
	gitT(t, other, "push", "origin", "main")

	tool := RepoFreshnessTool()
	params, _ := json.Marshal(map[string]any{"project_path": dir})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rf := res.(storage.RepoFreshness)
	if rf.Status != storage.RepoBehind {
		t.Fatalf("Status = %q, want %q", rf.Status, storage.RepoBehind)
	}
	if len(rf.Remotes) != 1 || len(rf.Remotes[0].NewestUpstreamSubjects) != 1 ||
		rf.Remotes[0].NewestUpstreamSubjects[0] != "advance remote.txt" {
		t.Errorf("Remotes = %#v, want one newest_upstream_subjects entry \"advance remote.txt\"", rf.Remotes)
	}
}

// TestRepoFreshness_NoRemoteIsUnverified pins the task's central requirement
// at the tool boundary: a repo nobody can check against is "unverified",
// never a fabricated "up_to_date".
func TestRepoFreshness_NoRemoteIsUnverified(t *testing.T) {
	sandboxHostEnv(t)
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initVaultRepo(t)

	tool := RepoFreshnessTool()
	params, _ := json.Marshal(map[string]any{"project_path": dir})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rf := res.(storage.RepoFreshness)
	if rf.Status != storage.RepoUnverified {
		t.Errorf("Status = %q, want %q", rf.Status, storage.RepoUnverified)
	}
}
