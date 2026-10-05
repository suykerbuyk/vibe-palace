// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
)

// SyncResult reports a full vault sync run (classify → refuse-on-dirt → commit
// artifacts locally → pull → push). Front-ends print from it; it is always
// non-nil on return so callers can render partial progress on error.
type SyncResult struct {
	Swept               []string
	Reported            []string
	ReportedUserContent []string
	Deferred            []string
	// LeftDeparted are artifacts under a departed project's trees, left
	// untouched and never committed (TidyResult.LeftDeparted).
	LeftDeparted []string
	GenuineDirt  []string // Reported minus ReportedUserContent — the set that BLOCKS a sync
	Refused      bool     // true iff GenuineDirt blocked the sync BEFORE any network I/O
	Committed    bool
	CommitSHA    string
	Pull         *PullResult
	Push         *PlainPushResult
}

// genuineDirt returns the set difference reported \ userContent — the reported
// dirt that is NOT deliberately-pending user memory. ReportedUserContent is a
// classified subset of Reported (see TidyResult), so this filter yields exactly
// the genuinely-unexpected paths that must get human eyes before a sync. Memory
// paths (Projects/<slug>/memory/...) are expected pending content and never
// block (decision 7); freshly-swept capture artifacts are not in `reported` at
// all, so they cannot appear here either.
func genuineDirt(reported, userContent []string) []string {
	if len(reported) == 0 {
		return nil
	}
	pending := make(map[string]bool, len(userContent))
	for _, p := range userContent {
		pending[p] = true
	}
	var dirt []string
	for _, p := range reported {
		if !pending[p] {
			dirt = append(dirt, p)
		}
	}
	return dirt
}

// GenuineDirt is the exported reading of the same set difference, taken from a
// TidyResult.
//
// 🔴 IT EXISTS SO THE BOOTSTRAP DIRT ALERT AND THIS FILE'S REFUSAL GATE CANNOT
// DISAGREE. The alert's whole claim is "a sync would refuse right now"; a second
// hand-rolled `Reported minus memory` filter in internal/tools would make that
// claim by coincidence rather than by construction, and would drift the first
// time either side's classification changed. internal/wrapstate/gitprobe.go is
// the standing evidence that this happens — it is a second, project-scoped dirt
// classifier that does not use sweepRules and disagrees with this one.
//
// The receiver is a *TidyResult rather than the two slices because that is the
// only shape a caller outside this package can obtain, and it makes the subset
// relationship (ReportedUserContent ⊂ Reported) impossible to get wrong.
func (r *TidyResult) GenuineDirt() []string {
	if r == nil {
		return nil
	}
	return genuineDirt(r.Reported, r.ReportedUserContent)
}

// refuseSyncOnDirt is SyncVault's refuse-on-dirt gate (step 2), the one
// definition that both the real sync and its dry run (SyncPreview) apply, so
// the two cannot order or word it differently.
func refuseSyncOnDirt(vaultPath string, scan *TidyResult) error {
	dirt := scan.GenuineDirt()
	if len(dirt) == 0 {
		return nil
	}
	msg := fmt.Sprintf("refusing to sync: %d uncommitted non-artifact file(s) need review: %s", len(dirt), strings.Join(dirt, ", "))
	// A stray under a departed project's trees is never committed here (the
	// departure record wins), so reviewing it can only end one way.
	var departed []string
	for _, rel := range dirt {
		if s, ok := departedpath.DepartedTree(vaultPath, rel); ok {
			departed = append(departed, fmt.Sprintf("%s (project %s departed; remove it — if it matters, carry it to the vault %s lives in now first)", rel, s, s))
		}
	}
	if len(departed) > 0 {
		msg += ". Under a departed project, nothing is ever committed in this vault: " + strings.Join(departed, "; ")
	}
	return errors.New(msg)
}

// SyncPreview is a sync DRY RUN's verdict on the working tree: SyncVault's
// steps before any network I/O, in SyncVault's order, without committing.
//
//  1. classify (TidyScan);
//  2. refuse on genuine dirt — refuseSyncOnDirt, with refused=true, which a
//     front-end maps exactly as it maps SyncResult.Refused;
//  3. the U1 commit guard the step-3 tidy commit would meet (previewCommitGuard).
//
// Step 3 is unreachable today: a pending departure record is itself genuine dirt
// (only Audits/*.md and Audits/baseline.json are artifacts), so step 2 refuses
// first. It is kept so a change to that classification cannot open a gap
// between the preview and the real sync. The incoming-departure preflight
// (step 2b) needs a fetch, so a preview does not run it.
//
// A scan failure returns a nil scan.
func SyncPreview(vaultPath string) (scan *TidyResult, refused bool, err error) {
	scan, err = TidyScan(vaultPath)
	if err != nil {
		return nil, false, err
	}
	if err := refuseSyncOnDirt(vaultPath, scan); err != nil {
		return scan, true, err
	}
	return scan, false, previewCommitGuard(vaultPath, scan)
}

// SyncVault runs a full vault sync: classify the working tree, refuse up front
// if it carries genuine (non-artifact, non-memory) dirt, commit the sweepable
// capture artifacts LOCALLY, pull each remote, re-assert the tree is clean, and
// only then push. The returned *SyncResult is always non-nil so a front-end can
// render partial progress even when the error is non-nil.
//
// The order and gates below are correctness-critical:
//
//   - Refuse-on-dirt happens BEFORE any network I/O (finding L1): a tree with
//     uncommitted non-artifact work must get human eyes before we entangle it
//     with a merge. Memory is NOT dirt (decision 7) and never blocks.
//
//   - The pull gate is on BOTH the Go error and the VERDICT (FINDING A). The
//     error is pullCore's pre-flight refusal (a pending lifecycle commit, a
//     nested git, a git operation already in progress): nothing was pulled,
//     and dropping it once let a sync go on to push with an empty verdict.
//     A merge failure — a conflict, which pullCore aborts, a refusal, a
//     killed merge — is recorded ONLY in PullResult.RemoteResults, so the
//     verdict gate is the one that sees it. DO NOT "simplify" either away.
//
//   - A post-merge re-assert (FINDING A) runs even after a clean pull verdict:
//     a merge that reports success can still leave residue (a half-applied
//     tree). Re-scan for genuine dirt AND probe
//     unmergedPaths; either one blocks the push. Freshly-written capture
//     artifacts (the background hook may write during the pull) are sweepable,
//     so they land in Swept on the re-scan and are absent from the re-scanned
//     GenuineDirt — they must not trip this gate, and the set math above
//     guarantees they don't.
//
// The git_enabled gate comes first and keeps the non-nil contract: both
// front-ends read res.Committed before err, so a refused sync returns an empty
// *SyncResult, never nil. Past the gate every inner step calls a core, so one
// sync reads the host config once.
func SyncVault(vaultPath string, remotes []string) (*SyncResult, error) {
	result := &SyncResult{}

	if err := RefuseIfGitDisabled(vaultPath, "sync"); err != nil {
		return result, err
	}
	if err := RefuseIfNestedVaultGit(vaultPath, "sync"); err != nil {
		return result, err
	}

	// 1. Classify the whole working tree (read-only; no commit, no network).
	// Return the (empty) result, never nil, so the non-nil contract holds even on
	// a scan failure — front-ends dereference the result before checking err.
	scan, err := TidyScan(vaultPath)
	if err != nil {
		return result, err
	}
	result.Swept = scan.Swept
	result.Reported = scan.Reported
	result.ReportedUserContent = scan.ReportedUserContent
	result.LeftDeparted = scan.LeftDeparted
	result.Deferred = scan.Deferred

	// 2. Refuse-on-dirt BEFORE any network I/O (finding L1). Genuine dirt is
	// reported paths that are not deliberately-pending user memory.
	result.GenuineDirt = scan.GenuineDirt()
	if err := refuseSyncOnDirt(vaultPath, scan); err != nil {
		result.Refused = true
		return result, err
	}

	// 2b. Departure pre-flight, BEFORE the tidy commit below. A departure
	// incoming onto this host's work under the departed slug must leave HEAD
	// exactly as it was, and step 3 would otherwise commit first. It costs one
	// extra fetch per remote (pullCore fetches again); a fetch failure here is
	// left for pullCore to record.
	branch := branchOrMain(vaultPath)
	for _, remote := range remotes {
		if _, err := gitCmd(vaultPath, 60*time.Second, "fetch", remote, branch); err != nil {
			continue
		}
		if err := guardIncomingDepartures(vaultPath, remote, branch); err != nil {
			result.Refused = true
			return result, err
		}
	}

	// 3. Commit the sweepable capture artifacts LOCALLY ONLY (push=false skips
	// all network). An empty swept set is a no-op — TidyVault does not commit and
	// leaves Committed=false; a lone deferred transcript half is fine here.
	if len(scan.Swept) > 0 {
		tidy, err := tidyVaultCore(vaultPath, false)
		if err != nil {
			return result, err
		}
		result.Committed = tidy.Committed
		result.CommitSHA = tidy.CommitSHA
	}

	// 4. Pull each remote. pullCore's error is a pre-flight refusal and stops
	// the sync; a per-remote failure lives in RemoteResults (FINDING A). Gate on
	// both — a non-empty verdict (a failed fetch/merge or an aborted conflict)
	// stops before we push.
	pull, pullErr := pullCore(vaultPath, remotes)
	result.Pull = pull
	if pullErr != nil {
		// A pre-flight refusal: nothing was pulled, so nothing to sweep.
		return result, pullErr
	}
	// Right after the merge and BEFORE the verdict gate below, so a sync that
	// merged a departure removes the moved project's embed cache even when a
	// later remote fails. The pass skips itself while a merge is unfinished.
	sweepDepartedAfterPull(vaultPath)
	if v := RemoteVerdict(OpPull, pull.RemoteResults, ""); v != "" {
		return result, errors.New(v)
	}

	// 5. Post-merge re-assert (FINDING A). Even a clean-verdict merge can leave
	// residue, so re-scan for genuine dirt AND probe for unmerged (conflict)
	// index entries before pushing. New capture artifacts written during the pull
	// are sweepable and land in Swept, not in this re-scanned GenuineDirt, so they
	// do not block; only genuine dirt or unmerged paths do.
	rescan, err := TidyScan(vaultPath)
	if err != nil {
		return result, err
	}
	postDirt := rescan.GenuineDirt()
	unmerged := unmergedPaths(vaultPath)
	if len(postDirt) > 0 || len(unmerged) > 0 {
		return result, fmt.Errorf(
			"refusing to push: tree is dirty or conflicted after merge (dirt: %s; unmerged: %s)",
			strings.Join(postDirt, ", "), strings.Join(unmerged, ", "))
	}

	// 6. Push the committed HEAD. Same verdict gate as the pull.
	push, _ := pushPlainCore(vaultPath, remotes)
	result.Push = push
	if v := RemoteVerdict(OpPush, push.RemoteResults, "HEAD"); v != "" {
		return result, errors.New(v)
	}

	return result, nil
}
