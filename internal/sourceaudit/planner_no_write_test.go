// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"strings"
	"testing"
)

// The tests for the planner-write rule.
//
// # Why this file exists
//
// The rule's only exercise used to be TestSourceAuditGate running against the
// live tree, where it is SILENT BY DESIGN — the planner does not acquire a
// writer, so the rule emits nothing. From CI's point of view a silent rule and
// a deleted rule are the same observation. The rule was known to work only
// because it had been broken by hand once, in a scratch copy of the tree, and
// the proof lived in a pane's scrollback.
//
// # There is deliberately no vacuity test here
//
// Every sibling syntactic rule carries a /VACUOUS floor and a test for it
// (vault_write_funnel_test.go, git_env_funnel_test.go, surface_remediation_test.go).
// This rule has neither, and that is a decision rather than an omission: a floor
// asserts a rule keeps FINDING things, and this rule's healthy steady state is
// zero findings. A floor on matches would be red forever and would be deleted
// within a week. The anchor check below is the correct analogue for a rule that
// is silent when healthy. See planner_no_write.go's absent-anchor block.

// plannerFixture is one synthetic package carrying every shape the rule must
// separate. Each function is a claim about the rule and is asserted below in
// BOTH directions — a rule that only ever proves it can fire is half a rule, and
// the half it is missing is the one that keeps it enabled.
//
// It declares BOTH guarded names, so the anchor check has nothing to report
// against this fixture and the write findings stand alone.
const plannerFixture = `package fixture

import (
	"os"

	"example.com/storage"
)

// WRITE: the direct reintroduction this rule exists for. This is the manual
// break that proved the rule works, relocated out of a scratch tree.
func planBoardFieldsMigration(root string) error {
	_ = storage.NewVault(root)
	return nil
}

// WRITE: the second guarded planner, reaching a different sink. Both names and
// both sink families matter; a test that only covers one arm goes green when
// half the rule is deleted.
func predictFormatStamp(root string) error {
	return os.WriteFile(root, nil, 0o644)
}

// CLEAN: the executor is precisely where the write BELONGS. Flagging it is the
// failure mode that gets a gate disabled.
func executeBoardFieldsMigration(root string) error {
	_ = storage.NewVault(root)
	return nil
}

// CLEAN: a planner that only reads. It calls something whose name is not a
// write verb at all.
func planBoardFieldsMigrationReadOnly(root string) error {
	_, err := os.ReadFile(root)
	return err
}
`

// isPlannerAnchor reports whether a finding is the absent-anchor sentinel rather
// than a real write finding.
//
// Every small fixture legitimately trips it — a one-file fixture cannot declare
// both guarded planners unless it happens to be about them — exactly as every
// sibling's /VACUOUS sentinel is tripped by a one-file fixture. Filtering by
// symbol is the same move hasRealEnvIsolationBypass makes for the same reason.
func isPlannerAnchor(f Finding) bool {
	return strings.HasPrefix(f.Symbol, "sourceaudit.plannerNoWrite/ABSENT/")
}

// plannerWriteFindings runs the audit over src and returns its real (non-anchor)
// planner-write findings.
func plannerWriteFindings(t *testing.T, src string) []Finding {
	t.Helper()
	findings, err := Run(writeFixture(t, src))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []Finding
	for _, f := range findings {
		if f.Kind == KindPlannerWrite && !isPlannerAnchor(f) {
			out = append(out, f)
		}
	}
	return out
}

// plannerWriteIDs is plannerWriteFindings reduced to symbols, for membership
// assertions.
func plannerWriteIDs(t *testing.T, src string) []string {
	t.Helper()
	var out []string
	for _, f := range plannerWriteFindings(t, src) {
		out = append(out, f.Symbol)
	}
	return out
}

// TestPlannerNoWriteFlagsADirectWrite is the rule's mutation proof, and it is
// the test the whole file exists for.
//
// Proven red at four different layers before being trusted — each break is a
// one-line production edit, and each fails in a different place:
//
//   - delete "NewVault" from plannerWriteCalls        -> the NewVault arm goes absent
//   - delete "planBoardFieldsMigration" from plannerFuncs -> that planner goes absent
//   - delete "WriteFile" from plannerWriteCalls       -> the predictFormatStamp arm goes absent
//   - comment out plannerNoWrite(files) in sourceaudit.go:243 -> everything goes absent
//
// A test that survives only one of those is not proof the rule works; it is
// proof that one map entry exists.
func TestPlannerNoWriteFlagsADirectWrite(t *testing.T) {
	got := plannerWriteIDs(t, plannerFixture)
	for _, want := range []string{
		"fixture.planBoardFieldsMigration -> NewVault",
		"fixture.predictFormatStamp -> WriteFile",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is a planner acquiring a writer and the rule did not flag it. "+
				"A ratchet that cannot see the defect it was written for is coverage in name only.\n  got: %v",
				want, got)
		}
	}
}

// TestPlannerNoWriteFindingNamesTheRuleAndTheCallSite pins what the task
// actually requires of the finding: that a reader can act on it without opening
// the analyzer.
//
// It asserts the STRUCTURAL claims — the Kind, the call site, and that Detail
// names the planner and the sink involved. It deliberately does NOT pin the
// remediation prose: a test that matches on the advice makes the advice
// un-editable, and this repository already carries a standing finding against
// pinning a Description against behaviour.
func TestPlannerNoWriteFindingNamesTheRuleAndTheCallSite(t *testing.T) {
	var got *Finding
	for _, f := range plannerWriteFindings(t, plannerFixture) {
		if f.Symbol == "fixture.planBoardFieldsMigration -> NewVault" {
			got = &f
			break
		}
	}
	if got == nil {
		t.Fatal("the planner-write finding is missing entirely; TestPlannerNoWriteFlagsADirectWrite has the detail")
	}

	if got.Kind != KindPlannerWrite {
		t.Errorf("finding must name the rule: Kind = %q, want %q", got.Kind, KindPlannerWrite)
	}
	// The call site, not merely the file: a finding that cannot point at the line
	// sends the reader hunting through a function for the call that tripped it.
	if !strings.Contains(got.Pos, "fixture.go:") {
		t.Errorf("finding must name the call site; Pos = %q, want a fixture.go:LINE reference", got.Pos)
	}
	// Detail must name the SYMBOLS — the planner and the sink — so the finding
	// stands on its own in a CI log. The wording around them is free to change.
	for _, sym := range []string{"planBoardFieldsMigration", "storage.NewVault"} {
		if !strings.Contains(got.Detail, sym) {
			t.Errorf("finding Detail must name the symbol %q so it is actionable without opening the "+
				"analyzer; detail: %s", sym, got.Detail)
		}
	}
}

// TestPlannerNoWriteDoesNotFlagTheExecutor pins the precision claim, and it is
// the half that keeps the gate enabled. The executor acquiring a writer is not a
// defect — it is the entire point of the planner/executor split. A rule that
// reddens correct code teaches everyone to wave off its findings, and the
// wave-off is permanent.
//
// Break: add "executeBoardFieldsMigration" to plannerFuncs.
func TestPlannerNoWriteDoesNotFlagTheExecutor(t *testing.T) {
	got := plannerWriteIDs(t, plannerFixture)
	for _, unwanted := range []string{
		"fixture.executeBoardFieldsMigration -> NewVault",
		"fixture.planBoardFieldsMigrationReadOnly -> ReadFile",
	} {
		if slices.Contains(got, unwanted) {
			t.Errorf("%s is correct code and the rule flagged it — a noisy gate is a disabled gate.\n  got: %v",
				unwanted, got)
		}
	}
}

// plannerFuncLitFixture carries the planner as a package-level func-literal var.
//
// It is a SEPARATE fixture rather than another function in plannerFixture
// because plannerFuncs matches the binding name, so expressing this shape means
// binding the name planBoardFieldsMigration a second time — which cannot happen
// in one package. ungated_writer_test.go splits funcLiteralWriterFixture out for
// the same reason.
const plannerFuncLitFixture = `package fixture

import "example.com/storage"

// WRITE: a planner bound as a package-level func-literal var. Before
// bindingScopes replaced the FuncDecl-only walk, a body in this position had no
// entry at all and a direct write inside it was invisible.
var planBoardFieldsMigration = func(root string) error {
	_ = storage.NewVault(root)
	return nil
}
`

// TestPlannerNoWriteFlagsAFuncLiteralVarPlanner pins the reason this rule walks
// bindingScopes(f) rather than FuncDecls.
//
// Break: swap bindingScopes(f) for a FuncDecl-only loop over f.ast.Decls. The
// shared-fixture tests stay green — every planner in plannerFixture is a
// FuncDecl — and only this one goes red, which is the point of separating it.
func TestPlannerNoWriteFlagsAFuncLiteralVarPlanner(t *testing.T) {
	got := plannerWriteIDs(t, plannerFuncLitFixture)
	want := "fixture.planBoardFieldsMigration -> NewVault"
	if !slices.Contains(got, want) {
		t.Errorf("a planner bound as a package-level func-literal var acquired a writer and the rule "+
			"did not flag it. A call built inside a var func-literal has no entry in the walk at all "+
			"unless bindingScopes enumerates it.\n  got: %v", got)
	}
}

// plannerOneHopFixture routes the write one hop out of the planner, into a
// helper the rule does not name.
const plannerOneHopFixture = `package fixture

import "example.com/storage"

// The helper the rule does not name, and therefore cannot see through.
func sneakyPlannerWrite(root string) { _ = storage.NewVault(root) }

// The planner no longer calls a write verb DIRECTLY, so the rule is silent —
// even though this function still causes the write.
func planBoardFieldsMigration(root string) error {
	sneakyPlannerWrite(root)
	return nil
}
`

// TestPlannerNoWriteBlindSpotOneHopHelperIsNotSeen records the rule's ACTUAL
// reach, so the next reader learns it from the suite instead of rediscovering it
// in a scratch tree.
//
// 🔴 THIS TEST PINS A KNOWN WEAKNESS, NOT A REQUIREMENT. The rule documents its
// own false negative at planner_no_write.go:40-46 — "a write reached INDIRECTLY
// — through a helper this rule does not name — is invisible to it" — and this is
// that sentence, executable.
//
// 🔴 IF THIS TEST FAILS, THE RULE IMPROVED. That is not a regression and this is
// not a contract. Closing the blind spot was out of scope for the task that
// added this test, but it was never ruled against. If you closed it
// deliberately: DELETE THIS TEST in the same commit, and update the rule's
// doc comment to match its new reach. Do NOT narrow the rule to keep this green
// — that is the one move that turns a recorded limitation into a permanent one.
//
// There is deliberately NO break test for this case. Every other test in this
// file is proven by an edit that makes it red; the edit that would make this one
// red is "improve the rule", which is the change this test exists to PERMIT
// rather than to demand. Faking a break here would mean asserting the blind spot
// closes, which is the opposite of what is being recorded.
func TestPlannerNoWriteBlindSpotOneHopHelperIsNotSeen(t *testing.T) {
	got := plannerWriteIDs(t, plannerOneHopFixture)
	if slices.Contains(got, "fixture.planBoardFieldsMigration -> NewVault") {
		t.Fatalf("the one-hop helper IS now flagged — the rule's reach grew beyond what "+
			"planner_no_write.go:40-46 documents. Delete this test and update that comment; "+
			"do not narrow the rule to restore this.\n  got: %v", got)
	}
}

// plannerRenamedFixture is the tree after an ordinary refactor renames the
// planner without updating plannerFuncs.
const plannerRenamedFixture = `package fixture

import "example.com/storage"

// Renamed from planBoardFieldsMigration. Nothing in the analyzer notices: the
// guarded name is a map key, not a symbol, so there is no compile error — and
// the rule now matches no body at all.
func planBoardFields(root string) error {
	_ = storage.NewVault(root)
	return nil
}
`

// TestPlannerNoWriteAnchorFiresWhenAPlannerIsMissing is the positive proof of
// the anchor check, and it guards the failure mode that costs the least to cause
// and is hardest to see.
//
// Renaming a planner leaves plannerFuncs matching zero bodies. There is no
// compile error, no write finding, and — critically — every other test in this
// file stays GREEN, because their fixtures carry the names the map declares
// rather than the names cmd/vp uses. Without this check the rule can be disarmed
// by a Tuesday refactor and nothing anywhere says so.
//
// Break: delete the absent-anchor block at the end of plannerNoWrite.
func TestPlannerNoWriteAnchorFiresWhenAPlannerIsMissing(t *testing.T) {
	findings, err := Run(writeFixture(t, plannerRenamedFixture))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var anchors []string
	var detail string
	for _, f := range findings {
		if f.Kind == KindPlannerWrite && isPlannerAnchor(f) {
			anchors = append(anchors, f.Symbol)
			detail = f.Detail
		}
	}

	for _, want := range []string{
		"sourceaudit.plannerNoWrite/ABSENT/planBoardFieldsMigration",
		"sourceaudit.plannerNoWrite/ABSENT/predictFormatStamp",
	} {
		if !slices.Contains(anchors, want) {
			t.Errorf("a guarded planner name exists nowhere in the tree and the rule did not say so. "+
				"The rule is guarding nothing for %q and reporting nothing, which is exactly the "+
				"silent-false-negative this package exists to eliminate.\n  anchors: %v", want, anchors)
		}
	}

	// A separate symbol per name, or the two collapse to one baseline ID and
	// ruling on one silently rules on the other. Same reason surfaceRemediation
	// suffixes its vacuity symbol.
	if len(anchors) < 2 {
		t.Errorf("each absent name needs its own symbol, got %d: %v", len(anchors), anchors)
	}

	// An anchor somebody baselines is a rule somebody deleted, so the finding has
	// to say so itself — the reader who sees it will otherwise silence it exactly
	// like real debt.
	if !strings.Contains(detail, "Do NOT baseline this entry") {
		t.Errorf("the anchor finding must tell the reader not to baseline it.\n  detail: %s", detail)
	}
}

// TestPlannerNoWriteAnchorIsSilentOnTheLiveTree is the half that runs against
// the REAL corpus rather than a fixture, and it is the one that would catch the
// rename on the day it happened.
//
// A bug that passes every fixture-based test and dies on the real tree is this
// project's signature failure, and the fixture above proves only that the anchor
// CAN fire. This proves it is not firing spuriously today — that both guarded
// names really do resolve against cmd/vp as they are spelled in plannerFuncs.
//
// Re-derive what it is checking against:
//
//	grep -rn "func planBoardFieldsMigration\|func predictFormatStamp" --include='*.go' cmd/ internal/
//
// If this goes red, do not edit plannerFuncs to match whatever the tree now says
// without reading the diff first: the same edit repairs an honest rename and
// hides a deleted planner.
func TestPlannerNoWriteAnchorIsSilentOnTheLiveTree(t *testing.T) {
	findings, err := Run(repoRoots...)
	if err != nil {
		t.Fatalf("Run over the live tree: %v", err)
	}
	for _, f := range findings {
		if f.Kind == KindPlannerWrite && isPlannerAnchor(f) {
			t.Errorf("the anchor fired against the real tree: %s\n  %s", f.Symbol, f.Detail)
		}
	}
}
