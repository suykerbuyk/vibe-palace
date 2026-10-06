// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestGenerationReload (C5): a warm engine sees every kind of commit another
// writer makes, through the store counter alone: an archive, a decision chunk
// (which the ledger never sees), a mempalace import batch, a relabel (a new
// epoch), and a counter deleted and recreated (a random epoch, a smaller gen).
func TestGenerationReload(t *testing.T) {
	a, v := testEngine(t)
	writer := NewEmbedCache(v)
	writer.fingerprint = a.cache.fingerprint
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note")
	search(t, a, "proj", "a note") // warm

	commitArchive(t, writer, v, "proj", "sess-a", "sha-a", []string{"an archive another process ingested"}, true)
	if !hasContent(search(t, a, "proj", "an archive another process ingested"), "an archive another process ingested") {
		t.Fatal("the warm engine missed an archive commit")
	}

	// A decision chunk written to the store by another process (its note backs
	// it, so the rebuild's notes tier keeps it) bumps the counter, so the warm
	// engine reloads and sees it although the ledger never records one.
	writeDecisionNote(t, v.Root, "proj", "2026-05-14-aaaa0000-01", "2026-05-14", "a decision recorded elsewhere")
	appendDecision(t, v, "proj", "a decision recorded elsewhere")
	if !hasContent(search(t, a, "proj", "a decision recorded elsewhere"), "a decision recorded elsewhere") {
		t.Fatal("the warm engine missed a decision chunk (the ledger never sees one)")
	}

	tx := lockStore(t, v, "proj")
	batch := chunkOf("a mempalace import chunk", "mempalace")
	if err := tx.CommitBatch(indexstore.BatchCommit{BatchID: "batch-1", StartDay: "2026-05-13",
		Chunks: []indexstore.OwnedChunk{batch}, Vectors: map[string][]float32{batch.ID: mockVec(t, batch.Content)}}, mustWriter(t, writer, tx)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !hasContent(search(t, a, "proj", batch.Content), batch.Content) {
		t.Fatal("the warm engine missed a mempalace batch")
	}

	tx = lockStore(t, v, "proj")
	if err := tx.Rewrite(map[string]indexstore.Labels{batch.ID: {Room: "relabelled-room"}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	res := search(t, a, "proj", batch.Content)
	if len(res) == 0 || res[0].Room != "relabelled-room" {
		t.Fatalf("the warm engine missed a relabel: %+v", res)
	}

	genPath, err := v.IndexGenerationPath("proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(genPath); err != nil {
		t.Fatal(err)
	}
	commitArchive(t, writer, v, "proj", "sess-b", "sha-b", []string{"an archive after the counter was recreated"}, true)
	if !hasContent(search(t, a, "proj", "an archive after the counter was recreated"), "an archive after the counter was recreated") {
		t.Fatal("the warm engine missed a commit after the counter was recreated")
	}
}

// TestALaterCommitSurfacesRecordsACrashLeftUnannounced (R-11): a process that
// dies after its records are durable and before it bumps the counter leaves
// them unannounced: a warm engine does not reload for them (documented, not
// required). The next commit by anyone moves the counter, and the full reload
// it triggers finds them.
func TestALaterCommitSurfacesRecordsACrashLeftUnannounced(t *testing.T) {
	a, v := testEngine(t)
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note")
	search(t, a, "proj", "a note")
	const crashed = "a chunk a crashed ingester committed"
	g0, _ := indexstore.ReadGeneration(v, "proj")
	runCrashHelper(t, a, v, "proj", crashed)
	if g, _ := indexstore.ReadGeneration(v, "proj"); g != g0 {
		t.Fatalf("precondition: the crash helper moved the counter (%v -> %v)", g0, g)
	}
	if hasContent(search(t, a, "proj", crashed), crashed) {
		t.Log("the warm engine saw the unannounced records early (allowed)")
	}
	writer := NewEmbedCache(v)
	writer.fingerprint = a.cache.fingerprint
	commitArchive(t, writer, v, "proj", "sess-later", "sha-later", []string{"an unrelated later commit"}, true)
	if !hasContent(search(t, a, "proj", crashed), crashed) {
		t.Fatal("after a later commit the crashed process's records are still not served")
	}
}

// TestAnUnchangedCompletenessRecordIsNotRewritten: two engines (two
// processes) search a project with a persistent local-tier miss. Each search
// that rebuilds re-finds the miss and writes the same record; an unchanged
// record must not move the counter, or each engine's write would make the
// other rebuild, forever. After one round both settle: across the second
// round the counter is unchanged and the store is not read at all.
func TestAnUnchangedCompletenessRecordIsNotRewritten(t *testing.T) {
	a, v := testEngine(t)
	b := NewEngine(a.embedder, v, a.config)
	commitArchive(t, a.cache, v, "proj", "sess-a", "sha-a", []string{"a chunk with no vector"}, false)
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note")
	for range 2 { // settle: each engine's first build
		search(t, a, "proj", "a note")
		search(t, b, "proj", "a note")
	}
	var reads atomic.Int64
	old := readStoreFn
	readStoreFn = func(v *storage.Vault, p string) (*indexstore.Store, error) {
		reads.Add(1)
		return old(v, p)
	}
	t.Cleanup(func() { readStoreFn = old })
	g0, _ := indexstore.ReadGeneration(v, "proj")
	search(t, a, "proj", "a note")
	search(t, b, "proj", "a note")
	if g, _ := indexstore.ReadGeneration(v, "proj"); g != g0 {
		t.Fatalf("the counter moved across a round with nothing new: %v -> %v", g0, g)
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("%d store reads across a round with nothing new; want 0", n)
	}
	if !slices.Contains(record(t, v, "proj").Stale, missingVectors) {
		t.Fatal("precondition: the persistent miss was not recorded")
	}
}

// TestSearchWritesTimeOutAndSkip: another process holds the project's commit
// lock for the whole search. A zero-timeout search that finds a chunks
// fingerprint mismatch and a note miss still answers (the note from its
// in-memory vector), and writes nothing: completeness.json and the vectors are
// unchanged. Once the lock is free, the next search sets stale and commits the
// note's vector.
func TestSearchWritesTimeOutAndSkip(t *testing.T) {
	eng, v := testEngine(t)
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{"a stored chunk"}, true)
	idir, _ := v.IndexDir("proj")
	if err := os.WriteFile(idir+"/chunks.fingerprint", []byte("vp-chunks indexer=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note body")
	noteIDs, _, _, _ := collectNoteCorpus(v, "proj")
	dir, _ := v.EmbedCacheDir("proj")
	before := dirHashes(t, dir)
	recBefore := record(t, v, "proj")

	held, err := indexstore.Lock(context.Background(), v, "proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	withSearchLockTimeout(t, 0)
	if !hasContent(search(t, eng, "proj", "a note body"), "a note body") {
		t.Fatal("a busy commit lock stopped the search answering")
	}
	if after := dirHashes(t, dir); !mapsEqual(before, after) {
		t.Fatal("a vector was written while the commit lock was busy")
	}
	if rec := record(t, v, "proj"); len(rec.Stale) != len(recBefore.Stale) {
		t.Fatalf("completeness.json changed while the commit lock was busy: %+v", rec)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	search(t, eng, "proj", "a note body")
	if !slices.Contains(record(t, v, "proj").Stale, staleChunks) {
		t.Fatal("the next search did not set the stale reason it skipped")
	}
	if got, _ := eng.cache.Get("proj", noteIDs[0]); got == nil {
		t.Fatal("the next search did not commit the note's vector")
	}
}
