// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// CanonicalGitignorePatterns is the growing set of .gitignore lines that
// vibe-palace considers canonical for a vault. The reconciler treats
// every entry here as an exact-line presence requirement: missing lines
// are appended at EOF in declaration order; already-present lines are
// left alone (including their position). Comment lines participate in
// the same exact-match rule so a user may override a header by moving
// or editing it — the reconciler will not re-inject it.
var CanonicalGitignorePatterns = []string{
	"# Machine-local data — not synced between machines.",
	"palace/.local/",
	// The header string is kept VERBATIM although no vp from this release
	// writes a reconcile sidecar: every entry is an exact-line presence
	// requirement, so rewording it would append a line to every vault's
	// .gitignore. "*.bak" still covers the content-named backups a
	// `vp commands reset` / `vp skills reset` keeps, and old binaries' .bak
	// files; "*.new" covers the .new / .new.bak sidecars an old binary's
	// keep/.new prompt wrote, which may still sit in a vault.
	"# Template reconcile sidecars",
	"*.bak",
	"*.new",
	"# Per-path advisory write locks (vaultlock) — host-local, never synced",
	".vp-locks/",
	"# Transient filesystem probes (check.CheckVaultFilesystem) — host-local, never synced",
	".vp-fs-probe-*",
}

// NOTE: the vault deliberately does NOT ignore Projects/<slug>/commit.msg.
// The /wrap two-copy workflow keeps two copies: the project-root copy is
// host-local scratch (ignored by CanonicalProjectGitignorePatterns) that
// `git commit -F commit.msg` consumes once and never commits, while the
// vault copy is the canonical, COMMITTED mirror of the LATEST commit message —
// each wrap overwrites and commits it. The single-overwrite contract holds:
// `commit.msg` always reflects the most recent message, no more. The PERMANENT
// history is a separate append-log, Projects/<slug>/commit-log.md, to which
// every landed commit's full message is appended at wrap (see commit_log.go);
// `git log -p Projects/<slug>/commit-log.md` recovers every message ever
// archived. An earlier change (a0477e4) wrongly added a depth-agnostic
// `commit.msg` rule here, ignoring the vault archive too; that was reverted.

// CanonicalProjectGitignorePatterns is the set of .gitignore lines that
// vibe-palace owns in a *consuming project's* repository root. These are
// the host-local AI artifacts vp writes into the project tree (CLAUDE.md,
// AGENTS.md, commit.msg, .claude/, .grok/, .vibe-palace/) and must never
// be committed. The reconciler treats every entry as an exact-line presence
// requirement: missing lines are appended at EOF in declaration order;
// already-present lines are left alone (including their position).
//
// This set is deliberately narrow: it covers ONLY vp-owned artifacts.
// Build, coverage, or binary entries (/vp, /build/, /coverage.*) are the
// consuming project's concern and are intentionally NOT managed here.
var CanonicalProjectGitignorePatterns = []string{
	"/CLAUDE.md",
	"/AGENTS.md",
	"/commit.msg",
	"/.claude/",
	"/.grok/",
	"/.vibe-palace/",
}

// ReconcileVaultGitignore ensures every pattern in
// CanonicalGitignorePatterns is present in <vaultRoot>/.gitignore as an
// exact line, creating the file if it is absent. Existing content —
// comments, blank lines, custom patterns, ordering — is preserved
// verbatim. Missing canonical lines append at EOF in declaration order.
// Calling twice on the same vault produces byte-identical files.
//
// Unlike ReconcileProjectGitignore's raw temp+rename (reconcileGitignore,
// below) — a non-vault path, correctly outside ADR-003's lock funnel — this
// acquires the .gitignore path's vaultlock ONCE, reads absent-tolerantly
// inside it, and writes with atomicfile.Write. It never calls
// TopUpVaultGitignore / LockedUpdate / lockedWrite: those would either error
// on a missing file (LockedUpdate has no absent-tolerant branch) or
// re-acquire this same path's lock while it is already held, which is a
// PERMANENT HANG, not an error (ADR-003, "RMW callers must not
// double-acquire" — vaultlock.Acquire is a blocking flock with no timeout).
//
// The unconditional write (never skipped, even when nothing was missing)
// matters for the case where two callers both observe the file absent at
// plan time: the second to actually acquire the lock must still succeed as
// a normalizing no-op against whatever the first caller (or some other
// writer) left behind, never assume the file is still absent.
func ReconcileVaultGitignore(vaultRoot string) error {
	path := filepath.Join(vaultRoot, ".gitignore")

	release, err := vaultlock.Acquire(vaultRoot, path)
	if err != nil {
		return fmt.Errorf("storage: lock %s: %w", path, err)
	}
	defer release()

	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read gitignore %s: %w", path, err)
	}

	out, _ := appendMissingGitignoreLines(existing, CanonicalGitignorePatterns)
	return atomicfile.Write(vaultRoot, path, out)
}

// ReconcileProjectGitignore ensures every pattern in
// CanonicalProjectGitignorePatterns is present in
// <projectRoot>/.gitignore as an exact line. It mirrors
// ReconcileVaultGitignore — existing content is preserved verbatim and
// missing canonical lines append at EOF in declaration order — but is
// strictly append-only and idempotent: when every canonical line is
// already present the file is NOT touched at all (no write, no mtime
// change). Callers treat any error as non-fatal and log it.
func ReconcileProjectGitignore(projectRoot string) error {
	// skipWhenComplete=true: a no-op run must not churn the file's mtime
	// or rewrite user content that already satisfies the canonical set.
	return reconcileGitignore(filepath.Join(projectRoot, ".gitignore"),
		CanonicalProjectGitignorePatterns, true)
}

// TopUpVaultGitignore appends every CanonicalGitignorePatterns line missing
// from an EXISTING <vaultRoot>/.gitignore, preserving the file's content and
// order verbatim, and returns how many lines it added. When nothing is missing
// the file is not touched at all.
//
// Unlike ReconcileVaultGitignore — which creates the file and always rewrites
// it — this is a genuine read-modify-write of a shared, git-tracked vault file,
// so it runs inside LockedUpdate: the missing set is computed from bytes read
// under the per-path lock, and no other LOCKED writer of the file can land an
// edit between that read and the write (ADR-003). ReconcileVaultGitignore also
// takes the lock now (vault-gitignore-create-bypasses-the-vault-lock), via its
// own absent-tolerant single acquisition rather than this function's
// LockedUpdate — LockedUpdate has no absent-tolerant branch (a missing file is
// an error here, not a create; the Vault reconciler plans a Create for that
// case, which is exactly the case ReconcileVaultGitignore's own acquisition
// handles). The two never run nested: a single VaultReconciler.Apply action is
// exactly one of Create or Update for a given target, never both.
func TopUpVaultGitignore(vaultRoot string) (int, error) {
	path := filepath.Join(vaultRoot, ".gitignore")
	added := 0
	err := LockedUpdate(vaultRoot, path, func(current []byte) ([]byte, error) {
		out, n := appendMissingGitignoreLines(current, CanonicalGitignorePatterns)
		if n == 0 {
			return nil, nil
		}
		added = n
		return out, nil
	})
	if err != nil {
		return 0, err
	}
	return added, nil
}

// MissingVaultGitignorePatterns returns the canonical vault patterns that are
// absent from <vaultRoot>/.gitignore, in declaration order. A missing file
// yields the full canonical set. It is read-only and underpins the Vault
// reconciler's top-up plan.
func MissingVaultGitignorePatterns(vaultRoot string) ([]string, error) {
	return missingGitignorePatterns(filepath.Join(vaultRoot, ".gitignore"), CanonicalGitignorePatterns)
}

// MissingProjectGitignorePatterns returns the canonical project-root
// patterns that are absent from <projectRoot>/.gitignore, in declaration
// order. A missing file yields the full canonical set. It is read-only
// and underpins the advisory `vp check` row.
func MissingProjectGitignorePatterns(projectRoot string) ([]string, error) {
	return missingGitignorePatterns(filepath.Join(projectRoot, ".gitignore"), CanonicalProjectGitignorePatterns)
}

// missingGitignorePatterns is the shared read-only core of the two Missing*
// helpers: the canonical lines absent from path, in declaration order.
func missingGitignorePatterns(path string, canonical []string) ([]string, error) {
	present, err := gitignorePresentLines(path)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, p := range canonical {
		if _, ok := present[p]; !ok {
			missing = append(missing, p)
		}
	}
	return missing, nil
}

// gitignorePresentLines reads path and returns the set of exact lines it
// contains. A nonexistent file yields an empty set with no error.
func gitignorePresentLines(path string) (map[string]struct{}, error) {
	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read gitignore %s: %w", path, err)
	}
	// Strip the final newline (if any) before splitting so an empty file
	// becomes an empty set rather than a single empty-string line.
	trimmed := bytes.TrimRight(existing, "\n")
	present := make(map[string]struct{})
	if len(trimmed) > 0 {
		for l := range strings.SplitSeq(string(trimmed), "\n") {
			present[l] = struct{}{}
		}
	}
	return present, nil
}

// appendMissingGitignoreLines returns existing with every canonical line it
// lacks appended at EOF in declaration order, normalized to exactly one
// trailing newline, plus how many lines were appended. Existing lines —
// comments, blanks, custom patterns, their order — are kept verbatim.
func appendMissingGitignoreLines(existing []byte, canonical []string) ([]byte, int) {
	// Split into lines without losing empty trailing lines. We strip the
	// final newline (if any) before splitting so an empty file becomes
	// []string{} rather than []string{""}.
	trimmed := bytes.TrimRight(existing, "\n")
	var lines []string
	if len(trimmed) > 0 {
		lines = strings.Split(string(trimmed), "\n")
	}

	present := make(map[string]struct{}, len(lines))
	for _, l := range lines {
		present[l] = struct{}{}
	}

	added := 0
	for _, p := range canonical {
		if _, ok := present[p]; ok {
			continue
		}
		lines = append(lines, p)
		present[p] = struct{}{}
		added++
	}
	return []byte(strings.Join(lines, "\n") + "\n"), added
}

// reconcileGitignore is ReconcileProjectGitignore's sole mechanism (a
// project-root .gitignore is not a vault path, so it correctly stays outside
// ADR-003's lock funnel — mirroring the ADR's own carve-out for
// applyUpgrade's host-local branch). ReconcileVaultGitignore no longer calls
// this: it has its own small, locked implementation (see its doc comment)
// since vault-gitignore-create-bypasses-the-vault-lock. It ensures every
// pattern in canonical is present in path as an exact line, preserving
// existing content verbatim and appending missing canonical lines at EOF in
// declaration order. The file is written atomically (sibling tmp-file +
// rename, 0o644, exactly one trailing newline).
//
// When skipWhenComplete is true and no canonical line was missing, the
// function returns without touching the file at all — no write, no mtime
// change. When false, the file is always (re)written, normalizing the
// trailing newline even on a no-additions run.
func reconcileGitignore(path string, canonical []string, skipWhenComplete bool) error {
	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read gitignore %s: %w", path, err)
	}

	out, added := appendMissingGitignoreLines(existing, canonical)
	if skipWhenComplete && added == 0 {
		return nil
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gitignore.*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp gitignore in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write tmp gitignore: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp gitignore: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod tmp gitignore: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename tmp gitignore to %s: %w", path, err)
	}
	return nil
}

// GitAvailable returns true if git is found in PATH.
func GitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// SafeGitEnv returns the process environment with every repo-local git
// variable (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, ...) stripped, plus extra.
// Every git subprocess this package spawns against a vault or a project repo
// must build cmd.Env from this, not os.Environ() directly: git gives those
// variables precedence over cmd.Dir, so a process that inherits one (spawned
// from a git hook, or from a shell exporting GIT_DIR) would run against
// whatever repository the variable names instead of the directory cmd.Dir
// says.
//
// This forwards to internal/gitenv, which holds the actual list and strip
// logic. It moved out of this package (project-git-runners-inherit-git-dir-from-the-environment)
// because internal/storage imports internal/project and internal/wrapstate —
// so either of those importing storage back for this one helper would be an
// import cycle. gitenv has no internal dependencies, so every package that
// spawns git can reach it. This wrapper's signature and every existing caller
// are unchanged by the move.
func SafeGitEnv(extra ...string) []string {
	return gitenv.SafeGitEnv(extra...)
}

// GitIsRepo returns true if dir contains a .git directory.
func GitIsRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

// VaultGit classifies how git can see a vault. It is the predicate for the
// Templates prune, whose fail-safe side is "treat this as a git vault": the
// prune verifies against HEAD and commits on a git vault, and must never
// mistake one for an unversioned vault and leave an uncommitted deletion.
type VaultGit int

const (
	// VaultNotGit: no .git entry at the vault or any directory above it.
	VaultNotGit VaultGit = iota
	// VaultGitUnavailable: a .git entry exists but git is not on PATH, so
	// nothing committed can be read.
	VaultGitUnavailable
	// VaultGitBroken: a .git entry exists but git cannot use the repository
	// (a dangling .git file, "dubious ownership", a corrupt repository).
	VaultGitBroken
	// VaultGitOK: the vault is the top level of its own work tree git can
	// read — an ordinary clone, a linked worktree or a submodule (a .git
	// FILE at the vault root).
	VaultGitOK
	// VaultGitNested: the vault is a subdirectory of another repository's
	// work tree (a project or dotfiles repo). That repository is not the
	// vault's: vp may read it and restore a vault path from its HEAD, but
	// must never fetch, rebase, stage into, commit or push it.
	VaultGitNested
)

// InspectVaultGit reports how git sees vault. It looks for a .git entry of
// either kind at the vault and every directory above it, so a nested vault is
// not mistaken for an unversioned one, then asks git itself — including
// whether the work tree's top level is the vault (VaultGitOK) or an enclosing
// repository's (VaultGitNested). The error is git's, for VaultGitBroken.
func InspectVaultGit(vault string) (VaultGit, error) {
	abs, err := filepath.Abs(vault)
	if err != nil {
		return VaultGitBroken, err
	}
	marker := false
	for dir := abs; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			marker = true
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if !marker {
		return VaultNotGit, nil
	}
	if !GitAvailable() {
		return VaultGitUnavailable, nil
	}
	out, err := gitCmd(abs, 10*time.Second, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return VaultGitBroken, err
	}
	if out != "true" {
		return VaultGitBroken, fmt.Errorf("git says the vault is not inside a work tree (rev-parse: %q)", out)
	}
	top, err := gitCmd(abs, 10*time.Second, "rev-parse", "--show-toplevel")
	if err != nil {
		return VaultGitBroken, err
	}
	if !samePath(top, abs) {
		return VaultGitNested, nil
	}
	return VaultGitOK, nil
}

// samePath reports whether a and b name the same directory, resolving
// symlinks (git reports the resolved top level; a vault path may not be).
func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// RefuseIfNestedVaultGit refuses an operation that would stage into, commit,
// merge/pull, or push vaultPath's repository, when the vault is nested inside
// another repository's work tree (VaultGitNested) — that repository is not
// the vault's own. verb names the operation in the message ("stage", "commit",
// "pull", "push", "sync"). Any other VaultGit state (including a broken or
// unavailable repository, or InspectVaultGit's own error) returns nil: this
// guard's only job is the confident VaultGitNested case, and the caller's own
// git commands already surface every other failure on their own terms.
//
// Deliberately NOT gated here: a read-only `git fetch` that only refreshes
// local remote-tracking refs (no merge, no working-tree change) — the shape
// `vp vault status` performs by default via GetRemoteStatus/BuildStatusReport
// (vaultstatus.go). That fetch never routes through this helper: it is bounded
// to ref updates only, never a merge, stage, commit, or push, and is not the
// harm a nested vault's enclosing repository needs protecting from. Do not
// describe this helper as covering "fetch" in any call site's comments or
// errors.
func RefuseIfNestedVaultGit(vaultPath, verb string) error {
	state, err := InspectVaultGit(vaultPath)
	if err != nil || state != VaultGitNested {
		return nil
	}
	top, _ := GitTopLevel(vaultPath)
	if top == "" {
		top = "an enclosing repository"
	}
	return fmt.Errorf("refusing to %s: the vault is inside another repository (%s), and vp never stages into, commits, merges/pulls, or pushes a repository that is not the vault's own", verb, top)
}

// RefuseIfGitDisabled is the one refusal for git_enabled = false, and the only
// function in this package that reads the setting. Every vault git entry point
// calls it BEFORE ITS FIRST GIT PROCESS — for all but one that is the first
// statement; each command and MCP handler also calls it where the CLI's old
// gitEnabledGuard stood, so dry runs and remote discovery refuse too. Class-2
// callers (the task write, the memory harvest) call it before their dirty
// probe and map the refusal to "skipped".
//
// THE ONE ENTRY POINT THAT DOES NOT GATE ON ITS FIRST STATEMENT is
// RetiredTemplatesLock (vaultsync_verify.go), which reads a path and a file —
// neither is git — and gates immediately before the index lookup. The reason
// is written there: a vault with no retired lock is the normal case, and a
// gate ahead of the read would report a skip row for a file that is not there.
//
// READ-ONLY PROBES STILL RUN, AND SOME RUN BEFORE A GATE. They spawn git and
// write nothing, which is the line CheckGit's text states. Named so a reader
// tracing "no git process before the refusal" is not surprised by them:
//
//   - `vp config sync`'s planning, before the prune's gate: the is-inside-
//     work-tree and --show-toplevel checks, and UncommittedRemovals'
//     `ls-files -v --deleted` (vaultsync_verify.go);
//   - template reset's ReadCommittedBlob and tracked lookups, which compute
//     whether the reset would commit at all;
//   - requireCleanVaultTree's GitAvailable / GitIsRepo (cmd/vp);
//   - the session-start instruments: the dirt scan, the fetch-age probe, the
//     wrap dirt probe and vp_manage_task move's rev-parse HEAD.
//
// It returns a caller-class error wrapping ErrGitDisabled when the host config
// disables git, or ErrGitConfigUnreadable when git_enabled cannot be read (fail
// closed). The message names the config path and deliberately carries no
// remedy: git_enabled = false is the operator's instruction, and an agent told
// to "set git_enabled = true" would edit the operator's config to make the
// error go away. verb names the operation ("pull", "commit the template
// reset"); vaultPath is named for the reader of a multi-vault host.
func RefuseIfGitDisabled(vaultPath, verb string) error {
	enabled, err := HostGitEnabled()
	if err != nil {
		return apperr.Caller(fmt.Errorf("refusing to %s (vault %s): %w", verb, vaultPath, err))
	}
	if enabled {
		return nil
	}
	cfgPath, _ := VaultConfigFilePath()
	return apperr.Caller(fmt.Errorf("refusing to %s (vault %s): %w (config: %s)", verb, vaultPath, ErrGitDisabled, cfgPath))
}

// GitInit runs git init in the given directory.
func GitInit(dir string) error {
	cmd := exec.Command("git", "init", dir)
	cmd.Env = SafeGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init %s: %s: %w", dir, out, err)
	}
	return nil
}

// GitStatusClean reports whether the git working tree rooted at dir has NO
// changes at all — no staged, unstaged, or untracked files. It runs
// `git status --porcelain`; any non-empty output means the tree is dirty. This
// is the refuse-on-dirty precondition for the KG migration: a clean tree makes
// `git checkout .` a guaranteed, complete rollback of the rename.
//
// A host config with git_enabled = false (or an unreadable one) refuses: the
// migrations that call this use git as their rollback.
func GitStatusClean(dir string) (bool, error) {
	if err := RefuseIfGitDisabled(dir, "check the vault's git status"); err != nil {
		return false, err
	}
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	cmd.Env = SafeGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status %s: %s: %w", dir, bytes.TrimSpace(out), err)
	}
	return len(bytes.TrimSpace(out)) == 0, nil
}

// GitAdd stages the given pathspecs in the repo at dir (`git add -- <paths>`).
// Ignored paths are silently skipped by git; use GitAddForce to stage a path
// that a .gitignore rule would otherwise exclude.
func GitAdd(dir string, paths ...string) error {
	if err := RefuseIfGitDisabled(dir, "stage"); err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"-C", dir, "add", "--"}, paths...)
	cmd := exec.Command("git", args...)
	cmd.Env = SafeGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add %v in %s: %s: %w", paths, dir, bytes.TrimSpace(out), err)
	}
	return nil
}

// GitAddForce stages pathspecs with -f so a gitignored path is added anyway
// (`git add -f -- <paths>`). It is how the migration tracks the data-format
// stamp `.vibe-palace/vault.toml`, which lives under the otherwise-ignored
// `.vibe-palace/` dir: once tracked, the stamp syncs to every other host with
// the renamed data instead of being left behind (the gitignored-stamp bug).
func GitAddForce(dir string, paths ...string) error {
	if err := RefuseIfGitDisabled(dir, "stage"); err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"-C", dir, "add", "-f", "--"}, paths...)
	cmd := exec.Command("git", args...)
	cmd.Env = SafeGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add -f %v in %s: %s: %w", paths, dir, bytes.TrimSpace(out), err)
	}
	return nil
}

// GitCommitAllAt stages EVERY currently dirty path at root (`git add -A`) and
// commits with both GIT_AUTHOR_DATE and GIT_COMMITTER_DATE set to at, via
// SafeGitEnv. No --date flag is used: both env vars are sufficient, and this
// keeps the call shape identical to every other git subprocess in this file.
//
// 🔴 `git add -A` STAGES THE WHOLE REPOSITORY ROOTED AT dir, NOT A SCOPED
// SUBTREE. That is safe ONLY where dir IS the entire repository on its own —
// an ephemeral test harness vault (testinfra.TestHarness.Vault.Root), never
// the real, multi-project vault, where an indiscriminate `git add -A` could
// bundle another project's or another session's in-flight, unrelated changes
// into this commit. Callers outside a throwaway, single-purpose repository
// must not reach for this helper.
//
// at should carry a real time-of-day, not just a date: a caller that only
// cares about the CALENDAR DAY (storage.CalendarDay's own YYYY-MM-DD grain,
// which is what this project's git-log-based date derivation ultimately
// reads back via `--date=format:%Y-%m-%d`) should pick a time comfortably
// inside the intended day (e.g. noon UTC) to avoid a near-midnight value
// landing on the adjacent day once formatted.
//
// Returns the underlying git error verbatim on failure, including the
// ordinary "nothing to commit, working tree clean" case — a caller invoking
// this with nothing dirty almost always has a fixture bug, and surfacing the
// real git message is more useful than silently treating it as a no-op.
func GitCommitAllAt(root, message string, at time.Time) error {
	addCmd := exec.Command("git", "-C", root, "add", "-A")
	addCmd.Env = SafeGitEnv()
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add -A: %s: %w", bytes.TrimSpace(out), err)
	}

	dateStr := at.Format(time.RFC3339)
	commitCmd := exec.Command("git", "-C", root, "commit", "-m", message)
	commitCmd.Env = SafeGitEnv("GIT_AUTHOR_DATE="+dateStr, "GIT_COMMITTER_DATE="+dateStr)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %w", bytes.TrimSpace(out), err)
	}
	return nil
}
