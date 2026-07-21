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
	root := os.Getenv("VP_LIVE_VAULT")
	if root == "" {
		v, err := storage.OpenVaultGlobal()
		if err != nil {
			t.Skipf("no vault configured on this host: %v", err)
		}
		root = v.Root
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("configured vault is not present on this host: %v", err)
	}
	project := os.Getenv("VP_LIVE_PROJECT")
	if project == "" {
		project = "vibe-palace"
	}
	vault := storage.NewVault(root)
	resolver := vpctx.NewResolver(root)
	dir, err := vault.ProjectDir(project)
	if err != nil {
		t.Skipf("project %q not resolvable in this vault: %v", project, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "resume.md")); err != nil {
		t.Skipf("project %q has no resume.md in this vault: %v", project, err)
	}

	// maxTokens=0 ⇒ the DEFAULT budget, and slim=false ⇒ stdio, the transport
	// every IDE agent actually uses. Both matter: the bug was invisible on the
	// HTTP path (which passes slim=true) and only ever bit the default.
	br := AssembleBootstrap(resolver, vault, project, 0, "", "", false)

	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const defaultMaxTokens = 8000
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
		if !strings.Contains(br.Resume, ResumePinMarker) {
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
