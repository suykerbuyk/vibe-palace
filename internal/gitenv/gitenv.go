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

// inheritedPathspecModes are git's global pathspec-mode variables. They are
// not repo-local, but an inherited one changes what every path vp hands git
// means: GLOB re-enables wildcards, ICASE folds case, and either beside a
// literal setting makes git refuse to run at all ("global 'literal' pathspec
// setting is incompatible with all other global pathspec settings"). They are
// matched case-insensitively because the Windows environment is.
var inheritedPathspecModes = []string{
	"GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS",
}

// literalPathspecs is the setting SafeGitEnv gives every git subprocess.
const literalPathspecs = "GIT_LITERAL_PATHSPECS=1"

// GlobPathspecs turns literal pathspecs back off for one command. Appended to
// SafeGitEnv's extra it wins, because exec keeps the last value of a repeated
// key. It exists for the one git command that refuses literal mode,
// `git check-ignore` (storage.GitPathIgnored), and the literal-pathspec-opt-out
// source-audit rule allows it nowhere else: a command run with it reads a
// leading ':' as pathspec magic, so its caller must neutralise that itself.
const GlobPathspecs = "GIT_LITERAL_PATHSPECS=0"

func isPathspecMode(name string) bool {
	for _, m := range inheritedPathspecModes {
		if strings.EqualFold(name, m) {
			return true
		}
	}
	return false
}

// SafeGitEnv returns the environment every git subprocess must use: the
// process environment with every repo-local git variable (GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, ...) and every inherited pathspec mode
// stripped, then GIT_LITERAL_PATHSPECS=1, then extra.
//
// The repo-local strip is because git gives those variables precedence over
// cmd.Dir, so a process that inherits one (spawned from a git hook, or from a
// shell exporting GIT_DIR) would run against whatever repository the variable
// names instead of the directory cmd.Dir says.
//
// Literal pathspecs are because every path vp hands git is a NAME, never a
// pattern. Without them a vault file named a[1].md is a glob that also
// matches a1.md, and one named ':!x.md' is magic that excludes x.md and so
// names every other path: `git add -- ':!x.md'` stages the whole dirty tree.
// Setting it here, rather than as --literal-pathspecs on each call, covers
// every git command built from this environment, CLI and MCP alike, including
// ones not yet written. It is placed before extra so GlobPathspecs can turn it
// off for the one command that needs that.
//
// The setting reaches every process git itself spawns while vp is the parent:
// the user's hooks, clean/smudge/process filter drivers, merge drivers, and
// credential or remote helpers that run git. Any of those that passes git a
// glob pathspec reads it literally under vp.
//
// The git-exec-unsafe-env source-audit rule checks that git subprocesses use
// this, but it recognises only exec.Command calls naming a literal "git"; a
// runner that names git through a variable is not seen by it.
func SafeGitEnv(extra ...string) []string {
	env := withoutRepoLocalGitEnv(os.Environ())
	out := make([]string, 0, len(env)+1+len(extra))
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !isPathspecMode(name) {
			out = append(out, kv)
		}
	}
	out = append(out, literalPathspecs)
	return append(out, extra...)
}
