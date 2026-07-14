// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package vaultaudit is the ratchet behind the vault audit: it compares what the
// vault ACTUALLY contains against what the design says it should, and records the
// gap so the next run reports NEW drift instead of ancient drift.
//
// # Why there is no date watermark
//
// The plan this package implements called for one: a cutoff date per dimension,
// with older artifacts reported `unknown` rather than `fail`. The live vault
// disproved it. `note_path` first appears in 2026-07-12-12a23ab8-21.md, while
// 2026-07-12-09.md — written the SAME DAY — lacks it; and note IDs sort by
// date-fingerprint-NN, so a second host writing later in the day sorts EARLIER.
// There is no sound boundary to cut on. Worse, `archive:` dates back to
// 2026-04-16: it is not a young field forgivably absent from old notes, it is an
// OLD field the linker has been failing to populate the whole time. A watermark
// would have framed a live bug as accepted history.
//
// So the accepted set is EXPLICIT — every accepted artifact is named — with the
// reason hoisted to the dimension, because the ~269 notes that predate a field
// genuinely do share one reason and 269 copies of it would be noise, not triage.
//
// # The ratchet
//
// Two failures, and the second is the point:
//
//   - NEW: a finding not in the accepted set. Fresh drift.
//   - STALE: an accepted entry that is NO LONGER a finding. Fixed, still recorded.
//
// Without STALE, the baseline rots into a lie — you fix something, the list keeps
// claiming it is broken, and the list stops meaning anything. With it, THE
// BASELINE CAN ONLY SHRINK. This is what makes fixing a bug mechanically compel
// the record to be corrected: when the archive-link backfill lands, the accepted
// entries stop being findings and the audit demands their removal.
//
// # Advisory, never blocking
//
// Unlike internal/sourceaudit — a source gate, where failing the build is right —
// the vault audit REPORTS and never blocks (operator decision, iteration 204: a
// blocking auditor gets disabled the first time it is wrong at a bad moment, and a
// disabled auditor audits nothing). "NEW finding" and "STALE entry" are loud lines
// in a report, not a non-zero exit from `make test`. The operator's vault contents
// are not a build input.
package vaultaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Finding is one artifact failing one dimension.
//
// Artifact is a STABLE, VAULT-RELATIVE identifier — a path, never an absolute one
// and never an array index. It is the baseline's key, so an unstable ID would make
// every run report the same defect as both NEW and STALE forever.
type Finding struct {
	Dimension string
	Artifact  string
	Detail    string
}

// DimensionBaseline is the accepted debt for one dimension: the shared reason, and
// the artifacts it covers.
//
// Reason is per-DIMENSION, not per-artifact, because that is where the reason
// actually lives — "these notes predate the field" is one fact about 269 notes, not
// 269 facts. Except carries the genuine one-offs: an artifact whose acceptance has a
// DIFFERENT reason than its dimension's. An artifact in Except need not also appear
// in Accepted; both are accepted.
type DimensionBaseline struct {
	Reason   string            `json:"reason"`
	Accepted []string          `json:"accepted"`
	Except   map[string]string `json:"except,omitempty"`
}

// accepts reports whether this dimension's baseline covers the artifact.
func (d DimensionBaseline) accepts(artifact string) bool {
	if _, ok := d.Except[artifact]; ok {
		return true
	}
	return slices.Contains(d.Accepted, artifact)
}

// all returns every artifact this baseline accepts, from both Accepted and Except.
func (d DimensionBaseline) all() []string {
	out := slices.Clone(d.Accepted)
	for a := range d.Except {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// Baseline is the accepted-findings record for the whole vault, keyed by dimension.
type Baseline struct {
	Dimensions map[string]DimensionBaseline `json:"dimensions"`
}

// StaleEntry is an accepted artifact that is no longer a finding. It is a FAILURE
// of the ratchet, not a success: the fix landed and nobody updated the record.
type StaleEntry struct {
	Dimension string
	Artifact  string
	Reason    string
}

// ReasonUntriaged marks a dimension nobody has explained yet. Deliberately ugly: a
// baseline wearing it is claiming nothing, and must read as unfinished work.
//
// internal/sourceaudit learned this the hard way — its first baseline stamped an
// equivalent marker over all 60 entries, and it took a full session of triage to
// climb back out. An untriaged entry must never be able to masquerade as an
// explained one.
const ReasonUntriaged = "UNTRIAGED: accepted at baseline creation — explain or fix"

// LoadBaseline reads a baseline. A missing file is an EMPTY baseline, not an error:
// a vault with no accepted debt is a legitimate state, and the audit should hold it
// to a clean bill of health rather than silently pass.
//
// A file that EXISTS but cannot be read or parsed IS an error. "I could not look" is
// not "there was nothing there" — the invariant this whole epic is built on.
func LoadBaseline(path string) (Baseline, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Baseline{Dimensions: map[string]DimensionBaseline{}}, nil
	}
	if err != nil {
		return Baseline{}, fmt.Errorf("read baseline %s: %w", path, err)
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return Baseline{}, fmt.Errorf("parse baseline %s: %w", path, err)
	}
	if b.Dimensions == nil {
		b.Dimensions = map[string]DimensionBaseline{}
	}
	return b, nil
}

// Save writes the baseline sorted and indented, so it DIFFS CLEANLY in review.
// A report that reorders its own rows between runs is not diffable, and
// week-over-week drift is the entire point of committing this file.
func (b Baseline) Save(path string) error {
	out := Baseline{Dimensions: make(map[string]DimensionBaseline, len(b.Dimensions))}
	for name, d := range b.Dimensions {
		accepted := slices.Clone(d.Accepted)
		slices.Sort(accepted)
		accepted = slices.Compact(accepted)
		out.Dimensions[name] = DimensionBaseline{Reason: d.Reason, Accepted: accepted, Except: d.Except}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	// The first baseline is written into an Audits/ directory that does not exist
	// yet on any vault that has never been audited — which is every vault, once.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create baseline dir: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Diff compares findings against the baseline and returns what the audit must
// report: fresh drift, and fixed-but-still-recorded debt.
//
// STALE is the half that makes this a ratchet instead of a suppression file. See
// the package doc.
func (b Baseline) Diff(findings []Finding) (added []Finding, stale []StaleEntry) {
	// present[dimension][artifact] — what the audit found THIS run.
	present := make(map[string]map[string]bool, len(b.Dimensions))
	for _, f := range findings {
		if present[f.Dimension] == nil {
			present[f.Dimension] = map[string]bool{}
		}
		present[f.Dimension][f.Artifact] = true

		if !b.Dimensions[f.Dimension].accepts(f.Artifact) {
			added = append(added, f)
		}
	}

	for name, d := range b.Dimensions {
		for _, artifact := range d.all() {
			if present[name][artifact] {
				continue
			}
			reason := d.Reason
			if r, ok := d.Except[artifact]; ok {
				reason = r
			}
			stale = append(stale, StaleEntry{Dimension: name, Artifact: artifact, Reason: reason})
		}
	}

	slices.SortFunc(added, func(x, y Finding) int { return compare2(x.Dimension, x.Artifact, y.Dimension, y.Artifact) })
	slices.SortFunc(stale, func(x, y StaleEntry) int { return compare2(x.Dimension, x.Artifact, y.Dimension, y.Artifact) })
	return added, stale
}

// Regenerate rebuilds the baseline from the current findings while PRESERVING every
// dimension's reason.
//
// The reason is the entire value of a baseline entry — it is where a human recorded
// WHY this debt is accepted. Regeneration is routine (every fix forces one), so a
// regen that stamps UNTRIAGED over the reasons destroys the triage that produced
// them and the list decays back into the undifferentiated blob it started as.
// internal/sourceaudit shipped exactly that bug and had to fix it at 203.
//
// Survivors keep their dimension's reason. Fixed findings drop out — the baseline
// may only shrink. A genuinely NEW dimension arrives marked UNTRIAGED, so an
// unexplained dimension can never masquerade as an explained one.
func (b Baseline) Regenerate(findings []Finding) Baseline {
	out := Baseline{Dimensions: map[string]DimensionBaseline{}}
	for _, f := range findings {
		d, ok := out.Dimensions[f.Dimension]
		if !ok {
			prior, existed := b.Dimensions[f.Dimension]
			d = DimensionBaseline{Reason: ReasonUntriaged}
			if existed && prior.Reason != "" {
				d.Reason = prior.Reason
			}
			// Carry ONLY the per-artifact overrides that are still findings; an
			// override for a fixed artifact is stale debt and must not survive.
			if existed && len(prior.Except) > 0 {
				d.Except = map[string]string{}
				for _, g := range findings {
					if g.Dimension != f.Dimension {
						continue
					}
					if r, has := prior.Except[g.Artifact]; has {
						d.Except[g.Artifact] = r
					}
				}
				if len(d.Except) == 0 {
					d.Except = nil
				}
			}
		}
		if _, overridden := d.Except[f.Artifact]; !overridden {
			d.Accepted = append(d.Accepted, f.Artifact)
		}
		out.Dimensions[f.Dimension] = d
	}
	for name, d := range out.Dimensions {
		slices.Sort(d.Accepted)
		d.Accepted = slices.Compact(d.Accepted)
		out.Dimensions[name] = d
	}
	return out
}

// compare2 orders by (a1,b1) then (a2,b2) — a stable two-key sort so reports and
// baselines are byte-reproducible across runs.
func compare2(a1, b1, a2, b2 string) int {
	switch {
	case a1 < a2:
		return -1
	case a1 > a2:
		return 1
	case b1 < b2:
		return -1
	case b1 > b2:
		return 1
	}
	return 0
}
