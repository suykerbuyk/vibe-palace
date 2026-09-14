// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package gitenv builds a safe environment for a git subprocess: the process
// environment with every repo-local git variable stripped, so cmd.Dir alone —
// never an inherited GIT_DIR/GIT_WORK_TREE — decides which repository the
// subprocess touches.
//
// This is a dependency-free leaf package on purpose. The logic originated in
// internal/storage (as the vault fix for
// vault-git-runners-inherit-git-dir-from-the-environment), but internal/storage
// itself imports internal/project and internal/wrapstate — so either of those
// importing storage back for this one helper would be a hard import cycle.
// Moving the helper here, with no internal dependencies of its own, lets every
// package that spawns git (storage, project, wrapstate, worktree, absorb,
// archive, and whatever comes next) reach it without constraint.
// internal/storage.SafeGitEnv now forwards here rather than duplicating it.
package gitenv

import (
	"os"
	"slices"
	"strings"
)

// repoLocalGitEnv is git's own list of variables that redirect a command to a
// different repository, index or work tree (`git rev-parse --local-env-vars`).
// A process that inherits any of them — vp spawned from a git hook, or from a
// shell exporting GIT_DIR — would have a git subprocess answer for that other
// repository instead of the directory cmd.Dir names.
var repoLocalGitEnv = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT", "GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX",
	"GIT_INTERNAL_SUPER_PREFIX", "GIT_SHALLOW_FILE", "GIT_COMMON_DIR",
}

// withoutRepoLocalGitEnv returns env minus every repoLocalGitEnv variable, so
// a git call built from it answers about the directory cmd.Dir names rather
// than a repository named by an inherited environment variable.
func withoutRepoLocalGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(repoLocalGitEnv, name) {
			out = append(out, kv)
		}
	}
	return out
}

// SafeGitEnv returns the process environment with every repo-local git
// variable (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, ...) stripped, plus extra.
// Every git subprocess must build cmd.Env from this, not os.Environ()
// directly: git gives those variables precedence over cmd.Dir, so a process
// that inherits one (spawned from a git hook, or from a shell exporting
// GIT_DIR) would run against whatever repository the variable names instead
// of the directory cmd.Dir says.
func SafeGitEnv(extra ...string) []string {
	return append(withoutRepoLocalGitEnv(os.Environ()), extra...)
}
