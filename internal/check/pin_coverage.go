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

// pinCoverageFinding records one project's undeclared live sections, BY NAME.
// A bare count would say a resume is under-declared without saying WHERE, which
// is a number that rots into noise — the reader cannot act on it and cannot tell
// when it stops being true.
//
// Core carries the project's measured inviolable core, because whether the
// finding is EXPOSED or LATENT is decided by that measurement and by nothing
// else — see the exposure note on CheckPinCoverage.
type pinCoverageFinding struct {
	Project  string
	Sections []string
	Core     coreMeasure
}

// exposed reports whether this project's undeclared sections are being dropped
// FOR REAL, right now.
//
// 🔴 IT IS THE CORE MEASUREMENT, NOT A NEW THRESHOLD. A resume is only reduced
// when its project's payload cannot fit — which is exactly coreMeasure.overCap,
// the same verdict CheckCoreFloor reports, over the same bytes, against the same
// CoreMaxBytes derived from the bootstrap budget. Inventing a second bound here
// (a resume-only size, a section count, a name list) would have created a number
// nothing else in the tree agrees with, and every hand-tuned number in this
// repository has since rotted. When the budget moves, this moves with it.
func (f pinCoverageFinding) exposed() bool { return f.Core.overCap() }

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
// 🔴 EXPOSED vs LATENT — WHY THE FINDING IS SPLIT (iteration 262). The first cut
// of this check reported every undeclared resume identically, and against the
// live vault that was 8 of 8 projects on day one. The report was TRUE and is not
// weakened here; what it was not, was AIMED. A row that is red for everything
// teaches its reader to skim it, and this repository already carries a live
// instance of that failure (the `vp init` scaffold `vault tidy` re-reports every
// single run, which is why nobody reads tidy).
//
// The aim comes from asking when an undeclared section actually COSTS anything.
// The answer is: when that project's resume is actually shed — and a resume is
// only shed when its project's payload cannot fit, i.e. when its inviolable core
// is over CoreMaxBytes. So each finding is classified by the measurement
// CheckCoreFloor already makes (measureCore / coreMeasure.overCap), NOT by a new
// threshold invented for this check:
//
//   - EXPOSED — core over cap, so the ladder reduces this resume and these named
//     sections are being dropped for real. This is the row that demands action,
//     and it is named FIRST.
//   - LATENT — undeclared sections exist, but the core fits, so nothing is being
//     dropped today. It becomes exposed the moment the project grows.
//
// Strictly READ-ONLY and advisory:
//
//   - Info when one or more findings are EXPOSED — real loss is happening now.
//   - Pass when every finding is LATENT — nothing is being dropped today. But
//     PASS IS NOT SILENCE HERE: the latent census still prints, naming every
//     project and every section, exactly as the pin-less exclusion count does.
//     A latent set that could grow unseen would be a defect maturing in the dark.
//   - Pass and genuinely silent only when there are no findings at all.
//
// Never Fail — the same deliberate stance as CheckResumeCaps and CheckCoreFloor.
// There is no typed write path left to gate (any agent holding Bash can write a
// resume directly), so prevention is unachievable in-process; and "unmarked" is a
// legitimate END STATE for genuinely live content, so failing on it would be
// demanding a lie.
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
	var findings []pinCoverageFinding
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
		undeclared := resumezone.UndeclaredLiveSections(body)
		if len(undeclared) == 0 {
			continue
		}
		// The core measurement decides exposed vs latent. A project whose
		// resume.md exists always measures, so the zero value is never used to
		// classify a real finding.
		core, _ := measureCore(projectsDir, name)
		findings = append(findings, pinCoverageFinding{Project: name, Sections: undeclared, Core: core})
	}

	if len(findings) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d resume.md fully declared%s", scanned, pinlessNote(pinless))
		return r
	}

	sort.Slice(findings, func(i, j int) bool { return findings[i].Project < findings[j].Project })
	var exposed, latent []pinCoverageFinding
	for _, f := range findings {
		if f.exposed() {
			exposed = append(exposed, f)
		} else {
			latent = append(latent, f)
		}
	}

	// The headline alone must say WHICH CASE the reader is in — action required,
	// or census only. Detail lines are for the reader who has already decided to
	// look; the summary is for the one deciding whether to.
	if len(exposed) > 0 {
		r.Status = Info
		r.Summary = fmt.Sprintf("%d of %d EXPOSED — undeclared live sections being shed now; %d latent%s",
			len(exposed), scanned, len(latent), pinlessNote(pinless))
	} else {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d of %d latent — undeclared live sections, core fits, none shed%s",
			len(latent), scanned, pinlessNote(pinless))
	}

	// EXPOSED first and unmistakably: these are the rows that cost something today.
	if len(exposed) > 0 {
		r.Details = append(r.Details,
			fmt.Sprintf("EXPOSED — core over the %d-byte floor, so the ladder reduces this resume and drops these:", CoreMaxBytes))
		for _, f := range exposed {
			r.Details = append(r.Details,
				fmt.Sprintf("  %s: %s  [core %s = %s + %s]",
					f.Project, strings.Join(f.Sections, "; "), humanKB(f.Core.total()),
					half("resume", f.Core.Resume, f.Core.HasResume),
					half("workflow", f.Core.Workflow, f.Core.HasWorkflow)))
		}
	}
	// LATENT prints on PASS runs too — an unseen census is a census that grows.
	if len(latent) > 0 {
		r.Details = append(r.Details,
			"LATENT — undeclared, but the core fits, so nothing is dropped today:")
		for _, f := range latent {
			r.Details = append(r.Details,
				fmt.Sprintf("  %s: %s  [core %s]", f.Project, strings.Join(f.Sections, "; "), humanKB(f.Core.total())))
		}
	}
	r.Details = append(r.Details,
		"A section carrying neither "+resumezone.ResumePinMarker+" nor "+resumezone.ResumeDisposableMarker+" is LIVE STATE:",
		"nobody has ruled on it, and the shed ladder drops it under budget pressure regardless.",
		"Rule on each at the next wrap (/vpc-wrap): pin what an agent must not act without;",
		"mark "+resumezone.ResumeDisposableMarker+" what is pure navigation an agent can re-derive from a tool call.",
		"Leaving a section unmarked is a legitimate answer for genuinely live state — but then this row",
		"is telling you the truth: live state is sitting in the zone the ladder sheds.")
	if len(exposed) > 0 {
		r.Details = append(r.Details,
			"Act on the EXPOSED projects first: their loss is not hypothetical, it is happening every bootstrap.")
	}
	if len(latent) > 0 {
		r.Details = append(r.Details,
			"LATENT is not a pass mark, it is a countdown: the same sections start dropping the day that",
			"project's core crosses the floor (the same measurement `vp check --check core-floor` reports).")
	}
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
