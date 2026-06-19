// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"fmt"
)

// ApplyOptions controls whether Apply is allowed to remove shims whose
// command names no longer exist. Default (zero value) is strictly
// additive: no removals, matching the safety contract in the plan doc.
type ApplyOptions struct {
	// AllowStaleRemoval permits Apply to delete files classified as Stale.
	// Callers that want the interactive/per-file accept prompt behaviour
	// from `vp commands upgrade` should filter the plan themselves and
	// pass only the approved Stale entries with this flag on.
	AllowStaleRemoval bool
}

// Outcome pairs a Change with the Result of acting on it. Kept as a flat
// slice (rather than a map) so callers can stream status lines in the
// same order Plan produced.
type Outcome struct {
	Change Change
	Result Result
	// Removed is true when Apply deleted a Stale shim. Distinct from the
	// Result.Kind values because removal is a plan-level concept — there
	// is no ResultKind for it.
	Removed bool
	Err     error
}

// Report is the aggregate summary of an Apply run. Counts align with the
// init-status line format from the plan doc:
//
//	shims: added N, updated M, unchanged K, stale S, custom C
type Report struct {
	Added     int
	Updated   int
	Unchanged int
	Removed   int
	Stale     int
	Custom    int
	Outcomes  []Outcome
}

// Apply executes the plan. It never touches Custom files. Stale files are
// removed iff opts.AllowStaleRemoval is true; otherwise they are counted
// as Stale and left on disk. Unchanged entries incur no IO.
//
// Returns the first IO error encountered alongside the Report accumulated
// up to that point — the caller can use the partial report to explain
// what succeeded before the failure.
func Apply(changes []Change, opts ApplyOptions) (Report, error) {
	var rep Report
	for _, ch := range changes {
		out := Outcome{Change: ch}
		switch ch.Kind {
		case New, Modified:
			res, err := Wire(ch.Path, ch.Name, ch.Brief, ch.Project, ch.ArgHint)
			out.Result = res
			out.Err = err
			if err != nil {
				rep.Outcomes = append(rep.Outcomes, out)
				return rep, fmt.Errorf("wire %s: %w", ch.Path, err)
			}
			switch res.Kind {
			case Added:
				rep.Added++
			case Updated:
				rep.Updated++
			case Unchanged:
				// Possible when Plan ran before a concurrent writer made
				// the file match — not an error, just no-op accounting.
				rep.Unchanged++
			case Custom:
				// Race: the file we expected to own lost its marker
				// between Plan and Apply. Record as Custom; do not write.
				rep.Custom++
			}
		case UnchangedChange:
			out.Result = Result{Kind: Unchanged, PrevSha: ch.PrevSha}
			rep.Unchanged++
		case Stale:
			if !opts.AllowStaleRemoval {
				rep.Stale++
				out.Result = Result{Kind: Unchanged, PrevSha: ch.PrevSha}
				break
			}
			removed, err := Remove(ch.Path)
			out.Err = err
			out.Removed = removed
			if err != nil {
				rep.Outcomes = append(rep.Outcomes, out)
				return rep, fmt.Errorf("remove %s: %w", ch.Path, err)
			}
			if removed {
				rep.Removed++
			} else {
				// Lost the marker between Plan and Apply — leave it.
				rep.Custom++
			}
		case CustomChange:
			rep.Custom++
			out.Result = Result{Kind: Custom}
		}
		rep.Outcomes = append(rep.Outcomes, out)
	}
	return rep, nil
}
