// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAutoSummary_HonestPlaceholder(t *testing.T) {
	dir := t.TempDir()
	// Repo full of friction-keyword commit subjects — must NOT appear in the summary.
	initGitRepoMulti(t, dir, []string{
		"fix revert wrong undo never mind",
		"failed error exception fatal",
		"try again start over",
	})

	summary := AutoSummary(dir)
	if summary != autoSummaryHonest {
		t.Fatalf("summary = %q, want %q", summary, autoSummaryHonest)
	}
	for _, banned := range []string{"Recent:", "fix", "revert", "wrong", "failed", "error"} {
		if strings.Contains(summary, banned) {
			t.Errorf("honest summary must not contain %q: %q", banned, summary)
		}
	}
}

func TestAutoSummary_IgnoresMissingGit(t *testing.T) {
	dir := t.TempDir()
	if got := AutoSummary(dir); got != autoSummaryHonest {
		t.Errorf("no-git summary = %q, want %q", got, autoSummaryHonest)
	}
}

func TestAutoSummary_IgnoresEmptyRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if got := AutoSummary(dir); got != autoSummaryHonest {
		t.Errorf("empty-repo summary = %q, want %q", got, autoSummaryHonest)
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
