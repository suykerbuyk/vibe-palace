// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Git post-commit hook — reap the project-root commit.msg the commit consumed.
//
// THE HOLE THIS CLOSES. `/wrap` and `/stage` hand the operator
// `git commit -F commit.msg && rm commit.msg`. The `&& rm` is what consumes the
// message. An operator who omits it — muscle memory, an IDE commit, a copied
// older command — leaves the file on disk, and the NEXT `git commit -F` relands
// that message on different work. `vp_preflight_wrap` reports the leftover as
// `commit_msg_unconsumed`, but only at the next wrap; the residual is precisely
// two commits with no wrap between them. Nothing outside the git commit path can
// close that, and `vp` is not in the commit path. A hook is.
//
// THE PREDICATE IS PROOF OF CONSUMPTION, NOT A TIMER OR A STAMP.
// After a successful commit, if stripspace(HEAD %B) equals
// stripspace(<repo>/commit.msg), the file is what the commit just consumed and
// is deleted. Anything else is left alone — an unrelated `git commit -m "typo"`
// does not match, so it cannot destroy a message the operator authored and has
// not committed yet.
//
// STRIPSPACE ON BOTH SIDES IS LOAD-BEARING, NOT HYGIENE. `git commit -F` applies
// `--cleanup=whitespace`, which strips trailing whitespace from every line and
// collapses consecutive internal blank lines. Measured against a real repo: a
// commit.msg carrying trailing spaces and a double blank line compares UNEQUAL
// raw and EQUAL under stripspace. A raw `==` therefore silently never fires on
// this project's multi-paragraph messages — it is a no-op that looks installed.
// TestPostCommitHookMutationRawCompareStopsTheReap pins that.
//
// WHY post-commit AND NOT prepare-commit-msg. `prepare-commit-msg` runs BEFORE
// the commit exists, so it has no freshness signal: its `$2` reads `message` for
// both `-F file` and `-m "text"`, and matching `$1` against commit.msg only
// proves the message came from that file — which is the INTENDED first use.
// Telling the correct first use from the stale second needs the file to be gone
// after the first, which is what this hook does. Decisively: git IGNORES
// post-commit's exit status (re-derived live — a hook exiting 1 left
// `git commit` at 0), so a failing reap can never block, fail, or slow a commit.
// prepare-commit-msg and commit-msg can both refuse. That asymmetry is the whole
// argument for this hook being safe to install automatically.
//
// COVERAGE, MEASURED — scoped to the `git commit -F` path this exists for:
// `git commit` fires it, `git commit --amend` fires it, a `git rebase` replaying
// a commit fires it. `git cherry-pick` and `git merge --no-ff` do NOT. State the
// rebase case rather than let it be discovered: a rebase replaying a commit
// whose message still matches a leftover commit.msg WILL reap that file. That is
// defensible — the message did land — but it is behaviour, not an accident.
//
// THE FILE IS ALREADY GITIGNORED. `/commit.msg` sits in
// CanonicalProjectGitignorePatterns (this package, git.go), so a reap can never
// dirty the working tree or surprise a later `git status`.
//
// PRIOR ART, AND WHAT IS DIFFERENT THIS TIME. doc/TESTING.md records, under
// "Surface Merge Driver (REMOVED, iter 174)", that the `vp-surface` git merge
// driver AND its auto-installer were deleted — same shape as this: vp writing
// into git's per-repo plumbing, auto-installed per clone, with an opt-out. It is
// still generating maintenance: `surface-merge-driver` remains a live check whose
// only job is finding its leftovers. The difference is the referent, not the
// shape. That driver died because the conflict class it served died — the
// byte-stable stamp stopped producing conflicts. This hook's class dies only if
// the PROCEDURE dies, and a procedure the operator must remember is exactly what
// this exists to replace. What is deliberately NOT copied is "opt-out and hope":
// there is no opt-out flag, no config key, and no silent write into a hooks
// directory this repo does not own (see the core.hooksPath refusal below).
//
// THIS IS A GIT HOOK. It is unrelated to `vp hook` / initHookWiring, which mean
// AI-host session hooks (SessionEnd/Stop/PreCompact) and write ~/.claude
// settings. The vocabularies must not be mixed.
//
// SELF-CONTAINED sh + git, NEVER `vp`. A hook that shells out to `vp` couples
// every commit in the repo to PATH and to a current binary, and fails on exactly
// the IDE and GUI commits that skip wrap in the first place.

package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PostCommitHookMarker identifies a post-commit hook this package wrote. It is
// the ONLY thing that distinguishes "ours, safe to rewrite" from "somebody
// else's, refuse" — an installer that decides by content equality would clobber
// a hand-edited copy and would refuse to upgrade its own.
const PostCommitHookMarker = "vibe-palace:post-commit-reap"

// PostCommitHookScript is the hook body, verbatim.
//
// Every failure path exits 0. That is belt-and-suspenders — git ignores this
// hook's exit status either way — but it keeps the script honest if it is ever
// run by hand.
const PostCommitHookScript = `#!/bin/sh
# ` + PostCommitHookMarker + ` v1
#
# Written by vibe-palace (` + "`vp init`" + `). Delete this file to disable it;
# ` + "`vp check`" + ` will then report the hook as missing.
#
# Reap the project-root commit.msg once the commit that consumed it has landed,
# so a ` + "`git commit -F commit.msg`" + ` typed WITHOUT the trailing
# ` + "`&& rm commit.msg`" + ` cannot reland this message on the next commit.
#
# The message is deleted only when it PROVABLY fed this commit: stripspace of
# HEAD's message must equal stripspace of the file. An unrelated commit does not
# match, so an unsent message is never destroyed. stripspace is required on both
# sides because ` + "`git commit -F`" + ` normalizes trailing whitespace and
# collapses consecutive blank lines; a raw comparison would never fire.
#
# git ignores this hook's exit status, so nothing here can block a commit.

root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
[ -n "$root" ] || exit 0

msg="$root/commit.msg"
[ -s "$msg" ] || exit 0

head=$(git log -1 --format=%B 2>/dev/null | git stripspace)
file=$(git stripspace <"$msg" 2>/dev/null)

# An empty normalized file cannot prove anything; refuse rather than match a
# degenerate case.
[ -n "$file" ] || exit 0

if [ "$head" = "$file" ]; then
	rm -f -- "$msg"
fi

exit 0
`

// HookStatus is the outcome of inspecting or installing the post-commit hook.
type HookStatus int

const (
	// HookInstalled — this call wrote the hook. Install only.
	HookInstalled HookStatus = iota
	// HookCurrent — ours, already byte-identical. Nothing to do.
	HookCurrent
	// HookMissing — no post-commit hook at all. Inspect only.
	HookMissing
	// HookStale — ours (marker present) but the body differs. Inspect only;
	// Install rewrites it and reports HookInstalled.
	HookStale
	// HookForeign — a post-commit hook exists that this package did not write.
	// REFUSED, never clobbered, never appended to.
	HookForeign
	// HookSharedHooksPath — core.hooksPath is set. REFUSED, never written.
	HookSharedHooksPath
	// HookNoRepo — not a git repo, or git is unavailable.
	HookNoRepo
	// HookError — the hook could not be read or written (permissions, a
	// read-only .git, a full disk). Deliberately NOT folded into HookForeign:
	// "somebody else's hook is here" and "I could not write" are different
	// facts, and a check row that reports the second as the first sends the
	// operator looking for a hook that does not exist.
	HookError
)

// HookReport is what a caller renders. Detail is one human-readable line; it is
// never empty.
type HookReport struct {
	Status HookStatus
	// Path is the resolved post-commit path, or "" when it could not be
	// resolved (HookNoRepo, HookSharedHooksPath).
	Path   string
	Detail string
}

// InspectPostCommitHook reports the hook state of the repo containing
// projectRoot without writing anything. It never returns HookInstalled.
func InspectPostCommitHook(projectRoot string) HookReport {
	path, rep, ok := resolvePostCommitHookPath(projectRoot)
	if !ok {
		return rep
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return HookReport{Status: HookMissing, Path: path,
				Detail: "no post-commit hook — a `git commit -F commit.msg` without `&& rm` will leave the message unconsumed"}
		}
		return HookReport{Status: HookError, Path: path,
			Detail: fmt.Sprintf("cannot read %s: %v", path, err)}
	}

	if !strings.Contains(string(data), PostCommitHookMarker) {
		return HookReport{Status: HookForeign, Path: path,
			Detail: fmt.Sprintf("a post-commit hook this project did not write is already installed at %s — refusing to touch it", path)}
	}
	if string(data) == PostCommitHookScript {
		return HookReport{Status: HookCurrent, Path: path,
			Detail: "commit.msg reaper installed"}
	}
	return HookReport{Status: HookStale, Path: path,
		Detail: fmt.Sprintf("the commit.msg reaper at %s is out of date", path)}
}

// InstallPostCommitHook writes the reaper into the repo containing projectRoot.
// It is idempotent, refuses a foreign hook, and refuses a repo with
// core.hooksPath set. It never returns HookMissing or HookStale — those are
// inspect-side states this call resolves.
//
// Every refusal is a report, not an error: this is called from `vp init`,
// `vp commands upgrade` and the wrap-time ingest, none of which may abort
// because a hook could not be installed.
func InstallPostCommitHook(projectRoot string) HookReport {
	cur := InspectPostCommitHook(projectRoot)
	switch cur.Status {
	case HookCurrent, HookForeign, HookSharedHooksPath, HookNoRepo, HookError:
		return cur
	}

	// HookMissing or HookStale — both are ours to write.
	if err := os.MkdirAll(filepath.Dir(cur.Path), 0o755); err != nil {
		return HookReport{Status: HookError, Path: cur.Path,
			Detail: fmt.Sprintf("cannot create hooks dir %s: %v", filepath.Dir(cur.Path), err)}
	}
	if err := writeExecutableHook(cur.Path, PostCommitHookScript); err != nil {
		return HookReport{Status: HookError, Path: cur.Path,
			Detail: fmt.Sprintf("cannot write %s: %v", cur.Path, err)}
	}
	return HookReport{Status: HookInstalled, Path: cur.Path,
		Detail: "commit.msg reaper installed"}
}

// resolvePostCommitHookPath resolves where the post-commit hook belongs for the
// repo containing projectRoot. ok=false means the report is terminal and the
// caller must return it unchanged.
//
// ORDER MATTERS. core.hooksPath is checked FIRST because
// `git rev-parse --git-path hooks` RESOLVES it: with core.hooksPath set to
// /tmp/shared-hooks, --git-path hooks answers /tmp/shared-hooks (measured). Ask
// git for the path first and the refusal never happens — vibe-palace would write
// its post-commit into a directory shared by every repo the operator owns, which
// is worse than a missing hook.
//
// Any core.hooksPath at all is refused, including one pointing inside the repo.
// A single rule beats a heuristic about which shared directories are "safe", and
// an in-repo hooks dir is typically tracked — writing there would commit this
// host-local hook into the project's history.
func resolvePostCommitHookPath(projectRoot string) (string, HookReport, bool) {
	if projectRoot == "" {
		return "", HookReport{Status: HookNoRepo, Detail: "no project root"}, false
	}

	// `git config --get` exits 1 when the key is unset; only a successful exit
	// with a non-empty value is a real hooksPath.
	out, err := exec.Command("git", "-C", projectRoot, "config", "--get", "core.hooksPath").Output()
	if err == nil {
		if hp := strings.TrimSpace(string(out)); hp != "" {
			return "", HookReport{Status: HookSharedHooksPath,
				Detail: fmt.Sprintf("core.hooksPath is set to %q — refusing to write a hook into a directory this repo does not own", hp)}, false
		}
	}

	// Resolve the repo root before asking for the hooks dir. --git-path answers
	// RELATIVE TO THE GIT PROCESS CWD, not the repo root: run from a subdirectory
	// it prints `../.git/hooks` (measured). Asking from the toplevel makes the
	// relative answer resolvable against the toplevel, which is the rule.
	top, err := exec.Command("git", "-C", projectRoot, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", HookReport{Status: HookNoRepo,
			Detail: fmt.Sprintf("%s is not a git repository — no hook to install", projectRoot)}, false
	}
	root := strings.TrimSpace(string(top))
	if root == "" {
		return "", HookReport{Status: HookNoRepo,
			Detail: fmt.Sprintf("%s is not a git repository — no hook to install", projectRoot)}, false
	}

	hooksOut, err := exec.Command("git", "-C", root, "rev-parse", "--git-path", "hooks").Output()
	if err != nil {
		return "", HookReport{Status: HookNoRepo,
			Detail: fmt.Sprintf("cannot resolve the hooks directory for %s: %v", root, err)}, false
	}
	hooks := strings.TrimSpace(string(hooksOut))
	if hooks == "" {
		return "", HookReport{Status: HookNoRepo,
			Detail: fmt.Sprintf("cannot resolve the hooks directory for %s", root)}, false
	}
	// A linked worktree answers with the MAIN repo's absolute hooks dir, which
	// is correct — worktrees share hooks, and a worktree commit with no
	// commit.msg beside it is simply a no-op reap.
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(root, hooks)
	}
	return filepath.Join(hooks, "post-commit"), HookReport{}, true
}

// writeExecutableHook writes body to path via a sibling tmp file + rename, mode
// 0o755. The chmod is explicit rather than relying on the O_CREATE mode so the
// hook is executable regardless of umask — a 0o644 post-commit is silently
// ignored by git, which is the failure mode this whole file exists to avoid.
func writeExecutableHook(path, body string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".post-commit.*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp hook in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write tmp hook: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp hook: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("chmod tmp hook: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename tmp hook to %s: %w", path, err)
	}
	return nil
}
