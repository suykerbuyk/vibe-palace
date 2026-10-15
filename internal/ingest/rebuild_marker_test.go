// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- rebuild-corpus marker helpers (U1 / ADR-016 decision 5) ----------------

func (f *fixture) noteRebuild(project string) {
	f.t.Helper()
	if err := indexstore.NoteRebuild(context.Background(), f.v, project, indexstore.NoTimeout); err != nil {
		f.t.Fatalf("NoteRebuild(%s): %v", project, err)
	}
}

func (f *fixture) rebuildWanted(project string) bool {
	f.t.Helper()
	want, err := indexstore.ReadRebuildWanted(f.v, project)
	if err != nil {
		f.t.Fatalf("ReadRebuildWanted(%s): %v", project, err)
	}
	return want
}

func (f *fixture) gen(project string) indexstore.Gen {
	f.t.Helper()
	g, err := indexstore.ReadGeneration(f.v, project)
	if err != nil {
		f.t.Fatalf("ReadGeneration(%s): %v", project, err)
	}
	return g
}

// writeNote writes a non-empty session note under Projects/<p>/sessions, so a
// project with no archives still has a corpus RebuildExplicit can embed.
func (f *fixture) writeNote(project, stem, body string) {
	f.t.Helper()
	dir, err := f.v.SessionDir(project)
	if err != nil {
		f.t.Fatalf("SessionDir(%s): %v", project, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	content := "---\ndate: 2026-05-13\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// TestRebuildMarkerWriteDuringReleaseIsNeverLost (U1, "never lost", m3): a
// NoteRebuild that lands after the holder's last inbox read and before
// ReleaseAndRecheck — the beforeReleaseFn seam — for a project the run never
// targeted is not dropped, AND the NEXT trigger actually serves it (rebuilds beta
// and clears the marker). The second half is what makes this catch a release-path
// bug: mere file survival would pass even if the want never got served.
func TestRebuildMarkerWriteDuringReleaseIsNeverLost(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	// beta has a corpus so the next trigger's rebuild of it bumps the generation.
	f.writeNote("beta", "s1", strings.Repeat("Beta note on the ledger and chunk store. ", 20))

	// beta's marker is written during alpha's release window — after the target
	// set was fixed, so beta is NOT a target this run and the want is not served.
	var once sync.Once
	beforeReleaseFn = func() { once.Do(func() { f.noteRebuild("beta") }) }
	t.Cleanup(func() { beforeReleaseFn = nil })

	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	if !f.rebuildWanted("beta") {
		t.Fatal("a rebuild-corpus marker written during the release window was lost; it must stay durable for the next trigger")
	}

	// The next trigger must SERVE the durable want: a fresh rebuild run sweeps
	// beta's marker (markedProjects), rebuilds beta, and clears the marker.
	beforeReleaseFn = nil
	betaBefore := f.gen("beta")
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if f.rebuildWanted("beta") {
		t.Fatal("the next trigger did not serve beta's durable want (marker still set)")
	}
	if f.gen("beta").Gen <= betaBefore.Gen {
		t.Fatalf("the next trigger did not actually rebuild beta: gen %+v -> %+v", betaBefore, f.gen("beta"))
	}
}

// markerProbe wraps the real engine and records whether the rebuild-corpus marker
// was still set when RebuildExplicit was called, so a test can pin that the clear
// happens AFTER the engine rebuild, not before. All other CacheEngine /
// rebuildEngine methods come from the embedded *search.Engine.
type markerProbe struct {
	*search.Engine
	v                      *storage.Vault
	sawMarkerDuringRebuild bool
}

func (m *markerProbe) RebuildExplicit(ctx context.Context, project string) (search.RebuildStats, error) {
	if want, _ := indexstore.ReadRebuildWanted(m.v, project); want {
		m.sawMarkerDuringRebuild = true
	}
	return m.Engine.RebuildExplicit(ctx, project)
}

// TestRebuildClearsMarkerOnlyAfterTheEngineRebuild (U1, m4): pins the order — the
// marker is still set when RebuildExplicit runs, and cleared only after. A clear
// moved above the engine rebuild makes sawMarkerDuringRebuild false and fails.
func TestRebuildClearsMarkerOnlyAfterTheEngineRebuild(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	f.noteRebuild("alpha")

	probe := &markerProbe{Engine: f.eng, v: f.v}
	d := Deps{Vault: f.v, Engine: probe, Embedder: f.emb, GraphHealer: NewEngineHealer(f.eng)}
	if _, err := Rebuild(context.Background(), d, RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !probe.sawMarkerDuringRebuild {
		t.Fatal("the marker was cleared BEFORE the engine rebuild; it must be cleared only after the build commits and bumps the generation")
	}
	if f.rebuildWanted("alpha") {
		t.Fatal("the marker was not cleared after the rebuild")
	}
}

// TestIngestRunDoesNotLoopOnRebuildMarker (M1): a drain-archives (KindIngest) Run
// holding the lock with a rebuild-corpus marker set for an archive-less project
// must TERMINATE (not re-acquire forever on a token it cannot serve, which would
// hold the vault run lock against the rebuild that would serve it) and must NOT
// clear the marker (it has not rebuilt).
func TestIngestRunDoesNotLoopOnRebuildMarker(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	f.noteRebuild("beta") // archive-less; an ingest run can never serve it

	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), f.deps(), RunOptions{VaultRoot: f.v.Root, Project: "alpha"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ingest Run did not terminate — it looped on an unservable rebuild marker (M1)")
	}
	if !f.rebuildWanted("beta") {
		t.Fatal("an ingest Run cleared beta's marker; only a `vp index rebuild` may serve it")
	}
}

// TestInScopePendingSurfacesRebuildTokenOnlyToRebuildRuns (M1): the rebuild token
// is surfaced to a `vp index rebuild` run (for its ReleaseAndRecheck seen
// membership) but NEVER to an ingest run (which would loop on it).
func TestInScopePendingSurfacesRebuildTokenOnlyToRebuildRuns(t *testing.T) {
	f := newFixture(t)
	f.writeNote("beta", "s1", "beta has a corpus")
	f.noteRebuild("beta")

	ing, err := inScopePending(f.deps(), RunOptions{VaultRoot: f.v.Root, Project: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ing, rebuildToken("beta")) {
		t.Fatal("an ingest run saw the rebuild token; it cannot serve it and would re-acquire on it forever")
	}

	reb, err := inScopePending(f.deps(), RunOptions{VaultRoot: f.v.Root, RebuildRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(reb, rebuildToken("beta")) {
		t.Fatal("a rebuild run did not see the rebuild token; its seen-membership (decision 5) needs it")
	}
}

// TestRebuildClearsMarkerAfterGenerationBump (U1, "post-generation-bump
// clear"): a marker set before a rebuild is cleared by the run, and only after
// the engine rebuild committed and bumped the generation.
func TestRebuildClearsMarkerAfterGenerationBump(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	f.noteRebuild("alpha")
	if !f.rebuildWanted("alpha") {
		t.Fatal("precondition: the marker was not set")
	}
	before := f.gen("alpha")

	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if f.rebuildWanted("alpha") {
		t.Fatal("the marker was not cleared after a completed rebuild")
	}
	after := f.gen("alpha")
	if after.Gen <= before.Gen {
		t.Fatalf("the generation did not bump: before %+v, after %+v — the clear must follow the committed build", before, after)
	}
}

// TestRebuildStoppedKeepsTheMarker: a run stopped by --max-archives embedded
// only part of the corpus, so the want is NOT served — the marker stays for the
// next trigger (the clear is tied to a completed embed build, not to the pass
// merely running).
func TestRebuildStoppedKeepsTheMarker(t *testing.T) {
	f := newFixture(t)
	for i := range 3 {
		f.archiveOf("alpha", "B"+string(rune('0'+i)), transcript(t0, "b", 2), day(10+i))
	}
	f.ensureLedgerWithBaseline("alpha")
	f.noteRebuild("alpha")

	res, err := f.rebuild(RebuildOptions{Project: "alpha", MaxArchives: 1, FreeSpace: &fakeFree{ok: plentyFree}})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if res.Stopped != "max archives" {
		t.Fatalf("stopped = %q, want \"max archives\"", res.Stopped)
	}
	if !f.rebuildWanted("alpha") {
		t.Fatal("a stopped rebuild cleared the marker; a partial corpus must leave the want for the next trigger")
	}
}

// TestContendedArchivelessMarkerGetsRebuilt (U1, "contended notes-only case"):
// an archive-less project carrying a rebuild-corpus marker, recorded while a run
// is invoked for a DIFFERENT project, still gets its engine rebuild and its
// marker cleared — because `vp index rebuild` is the only path that serves a
// corpus want, and an ingest pass could never serve an archive-less one.
func TestContendedArchivelessMarkerGetsRebuilt(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))

	// beta: no archives, a notes-only corpus, and a rebuild-corpus marker.
	f.writeNote("beta", "s1", strings.Repeat("Beta note on the ledger, chunk store and embed cache design. ", 20))
	f.noteRebuild("beta")
	betaBefore := f.gen("beta")

	// The run names only alpha; beta is never a positional target or --all.
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if f.rebuildWanted("beta") {
		t.Fatal("beta's archive-less marker was not served by a run for alpha; the want is stranded")
	}
	betaAfter := f.gen("beta")
	if betaAfter.Gen <= betaBefore.Gen {
		t.Fatalf("beta was not actually rebuilt: generation before %+v, after %+v", betaBefore, betaAfter)
	}
}

// TestServedRebuildMarkerDoesNotReacquire (U1, "seen membership / never
// loops"): the holder's loop-prevention contract — ReleaseAndRecheck treats a
// rebuild token NOT in seen as pending work (re-acquires), but a token the hold
// has already served and placed in seen does NOT make it re-acquire, so a run
// never loops on its own served want.
func TestServedRebuildMarkerDoesNotReacquire(t *testing.T) {
	f := newFixture(t)
	held, ok, err := indexstore.TryRunLock(f.v, indexstore.KindRebuild, "alpha")
	if err != nil || !ok {
		t.Fatalf("acquire run lock: ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() { _ = held.Release() })

	tok := rebuildToken("alpha")
	pending := func() ([]string, error) { return []string{tok}, nil }

	// Unseen token: pending work, so the holder re-acquires.
	again, err := held.ReleaseAndRecheck(map[string]struct{}{}, pending)
	if err != nil {
		t.Fatalf("ReleaseAndRecheck (unseen): %v", err)
	}
	if !again {
		t.Fatal("did not re-acquire on an unseen rebuild token")
	}

	// Served token in seen: the holder must NOT re-acquire (never loops).
	again, err = held.ReleaseAndRecheck(map[string]struct{}{tok: {}}, pending)
	if err != nil {
		t.Fatalf("ReleaseAndRecheck (seen): %v", err)
	}
	if again {
		t.Fatal("re-acquired on a served rebuild token already in seen; the holder would loop on its own want")
	}
}
