// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// --- Project-root .gitignore reconciler ---

// TestReconcileProjectGitignore_FreshFile: a missing project .gitignore
// gains the full canonical set, with exactly one trailing newline.
func TestReconcileProjectGitignore_FreshFile(t *testing.T) {
	dir := t.TempDir()
	if err := ReconcileProjectGitignore(dir); err != nil {
		t.Fatalf("ReconcileProjectGitignore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	s := string(data)
	for _, p := range CanonicalProjectGitignorePatterns {
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
	// vp-owned artifacts only — must NOT manage build/coverage/binary lines.
	for _, forbidden := range []string{"/vp\n", "/build/", "/coverage."} {
		if strings.Contains(s, forbidden) {
			t.Errorf("reconciler must not inject project-owned entry %q:\n%s", forbidden, s)
		}
	}
}

// TestReconcileProjectGitignore_PartialPresence: only the missing
// canonical lines are appended; the pre-existing canonical line and the
// user's own lines are preserved in their original order.
func TestReconcileProjectGitignore_PartialPresence(t *testing.T) {
	dir := t.TempDir()
	seed := "# user header\nnode_modules/\n/.claude/\nbuild/\n"
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileProjectGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	// User content preserved verbatim at the head, in original order.
	if !strings.HasPrefix(s, seed) {
		t.Errorf("user content not preserved verbatim at head:\n%s", s)
	}
	// Every canonical present exactly once (the seeded /.claude/ not doubled).
	for _, p := range CanonicalProjectGitignorePatterns {
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

// TestReconcileProjectGitignore_AllPresentNoOp: when every canonical line
// is already present, the file must NOT be touched at all — content and
// mtime unchanged (idempotent no-op so a stable project never churns).
func TestReconcileProjectGitignore_AllPresentNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	// Seed with all canonical lines plus user content, deliberately
	// WITHOUT trailing-newline normalization (no final newline) so a
	// rewrite would be detectable by the changed bytes.
	seed := "# mine\nfoo/\n" + strings.Join(CanonicalProjectGitignorePatterns, "\n")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate mtime so a rewrite would visibly bump it.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReconcileProjectGitignore(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("no-op run changed mtime: before=%v after=%v", before.ModTime(), after.ModTime())
	}
	data, _ := os.ReadFile(path)
	if string(data) != seed {
		t.Errorf("no-op run altered content:\nwant:\n%q\ngot:\n%q", seed, string(data))
	}
}

// TestReconcileProjectGitignore_Idempotent: a second run after the first
// produces byte-identical content and does not touch the file again.
func TestReconcileProjectGitignore_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := ReconcileProjectGitignore(dir); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Backdate so a needless rewrite would be observable via mtime.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(path)
	if err := ReconcileProjectGitignore(dir); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("not byte-identical on re-run:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	st2, _ := os.Stat(path)
	if !st2.ModTime().Equal(st1.ModTime()) {
		t.Error("second reconcile rewrote a complete file (mtime changed)")
	}
}

// TestReconcileProjectGitignore_PreservesLeadingTrailingContent: user
// content before and after the appended block survives verbatim.
func TestReconcileProjectGitignore_PreservesLeadingTrailingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	// Some canonical present (/.claude/) interleaved with user lines, and
	// a trailing user line after it.
	user := "# top\n*.log\n\n/.claude/\n\n# bottom\ndist/\n"
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileProjectGitignore(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.HasPrefix(s, user) {
		t.Errorf("user content not preserved verbatim at head:\n%s", s)
	}
	if strings.Contains(s, "\n\n\n") {
		t.Errorf("introduced consecutive blank lines:\n%s", s)
	}
	if !strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\n\n") {
		t.Errorf("expected exactly one trailing newline:\n%q", s)
	}
}

// TestReconcileProjectGitignore_AtomicWrite: no leftover .tmp sidecars
// remain after a successful reconcile (temp-file + rename leaves the dir
// clean).
func TestReconcileProjectGitignore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	if err := ReconcileProjectGitignore(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after reconcile: %s", e.Name())
		}
	}
	// The final file must exist with 0o644.
	info, err := os.Stat(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("expected 0o644, got %o", perm)
	}
}

// TestMissingProjectGitignorePatterns covers the advisory detection used
// by `vp check`: a missing file reports the full set; a complete file
// reports none; a partial file reports exactly the absent lines in
// declaration order.
func TestMissingProjectGitignorePatterns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")

	// Missing file → full canonical set.
	missing, err := MissingProjectGitignorePatterns(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != len(CanonicalProjectGitignorePatterns) {
		t.Errorf("missing-file: want %d, got %d", len(CanonicalProjectGitignorePatterns), len(missing))
	}

	// Complete file → none missing.
	if err := ReconcileProjectGitignore(dir); err != nil {
		t.Fatal(err)
	}
	missing, err = MissingProjectGitignorePatterns(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("complete-file: want 0 missing, got %v", missing)
	}

	// Partial file → only the absent lines, in declaration order.
	seed := "/.claude/\n/.grok/\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	missing, err = MissingProjectGitignorePatterns(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/CLAUDE.md", "/commit.msg", "/.vibe-palace/"}
	if strings.Join(missing, "|") != strings.Join(want, "|") {
		t.Errorf("partial-file: want %v, got %v", want, missing)
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
