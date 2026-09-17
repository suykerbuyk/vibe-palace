// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package giterr pairs a failed git subprocess's exit status with git's own
// explanation of the failure, so an error that reaches a human or an agent says
// what git said rather than only "exit status 128".
//
// This is a dependency-free leaf package, and it is one for the same reason
// internal/gitenv is: the logic originated in internal/storage (as the fix for
// git-call-sites-drop-captured-output-leaving-bare-exit-128), but
// internal/storage imports internal/project and internal/wrapstate — so either
// of those importing storage back for this one helper would be a hard import
// cycle. internal/storage.GitError now aliases the type here rather than
// declaring its own, exactly as internal/storage.SafeGitEnv forwards to
// internal/gitenv.
//
// # Why the move was necessary, not merely tidy
//
// The field report that motivated the original wrap (iteration 277) carried
// this as its entire content:
//
//	commit bodies since anchor: exit status 128
//
// That message is produced by internal/tools wrapping
// wrapstate.CommitBodiesSinceAnchor — which runs through internal/wrapstate's
// OWN git runner, not internal/storage's gitCmd. The wrap was added to gitCmd
// and its acceptance test drives a storage path, so the fix went green while
// the call site the report actually came from kept emitting the bare exit
// code. A second copy of the helper in wrapstate would have left the same
// split available to the next runner; one leaf package both call does not.
package giterr

import (
	"errors"
	"os/exec"
	"strings"
)

// GitError pairs git's own message with the exit status exec reports.
//
// It is a POINTER type with Unwrap, so errors.As reaches it through any number
// of fmt.Errorf("%w") wraps and errors.Is still matches the underlying
// *exec.ExitError — callers that switch on exit status keep working.
type GitError struct {
	// Detail is ONE line of git's output (see DetailLine). One line on
	// purpose: these strings reach an agent's context window, and a full
	// multi-line rebase dump would be a regression in the other direction.
	Detail string
	Err    error
}

func (e *GitError) Error() string {
	if e.Detail == "" {
		return e.Err.Error()
	}
	return e.Err.Error() + ": " + e.Detail
}

// Unwrap exposes the underlying exec error so errors.Is/As continue down the
// chain, and so a renderer that has already printed the raw output separately
// can recover the bare cause instead of printing git's text twice.
func (e *GitError) Unwrap() error { return e.Err }

// DetailLine picks the single most diagnostic line out of git's output. git
// marks its own failures with "fatal:" or "error:", so prefer the first such
// line; absent one, the first non-empty line. Returns "" for empty output,
// which makes GitError render exactly as the bare error did.
func DetailLine(out string) string {
	if out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "fatal:") || strings.HasPrefix(t, "error:") {
			return t
		}
	}
	for _, ln := range lines {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

// Wrap attaches git's own explanation to err, taking it from the captured
// stderr an *exec.ExitError carries. Returns err unchanged when it is nil.
//
// This is the cmd.Output() counterpart to building a GitError from
// CombinedOutput: os/exec populates (*exec.ExitError).Stderr only when
// cmd.Stderr was nil, which is exactly the shape a read-only probe uses when
// it wants stdout clean of git's diagnostics. The diagnosis is therefore
// already in hand at every such call site; without this it is discarded and
// the error renders as a bare exit status.
//
// Callers that already hold git's output as a string (CombinedOutput) build
// &GitError{Detail: DetailLine(out), Err: err} directly instead — the detail
// is theirs to supply, and re-reading Stderr would find it empty.
func Wrap(err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || len(ee.Stderr) == 0 {
		// No captured stderr: a context deadline, a chdir failure, or a git
		// binary that is not on PATH. There is nothing to attach, and
		// inventing a detail would be worse than the bare error.
		return err
	}
	detail := DetailLine(strings.TrimSpace(string(ee.Stderr)))
	if detail == "" {
		return err
	}
	return &GitError{Detail: detail, Err: err}
}
