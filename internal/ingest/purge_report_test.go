// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

// KG honesty, rebuild side (plan v3 §3, C2/C3): the purge result is kept in
// ONE per-project field, appended to the aggregate by RunResult.add, and
// survives every error return of Rebuild, `--all` with a later failure
// included.
//
// The new RunResult field is read by NAME through reflect, so these compile
// before the change and fail on the absent field.

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// purgesOf returns RunResult.Purges as a slice of reflect values.
func purgesOf(t *testing.T, res RunResult) []reflect.Value {
	t.Helper()
	f := reflect.ValueOf(res).FieldByName("Purges")
	if !f.IsValid() {
		t.Fatal("RunResult has no Purges field")
	}
	out := make([]reflect.Value, f.Len())
	for i := range out {
		out[i] = f.Index(i)
	}
	return out
}

func purgeString(t *testing.T, p reflect.Value, name string) string {
	t.Helper()
	f := p.FieldByName(name)
	if !f.IsValid() {
		t.Fatalf("ProjectPurge has no field %s", name)
	}
	return f.String()
}

// failingEngine wraps the real engine; its RebuildExplicit fails for project
// `failOn`, AFTER the purge of that project has already committed.
type failingEngine struct {
	*search.Engine
	failOn string
}

var errInjected = errors.New("injected: engine rebuild failed")

func (e failingEngine) RebuildExplicit(ctx context.Context, project string) (search.RebuildStats, error) {
	if project == e.failOn {
		return search.RebuildStats{}, errInjected
	}
	return e.Engine.RebuildExplicit(ctx, project)
}

// seedIngested ingests one archive per project with the tier INCLUDED so a
// later --drop-transcripts has something to purge.
func (f *fixture) seedIngested(projects ...string) {
	f.t.Helper()
	for _, p := range projects {
		f.ensureLedger(p)
		f.archiveOf(p, "S-"+p, transcript(t0, p, 2), day(1))
	}
	if _, err := f.rebuild(RebuildOptions{All: true, FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		f.t.Fatalf("seed rebuild: %v", err)
	}
}

// C2: a purge that committed, followed by a failing engine rebuild, still
// reaches the caller through the returned (partial) RunResult.
//
// Q1: the real Rebuild. Q2: the injected failure is absent by default. Q3: the
// error really is the injected one and the purge ran (ChunksDropped > 0 is
// asserted in the --all test below via the project name).
//
// Mutation (M16): set res.Purge only on the success return of rebuildProject.
func TestRebuildKeepsThePurgeWhenALaterStepFails(t *testing.T) {
	f := newFixture(t)
	f.seedIngested("alpha")

	d := f.excludedDeps()
	d.Engine = failingEngine{Engine: f.eng, failOn: "alpha"}
	res, err := f.rebuildD(d, RebuildOptions{Project: "alpha", DropTranscripts: true, FreeSpace: &fakeFree{ok: plentyFree}})
	if !errors.Is(err, errInjected) {
		t.Fatalf("Rebuild returned %v, want the injected engine failure", err)
	}
	ps := purgesOf(t, res)
	if len(ps) != 1 || purgeString(t, ps[0], "Project") != "alpha" {
		t.Fatalf("the committed purge was lost on the error return: Purges = %v", ps)
	}
}

// C2: `--all`, a later target fails: the earlier target's purge is still in the
// aggregate (RunResult.add appended it), and the failing target's own purge,
// which had committed, is there too.
//
// Mutation (M16): drop the append in RunResult.add.
func TestAllRebuildKeepsEarlierPurgesWhenALaterTargetFails(t *testing.T) {
	f := newFixture(t)
	f.seedIngested("alpha", "beta")

	d := f.excludedDeps()
	d.Engine = failingEngine{Engine: f.eng, failOn: "beta"}
	res, err := f.rebuildD(d, RebuildOptions{All: true, DropTranscripts: true, FreeSpace: &fakeFree{ok: plentyFree}})
	if !errors.Is(err, errInjected) {
		t.Fatalf("Rebuild returned %v, want the injected failure", err)
	}
	var got []string
	for _, p := range purgesOf(t, res) {
		got = append(got, purgeString(t, p, "Project"))
	}
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("Purges projects = %v, want [alpha beta]: alpha's purge must survive beta's failure", got)
	}
}

// C3: a chunks-fingerprint mismatch discards the store BEFORE the purge, so the
// purge reports zero; the report must say the store was discarded earlier.
//
// Mutation (M12): force StoreDiscardedEarlier to "".
func TestThePurgeReportNamesAnEarlierDiscard(t *testing.T) {
	f := newFixture(t)
	f.seedIngested("alpha")
	f.setStale("alpha", chunkFP)

	d := f.excludedDeps()
	res, err := f.rebuildD(d, RebuildOptions{Project: "alpha", DropTranscripts: true, FreeSpace: &fakeFree{ok: plentyFree}})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	ps := purgesOf(t, res)
	if len(ps) != 1 {
		t.Fatalf("Purges = %d entries, want 1", len(ps))
	}
	if got := purgeString(t, ps[0], "StoreDiscardedEarlier"); got != "chunks" {
		t.Errorf("StoreDiscardedEarlier = %q, want \"chunks\": the purge is a no-op over a store the fingerprint mismatch had already emptied", got)
	}
}

// The control: no mismatch, no earlier discard.
func TestThePurgeReportDoesNotClaimADiscardWithoutOne(t *testing.T) {
	f := newFixture(t)
	f.seedIngested("alpha")
	res, err := f.rebuildD(f.excludedDeps(), RebuildOptions{Project: "alpha", DropTranscripts: true, FreeSpace: &fakeFree{ok: plentyFree}})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	ps := purgesOf(t, res)
	if len(ps) != 1 {
		t.Fatalf("Purges = %d entries, want 1", len(ps))
	}
	if got := purgeString(t, ps[0], "StoreDiscardedEarlier"); got != "" {
		t.Errorf("StoreDiscardedEarlier = %q with no mismatch", got)
	}
}
