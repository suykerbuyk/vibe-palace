// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

// Guards for the transcript tier's admission and rebuild behaviour.
//
// Each guard records its three answers:
//   Q1 PATH     — WIRING (argv/config file to a durable effect) or COMPONENT
//                 (one function driven with inputs the test supplies). A
//                 COMPONENT guard is never evidence for a wiring claim; the
//                 wiring guards for this feature live in cmd/vp.
//   Q2 VALUE    — the fixture's value differs from what the mutation falls
//                 back to.
//   Q3 PRESENCE — the fixture CONTAINS the thing the mutation acts on, and
//                 that is asserted BEFORE the call.

import (
	"context"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
)

// W3's subject MOVED. The precedence rule now lives in
// storage.ResolveTranscripts, because search.Engine became its second caller,
// and the ingest-side delegate that W3 drove is gone — the source audit's
// `uninvoked` rule caught it the moment nothing called it. The rule is guarded
// by storage.TestResolveTranscriptsIsTheOnePrecedenceRule; that a flag
// actually REACHES ingest from an argv slice is cmd/vp's
// TestTheTranscriptsFlagBeatsTheConfigFromArgv, and that it reaches the engine
// is TestTheTranscriptsFlagReachesTheSearchEngine. Nothing W3 asserted is
// unguarded.

// G5 — COMPONENT. TestTheAutomaticIngesterAdmitsNoArchiveWhenExcluded runs the
// SAME fixture twice, excluded and not-excluded.
//
// Q1 PATH: component. It drives ingest.Run with a Deps the test built.
// Q2 VALUE: the two arms differ in exactly one field, and assert opposite
// outcomes.
// Q3 PRESENCE: the NOT-EXCLUDED arm asserting > 0 IS the presence check, and
// it is empirical rather than asserted by hand: it proves the fixture's
// archives clear every OTHER admission arm in decide (the baseline set, the
// 8 MiB size cap, the failure limit, already-ledgered). A single excluded arm
// would be worthless, because "nothing ingested" is also what a fixture with
// no admissible archive produces.
//
// Mutation: delete the exclusion arm from decide. The excluded arm then
// commits > 0.
func TestTheAutomaticIngesterAdmitsNoArchiveWhenExcluded(t *testing.T) {
	// Arm 1: NOT excluded. This is the Q3 presence proof.
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))
	included := f.run(RunOptions{Project: "alpha", First: a.Manifest.SourceSHA256})
	if included.Committed == 0 {
		t.Fatalf("PRESENCE: the not-excluded arm committed nothing, so the fixture holds no archive "+
			"decide would admit and the excluded arm proves nothing: %+v", included)
	}

	// Arm 2: the same fixture shape, excluded.
	g := newFixture(t)
	g.ensureLedger("alpha")
	b := g.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))
	res, err := Run(context.Background(), g.excludedDeps(),
		RunOptions{VaultRoot: g.v.Root, Project: "alpha", First: b.Manifest.SourceSHA256})
	if err != nil {
		t.Fatalf("excluded run: %v", err)
	}
	if res.Committed != 0 || res.Failed != 0 {
		t.Fatalf("the automatic ingester admitted %d committed / %d failed archives with the "+
			"transcript tier excluded; decide's exclusion arm did not bind", res.Committed, res.Failed)
	}
	if n := g.store("alpha").CountChunks(indexstore.ArchiveOwner(b.Manifest.SourceSHA256)); n != 0 {
		t.Fatalf("the excluded run wrote %d archive-owned chunks", n)
	}
}

// G5b — COMPONENT. TestTheExclusionArmBindsTheExplicitPassToo: the EXPLICIT
// pass takes everything decide offers it (`if o.Explicit { return it, true }`),
// so an exclusion arm placed below that early return would bind the automatic
// drain and leave the days-long explicit backlog drain untouched — the cost
// this whole change exists to remove.
//
// Q1 PATH: component. Q2 VALUE: the control arm is the opposite setting.
// Q3 PRESENCE: the control run asserts > 0 archives the explicit pass takes.
//
// Mutation: move the exclusion arm below `if o.Explicit { return it, true }`
// in decide.
func TestTheExclusionArmBindsTheExplicitPassToo(t *testing.T) {
	// Control: the explicit pass DOES take these archives when not excluded.
	f := newFixture(t)
	f.ensureLedgerWithBaseline("alpha") // the historical backlog
	f.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))
	ctl, err := RunHeld(context.Background(), f.deps(), heldRunLock(t, f),
		RunOptions{VaultRoot: f.v.Root, Project: "alpha", Only: "alpha", Explicit: true, SkipHeal: true})
	if err != nil {
		t.Fatalf("control explicit pass: %v", err)
	}
	if ctl.Committed == 0 {
		t.Fatalf("PRESENCE: the explicit pass took nothing from the baseline set, so the excluded "+
			"arm below proves nothing: %+v", ctl)
	}

	// The same shape, excluded.
	g := newFixture(t)
	g.ensureLedgerWithBaseline("alpha")
	sha := g.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1)).Manifest.SourceSHA256
	res, err := RunHeld(context.Background(), g.excludedDeps(), heldRunLock(t, g),
		RunOptions{VaultRoot: g.v.Root, Project: "alpha", Only: "alpha", Explicit: true, SkipHeal: true})
	if err != nil {
		t.Fatalf("excluded explicit pass: %v", err)
	}
	if res.Committed != 0 {
		t.Fatalf("the EXPLICIT pass committed %d archives with the transcript tier excluded. "+
			"The explicit pass draining the baseline set is the days-long build; an exclusion arm "+
			"that binds only the automatic drain removes none of that cost", res.Committed)
	}
	if n := g.store("alpha").CountChunks(indexstore.ArchiveOwner(sha)); n != 0 {
		t.Fatalf("the excluded explicit pass wrote %d archive-owned chunks", n)
	}
}

// heldRunLock takes the vault run lock the way the rebuild driver does, so a
// guard can call RunHeld directly.
func heldRunLock(t *testing.T, f *fixture) *indexstore.RunLock {
	t.Helper()
	held, ok, err := indexstore.TryRunLock(f.v, indexstore.KindRebuild, "alpha")
	if err != nil || !ok {
		t.Fatalf("run lock: ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() { _ = held.Release() })
	return held
}

// G6 — COMPONENT. TestTheExplicitRebuildSkipsTheArchivePassWhenExcluded: step
// 4 is not entered at all, so no archive is listed, opened or ledgered.
//
// Q1 PATH: component (ingest.Rebuild with a Deps literal; W1 covers the argv).
// Q2 VALUE: the control run uses the opposite setting.
// Q3 PRESENCE: > 0 pending archives, asserted by the control run.
//
// Mutation: delete the `if transcripts {` guard around step 4 in
// rebuildProject, so the pass runs and decide alone refuses. The ledger-row
// assertion survives that (decide still refuses) but the SESSION-LINE
// assertion below does not: entering the pass creates the ledger, and this
// guard asserts no ledger was created at all.
func TestTheExplicitRebuildSkipsTheArchivePassWhenExcluded(t *testing.T) {
	// Control: not excluded, the pass runs and ingests.
	f := newFixture(t)
	f.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("control rebuild: %v", err)
	}
	if n := f.sessionLines("alpha", "A"); n == 0 {
		t.Fatal("PRESENCE: the control rebuild ledgered nothing, so the fixture has no pending archive " +
			"and the excluded arm proves nothing")
	}

	// Excluded.
	g := newFixture(t)
	sha := g.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1)).Manifest.SourceSHA256
	if _, err := g.rebuildD(g.excludedDeps(),
		RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("excluded rebuild: %v", err)
	}
	if n := g.sessionLines("alpha", "A"); n != 0 {
		t.Fatalf("the excluded rebuild wrote %d session ledger lines; step 4 was entered", n)
	}
	if n := g.store("alpha").CountChunks(indexstore.ArchiveOwner(sha)); n != 0 {
		t.Fatalf("the excluded rebuild wrote %d archive-owned chunks", n)
	}
	// The ledger itself is never created: skipping step 4 skips its
	// ensureLedger, which is what leaves a later opt-in free to record every
	// archive on disk as the baseline set (ADR-014 decision 2).
	if l := g.store("alpha").Ledger(); l != nil && l.Exists() {
		t.Fatal("the excluded rebuild created a ledger; step 4's ensureLedger ran")
	}
}

// G6b — COMPONENT. TestTheExcludedRebuildStillBuildsTheAuthoredTiers: step 5
// runs exactly as today. Excluding transcripts must not quietly exclude the
// tiers search is actually for.
//
// Q1 PATH: component. Q2 VALUE: the assertion is about tiers PRESENT, which no
// fixture produces by accident.
// Q3 PRESENCE: the fixture carries a session note WITH A BODY, and the control
// run is asserted to produce the notes tier from it — without content, the
// tier is absent anyway and skipping step 5 would change nothing.
//
// Mutation: skip step 5 as well as step 4 under exclusion.
func TestTheExcludedRebuildStillBuildsTheAuthoredTiers(t *testing.T) {
	const body = "Authored content about the ledger, the chunk store and the embed cache. "
	f := newFixture(t)
	f.writeNote("alpha", "s1", strings.Repeat(body, 20))
	f.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))

	// Control: the same corpus, not excluded, produces the notes tier.
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("control rebuild: %v", err)
	}
	ctl := completenessOf(t, f, "alpha")
	if _, ok := ctl.Tiers["notes"]; !ok {
		t.Fatalf("PRESENCE: the control rebuild produced no notes tier from the fixture's session "+
			"note, so this guard could not tell a skipped step 5 from an empty corpus: %v", ctl.Tiers)
	}

	// Excluded: the same corpus must still produce it.
	g := newFixture(t)
	g.writeNote("alpha", "s1", strings.Repeat(body, 20))
	g.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))
	if _, err := g.rebuildD(g.excludedDeps(),
		RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("excluded rebuild: %v", err)
	}
	got := completenessOf(t, g, "alpha")
	if _, ok := got.Tiers["notes"]; !ok {
		t.Fatalf("the excluded rebuild built NO notes tier: %v. Excluding transcripts must leave "+
			"notes, decisions, iterations and the commit log exactly as they were — they are what "+
			"search is for", got.Tiers)
	}
}

func completenessOf(t *testing.T, f *fixture, project string) indexstore.Completeness {
	t.Helper()
	rec, err := indexstore.ReadCompleteness(f.v, project)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// G10b — COMPONENT. TestATranscriptFreeRebuildCompletesAndClearsStale.
//
// Under exclusion no archive is ledgered, so everyLiveArchiveAccounted would
// return false on its ordinary rule and completeRebuild would never run. A
// pre-existing StaleMissingVectors would then persist for ever — the search
// path never clears a stale reason — and the bootstrap would tell the operator
// to run `vp index rebuild` on every session start, for the very build the
// exclusion exists to avoid.
//
// Q1 PATH: component. Q2 VALUE: the reason is PRESENT before and must be ABSENT
// after; both states are asserted.
// Q3 PRESENCE: a StaleMissingVectors reason is written first and asserted
// present, and a live archive exists on disk with no ledger record — the case
// the new arm covers.
//
// Mutation: delete the exclusion arm from everyLiveArchiveAccounted.
func TestATranscriptFreeRebuildCompletesAndClearsStale(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.writeNote("alpha", "s1", strings.Repeat("Notes about the chunk store and the ledger. ", 20))
	f.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))
	f.setStale("alpha", indexstore.StaleReason{Kind: indexstore.StaleMissingVectors})

	if !hasReason(f.reasons("alpha"), indexstore.StaleMissingVectors) {
		t.Fatalf("PRESENCE: no missing-vector reason to clear: %v", f.reasons("alpha"))
	}
	if _, ok := f.live("alpha", "A"); ok {
		t.Fatal("PRESENCE: the archive is already ledgered live, so the new accounted-for arm is not reached")
	}

	if _, err := f.rebuildD(f.excludedDeps(),
		RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("excluded rebuild: %v", err)
	}
	if hasReason(f.reasons("alpha"), indexstore.StaleMissingVectors) {
		t.Fatalf("a transcript-free rebuild did not COMPLETE, so the missing-vector stale reason "+
			"survived: %v. Every bootstrap would then tell the operator to run `vp index rebuild`",
			f.reasons("alpha"))
	}
}

// G10c — COMPONENT. TestATranscriptFreeCompletionDoesNotClearTheBaseline.
//
// The baseline set is the AUTOMATIC ingester's skip-list for the historical
// backlog, and under exclusion that backlog is still un-ingested. Emptying it
// on completion would hand every archive on disk to the automatic path the
// moment the operator opts in — the drowning ADR-014 decision 2's baseline set
// exists to prevent.
//
// Q1 PATH: component. Q2 VALUE: non-empty before, must stay non-empty after.
// Q3 PRESENCE: a NON-EMPTY baseline set is asserted before the call.
//
// Mutation: let ClearBaseline run unconditionally in completeRebuild.
func TestATranscriptFreeCompletionDoesNotClearTheBaseline(t *testing.T) {
	f := newFixture(t)
	f.writeNote("alpha", "s1", strings.Repeat("Notes about the chunk store and the ledger. ", 20))
	f.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))
	f.ensureLedgerWithBaseline("alpha")

	before := f.baselineCount("alpha")
	if before == 0 {
		t.Fatal("PRESENCE: the baseline set is empty before the rebuild, so 'it was not cleared' " +
			"is true of a correct run and of one that cleared it alike")
	}

	if _, err := f.rebuildD(f.excludedDeps(),
		RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("excluded rebuild: %v", err)
	}
	if after := f.baselineCount("alpha"); after != before {
		t.Fatalf("a transcript-free completion emptied the baseline set (%d -> %d). The set is the "+
			"automatic ingester's skip-list for a backlog that is still un-ingested; clearing it "+
			"hands every archive to the automatic path on a later opt-in", before, after)
	}
}

// TestAnExcludedRebuildClearsTheColdSearchWant — the acceptance item "the
// cold-search hair trigger is dead".
//
// A cold search records a durable rebuild-corpus want and spawns a detached
// `vp index rebuild`. If an excluded rebuild did not CLEAR that want, every
// later cold search would re-spawn, and the exclusion would have removed the
// cost of the build without removing the trigger. The want is tied to the
// ENGINE rebuild (stopped == "" && !NoEmbed), not to archive-ledger
// completion, exactly so the archive-less case can clear it — and under
// exclusion every project is that case.
//
// Q1 PATH: component — it drives ingest.Rebuild. The spawn half (a second
// cold search calling the launch seam zero times) is internal/search's.
// Q2 VALUE: the want is SET before and must be CLEAR after.
// Q3 PRESENCE: the want is asserted set before the call; with no want, "it was
// cleared" is true of a rebuild that never touched it.
//
// Mutation: tie the want-clear to `accounted` (archive-ledger completion)
// instead of to the engine rebuild.
func TestAnExcludedRebuildClearsTheColdSearchWant(t *testing.T) {
	f := newFixture(t)
	f.writeNote("alpha", "s1", strings.Repeat("Notes about the chunk store and the ledger. ", 20))
	f.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))
	f.noteRebuild("alpha")

	if !f.rebuildWanted("alpha") {
		t.Fatal("PRESENCE: no rebuild-corpus want is set, so 'the want was cleared' is true of a " +
			"rebuild that never touched it")
	}

	if _, err := f.rebuildD(f.excludedDeps(),
		RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("excluded rebuild: %v", err)
	}
	if f.rebuildWanted("alpha") {
		t.Fatal("an excluded rebuild left the cold-search rebuild-corpus want SET. Every later " +
			"cold search would re-spawn a detached rebuild: the exclusion would have removed the " +
			"build's cost without removing the trigger that arms it")
	}
}
