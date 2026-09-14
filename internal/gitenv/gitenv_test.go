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
