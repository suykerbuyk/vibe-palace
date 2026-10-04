// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var t0 = time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC)

func ingestOnce(t *testing.T, f *fixture, e *archive.Entry, opts IngestOptions) ArchiveResult {
	t.Helper()
	res, err := IngestArchive(context.Background(), f.deps(), "alpha", e, opts)
	if err != nil {
		t.Fatalf("IngestArchive: %v", err)
	}
	return res
}

// TestIngestArchiveCommitsAPendingArchive: an archive is chunked, embedded and
// committed: every chunk is owned by the archive's source_sha256, every chunk
// has a vector, and the ledger's session record carries the path, the hash,
// the distinct-id chunk count, captured_at, the start day and its source, and
// generation 1. Nothing tracked changes.
func TestIngestArchiveCommitsAPendingArchive(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 6), t0.Add(2*time.Hour))
	f.ensureLedger("alpha")
	f.commitAll()

	res := ingestOnce(t, f, e, IngestOptions{Embed: true})
	if res.Outcome != Committed || res.Chunks == 0 || res.Embedded != res.Chunks {
		t.Fatalf("result %+v", res)
	}
	st := f.store("alpha")
	chunks := st.Chunks(true)
	if len(chunks) != res.Chunks {
		t.Fatalf("%d loadable chunks, want %d", len(chunks), res.Chunks)
	}
	for _, c := range chunks {
		if len(c.Owners) != 1 || c.Owners[0] != indexstore.ArchiveOwner(e.Manifest.SourceSHA256) {
			t.Fatalf("chunk %s owners %v, want the archive's sha", c.ID, c.Owners)
		}
		if c.FiledAt != "2026-05-13" || c.SourceRef != "S1" {
			t.Fatalf("chunk %s filed %s ref %s", c.ID, c.FiledAt, c.SourceRef)
		}
		if vec, hit, err := f.eng.CachedVector("alpha", c.ID); err != nil || !hit || len(vec) == 0 {
			t.Fatalf("chunk %s has no cached vector: %v %v", c.ID, hit, err)
		}
	}
	if len(st.KG(true)) == 0 {
		t.Fatal("no KG record was committed")
	}
	sr, ok := st.Ledger().Session("S1")
	if !ok || sr.State != indexstore.StateLive || sr.SHA != e.Manifest.SourceSHA256 || sr.ArchivePath != "Projects/alpha/transcripts/"+filepath.Base(e.ArchivePath) ||
		sr.ChunkCount != res.Chunks || sr.StartDay != "2026-05-13" || sr.StartDaySource != archive.DayFromTranscript ||
		sr.Generation != 1 || sr.CapturedAt != e.Manifest.CapturedAt {
		t.Fatalf("session record %+v", sr)
	}
	if out := f.git("status", "--porcelain", "-uall"); out != "" {
		t.Fatalf("the ingest changed tracked files:\n%s", out)
	}
}

// TestIngestArchiveDatesFromTheSession: the start day is the UTC day of the
// transcript's first timestamped record, never the clock or captured_at; with
// no timestamped record it is captured_at's UTC day.
func TestIngestArchiveDatesFromTheSession(t *testing.T) {
	f := newFixture(t)
	start := time.Date(2026, 5, 12, 23, 30, 0, 0, time.FixedZone("x", -2*3600)) // UTC 2026-05-13
	e := f.archiveOf("alpha", "S1", transcript(start, "s1", 3), time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	plain := "No timestamps in this transcript at all, just prose about the ledger and the chunk store.\n"
	e2 := f.archiveOf("alpha", "S2", plain, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	f.ensureLedger("alpha")
	ingestOnce(t, f, e, IngestOptions{Embed: true})
	ingestOnce(t, f, e2, IngestOptions{Embed: true})
	l := f.store("alpha").Ledger()
	if day, ok := l.StartDay("S1"); !ok || day != "2026-05-13" {
		t.Fatalf("S1 start day %q, want 2026-05-13 (the transcript's UTC day)", day)
	}
	if s, _ := l.Session("S1"); s.StartDaySource != archive.DayFromTranscript {
		t.Fatalf("S1 source %q", s.StartDaySource)
	}
	if s, _ := l.Session("S2"); s.StartDay != "2026-10-01" || s.StartDaySource != archive.DayFromCapturedAt {
		t.Fatalf("S2 %+v, want captured_at's day", s)
	}
	for _, c := range f.store("alpha").Chunks(true) {
		if c.SourceRef == "S1" && c.FiledAt != "2026-05-13" {
			t.Fatalf("S1 chunk filed %s", c.FiledAt)
		}
	}
}

// TestToCommitCountsDistinctChunks (4-S2): an archive that repeats a chunk owns
// it once, so the ledger's count is the distinct ids.
func TestToCommitCountsDistinctChunks(t *testing.T) {
	prep := palace.Prepared{}
	for _, content := range []string{"x", "y", "x", "z"} {
		prep.Chunks = append(prep.Chunks, palace.PreparedChunk{ID: index.ChunkID(content), Content: content, SourceRef: "S"})
	}
	c := toCommit(prep, ArchiveResult{SessionID: "S", SHA: "h"}, archive.SessionDay{Day: "2026-05-13", Source: archive.DayFromTranscript},
		&archive.Entry{ArchivePath: "p", Manifest: &archive.Manifest{}})
	if got := distinct(c.Chunks); got != 3 {
		t.Fatalf("distinct = %d, want 3", got)
	}
	for _, ch := range c.Chunks {
		if ch.SourceType != sourceTypeSession || ch.SourceRef != "S" {
			t.Fatalf("chunk ownership %+v", ch.Ownership)
		}
	}
}

// TestIngestArchiveEmbedsOnlyMisses (cap-S3): with half the archive's chunk
// vectors already cached, the embedder sees exactly the other half.
func TestIngestArchiveEmbedsOnlyMisses(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 8), t0.Add(time.Hour))
	f.ensureLedger("alpha")
	va, err := archive.ReadVerified(e)
	if err != nil {
		t.Fatal(err)
	}
	ix, _ := palace.ProjectIndexing(f.v, "alpha")
	prep, _ := palace.Prepare(ix, palace.PrepareInput{Project: "alpha", SourceRef: "S1", Date: t0, Text: string(va.Bytes)})
	ids := map[string]bool{}
	for _, c := range prep.Chunks {
		ids[c.ID] = true
	}
	if len(ids) < 4 {
		t.Fatalf("precondition: %d distinct chunks", len(ids))
	}
	seeded := map[string][]float32{}
	for id := range ids {
		if len(seeded) == len(ids)/2 {
			break
		}
		seeded[id] = make([]float32, 384)
	}
	tx, err := indexstore.Lock(context.Background(), f.v, "alpha", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	w, err := f.eng.CacheWriter(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.PutVectors(w, seeded); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	res := ingestOnce(t, f, e, IngestOptions{Embed: true})
	if got, want := f.emb.texts.Load(), int64(len(ids)-len(seeded)); got != want || int64(res.Embedded) != want {
		t.Fatalf("embedded %d texts (result %d), want exactly the %d misses", got, res.Embedded, want)
	}
}

// TestIngestArchiveHoldsNoCommitLockWhileEmbedding: while the embedder is
// blocked mid-embed, the project's commit lock is free.
func TestIngestArchiveHoldsNoCommitLockWhileEmbedding(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 3), t0.Add(time.Hour))
	f.ensureLedger("alpha")
	gate, entered := make(chan struct{}), make(chan struct{})
	f.emb.mu.Lock()
	f.emb.gate, f.emb.entered = gate, entered
	f.emb.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := IngestArchive(context.Background(), f.deps(), "alpha", e, IngestOptions{Embed: true})
		done <- err
	}()
	<-entered
	tx, err := indexstore.Lock(context.Background(), f.v, "alpha", 0)
	if err != nil {
		close(gate)
		t.Fatalf("the commit lock was held during the embed: %v", err)
	}
	_ = tx.Release()
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestIngestArchiveRechecksBeforeEmbedding (R6): another process ledgers the
// archive after it was prepared, without embedding it (as the rebuild's
// --no-embed does), so its vectors are still cache misses: this ingest
// still embeds nothing, and reports AlreadyLedgered.
func TestIngestArchiveRechecksBeforeEmbedding(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 4), t0.Add(time.Hour))
	f.ensureLedger("alpha")
	var otherCalls int64
	afterPrepareFn = func(string, *archive.Entry) {
		afterPrepareFn = nil
		ingestOnce(t, f, e, IngestOptions{Embed: false})
		otherCalls = f.emb.calls.Load()
	}
	t.Cleanup(func() { afterPrepareFn = nil })
	res := ingestOnce(t, f, e, IngestOptions{Embed: true})
	if res.Outcome != AlreadyLedgered || res.Embedded != 0 {
		t.Fatalf("result %+v, want AlreadyLedgered with nothing embedded", res)
	}
	if got := f.emb.calls.Load(); got != otherCalls {
		t.Fatalf("embed calls %d after the other process's %d: this ingest embedded", got, otherCalls)
	}
	if n := f.sessionLines("alpha", "S1"); n != 1 {
		t.Fatalf("%d session records, want 1", n)
	}
}

// TestIngestArchiveRechecksUnderTheLock (R6): another process ledgers the
// archive after this one embedded and before it takes the lock: the commit
// writes nothing and adds no second record.
func TestIngestArchiveRechecksUnderTheLock(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 4), t0.Add(time.Hour))
	f.ensureLedger("alpha")
	var before []string
	beforeLockFn = func(string, *archive.Entry) {
		beforeLockFn = nil
		ingestOnce(t, f, e, IngestOptions{Embed: true})
		before = f.ledgerLines("alpha")
	}
	t.Cleanup(func() { beforeLockFn = nil })
	if res := ingestOnce(t, f, e, IngestOptions{Embed: true}); res.Outcome != AlreadyLedgered {
		t.Fatalf("outcome %v, want AlreadyLedgered", res.Outcome)
	}
	if after := f.ledgerLines("alpha"); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatal("the commit wrote to the ledger after the re-check found the archive ledgered")
	}
	if n := f.sessionLines("alpha", "S1"); n != 1 {
		t.Fatalf("%d session records, want 1", n)
	}
}

// TestIngestArchiveStopsOnAProjectRemovedMidRun: the project is removed
// between the prepare and the commit: ErrProjectGone, and no index directory.
func TestIngestArchiveStopsOnAProjectRemovedMidRun(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("beta", "S1", transcript(t0, "s1", 3), t0.Add(time.Hour))
	beforeLockFn = func(string, *archive.Entry) {
		beforeLockFn = nil
		if err := os.RemoveAll(filepath.Join(f.v.Root, "Projects", "beta")); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeLockFn = nil })
	_, err := IngestArchive(context.Background(), f.deps(), "beta", e, IngestOptions{Embed: true})
	if !errors.Is(err, indexstore.ErrProjectGone) {
		t.Fatalf("IngestArchive = %v, want ErrProjectGone", err)
	}
	if _, err := os.Stat(filepath.Join(f.v.Root, "palace", ".local", "index", "beta")); !os.IsNotExist(err) {
		t.Fatalf("index/beta exists for a gone project (stat err %v)", err)
	}
}

// TestIngestArchiveRefusesAnotherRecipe: a store whose chunks.fingerprint is
// another recipe's (present before the run, or written by another process
// between the pre-check and the commit, R7) gets nothing written.
func TestIngestArchiveRefusesAnotherRecipe(t *testing.T) {
	for _, when := range []string{"before", "between"} {
		t.Run(when, func(t *testing.T) {
			f := newFixture(t)
			e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 3), t0.Add(time.Hour))
			f.ensureLedger("alpha")
			fp := filepath.Join(f.v.Root, "palace", ".local", "index", "alpha", "chunks.fingerprint")
			write := func() {
				if err := os.WriteFile(fp, []byte("another recipe"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if when == "before" {
				write()
			} else {
				beforeLockFn = func(string, *archive.Entry) { beforeLockFn = nil; write() }
				t.Cleanup(func() { beforeLockFn = nil })
			}
			ledger := f.ledgerLines("alpha")
			_, err := IngestArchive(context.Background(), f.deps(), "alpha", e, IngestOptions{Embed: true})
			if !errors.Is(err, ErrFingerprintStale) {
				t.Fatalf("IngestArchive = %v, want ErrFingerprintStale", err)
			}
			if got := f.ledgerLines("alpha"); strings.Join(got, "\n") != strings.Join(ledger, "\n") {
				t.Fatal("the ledger changed")
			}
			if n := len(f.store("alpha").Chunks(false)); n != 0 {
				t.Fatalf("%d chunks written under another recipe", n)
			}
		})
	}
}

// TestIngestArchiveRefusesAnotherRegime: a cache that holds another embedding
// regime's vectors refuses the writer: nothing is committed and no vector is
// written.
func TestIngestArchiveRefusesAnotherRegime(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 3), t0.Add(time.Hour))
	f.ensureLedger("alpha")
	dir := filepath.Join(f.v.Root, "palace", ".local", "embed-cache", "alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, storage.EmbedCacheFingerprintFile), []byte(embedder.Fingerprint("other-model", 0)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := IngestArchive(context.Background(), f.deps(), "alpha", e, IngestOptions{Embed: true})
	if !errors.Is(err, ErrFingerprintStale) {
		t.Fatalf("IngestArchive = %v, want ErrFingerprintStale", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("the cache holds %d entries, want only the other regime's sidecar", len(ents))
	}
	if _, ok := f.store("alpha").Ledger().Session("S1"); ok {
		t.Fatal("the session was ledgered under another regime")
	}
}

// TestIngestArchiveSupersedes: a newer archive of a ledgered session
// supersedes it (generation 2, the older archive superseded, owning
// nothing); an archive older than the live one met under the lock is
// recorded superseded, never ingested over it.
func TestIngestArchiveSupersedes(t *testing.T) {
	f := newFixture(t)
	a := f.archiveOf("alpha", "S1", transcript(t0, "a", 3), t0.Add(time.Hour))
	b := f.archiveOf("alpha", "S1", transcript(t0, "b", 4), t0.Add(72*time.Hour))
	f.ensureLedger("alpha")
	ingestOnce(t, f, a, IngestOptions{Embed: true})
	if res := ingestOnce(t, f, b, IngestOptions{Embed: true}); res.Outcome != Superseded {
		t.Fatalf("outcome %v, want Superseded", res.Outcome)
	}
	st := f.store("alpha")
	sr, _ := st.Ledger().Session("S1")
	if sr.SHA != b.Manifest.SourceSHA256 || sr.Generation != 2 || sr.State != indexstore.StateLive {
		t.Fatalf("session %+v", sr)
	}
	if !st.Ledger().Superseded(a.Manifest.SourceSHA256) || st.CountChunks(indexstore.ArchiveOwner(a.Manifest.SourceSHA256)) != 0 {
		t.Fatal("the older archive must be superseded and own nothing")
	}

	// An older archive of a session whose live archive is newer.
	c := f.archiveOf("alpha", "S2", transcript(t0, "c-new", 3), t0.Add(240*time.Hour))
	old := f.archiveOf("alpha", "S2", transcript(t0, "c-old", 3), t0.Add(144*time.Hour))
	ingestOnce(t, f, c, IngestOptions{Embed: true})
	if res := ingestOnce(t, f, old, IngestOptions{Embed: true}); res.Outcome != RecordedOlder {
		t.Fatalf("an older archive: outcome %v, want RecordedOlder", res.Outcome)
	}
	st = f.store("alpha")
	if sr, _ := st.Ledger().Session("S2"); sr.SHA != c.Manifest.SourceSHA256 {
		t.Fatalf("the session was rolled back to %s", sr.SHA)
	}
	if !st.Ledger().Superseded(old.Manifest.SourceSHA256) || st.CountChunks(indexstore.ArchiveOwner(old.Manifest.SourceSHA256)) != 0 {
		t.Fatal("the older archive must be recorded superseded and own nothing")
	}
}

// TestIngestArchiveCheckpoint: a Checkpoint error abandons the archive before
// its ledger entry.
func TestIngestArchiveCheckpoint(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 3), t0.Add(time.Hour))
	f.ensureLedger("alpha")
	calls := 0
	_, err := IngestArchive(context.Background(), f.deps(), "alpha", e, IngestOptions{Embed: true, Checkpoint: func(int) error {
		calls++
		return errors.New("disk low")
	}})
	if !errors.Is(err, ErrCheckpoint) || calls != 1 {
		t.Fatalf("IngestArchive = %v after %d calls, want ErrCheckpoint", err, calls)
	}
	if _, ok := f.store("alpha").Ledger().Session("S1"); ok {
		t.Fatal("an archive stopped at a checkpoint was ledgered")
	}
}

// TestIngestArchiveFailsOnCorruptBytes: bytes that do not match the manifest
// are an error that is not one of the run's non-failures.
func TestIngestArchiveFailsOnCorruptBytes(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 3), t0.Add(time.Hour))
	f.ensureLedger("alpha")
	fh, err := os.OpenFile(e.ArchivePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString("junk")
	fh.Close()
	_, err = IngestArchive(context.Background(), f.deps(), "alpha", e, IngestOptions{Embed: true})
	if !errors.Is(err, archive.ErrArchiveCorrupt) {
		t.Fatalf("IngestArchive = %v, want ErrArchiveCorrupt", err)
	}
	if _, ok := f.store("alpha").Ledger().Session("S1"); ok {
		t.Fatal("a corrupt archive was ledgered")
	}
}

// TestIngestArchiveWithoutEmbedding: Embed false (the rebuild's --no-embed)
// commits the archive with no embedder call and no vector; its chunks are
// then missing-vector repairs.
func TestIngestArchiveWithoutEmbedding(t *testing.T) {
	f := newFixture(t)
	e := f.archiveOf("alpha", "S1", transcript(t0, "s1", 3), t0.Add(time.Hour))
	f.ensureLedger("alpha")
	res := ingestOnce(t, f, e, IngestOptions{Embed: false})
	if res.Outcome != Committed || res.Embedded != 0 || f.emb.calls.Load() != 0 {
		t.Fatalf("result %+v, %d embed calls; want a commit with no embedding", res, f.emb.calls.Load())
	}
	for _, c := range f.store("alpha").Chunks(true) {
		if _, hit, _ := f.eng.CachedVector("alpha", c.ID); hit {
			t.Fatalf("chunk %s has a vector", c.ID)
		}
	}
}
