// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bodyRepo builds a real git repo with a root commit plus each message in
// msgs committed (empty commits, in order). It returns the repo dir.
func bodyRepo(t *testing.T, msgs ...string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commitMsg := func(msg string) {
		f := filepath.Join(dir, "MSG")
		if err := os.WriteFile(f, []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		run("commit", "--allow-empty", "-F", f)
	}
	run("init")
	run("commit", "--allow-empty", "-m", "root")
	for _, m := range msgs {
		commitMsg(m)
	}
	return dir
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	sha, err := HeadSHA(context.Background(), dir)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if sha == "" {
		t.Fatal("HeadSHA returned empty on a real repo")
	}
	return sha
}

// TestCommitBodiesSinceAnchor_MultiLineIntegrity is the load-bearing case: a
// commit body with blank lines, bullet lines, and a trailing footer must reach
// the parser byte-for-byte, where the old subject-only line-split would shred
// it into bogus records.
func TestCommitBodiesSinceAnchor_MultiLineIntegrity(t *testing.T) {
	multi := "feat: the thing\n\nParagraph one has\nmultiple lines.\n\n- bullet a\n- bullet b\n\nCloses #42"
	dir := bodyRepo(t, "chore: first\n", multi)

	root, err := OldestRootCommit(context.Background(), dir)
	if err != nil {
		t.Fatalf("OldestRootCommit: %v", err)
	}
	commits, err := CommitBodiesSinceAnchor(context.Background(), dir, root)
	if err != nil {
		t.Fatalf("CommitBodiesSinceAnchor: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2 (root excluded): %+v", len(commits), commits)
	}
	// Oldest-first ordering.
	if commits[0].Subject != "chore: first" {
		t.Errorf("commits[0].Subject = %q, want %q", commits[0].Subject, "chore: first")
	}
	last := commits[1]
	if last.Body != strings.TrimRight(multi, "\n") {
		t.Errorf("multi-line body corrupted:\n got: %q\nwant: %q", last.Body, multi)
	}
	if last.Subject != "feat: the thing" {
		t.Errorf("subject = %q, want first line of body", last.Subject)
	}
}

// TestCommitBodiesSinceAnchor_EmptyAnchor degrades to an empty list rather than
// walking the whole history.
func TestCommitBodiesSinceAnchor_EmptyAnchor(t *testing.T) {
	dir := bodyRepo(t, "only: one\n")
	commits, err := CommitBodiesSinceAnchor(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("CommitBodiesSinceAnchor: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("empty anchor should yield no commits, got %+v", commits)
	}
}

// TestCommitBodiesSinceAnchor_NoNewCommits — anchor == HEAD yields nothing,
// the idempotence the archive relies on.
func TestCommitBodiesSinceAnchor_NoNewCommits(t *testing.T) {
	dir := bodyRepo(t, "a: one\n")
	head := headSHA(t, dir)
	commits, err := CommitBodiesSinceAnchor(context.Background(), dir, head)
	if err != nil {
		t.Fatalf("CommitBodiesSinceAnchor: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("anchor==HEAD should yield no commits, got %+v", commits)
	}
}

// TestHeadSHA_NonRepo returns "" (no error) for a non-repo dir.
func TestHeadSHA_NonRepo(t *testing.T) {
	sha, err := HeadSHA(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if sha != "" {
		t.Errorf("non-repo HeadSHA = %q, want empty", sha)
	}
}

// TestParseCommitBodies_EmptyBody records the header for a commit whose message
// is only a subject (no body paragraph) without dropping the SHA.
func TestParseCommitBodies_EmptyBody(t *testing.T) {
	// One record: sha, US, "subject only", RS.
	out := "abc123\x1fsubject only\x1e\n"
	got := parseCommitBodies(out)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].SHA != "abc123" || got[0].Body != "subject only" {
		t.Errorf("parsed = %+v, want sha=abc123 body=%q", got[0], "subject only")
	}
}

// brokenConfigRepo builds a repo that is entirely intact — real objects, a real
// commit — but whose .git/config is malformed, so every git command against it
// fails with git's own sentence and exit 128.
//
// This is the shape the live corpus can actually produce (a truncated write, a
// half-applied config edit, a repo on a filesystem that lost a tail) and it is
// the one that matters here: it is NOT a missing repository, so any instrument
// that reports it as one is lying. Measured directly with the git binary before
// it was made a fixture:
//
//	git -C <repo> rev-parse HEAD
//	fatal: bad config line 1 in file .git/config
//	rc=128
func brokenConfigRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "throwaway@example.invalid")
	run("config", "user.name", "Throwaway")
	run("commit", "--allow-empty", "-m", "real commit")
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core\nnot valid ini\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestGitCmdRunnerCarriesGitsOwnMessage is Unit A's acceptance gate, and it is
// driven through CommitBodiesSinceAnchor on purpose: that is the exact call
// site the iteration-277 field report came from, reported as its entire
// content as
//
//	commit bodies since anchor: exit status 128
//
// The wrap that fixed that report was added to internal/storage's gitCmd, whose
// acceptance test drives a storage path — so it went green while THIS path, the
// one the report actually named, kept emitting the bare exit code. Reproduced
// at 4524595 before the fix, verbatim.
//
// BREAK: restore `return "", err` in gitCmdRunner and the message collapses to
// "exit status 128" again, failing both assertions below.
func TestGitCmdRunnerCarriesGitsOwnMessage(t *testing.T) {
	dir := brokenConfigRepo(t)

	_, err := CommitBodiesSinceAnchor(context.Background(), dir, "HEAD~1")
	if err == nil {
		t.Fatal("fixture is degenerate: git succeeded against a malformed config")
	}
	// Wrapped the way internal/tools/commit_log_tools.go wraps it, so the
	// assertion is on what a caller actually reads.
	msg := fmt.Errorf("commit bodies since anchor: %w", err).Error()

	if !strings.Contains(msg, "bad config") {
		t.Errorf("error must carry git's own explanation, got %q", msg)
	}
	if strings.HasSuffix(strings.TrimSpace(msg), "exit status 128") {
		t.Errorf("error ends at the exit code — git's captured output was dropped again: %q", msg)
	}
	if n := strings.Count(msg, "\n"); n > 0 {
		t.Errorf("error should carry ONE line of git text, got %d newlines: %q", n, msg)
	}
}
