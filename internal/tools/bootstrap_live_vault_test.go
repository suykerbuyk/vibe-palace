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

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/resumezone"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestBootstrapLiveVaultFitsItsOwnBudget is the canary this whole task turns on,
// and it is deliberately NOT a fixture test.
//
// 🔴 IT AUTO-DISCOVERS THE VAULT AND RUNS IN `make test`, ON PURPOSE. The obvious
// alternative — gate it behind VP_LIVE_VAULT, like the vaultaudit canary — would
// make it a test that runs when someone REMEMBERS to run it. This project has a
// name for that: capability built, nothing invokes it. It is the root cause this
// epic is named after, and shipping the fix for it behind an opt-in flag nobody
// sets would be a joke at its own expense. It skips cleanly where there is no
// vault (CI, a fresh clone), which is the only case that needs the escape hatch.
//
// 🔴 -count=1 IS NOT OPTIONAL WHEN YOU EDIT THE VAULT. The vault lives OUTSIDE the
// module, so `go test` cannot see that its contents changed and will serve a
// CACHED verdict — an instrument confidently describing a vault it did not look
// at, which is the exact failure this epic is named after. It bit the author of
// the vaultaudit canary; see internal/vaultaudit/archive_test.go.
//
// 🔴 AND A FIXTURE HERE WOULD BE WORSE THAN NO TEST. The defect being pinned is
// that the payload overran its own budget ON THE LIVE VAULT while every unit test
// in the tree was green. A seeded resume proves only that the ladder works on a
// resume the test's own author wrote — which is precisely how note_path stayed
// empty for six months behind a green suite. The bar, from the task: "Drive the
// real vp_bootstrap_context against the LIVE vault and measure the returned bytes."
func TestBootstrapLiveVaultFitsItsOwnBudget(t *testing.T) {
	// 🔴 EXACTLY ONE SKIP IS LEGITIMATE: this host has no vault at all (CI, a
	// fresh clone). Every OTHER way of not measuring is a FAILURE, deliberately.
	//
	// The reason is the lesson the windows-lock job taught this repo on
	// 2026-07-26: `go test` prints a bare `ok` for a package whose tests all
	// SKIPPED, so a skip is visually identical to a pass. That job sat green-ish
	// and un-run for 20 CI runs. A canary that quietly declines to measure is
	// worth less than no canary, because it also supplies false confidence.
	//
	// So once the host CLAIMS a vault, a missing root / unresolvable project /
	// absent resume.md is a broken setup on a machine that believes it is
	// covered — the precise silent-green state this test exists to deny. Fail.
	root := os.Getenv("VP_LIVE_VAULT")
	explicit := root != ""
	if root == "" {
		v, err := storage.OpenVaultGlobal()
		if err != nil {
			t.Skipf("no vault configured on this host — the one legitimate skip: %v", err)
		}
		root = v.Root
	}
	origin := "global config"
	if explicit {
		origin = "VP_LIVE_VAULT"
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("this host claims a vault at %q (via %s) but it is not present: %v — "+
			"the canary cannot measure, and skipping here would report a green gate that never ran",
			root, origin, err)
	}
	project := os.Getenv("VP_LIVE_PROJECT")
	if project == "" {
		project = "vibe-palace"
	}
	vault := storage.NewVault(root)
	resolver := vpctx.NewResolver(root)
	dir, err := vault.ProjectDir(project)
	if err != nil {
		t.Fatalf("project %q not resolvable in the vault at %q (via %s): %v — "+
			"set VP_LIVE_PROJECT if this vault uses a different slug", project, root, origin, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "resume.md")); err != nil {
		t.Fatalf("project %q has no resume.md in the vault at %q: %v — "+
			"there is nothing to measure, so this is a broken setup, not a skip", project, root, err)
	}

	// maxTokens=0 ⇒ the DEFAULT budget, and slim=false ⇒ stdio, the transport
	// every IDE agent actually uses. Both matter: the bug was invisible on the
	// HTTP path (which passes slim=true) and only ever bit the default.
	br := AssembleBootstrap(resolver, vault, project, 0, "", "", false)

	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The REAL default, not a copy of it — a canary asserting against its own
	// private constant would keep passing after the budget moved.
	const defaultMaxTokens = DefaultBootstrapMaxTokens
	tokens := len(raw) / 4

	// Record the grep, never the count: print the census, assert the invariant.
	t.Logf("project=%s payload=%d bytes ≈ %d tokens (budget %d)", project, len(raw), tokens, defaultMaxTokens)
	t.Logf("  resume=%d workflow=%d tasks=%d(%d open) directive=%d",
		len(br.Resume), len(br.Workflow), sizeOf(t, br.ActiveTasks), br.ActiveTaskCount,
		len(br.PostBootstrapInstructions))
	if br.Budget != nil {
		t.Logf("  budget: over=%v estimated=%d shed=%v", br.Budget.OverBudget, br.Budget.EstimatedTokens, br.Budget.Shed)
	} else {
		t.Log("  budget: nil (nothing shed, inside budget)")
	}

	if tokens > defaultMaxTokens {
		t.Errorf("live payload is %d tokens against its own default budget of %d — the ladder ran out of rungs. shed=%v",
			tokens, defaultMaxTokens, shedOf(br))
	}

	// Headroom is a SIGNAL, not an assertion — deliberately. A margin on the
	// total measures the wrong quantity: the ladder absorbs a fat contract by
	// shedding OTHER rungs, so the total barely moves. Measured 2026-07-26 —
	// growing the live workflow.md by 6 KB moved the payload only 7,072 → 7,391
	// tokens because active_tasks was shed to pay for it. A margin would have
	// stayed green through exactly the regression it was proposed to catch.
	t.Logf("  headroom=%d tokens (signal only — the gate is the core-integrity assertion below)",
		defaultMaxTokens-tokens)

	// 🔴 THE GATE: ADR-009 — the inviolable core is delivered WHOLE or not at all.
	//
	// This asserts the invariant the budget arithmetic cannot: shedRungTier
	// (context_tools.go) classifies resume->pinned and workflow->excerpt as
	// shedTierCore, and coreShed derives ShedCore from Shed at report time. A
	// payload can sit comfortably under budget while shedding a core rung — that
	// is the state the live vault is in today — so total-token slack is not
	// evidence the contract arrived.
	//
	// 🔴 THIS FAILS ON THE LIVE VAULT TODAY, ON MERIT, AND THAT IS THE POINT.
	// shed_core is ["resume->pinned"]: resume.md alone exceeds the whole 8,000-token
	// budget, so the rung must keep firing until resume.md shrinks to ~6-10 KB
	// (the resume-as-task-index work). Operator decision 2026-07-26: ship it red
	// rather than skip it, because a skipped assertion is the same silent-success
	// defect this epic exists to delete. DO NOT loosen this to make the suite
	// green — fix the payload, or the instrument is lying again.
	if br.Budget != nil && len(br.Budget.ShedCore) > 0 {
		t.Errorf("bootstrap shed a CORE rung: %v (ADR-009: the core is delivered whole or the call fails loud). payload=%d tokens, full shed=%v",
			br.Budget.ShedCore, tokens, br.Budget.Shed)
	}

	// The behavioral contract specifically must never arrive excerpted. An agent
	// holding two-thirds of the rules does not know which third it is missing —
	// that is the ADR-006 prose-as-enforcement trap, re-entered through the
	// budget. Asserted separately from ShedCore so the failure names the cause.
	//
	// NB the diagnostic must NOT print len(br.Workflow) as "the contract size":
	// once this rung fires that field holds the EXCERPT, so it reads as a tiny
	// number safely under the cap — an error message arguing against itself.
	// Report what it actually is, and point at the check that measures the source.
	if br.Budget != nil && sliceHas(br.Budget.Shed, shedWorkflow) {
		t.Errorf("the workflow contract was excerpted (%q in shed) — an agent cannot follow rules it did not receive. "+
			"the served workflow field is now a %d-byte excerpt of a contract that exceeded the %d-byte cap; "+
			"run `vp check --check workflow-caps` for the source size",
			shedWorkflow, len(br.Workflow), bootstrapExcerptCap)
	}

	// THE ALERTS MUST SURVIVE. They are the highest-value thing in the payload and
	// they ride in the tail, which is exactly what a host truncates first.
	if br.PostBootstrapInstructions == "" {
		t.Error("post_bootstrap_instructions is empty — the alerts (friction / staleness / health / over-budget) ride in it")
	}

	// A reduced resume must still be reachable and still CAS-able. resume_sha256
	// covers the FULL RAW file; a digest of the pinned zone would match nothing on
	// disk and would fail every compare-and-set a writer makes after paging the
	// body back through resume_uri.
	if br.Budget != nil && sliceHas(br.Budget.Shed, shedResumePinned) {
		if !strings.Contains(br.Resume, resumezone.ResumePinMarker) {
			t.Error("resume was shed to its pinned zone but carries no pin marker — the zone splitter kept the wrong lines")
		}
		if !strings.Contains(br.Resume, br.ResumeURI) {
			t.Error("shed resume carries no resume_uri: the full body is unreachable")
		}
		_, _, wantSha, err := resolver.ResolveDigest("resume", project)
		if err != nil {
			t.Fatalf("ResolveDigest: %v", err)
		}
		if br.ResumeSha256 != wantSha {
			t.Errorf("resume_sha256 %q is not the digest of the full body %q — every CAS write by this session would fail", br.ResumeSha256, wantSha)
		}
	}
}

func sliceHas(xs []string, want string) bool {
	return slices.Contains(xs, want)
}

func shedOf(br BootstrapResult) []string {
	if br.Budget == nil {
		return nil
	}
	return br.Budget.Shed
}

func sizeOf(t *testing.T, v any) int {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(raw)
}
