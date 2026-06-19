// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitCmdRunner runs a git command in dir and returns its stdout. Test seam.
//
// It pins GIT_TERMINAL_PROMPT=0 (no credential prompts) and GIT_EDITOR=true
// (no interactive editor) so read-only probes can never hang on hosts where
// core.editor is configured to an interactive command.
var gitCmdRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ResolveProjectRoot walks upward from start looking for a `.git` entry and
// returns the directory containing it. When no `.git` is found it returns
// start unchanged — anchors and git probes then degrade gracefully.
func ResolveProjectRoot(start string) string {
	if start == "" {
		return ""
	}
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// DetectBranch returns the current branch name for the repo at projectDir, or
// "" when detection fails (detached HEAD, not a repo, timeout).
func DetectBranch(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	out, err := gitCmdRunner(ctx, projectDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// VaultDirtyByCategory reports uncommitted writes under Projects/<project>/,
// split into memory (Projects/<project>/memory/...) and non-memory. Memory is
// committed automatically (by the Claude SessionEnd harvest and by /wrap's
// vault sync) and is not nag-worthy; non-memory is the signal the wrap
// preflight warns on. Non-repo / empty project / clean tree → both false.
//
// When project is non-empty the probe is scoped to Projects/<project>/ so a
// sibling project's uncommitted writes do not falsely trip the flags. When
// project is empty, all dirt counts as non-memory (no memory subtree to scope).
func VaultDirtyByCategory(vaultPath, project string) (nonMemory, memory bool, err error) {
	if vaultPath == "" {
		return false, false, nil
	}
	if _, err := os.Stat(filepath.Join(vaultPath, ".git")); err != nil {
		return false, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	args := []string{"status", "--porcelain"}
	if project != "" {
		// `--` forces the trailing arg to be interpreted as a path; the
		// trailing slash limits the match to the subtree.
		args = append(args, "--", "Projects/"+project+"/")
	}
	out, err := gitCmdRunner(ctx, vaultPath, args...)
	if err != nil {
		return false, false, err
	}

	memPrefix := ""
	if project != "" {
		memPrefix = "Projects/" + project + "/memory/"
	}
	for line := range strings.SplitSeq(out, "\n") {
		path := porcelainPath(line)
		if path == "" {
			continue
		}
		if memPrefix != "" && strings.HasPrefix(path, memPrefix) {
			memory = true
		} else {
			nonMemory = true
		}
	}
	return nonMemory, memory, nil
}

// porcelainPath extracts the working-tree path from a single
// `git status --porcelain` line. Each line is `XY <path>` (two status chars,
// a space, then the path); renames/copies are `XY <old> -> <new>`, for which
// the new (destination) path is returned. Blank/short lines yield "".
func porcelainPath(line string) string {
	if len(line) < 4 {
		return ""
	}
	// Strip the two status chars and the separating space.
	path := strings.TrimSpace(line[3:])
	if path == "" {
		return ""
	}
	// For renames/copies, classify by the destination path.
	if idx := strings.Index(path, " -> "); idx >= 0 {
		path = path[idx+len(" -> "):]
	}
	// Porcelain quotes paths containing special chars; drop the wrapping
	// quotes so prefix matching still works for the common case.
	if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
		path = path[1 : len(path)-1]
	}
	return path
}

// VaultHasUncommittedWrites returns true iff there are any uncommitted writes
// under Projects/<project>/ (memory or non-memory). Returns false and nil error
// when vaultPath is empty or not a git repo. Retained for callers that want the
// "any dirt" meaning; reimplemented atop VaultDirtyByCategory.
//
// When project is non-empty, the probe is scoped to Projects/<project>/ so a
// sibling project's uncommitted writes do not falsely trip the warning.
func VaultHasUncommittedWrites(vaultPath, project string) (bool, error) {
	nonMemory, memory, err := VaultDirtyByCategory(vaultPath, project)
	if err != nil {
		return false, err
	}
	return nonMemory || memory, nil
}

// ProjectHasUncommittedWrites returns true iff `git status --porcelain` in
// projectDir produces any output. Returns false and nil error when projectDir
// is empty or not a git repo.
func ProjectHasUncommittedWrites(projectDir string) (bool, error) {
	if projectDir == "" {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	out, err := gitCmdRunner(ctx, projectDir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// LastIterAnchorSha returns the SHA of the most recent commit that touched the
// project's iter stamp file (.vibe-palace/last-iter). Empty string when the
// file is not yet tracked — the canonical "no prior wrap" signal.
func LastIterAnchorSha(projectDir string) (string, error) {
	if projectDir == "" {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := gitCmdRunner(ctx, projectDir,
		"log", "-n", "1", "--format=%H", "--",
		AnchorDir+"/"+AnchorFile)
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// CommitsSinceAnchor returns the commits between anchorSHA (exclusive) and HEAD
// (inclusive) in projectDir, as a list of {SHA, Subject} records. Empty
// anchorSHA degrades to an empty list.
func CommitsSinceAnchor(ctx context.Context, projectDir, anchorSHA string) ([]CommitInfo, error) {
	if projectDir == "" || anchorSHA == "" {
		return nil, nil
	}
	out, err := gitCmdRunner(ctx, projectDir,
		"log", "--format=%H %s", anchorSHA+"..HEAD")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	commits := []CommitInfo{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Format is "<sha><space><subject>"; split on the first space only.
		idx := strings.IndexByte(line, ' ')
		if idx < 0 {
			commits = append(commits, CommitInfo{SHA: line})
			continue
		}
		commits = append(commits, CommitInfo{SHA: line[:idx], Subject: line[idx+1:]})
	}
	return commits, nil
}

// FilesChangedSinceAnchor returns the list of files that differ between
// anchorSHA and HEAD in projectDir. Empty anchorSHA degrades to an empty list.
func FilesChangedSinceAnchor(ctx context.Context, projectDir, anchorSHA string) ([]string, error) {
	if projectDir == "" || anchorSHA == "" {
		return nil, nil
	}
	out, err := gitCmdRunner(ctx, projectDir,
		"diff", "--name-only", anchorSHA+"..HEAD")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	files := []string{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		files = append(files, line)
	}
	return files, nil
}

// OldestRootCommit returns the SHA of the oldest root commit reachable from
// HEAD in projectDir. `git rev-list --max-parents=0 HEAD` lists every
// parent-less commit (newest first); the last non-empty line is the oldest.
func OldestRootCommit(ctx context.Context, projectDir string) (string, error) {
	if projectDir == "" {
		return "", nil
	}
	out, err := gitCmdRunner(ctx, projectDir, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l, nil
		}
	}
	return "", nil
}
