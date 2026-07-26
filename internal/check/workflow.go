// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// WorkflowMaxBytes caps the served per-project workflow.md — the behavioral
// contract that vp_bootstrap_context inlines into every session's payload
// from <vault>/Projects/<p>/workflow.md.
//
// DERIVATION — from the bootstrap budget arithmetic in
// internal/tools/context_tools.go, not chosen by taste:
//
//   - AssembleBootstrap defaults max_tokens to 8000 and estimates ~4 bytes
//     per token, so the whole payload (workflow, resume, tasks, sessions,
//     directive and budget report together) shares a ~32,000-byte default
//     ceiling.
//   - The token shed ladder's workflow rung ("workflow->excerpt",
//     shedToBudget) excerpts the contract ONLY when its body exceeds
//     bootstrapExcerptCap (4,000 bytes). At or under that bound the contract
//     is structurally immune to excerpting: it ships whole on every rung of
//     the ladder, honoring ADR-009 ("inviolable core delivered whole") by
//     construction rather than by the futility restore.
//
// So the cap IS that 4,000-byte bound — ~1,000 tokens, one eighth of the
// default budget. The ADR-008 thin embedded scaffold is ~1.5 KB, leaving
// ~2.5 KB of headroom for project-specific patterns before this advisory
// fires. TestWorkflowCapMirrorsExcerptCap (internal/tools) pins this constant
// to bootstrapExcerptCap so the two cannot drift apart silently.
const WorkflowMaxBytes = 4000

// workflowViolation records one project's over-cap workflow.md. A project
// within the cap is never added to the report.
type workflowViolation struct {
	Project string
	Size    int
}

// CheckWorkflowCaps scans every project under <vault>/Projects/ and reports
// the ones whose workflow.md has outgrown WorkflowMaxBytes — the size above
// which the bootstrap shed ladder may excerpt the behavioral contract under
// token pressure. workflow.md grew 21% between iterations 205 and 209 while
// nobody was measuring it; this check is the measurement.
//
// It is strictly READ-ONLY and advisory: Pass when every workflow is within
// the cap, Info when one or more are not. Never Fail — the same deliberate
// stance as CheckResumeCaps: there is no typed write path to gate (any agent
// holding Bash can write the file directly), so prevention is unachievable
// in-process and a fat contract is a tax, not a breakage. The un-bypassable
// gate on the payload lives in the LiveVault canary, not here.
//
// A healthy state is SILENT: within the cap there are no details, no floor
// sizes, no numbers — just the pass row. Absence is never a violation: a
// missing Projects/ directory, a project with no workflow.md, and a workflow
// within the cap all report nothing.
func CheckWorkflowCaps(v *storage.Vault) Result {
	r := Result{Name: "Workflow caps"}

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

	scanned := 0
	var violations []workflowViolation
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		info, serr := os.Stat(filepath.Join(projectsDir, name, "workflow.md"))
		if serr != nil || info.IsDir() {
			// Missing workflow.md is not a violation — nothing to report.
			continue
		}
		scanned++
		if n := int(info.Size()); n > WorkflowMaxBytes {
			violations = append(violations, workflowViolation{Project: name, Size: n})
		}
	}

	if len(violations) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d workflow.md within cap", scanned)
		return r
	}

	sort.Slice(violations, func(i, j int) bool { return violations[i].Project < violations[j].Project })
	r.Status = Info
	r.Summary = fmt.Sprintf("%d of %d workflow.md over cap", len(violations), scanned)
	for _, viol := range violations {
		r.Details = append(r.Details,
			fmt.Sprintf("  %s: %s (cap %d bytes)", viol.Project, humanKB(viol.Size), WorkflowMaxBytes))
	}
	r.Details = append(r.Details,
		fmt.Sprintf("Over %d bytes the bootstrap shed ladder may excerpt the contract under token pressure;", WorkflowMaxBytes),
		"at or under it the contract ships whole on every rung (ADR-009).",
		"workflow.md is thin by design (ADR-008): project-specific patterns only —",
		"the generic doctrine lives in the binary, served on demand via vp_get_doctrine.")
	if wf, doc, ok := embeddedWorkflowFloors(); ok {
		r.Details = append(r.Details,
			fmt.Sprintf("Embedded floors: thin workflow scaffold %s; doctrine %s (served on demand, never in the payload).",
				humanKB(wf), humanKB(doc)))
	}
	return r
}

// embeddedWorkflowFloors returns the byte sizes of the two embedded ADR-008
// artifacts that define how small the served contract CAN be: the thin
// workflow.md scaffold every new project starts from, and the doctrine.md the
// binary serves on demand (never in the bootstrap payload). ok is false when
// the embedded corpus cannot be walked or either file is absent — the
// advisory then simply omits the floor context rather than guessing.
func embeddedWorkflowFloors() (workflow, doctrine int, ok bool) {
	rs, err := templates.WalkEmbedded()
	if err != nil {
		return 0, 0, false
	}
	for _, res := range rs {
		switch res.RelPath {
		case "workflow.md":
			workflow = len(res.Bytes)
		case "doctrine.md":
			doctrine = len(res.Bytes)
		}
	}
	return workflow, doctrine, workflow > 0 && doctrine > 0
}
