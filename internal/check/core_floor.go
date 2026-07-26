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
)

// CoreMaxBytes caps a project's INVIOLABLE CORE — resume.md + workflow.md, the
// two artifacts vp_bootstrap_context must deliver whole per ADR-009.
//
// DERIVATION — from the bootstrap budget, not chosen by taste:
//
//	tools.DefaultBootstrapMaxTokens (16,000) × ~4 bytes/token = 64,000 B payload
//	− CoreContextReserveBytes (8,000 B) for the shedTierContext rungs and overhead
//	= 56,000 B available to the core
//
// The reserve is measured, not guessed: with everything inline the context tier
// (active tasks, sessions, memory, kg, commands+skills) plus the directive, URIs
// and budget report cost ≈ 1,935 tokens ≈ 7,740 B. 8,000 rounds that up.
//
// TestCoreFloorMatchesBootstrapBudget (internal/tools) pins this to the real
// budget constant so the two cannot drift apart silently. The pinning test lives
// in tools, not here, because internal/check must not import internal/tools —
// tools' own tests import check, and the reverse edge would cycle.
const CoreMaxBytes = 56000

// CoreContextReserveBytes is the slice of the payload budget held back for the
// sheddable context tier and JSON overhead. It exists so the CoreMaxBytes
// arithmetic above is auditable rather than a bare number.
const CoreContextReserveBytes = 8000

// coreViolation records one project whose core cannot fit the payload budget.
type coreViolation struct {
	Project     string
	Resume      int
	Workflow    int
	HasResume   bool
	HasWorkflow bool
}

func (v coreViolation) total() int { return v.Resume + v.Workflow }

// half renders one core artifact's contribution. An ABSENT file reports
// "absent", never "0.0 KB" — a zero would read as a present-but-empty file and
// send a reader hunting for content that was never there.
func half(label string, n int, present bool) string {
	if !present {
		return label + " absent"
	}
	return label + " " + humanKB(n)
}

// CheckCoreFloor scans every project under <vault>/Projects/ and reports the
// ones whose INVIOLABLE CORE — resume.md + workflow.md together — has outgrown
// the share of the bootstrap budget available to it.
//
// 🔴 WHY THIS REPLACED THE WORKFLOW-ONLY CAP (iteration 260). The predecessor
// measured workflow.md alone against bootstrapExcerptCap (4,000 B), on the
// rationale that above that bound the shed ladder MAY excerpt the contract. That
// framing died with the budget raise: the ladder now rarely reaches the workflow
// rung, so the old check fired on a 13.2 KB contract that was no longer causing
// harm — an advisory crying wolf, which is the soft tier nobody reads.
//
// It also measured the wrong thing. A workflow cap is not derivable on its own,
// because what must fit is the core TOGETHER: a project with a lean resume can
// afford a fat contract and vice versa. The defect that actually bit — core
// 8,015 tokens against an 8,000 budget, making ADR-009 unsatisfiable — was
// invisible to a workflow-only measurement, and would be caught by this one.
//
// Strictly READ-ONLY and advisory: Pass when every core is within the cap, Info
// when one or more are not. Never Fail — the same deliberate stance as
// CheckResumeCaps: there is no typed write path to gate (any agent holding Bash
// can write either file), so prevention is unachievable in-process. The
// un-bypassable gate lives in the LiveVault canary, not here.
//
// A healthy state is SILENT: within the cap there are no details and no numbers,
// just the pass row. Absence is never a violation — a missing Projects/
// directory, and a project lacking either file, report nothing.
func CheckCoreFloor(v *storage.Vault) Result {
	r := Result{Name: "Core floor"}

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
	var violations []coreViolation
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		resume, rok := fileSize(filepath.Join(projectsDir, name, "resume.md"))
		workflow, wok := fileSize(filepath.Join(projectsDir, name, "workflow.md"))
		if !rok && !wok {
			// Neither artifact present — nothing to measure, not a violation.
			continue
		}
		scanned++
		viol := coreViolation{
			Project: name, Resume: resume, Workflow: workflow,
			HasResume: rok, HasWorkflow: wok,
		}
		if viol.total() > CoreMaxBytes {
			violations = append(violations, viol)
		}
	}

	if len(violations) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d core within cap", scanned)
		return r
	}

	sort.Slice(violations, func(i, j int) bool { return violations[i].Project < violations[j].Project })
	r.Status = Info
	r.Summary = fmt.Sprintf("%d of %d core over cap", len(violations), scanned)
	for _, viol := range violations {
		r.Details = append(r.Details,
			fmt.Sprintf("  %s: %s core (%s + %s; cap %d bytes)",
				viol.Project, humanKB(viol.total()),
				half("resume", viol.Resume, viol.HasResume),
				half("workflow", viol.Workflow, viol.HasWorkflow), CoreMaxBytes))
	}
	r.Details = append(r.Details,
		"resume.md + workflow.md are the ADR-009 inviolable core: the bootstrap must deliver them WHOLE.",
		fmt.Sprintf("Over %d bytes the core cannot fit its share of the payload budget, so the shed ladder", CoreMaxBytes),
		"must amputate something core — the failure ADR-009 exists to make impossible.",
		"Fix by moving derivable content OUT (history -> iterations.md, task indexes -> vp_list_tasks),",
		"not by shrinking the operating contract: the contract sets the budget, not the reverse.")
	return r
}

// fileSize reports a regular file's size, and whether it is a regular file at
// all. A directory or a missing path reports ok=false rather than a zero that
// would silently read as "empty but present".
func fileSize(path string) (int, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0, false
	}
	return int(info.Size()), true
}
