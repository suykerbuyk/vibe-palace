// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// afterPushHook runs immediately after a successful push records its SHA in
// remoteSHA, before the convergence loop. Production code must leave this as a
// no-op; tests override it to inject mid-flight state changes (e.g. a
// concurrent writer mutating a bare remote between the recorded push and the
// convergence force-with-lease).
var afterPushHook = func(remote string) {}

// PushResult reports what happened during a CommitAndPushPaths operation.
type PushResult struct {
	CommitSHA     string           // the commit SHA that was pushed (empty if nothing to commit)
	RemoteResults map[string]error // per-remote push result (nil = success)
	// SkippedPaths lists the supplied paths that were dropped before staging
	// because they matched nothing in BOTH the worktree and the index (absent
	// file that is not tracked). Tracked-but-deleted paths are NOT skipped —
	// their deletion is staged. A populated entry here flags either a benign
	// not-yet-created path (e.g. an empty Projects/<slug>/memory/) or a
	// misspelled path the caller should notice.
	SkippedPaths []string
}

// AllPushed returns true if all remotes were pushed successfully.
func (r *PushResult) AllPushed() bool {
	if len(r.RemoteResults) == 0 {
		return false
	}
	for _, err := range r.RemoteResults {
		if err != nil {
			return false
		}
	}
	return true
}

// AnyPushed returns true if at least one remote was pushed successfully.
func (r *PushResult) AnyPushed() bool {
	for _, err := range r.RemoteResults {
		if err == nil {
			return true
		}
	}
	return false
}

// CommitAndPushPaths stages only the supplied paths, commits with a
// machine-stamped message, and (when push=true) pushes to all configured
// remotes. Dirty paths NOT in the supplied list are left dirty in the working
// tree — this is the contamination-safe entry point used by callers that know
// which files belong to their work unit. It NEVER runs `git add -A`.
//
// Empty or nil paths returns (nil, error) — explicit caller intent is
// required.
//
// Staging uses `git add -- <paths>...` with the `--` separator so paths
// beginning with `-` are treated as paths, not flags. Long path lists are
// batched under a conservative ~64 KB argv-byte budget per invocation to stay
// clear of the Linux ~128 KB and macOS ~64 KB MAX_ARG_LEN ceilings; all
// batches must succeed before the commit step.
//
// When push=false, the function stages, commits, and returns immediately with
// PushResult.CommitSHA set and RemoteResults nil. No remote network I/O occurs
// and the rebase / converge loop is skipped — useful for local-only commits
// that a later push will carry to remotes.
//
// When push=true: the happy path is a sequential fast-forward push. On a
// non-fast-forward rejection, the rejected remote is fetched and the local
// branch is rebased onto it; the rebased commit is then pushed to that remote.
// Fetch failures and rebase failures (after `rebase --abort`) surface directly
// via per-remote RemoteResults rather than masquerading as downstream errors.
//
// After every successful rebase-and-push, prior remotes whose last-known-good
// SHA differs from the new HEAD are converged via
// `git push --force-with-lease=refs/heads/<branch>:<expected> <remote>
// <branch>`. The lease is an atomic compare-and-swap: if any concurrent writer
// has moved the prior remote's ref since we recorded it, the push is rejected
// and the failure surfaces as "convergence rejected (concurrent writer at
// <remote>): <err>" in PushResult.RemoteResults. The lease is the only path in
// this function that uses --force-with-lease; naked --force is never used.
//
// PushResult.CommitSHA is refreshed to the post-loop HEAD if any rebase
// happened, so the printed SHA always exists at the converged remotes.
func CommitAndPushPaths(vaultPath, message string, paths []string, push bool) (*PushResult, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no paths specified")
	}

	result := &PushResult{}

	// Drop paths that match nothing in BOTH the worktree and the index; `git
	// add -- <path>` fatals (exit 128) on such a no-match and would abort the
	// whole commit for one absent path (e.g. a never-written memory/ dir).
	// Tracked-but-deleted paths survive the filter so their deletion is staged.
	keep, skipped := filterStageablePaths(vaultPath, paths)
	result.SkippedPaths = skipped
	if len(keep) == 0 {
		// Non-empty input filtered to nothing: benign no-op, not an error.
		// (The zero-input case errored at the guard above.)
		return result, nil
	}

	if err := checkIdentity(vaultPath); err != nil {
		return nil, err
	}

	// Remote enumeration is required only for the network push path.
	// Local-only commits (push=false) must succeed on a vault with zero
	// remotes.
	var remotes []string
	if push {
		var rErr error
		remotes, rErr = listRemotes(vaultPath)
		if rErr != nil {
			return nil, fmt.Errorf("listing remotes: %w", rErr)
		}
		if len(remotes) == 0 {
			return nil, fmt.Errorf("no git remotes configured in vault %s", vaultPath)
		}
	}

	// Stage only the surviving paths. Chunk under a conservative argv byte
	// budget to stay clear of MAX_ARG_LEN ceilings.
	if err := stageInBatches(vaultPath, keep); err != nil {
		return nil, fmt.Errorf("git add: %w", err)
	}

	// Check if anything to commit.
	if _, err := gitCmd(vaultPath, 10*time.Second, "diff", "--cached", "--quiet"); err == nil {
		// Exit code 0 means no staged changes.
		return result, nil
	}

	// Stamp with hostname.
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	fullMsg := fmt.Sprintf("%s\n\n[%s]", message, hostname)

	// Commit.
	if _, err := gitCmd(vaultPath, 10*time.Second, "commit", "-m", fullMsg); err != nil {
		return nil, fmt.Errorf("git commit: %w", err)
	}

	// Get commit SHA.
	sha, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--short", "HEAD")
	result.CommitSHA = sha

	// Local-only commit: skip the push loop entirely.
	if !push {
		return result, nil
	}

	// Push to all remotes.
	branch, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" {
		branch = "main"
	}

	result.RemoteResults = make(map[string]error, len(remotes))
	remoteSHA := make(map[string]string, len(remotes))
	rebasedAny := false

	for _, remote := range remotes {
		curSHA, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")

		if _, pushErr := gitCmd(vaultPath, 60*time.Second, "push", remote, branch); pushErr == nil {
			// Happy path: fast-forward push succeeded.
			remoteSHA[remote] = curSHA
			result.RemoteResults[remote] = nil
			afterPushHook(remote)
			continue
		}

		// Rejection path (non-fast-forward or other push error). Fetch
		// failure surfaces directly — no rebase/converge.
		if _, fetchErr := gitCmd(vaultPath, 60*time.Second, "fetch", remote); fetchErr != nil {
			result.RemoteResults[remote] = fmt.Errorf("fetch %s: %w", remote, fetchErr)
			continue
		}

		// Rebase failure aborts cleanly so HEAD does not stay polluted for
		// the next remote in the loop.
		if _, rebaseErr := gitCmd(vaultPath, 60*time.Second, "rebase", remote+"/"+branch); rebaseErr != nil {
			_, _ = gitCmd(vaultPath, 10*time.Second, "rebase", "--abort")
			result.RemoteResults[remote] = fmt.Errorf("rebase against %s failed: %w", remote, rebaseErr)
			continue
		}

		rebasedAny = true
		curSHA, _ = gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")

		if _, pushErr := gitCmd(vaultPath, 60*time.Second, "push", remote, branch); pushErr != nil {
			// Non-NFF push error after rebase (auth, network, etc.).
			result.RemoteResults[remote] = pushErr
			continue
		}
		remoteSHA[remote] = curSHA
		result.RemoteResults[remote] = nil
		afterPushHook(remote)

		// Converge prior remotes whose recorded SHA != current HEAD.
		for priorRemote, priorSHA := range remoteSHA {
			if priorSHA == curSHA {
				continue
			}
			if leaseErr := forceWithLease(vaultPath, priorRemote, branch, priorSHA); leaseErr != nil {
				result.RemoteResults[priorRemote] = fmt.Errorf(
					"convergence rejected (concurrent writer at %s): %w",
					priorRemote, leaseErr)
				// Leave remoteSHA[priorRemote] unchanged — caller sees
				// divergent state via per-remote error.
				continue
			}
			remoteSHA[priorRemote] = curSHA
			// result.RemoteResults[priorRemote] stays nil (still success).
			log.Printf("vault: force-converged %s from %s\n", priorRemote, short(priorSHA))
		}
	}

	// Refresh CommitSHA so callers never print a SHA that no longer exists at
	// any remote.
	if rebasedAny {
		if newSHA, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--short", "HEAD"); err == nil {
			result.CommitSHA = newSHA
		}
	}

	return result, nil
}

// CommitAndPushPathsWithDowngrade wraps CommitAndPushPaths with the remote-
// downgrade policy shared by tidy and harvest: when push is requested but the
// vault has zero configured remotes, it downgrades to a local-only commit
// (CommitAndPushPaths errors on push against a remote-less vault) and reports
// downgraded=true. When push is false it passes straight through and downgraded
// is always false. The returned *PushResult and error are CommitAndPushPaths's
// own (RemoteResults populated only when an effective push ran).
func CommitAndPushPathsWithDowngrade(vaultPath, message string, paths []string, push bool) (res *PushResult, downgraded bool, err error) {
	effectivePush := push
	if push {
		remotes, rErr := listRemotes(vaultPath)
		if rErr != nil {
			return nil, false, fmt.Errorf("listing remotes: %w", rErr)
		}
		if len(remotes) == 0 {
			effectivePush = false
			downgraded = true
		}
	}
	res, err = CommitAndPushPaths(vaultPath, message, paths, effectivePush)
	if err != nil {
		return nil, downgraded, err
	}
	return res, downgraded, nil
}

// HasUncommittedChanges reports whether `git status --porcelain` limited to the
// given vault-relative paths produces any output. Empty/clean → false. Returns
// false (not an error) when vaultPath is not a git repo (e.g. a never-init'd
// vault), so callers can treat "no repo" the same as "nothing to commit".
//
// Paths are passed after a `--` separator so entries beginning with `-` are
// treated as paths, not flags. A directory path includes everything beneath it.
func HasUncommittedChanges(vaultPath string, relPaths ...string) (bool, error) {
	if _, err := gitCmd(vaultPath, 5*time.Second, "rev-parse", "--is-inside-work-tree"); err != nil {
		// Not a git repo (or git unavailable) → treat as clean, not an error.
		return false, nil
	}
	args := make([]string, 0, len(relPaths)+3)
	args = append(args, "status", "--porcelain", "--")
	args = append(args, relPaths...)
	out, err := gitCmd(vaultPath, 10*time.Second, args...)
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

// filterStageablePaths partitions paths into those safe to `git add` (keep) and
// those that would make `git add -- <path>` fatal because they match nothing in
// BOTH the worktree and the index (skipped).
//
// The predicate is deletion-safe: `git add` only aborts (exit 128) when a path
// is absent from the worktree AND not tracked. A tracked file deleted from the
// worktree is NOT a no-match — `git add` correctly stages the deletion — so it
// must be kept. A naive os.Stat filter would drop it and silently fail to commit
// the removal. Keep path P when os.Lstat(P) succeeds (present in the worktree,
// including as a symlink or directory) OR `git ls-files --error-unmatch -- P`
// exits 0 (tracked, possibly deleted). Skip only when both fail.
//
// Paths are literal vault-relative strings; no glob matching is performed.
func filterStageablePaths(vaultPath string, paths []string) (keep, skipped []string) {
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(vaultPath, p)); err == nil {
			keep = append(keep, p)
			continue
		}
		// Absent from the worktree — keep only if git tracks it (a staged
		// deletion is legitimate work; `git add` will record the removal).
		if _, err := gitCmd(vaultPath, 10*time.Second, "ls-files", "--error-unmatch", "--", p); err == nil {
			keep = append(keep, p)
			continue
		}
		skipped = append(skipped, p)
	}
	return keep, skipped
}

// stageBatchByteBudget caps the per-`git add` argv path-byte budget at ~64 KB.
// Linux MAX_ARG_LEN is ~128 KB and macOS is ~64 KB; the lower figure governs.
// Each path costs len(path)+1 bytes (NUL terminator) when measured against the
// kernel argv ceiling.
const stageBatchByteBudget = 64 * 1024

// stageInBatches runs `git add -- <chunk>...` over paths, splitting into
// chunks whose combined argv-path bytes stay under stageBatchByteBudget.
// Always emits the `--` separator so paths beginning with `-` are treated as
// paths, not flags.
func stageInBatches(vaultPath string, paths []string) error {
	batch := make([]string, 0, len(paths))
	bytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		args := make([]string, 0, len(batch)+2)
		args = append(args, "add", "--")
		args = append(args, batch...)
		if _, err := gitCmd(vaultPath, 30*time.Second, args...); err != nil {
			return err
		}
		batch = batch[:0]
		bytes = 0
		return nil
	}
	for _, p := range paths {
		cost := len(p) + 1
		if len(batch) > 0 && bytes+cost > stageBatchByteBudget {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, p)
		bytes += cost
	}
	return flush()
}

// forceWithLease pushes branch to remote with a lease keyed to expectedSHA.
// The lease causes git to reject the push if the remote's branch ref has moved
// off expectedSHA since we last observed it — catching concurrent writers
// without resorting to naked --force.
func forceWithLease(vaultPath, remote, branch, expectedSHA string) error {
	lease := fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", branch, expectedSHA)
	_, err := gitCmd(vaultPath, 60*time.Second, "push", lease, remote, branch)
	return err
}

// short returns the first 7 characters of a SHA for log breadcrumbs, or the
// original string if shorter.
func short(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// listRemotes discovers all configured git remotes for the vault repo.
func listRemotes(vaultPath string) ([]string, error) {
	out, err := gitCmd(vaultPath, 10*time.Second, "remote")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var remotes []string
	for r := range strings.SplitSeq(out, "\n") {
		if r = strings.TrimSpace(r); r != "" {
			remotes = append(remotes, r)
		}
	}
	return remotes, nil
}

// checkIdentity returns nil if git can resolve a committer identity from any
// source (.git/config, ~/.gitconfig, system gitconfig, or GIT_AUTHOR_*/
// GIT_COMMITTER_* env vars). Returns an actionable error otherwise. Uses
// `git var GIT_AUTHOR_IDENT` — git's own identity-resolution check, mirroring
// what `git commit` does internally.
func checkIdentity(vaultPath string) error {
	if _, err := gitCmd(vaultPath, 5*time.Second, "var", "GIT_AUTHOR_IDENT"); err != nil {
		return fmt.Errorf(
			"no git identity configured for vault commits (HOME=%s). "+
				"Set with: git config --global user.email <addr> && "+
				"git config --global user.name <name>",
			os.Getenv("HOME"),
		)
	}
	return nil
}

// gitCmd runs a git command in the vault directory with a timeout. Output is
// whitespace-trimmed — convenient for SHA / branch-name parsing.
func gitCmd(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Prevent interactive prompts. GIT_EDITOR=true short-circuits any editor
	// invocation (e.g. rebase --continue composing a merge commit message) so
	// operators with an interactive core.editor do not see the backend hang
	// waiting for stdin. All explicit commits here use `-m` already.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")

	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
