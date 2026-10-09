// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/search"
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

	// The bounded prefix brief carries the state (survives a host cut).
	if br.IndexCoverage == nil {
		t.Fatal("index_coverage (prefix brief) is absent on a project with content")
	}
	if br.IndexCoverage.State != "unbuilt" {
		t.Errorf("prefix state = %q, want unbuilt", br.IndexCoverage.State)
	}
	// The full report beside kg_snapshot carries the state and a reason.
	if br.IndexCoverageDetail == nil {
		t.Fatal("index_coverage_detail is absent on a project with content")
	}
	if br.IndexCoverageDetail.State != "unbuilt" || br.IndexCoverageDetail.Reason == "" {
		t.Fatalf("detail state/reason wrong: %+v", br.IndexCoverageDetail)
	}
	// kg_snapshot is present (KGStats returns a zero-valued block, nil error for
	// a project with no graph), so coverage and the KG counts ride together.
	if br.KGSnapshot == nil {
		t.Error("kg_snapshot is absent, so coverage has nothing to disambiguate")
	}
}

// TestIndexCoverageAlertScope (Scope 7): the advisory alert fires ONLY on stale
// and absent, is silent for the expected-after-install states, and stays within
// its prefix byte cap.
func TestIndexCoverageAlertScope(t *testing.T) {
	if indexCoverageAlert(search.Coverage{State: search.CoverageStale, Project: "p"}) == "" {
		t.Error("stale should raise an alert")
	}
	if indexCoverageAlert(search.Coverage{State: search.CoverageAbsent, Project: "p"}) == "" {
		t.Error("absent should raise an alert")
	}
	for _, st := range []search.CoverageState{
		search.CoverageLegacy, search.CoverageUnbuilt, search.CoverageNotes,
		search.CoveragePartial, search.CoverageCurrent,
	} {
		if msg := indexCoverageAlert(search.Coverage{State: st, Project: "p"}); msg != "" {
			t.Errorf("state %q should NOT alert, got %q", st, msg)
		}
	}
	// The longest alert stays within the prefix cap even at a 64-char slug.
	long := indexCoverageAlert(search.Coverage{State: search.CoverageStale, Project: strings.Repeat("s", 64)})
	if n := len([]rune(long)); n > indexCoverageAlertMax {
		t.Errorf("alert is %d runes, over the cap %d", n, indexCoverageAlertMax)
	}
}

// TestBootstrapCoverageIsInBoundedPrefix (D1): the index_coverage brief marshals
// AHEAD of head_of_queue, inside the bounded prefix a truncating host keeps — the
// whole point of the fix. Before it, the coverage instrument sat behind
// head_of_queue and was cut away on a <=2 KB-preview host. The full detail rides
// behind the boundary.
func TestBootstrapCoverageIsInBoundedPrefix(t *testing.T) {
	vault, resolver := testSetup(t)
	seedNote(t, vault, "test-proj")
	br := assembleBootstrap(resolver, vault, "test-proj", "", "", "", nil, true)
	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	cov := strings.Index(s, `"index_coverage":`)
	bulk := strings.Index(s, `"head_of_queue":`)
	if cov < 0 {
		t.Fatal("index_coverage is absent from the payload")
	}
	if cov > bulk {
		t.Errorf("index_coverage is at %d, AFTER head_of_queue at %d — it would be cut away on a truncating host", cov, bulk)
	}
	// The full detail rides behind the boundary (bulk), by design.
	if d := strings.Index(s, `"index_coverage_detail":`); d < 0 || d < bulk {
		t.Errorf("index_coverage_detail should ride behind head_of_queue (at %d, boundary %d)", d, bulk)
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

// TestBootstrapCeilingWith30Projects (Scope 10 / "Ceiling with 30 projects"):
// the bounded instrument prefix still fits the host preview with 30 projects in
// the vault — the per-project OTHERS tally rides in the bulk detail, not the
// prefix, so the prefix does not grow with the project count, and the live
// bootstrap carries the coverage brief in the prefix.
func TestBootstrapCeilingWith30Projects(t *testing.T) {
	vault, resolver := testSetup(t)
	for i := 0; i < 30; i++ {
		seedNote(t, vault, fmt.Sprintf("cov-proj-%02d", i))
	}

	br := assembleBootstrap(resolver, vault, "cov-proj-00", "", "", "", nil, true)
	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatal(err)
	}
	got := boundedPrefixBytes(t, raw)
	if got > hostPreviewSpecimen {
		t.Errorf("bounded prefix with 30 projects is %d B, over the %d B host preview", got, hostPreviewSpecimen)
	}
	if br.IndexCoverage == nil {
		t.Error("the coverage brief is absent from the prefix with 30 projects")
	}
	// The other-project tally rides in the bulk detail, where project count may
	// grow freely.
	if br.IndexCoverageDetail == nil {
		t.Fatal("the coverage detail is absent")
	}
	t.Logf("bounded prefix with 30 projects = %d B (budget %d)", got, hostPreviewSpecimen)
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
