// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// seedNote writes a real session note so the project is not truly empty but has
// no local index built (the unbuilt fixture).
func seedNote(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	testutil.InitProject(t, v.Root, project)
	dir, err := v.SessionDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-05-13-aaaa0000-01.md"), []byte("# note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBootstrapCarriesCoverageBesideKGSnapshot (Scope 8): a project with
// authored content but no local index reports kg_snapshot AND the coverage
// state together, so a zero kg count is read as "unbuilt on this host" rather
// than "nothing recorded".
func TestBootstrapCarriesCoverageBesideKGSnapshot(t *testing.T) {
	vault, resolver := testSetup(t)
	seedNote(t, vault, "test-proj")

	br := assembleBootstrap(resolver, vault, "test-proj", "", "", "", nil, true)

	if br.IndexCoverage == nil {
		t.Fatal("index_coverage is absent on a project with content")
	}
	if br.IndexCoverage.State == "" || br.IndexCoverage.Reason == "" {
		t.Fatalf("coverage state/reason empty: %+v", br.IndexCoverage)
	}
	// A migrated vault with notes and no built tier reads unbuilt.
	if br.IndexCoverage.State != "unbuilt" {
		t.Errorf("state = %q, want unbuilt", br.IndexCoverage.State)
	}
	// kg_snapshot is present (KGStats returns a zero-valued block, nil error for
	// a project with no graph), so the two ride together.
	if br.KGSnapshot == nil {
		t.Error("kg_snapshot is absent, so coverage has nothing to disambiguate")
	}
}

// TestBootstrapAbsentProjectReadsAbsent: a project with nothing reads absent.
func TestBootstrapAbsentProjectReadsAbsent(t *testing.T) {
	vault, resolver := testSetup(t)
	testutil.InitProject(t, vault.Root, "test-proj")
	br := assembleBootstrap(resolver, vault, "test-proj", "", "", "", nil, true)
	if br.IndexCoverage == nil || br.IndexCoverage.State != "absent" {
		t.Fatalf("coverage = %+v, want state absent", br.IndexCoverage)
	}
}

// TestBootstrapCoverageNeverTakesRunLock (Scope 5): assembling the bootstrap —
// which reads the run lock's holder record for the coverage instrument — takes
// the run lock 0 times.
func TestBootstrapCoverageNeverTakesRunLock(t *testing.T) {
	vault, resolver := testSetup(t)
	seedNote(t, vault, "test-proj")

	tries := 0
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		if ev == indexstore.RunTry {
			tries++
		}
	})
	defer restore()

	_ = assembleBootstrap(resolver, vault, "test-proj", "", "", "", nil, true)
	if tries != 0 {
		t.Fatalf("bootstrap tried the run lock %d time(s), want 0", tries)
	}
}

// TestBootstrapCoverageLoadsNoEmbedder: coverage is computed over a no-embedder
// engine whose lazy constructor returns an error. A populated coverage report
// therefore proves the constructor was never called — had the coverage path
// tried to embed, CoverageState would have failed and the report would be nil.
func TestBootstrapCoverageLoadsNoEmbedder(t *testing.T) {
	vault, resolver := testSetup(t)
	seedNote(t, vault, "test-proj")
	br := assembleBootstrap(resolver, vault, "test-proj", "", "", "", nil, true)
	if br.IndexCoverage == nil {
		t.Fatal("coverage is nil — the no-embedder engine failed, i.e. the coverage path tried to embed")
	}
}
