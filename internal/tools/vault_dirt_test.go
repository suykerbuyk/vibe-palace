// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// bootstrapOn runs the real registered bootstrap tool against a vault.
func bootstrapOn(t *testing.T, vault *storage.Vault) BootstrapResult {
	t.Helper()
	tool := BootstrapContextTool(vpctx.NewResolver(vault.Root), vault, nil)
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj"}`))
	if err != nil {
		t.Fatalf("vp_bootstrap_context: %v", err)
	}
	br, ok := res.(BootstrapResult)
	if !ok {
		t.Fatalf("result type = %T, want BootstrapResult", res)
	}
	return br
}

// dirtyFile writes an uncommitted file into the vault at a vault-relative path.
func dirtyFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- the two directions -----------------------------------------------------

// TestBootstrapRaisesVaultDirtAlert is one half of the pin. A vault carrying an
// uncommitted task file must say so on the next session's FIRST call, in both
// the structured field and the directive.
func TestBootstrapRaisesVaultDirtAlert(t *testing.T) {
	vault := newGitBackedTestVault(t)
	const rel = "Projects/test-proj/tasks/hand-edited.md"
	dirtyFile(t, vault.Root, rel, "# Hand edited\n\n**Status:** pending\n**Priority:** high\n\nbody\n")

	br := bootstrapOn(t, vault)

	if br.VaultDirt == nil {
		t.Fatal("vault_dirt is nil on a vault with an uncommitted task file — " +
			"the session opens with no idea that nothing it produces can be saved")
	}
	if br.VaultDirt.Count != 1 {
		t.Errorf("count = %d, want 1 (paths %v)", br.VaultDirt.Count, br.VaultDirt.SamplePaths)
	}
	if len(br.VaultDirt.SamplePaths) != 1 || br.VaultDirt.SamplePaths[0] != rel {
		t.Errorf("paths = %v, want [%s]", br.VaultDirt.SamplePaths, rel)
	}

	// 🔴 THE ALERT MUST LEAD THE DIRECTIVE. Alerts are joined in append order
	// and lead the capability announcement, so a host that keeps only a prefix
	// keeps the alerts. An alert appended late lands on the wrong side of a cut,
	// which is the defect TestDirectiveCutKeepsAlertsAndLosesAnnouncement
	// records. Nothing else fires on this fixture, so this one is first.
	if !strings.HasPrefix(br.PostBootstrapInstructions, br.VaultDirt.Message) {
		t.Errorf("the dirt alert does not lead post_bootstrap_instructions:\n%s", br.PostBootstrapInstructions)
	}
}

// TestBootstrapIsSilentOnACleanVault is the other half, and it is the half that
// decides whether this alert is worth anything.
//
// 🔴 THE RULE IS "SILENT WHEN HEALTHY", NOT A HEADCOUNT (BootstrapResult's
// AuditStaleness field comment). An alert that fires on a clean vault teaches the
// reader to skim every alert on this payload, including the ones that mean the
// session cannot save its work. Asserting the negative is therefore not
// bookkeeping — it is the property the alert's value rests on.
func TestBootstrapIsSilentOnACleanVault(t *testing.T) {
	vault := newGitBackedTestVault(t)
	assertVaultClean(t, vault.Root)

	br := bootstrapOn(t, vault)

	if br.VaultDirt != nil {
		t.Errorf("vault_dirt is non-nil on a CLEAN vault: %#v", br.VaultDirt)
	}
	if strings.Contains(br.PostBootstrapInstructions, "VAULT DIRT") {
		t.Errorf("the directive carries a dirt alert on a clean vault:\n%s", br.PostBootstrapInstructions)
	}
}

// TestVaultDirtGatesOnGenuineDirtNotReported is the specific silence the review
// called out, and it fails on the obvious implementation.
//
// tidy's `Reported` is the full catch-all of everything it declined to sweep, and
// it INCLUDES Projects/<slug>/memory/ — deliberately-pending user memory that is
// expected content and never blocks a sync (decision 7). Gating on Reported would
// fire this alert on a vault that is behaving exactly as designed. The gate is
// genuineDirt: Reported minus that user content, taken from the same function
// SyncVault's refusal reads.
func TestVaultDirtGatesOnGenuineDirtNotReported(t *testing.T) {
	vault := newGitBackedTestVault(t)
	dirtyFile(t, vault.Root, "Projects/test-proj/memory/pending-note.md", "a memory awaiting harvest\n")

	// Premise, asserted rather than assumed: the file really is Reported, so a
	// Reported-gated alert would fire here.
	scan, err := storage.TidyScan(vault.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Reported) == 0 {
		t.Fatal("test premise broken: the memory file is not Reported, so this measures nothing")
	}

	if br := bootstrapOn(t, vault); br.VaultDirt != nil {
		t.Errorf("vault_dirt fired on deliberately-pending user memory: %#v — "+
			"the gate is reading Reported, not genuine dirt", br.VaultDirt)
	}
}

// TestVaultDirtIgnoresSweepableCaptureArtifacts is the second silence. A session
// note is a capture artifact: tidy sweeps it, SyncVault commits it at step 3, and
// it never blocks anything. Alerting on it would fire on every session that has
// written anything at all.
func TestVaultDirtIgnoresSweepableCaptureArtifacts(t *testing.T) {
	vault := newGitBackedTestVault(t)
	dirtyFile(t, vault.Root, "Projects/test-proj/sessions/2026-09-06-abc-01.md", "# A session\n")

	if br := bootstrapOn(t, vault); br.VaultDirt != nil {
		t.Errorf("vault_dirt fired on a sweepable capture artifact: %#v", br.VaultDirt)
	}
}

// TestVaultDirtAgreesWithTheSyncRefusal is the claim the alert actually makes.
//
// The message says vp_vault_sync REFUSES. That has to be true by construction,
// not by two filters happening to agree — internal/wrapstate/gitprobe.go is the
// standing evidence that a second, independently written dirt classifier drifts
// (it does not use sweepRules and is project-scoped, so it would have missed the
// sibling-project specimen this task was filed over). Both sides read
// storage.(*TidyResult).GenuineDirt; this asserts the consequence.
func TestVaultDirtAgreesWithTheSyncRefusal(t *testing.T) {
	vault := newGitBackedTestVault(t)
	dirtyFile(t, vault.Root, "Projects/test-proj/tasks/hand-edited.md", "# Hand edited\n")
	dirtyFile(t, vault.Root, "Knowledge/a-human-note.md", "prose\n")

	br := bootstrapOn(t, vault)
	if br.VaultDirt == nil {
		t.Fatal("vault_dirt is nil while the vault carries genuine dirt")
	}

	res, err := storage.SyncVault(vault.Root, []string{"origin"})
	if !res.Refused {
		t.Fatalf("SyncVault did not refuse (err %v) while the alert says it would", err)
	}
	if len(res.GenuineDirt) != br.VaultDirt.Count {
		t.Errorf("the alert reports %d dirty file(s); SyncVault refuses on %d (%v) — "+
			"the alert and the gate disagree about the same worktree",
			br.VaultDirt.Count, len(res.GenuineDirt), res.GenuineDirt)
	}
}

// TestVaultDirtCoversSiblingProjects pins the scope decision. The scan is
// whole-vault, not project-scoped, because the specimen that motivated this task
// belonged to a DIFFERENT project than the session reporting it and was ignored
// across two sessions of the reporting project. A project-scoped pathspec would
// be cheaper and would miss it by construction.
func TestVaultDirtCoversSiblingProjects(t *testing.T) {
	vault := newGitBackedTestVault(t)
	const rel = "Projects/other-project/tasks/a-sibling-task.md"
	dirtyFile(t, vault.Root, rel, "# Sibling\n")

	br := bootstrapOn(t, vault)
	if br.VaultDirt == nil {
		t.Fatal("vault_dirt is nil for a sibling project's dirty task file — the scan is project-scoped")
	}
	if len(br.VaultDirt.SamplePaths) != 1 || br.VaultDirt.SamplePaths[0] != rel {
		t.Errorf("paths = %v, want [%s]", br.VaultDirt.SamplePaths, rel)
	}
}

// TestVaultDirtSampleIsBounded pins the field's cost: the sample must not grow
// with the dirt it summarises. An unbounded record list in an alert's slot is the
// defect Health.RecentWarns was fixed for — it spent 28% of everything ahead of
// the bulk in the one region of the payload a host preview keeps.
//
// 🔴 IT MEASURES TWO DIRT LEVELS BECAUSE ONE LEVEL PROVES NOTHING. An earlier
// version asserted len(SamplePaths) == vaultDirtSampleN against a single
// fixture, which is a restatement of the slicing expression in computeVaultDirt
// and would hold for any value of the constant, including one large enough to
// blow the payload. The invariant here is the derivative: TWELVE dirty files and
// TWENTY-FOUR dirty files must produce the SAME sample width and DIFFERENT
// counts. The absolute width is enforced where it actually costs something —
// TestBootstrapInstrumentBlockWorstCaseFitsHostPreview, whose fixture derives
// its paths from vaultDirtSampleN so that raising the constant reddens the
// ceiling.
func TestVaultDirtSampleIsBounded(t *testing.T) {
	measure := func(t *testing.T, n int) *VaultDirt {
		t.Helper()
		vault := newGitBackedTestVault(t)
		for i := range n {
			dirtyFile(t, vault.Root, fmt.Sprintf("Knowledge/note-%02d.md", i), "prose\n")
		}
		br := bootstrapOn(t, vault)
		if br.VaultDirt == nil {
			t.Fatalf("vault_dirt is nil while the vault carries %d dirty files", n)
		}
		if br.VaultDirt.Count != n {
			t.Fatalf("count = %d, want %d", br.VaultDirt.Count, n)
		}
		return br.VaultDirt
	}

	small := measure(t, 12)
	large := measure(t, 24)

	if len(small.SamplePaths) != len(large.SamplePaths) {
		t.Errorf("the sample grew with the dirt: %d paths at count 12, %d at count 24 — "+
			"this field is a sample, and a sample that scales is a record list",
			len(small.SamplePaths), len(large.SamplePaths))
	}
	for _, vd := range []*VaultDirt{small, large} {
		if len(vd.SamplePaths) >= vd.Count {
			t.Errorf("SamplePaths carries %d of %d paths — it is the whole list, not a sample",
				len(vd.SamplePaths), vd.Count)
		}
	}
	// The count, not the sample, is what the reader is told.
	if !strings.Contains(large.Message, "24 uncommitted") {
		t.Errorf("message reports the sample rather than the count: %q", large.Message)
	}
}

// TestVaultDirtMessageNamesTheWholeBlastRadius is a prose pin, and it earns its
// place because the message is the only thing standing between a reader and the
// wrong conclusion.
//
// The task body framed the harm as "the task file survives only if someone
// remembers to sync". Source says worse: SyncVault refuses at step 2 and returns
// BEFORE the capture-artifact commit at step 3, so ONE dirty file wedges
// sessions, transcripts, drawers and KG triples too. A reader told only "some
// files are uncommitted" will under-react and lose a whole session's capture.
func TestVaultDirtMessageNamesTheWholeBlastRadius(t *testing.T) {
	msg := vaultDirtMessage(3)
	for _, want := range []string{"REFUSES", "sessions", "transcripts", "drawers", "KG triples", "vp_vault_tidy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the alert does not mention %q — a reader cannot tell what is actually blocked:\n%s", want, msg)
		}
	}
}

// TestVaultDirtScanIsBestEffort pins the degradation. A vault that is not a git
// repository (or a git that fails for any other reason) must produce NO alert and
// must not fail the bootstrap: an alert saying "I could not check" is not
// actionable and is not silent when healthy.
func TestVaultDirtScanIsBestEffort(t *testing.T) {
	vault := newTestVault(t) // a plain temp dir; no .git
	if vd := computeVaultDirt(vault.Root); vd != nil {
		t.Errorf("computeVaultDirt returned %#v for a non-repository vault, want nil", vd)
	}
	if br := bootstrapOn(t, vault); br.VaultDirt != nil {
		t.Errorf("vault_dirt is non-nil on a non-repository vault: %#v", br.VaultDirt)
	}
}

// TestVaultDirtScanCost measures what this actually adds to the session
// handshake, which is the hottest path in the system (190 took it from ~0.4 s to
// 0.012 s).
//
// 🔴 IT MEASURES, IT DOES NOT GATE. A wall-clock threshold on a shared CI box is
// a flake, and the number that matters is the one on the operator's real vault —
// 64,893 tracked files — not on a temp dir with four. So this asserts only the
// property that must hold everywhere (the scan completes inside the budget the
// handshake gives it) and LOGS the duration, on the live vault when there is one.
// Run `go test -run TestVaultDirtScanCost -v ./internal/tools/` to read it.
func TestVaultDirtScanCost(t *testing.T) {
	root := os.Getenv("VP_LIVE_VAULT")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), "obsidian", "vibe-palace-vault")
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Skipf("no live vault at %s — nothing to measure", root)
	}

	// Warm the page cache first; a cold first read measures the filesystem, not
	// the scan.
	_, _ = storage.TidyScanWithTimeout(root, vaultDirtScanBudget)

	const runs = 5
	var total time.Duration
	for range runs {
		start := time.Now()
		if _, err := storage.TidyScanWithTimeout(root, vaultDirtScanBudget); err != nil {
			t.Fatalf("TidyScanWithTimeout: %v", err)
		}
		total += time.Since(start)
	}
	avg := total / runs
	t.Logf("vault dirt scan over %s: %v average of %d warm runs (budget %v)", root, avg, runs, vaultDirtScanBudget)
	if avg > vaultDirtScanBudget {
		t.Errorf("the scan averages %v, over its own %v budget — every session start would degrade to silence", avg, vaultDirtScanBudget)
	}
}
