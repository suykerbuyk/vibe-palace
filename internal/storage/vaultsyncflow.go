// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"strings"
)

// SyncResult reports a full vault sync run (classify → refuse-on-dirt → commit
// artifacts locally → pull → push). Front-ends print from it; it is always
// non-nil on return so callers can render partial progress on error.
type SyncResult struct {
	Swept               []string
	Reported            []string
	ReportedUserContent []string
	Deferred            []string
	GenuineDirt         []string // Reported minus ReportedUserContent — the set that BLOCKS a sync
	Refused             bool     // true iff GenuineDirt blocked the sync BEFORE any network I/O
	Committed           bool
	CommitSHA           string
	Pull                *PullResult
	Push                *PlainPushResult
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
//   - The pull gate is on the VERDICT, not the Go error (FINDING A). Pull
//     returns err == nil UNCONDITIONALLY — a merge conflict is recorded ONLY in
//     PullResult.RemoteResults, and Pull leaves conflict markers plus unmerged
//     index entries in the tree with no cleanup. A bare `if err != nil` would
//     sail straight past a conflicted merge and push the broken tree. Gate on
//     RemoteVerdict, which reads RemoteResults. DO NOT "simplify" this into an
//     error check.
//
//   - A post-merge re-assert (FINDING A) runs even after a clean pull verdict:
//     a merge that reports success can still leave residue (an autostash-style
//     conflict, a half-applied tree). Re-scan for genuine dirt AND probe
//     unmergedPaths; either one blocks the push. Freshly-written capture
//     artifacts (the background hook may write during the pull) are sweepable,
//     so they land in Swept on the re-scan and are absent from the re-scanned
//     GenuineDirt — they must not trip this gate, and the set math above
//     guarantees they don't.
func SyncVault(vaultPath string, remotes []string) (*SyncResult, error) {
	result := &SyncResult{}

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
	result.Deferred = scan.Deferred

	// 2. Refuse-on-dirt BEFORE any network I/O (finding L1). Genuine dirt is
	// reported paths that are not deliberately-pending user memory.
	result.GenuineDirt = genuineDirt(scan.Reported, scan.ReportedUserContent)
	if len(result.GenuineDirt) > 0 {
		result.Refused = true
		return result, fmt.Errorf(
			"refusing to sync: %d uncommitted non-artifact file(s) need review: %s",
			len(result.GenuineDirt), strings.Join(result.GenuineDirt, ", "))
	}

	// 3. Commit the sweepable capture artifacts LOCALLY ONLY (push=false skips
	// all network). An empty swept set is a no-op — TidyVault does not commit and
	// leaves Committed=false; a lone deferred transcript half is fine here.
	if len(scan.Swept) > 0 {
		tidy, err := TidyVault(vaultPath, false)
		if err != nil {
			return result, err
		}
		result.Committed = tidy.Committed
		result.CommitSHA = tidy.CommitSHA
	}

	// 4. Pull each remote. Pull ALWAYS returns err == nil; the real verdict lives
	// in RemoteResults (FINDING A). Gate on RemoteVerdict — a non-empty verdict
	// (a failed fetch/merge or a conflict) aborts before we push over it.
	pull, _ := Pull(vaultPath, remotes)
	result.Pull = pull
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
	postDirt := genuineDirt(rescan.Reported, rescan.ReportedUserContent)
	unmerged := unmergedPaths(vaultPath)
	if len(postDirt) > 0 || len(unmerged) > 0 {
		return result, fmt.Errorf(
			"refusing to push: tree is dirty or conflicted after merge (dirt: %s; unmerged: %s)",
			strings.Join(postDirt, ", "), strings.Join(unmerged, ", "))
	}

	// 6. Push the committed HEAD. Same verdict gate as the pull.
	push, _ := PushPlain(vaultPath, remotes)
	result.Push = push
	if v := RemoteVerdict(OpPush, push.RemoteResults, "HEAD"); v != "" {
		return result, errors.New(v)
	}

	return result, nil
}
