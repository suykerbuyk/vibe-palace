// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// GitState is the three-valued result of probing a project directory for
// uncommitted work. It exists to separate "not a git repo at all" from "a git
// repo whose tree is clean" — a distinction ProjectHasUncommittedWrites
// deliberately collapses (both yield false), and which callers that must refuse
// on an empty diff (vp_ingest_commit_msg) cannot afford to lose.
type GitState int

const (
	// GitNotARepo — no `.git` entry at the directory, so there is nothing to
	// probe. Also the indeterminate value returned alongside a non-nil error.
	GitNotARepo GitState = iota
	// GitClean — a git repo whose `git status --porcelain` output is empty.
	GitClean
	// GitDirty — a git repo with staged, unstaged, or untracked changes.
	GitDirty
)

func (s GitState) String() string {
	switch s {
	case GitNotARepo:
		return "not-a-repo"
	case GitClean:
		return "clean"
	case GitDirty:
		return "dirty"
	default:
		return "unknown"
	}
}

// ProjectGitState reports whether dir is a git repo and, if so, whether its
// working tree carries any uncommitted work. `git status --porcelain` (run
// WITHOUT --ignored) covers staged, unstaged, and untracked changes alike, so
// GitDirty means "any dirt at all" and gitignored files never trip it.
//
// dir must already be a repo ROOT — resolve it with ResolveProjectRoot first.
// The `.git` lookup is a shallow stat of dir itself, so a subdirectory of a
// repo would otherwise misreport GitNotARepo.
//
// The stat deliberately does NOT require a directory: in git worktrees and
// submodules `.git` is a FILE containing a gitdir pointer, and an IsDir() check
// would wrongly classify those as not-a-repo.
//
// An empty dir yields (GitNotARepo, nil). A failed probe (timeout on a slow
// filesystem, broken git) yields (GitNotARepo, err) — the state is
// indeterminate and callers should key off err, not off the state.
func ProjectGitState(dir string) (GitState, error) {
	if dir == "" {
		return GitNotARepo, nil
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return GitNotARepo, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	out, err := gitCmdRunner(ctx, dir, "status", "--porcelain")
	if err != nil {
		return GitNotARepo, err
	}
	if strings.TrimSpace(out) != "" {
		return GitDirty, nil
	}
	return GitClean, nil
}

// ProjectHasUncommittedWrites returns true iff `git status --porcelain` in
// projectDir produces any output. Returns false and nil error when projectDir
// is empty or not a git repo.
//
// Thin wrapper over ProjectGitState so the probe has exactly one
// implementation; it flattens GitNotARepo and GitClean back into false.
func ProjectHasUncommittedWrites(projectDir string) (bool, error) {
	state, err := ProjectGitState(projectDir)
	if err != nil {
		return false, err
	}
	return state == GitDirty, nil
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
		before, after, ok := strings.Cut(line, " ")
		if !ok {
			commits = append(commits, CommitInfo{SHA: line})
			continue
		}
		commits = append(commits, CommitInfo{SHA: before, Subject: after})
	}
	return commits, nil
}

// CommitBodiesSinceAnchor returns the commits between anchorSHA (exclusive) and
// HEAD (inclusive) in projectDir, in CHRONOLOGICAL order (oldest first), each
// carrying its FULL raw message (%B) in CommitInfo.Body. Empty anchorSHA
// degrades to an empty list.
//
// It exists as a sibling of CommitsSinceAnchor rather than an extension of it
// because %B is multi-line: the subject-only walk splits on '\n', which a
// commit body would shred into bogus records. The framing here is
// record-separated instead — each commit is emitted as
//
//	<sha><US><raw body><RS>
//
// where US is the ASCII unit separator (0x1f) between the sha and the body and
// RS is the ASCII record separator (0x1e) terminating each commit. Both are
// control characters that never occur in a commit message, so a body of
// arbitrary length and internal newlines round-trips intact. --reverse yields
// oldest-first so appends to commit-log.md read as a forward history.
func CommitBodiesSinceAnchor(ctx context.Context, projectDir, anchorSHA string) ([]CommitInfo, error) {
	if projectDir == "" || anchorSHA == "" {
		return nil, nil
	}
	out, err := gitCmdRunner(ctx, projectDir,
		"log", "--reverse", "--format=%H%x1f%B%x1e", anchorSHA+"..HEAD")
	if err != nil {
		return nil, err
	}
	return parseCommitBodies(out), nil
}

// parseCommitBodies splits the record-separated log emitted by
// CommitBodiesSinceAnchor into {SHA, Body} records. Each RS-delimited record is
// "<sha><US><body>", possibly wrapped in the newlines git inserts between
// format outputs; those wrapping newlines are trimmed while the body's own
// interior newlines are preserved. The subject is derived as the body's first
// line so callers that want just the headline still have it.
func parseCommitBodies(out string) []CommitInfo {
	commits := []CommitInfo{}
	for rec := range strings.SplitSeq(out, "\x1e") {
		rec = strings.Trim(rec, "\n")
		if rec == "" {
			continue
		}
		sha, body, ok := strings.Cut(rec, "\x1f")
		if !ok {
			commits = append(commits, CommitInfo{SHA: strings.TrimSpace(rec)})
			continue
		}
		body = strings.TrimRight(body, "\n")
		subject, _, _ := strings.Cut(body, "\n")
		commits = append(commits, CommitInfo{
			SHA:     strings.TrimSpace(sha),
			Subject: subject,
			Body:    body,
		})
	}
	return commits
}

// HeadSHA returns the full SHA of HEAD in projectDir, or "" when projectDir is
// empty, is not a repo, or the probe fails (an unborn branch, a detached
// probe error). Callers treat "" as "nothing to anchor to" and no-op.
func HeadSHA(ctx context.Context, projectDir string) (string, error) {
	if projectDir == "" {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil {
		return "", nil
	}
	out, err := gitCmdRunner(ctx, projectDir, "rev-parse", "HEAD")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(out), nil
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
	for _, line := range slices.Backward(lines) {
		if l := strings.TrimSpace(line); l != "" {
			return l, nil
		}
	}
	return "", nil
}
