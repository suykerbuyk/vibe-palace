// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Tests for the mempalace import into the host-local store as ledgered import
// batches (task importers-write-the-frozen-tracked-corpus, Scope 4 and plan
// revisions R-4, R-5, R-6, R-9).

// bigExport is an export of nDrawers drawers, nEnts entities and nTriples
// triples, every drawer's content distinct.
func bigExport(nDrawers, nEnts, nTriples int) memPalaceExport {
	var e memPalaceExport
	for i := range nDrawers {
		e.Drawers = append(e.Drawers, memPalaceDrawer{ID: fmt.Sprintf("d%d", i), Wing: "technical", Room: "golang",
			Content: fmt.Sprintf("drawer number %d about the import", i), FiledAt: "2026-04-01T10:00:00Z"})
	}
	for i := range nEnts {
		e.Entities = append(e.Entities, memPalaceEntity{ID: fmt.Sprintf("e%d", i), Name: fmt.Sprintf("Entity %d", i), Type: "concept"})
	}
	for i := range nTriples {
		e.Triples = append(e.Triples, memPalaceTriple{Subject: fmt.Sprintf("s%d", i), Predicate: "uses", Object: "go"})
	}
	return e
}

// newStoreFixture is a vault with project alpha (Projects/alpha/), an engine
// over a counting mock embedder, and the export written to disk.
func newStoreFixture(t *testing.T, e memPalaceExport) (*storage.Vault, *search.Engine, *countingEmbedder, *MemPalaceExport) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Projects", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	v := storage.NewVault(dir)
	emb := &countingEmbedder{Embedder: embedder.NewMock(384)}
	eng := search.NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })
	p := filepath.Join(t.TempDir(), "export.json")
	writeExportJSON(t, p, e)
	return v, eng, emb, mustLoadExport(t, p)
}

func importStore(t *testing.T, v *storage.Vault, eng *search.Engine, emb embedder.Embedder, ex *MemPalaceExport) ImportResult {
	t.Helper()
	res, err := ImportMemPalace(context.Background(), v, "alpha", eng, emb, ex, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// rawBatchLines returns the raw "kind":"batch" lines of alpha's ledger whose
// batch_id is id (or every batch line when id is empty). The Ledger fold is
// last-wins and would hide a duplicate, so the rows that pin "exactly one
// batch record" count raw lines.
func rawBatchLines(t *testing.T, v *storage.Vault, id string) []string {
	t.Helper()
	idir, _ := v.IndexDir("alpha")
	data, err := os.ReadFile(filepath.Join(idir, "ledger.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, `"kind":"batch"`) && (id == "" || strings.Contains(l, `"batch_id":"`+id+`"`)) {
			out = append(out, l)
		}
	}
	return out
}

func readStore(t *testing.T, v *storage.Vault) *indexstore.Store {
	t.Helper()
	st, err := indexstore.ReadStore(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestMempalaceRefusesAnUnknownProject (C6): a project in neither tree is
// refused and nothing is written.
func TestMempalaceRefusesAnUnknownProject(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	if _, err := ImportMemPalace(context.Background(), v, "nope", eng, emb, ex, ImportOptions{}); err == nil {
		t.Fatal("an import into a project the vault does not hold was accepted")
	}
	if _, err := os.Stat(filepath.Join(v.Root, "palace")); !os.IsNotExist(err) {
		t.Fatalf("a refused import wrote under palace/ (stat err %v)", err)
	}
}

// TestMempalaceImportIsSearchableAndSurvivesTheSweep: imported into an
// existing project, the import survives the embed-cache and index sweeps and
// a search of the project returns an imported drawer (its vector was written
// with it, so the chunk is a cache hit).
func TestMempalaceImportIsSearchableAndSurvivesTheSweep(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	importStore(t, v, eng, emb, ex)
	if _, err := v.SweepEmbedCaches(); err != nil {
		t.Fatal(err)
	}
	indexstore.ReapGoneProjects(context.Background(), v)
	searcher := search.NewEngine(embedder.NewMock(384), v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { searcher.Close() })
	res, err := searcher.Search(context.Background(), "Implemented a new search engine with vector embeddings.", search.SearchFilters{Project: "alpha", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || !strings.Contains(res[0].Content, "vector embeddings") {
		t.Fatalf("the imported drawer is not searchable: %+v", res)
	}
}

// TestMempalaceImportIsLocalOnlyAndBatchOwned (ADR-014 lines 539-545): 2,500
// drawers, 40 entities and 60 triples make 3 drawer batches and 1 KG batch.
// Nothing tracked is written; every chunk and KG record is owned by exactly
// one mempalace:<export_sha256>:<n> batch; the ledger records each batch with
// its distinct-id chunk count, the import's start day, start_day_source
// import.
func TestMempalaceImportIsLocalOnlyAndBatchOwned(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, bigExport(2500, 40, 60))
	res := importStore(t, v, eng, emb, ex)
	if res.BatchesCommitted != 4 {
		t.Fatalf("%d batches committed, want 4", res.BatchesCommitted)
	}
	for _, rel := range []string{"palace/alpha", "Projects/alpha/transcripts"} {
		if _, err := os.Stat(filepath.Join(v.Root, rel)); !os.IsNotExist(err) {
			t.Errorf("the import wrote tracked %s (stat err %v)", rel, err)
		}
	}
	st := readStore(t, v)
	want := map[string]int{batchID(ex.sha256, 0): 1000, batchID(ex.sha256, 1): 1000, batchID(ex.sha256, 2): 500, batchID(ex.sha256, 3): 0}
	for id, n := range want {
		lines := rawBatchLines(t, v, id)
		if len(lines) != 1 || !strings.Contains(lines[0], fmt.Sprintf(`"chunk_count":%d`, n)) ||
			!strings.Contains(lines[0], `"start_day":"2026-04-01"`) || !strings.Contains(lines[0], `"start_day_source":"import"`) {
			t.Errorf("batch %s ledger lines %v; want one with chunk_count %d, start_day 2026-04-01, source import", id, lines, n)
		}
	}
	chunks := st.Chunks(true)
	if len(chunks) != 2500 {
		t.Fatalf("%d chunks, want 2500", len(chunks))
	}
	for _, c := range chunks {
		if c.ID != index.ChunkID(c.Content) || len(c.Owners) != 1 || c.Owners[0].Kind != indexstore.OwnerBatch ||
			!strings.HasPrefix(c.Owners[0].ID, "mempalace:"+ex.sha256+":") {
			t.Fatalf("chunk %s: owners %v", c.ID, c.Owners)
		}
	}
	kg := st.KG(true)
	if len(kg) != 100 {
		t.Fatalf("%d KG records, want 100", len(kg))
	}
	for _, r := range kg {
		if len(r.Owners) != 1 || r.Owners[0] != indexstore.BatchOwner(batchID(ex.sha256, 3)) || !strings.Contains(string(r.Payload), `"origin":"extracted"`) {
			t.Fatalf("KG record %s: owners %v payload %s", r.ID, r.Owners, r.Payload)
		}
	}
}

// TestMempalaceFinishesAHalfWrittenBatch: a batch whose chunks are in the
// store with no batch ledger record (a run killed before its ledger step) is
// invisible, and the next import finishes it: exactly one batch record, and
// each record returned once.
func TestMempalaceFinishesAHalfWrittenBatch(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	batches, _ := ex.batches()
	tx, err := indexstore.Lock(context.Background(), v, "alpha", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	ix := mustIndexing(t, v)
	tx.UseRecipe(ix)
	if err := tx.Append(indexstore.BatchOwner(batches[0].id), batches[0].chunks); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := len(readStore(t, v).Chunks(true)); n != 0 {
		t.Fatalf("%d chunks of an unledgered batch are visible", n)
	}
	importStore(t, v, eng, emb, ex)
	if n := len(rawBatchLines(t, v, batches[0].id)); n != 1 {
		t.Fatalf("%d batch records for the finished batch, want 1", n)
	}
	if n := len(readStore(t, v).Chunks(true)); n != 3 {
		t.Fatalf("%d visible chunks after the re-run, want 3, each once", n)
	}
}

// TestMempalaceReimportIsIdempotent: a second import of the same export
// embeds nothing and writes no ledger line; one changed byte gives new batch
// ids.
func TestMempalaceReimportIsIdempotent(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	importStore(t, v, eng, emb, ex)
	idir, _ := v.IndexDir("alpha")
	before, _ := os.ReadFile(filepath.Join(idir, "ledger.jsonl"))
	calls := emb.batches.Load()
	res := importStore(t, v, eng, emb, ex)
	if res.BatchesCommitted != 0 || res.BatchesSkipped != 2 || emb.batches.Load() != calls {
		t.Fatalf("re-import: %+v, embed calls %d -> %d", res, calls, emb.batches.Load())
	}
	if after, _ := os.ReadFile(filepath.Join(idir, "ledger.jsonl")); string(after) != string(before) {
		t.Fatal("a re-import wrote to the ledger")
	}
	changed := testFixture()
	changed.ExportedAt = "2026-04-09T00:00:01Z"
	p := filepath.Join(t.TempDir(), "export.json")
	writeExportJSON(t, p, changed)
	ex2 := mustLoadExport(t, p)
	if ex2.sha256 == ex.sha256 || batchID(ex2.sha256, 0) == batchID(ex.sha256, 0) {
		t.Fatal("a changed export kept its batch ids")
	}
}

// TestMempalaceReportsTheLedgerItCreated: the import that creates the
// project's ledger says so (the output then tells the user the existing
// archives became historical backlog); an import under an existing ledger
// does not.
func TestMempalaceReportsTheLedgerItCreated(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	if res := importStore(t, v, eng, emb, ex); !res.LedgerCreated {
		t.Fatalf("the first import created the ledger but reported %+v", res)
	}
	changed := testFixture()
	changed.ExportedAt = "2026-04-09T00:00:01Z"
	p := filepath.Join(t.TempDir(), "export.json")
	writeExportJSON(t, p, changed)
	res := importStore(t, v, eng, emb, mustLoadExport(t, p))
	if res.BatchesCommitted == 0 || res.LedgerCreated {
		t.Fatalf("an import under an existing ledger: %+v, want batches committed and no ledger created", res)
	}
}

// TestMempalaceBatchStartDay: the import's day is the UTC day of the earliest
// drawer filed_at, in every zone; with none, the epoch; every chunk folds to
// it, whatever its own filed_at.
func TestMempalaceBatchStartDay(t *testing.T) {
	e := testFixture()
	e.Drawers[0].FiledAt = "2026-01-05T09:00:00Z"
	e.Drawers[1].FiledAt = "2025-11-03T22:10:00-05:00"
	e.Drawers[2].FiledAt = "not a date"
	for _, zone := range []string{"America/Los_Angeles", "Asia/Tokyo"} {
		withZone(t, zone, func() {
			v, eng, emb, ex := newStoreFixture(t, e)
			importStore(t, v, eng, emb, ex)
			st := readStore(t, v)
			if day, ok := st.Ledger().StartDay(batchID(ex.sha256, 0)); !ok || day != "2025-11-04" {
				t.Fatalf("%s: start day %q, %v; want 2025-11-04", zone, day, ok)
			}
			for _, c := range st.Chunks(true) {
				if !strings.HasPrefix(c.FiledAt, "2025-11-04") {
					t.Fatalf("%s: chunk %s folds to %s, want the batch day", zone, c.SourceRef, c.FiledAt)
				}
			}
		})
	}
	none := testFixture()
	for i := range none.Drawers {
		none.Drawers[i].FiledAt = ""
	}
	v, eng, emb, ex := newStoreFixture(t, none)
	importStore(t, v, eng, emb, ex)
	if day, _ := readStore(t, v).Ledger().StartDay(batchID(ex.sha256, 0)); day != "2000-01-01" {
		t.Fatalf("no filed_at: start day %q, want 2000-01-01", day)
	}
}

// TestMempalaceRefusesABatchIDThatNamesASession: a batch id that a ledgered
// session already uses is refused by the store, and the import reports it
// and commits nothing for it.
func TestMempalaceRefusesABatchIDThatNamesASession(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	tx, err := indexstore.Lock(context.Background(), v, "alpha", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	tx.UseRecipe(mustIndexing(t, v))
	if _, err := tx.EnsureLedger(nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.CommitArchive(indexstore.ArchiveCommit{SessionID: batchID(ex.sha256, 0), SHA: "sha-x", ArchivePath: "x",
		StartDay: "2026-04-01", StartDaySource: indexstore.DayFromTranscript}, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = ImportMemPalace(context.Background(), v, "alpha", eng, emb, ex, ImportOptions{})
	if !errors.Is(err, indexstore.ErrBatchIsSession) {
		t.Fatalf("import over a session-named batch id: %v, want ErrBatchIsSession", err)
	}
	if n := len(rawBatchLines(t, v, "")); n != 0 {
		t.Fatalf("%d batch records written", n)
	}
}

func mustIndexing(t *testing.T, v *storage.Vault) index.ChunkRecipe {
	t.Helper()
	ix, err := palace.ProjectIndexing(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	return ix.Recipe
}

// TestMempalaceWritesTheChunksFingerprint (5-N4): an import into a project
// with no chunks.fingerprint writes it; a recipe change through config then
// reads as a mismatch, and the engine reports the project stale for it.
func TestMempalaceWritesTheChunksFingerprint(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("HOME", t.TempDir())
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	importStore(t, v, eng, emb, ex)
	if st, err := indexstore.ReadFingerprint(v, "alpha", mustIndexing(t, v)); err != nil || st != indexstore.FingerprintMatch {
		t.Fatalf("after the import: %v, %v; want a match", st, err)
	}
	cfg := filepath.Join(cfgHome, "vibe-palace", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[meta]\nversion_major = 1\n\n[chunker]\nmax_chars = 1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ := indexstore.ReadFingerprint(v, "alpha", mustIndexing(t, v)); st != indexstore.FingerprintMismatch {
		t.Fatalf("after a recipe change: %v, want a mismatch", st)
	}
	stale, reasons, err := eng.Stale("alpha")
	if err != nil || !stale || !slices.Contains(reasons, indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintChunks}) {
		t.Fatalf("Stale = %v %v, %v; want the chunks fingerprint reason", stale, reasons, err)
	}
}

// TestMempalaceCommitsUnderTheLockInBatches (importers-N2, N7; R1): 2,500
// drawers and 100 KG records are exactly 4 CommitBatch calls, each under a held
// Tx; no two commit locks are ever held at once; and the import waits for a
// commit lock another process holds.
func TestMempalaceCommitsUnderTheLockInBatches(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, bigExport(2500, 40, 60))
	var mu sync.Mutex
	var calls []string
	old := commitBatchFn
	commitBatchFn = func(tx *indexstore.Tx, c indexstore.BatchCommit, vw indexstore.VectorWriter) error {
		if !tx.Held() {
			t.Error("CommitBatch called without a held Tx")
		}
		mu.Lock()
		calls = append(calls, c.BatchID)
		mu.Unlock()
		return old(tx, c, vw)
	}
	t.Cleanup(func() { commitBatchFn = old })
	held, maxHeld := 0, 0
	restore := indexstore.ObserveCommitLocks(func(_ string, ev indexstore.CommitLockEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch ev {
		case indexstore.CommitAcquired:
			held++
			maxHeld = max(maxHeld, held)
		case indexstore.CommitReleased:
			held--
		}
	})
	defer restore()

	other, err := indexstore.Lock(context.Background(), v, "alpha", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{})
	var once sync.Once
	beforeBatchLockFn = func(int, string) { once.Do(func() { close(reached) }) }
	t.Cleanup(func() { beforeBatchLockFn = nil })
	done := make(chan error, 1)
	go func() {
		_, err := ImportMemPalace(context.Background(), v, "alpha", eng, emb, ex, ImportOptions{})
		done <- err
	}()
	<-reached
	mu.Lock()
	n := len(calls)
	mu.Unlock()
	if n != 0 {
		t.Fatal("the import committed while another process held the commit lock")
	}
	if err := other.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := []string{batchID(ex.sha256, 0), batchID(ex.sha256, 1), batchID(ex.sha256, 2), batchID(ex.sha256, 3)}
	if !slices.Equal(calls, want) {
		t.Fatalf("CommitBatch calls %v, want %v", calls, want)
	}
	if maxHeld > 1 {
		t.Fatalf("%d commit locks held at once", maxHeld)
	}
}

// TestMempalaceRecheckUnderTheLock: a batch committed by another importer
// between this import's pre-check and its lock is found ledgered under the
// lock and skipped: exactly one batch record; and a re-run embeds nothing.
func TestMempalaceRecheckUnderTheLock(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	batches, _ := ex.batches()
	beforeBatchLockFn = func(n int, id string) {
		if n != 0 {
			return
		}
		tx, err := indexstore.Lock(context.Background(), v, "alpha", indexstore.NoTimeout)
		if err != nil {
			t.Fatal(err)
		}
		tx.UseRecipe(mustIndexing(t, v))
		if _, err := tx.EnsureLedger(nil); err != nil {
			t.Fatal(err)
		}
		if err := tx.CommitBatch(indexstore.BatchCommit{BatchID: id, StartDay: "2026-04-01", Chunks: batches[0].chunks}, nil); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeBatchLockFn = nil })
	res := importStore(t, v, eng, emb, ex)
	if res.BatchesSkipped != 1 {
		t.Fatalf("batches skipped %d, want 1 (the one committed meanwhile)", res.BatchesSkipped)
	}
	if n := len(rawBatchLines(t, v, batches[0].id)); n != 1 {
		t.Fatalf("%d batch records for the batch committed meanwhile, want 1", n)
	}
	beforeBatchLockFn = nil
	calls := emb.batches.Load()
	importStore(t, v, eng, emb, ex)
	if emb.batches.Load() != calls {
		t.Fatal("a re-run embedded again")
	}
}

// TestMempalaceAbortsOnAnotherEmbedRegime: when the project's embed cache
// holds another regime, the import aborts with an error naming it and
// commits no batch.
func TestMempalaceAbortsOnAnotherEmbedRegime(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	dir, _ := v.EmbedCacheDir("alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, storage.EmbedCacheFingerprintFile), []byte("vp-embed behaviour=0 model=other max_seq_len=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ImportMemPalace(context.Background(), v, "alpha", eng, emb, ex, ImportOptions{})
	if !errors.Is(err, search.ErrEmbedRegimeMismatch) {
		t.Fatalf("import over another regime: %v, want ErrEmbedRegimeMismatch", err)
	}
	if n := len(rawBatchLines(t, v, "")); n != 0 {
		t.Fatalf("%d batches committed", n)
	}
}

// TestMempalaceAbortsOnAnotherRecipe: when chunks.fingerprint records another
// recipe, the import refuses before it writes anything: no batch and no
// ledger.
func TestMempalaceAbortsOnAnotherRecipe(t *testing.T) {
	v, eng, emb, ex := newStoreFixture(t, testFixture())
	idir, _ := v.IndexDir("alpha")
	if err := os.MkdirAll(idir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idir, "chunks.fingerprint"), []byte("vp-chunks indexer=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ImportMemPalace(context.Background(), v, "alpha", eng, emb, ex, ImportOptions{})
	if !errors.Is(err, ErrRecipeMismatch) {
		t.Fatalf("import over another recipe: %v, want ErrRecipeMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(idir, "ledger.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a refused import created a ledger (stat err %v)", err)
	}
}

// TestMempalaceSkipsBlankDrawers: a drawer whose content is blank is skipped
// before its id is computed, and counted.
func TestMempalaceSkipsBlankDrawers(t *testing.T) {
	e := testFixture()
	e.Drawers = append(e.Drawers, memPalaceDrawer{ID: "blank", Wing: "memory", Room: "x", Content: "   "})
	v, eng, emb, ex := newStoreFixture(t, e)
	res := importStore(t, v, eng, emb, ex)
	if res.DrawersSkippedBlank != 1 || res.DrawersCreated != 3 {
		t.Fatalf("blank %d, created %d; want 1 and 3", res.DrawersSkippedBlank, res.DrawersCreated)
	}
	for _, c := range readStore(t, v).Chunks(false) {
		if strings.TrimSpace(c.Content) == "" {
			t.Fatal("a blank drawer became a chunk")
		}
	}
}
