// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package ingest is the pending-archive ingester (ADR-014 decision 7): one
// idempotent, ledger-driven pass that turns tracked transcript archives into
// this host's local index, and the per-archive commit step that the pass and
// `vp index rebuild` share. It writes only under palace/.local/: the chunk
// store, the local KG, the ledger (through indexstore.Tx) and the embed cache
// (through the engine's Tx-bound writer). It touches no in-memory engine.
//
// Only cmd/vp imports it: `vp drain archives` and the rebuild driver.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// sourceTypeSession is the source type of a transcript chunk, as capture's
// indexer has always recorded it.
const sourceTypeSession = "session"

// embedBatchSize is how many chunks one embedder call takes.
const embedBatchSize = 32

// checkpointEvery is how many chunks pass between Checkpoint calls.
const checkpointEvery = 500

// CacheEngine is what the ingester needs of the search engine: the embed
// cache's regime-checked lookup and its Tx-bound writer. *search.Engine
// satisfies it, built with the run's embedder so the regime is its config's.
type CacheEngine interface {
	CachedVector(project, id string) ([]float32, bool, error)
	CacheWriter(tx *indexstore.Tx) (*search.CacheWriter, error)
}

// Deps are the ingester's collaborators.
type Deps struct {
	Vault  *storage.Vault
	Engine CacheEngine
	// Embedder embeds the cache misses. It should be lazy
	// (embedder.NewLazy): the model loads only on the first miss.
	Embedder embedder.Embedder
	// GraphHealer is the optional graph-fingerprint self-heal seam; nil
	// until hnsw-graph-file-envelope-and-warm-start provides one.
	GraphHealer GraphHealer
	// Now is the clock the wall-clock cap reads; nil means time.Now.
	Now func() time.Time
}

// IngestOptions controls one IngestArchive.
type IngestOptions struct {
	// Embed embeds the cache misses. True on every routine path; the
	// rebuild's --no-embed passes false.
	Embed bool
	// Checkpoint, when set, is called with the running chunk count every
	// checkpointEvery chunks and once before the commit. An error abandons
	// the archive before its ledger entry (ErrCheckpoint).
	Checkpoint func(n int) error
	// Retarget is the source_sha256 of a superseding session's target that
	// is no longer on disk: the archive being ingested replaces it (plan
	// revision R1). Run sets it; empty otherwise.
	Retarget string
}

// Outcome is what IngestArchive did with an archive.
type Outcome int

const (
	// Committed: the archive is its session's live archive now.
	Committed Outcome = iota
	// Superseded: the archive replaced its session's older live archive.
	Superseded
	// AlreadyLedgered: the archive was already its session's live archive
	// (another run committed it); nothing was embedded or written.
	AlreadyLedgered
	// RecordedOlder: under the lock the session's live archive turned out
	// to be newer than this one (another run committed it meanwhile), so
	// this one was recorded superseded rather than rolling the session back.
	RecordedOlder
)

// ArchiveResult reports one IngestArchive.
type ArchiveResult struct {
	SessionID string
	SHA       string
	Path      string
	Outcome   Outcome
	Chunks    int // distinct chunk ids the archive owns
	Embedded  int // chunks embedded (cache misses)
}

// Errors IngestArchive returns that are not a failure of the archive. Run
// treats every other error as a failure (Tx.RecordFailure).
var (
	// ErrFingerprintStale: the project's chunks.fingerprint or embed-cache
	// regime is another's. Nothing was written; the project is skipped until
	// `vp index rebuild`.
	ErrFingerprintStale = errors.New("ingest: the project's index was built under another chunk recipe or embedding regime")
	// ErrCheckpoint: the caller's Checkpoint stopped the archive.
	ErrCheckpoint = errors.New("ingest: stopped at a checkpoint")
)

// Test seams, nil in production: afterPrepareFn runs after the archive is
// prepared and before the lock-free ledger check that precedes embedding;
// beforeLockFn runs after embedding and before the commit lock is taken.
var (
	afterPrepareFn func(project string, e *archive.Entry)
	beforeLockFn   func(project string, e *archive.Entry)
)

// IngestArchive is the per-archive commit step (Scope 1), shared by Run and
// the rebuild driver. The archive must be its session's live archive (Run
// decides that from the listing). Outside every lock it:
//
//  1. reads the archive whole and verified (archive.ReadVerified);
//  2. dates the session (archive.Manifest.SessionDay) and prepares the raw
//     bytes with the project's recipe (palace.ProjectIndexing, palace.Prepare;
//     Chair ruling 1: the raw bytes, as the backfill always did);
//  3. checks the ledger lock-free and returns AlreadyLedgered, embedding
//     nothing, when the archive is already live;
//  4. embeds the chunks the cache misses (Engine.CachedVector), if opts.Embed.
//
// Then, under the project's commit lock, in this order: the chunk recipe
// fingerprint (a mismatch is ErrFingerprintStale, nothing written); the ledger
// re-checked (AlreadyLedgered); the recipe; the embed-cache writer (another
// regime is ErrFingerprintStale); and Tx.CommitArchive, or Tx.Supersede when
// the session's ledger names another archive.
func IngestArchive(ctx context.Context, d Deps, project string, e *archive.Entry, opts IngestOptions) (ArchiveResult, error) {
	res := ArchiveResult{Path: e.ArchivePath}
	if e.Manifest != nil {
		res.SessionID, res.SHA = e.Manifest.SessionID, e.Manifest.SourceSHA256
	}
	va, err := archive.ReadVerified(e)
	if err != nil {
		return res, err
	}
	res.SHA = va.SHA
	if res.SessionID == "" {
		return res, fmt.Errorf("ingest: %s: the manifest names no session", e.ArchivePath)
	}
	sd, err := e.Manifest.SessionDay(va.Bytes)
	if err != nil {
		return res, fmt.Errorf("ingest: date %s: %w", e.ArchivePath, err)
	}
	day, err := time.Parse("2006-01-02", sd.Day)
	if err != nil {
		return res, fmt.Errorf("ingest: session day %q: %w", sd.Day, err)
	}
	ix, err := palace.ProjectIndexing(d.Vault, project)
	if err != nil {
		return res, err
	}
	prep, err := palace.Prepare(ix, palace.PrepareInput{Project: project, SourceRef: res.SessionID, Date: day, Text: string(va.Bytes)})
	if err != nil {
		return res, fmt.Errorf("ingest: prepare %s: %w", e.ArchivePath, err)
	}
	c := toCommit(prep, res, sd, e)
	c.ArchivePath = vaultRel(d.Vault.Root, e.ArchivePath)
	res.Chunks = distinct(c.Chunks)

	if afterPrepareFn != nil {
		afterPrepareFn(project, e)
	}
	if done, err := alreadyLive(d.Vault, project, res.SessionID, res.SHA); err != nil {
		return res, err
	} else if done {
		res.Outcome = AlreadyLedgered
		return res, nil
	}

	vecs, n, err := embedMisses(ctx, d, project, c.Chunks, opts)
	if err != nil {
		return res, err
	}
	c.Vectors, res.Embedded = vecs, n
	if opts.Checkpoint != nil {
		if err := opts.Checkpoint(len(c.Chunks)); err != nil {
			return res, fmt.Errorf("%w: %v", ErrCheckpoint, err)
		}
	}
	if beforeLockFn != nil {
		beforeLockFn(project, e)
	}
	out, err := commit(ctx, d, project, ix.Recipe, c, opts.Retarget)
	res.Outcome = out
	return res, err
}

// toCommit turns Prepare's output into an archive commit owned by the
// archive's source_sha256 (plan revision D2). The store dates archive owners
// from the ledger record at read time, so no record carries a date of its
// own; Prepare's dates were the session day anyway.
func toCommit(prep palace.Prepared, res ArchiveResult, sd archive.SessionDay, e *archive.Entry) indexstore.ArchiveCommit {
	c := indexstore.ArchiveCommit{
		SessionID: res.SessionID, SHA: res.SHA, ArchivePath: e.ArchivePath, CapturedAt: e.Manifest.CapturedAt,
		StartDay: sd.Day, StartDaySource: sd.Source,
	}
	for _, p := range prep.Chunks {
		c.Chunks = append(c.Chunks, indexstore.OwnedChunk{
			Chunk:     indexstore.Chunk{ID: p.ID, Content: p.Content, Wing: p.Wing, Room: p.Room, Hall: p.Hall},
			Ownership: indexstore.Ownership{SourceRef: p.SourceRef, SourceType: sourceTypeSession, ChunkIndex: p.ChunkIndex},
		})
	}
	for _, en := range prep.Entities {
		id, payload := index.EntityRecord(en.ID, en.Name, en.Type)
		c.KG = append(c.KG, indexstore.KGRecord{ID: id, Payload: payload})
	}
	for _, tr := range prep.Triples {
		id, payload := index.TripleRecord(tr.Subject, tr.Predicate, tr.Object, "")
		c.KG = append(c.KG, indexstore.KGRecord{ID: id, Payload: payload})
	}
	return c
}

// vaultRel is path relative to the vault root, slash-separated: the ledger
// records an archive by its vault-relative path, so coverage matches a
// listed archive without rehashing and the record survives a vault move.
func vaultRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// distinct is the number of distinct chunk ids: the chunk count the ledger
// records.
func distinct(chunks []indexstore.OwnedChunk) int {
	seen := map[string]bool{}
	for _, c := range chunks {
		seen[c.ID] = true
	}
	return len(seen)
}

// alreadyLive is the lock-free check before embedding: whether the session's
// latest record is live with this archive.
func alreadyLive(v *storage.Vault, project, session, sha string) (bool, error) {
	st, err := indexstore.ReadStore(v, project)
	if err != nil {
		return false, err
	}
	s, ok := st.Ledger().Session(session)
	return ok && s.State == indexstore.StateLive && s.SHA == sha, nil
}

// embedMisses embeds the distinct chunk ids the cache misses, outside every
// lock, calling opts.Checkpoint every checkpointEvery chunks.
func embedMisses(ctx context.Context, d Deps, project string, chunks []indexstore.OwnedChunk, opts IngestOptions) (map[string][]float32, int, error) {
	vecs := map[string][]float32{}
	var missIDs []string
	var missText []string
	seen := map[string]bool{}
	for i, c := range chunks {
		if opts.Checkpoint != nil && i > 0 && i%checkpointEvery == 0 {
			if err := opts.Checkpoint(i); err != nil {
				return nil, 0, fmt.Errorf("%w: %v", ErrCheckpoint, err)
			}
		}
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		_, hit, err := d.Engine.CachedVector(project, c.ID)
		if err != nil {
			return nil, 0, err
		}
		if !hit {
			missIDs = append(missIDs, c.ID)
			missText = append(missText, c.Content)
		}
	}
	if !opts.Embed || len(missIDs) == 0 {
		return vecs, 0, nil
	}
	if d.Embedder == nil {
		return nil, 0, errors.New("ingest: chunks to embed but no embedder")
	}
	for start := 0; start < len(missIDs); start += embedBatchSize {
		end := min(start+embedBatchSize, len(missIDs))
		got, err := d.Embedder.EmbedBatch(ctx, missText[start:end])
		if err != nil {
			return nil, 0, fmt.Errorf("ingest: embed: %w", err)
		}
		if len(got) != end-start {
			return nil, 0, fmt.Errorf("ingest: embed: %d vectors for %d chunks", len(got), end-start)
		}
		for i, id := range missIDs[start:end] {
			vecs[id] = got[i]
		}
	}
	return vecs, len(missIDs), nil
}

// newerThan reports whether captured_at a is strictly after b. An empty or
// unparseable value is never newer: with no way to tell, the caller's order
// (the listing's newest) stands.
func newerThan(a, b string) bool {
	ta, err := time.Parse(time.RFC3339, a)
	if err != nil {
		return false
	}
	tb, err := time.Parse(time.RFC3339, b)
	if err != nil {
		return false
	}
	return ta.After(tb)
}

// commit is the commit step under the project's index commit lock.
func commit(ctx context.Context, d Deps, project string, recipe index.ChunkRecipe, c indexstore.ArchiveCommit, retarget string) (Outcome, error) {
	tx, err := indexstore.Lock(ctx, d.Vault, project, indexstore.NoTimeout)
	if err != nil {
		return Committed, err
	}
	defer tx.Release()

	// 1. The chunk recipe, under the lock: a fingerprint another process
	// wrote since the pre-check is caught here (plan revision R7).
	st, err := indexstore.ReadFingerprint(d.Vault, project, recipe)
	if err != nil {
		return Committed, err
	}
	if st == indexstore.FingerprintMismatch {
		return Committed, fmt.Errorf("%w: %s: chunks.fingerprint", ErrFingerprintStale, project)
	}
	// 2. The ledger, re-checked under the lock.
	l, err := tx.Ledger()
	if err != nil {
		return Committed, err
	}
	if !l.Exists() {
		return Committed, indexstore.ErrNoLedger
	}
	prev, has := l.Session(c.SessionID)
	if has && prev.State == indexstore.StateLive && prev.SHA == c.SHA {
		return AlreadyLedgered, nil
	}
	// An archive older than the live one is never ingested over it.
	if has && prev.State == indexstore.StateLive && newerThan(prev.CapturedAt, c.CapturedAt) {
		if err := tx.RecordSuperseded(c.SessionID, c.SHA, c.ArchivePath); err != nil {
			return RecordedOlder, err
		}
		return RecordedOlder, tx.Commit()
	}
	// 3. The recipe this commit records when the store has none.
	tx.UseRecipe(recipe)
	// 4. The embed cache's writer, under this engine's regime.
	var vw indexstore.VectorWriter
	if len(c.Vectors) > 0 {
		w, err := d.Engine.CacheWriter(tx)
		if err != nil {
			if errors.Is(err, search.ErrEmbedRegimeMismatch) {
				return Committed, fmt.Errorf("%w: %s: %v", ErrFingerprintStale, project, err)
			}
			return Committed, err
		}
		vw = w
	}
	// 5. The commit, or the supersede.
	out := Committed
	if has {
		out = Superseded
		c.Retarget = retarget
		err = tx.Supersede(c, vw)
	} else {
		err = tx.CommitArchive(c, vw)
	}
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
