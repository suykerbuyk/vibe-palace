// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// throwawayConflictedRepo builds a repo whose index carries a real unmerged
// entry — the iteration-277 condition — in a t.TempDir(), never the live vault.
//
// The conflict is produced by an actual conflicting merge rather than a forged
// index entry: `git update-index --index-info` with a null sha is rejected
// outright ("cache entry has null sha1"), so the forged version would have
// tested a different failure than the one the field report carried.
//
// Identity is set LOCALLY and the host's global/system config is neutralized,
// so the repo cannot inherit an operator's settings — and, more importantly, so
// a missing user.name cannot make git fail for an unrelated reason that would
// still satisfy a loose "contains git text" assertion.
func throwawayConflictedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) (string, error) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
		out, err := c.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) {
		t.Helper()
		if out, err := git(args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	must("init", "-b", "main")
	must("config", "user.email", "throwaway@example.invalid")
	must("config", "user.name", "Throwaway")
	write("f.txt", "base\n")
	must("add", "f.txt")
	must("commit", "-m", "base")
	must("checkout", "-b", "side")
	write("f.txt", "side\n")
	must("commit", "-am", "side")
	must("checkout", "main")
	write("f.txt", "main\n")
	must("commit", "-am", "main")
	if out, err := git("merge", "side"); err == nil {
		t.Fatalf("fixture is degenerate: the merge succeeded, so the index is not unmerged:\n%s", out)
	}
	return dir
}

// TestCommitPathSurfacesGitsOwnDiagnosis is the acceptance gate for
// git-call-sites-drop-captured-output-leaving-bare-exit-128.
//
// At iteration 277 a vp_vault_tidy failure reached an agent on another host as
// its ENTIRE content:
//
//	commit bodies since anchor: exit status 128
//
// Two different failures wearing that exit code were conflated for most of an
// investigation, because neither carried a message that told them apart. git's
// explanation was captured by gitCmd's CombinedOutput and thrown away one line
// later.
//
// This drives the real commit path (CommitAndPushPaths, what vp_vault_tidy
// calls) against a throwaway conflicted repo and asserts the returned error
// carries git's OWN sentence. Mutation: restore `return trimmed, err` in gitCmd
// and this fails — an assertion on the exit code alone would not, which is
// precisely the defect.
func TestCommitPathSurfacesGitsOwnDiagnosis(t *testing.T) {
	dir := throwawayConflictedRepo(t)

	// Stage a file that is NOT the conflicted one: `git add f.txt` would mark
	// the conflict resolved and the commit would succeed, testing nothing.
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := CommitAndPushPaths(dir, "tidy: should fail on an unmerged index", []string{"other.txt"}, false)
	if err == nil {
		t.Fatal("expected the commit to fail while the index is unmerged")
	}
	msg := err.Error()

	// git's own diagnosis, not merely "some text".
	if !strings.Contains(msg, "unmerged") {
		t.Errorf("error must carry git's own explanation (expected it to mention \"unmerged\"); got %q", msg)
	}
	// The defect itself: an error that is ONLY an exit code.
	if !strings.Contains(msg, "exit status") {
		t.Logf("note: error carries no exit status at all: %q", msg)
	}
	if strings.HasSuffix(strings.TrimSpace(msg), "exit status 128") {
		t.Errorf("error ends at the exit code — git's captured output was dropped again: %q", msg)
	}
	// One line of git text, not a dump: these strings reach an agent's context.
	if n := strings.Count(msg, "\n"); n > 0 {
		t.Errorf("error should carry ONE line of git text, got %d newlines: %q", n, msg)
	}
}

// TestGitErrorUnwrapsToTheExecError pins the compatibility half: attaching the
// detail must not break callers that inspect the underlying exec error, and the
// bare cause must stay recoverable for a renderer that already printed git's
// raw output separately.
func TestGitErrorUnwrapsToTheExecError(t *testing.T) {
	dir := throwawayConflictedRepo(t)

	_, err := gitCmd(dir, 10*time.Second, "commit", "-m", "should fail")
	if err == nil {
		t.Fatal("expected commit to fail")
	}
	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("gitCmd error is not a *GitError: %T", err)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Errorf("errors.As must still reach *exec.ExitError through the wrap; got %T", err)
	}
	if ge.Detail == "" {
		t.Error("Detail is empty — git's captured output was not attached")
	}
	if strings.Contains(ge.Unwrap().Error(), "unmerged") {
		t.Error("Unwrap must yield the BARE exec error, so a renderer that already printed the output can avoid repeating it")
	}
}

// TestGitDetailLinePrefersGitsOwnMarker pins the selection rule. git's real
// unmerged-commit output leads with the specific `error:` line and ends with a
// generic `fatal: Exiting because of an unresolved conflict.`; the specific one
// is the useful one, so the first marker line wins rather than the last.
func TestGitDetailLinePrefersGitsOwnMarker(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"prefers first marker over trailing generic fatal",
			"error: Committing is not possible because you have unmerged files.\nhint: Fix them up\nfatal: Exiting because of an unresolved conflict.",
			"error: Committing is not possible because you have unmerged files."},
		{"falls back to the first non-empty line",
			"\n\nsomething unlabelled went wrong\nmore",
			"something unlabelled went wrong"},
		{"empty output stays empty so the error renders exactly as before",
			"", ""},
		{"skips leading noise to reach the marker",
			"Everything up-to-date\nfatal: the real cause",
			"fatal: the real cause"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitDetailLine(tc.in); got != tc.want {
				t.Errorf("gitDetailLine() = %q, want %q", got, tc.want)
			}
		})
	}
}
