// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"sort"
	"strings"
)

// RemoteOp names the operation a verdict describes. It exists so the two
// front-ends cannot word the same outcome differently — which is how they came
// to DISAGREE about whether a partial failure was a failure at all.
type RemoteOp string

const (
	OpPull RemoteOp = "pull"
	OpPush RemoteOp = "push"
)

// FailedRemotes returns the names of every remote whose operation failed, sorted.
//
// An EMPTY result means every configured remote succeeded — or that none were
// configured at all, which is a clean local-only degrade and NOT a failure. That
// distinction is why callers must not simply negate AllPulled/AllPushed: those
// return false on a remote-less vault, and a vault with no remotes is allowed.
func FailedRemotes(results map[string]error) []string {
	var failed []string
	for name, err := range results {
		if err != nil {
			failed = append(failed, name)
		}
	}
	sort.Strings(failed)
	return failed
}

// RemoteVerdict returns the one-line verdict for a pull/push whose remotes did not
// all succeed, or "" when there is nothing wrong.
//
// 🔴 STRANDED AND PARTIAL ARE DISTINCT IN WHAT THEY SAY AND IDENTICAL IN WHAT THEY
// RETURN. Every caller of this function treats a non-empty string as an ERROR —
// non-zero exit on the CLI, isError over MCP. There is no third "warning" tier, and
// that is settled precedent rather than taste: this project already killed the
// `partial` capture tier at 196 for the same reason. A status that is neither
// success nor failure is one that gets skimmed, and then it might as well not exist.
//
// Both outcomes are losses; they differ only in the remediation, so they differ only
// in the MESSAGE:
//
//   - STRANDED — no remote succeeded. The data exists in exactly one place, which is
//     the thing the vault exists to prevent.
//   - PARTIAL — some remotes took it, others did not. The data survives a disk
//     failure, but a remote you deliberately configured did not get it, and the next
//     machine to pull from that remote WILL SILENTLY BE BEHIND. That silence is the
//     whole bug: this is the mechanism that is supposed to tell you a machine is out
//     of sync, and it was reporting success while a host quietly stopped receiving
//     vault updates.
//
// sha is the commit being pushed; it is ignored for a pull.
func RemoteVerdict(op RemoteOp, results map[string]error, sha string) string {
	failed := FailedRemotes(results)
	if len(failed) == 0 {
		return ""
	}
	stranded := len(results) > 0 && len(failed) == len(results)
	list := strings.Join(failed, ", ")

	switch {
	case op == OpPull && stranded:
		return fmt.Sprintf("⚠ STRANDED: no configured remote could be pulled (%s) — this host is running on STALE vault state", list)
	case op == OpPull:
		return fmt.Sprintf("⚠ PARTIAL: pull FAILED on %s — this host may be missing changes that exist only there", list)
	case stranded:
		return fmt.Sprintf("⚠ STRANDED: commit %s reached NO remote (%s) — it exists in one place only; run `vp vault sync` to reconcile", shortSHA(sha), list)
	default:
		return fmt.Sprintf("⚠ PARTIAL: commit %s FAILED to reach %s — the next machine to pull from there will silently be behind", shortSHA(sha), list)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	if sha == "" {
		return "(none)"
	}
	return sha
}
