// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The derived-residue filter (task split-and-merge-exclude-derived-palace-paths,
// Scopes 1-3): split and merge leave out files that are untracked, ignored AND
// a derived index path, and split's purge removes them as untracked rows.

const (
	residueDrawer = "palace/alpha/drawers/w/r/drawers.jsonl"
	residueLedger = "palace/alpha/ingested-archives.jsonl"
	residueBak    = "Projects/alpha/transcripts/x.manifest.json.abc.bak"
	looseDrawer   = "palace/alpha/drawers/loose/r/drawers.jsonl" // untracked, NOT ignored
)

// splitResidueVault is a migrated git source: the marker, the derived ignore
// lines, the fixture committed, and its drawers left untracked and ignored —
// v8 residue on a migrated host. It also holds an ignored *.bak manifest.
func splitResidueVault(t *testing.T) string {
	t.Helper()
	splitGitEnv(t)
	root := splitFixtureVault(t, "alpha", "beta")
	if err := storage.ReconcileVaultGitignore(root); err != nil {
		t.Fatal(err)
	}
	writeSplitFile(t, root, residueLedger, "{}\n")
	writeSplitFile(t, root, residueBak, "{}\n")
	splitGit(t, root, "init", "-q", "-b", "main")
	splitGit(t, root, "add", "-A")
	splitGit(t, root, "commit", "-q", "-m", "migrated source")
	return root
}

func manifestPaths(m *splitManifest) []string {
	var out []string
	for _, e := range m.Entries {
		out = append(out, e.Path)
	}
	return out
}

// Untracked ignored drawer dropped by split; the ignored *.bak stays. Mutant:
// the filter defined but not applied at the split plan; "untracked and
// ignored" without the path condition (split-B1).
func TestVaultSplitPlan_LeavesOutDerivedResidueOnly(t *testing.T) {
	root := splitResidueVault(t)
	m, err := buildSplitManifest(storage.NewVault(root), splitPlanParams(splitDest(t), "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	paths := manifestPaths(m)
	for _, rel := range []string{residueDrawer, residueLedger} {
		if slices.Contains(paths, rel) {
			t.Errorf("residue %s is in the manifest", rel)
		}
	}
	if !slices.Contains(paths, residueBak) {
		t.Errorf("the ignored *.bak manifest left the manifest: %q", paths)
	}
}

// Untracked, not ignored: a derived-looking file git does not ignore stays.
// Mutant: over-broad filtering by path alone.
func TestVaultSplitPlan_KeepsAnUnignoredDerivedFile(t *testing.T) {
	root := splitResidueVault(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("palace/.local/\n*.bak\n.vp-locks/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	splitGit(t, root, "commit", "-q", "-am", "an older .gitignore")
	writeSplitFile(t, root, looseDrawer, "{}\n")
	m, err := buildSplitManifest(storage.NewVault(root), splitPlanParams(splitDest(t), "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(manifestPaths(m), looseDrawer) {
		t.Errorf("an untracked drawer git does not ignore left the manifest")
	}
}

// Apply plus purge, end to end, into a `vault init` destination: plan, apply,
// verify and purge complete; the destination inventory does not count the
// residue (computed against the SOURCE); purge's unaccounted check skips it,
// and its cleanup removes it, leaving cleanup_left empty. Mutants: the filter
// missing at the destination inventory or at the purge's unaccounted check;
// the set computed against the destination.
func TestVaultSplit_ResidueApplyVerifyPurge(t *testing.T) {
	root := splitResidueVault(t)
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")
	for _, action := range []string{"apply", "verify"} {
		p.Action = action
		if _, err := callSplit(t, root, p); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	for _, rel := range []string{residueDrawer, residueLedger} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("residue %s reached the destination (stat err %v)", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(residueBak))); err != nil {
		t.Errorf("the *.bak manifest did not travel: %v", err)
	}
	p.Action = "purge"
	p.DepartureTo = purgeLabel
	res, err := callSplit(t, root, p)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if left, _ := res["cleanup_left"].([]any); len(left) != 0 {
		t.Errorf("cleanup_left = %v, want nothing left", left)
	}
	for _, rel := range []string{residueDrawer, residueLedger} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("residue %s survived the purge (stat err %v)", rel, err)
		}
	}
}

// The destination inventory uses the SOURCE's residue: a destination file at
// a residue path is not counted. With the set computed against the destination
// (which has no such untracked file listed), verify would report it as a leak.
func TestVaultSplitVerify_InventoryUsesTheSourceResidue(t *testing.T) {
	root := splitResidueVault(t)
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")
	p.Action = "apply"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// A residue-path file at the destination, not tracked there: the source's
	// set names it, so verify does not count it.
	writeSplitFile(t, dest, residueDrawer, "{}\n")
	m, err := splitBindManifest(storage.NewVault(root), p)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := splitDestInventory(dest, p, m.Slugs, m.Residue)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Path == residueDrawer {
			t.Fatalf("the destination inventory counted the source's residue path %s", residueDrawer)
		}
	}
}

// Global walks untouched: an untracked, ignored file under an included
// vault-global class is handled exactly as before (it travels). Mutant:
// hooking the residue filter into splitSubtracted or walkSplitGlobal.
func TestVaultSplitPlan_GlobalWalksIgnoreTheResidueRule(t *testing.T) {
	root := splitResidueVault(t)
	writeSplitFile(t, root, "Audits/report.md.bak", "old\n")
	p := splitPlanParams(splitDest(t), "alpha")
	p.IncludeAudits = true
	m, err := buildSplitManifest(storage.NewVault(root), p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(manifestPaths(m), func(s string) bool { return strings.HasSuffix(s, "report.md.bak") }) {
		t.Errorf("an ignored file under an included global class left the manifest")
	}
}
