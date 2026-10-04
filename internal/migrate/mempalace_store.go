// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// A mempalace export has no transcripts, so it has no archive: its drawers,
// entities and triples go only to this host's local index store, as LEDGERED
// IMPORT BATCHES (task importers-write-the-frozen-tracked-corpus, Scope 4 and
// plan revisions R-4, R-5, R-9). The result is single-host and untracked; a
// chunks.fingerprint mismatch makes the next full index rebuild discard it,
// and only re-running the import from the export file restores it.

// mempalaceBatchSize is the most drawers, or KG records, one batch holds.
const mempalaceBatchSize = 1000

// mempalaceEpochDay is the start day of an export with no parseable drawer
// filed_at.
const mempalaceEpochDay = "2000-01-01"

// ErrRecipeMismatch refuses an import into a project whose chunks.fingerprint
// records another chunk recipe: chunks are never added under a stale recipe.
var ErrRecipeMismatch = errors.New("the project's index was built under another chunk recipe; the next full index rebuild discards it, so nothing is imported until then")

// CacheWriterSource gives the importer the embed cache's writer for a held
// Tx. *search.Engine satisfies it. The importer depends on this, not on the
// engine, so it cannot call an engine method that takes a lock while it holds
// a Tx.
type CacheWriterSource interface {
	CacheWriter(tx *indexstore.Tx) (*search.CacheWriter, error)
}

// commitBatchFn is the batch commit step, a seam so a test can count the
// commits and check each runs under a held Tx.
var commitBatchFn = func(tx *indexstore.Tx, c indexstore.BatchCommit, vw indexstore.VectorWriter) error {
	return tx.CommitBatch(c, vw)
}

// beforeBatchLockFn, when a test sets it, runs for batch n after the
// pre-check outside the lock and before the lock is taken.
var beforeBatchLockFn func(n int, batchID string)

// mempalaceBatch is one import batch: chunks, or KG records.
type mempalaceBatch struct {
	id     string
	chunks []indexstore.OwnedChunk
	kg     []indexstore.KGRecord
	nEnts  int // entities among kg
}

// batchID is the n-th import batch id of an export.
func batchID(exportSHA string, n int) string {
	return "mempalace:" + exportSHA + ":" + strconv.Itoa(n)
}

// The sources of a mempalace import's start day.
const (
	StartDayFromFiledAt   = "filed_at"
	StartDayFromValidFrom = "valid_from"
	StartDayFromEpoch     = "epoch"
)

// exportTimeLayouts are the timestamp forms an export carries. The export
// script copies MemPalace's filed_at unchanged, and MemPalace writes Python's
// datetime.isoformat(): often with no zone, often with microseconds. A
// timestamp with no zone is read as UTC.
var exportTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02",
}

// parseExportTime parses one export timestamp or date, in UTC.
func parseExportTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range exportTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// startDay is one import's start day and its source: the UTC day of the
// earliest parseable drawer filed_at; else, for an export whose drawers carry
// none (an export of entities and triples alone), the earliest parseable
// triple valid_from; else the epoch. Every batch carries it; the store's fold
// dates every batch-owned chunk and KG record from it.
func (e *MemPalaceExport) startDay() (string, string) {
	earliest := func(values []string) (time.Time, bool) {
		var min time.Time
		for _, v := range values {
			if t, ok := parseExportTime(v); ok && (min.IsZero() || t.Before(min)) {
				min = t
			}
		}
		return min, !min.IsZero()
	}
	filed := make([]string, 0, len(e.data.Drawers))
	for _, d := range e.data.Drawers {
		filed = append(filed, d.FiledAt)
	}
	if t, ok := earliest(filed); ok {
		return t.Format("2006-01-02"), StartDayFromFiledAt
	}
	valid := make([]string, 0, len(e.data.Triples))
	for _, tr := range e.data.Triples {
		valid = append(valid, tr.ValidFrom)
	}
	if t, ok := earliest(valid); ok {
		return t.Format("2006-01-02"), StartDayFromValidFrom
	}
	return mempalaceEpochDay, StartDayFromEpoch
}

// batches cuts the export into import batches: its non-blank drawers in
// export order, up to mempalaceBatchSize per batch, then its entities and its
// triples as one sequence of KG records, cut the same way. It returns the
// number of blank drawers skipped.
func (e *MemPalaceExport) batches() ([]mempalaceBatch, int) {
	var out []mempalaceBatch
	blank := 0
	var cur []indexstore.OwnedChunk
	flushChunks := func() {
		if len(cur) > 0 {
			out = append(out, mempalaceBatch{id: batchID(e.sha256, len(out)), chunks: cur})
			cur = nil
		}
	}
	for _, d := range e.data.Drawers {
		if d.embedText() == "" {
			blank++
			continue
		}
		room := d.Room
		if room == "" {
			room = "general"
		}
		cur = append(cur, indexstore.OwnedChunk{
			Chunk: indexstore.Chunk{
				ID:      index.ChunkID(d.Content),
				Content: d.Content,
				Wing:    mapWing(d.Wing),
				Room:    room,
				Hall:    palace.DetectHall(d.Content),
			},
			Ownership: indexstore.Ownership{SourceType: "mempalace", SourceRef: d.ID, ChunkIndex: d.ChunkIndex, AddedBy: d.AddedBy},
		})
		if len(cur) == mempalaceBatchSize {
			flushChunks()
		}
	}
	flushChunks()

	var kg []indexstore.KGRecord
	nEnts := 0
	flushKG := func() {
		if len(kg) > 0 {
			out = append(out, mempalaceBatch{id: batchID(e.sha256, len(out)), kg: kg, nEnts: nEnts})
			kg, nEnts = nil, 0
		}
	}
	for _, en := range e.data.Entities {
		id, payload := index.EntityRecord(en.ID, en.Name, en.Type)
		kg = append(kg, indexstore.KGRecord{ID: id, Payload: payload})
		nEnts++
		if len(kg) == mempalaceBatchSize {
			flushKG()
		}
	}
	for _, t := range e.data.Triples {
		validTo := ""
		if t.ValidTo != nil {
			validTo = *t.ValidTo
		}
		id, payload := index.TripleRecord(t.Subject, t.Predicate, t.Object, validTo)
		kg = append(kg, indexstore.KGRecord{ID: id, Payload: payload})
		if len(kg) == mempalaceBatchSize {
			flushKG()
		}
	}
	flushKG()
	return out, blank
}

// ImportMemPalace imports a loaded MemPalace export into project's host-local
// index store, as ledgered import batches. project must exist in the vault: a
// slug in neither tree is not searchable, and the index sweep would remove
// its store.
//
// For each batch: a batch the ledger already records is skipped before it is
// embedded; its chunks are embedded outside the lock (when emb is non-nil);
// then one Tx, in this order: the fingerprint check (a recipe mismatch aborts
// the import with ErrRecipeMismatch before anything is written), the ledger
// (created if missing), the recipe, the skip re-checked under the lock, the
// embed cache's writer from writers (another embedding regime aborts the
// import), and the batch commit (vectors, chunks, KG records, then the batch
// ledger record), then Commit.
//
// writers and emb may be nil when no drawer is embeddable; a dry run uses
// neither and writes nothing.
func ImportMemPalace(
	ctx context.Context,
	vault *storage.Vault,
	project string,
	writers CacheWriterSource,
	emb embedder.Embedder,
	mpExport *MemPalaceExport,
	opts ImportOptions,
) (ImportResult, error) {
	var result ImportResult
	exists, err := vault.ProjectExists(project)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, fmt.Errorf("project %q is not in the vault (neither palace/%s/ nor Projects/%s/ holds a file): import into an existing project", project, project, project)
	}
	batches, blank := mpExport.batches()
	result.DrawersSkippedBlank = blank
	day, daySource := mpExport.startDay()
	result.StartDay, result.StartDaySource = day, daySource
	if opts.DryRun {
		for _, b := range batches {
			result.BatchesCommitted++
			result.DrawersCreated += len(b.chunks)
			result.EntitiesCreated += b.nEnts
			result.TriplesCreated += len(b.kg) - b.nEnts
		}
		return result, nil
	}
	ix, err := palace.ProjectIndexing(vault, project)
	if err != nil {
		return result, err
	}
	for n, b := range batches {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		done, err := batchLedgered(vault, project, b.id)
		if err != nil {
			return result, err
		}
		if done {
			result.BatchesSkipped++
			continue
		}
		vecs, err := embedBatch(ctx, emb, b.chunks)
		if err != nil {
			return result, err
		}
		if beforeBatchLockFn != nil {
			beforeBatchLockFn(n, b.id)
		}
		committed, err := commitMempalaceBatch(ctx, vault, project, ix.Recipe, writers, b, day, vecs, &result)
		if err != nil {
			return result, err
		}
		if !committed {
			result.BatchesSkipped++
			continue
		}
		result.BatchesCommitted++
		result.DrawersCreated += len(b.chunks)
		result.EntitiesCreated += b.nEnts
		result.TriplesCreated += len(b.kg) - b.nEnts
		progress(opts, ProgressEvent{Type: ProgressSessionDone, Project: project, Message: "committed " + b.id, Current: n + 1, Total: len(batches)})
	}
	return result, nil
}

// batchLedgered is the pre-check outside the lock: whether the ledger already
// records the batch, so a committed batch is never embedded again.
func batchLedgered(vault *storage.Vault, project, id string) (bool, error) {
	st, err := indexstore.ReadStore(vault, project)
	if err != nil {
		return false, err
	}
	return batchRecorded(st.Ledger(), id)
}

// batchRecorded reports whether the ledger records batch id. Ledger.StartDay
// answers for sessions and batches alike, so an id that names a session is
// refused (indexstore.ErrBatchIsSession), never read as "already imported".
func batchRecorded(l *indexstore.Ledger, id string) (bool, error) {
	if _, isSession := l.Session(id); isSession {
		return false, fmt.Errorf("%w: %s", indexstore.ErrBatchIsSession, id)
	}
	_, ok := l.StartDay(id)
	return ok, nil
}

// embedBatch embeds a batch's chunks, outside any lock. With no embedder the
// chunks are committed without vectors: they are then local-tier misses
// (the missing-vector stale reason) until a repair embeds them.
func embedBatch(ctx context.Context, emb embedder.Embedder, chunks []indexstore.OwnedChunk) (map[string][]float32, error) {
	vecs := map[string][]float32{}
	if emb == nil || len(chunks) == 0 {
		return vecs, nil
	}
	for start := 0; start < len(chunks); start += embedBatchSize {
		end := min(start+embedBatchSize, len(chunks))
		texts := make([]string, 0, end-start)
		for _, c := range chunks[start:end] {
			texts = append(texts, strings.TrimSpace(c.Content))
		}
		got, err := emb.EmbedBatch(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embed batch: %w", err)
		}
		if len(got) != len(texts) {
			return nil, fmt.Errorf("embed batch: got %d vecs for %d inputs", len(got), len(texts))
		}
		for i, c := range chunks[start:end] {
			vecs[c.ID] = got[i]
		}
	}
	return vecs, nil
}

// commitMempalaceBatch commits one batch in its own Tx, in the order
// ImportMemPalace documents. It reports false when the batch was found
// ledgered under the lock (committed meanwhile by another import).
func commitMempalaceBatch(ctx context.Context, vault *storage.Vault, project string, recipe index.ChunkRecipe,
	writers CacheWriterSource, b mempalaceBatch, day string, vecs map[string][]float32, result *ImportResult) (bool, error) {
	tx, err := indexstore.Lock(ctx, vault, project, indexstore.NoTimeout)
	if err != nil {
		return false, err
	}
	defer tx.Release()

	// 1. The fingerprint first: it needs no ledger, and a mismatch must leave
	// nothing behind, not even a new ledger.
	status, err := indexstore.ReadFingerprint(vault, project, recipe)
	if err != nil {
		return false, err
	}
	if status == indexstore.FingerprintMismatch {
		return false, fmt.Errorf("%w (project %s)", ErrRecipeMismatch, project)
	}
	// 2. The ledger, in every Tx: a discard can remove it mid-run.
	created, err := tx.EnsureLedger(nil)
	if err != nil {
		return false, err
	}
	if created {
		result.LedgerCreated = true
	}
	// 3. The recipe lives on the Tx.
	tx.UseRecipe(recipe)
	// 4. The skip, re-checked under the lock.
	l, err := tx.Ledger()
	if err != nil {
		return false, err
	}
	if done, err := batchRecorded(l, b.id); err != nil || done {
		return false, err
	}
	// 5. The writer, under this engine's regime; another regime aborts.
	var vw indexstore.VectorWriter
	if len(vecs) > 0 {
		if writers == nil {
			return false, errors.New("mempalace import: vectors to write but no embed cache writer")
		}
		w, err := writers.CacheWriter(tx)
		if err != nil {
			if errors.Is(err, search.ErrEmbedRegimeMismatch) {
				return false, fmt.Errorf("mempalace import aborted: %w; nothing more is imported until the next full index rebuild replaces that regime", err)
			}
			return false, err
		}
		vw = w
	}
	// 6. The batch: vectors, chunks, KG records, then its ledger record.
	if err := commitBatchFn(tx, indexstore.BatchCommit{BatchID: b.id, StartDay: day, Vectors: vecs, Chunks: b.chunks, KG: b.kg}, vw); err != nil {
		return false, fmt.Errorf("commit batch %s: %w", b.id, err)
	}
	return true, tx.Commit()
}
