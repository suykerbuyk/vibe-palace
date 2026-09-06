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

	// UnknownDimensions names the baseline keys this binary's registry does not
	// know — proof this binary is BEHIND THE VAULT. See Baseline.UnknownDimensions.
	//
	// It is carried on the report because without it the report is SILENTLY PARTIAL,
	// and silently is the whole problem. Run only ever builds a row per KNOWN
	// dimension (the loop below), so an unknown dimension has no row at all. Its
	// accepted artifacts do produce stale entries inside Baseline.Diff — that loop
	// ranges over every baseline key — but newDimensionResult then keeps only the
	// entries whose Dimension equals the KNOWN dimension it is building (the
	// `s.Dimension == name` filter that builds ownStale, below in this file; a line
	// number is not recorded here because a recorded one rots), and no known name
	// ever equals an unknown one. So those stale entries are COMPUTED AND DISCARDED,
	// Failed() sees nothing, and the report can render `✅ PASS — no new drift`
	// while whole dimensions of accepted debt went unexamined.
	//
	// There is therefore no pre-existing signal for this field to decorate. It is
	// the ONLY signal, which is exactly why it is loud in Render — and why it still
	// must not become a verdict: this field EXPLAINS a partial audit, it does not
	// fail one. Gating belongs at the dispatch seam, where it already is.
	UnknownDimensions []string
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

// NewFindings returns every NEW finding across every dimension — the raise set for
// `vp audit vault --accept --raise`. It is deliberately NOT Findings(): raising is
// scoped to what this run reported, never to the whole accepted backlog.
func (r Report) NewFindings() []Finding {
	var out []Finding
	for _, d := range r.Dimensions {
		out = append(out, d.New...)
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

// auditDimension is one entry in the dimension registry: the name the report and the
// baseline key on, the command a reader runs to reproduce its numbers, and the scan.
type auditDimension struct {
	name     string
	evidence string
	run      func(*storage.Vault) ([]Finding, []string, error)
}

// dimensions is THE dimension registry. Every entry is EVIDENCE-BACKED — it exists
// because a real defect got through — and carries the command that reproduces its
// numbers. Order is fixed so the report is DIFFABLE: a report that reorders its own
// rows between runs destroys the week-over-week drift signal that is the point of
// committing it.
//
// It is package-level, not a local in Run, so that the two surfaces which DESCRIBE
// this audit can derive their wording from it instead of restating it. Three places
// once named five of these dimensions in prose — the CLI description, the MCP tool
// description, and the vault-audit command template — and all three were wrong the
// moment a sixth landed. See DimensionNames.
var dimensions = []auditDimension{
	{DimArchiveRoundTrip, EvidenceArchiveRoundTrip, auditArchiveRoundTrip},
	{DimProjectTreeCoherence, EvidenceProjectTreeCoherence, auditProjectTreeCoherence},
	{DimKGPortability, EvidenceKGPortability, auditKGPortability},
	{DimResumeDiscipline, EvidenceResumeDiscipline, auditResumeDiscipline},
	{DimIterationHeadings, EvidenceIterationHeadings, auditIterationHeadings},
	{DimMemoryPortability, EvidenceMemoryPortability, auditMemoryPortability},
	{DimTaskHeadingMarkers, EvidenceTaskHeadingMarkers, auditTaskHeadingMarkers},
	{DimPalaceStoreDrawers, EvidencePalaceStoreDrawers, auditPalaceStoreDrawers},
	{DimTaskPreamble, EvidenceTaskPreamble, auditTaskPreamble},
	{DimTaskStatusDirectory, EvidenceTaskStatusDirectory, auditTaskStatusDirectory},
}

// DimensionNames returns the registry's dimension names in report order.
//
// It exists so a human-facing DESCRIPTION of this audit can be built from the
// registry rather than written beside it. A name list stated in prose is the
// stored-derived-value defect ADR-007 is about: it can only ever drift out of
// agreement with the thing it describes, and it had — three surfaces named five
// dimensions against a registry of ten.
//
// GENERATING IS CORRECT HERE, AND THAT IS NOT THE DEFAULT. The source-audit
// ungated-vault-writer rule is a PIN precisely because generating its answer would
// be WRONG: its anchor set and the derived predicate's do not cover the same
// mutations, so a generated list would silently drop two writers. This has no such
// gap. Run ranges over exactly this slice, and a dimension whose scan ERRORS still
// emits a row — as StatusUnknown, never absent, by explicit decision in Run. So
// every registry entry always produces a report row, and the derived list cannot
// overstate what the audit does.
//
// The returned slice is a copy: a caller that sorted or truncated the registry in
// place would silently reorder the report, which is the diffability property the
// fixed order exists to protect.
func DimensionNames() []string {
	names := make([]string, len(dimensions))
	for i, d := range dimensions {
		names[i] = d.name
	}
	return names
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

	// The unknown set comes off the baseline that was JUST LOADED, not off a second
	// read: a re-read could see a different file, and a report that disagreed with
	// the record it diffed against would be reporting on a vault that never existed.
	report := Report{
		SessionNotes:      SessionNoteCount(vault),
		UnknownDimensions: base.UnknownDimensions(),
	}
	for _, d := range dimensions {
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
		if base.Dimensions[f.Dimension].accepts(f) {
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
