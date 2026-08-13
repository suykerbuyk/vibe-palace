// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/resumezone"
)

const (
	wfPin  = resumezone.ResumePinMarker
	wfDisp = resumezone.ResumeDisposableMarker
)

// The three parts of every workflow fixture below, kept as named constants so a
// test can assert on the exact prose that must (or must not) survive the digest.
const (
	wfPreamble  = "# test-proj — Workflow\n\n<!-- thin by design -->\n\n"
	wfRule      = "never write a vault file by hand from internal/tools"
	wfNav       = "resume.md is the current project state, iterations.md is the archive"
	wfUnruled   = "the paragraph nobody has ruled on yet"
	wfRuleSec   = "## Project-Specific Behavioral Notes\n" + wfPin + "\n\n- " + wfRule + "\n\n"
	wfNavSec    = "## Files\n" + wfDisp + "\n\n- " + wfNav + "\n\n"
	wfLiveSec   = "## Auto-memory\n\n- " + wfUnruled + "\n\n"
	wfPaddedNav = "## Files\n" + wfDisp + "\n\n" + wfNav + "\n\n"
)

// writeProjectWorkflow writes <vault>/Projects/<slug>/workflow.md, which is the
// project-tier override the resolver prefers over the embedded template.
func writeProjectWorkflow(t *testing.T, vaultRoot, project, body string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflow.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow.md: %v", err)
	}
}

// bootstrapWithWorkflow runs the real tool over a project whose workflow.md is
// exactly body, at the DEFAULT budget — which is the state that matters. The
// digest answers a HOST INLINE CAP, not max_tokens: the epic measured a
// 61,747-byte payload on which the ladder shed nothing and reported
// `budget: absent` while two thirds of the bytes never reached the model. A
// fixture that squeezed max_tokens would prove the ladder works and say nothing
// about the delivery property under test.
func bootstrapWithWorkflow(t *testing.T, body string) BootstrapResult {
	t.Helper()
	vault, resolver := testSetup(t)
	writeProjectWorkflow(t, vault.Root, "test-proj", body)
	tool := BootstrapContextTool(resolver, vault)
	br := bootstrapResult(t, tool, `{"project":"test-proj"}`)
	if br.Workflow == "" {
		t.Fatal("test premise broken: no workflow resolved at all, so nothing below measures the digest")
	}
	return br
}

// TestBootstrapWorkflowDigestDeliversThePinnedZone covers the happy path: a
// workflow that declares a pin zone arrives as that zone, behind the same banner
// a shed resume carries, naming workflow_uri.
//
// The banner and the URI are asserted together on purpose. A reduction that
// announces itself but does not say where the rest went leaves an agent knowing
// it is missing something and unable to fetch it, which is the failure shape
// this epic exists to delete.
func TestBootstrapWorkflowDigestDeliversThePinnedZone(t *testing.T) {
	br := bootstrapWithWorkflow(t, wfPreamble+wfRuleSec+wfNavSec)

	if !strings.HasPrefix(br.Workflow, bootstrapZoneBannerLead) {
		t.Errorf("the digested workflow carries no pinned-zone banner; it starts %q", first80(br.Workflow))
	}
	if !strings.Contains(br.Workflow, br.WorkflowURI) {
		t.Errorf("the banner does not name workflow_uri (%q) — the agent cannot fetch what the digest dropped:\n%s", br.WorkflowURI, br.Workflow)
	}
	// The pinned rule survives WHOLE; the section ruled disposable is gone.
	if !strings.Contains(br.Workflow, wfRule) {
		t.Errorf("the pinned rule did not survive the digest:\n%s", br.Workflow)
	}
	if strings.Contains(br.Workflow, wfNav) {
		t.Errorf("a section declared disposable was delivered inline anyway — the digest reduced nothing:\n%s", br.Workflow)
	}
	// The preamble is unconditionally inline: it is not a section, it has no
	// heading to rule on, and a marker stranded there declares nothing.
	if !strings.Contains(br.Workflow, "thin by design") {
		t.Errorf("the preamble was dropped — everything above the first H2 is unconditionally inline:\n%s", br.Workflow)
	}
	// The reduction is REPORTED. A payload whose contract is now a digest must
	// not answer "nothing was reduced".
	if br.Budget == nil || !slices.Contains(br.Budget.Shed, shedWorkflowDigest) {
		t.Errorf("budget does not name %s: %+v — a silent reduction is the instrument dishonesty this epic deletes", shedWorkflowDigest, br.Budget)
	}
}

// 🔴 THE HEADLINE. A workflow.md that declares NO pin zone is delivered WHOLE —
// byte for byte, no digest, no banner, no rung.
//
// Nine of the ten projects in the live vault carry no markers at all. Any
// behaviour here other than "exactly as before" silently degrades nine projects
// to improve one, and it degrades them in the direction that costs correctness
// rules. This is the same rule PinnedZone's `declared` half enforces for the
// resume, and the same rule resumeRungTier errs toward: a document the server
// cannot rule on is delivered, not guessed at.
func TestBootstrapUndeclaredWorkflowIsDeliveredWhole(t *testing.T) {
	whole := wfPreamble + "## Files\n\n- " + wfNav + "\n\n## Auto-memory\n\n- " + wfUnruled + "\n"
	br := bootstrapWithWorkflow(t, whole)

	if br.Workflow != whole {
		t.Errorf("an unmarked workflow.md was not delivered whole.\n got %q\nwant %q", br.Workflow, whole)
	}
	if strings.Contains(br.Workflow, bootstrapZoneBannerLead) {
		t.Error("an unmarked workflow.md arrived behind a pinned-zone banner — the server guessed which half of an unruled contract was safe to drop")
	}
	if br.Budget != nil && slices.Contains(br.Budget.Shed, shedWorkflowDigest) {
		t.Errorf("budget names %s for a workflow that declares no pin zone: %+v", shedWorkflowDigest, br.Budget)
	}
	// Silent when healthy: nothing was reduced, so there is no budget block at
	// all. This is the property that proves the nine unmarked projects see no
	// change whatsoever, not merely that their bytes survived.
	if br.Budget != nil {
		t.Errorf("budget = %+v on an unmarked workflow — nothing was reduced, so the report must be absent", br.Budget)
	}
}

// A marker stranded in the PREAMBLE declares nothing: it pins nothing that was
// not already inline, and honouring it would shed the whole body down to the H1
// on the strength of one stray line. Same rule as the resume, through the same
// parser — which is the point of not having written a second one.
func TestBootstrapWorkflowPreambleMarkerIsNotADeclaration(t *testing.T) {
	whole := "# test-proj — Workflow\n" + wfPin + "\n\n## Files\n\n- " + wfNav + "\n"
	br := bootstrapWithWorkflow(t, whole)
	if br.Workflow != whole {
		t.Errorf("a preamble-only marker was read as a pin zone.\n got %q\nwant %q", br.Workflow, whole)
	}
}

// A workflow that pins EVERY section digests to itself, so there is nothing to
// report and no banner to add. Mirrors shedResumeToPinnedZone's "a resume that
// pins everything sheds nothing".
func TestBootstrapWorkflowThatPinsEverythingIsNotDigested(t *testing.T) {
	whole := wfPreamble + "## Rules\n" + wfPin + "\n\n- " + wfRule + "\n"
	br := bootstrapWithWorkflow(t, whole)
	if strings.Contains(br.Workflow, bootstrapZoneBannerLead) {
		t.Errorf("a fully-pinned workflow was banded with a reduction banner:\n%s", br.Workflow)
	}
	if br.Budget != nil && slices.Contains(br.Budget.Shed, shedWorkflowDigest) {
		t.Errorf("budget names %s for a workflow that lost nothing: %+v", shedWorkflowDigest, br.Budget)
	}
}

// The digest is IDEMPOTENT: applying it to its own output is a no-op, so a
// second reduction cannot band a banner over a banner. The property comes from
// the size guard, not from a banner-sniffing special case — the zone of a zone
// is that zone, so the second pass measures equal and declines.
func TestBootstrapWorkflowDigestIsIdempotent(t *testing.T) {
	result := BootstrapResult{WorkflowURI: "vibe-palace://workflow/test-proj", Workflow: wfPreamble + wfRuleSec + wfNavSec}
	if !digestWorkflowToPinnedZone(&result) {
		t.Fatal("test premise broken: the first digest did not fire")
	}
	once := result.Workflow
	if digestWorkflowToPinnedZone(&result) {
		t.Error("the digest fired twice on the same payload")
	}
	if result.Workflow != once {
		t.Errorf("a second application changed the payload:\n got %q\nwant %q", first80(result.Workflow), first80(once))
	}
}

// TestWorkflowRungTierDerivation covers the derivation itself, table-driven,
// including every path that must ERR DOWNWARD to core.
//
// The rule under test: digesting the workflow is a core loss unless EVERY
// un-pinned section is positively declared disposable. Iteration 262 falsified
// this exact shape of claim when it was hard-coded for the resume rung — it was
// wrong for 8 of 8 live projects — so the workflow gets the derivation on day
// one rather than after its own incident.
func TestWorkflowRungTierDerivation(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		want     shedTier
	}{{
		name:     "one undeclared section makes the digest a core loss",
		workflow: wfPreamble + wfRuleSec + wfLiveSec,
		want:     shedTierCore,
	}, {
		name:     "every un-pinned section declared disposable is context",
		workflow: wfPreamble + wfRuleSec + wfNavSec,
		want:     shedTierContext,
	}, {
		name:     "one undeclared section among declared ones is still core",
		workflow: wfPreamble + wfRuleSec + wfNavSec + wfLiveSec,
		want:     shedTierCore,
	}, {
		name:     "a workflow that pins everything has nothing un-pinned to rule on",
		workflow: wfPreamble + wfRuleSec + "## More\n" + wfPin + "\n\n- also pinned\n",
		want:     shedTierContext,
	}, {
		// ERR DOWNWARD from here down. None of these can be ruled on.
		name:     "a workflow declaring no pin zone cannot be ruled on",
		workflow: wfPreamble + wfNavSec + wfLiveSec,
		want:     shedTierCore,
	}, {
		name:     "a body with no H2 sections at all",
		workflow: wfPreamble,
		want:     shedTierCore,
	}, {
		name:     "no workflow resolved at all",
		workflow: "",
		want:     shedTierCore,
	}, {
		// Fence-awareness comes from resumezone, the one reader — a marker quoted
		// as sample text declares nothing, here as everywhere else.
		name:     "a disposable marker quoted inside a code fence declares nothing",
		workflow: wfPreamble + wfRuleSec + "## Auto-memory\n\n```\n" + wfDisp + "\n```\n\n- " + wfUnruled + "\n",
		want:     shedTierCore,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := workflowRungTier(tc.workflow); got != tc.want {
				t.Errorf("workflowRungTier = %q, want %q", got, tc.want)
			}
		})
	}
}

// 🔴 THE TIER IS DERIVED FROM THE DOCUMENT, END TO END, THROUGH THE REAL TOOL.
//
// Two workflows differing by ONE MARKER LINE must produce two different
// shed_core reports on the same payload shape. A hard-coded tier — of either
// value — makes one of these two halves fail, which is the whole point: a
// constant here would be a claim about one project's editorial state written
// down as a property of every vault, and that claim has already been measured
// false once (iteration 262, 8 of 8 projects).
func TestBootstrapWorkflowDigestTierIsDerivedNotConstant(t *testing.T) {
	t.Run("an undeclared live section makes the digest a core loss", func(t *testing.T) {
		br := bootstrapWithWorkflow(t, wfPreamble+wfRuleSec+wfNavSec+wfLiveSec)
		if br.Budget == nil || !slices.Contains(br.Budget.Shed, shedWorkflowDigest) {
			t.Fatalf("test premise broken: the workflow was not digested: %+v", br.Budget)
		}
		if !slices.Contains(br.Budget.ShedCore, shedWorkflowDigest) {
			t.Errorf("`## Auto-memory` carries neither marker and left the payload, yet shed_core = %v — a dropped rule reported as no core loss is the exact silence shed_core exists to break", br.Budget.ShedCore)
		}
		// And the dropped section really is gone, so the report is about a real loss.
		if strings.Contains(br.Workflow, wfUnruled) {
			t.Error("the undeclared section survived the digest — this test would then be reporting a loss that did not happen")
		}
	})

	t.Run("every un-pinned section declared disposable is not", func(t *testing.T) {
		br := bootstrapWithWorkflow(t, wfPreamble+wfRuleSec+wfNavSec)
		if br.Budget == nil || !slices.Contains(br.Budget.Shed, shedWorkflowDigest) {
			t.Fatalf("test premise broken: the workflow was not digested: %+v", br.Budget)
		}
		if slices.Contains(br.Budget.ShedCore, shedWorkflowDigest) {
			t.Errorf("every un-pinned section is declared disposable, so the digest costs a lookup and not a rule — reporting a core loss is the alarm that cries wolf: %v", br.Budget.ShedCore)
		}
	})
}

// TestBootstrapDigestedWorkflowIsNeverExcerpted is the LADDER RECONCILIATION,
// asserted rather than described: the digest and the excerpt are two reductions
// of one field, and only one of them may ever touch a given payload.
//
// A digest is a deliberate selection of WHOLE sections. Excerpting it would cut
// a pinned rule mid-sentence at bootstrapExcerptCap — amputating exactly the
// content the marker exists to protect, which is why bootstrapZoneBanner does
// not truncate either. The excerpt rung survives for workflows that declare
// nothing, where there is no selection to honour.
func TestBootstrapDigestedWorkflowIsNeverExcerpted(t *testing.T) {
	// A pinned zone far over the excerpt cap, so the excerpt rung would fire if
	// it were still reachable.
	fat := wfPreamble + "## Project-Specific Behavioral Notes\n" + wfPin + "\n\n" +
		strings.Repeat("- "+wfRule+"\n", 200) + "\n" + wfPaddedNav

	vault, resolver := testSetup(t)
	writeProjectWorkflow(t, vault.Root, "test-proj", fat)
	tool := BootstrapContextTool(resolver, vault)

	// 🔴 THE BUDGET IS CHOSEN, NOT ARBITRARY, and getting it wrong is how this
	// test would silently stop measuring anything. It must be low enough that the
	// ladder REACHES the excerpt rung and high enough that excerpting would make
	// the payload FIT — because the give-back and the ADR-009 core restore both
	// undo a workflow shed that bought nothing, so at a hopeless budget the
	// excerpt is reverted anyway and removing the guard changes no observable
	// byte. Measured on this fixture: without the guard, max_tokens=2500 cuts the
	// 11,239-byte digest to 4,064 and the rung STICKS. It is the discriminating
	// budget; a lower one makes this a dead test.
	br := bootstrapResult(t, tool, `{"project":"test-proj","max_tokens":2500}`)

	if len(br.Workflow) <= bootstrapExcerptCap {
		t.Fatalf("test premise broken: the digest is %d bytes, under the %d-byte excerpt cap, so the excerpt rung could not have fired anyway", len(br.Workflow), bootstrapExcerptCap)
	}
	if br.Budget == nil {
		t.Fatal("no budget report on a payload that could not meet its budget")
	}
	// The honest outcome of refusing this rung: over budget, and saying so. The
	// contract arrives whole and the report is loud, which beats a payload that
	// fits because a correctness rule was cut in half.
	if !br.Budget.OverBudget {
		t.Errorf("over_budget=false at %d estimated tokens against max_tokens=2500 — the premise that this rung was the ladder's last resort no longer holds", br.Budget.EstimatedTokens)
	}
	if slices.Contains(br.Budget.Shed, shedWorkflow) {
		t.Errorf("shed names %s on a digested workflow: %v — a deliberate selection was re-cut by a blind prefix", shedWorkflow, br.Budget.Shed)
	}
	if !slices.Contains(br.Budget.Shed, shedWorkflowDigest) {
		t.Errorf("shed does not name %s: %v", shedWorkflowDigest, br.Budget.Shed)
	}
	if strings.Contains(br.Workflow, "⚠ excerpt") {
		t.Errorf("the digest was banded with the EXCERPT banner as well:\n%s", first80(br.Workflow))
	}
	// The pinned zone arrived whole: the LAST pinned line is still there, which a
	// prefix cut at bootstrapExcerptCap could not have left.
	if !strings.HasSuffix(strings.TrimRight(br.Workflow, "\n"), "- "+wfRule) {
		t.Error("the pinned zone does not end where the file's pinned zone ends — something truncated it")
	}
}

// TestBootstrapDigestPreservesTheWireOrder re-asserts the transport contract on
// a DIGESTED payload. The digest changes what is in the bulk; it must not move
// an instrument below the bulk or displace the terminal sentinel.
func TestBootstrapDigestPreservesTheWireOrder(t *testing.T) {
	br := bootstrapWithWorkflow(t, wfPreamble+wfRuleSec+wfNavSec+wfLiveSec)
	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc := string(raw)

	at := func(key string) int {
		i := strings.Index(doc, key)
		if i < 0 {
			t.Fatalf("%s is absent from the digested payload entirely", key)
		}
		return i
	}
	for _, instrument := range []string{`"budget"`, `"resume_uri"`, `"workflow_uri"`, `"resume_sha256"`, `"active_task_count"`} {
		for _, bulk := range []string{`"workflow":`, `"resume":`} {
			if at(instrument) > at(bulk) {
				t.Errorf("%s is at byte %d, AFTER %s at byte %d — a host cut inside the bulk takes the instrument with it",
					instrument, at(instrument), bulk, at(bulk))
			}
		}
	}
	if !strings.HasSuffix(doc, `,"complete":true}`) {
		tail := doc
		if len(tail) > 120 {
			tail = "…" + tail[len(tail)-120:]
		}
		t.Errorf("the digested payload does not END with the sentinel; tail = %s", tail)
	}
}

func first80(s string) string {
	if len(s) <= 80 {
		return s
	}
	return s[:80] + "…"
}
