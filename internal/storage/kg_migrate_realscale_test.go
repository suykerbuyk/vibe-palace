// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// TestApplyMigration_RealScale is the goof-proof-against-reality proof. It is
// ENV-GATED (VP_MIGRATE_REALSCALE) so CI skips it, and operates ONLY on a cp -r
// COPY of the live vault in t.TempDir() — the original is never touched.
//
// It copies the real palace/ subtree (where every triple lives) into an isolated
// temp vault, git-inits + commits the copy so the refuse-on-dirty gate passes,
// runs the full migration, and asserts the data survived: total count preserved,
// per-project KGStats.TripleCount unchanged, every triple queryable both sides,
// the tree fully flat, a second run idempotent, and the format stamped.
func TestApplyMigration_RealScale(t *testing.T) {
	if os.Getenv("VP_MIGRATE_REALSCALE") == "" {
		t.Skip("set VP_MIGRATE_REALSCALE=1 to run the high-fidelity real-vault copy test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Resolve the real vault via config vault_path (a neutral cwd forces the
	// global config path, not a cwd override).
	vaultPath, _, err := ResolveVaultPath(os.TempDir())
	if err != nil {
		t.Skipf("cannot resolve vault_path: %v", err)
	}
	srcPalace := filepath.Join(vaultPath, "palace")
	if fi, err := os.Stat(srcPalace); err != nil || !fi.IsDir() {
		t.Skipf("real vault palace/ absent at %s: %v", srcPalace, err)
	}

	// Copy ONLY the palace subtree (all triples live under it) + the vault
	// .gitignore, into an isolated temp vault. We NEVER write to the original.
	copyRoot := t.TempDir()
	if out, err := exec.Command("cp", "-r", srcPalace, filepath.Join(copyRoot, "palace")).CombinedOutput(); err != nil {
		t.Fatalf("cp -r palace: %s: %v", out, err)
	}
	if data, err := os.ReadFile(filepath.Join(vaultPath, ".gitignore")); err == nil {
		if werr := os.WriteFile(filepath.Join(copyRoot, ".gitignore"), data, 0o644); werr != nil {
			t.Fatal(werr)
		}
	}

	// git init + commit the copy so the dirty-tree gate is satisfied.
	gitRun(t, copyRoot, "init", "-b", "main")
	gitRun(t, copyRoot, "config", "user.email", "test@example.com")
	gitRun(t, copyRoot, "config", "user.name", "Test User")
	gitRun(t, copyRoot, "add", "-A")
	gitRun(t, copyRoot, "commit", "-m", "baseline", "--quiet")

	clean, err := GitStatusClean(copyRoot)
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if !clean {
		t.Fatal("copy tree not clean after commit; the dirty-tree gate would refuse")
	}

	v := NewVault(copyRoot)

	// Per-project TRUE (recursive) pre-count — this is the count KGStats must
	// report AFTER flattening (KGStats' single-level glob undercounts while
	// nested).
	projects, err := os.ReadDir(filepath.Join(copyRoot, "palace"))
	if err != nil {
		t.Fatal(err)
	}
	preCount := map[string]int{}
	preTotal := 0
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		td := filepath.Join(copyRoot, "palace", p.Name(), "kg", "triples")
		if _, err := os.Stat(td); err != nil {
			continue
		}
		c := countTripleFilesRecursive(t, td)
		preCount[p.Name()] = c
		preTotal += c
	}
	t.Logf("real-scale pre-count: %d triples across %d projects", preTotal, len(preCount))
	if preTotal < 10000 {
		t.Fatalf("pre-count %d is implausibly low for the real vault", preTotal)
	}

	m, err := v.ApplyTripleFilenameMigration()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.HasBlockers() {
		t.Fatalf("real vault produced blockers: %d collisions, %d bad files", len(m.Collisions), len(m.BadFiles))
	}
	if m.Collapsed != 0 {
		t.Errorf("expected 0 collapses on real data, got %d", m.Collapsed)
	}
	if m.Scanned != preTotal {
		t.Errorf("Scanned = %d, want %d (pre-count)", m.Scanned, preTotal)
	}

	// Stamp + stage, exactly as the command does.
	if err := surface.WriteFormat(copyRoot, surface.RequiredDataFormat); err != nil {
		t.Fatalf("WriteFormat: %v", err)
	}
	if err := GitAdd(copyRoot, "palace"); err != nil {
		t.Fatalf("stage palace: %v", err)
	}
	if err := GitAddForce(copyRoot, filepath.Join(".vibe-palace", "vault.toml")); err != nil {
		t.Fatalf("stage stamp: %v", err)
	}

	// (a) total flat-count == pre-count.
	postTotal := 0
	for name := range preCount {
		td := filepath.Join(copyRoot, "palace", name, "kg", "triples")
		flat := countTripleFilesRecursive(t, td)
		postTotal += flat

		// (d) tree fully flat.
		if hasNestedDir(td) {
			t.Errorf("project %q still has nested subdirs after migration", name)
		}

		// (b) per-project KGStats.TripleCount unchanged vs pre.
		stats, err := v.KGStats(name)
		if err != nil {
			t.Fatalf("project %q KGStats: %v", name, err)
		}
		if stats.TripleCount != preCount[name] {
			t.Errorf("project %q KGStats.TripleCount = %d, want %d", name, stats.TripleCount, preCount[name])
		}
	}
	if postTotal != preTotal {
		t.Errorf("post total flat-count = %d, want %d", postTotal, preTotal)
	}

	// (c) every triple queryable subject- AND object-side (bounded sample/project).
	for name := range preCount {
		triples, err := v.ListTriples(name)
		if err != nil {
			t.Fatalf("project %q ListTriples: %v", name, err)
		}
		if err := v.spotCheckBothSides(name, triples); err != nil {
			t.Errorf("project %q both-side queryability: %v", name, err)
		}
	}

	// (f) format stamped.
	if f, err := surface.ReadFormat(copyRoot); err != nil || f != surface.RequiredDataFormat {
		t.Errorf("format stamp = %d (err %v), want %d", f, err, surface.RequiredDataFormat)
	}

	// (e) idempotent second run renames 0.
	m2, err := v.ApplyTripleFilenameMigration()
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if m2.Renamed != 0 || m2.Collapsed != 0 {
		t.Errorf("second run mutated: Renamed=%d Collapsed=%d, want 0/0", m2.Renamed, m2.Collapsed)
	}
	if m2.AlreadyOK != preTotal {
		t.Errorf("second run AlreadyOK = %d, want %d (idempotent)", m2.AlreadyOK, preTotal)
	}
}
