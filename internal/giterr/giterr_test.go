// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package giterr

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetailLinePrefersGitsOwnFatalLine(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"fatal wins over earlier prose", "Cloning into 'x'...\nfatal: repository not found\n", "fatal: repository not found"},
		{"error: also counts", "hint: something\nerror: pathspec did not match\n", "error: pathspec did not match"},
		{"falls back to first non-empty", "\n\n  just a message\n", "just a message"},
		{"only whitespace", "\n  \n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetailLine(tc.in); got != tc.want {
				t.Errorf("DetailLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWrapCarriesGitsOwnMessageFromCapturedStderr is the leaf-package half of
// the Unit A gate: the diagnosis is already in (*exec.ExitError).Stderr at
// every cmd.Output() call site, and Wrap is what stops it being discarded.
//
// The fixture is a real git failure on a real repo, not a forged ExitError:
// only a genuine subprocess proves os/exec populates Stderr the way this
// depends on.
func TestWrapCarriesGitsOwnMessageFromCapturedStderr(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A .git that exists but is not a repository: git fails with its own
	// sentence, exit 128.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("fixture is degenerate: git succeeded against a non-repository")
	}

	wrapped := Wrap(err)
	msg := wrapped.Error()
	if !strings.Contains(msg, "fatal:") {
		t.Errorf("wrapped error must carry git's own sentence, got %q", msg)
	}
	if strings.TrimSpace(msg) == "exit status 128" {
		t.Errorf("wrapped error collapsed back to the bare exit code: %q", msg)
	}
	if n := strings.Count(msg, "\n"); n > 0 {
		t.Errorf("error should carry ONE line of git text, got %d newlines: %q", n, msg)
	}

	// The compatibility half: errors.As still reaches the exec error, so a
	// caller switching on exit status keeps working.
	var ee *exec.ExitError
	if !errors.As(wrapped, &ee) {
		t.Errorf("wrapped error no longer unwraps to *exec.ExitError: %T", wrapped)
	}
	var ge *GitError
	if !errors.As(wrapped, &ge) {
		t.Fatalf("wrapped error is not a *GitError: %T", wrapped)
	}
}

// TestWrapLeavesErrorsWithNothingToAttachAlone pins the negative half. A
// context cancellation or a missing git binary carries no captured stderr;
// inventing a detail there would be worse than the bare error, so Wrap must
// return err unchanged rather than produce an empty-detail GitError.
func TestWrapLeavesErrorsWithNothingToAttachAlone(t *testing.T) {
	if got := Wrap(nil); got != nil {
		t.Errorf("Wrap(nil) = %v, want nil", got)
	}

	plain := errors.New("context deadline exceeded")
	if got := Wrap(plain); got != plain {
		t.Errorf("Wrap() must return a non-exec error unchanged, got %v (%T)", got, got)
	}

	// An ExitError with no captured stderr: nothing to attach.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = t.TempDir()
	cmd.Stderr = os.NewFile(0, os.DevNull) // suppresses capture, as a caller wiring its own stderr would
	err := cmd.Run()
	if err == nil {
		t.Skip("git unexpectedly succeeded; nothing to assert")
	}
	if got := Wrap(err); got != err {
		t.Errorf("Wrap() must return an ExitError with no captured stderr unchanged, got %v", got)
	}
}
