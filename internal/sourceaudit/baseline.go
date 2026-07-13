// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// BaselineEntry is one KNOWN, ACCEPTED finding. Every entry must carry a Reason —
// an unexplained entry is indistinguishable from an oversight, and in six months
// nobody will know which it was.
type BaselineEntry struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Baseline is the accepted-findings list.
//
// It exists because the repository does not start clean, and a gate that fails
// loudly on day one is a gate that gets disabled on day one. But it is designed so
// that it CAN ONLY SHRINK — see Diff.
type Baseline struct {
	Entries []BaselineEntry `json:"entries"`
}

// LoadBaseline reads a baseline file. A missing file is an EMPTY baseline, not an
// error: a fresh tree with no accepted debt is a legitimate state, and the gate
// should hold it to a clean bill of health rather than silently pass.
func LoadBaseline(path string) (Baseline, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Baseline{}, nil
	}
	if err != nil {
		return Baseline{}, fmt.Errorf("read baseline: %w", err)
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return Baseline{}, fmt.Errorf("parse baseline %s: %w", path, err)
	}
	return b, nil
}

// Save writes the baseline, sorted and indented so it diffs cleanly in review.
func (b Baseline) Save(path string) error {
	sort.Slice(b.Entries, func(i, j int) bool { return b.Entries[i].ID < b.Entries[j].ID })
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// reasonTODO marks an entry nobody has triaged yet. It is deliberately ugly: an
// entry wearing it is claiming nothing, and should read as unfinished work.
const reasonTODO = "TODO: accepted at baseline creation — explain or fix"

// Regenerate rebuilds the baseline from the current findings while PRESERVING the
// reason on every entry that survives.
//
// The reason string is the entire value of a baseline entry — it is where a human
// recorded why a finding is accepted (the stdlib dispatches it, task X owns the fix,
// it is deliberate). Regeneration is routine: every deletion forces one. So a regen
// that stamps TODO over every reason destroys the triage that produced them, and the
// list decays back into the undifferentiated blob it started as — which is exactly
// the state the first 60-entry baseline was in, and it took a full session of
// subagent triage to climb out of it.
//
// Survivors keep their reason. Fixed findings drop out (the baseline may only
// shrink). Genuinely new findings arrive marked TODO, so an untriaged entry can never
// masquerade as an explained one.
func (b Baseline) Regenerate(findings []Finding) Baseline {
	prior := make(map[string]string, len(b.Entries))
	for _, e := range b.Entries {
		prior[e.ID] = e.Reason
	}
	out := Baseline{}
	for _, f := range findings {
		reason, ok := prior[f.ID()]
		if !ok || reason == "" {
			reason = reasonTODO
		}
		out.Entries = append(out.Entries, BaselineEntry{ID: f.ID(), Reason: reason})
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].ID < out.Entries[j].ID })
	return out
}

// Diff compares findings against the baseline and returns what the gate must act on.
//
// TWO failures, and the second one is the point:
//
//   - added: a finding not in the baseline. New debt. Fail the build.
//   - stale: a baseline entry that is NO LONGER a finding. Fixed debt, still
//     recorded. ALSO fail the build.
//
// Without `stale`, the baseline rots into a lie: you fix something, the list keeps
// claiming it is broken, and the list stops meaning anything. With it, THE BASELINE
// CAN ONLY EVER SHRINK — every fix must be recorded, and no fix can be quietly
// un-recorded. That is the ratchet, and it is the difference between an audit that
// compounds and a checklist that decays.
func (b Baseline) Diff(findings []Finding) (added []Finding, stale []BaselineEntry) {
	known := make(map[string]bool, len(b.Entries))
	for _, e := range b.Entries {
		known[e.ID] = true
	}
	present := make(map[string]bool, len(findings))
	for _, f := range findings {
		present[f.ID()] = true
		if !known[f.ID()] {
			added = append(added, f)
		}
	}
	for _, e := range b.Entries {
		if !present[e.ID] {
			stale = append(stale, e)
		}
	}
	return added, stale
}
