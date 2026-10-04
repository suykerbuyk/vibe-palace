// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitenv

import (
	"strings"
	"testing"
)

// TestSafeGitEnv pins the repo-local-git-var strip: an inherited GIT_DIR /
// GIT_WORK_TREE must not survive into a git subprocess's environment, while an
// unrelated variable and the caller's own extras must pass through unchanged.
// internal/storage.SafeGitEnv now forwards here; its own TestSafeGitEnv
// (internal/storage/git_test.go) pins that the forward itself works, and is
// unchanged by this move.
func TestSafeGitEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/decoy/.git")
	t.Setenv("GIT_WORK_TREE", "/decoy")
	t.Setenv("VP_SAFE_GIT_ENV_MARKER", "keep-me")

	env := SafeGitEnv("EXTRA=1")

	var sawMarker, sawExtra bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") {
			t.Errorf("SafeGitEnv leaked a repo-local variable through: %s", kv)
		}
		if kv == "VP_SAFE_GIT_ENV_MARKER=keep-me" {
			sawMarker = true
		}
		if kv == "EXTRA=1" {
			sawExtra = true
		}
	}
	if !sawMarker {
		t.Error("SafeGitEnv dropped an unrelated variable it should have passed through")
	}
	if !sawExtra {
		t.Error("SafeGitEnv did not append its extra argument")
	}
}

// lastValue returns the value exec would use for name: the last one in env.
func lastValue(env []string, name string) (string, bool) {
	val, found := "", false
	for _, kv := range env {
		if n, v, _ := strings.Cut(kv, "="); n == name {
			val, found = v, true
		}
	}
	return val, found
}

func TestSafeGitEnvReadsPathspecsLiterally(t *testing.T) {
	t.Setenv("GIT_LITERAL_PATHSPECS", "0")
	t.Setenv("GIT_GLOB_PATHSPECS", "1")
	t.Setenv("GIT_NOGLOB_PATHSPECS", "1")
	t.Setenv("GIT_ICASE_PATHSPECS", "1")
	t.Setenv("git_glob_pathspecs", "1")
	t.Setenv("Git_Icase_Pathspecs", "1")

	env := SafeGitEnv("EXTRA=1")
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(name, "GIT_LITERAL_PATHSPECS") {
			continue
		}
		if strings.HasSuffix(strings.ToUpper(name), "_PATHSPECS") {
			t.Errorf("SafeGitEnv let an inherited pathspec mode through: %s", kv)
		}
	}
	if v, ok := lastValue(env, "GIT_LITERAL_PATHSPECS"); !ok || v != "1" {
		t.Errorf("GIT_LITERAL_PATHSPECS = %q (set %v), want 1 over the inherited 0", v, ok)
	}
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_LITERAL_PATHSPECS=") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("GIT_LITERAL_PATHSPECS appears %d times, want 1 (the inherited one stripped)", n)
	}

	if v, _ := lastValue(SafeGitEnv(GlobPathspecs), "GIT_LITERAL_PATHSPECS"); v != "0" {
		t.Errorf("with GlobPathspecs, GIT_LITERAL_PATHSPECS = %q, want the opt-out 0 to win", v)
	}
}
