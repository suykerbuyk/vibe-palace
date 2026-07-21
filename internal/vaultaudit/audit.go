// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// BaselineRelPath is where the accepted-debt record lives, vault-relative.
//
// Phase 0 taught tidy and the surface stamp about this exact path. If it moves, the
// tidy SweepRule in internal/storage/vaulttidy.go must move with it — otherwise the
// first baseline written becomes permanent vault dirt that blocks `vault push`,
// which is precisely the failure Phase 0 existed to prevent.
const BaselineRelPath = "Audits/baseline.json"

// Status is a dimension's verdict.
//
// Unknown is not a shade of pass. It is the §1c invariant (vp_health reported
// `healthy` for a log it could not read) applied to the auditor itself, and it is
// the difference between an audit and a rubber stamp.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusUnknown Status = "unknown"
)

// DimensionResult is one dimension's outcome.
//
// Accepted is reported alongside New and Stale on purpose: a dimension carrying 82
// accepted findings and 0 new ones is PASSING, but it is not CLEAN, and a report
// that renders those identically is teaching its reader that the debt is gone.
type DimensionResult struct {
	Name     string
	Status   Status
	Evidence string // the command that reproduces this dimension's numbers
	New      []Finding
	Stale    []StaleEntry
	Accepted int
	Unknowns []string // why the auditor could not look; non-empty ⇒ Status is Unknown

	// acceptedFindings backs Report.Findings(). It is unexported because a caller
	// has no business distinguishing accepted findings from new ones — that is the
	// baseline's job — but Regenerate needs every finding, not just the new ones,
	// or accepting one dimension's drift would silently drop the accepted debt of
	// every other dimension from the record.
	acceptedFindings []Finding
}

// Report is one audit run over the whole vault.
type Report struct {
	Dimensions []DimensionResult

	// SessionNotes is the vault-wide session-note count AT RUN TIME. It is stamped
	// into the report's frontmatter, and it is the anchor the staleness nag
	// subtracts from to compute churn — without it, the next bootstrap can only ask
	// "how OLD is the last audit", never "how much has HAPPENED since it".
	SessionNotes int
}

// Findings returns every finding across every dimension — the input to
// Baseline.Regenerate when the operator accepts the current state.
func (r Report) Findings() []Finding {
	var out []Finding
	for _, d := range r.Dimensions {
		out = append(out, d.New...)
		// Accepted findings are still findings; Regenerate needs them all, or
		// accepting one dimension's drift would silently drop every other
		// dimension's accepted debt from the record.
		out = append(out, d.acceptedFindings...)
	}
	return out
}

// Failed reports whether any dimension found NEW drift or STALE debt. It does NOT
// gate anything — the audit is advisory (operator decision, 204). It exists so a
// caller can decide how loudly to render the report.
func (r Report) Failed() bool {
	for _, d := range r.Dimensions {
		if len(d.New) > 0 || len(d.Stale) > 0 {
			return true
		}
	}
	return false
}

// Run executes every dimension against the vault and diffs the results against the
// baseline.
//
// It is VAULT-GLOBAL and takes no project parameter — the thing being audited is the
// vault, and scoping it to whichever project the caller happened to invoke from is
// exactly how a project nobody has opened in three months escapes scrutiny forever.
// (A required parameter that does nothing is theatre — that was defect #4 of
// vp_health, deleted at 201.)
func Run(vault *storage.Vault) (Report, error) {
	base, err := LoadBaseline(filepath.Join(vault.Root, filepath.FromSlash(BaselineRelPath)))
	if err != nil {
		return Report{}, err
	}

	// The dimension registry. Every entry is EVIDENCE-BACKED — it exists because a
	// real defect got through — and carries the command that reproduces its numbers.
	// Order is fixed so the report is DIFFABLE: a report that reorders its own rows
	// between runs destroys the week-over-week drift signal that is the point of
	// committing it.
	dims := []struct {
		name     string
		evidence string
		run      func(*storage.Vault) ([]Finding, []string, error)
	}{
		{DimArchiveRoundTrip, EvidenceArchiveRoundTrip, auditArchiveRoundTrip},
		{DimProjectTreeCoherence, EvidenceProjectTreeCoherence, auditProjectTreeCoherence},
		{DimKGPortability, EvidenceKGPortability, auditKGPortability},
		{DimResumeDiscipline, EvidenceResumeDiscipline, auditResumeDiscipline},
		{DimIterationHeadings, EvidenceIterationHeadings, auditIterationHeadings},
		{DimMemoryPortability, EvidenceMemoryPortability, auditMemoryPortability},
	}

	report := Report{SessionNotes: SessionNoteCount(vault)}
	for _, d := range dims {
		findings, unknowns, err := d.run(vault)
		if err != nil {
			// A dimension that cannot run at all is UNKNOWN, not absent. Dropping it
			// silently would shrink the audit's own scope without saying so — the
			// failure this whole epic is named after.
			report.Dimensions = append(report.Dimensions, DimensionResult{
				Name:     d.name,
				Status:   StatusUnknown,
				Evidence: d.evidence,
				Unknowns: []string{fmt.Sprintf("dimension failed to run: %v", err)},
			})
			continue
		}
		report.Dimensions = append(report.Dimensions,
			newDimensionResult(d.name, d.evidence, base, findings, unknowns))
	}
	return report, nil
}

// newDimensionResult diffs one dimension's findings against the baseline and decides
// its status.
//
// Status precedence is deliberate: UNKNOWN outranks PASS. A dimension that could not
// read part of the vault has not passed, no matter how clean the part it could read
// looked. It does NOT outrank FAIL — real drift you can see is not made less real by
// a corner you could not.
func newDimensionResult(name, evidence string, base Baseline, findings []Finding, unknowns []string) DimensionResult {
	added, stale := base.Diff(findings)

	accepted := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if base.Dimensions[f.Dimension].accepts(f.Artifact) {
			accepted = append(accepted, f)
		}
	}

	// Only this dimension's stale entries belong to this dimension's result.
	ownStale := make([]StaleEntry, 0, len(stale))
	for _, s := range stale {
		if s.Dimension == name {
			ownStale = append(ownStale, s)
		}
	}
	ownAdded := make([]Finding, 0, len(added))
	for _, f := range added {
		if f.Dimension == name {
			ownAdded = append(ownAdded, f)
		}
	}

	status := StatusPass
	switch {
	case len(ownAdded) > 0 || len(ownStale) > 0:
		status = StatusFail
	case len(unknowns) > 0:
		status = StatusUnknown
	}

	return DimensionResult{
		Name:             name,
		Status:           status,
		Evidence:         evidence,
		New:              ownAdded,
		Stale:            ownStale,
		Accepted:         len(accepted),
		Unknowns:         unknowns,
		acceptedFindings: accepted,
	}
}
