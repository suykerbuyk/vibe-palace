// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitAvailable(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
}

func TestGitIsRepo(t *testing.T) {
	dir := t.TempDir()
	if GitIsRepo(dir) {
		t.Error("fresh dir should not be a git repo")
	}

	if err := GitInit(dir); err != nil {
		t.Fatalf("GitInit: %v", err)
	}
	if !GitIsRepo(dir) {
		t.Error("dir should be a git repo after init")
	}
}

func TestGitInit(t *testing.T) {
	dir := t.TempDir()
	if err := GitInit(dir); err != nil {
		t.Fatalf("GitInit: %v", err)
	}

	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		t.Fatalf(".git not created: %v", err)
	}
	if !info.IsDir() {
		t.Error(".git is not a directory")
	}
}

func TestReconcileVaultGitignore_FreshFile(t *testing.T) {
	dir := t.TempDir()
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatalf("ReconcileVaultGitignore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	s := string(data)
	for _, p := range CanonicalGitignorePatterns {
		if !strings.Contains(s, p) {
			t.Errorf("missing canonical pattern %q in:\n%s", p, s)
		}
	}
	if !strings.HasSuffix(s, "\n") {
		t.Error(".gitignore should end with exactly one newline")
	}
	if strings.HasSuffix(s, "\n\n") {
		t.Error(".gitignore should not end with multiple trailing newlines")
	}
	// The vaultlock sidecar dir is host-local and must never be synced.
	if !strings.Contains(s, "\n.vp-locks/\n") && !strings.HasSuffix(s, ".vp-locks/\n") {
		t.Errorf(".gitignore missing .vp-locks/ entry:\n%s", s)
	}
}

func TestReconcileVaultGitignore_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("not byte-identical on re-run:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestReconcileVaultGitignore_PreservesUserContent(t *testing.T) {
	dir := t.TempDir()
	userContent := "# my own comment\nnode_modules/\n\n# another header\nbuild/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	// User content must appear first, exactly as written.
	if !strings.HasPrefix(s, userContent) {
		t.Errorf("user content not preserved verbatim at head:\n%s", s)
	}
	// Canonical patterns appear.
	for _, p := range CanonicalGitignorePatterns {
		if !strings.Contains(s, p) {
			t.Errorf("missing canonical %q", p)
		}
	}
}

func TestReconcileVaultGitignore_PartialPresence(t *testing.T) {
	dir := t.TempDir()
	// Pre-seed with only two of the canonical lines.
	seed := "palace/.local/\n*.bak\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	s := string(data)
	// Each canonical should appear exactly once.
	for _, p := range CanonicalGitignorePatterns {
		if strings.Count(s, p+"\n") != 1 && !strings.HasSuffix(s, p) {
			// Count occurrences either as full lines or the final line.
			n := 0
			for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
				if line == p {
					n++
				}
			}
			if n != 1 {
				t.Errorf("canonical %q appears %d times, want 1:\n%s", p, n, s)
			}
		}
	}
}

// TestReconcileVaultGitignore_IdempotentRepeat calls reconcile N times
// and asserts each run produces byte-identical output. This is the
// load-bearing invariant for `vp config sync` on a stable vault.
func TestReconcileVaultGitignore_IdempotentRepeat(t *testing.T) {
	dir := t.TempDir()
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".gitignore")
	reference, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := ReconcileVaultGitignore(dir); err != nil {
			t.Fatalf("iter %d: %v", i+1, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read iter %d: %v", i+1, err)
		}
		if string(got) != string(reference) {
			t.Fatalf("iter %d diverged:\nreference:\n%s\ngot:\n%s", i+1, reference, got)
		}
	}
}

// TestReconcileVaultGitignore_InterleavedUserContent verifies that user
// comments and blank lines are preserved verbatim when a *subset* of the
// canonical patterns is already present, interleaved with user content.
// Missing canonical patterns append at EOF.
func TestReconcileVaultGitignore_InterleavedUserContent(t *testing.T) {
	dir := t.TempDir()
	user := "# my own block\n" +
		"*.log\n" +
		"\n" +
		"# vibe stuff (partial)\n" +
		"*.bak\n" +
		"\n" +
		"# another section\n" +
		"dist/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.HasPrefix(s, user) {
		t.Errorf("user content not preserved verbatim at head:\n%s", s)
	}
	// Every canonical pattern present exactly once.
	for _, p := range CanonicalGitignorePatterns {
		n := 0
		for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
			if line == p {
				n++
			}
		}
		if n != 1 {
			t.Errorf("canonical %q appears %d times, want 1:\n%s", p, n, s)
		}
	}
}

// TestReconcileVaultGitignore_NoDoubleBlanks confirms no duplicate
// consecutive blank lines are introduced and the file ends with exactly
// one trailing newline. A user who lays out their gitignore with single
// blank-line separators should keep it that way after reconcile.
func TestReconcileVaultGitignore_NoDoubleBlanks(t *testing.T) {
	dir := t.TempDir()
	// User file ends with a single newline; no trailing blank line.
	user := "# top\nfoo/\n\n# middle\nbar/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	s := string(data)
	if strings.Contains(s, "\n\n\n") {
		t.Errorf("introduced consecutive blank lines:\n%s", s)
	}
	if !strings.HasSuffix(s, "\n") {
		t.Error("should end with a newline")
	}
	if strings.HasSuffix(s, "\n\n") {
		t.Error("should not end with trailing blank line")
	}
}

// TestReconcileVaultGitignore_CaseSensitive pins the current matching
// semantics: canonical patterns are case-sensitive. A pre-existing
// "*.BAK" line does NOT suppress the append of canonical "*.bak".
// Git itself treats pattern matches case-sensitively on most
// filesystems, so this mirrors git's own behavior.
func TestReconcileVaultGitignore_CaseSensitive(t *testing.T) {
	dir := t.TempDir()
	// Pre-seed with upper-case variants of two canonicals.
	seed := "*.BAK\n*.NEW\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	s := string(data)
	// Upper-case seeds survive.
	if !strings.Contains(s, "*.BAK") || !strings.Contains(s, "*.NEW") {
		t.Errorf("upper-case user patterns dropped:\n%s", s)
	}
	// Canonical lower-case forms were appended.
	if !strings.Contains(s, "\n*.bak\n") && !strings.HasSuffix(s, "*.bak\n") {
		t.Errorf("canonical *.bak not appended (case-sensitive match expected):\n%s", s)
	}
	if !strings.Contains(s, "\n*.new\n") && !strings.HasSuffix(s, "*.new\n") {
		t.Errorf("canonical *.new not appended (case-sensitive match expected):\n%s", s)
	}
}

func TestReconcileVaultGitignore_AddsTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	// No trailing newline in seeded content.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("foo/"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("should end with newline")
	}
	if strings.HasSuffix(string(data), "\n\n") {
		t.Error("should not double the trailing newline")
	}
	// foo/ preserved once.
	n := 0
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "foo/" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected foo/ once, got %d:\n%s", n, data)
	}
}
