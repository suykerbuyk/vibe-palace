// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAutoSummary_WithGitRepo(t *testing.T) {
	dir := t.TempDir()
	initGitRepoMulti(t, dir, []string{"first commit", "second commit", "third commit"})

	summary := AutoSummary(dir)
	if !strings.HasPrefix(summary, "Auto-captured session. Recent: ") {
		t.Fatalf("unexpected prefix: %q", summary)
	}
	if !strings.Contains(summary, "first commit") {
		t.Errorf("expected summary to contain 'first commit': %q", summary)
	}
	if !strings.Contains(summary, "third commit") {
		t.Errorf("expected summary to contain 'third commit': %q", summary)
	}
}

func TestAutoSummary_WithoutGitRepo(t *testing.T) {
	dir := t.TempDir()
	summary := AutoSummary(dir)
	if summary != "Auto-captured session" {
		t.Errorf("expected fallback, got %q", summary)
	}
}

func TestAutoSummary_EmptyRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	summary := AutoSummary(dir)
	if summary != "Auto-captured session" {
		t.Errorf("expected fallback for empty repo, got %q", summary)
	}
}

// initGitRepoMulti creates a git repo with multiple commits.
func initGitRepoMulti(t *testing.T, dir string, msgs []string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	for _, msg := range msgs {
		cmd := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", msg)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit %q: %v\n%s", msg, err, out)
		}
	}
}
