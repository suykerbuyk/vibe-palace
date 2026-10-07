// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
)

// incomingIngestLauncher starts the pending-archive ingester after a pull or
// merge brings new transcript archives into the vault (ADR-014 decision 7). It
// is a package variable, nil by default, so a library build and every test
// spawns nothing and never relaunches the test binary; cmd/vp sets it to
// detachlaunch.Launch at startup via SetIncomingIngestLauncher.
var incomingIngestLauncher detachlaunch.LaunchFunc

// SetIncomingIngestLauncher installs the launcher used by spawnIngestForIncoming.
// cmd/vp calls it once at startup with detachlaunch.Launch; leaving it unset
// (the library/test default) means pull/merge paths spawn no ingester.
func SetIncomingIngestLauncher(fn detachlaunch.LaunchFunc) { incomingIngestLauncher = fn }

// headOrEmpty returns the vault's current HEAD sha, or "" on any failure. ""
// short-circuits spawnIngestForIncoming, so a repo with no commits, or a git
// error, simply triggers no ingest rather than failing the pull.
func headOrEmpty(vaultPath string) string {
	out, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// headForTrigger is headOrEmpty, but ONLY when the ingester trigger is armed (a
// launcher is installed, i.e. in cmd/vp). A library or test build installs no
// launcher, so it returns "" WITHOUT running git — spawnIngestForIncoming
// no-ops on an empty head, so every pull/merge path pays nothing (no extra
// `git rev-parse HEAD` per call) for a trigger that could never fire. The
// pull/merge sites use this rather than headOrEmpty for that reason.
func headForTrigger(vaultPath string) string {
	if incomingIngestLauncher == nil {
		return ""
	}
	return headOrEmpty(vaultPath)
}

// spawnIngestForIncoming starts ONE detached `vp drain archives` run when a pull
// or merge moved HEAD from before to after and brought in at least one
// transcript manifest (ADR-014 decision 7; task
// capture-and-backfill-write-host-local-index-only, Scope 1). It is the single
// trigger shared by every pull/merge path.
//
//   - ONE ingester per pull, never one per project: --project names the first
//     changed project in slug order, and the run serves every other project of
//     the vault (the ingester's own scope). The run lock makes any racing
//     sibling exit at once, so a redundant spawn costs nothing. It passes no
//     --first: a pull is not the host that created these archives.
//   - It does nothing when the launcher is unset, when HEAD did not move, when a
//     merge or rebase is still unfinished, or when no transcript manifest
//     changed between the two heads. The manifest diff is one `git diff
//     --name-only`, so a pull that brought no archive costs one git call, not a
//     walk of every project.
//   - A launch failure is a vp.log warning, never an error: the note/commit is
//     safe, and the `vp mcp` startup backstop or the next trigger ingests the
//     archive later.
func spawnIngestForIncoming(vaultPath, before, after string) {
	launch := incomingIngestLauncher
	if launch == nil || before == "" || after == "" || before == after {
		return
	}
	// An unfinished merge/rebase means the tree is mid-reconcile; do not act on a
	// half-applied HEAD. (Callers invoke this at their success exit, so this is a
	// belt-and-braces guard.)
	if op, err := operationInProgress(vaultPath); err != nil || op != "" {
		return
	}

	out, err := gitCmd(vaultPath, 30*time.Second, "-c", "core.quotePath=false", "diff", "--name-only", "-z", before, after)
	if err != nil {
		slog.Warn("ingest trigger: could not diff incoming commits (non-fatal)", "vault", vaultPath, "error", err)
		return
	}

	projects := map[string]struct{}{}
	for _, p := range strings.Split(out, "\x00") {
		if p == "" {
			continue
		}
		// Projects/<slug>/transcripts/<name>.manifest.json
		seg := strings.Split(p, "/")
		if len(seg) == 4 && seg[0] == "Projects" && seg[2] == "transcripts" && strings.HasSuffix(seg[3], ".manifest.json") {
			projects[seg[1]] = struct{}{}
		}
	}
	if len(projects) == 0 {
		return
	}
	slugs := make([]string, 0, len(projects))
	for s := range projects {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	first := slugs[0]

	args := []string{"drain", "archives", "--vault-root", vaultPath, "--project", first}
	logPath := filepath.Join(NewVault(vaultPath).VaultLocalDir(), "ingester.log")
	if _, lerr := launch("", args, logPath); lerr != nil {
		slog.Warn("ingest trigger: could not launch the archive ingester (non-fatal)",
			"vault", vaultPath, "project", first, "error", lerr)
	}
}
