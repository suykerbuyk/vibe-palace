// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

// Guards for the `authored` coverage state, the ExcludedTiers record and the
// walk's exclusion belt.
//
// Every guard here is a COMPONENT guard: it hands the engine a storage.Config
// literal, so it proves the PREDICATE and says nothing about whether a config
// FILE ever reaches it. The wiring guard for that is cmd/vp's
// TestTheHostLocalConfigFileReachesTheIngestDecision, and none of these may be
// cited in its place.

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// excludedEngine builds an engine for a host that EXCLUDES the transcript
// tier, over a fresh vault. IndexTranscripts: false is this project's HOST
// DEFAULT; the rest of this package's tests set it true because they predate
// the default (see testEngine).
func excludedEngine(t *testing.T) (*Engine, *storage.Vault) {
	t.Helper()
	v := testVault(t)
	eng := NewEngine(embedder.NewMock(384), v, storage.Config{SearchDefaultLimit: 10, IndexTranscripts: false})
	t.Cleanup(func() { eng.Close() })
	return eng, v
}

// warmEngine is an engine over the SAME vault that DOES index transcripts —
// the already-built host whose store still serves them.
func warmEngine(t *testing.T, v *storage.Vault) *Engine {
	t.Helper()
	eng := NewEngine(embedder.NewMock(384), v, storage.Config{SearchDefaultLimit: 10, IndexTranscripts: true})
	t.Cleanup(func() { eng.Close() })
	return eng
}

// seedAuthoredCorpus gives the project the authored content a real build turns
// into a tier, and runs that build through the PRODUCTION writer
// (RebuildExplicit -> walkCorpus -> writeCompletenessLocked) so the
// completeness record — Tiers AND ExcludedTiers — is the one production
// writes, not one the test hand-assembled.
func seedAuthoredCorpus(t *testing.T, eng *Engine, v *storage.Vault, project string) {
	t.Helper()
	markMigrated(t, v)
	testutil.InitProject(t, v.Root, project)
	body := strings.Repeat("Authored content about the ledger, the chunk store and the embed cache. ", 20)
	writeSessionNote(t, v.Root, project, "2026-05-13-aaaa0000-01", "2026-05-13", "an authored note", body)
	if _, err := eng.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	// The seed must actually produce a built tier, or every guard below is
	// reasoning about an `unbuilt` project.
	if rec := record(t, v, project); len(rec.Tiers) == 0 {
		t.Fatalf("seed build produced no tier: %+v", rec)
	}
}

// writeArchiveOnDisk puts an archive manifest under the project so
// coverageCounts sees m > 0. Capture is untouched by the exclusion, so on a
// real host these are always there.
func writeArchiveOnDisk(t *testing.T, v *storage.Vault, project, session, sha string) {
	t.Helper()
	writeArchive(t, v, project, session, session, sha, "2026-05-13T00:00:00Z")
}

// G1 — COMPONENT. TestAnExcludedProjectWithArchivesReadsAuthoredNotPartial.
//
// Q1 PATH: component. Q2 VALUE: the engine's IndexTranscripts is false while
// the fixture HAS archives, so a project that reads `partial` and one that
// reads `authored` are distinguishable outcomes.
// Q3 PRESENCE: ALL FOUR predicate arms are asserted true before the call —
// built (len(Tiers) > 0), m > 0, !IndexTranscripts, and ExcludedTiers naming
// the transcript tier. Deleting a branch the fixture does not reach changes
// nothing, which is how this shape goes inert.
//
// Mutation: delete the `authored` branch from CoverageState; the project then
// reads `partial`.
func TestAnExcludedProjectWithArchivesReadsAuthoredNotPartial(t *testing.T) {
	eng, v := excludedEngine(t)
	const project = "proj"
	seedAuthoredCorpus(t, eng, v, project)
	writeArchiveOnDisk(t, v, project, "sess-a", "sha-a")

	rec := record(t, v, project)
	if len(rec.Tiers) == 0 {
		t.Fatalf("PRESENCE arm 1: the completeness record names no built tier: %+v", rec)
	}
	if !slices.Contains(rec.ExcludedTiers, tierTranscripts) {
		t.Fatalf("PRESENCE arm 4: the record's ExcludedTiers is %v, which does not name %q; "+
			"the fourth arm of the predicate is not satisfied and deleting it would change nothing",
			rec.ExcludedTiers, tierTranscripts)
	}
	if eng.config.IndexTranscripts {
		t.Fatal("PRESENCE arm 3: the engine indexes transcripts")
	}

	cov := mustCoverage(t, eng, project)
	// G1b: non-vacuity. m == 0 would land in `notes` whatever the authored
	// branch does, and n == m would otherwise land in `current`.
	if cov.M == 0 {
		t.Fatal("PRESENCE arm 2 (G1b): m == 0, so this fixture lands in `notes` and says nothing about `authored`")
	}
	if cov.N == cov.M {
		t.Fatalf("G1b: n == m == %d, so this fixture would read `current` with the authored branch "+
			"deleted, not `partial`", cov.M)
	}

	if cov.State != CoverageAuthored {
		t.Fatalf("an excluded project with %d archives reads %q, want %q. `partial` means "+
			"incomplete and catching up; a chosen exclusion is not that, and a project sitting at "+
			"partial for ever misreports intent on every host", cov.M, cov.State, CoverageAuthored)
	}
	if !strings.Contains(cov.Reason, "excluded on this host") {
		t.Fatalf("the authored reason does not say the tier is excluded: %q", cov.Reason)
	}
}

// G1c — COMPONENT. TestAuthoredIsUnreachableWhileArchiveChunksAreLiveOwned.
//
// 🔴 THIS IS THE ARM THAT STOPS THE STATE LYING. The first three arms are a
// statement of intent; without the fourth, a previously-built host flips to
// `authored` the instant the setting changes while its index still answers
// from transcript chunks.
//
// Q1 PATH: component. Q2 VALUE: the other three arms are TRUE and the fourth
// is ABSENT, so deleting the fourth changes the observed state.
// Q3 PRESENCE: asserted — the other three arms hold, ExcludedTiers is absent,
// and ≥1 archive-owned chunk is present under a live recSession row.
//
// Mutation: delete the `slices.Contains(rec.ExcludedTiers, tierTranscripts)`
// arm ALONE — not together with !IndexTranscripts. One arm of a compound
// predicate at a time.
func TestAuthoredIsUnreachableWhileArchiveChunksAreLiveOwned(t *testing.T) {
	eng, v := excludedEngine(t)
	const project = "proj"
	// A store that STILL SERVES transcripts: committed while the host indexed
	// them, as an already-built host's store does.
	warm := warmEngine(t, v)
	seedAuthoredCorpus(t, warm, v, project)
	commitArchive(t, warm.cache, v, project, "sess-a", "sha-a", []string{"a transcript chunk that is still served"}, true)
	writeArchiveOnDisk(t, v, project, "sess-a", "sha-a")
	if _, err := warm.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	rec := record(t, v, project)
	if len(rec.Tiers) == 0 {
		t.Fatalf("PRESENCE arm 1: no built tier: %+v", rec)
	}
	if len(rec.ExcludedTiers) != 0 {
		t.Fatalf("PRESENCE: ExcludedTiers is %v, want EMPTY — the fourth arm must be the only one "+
			"that is false, or deleting it changes nothing", rec.ExcludedTiers)
	}
	st, err := indexstore.ReadStore(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if n := st.CountChunks(indexstore.ArchiveOwner("sha-a")); n == 0 {
		t.Fatal("PRESENCE: no archive-owned chunk under a live session row, so there is nothing " +
			"for `authored` to lie about")
	}

	cov, err := eng.CoverageState(project)
	if err != nil {
		t.Fatal(err)
	}
	if cov.State == CoverageAuthored {
		t.Fatalf("the project reads %q while its store still holds live-owned archive chunks. "+
			"`authored` is a claim about what search SERVES; flipping a config setting cannot make "+
			"it true, only the purge can", cov.State)
	}
	if !strings.Contains(cov.Reason, "--drop-transcripts") {
		t.Fatalf("the in-between state's reason does not name the remedy: %q", cov.Reason)
	}
}

// G1d — COMPONENT. TestTheWalkCountsArchiveChunksBeforeTheExclusionSkip.
//
// The belt in walkCorpus skips archive-owned store chunks when the tier is
// excluded. If archiveOwned were incremented AFTER that skip it would always
// be zero, writeCompletenessLocked would record the tier as excluded over a
// store still holding them, and `authored` would be reachable on exactly the
// host G1c protects.
//
// Q1 PATH: component. Q2 VALUE: archiveOwned > 0 and ExcludedTiers empty are
// both the opposite of what the mutation produces.
// Q3 PRESENCE: 🔴 ≥1 live-owned archive chunk that the belt ACTUALLY SKIPS,
// which since the 2026-11-06 ruling means one with NO CACHED VECTOR — the belt
// now continues only on `vec == nil`, so a vectored chunk is loaded and never
// reaches the skip at all. Both halves are asserted below. This fixture used
// to commit WITH vectors; after the ruling that made the guard INERT, and the
// relocation mutation below ran green against it.
//
// Mutation: move `facts.archiveOwned++` below the exclusion `continue` in
// walkCorpus (relocate it, do not delete it — a deletion reddens for the
// wrong reason, because archiveOwned is then always zero).
func TestTheWalkCountsArchiveChunksBeforeTheExclusionSkip(t *testing.T) {
	eng, v := excludedEngine(t)
	const project = "proj"
	warm := warmEngine(t, v)
	seedAuthoredCorpus(t, warm, v, project)
	// withVecs=false: the belt skips only a VECTORLESS archive chunk.
	commitArchive(t, warm.cache, v, project, "sess-a", "sha-a", []string{"a live-owned transcript chunk"}, false)

	// Q3: the chunk must be YIELDED by Chunks(true) — a chunk on disk with no
	// live owner is skipped there and the belt never sees it — AND it must
	// have no cached vector, or the belt loads it and never reaches the skip.
	st, err := indexstore.ReadStore(v, project)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, c := range st.Chunks(true) {
		if c.Selected == nil || c.Selected.Kind != indexstore.OwnerArchive {
			continue
		}
		live++
		if vec, verr := eng.cache.Get(project, c.ID); verr != nil {
			t.Fatal(verr)
		} else if vec != nil {
			t.Fatal("PRESENCE: the archive-owned chunk HAS a cached vector, so the belt loads it " +
				"and the skip is never reached — moving the count past the skip would change nothing")
		}
	}
	if live == 0 {
		t.Fatal("PRESENCE: Store.Chunks(true) yields no archive-owned chunk, so the belt is never " +
			"reached and moving the count past it changes nothing")
	}

	// The EXCLUDED engine's own build: the belt fires, and the count must
	// still have happened.
	if _, err := eng.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	rec := record(t, v, project)
	if len(rec.ExcludedTiers) != 0 {
		t.Fatalf("the build recorded ExcludedTiers = %v over a store holding %d live-owned archive "+
			"chunks. The count must be taken BEFORE the exclusion skip, or the skip is mistaken for "+
			"their absence and `authored` becomes reachable while transcripts are still served",
			rec.ExcludedTiers, live)
	}
}

// G1e — COMPONENT. TestUnreclaimedChunksDoNotBlockAuthored.
//
// After the purge's ledger step, archive-owned chunks may still sit in the
// fold with no live owner. They are NOT served, so they are a disk-space fact,
// not a search fact, and making them an input to the predicate would leave a
// correct index reading `partial` for ever on a disk fact.
//
// Q1 PATH: component. Q2 VALUE: UnreclaimedTranscriptChunks > 0 while the
// state must still be authored.
// Q3 PRESENCE: UnreclaimedTranscriptChunks > 0 asserted, and the other arms
// satisfied.
//
// Mutation: add the unreclaimed count to the `authored` predicate.
func TestUnreclaimedChunksDoNotBlockAuthored(t *testing.T) {
	eng, v := excludedEngine(t)
	const project = "proj"
	warm := warmEngine(t, v)
	seedAuthoredCorpus(t, warm, v, project)
	commitArchive(t, warm.cache, v, project, "sess-a", "sha-a", []string{"a transcript chunk to be orphaned"}, true)
	writeArchiveOnDisk(t, v, project, "sess-a", "sha-a")

	// The purge: the ledger step makes them ownerless, the rest of the reclaim
	// is what leaves them on disk.
	if _, err := warm.DropTranscriptTier(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	// Put the orphan rows back so the fold still carries archive owners while
	// nothing is live — the exact residue state.
	tx := lockStore(t, v, project)
	if err := tx.Append(indexstore.ArchiveOwner("sha-orphan"), []indexstore.OwnedChunk{chunkOf("unreclaimed residue", "session")}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	cov, err := eng.CoverageState(project)
	if err != nil {
		t.Fatal(err)
	}
	if cov.UnreclaimedTranscriptChunks == 0 {
		t.Fatalf("PRESENCE: no unreclaimed chunks, so adding them to the predicate would change "+
			"nothing: %+v", cov)
	}
	if cov.State != CoverageAuthored {
		t.Fatalf("a project with %d UNSERVED archive-owned chunks on disk reads %q, want %q. "+
			"`authored` is a claim about what search serves; an unreclaimed-disk fact must not "+
			"make a correct index report incomplete for ever",
			cov.UnreclaimedTranscriptChunks, cov.State, CoverageAuthored)
	}
}

// G2 — COMPONENT. TestTheExcludedTierIsNamedInTheCoverageReport: the coverage
// struct carries the excluded tiers the completeness record names.
//
// Q1 PATH: component. Q2 VALUE: non-empty vs empty.
// Q3 PRESENCE: the completeness record's ExcludedTiers is asserted NON-EMPTY
// before the call — with an empty one, "stop copying it" changes nothing.
//
// Mutation: stop copying rec.ExcludedTiers into Coverage.
func TestTheExcludedTierIsNamedInTheCoverageReport(t *testing.T) {
	eng, v := excludedEngine(t)
	const project = "proj"
	seedAuthoredCorpus(t, eng, v, project)
	writeArchiveOnDisk(t, v, project, "sess-a", "sha-a")

	if rec := record(t, v, project); len(rec.ExcludedTiers) == 0 {
		t.Fatalf("PRESENCE: the completeness record's ExcludedTiers is empty, so a guard on the "+
			"copy would pass either way: %+v", rec)
	}
	cov, err := eng.CoverageState(project)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cov.ExcludedTiers, tierTranscripts) {
		t.Fatalf("the coverage report's ExcludedTiers is %v and does not name %q, although the "+
			"completeness record does", cov.ExcludedTiers, tierTranscripts)
	}
}

// G10 — COMPONENT. TestAnExcludedProjectWithLiveOwnedArchiveChunksIsNotStale.
//
// An excluded project whose store still holds live-owned archive chunks with
// no vectors must NOT read `stale`: that reason fires the bootstrap "run
// `vp index rebuild`" alert, demanding the very build the exclusion exists to
// avoid, for ever (the search path never clears a stale reason).
//
// Q1 PATH: component. Q2 VALUE: stale vs not-stale.
// Q3 PRESENCE: 🔴 TWO preconditions, both asserted, because the mutated line
// (facts.localMisses++) is reached only inside the Chunks(true) loop, in the
// non-decision branch, when vec == nil && !mode.embedLocal:
//
//	(i)  ≥1 archive-owned chunk PRESENT in Store.Chunks(true) — genuinely
//	     INGESTED, with a live recSession row, not merely an archive file on
//	     disk. An archive on disk with no owner rows produces no such chunk,
//	     the mutated line is never reached and the guard is inert.
//	(ii) that chunk has NO vector in the embed cache, so it is a MISS.
//
// Mutation: make walkCorpus count archive-owned chunks as localMisses even
// when excluded (i.e. drop the belt's `continue`).
func TestAnExcludedProjectWithLiveOwnedArchiveChunksIsNotStale(t *testing.T) {
	eng, v := excludedEngine(t)
	const project = "proj"
	warm := warmEngine(t, v)
	seedAuthoredCorpus(t, warm, v, project)
	// withVecs=false: the chunk is a cache MISS, precondition (ii).
	commitArchive(t, warm.cache, v, project, "sess-a", "sha-a", []string{"an ingested transcript chunk with no vector"}, false)

	st, err := indexstore.ReadStore(v, project)
	if err != nil {
		t.Fatal(err)
	}
	var missID string
	for _, c := range st.Chunks(true) {
		if c.Selected != nil && c.Selected.Kind == indexstore.OwnerArchive {
			missID = c.ID
			break
		}
	}
	if missID == "" {
		t.Fatal("PRESENCE (i): Store.Chunks(true) yields no archive-owned chunk. An archive on disk " +
			"is not enough: engine.go's localMisses++ is reached only while VISITING an ingested, " +
			"live-owned chunk")
	}
	if vec, err := eng.cache.Get(project, missID); err != nil {
		t.Fatal(err)
	} else if vec != nil {
		t.Fatal("PRESENCE (ii): the archive-owned chunk HAS a vector, so it is a hit and " +
			"localMisses is never incremented for it")
	}

	if _, err := eng.RebuildNoEmbed(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	rec := record(t, v, project)
	if rec.LocalMisses != 0 {
		t.Fatalf("an EXCLUDED build counted %d local misses over archive-owned chunks it does not "+
			"serve", rec.LocalMisses)
	}
	if _, reasons, err := eng.Stale(project); err != nil {
		t.Fatal(err)
	} else if containsReason(reasons, indexstore.StaleReason{Kind: indexstore.StaleMissingVectors}) {
		t.Fatalf("an excluded project reads STALE for missing vectors on transcript chunks it does "+
			"not serve: %v. That fires the bootstrap 'run `vp index rebuild`' alert on every session "+
			"start, for the build this exclusion exists to avoid", reasons)
	}
}
