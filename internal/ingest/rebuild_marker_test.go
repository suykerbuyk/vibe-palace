// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
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

// TestRebuildMarkerWriteDuringReleaseIsNeverLost (U1, "never lost"): a
// NoteRebuild that lands after the holder's last inbox read and before
// ReleaseAndRecheck — the beforeReleaseFn seam — for a project the run never
// targeted is not dropped. The want stays durable for the next trigger, exactly
// like an archive First written in the same window.
func TestRebuildMarkerWriteDuringReleaseIsNeverLost(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))

	// beta's marker is written during alpha's release window — after the target
	// set was fixed, so beta is NOT a target this run and the want is not served.
	var once sync.Once
	beforeReleaseFn = func() { once.Do(func() { f.noteRebuild("beta") }) }
	t.Cleanup(func() { beforeReleaseFn = nil })

	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if !f.rebuildWanted("beta") {
		t.Fatal("a rebuild-corpus marker written during the release window was lost; it must stay durable for the next trigger")
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
