// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/giterr"
)

// gitCmdRunner runs a git command in dir and returns its stdout. Test seam.
//
// It pins GIT_TERMINAL_PROMPT=0 (no credential prompts) and GIT_EDITOR=true
// (no interactive editor) so read-only probes can never hang on hosts where
// core.editor is configured to an interactive command.
//
// Failures are wrapped by giterr HERE, not at the ten call sites below, for the
// same reason internal/storage's gitCmd wraps at its own single point: exec's
// *ExitError renders as exactly "exit status 128" while git's own explanation —
// already captured in (*exec.ExitError).Stderr, because cmd.Stderr is nil —
// would otherwise be dropped one line later. Wrapping here means every existing
// `fmt.Errorf("...: %w", err)` site gains the diagnosis without being rewritten,
// and every future one is born with it.
//
// This runner is SEPARATE from internal/storage's gitCmd by necessity, not by
// preference: internal/storage imports this package, so importing it back for a
// shared runner would be a hard cycle. giterr is the leaf both call instead —
// the same shape as gitenv, which this runner already uses for cmd.Env.
var gitCmdRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitenv.SafeGitEnv("GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.Output()
	if err != nil {
		return "", giterr.Wrap(err)
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

	// An unborn repo is checked FIRST, because `git log` cannot express it as
	// anything but a fatal: measured, it exits 128 with "your current branch
	// 'main' does not have any commits yet" — the same code a malformed config
	// produces. Asking rev-parse first is what keeps this distinguishable
	// without matching on that sentence.
	if _, hasHead, err := resolveHead(ctx, projectDir); err != nil {
		return "", err
	} else if !hasHead {
		return "", nil
	}

	out, err := gitCmdRunner(ctx, projectDir,
		"log", "-n", "1", "--format=%H", "--",
		AnchorDir+"/"+AnchorFile)
	if err != nil {
		return "", err
	}
	// 🔴 EMPTY OUTPUT ON A SUCCESSFUL LOG IS THE "NO PRIOR WRAP" SIGNAL AND MUST
	// STAY ("", nil). `git log -- <untracked path>` exits 0 with no output, and
	// that is the canonical first-wrap state — every repo is in it once.
	// Collapsing it into the error path would be a regression wearing a fix's
	// clothes: the caller would start failing on the one state it is guaranteed
	// to meet. Verified against the live corpus, where all three real
	// repositories are in exactly this state.
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

// resolveHead resolves HEAD in projectDir, separating the two outcomes that
// used to be one: "this repo has no HEAD yet" and "git could not answer".
//
// hasHead=false with a nil error means the repo is FINE and simply has no
// commit to point at — an unborn branch. A non-nil error means git failed and
// the caller has been told nothing about this repo at all.
//
// The discriminator is `rev-parse --verify --quiet HEAD`'s EXIT CODE, not a
// message match. Measured directly with the git binary against the four shapes
// this package can meet:
//
//	healthy repo                    rc=0    <sha>
//	unborn branch (git init, no commit)  rc=1    (no output)
//	malformed .git/config           rc=128  fatal: bad config line 1 in file .git/config
//	.git gitfile -> deleted gitdir  rc=128  fatal: not a git repository: ...
//
// So rc=1 is "the ref does not resolve, the repository is otherwise fine" and
// rc=128 is a real failure. Keying on the code rather than on git's wording is
// deliberate: the wording is localized and version-dependent, the codes are
// part of rev-parse's contract. --quiet suppresses only the unknown-revision
// message; a fatal still reaches stderr, which is what giterr.Wrap needs.
//
// Reaching the exit code through the wrapped error is the payoff from giterr
// keeping Unwrap: errors.As walks to the *exec.ExitError underneath while the
// error a caller prints still carries git's own sentence.
func resolveHead(ctx context.Context, projectDir string) (sha string, hasHead bool, err error) {
	out, err := gitCmdRunner(ctx, projectDir, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// HeadSHA returns the full SHA of HEAD in projectDir. It returns ("", nil) for
// the three states that genuinely have no HEAD to name — an empty projectDir,
// a directory that is not a repo, and a repo with no commits yet — and a real
// error for anything else.
//
// 🔴 THE EMPTY-AND-NIL CASES ARE A CLOSED LIST, AND THAT IS THE POINT. This
// function used to return ("", nil) for a FAILED probe too, which made a
// healthy repository that git merely could not read indistinguishable from a
// directory that has never seen git. vp_archive_commit_log keys on `head == ""`
// and reports `commits_archived: 0` with the note "project_path is not a git
// repo with commits" — a statement that was false for a repo whose config was
// malformed, and that a reader could not tell from the true case. The error
// return existed the whole time and could never fire: the caller's
// `if err != nil` was dead code guarding a live defect.
//
// Adding a fourth quiet case here re-opens that hole, so any future "degrade
// gracefully" instinct belongs in the CALLER, where the decision is visible,
// not here where it is silent.
func HeadSHA(ctx context.Context, projectDir string) (string, error) {
	if projectDir == "" {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil {
		return "", nil
	}
	sha, hasHead, err := resolveHead(ctx, projectDir)
	if err != nil {
		return "", err
	}
	if !hasHead {
		return "", nil
	}
	return sha, nil
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

// ErrAnchorUnresolvable and ErrAnchorNotAncestor classify the two ways a
// commit-log anchor can fail to describe this repo's history.
var (
	ErrAnchorUnresolvable = errors.New("anchor does not resolve to a commit in this repository")
	ErrAnchorNotAncestor  = errors.New("anchor is not an ancestor of HEAD")
)

// ValidateAnchorAgainstHEAD reports whether anchorSHA is a commit in projectDir
// that HEAD descends from — the only condition under which `git log
// <anchor>..HEAD` means "everything archived since the anchor".
//
// 🔴 Why an existence check is NOT enough. When the anchor object is missing
// (gc'd, or absent on a fresh clone) `git log <anchor>..HEAD` already fails
// loudly with exit 128, so that case was never the silent one. The case that
// silently corrupts is an anchor that RESOLVES but sits off the HEAD line —
// after a rebase or an abandoned branch. There `git log <orphan>..HEAD` happily
// emits the symmetric difference, re-yielding commits already archived on the
// surviving line, and the caller cannot tell that from honest new work. That is
// how iter 281 re-archived a commit and recorded an orphan as landed history.
//
// The two probes are separate on purpose: `merge-base --is-ancestor` exits
// non-zero for BOTH "not an ancestor" and "bad object", and gitCmdRunner
// surfaces only an exit status (stderr is captured by cmd.Output and never
// read), so a single call could not tell the operator which of the two
// happened. Resolving first is what makes the diagnosis honest rather than a
// guess at an exit code.
func ValidateAnchorAgainstHEAD(ctx context.Context, projectDir, anchorSHA string) error {
	if projectDir == "" || strings.TrimSpace(anchorSHA) == "" {
		return nil
	}
	if _, err := gitCmdRunner(ctx, projectDir, "rev-parse", "--verify", "--quiet", anchorSHA+"^{commit}"); err != nil {
		return fmt.Errorf("%w: %s", ErrAnchorUnresolvable, anchorSHA)
	}
	if _, err := gitCmdRunner(ctx, projectDir, "merge-base", "--is-ancestor", anchorSHA, "HEAD"); err != nil {
		return fmt.Errorf("%w: %s", ErrAnchorNotAncestor, anchorSHA)
	}
	return nil
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
