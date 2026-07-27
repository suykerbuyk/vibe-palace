// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/resumezone"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// pinCoverageViolation records one project's undeclared live sections, BY NAME.
// A bare count would say a resume is under-declared without saying WHERE, which
// is a number that rots into noise — the reader cannot act on it and cannot tell
// when it stops being true.
type pinCoverageViolation struct {
	Project  string
	Sections []string
}

// CheckPinCoverage scans every project under <vault>/Projects/ and reports the
// ones whose resume.md holds LIVE SECTIONS IN THE SHEDDABLE ZONE: H2 sections
// carrying neither `<!-- vp:pin -->` nor `<!-- vp:disposable -->`.
//
// Under the three-state rule an unmarked section is LIVE STATE, not reference
// material: absence is an omission, never a decision, and reading silence as
// consent to drop is how correctness notes disappear with nobody being told. The
// shed ladder drops those sections today, so this row is a report of real
// exposure, not a style preference.
//
// This is the ratchet. Before it, NOTHING in this repository read a pin marker
// except the shed ladder itself — which is precisely why the shipped resume
// template could go on declaring live state sheddable with no test, no check and
// no review noticing. A rule with no reader is not a rule.
//
// 🔴 A RESUME THAT PINS NOTHING IS A DIFFERENT CONDITION, and it is deliberately
// NOT reported as a violation here. It is EXCLUDED from the scan and counted
// separately in the summary, for two reasons:
//
//   - The finding would degenerate. With no pin zone, EVERY section is undeclared,
//     so the report becomes the resume's entire table of contents — naming
//     everything names nothing, and it buries the case this check exists for: the
//     resume that pinned two sections and left one un-ruled.
//   - It is already reported, and its remedy is different. resumezone.PinnedZone
//     returns declared=false for such a document, so the ladder keeps it WHOLE and
//     vp_bootstrap_context reports over-budget rather than guessing which half was
//     safe to drop; CheckCoreFloor surfaces the size consequence. That condition's
//     fix is "declare a pin zone"; this one's is "rule on these named sections".
//     Merging two conditions with different fixes into one row helps nobody.
//
// The exclusion stays VISIBLE rather than silent: the count of pin-less resumes
// rides in the summary of every run, Pass or Info, so it can never quietly grow.
//
// Strictly READ-ONLY and advisory: Pass when every pin-declaring resume is fully
// ruled on, Info when one or more are not. Never Fail — the same deliberate stance
// as CheckResumeCaps and CheckCoreFloor. There is no typed write path left to gate
// (any agent holding Bash can write a resume directly), so prevention is
// unachievable in-process; and "unmarked" is a legitimate END STATE for genuinely
// live content, so failing on it would be demanding a lie.
//
// Absence is never a violation: a missing Projects/ directory, a project with no
// resume.md, and a resume with no H2 sections at all all report nothing.
func CheckPinCoverage(v *storage.Vault) Result {
	r := Result{Name: "Pin coverage"}

	if v.Root == "" {
		r.Status = Skip
		r.Summary = "no vault configured"
		return r
	}

	projectsDir := filepath.Join(v.Root, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			r.Status = Pass
			r.Summary = "no Projects/ directory"
			return r
		}
		r.Status = Info
		r.Summary = fmt.Sprintf("scan Projects/: %v", err)
		return r
	}

	scanned := 0 // resumes declaring a pin zone — the ones this check can rule on
	pinless := 0 // resumes declaring none — excluded; see the doc comment above
	var violations []pinCoverageViolation
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		path := filepath.Join(projectsDir, name, "resume.md")
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			// Missing resume.md is not a violation — nothing to report.
			continue
		}
		body := string(data)

		// The zone string is discarded on purpose: only the `declared` half is
		// wanted here, and it is taken from the SAME function the shed ladder
		// calls so that "declares a pin zone" cannot come to mean one thing in
		// the ladder and another in the advisory reporting on it.
		if _, declared := resumezone.PinnedZone(body); !declared {
			pinless++
			continue
		}
		scanned++
		if undeclared := resumezone.UndeclaredLiveSections(body); len(undeclared) > 0 {
			violations = append(violations, pinCoverageViolation{Project: name, Sections: undeclared})
		}
	}

	if len(violations) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d resume.md fully declared%s", scanned, pinlessNote(pinless))
		return r
	}

	sort.Slice(violations, func(i, j int) bool { return violations[i].Project < violations[j].Project })
	r.Status = Info
	r.Summary = fmt.Sprintf("%d of %d resume.md hold undeclared live sections%s",
		len(violations), scanned, pinlessNote(pinless))
	for _, viol := range violations {
		r.Details = append(r.Details,
			fmt.Sprintf("  %s: %s", viol.Project, strings.Join(viol.Sections, "; ")))
	}
	r.Details = append(r.Details,
		"A section carrying neither "+resumezone.ResumePinMarker+" nor "+resumezone.ResumeDisposableMarker+" is LIVE STATE:",
		"nobody has ruled on it, and the shed ladder drops it under budget pressure regardless.",
		"Rule on each at the next wrap (/vpc-wrap): pin what an agent must not act without;",
		"mark "+resumezone.ResumeDisposableMarker+" what is pure navigation an agent can re-derive from a tool call.",
		"Leaving a section unmarked is a legitimate answer for genuinely live state — but then this row",
		"is telling you the truth: live state is sitting in the zone the ladder sheds.")
	return r
}

// pinlessNote renders the parenthetical count of resumes excluded for declaring
// no pin zone, or "" when there are none. It rides in the summary on PASS runs
// too: an exclusion nobody can see is an exclusion that grows.
func pinlessNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d declare no pin zone)", n)
}
