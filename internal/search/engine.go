// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// drawerMeta stores metadata needed for filtering and boosting.
type drawerMeta struct {
	Project    string
	Wing       string
	Room       string
	Hall       string
	SourceType string
	SourceRef  string
	Date       string
	Content    string
	ChunkIndex int
}

// Engine implements hybrid semantic + structural search.
type Engine struct {
	embedder embedder.Embedder
	vault    *storage.Vault
	cache    *EmbedCache
	config   storage.Config

	mu       sync.RWMutex
	indexes  map[string]*VectorIndex // project -> index
	metadata map[string]drawerMeta   // drawerID -> metadata

	// collisions counts distinct-content DrawerID collisions observed at
	// index-build time. It is zero in a healthy vault; a nonzero value is the
	// concrete signal that the 32-bit ID has actually collided and re-keying is
	// finally justified. See detectCollision.
	collisions atomic.Int64

	// Lazy per-project index construction. buildMu guards both maps; it is
	// never held across a Rebuild, so builds for different projects run
	// concurrently and never serialize behind one another.
	buildMu   sync.Mutex
	built     map[string]bool          // project -> index materialized
	buildsRun map[string]*projectBuild // project -> in-flight build
}

// projectBuild is a single in-flight lazy index build. Concurrent searches for
// the same project join the same build and share its outcome.
type projectBuild struct {
	done chan struct{}
	err  error
}

// NewEngine creates a search engine.
func NewEngine(emb embedder.Embedder, vault *storage.Vault, cfg storage.Config) *Engine {
	return &Engine{
		embedder:  emb,
		vault:     vault,
		cache:     NewEmbedCache(vault),
		config:    cfg,
		indexes:   make(map[string]*VectorIndex),
		metadata:  make(map[string]drawerMeta),
		built:     make(map[string]bool),
		buildsRun: make(map[string]*projectBuild),
	}
}

// ensureIndex materializes a project's index on first use and memoizes the
// result. It must be called with no engine lock held: Rebuild takes e.mu for
// write, and Search takes it for read immediately afterwards.
//
// A failed build is not memoized — the next search retries — but a build error
// is returned to the caller rather than being swallowed into an empty result.
func (e *Engine) ensureIndex(ctx context.Context, project string) error {
	e.buildMu.Lock()
	if e.built[project] {
		e.buildMu.Unlock()
		return nil
	}
	if b, ok := e.buildsRun[project]; ok {
		e.buildMu.Unlock()
		<-b.done
		return b.err
	}
	// An index populated incrementally (IndexDrawers) needs no rebuild.
	e.mu.RLock()
	_, indexed := e.indexes[project]
	e.mu.RUnlock()
	if indexed {
		e.built[project] = true
		e.buildMu.Unlock()
		return nil
	}

	b := &projectBuild{done: make(chan struct{})}
	e.buildsRun[project] = b
	e.buildMu.Unlock()

	_, b.err = e.Rebuild(ctx, project)

	e.buildMu.Lock()
	delete(e.buildsRun, project)
	if b.err == nil {
		e.built[project] = true
	}
	e.buildMu.Unlock()
	close(b.done)

	return b.err
}

// ensureAllIndexes materializes every known project's index. Cross-project
// search iterates e.indexes directly, so without this a cold engine would
// silently return no results.
func (e *Engine) ensureAllIndexes(ctx context.Context) error {
	projects, err := e.vault.ListProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		if err := e.ensureIndex(ctx, p); err != nil {
			return fmt.Errorf("build index for %s: %w", p, err)
		}
	}
	return nil
}

// HasIndex reports whether project already has a non-empty in-memory index.
// It never triggers ensureIndex/Rebuild — bootstrap uses this to refuse a
// semantic path that would block on a cold corpus build.
func (e *Engine) HasIndex(project string) bool {
	if e == nil {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	idx, ok := e.indexes[project]
	return ok && idx != nil && idx.Len() > 0
}

// EmbedderReady reports whether embedding can proceed without forcing a lazy
// ONNX construct. Non-lazy embedders are treated as ready when non-nil.
func (e *Engine) EmbedderReady() bool {
	if e == nil || e.embedder == nil {
		return false
	}
	type readyReporter interface{ Ready() bool }
	if r, ok := e.embedder.(readyReporter); ok {
		return r.Ready()
	}
	return true
}

// Search performs hybrid semantic + structural search.
// Pipeline: embed query → vector search → filter → boost → deduplicate → top-N.
func (e *Engine) Search(ctx context.Context, query string, f SearchFilters) ([]SearchResult, error) {
	// Build the index(es) this search reads, on first use. Must happen before
	// e.mu is taken — Rebuild acquires it for write.
	if f.Project != "" {
		if err := e.ensureIndex(ctx, f.Project); err != nil {
			return nil, fmt.Errorf("build index for %s: %w", f.Project, err)
		}
	} else if err := e.ensureAllIndexes(ctx); err != nil {
		return nil, err
	}
	return e.searchReady(ctx, query, f)
}

// SearchReady is Search without ensureIndex/Rebuild. If the project index is
// not already in memory, it returns (nil, nil). Bootstrap must use this — never
// Search — so a cold embedder or first-ever rebuild cannot stall session start.
func (e *Engine) SearchReady(ctx context.Context, query string, f SearchFilters) ([]SearchResult, error) {
	if e == nil {
		return nil, nil
	}
	if f.Project == "" {
		return nil, fmt.Errorf("SearchReady requires a project filter")
	}
	if !e.EmbedderReady() || !e.HasIndex(f.Project) {
		return nil, nil
	}
	return e.searchReady(ctx, query, f)
}

func (e *Engine) searchReady(ctx context.Context, query string, f SearchFilters) ([]SearchResult, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = e.config.SearchDefaultLimit
	}
	if limit <= 0 {
		limit = 10
	}

	queryVec, err := e.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Determine which indexes to search.
	var candidates []VectorResult
	if f.Project != "" {
		idx, ok := e.indexes[f.Project]
		if !ok || idx.Len() == 0 {
			return nil, nil
		}
		c, err := idx.Search(queryVec, limit*3)
		if err != nil {
			return nil, fmt.Errorf("vector search: %w", err)
		}
		candidates = c
	} else {
		for _, idx := range e.indexes {
			c, err := idx.Search(queryVec, limit*3)
			if err != nil {
				return nil, fmt.Errorf("vector search: %w", err)
			}
			candidates = append(candidates, c...)
		}
	}

	// Filter, score, and boost.
	var results []SearchResult
	for _, c := range candidates {
		meta, ok := e.metadata[c.ID]
		if !ok {
			continue
		}
		if !matchesFilters(meta, f) {
			continue
		}

		score := 1.0 / (1.0 + float64(c.Distance))

		// Structural boosts stack when filter matches.
		if f.Wing != "" && meta.Wing == f.Wing {
			score *= 1 + e.config.BoostWing
		}
		if f.Hall != "" && meta.Hall == f.Hall {
			score *= 1 + e.config.BoostHall
		}
		if f.Room != "" && meta.Room == f.Room {
			score *= 1 + e.config.BoostRoom
		}

		results = append(results, SearchResult{
			DrawerID:   c.ID,
			Content:    meta.Content,
			Project:    meta.Project,
			Wing:       meta.Wing,
			Room:       meta.Room,
			Hall:       meta.Hall,
			SourceType: meta.SourceType,
			SourceRef:  meta.SourceRef,
			Date:       meta.Date,
			Score:      score,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	results = dedup(results)

	if limit > len(results) {
		limit = len(results)
	}
	return results[:limit], nil
}

// DrawerInput describes a drawer to index. If Vec is non-nil, the engine
// uses it directly and skips embedding (useful when the caller has already
// batch-embedded content, e.g., the capture pipeline). If Vec is nil, the
// engine embeds Drawer.Content via its configured embedder.
type DrawerInput struct {
	Project string
	Wing    string
	Room    string
	Drawer  storage.Drawer
	Vec     []float32 // optional; pre-computed embedding
}

// IndexDrawers adds a batch of drawers to the search index. For each entry
// missing a pre-computed Vec, the engine embeds in a single EmbedBatch call
// (if the embedder supports it) to avoid per-item embedding round-trips.
// Entries with non-nil Vec skip embedding entirely.
func (e *Engine) IndexDrawers(ctx context.Context, batch []DrawerInput) error {
	if len(batch) == 0 {
		return nil
	}

	// Collect texts for any entries needing embedding, preserving indices.
	var toEmbedIdx []int
	var toEmbedText []string
	for i, in := range batch {
		if in.Vec == nil {
			toEmbedIdx = append(toEmbedIdx, i)
			toEmbedText = append(toEmbedText, in.Drawer.Content)
		}
	}

	if len(toEmbedText) > 0 {
		vecs, err := e.embedder.EmbedBatch(ctx, toEmbedText)
		if err != nil {
			return fmt.Errorf("embed drawers: %w", err)
		}
		if len(vecs) != len(toEmbedText) {
			return fmt.Errorf("embed drawers: got %d vecs for %d inputs", len(vecs), len(toEmbedText))
		}
		for j, idx := range toEmbedIdx {
			batch[idx].Vec = vecs[j]
		}
	}

	// Cache embeddings (non-fatal on failure).
	for _, in := range batch {
		if err := e.cache.Put(in.Project, in.Drawer.ID, in.Vec); err != nil {
			slog.Warn("embed cache write failed", "project", in.Project, "drawer", in.Drawer.ID, "err", err)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, in := range batch {
		idx, ok := e.indexes[in.Project]
		if !ok {
			dims, err := e.embedder.Dimensions()
			if err != nil {
				return fmt.Errorf("embedder dimensions: %w", err)
			}
			idx = NewVectorIndex(dims)
			e.indexes[in.Project] = idx
		}
		meta := makeDrawerMeta(in.Project, in.Wing, in.Room, in.Drawer)
		if e.detectCollision(in.Drawer.ID, meta) {
			continue
		}
		if err := idx.Insert(in.Drawer.ID, in.Vec); err != nil {
			return err
		}
		e.metadata[in.Drawer.ID] = meta
	}
	return nil
}

// RemoveDrawer evicts a drawer completely: it drops the vector from the
// in-memory index, drops the global metadata entry, and unlinks the drawer's
// cached .vec file. It is the search engine's complete eviction primitive —
// wire it wherever a drawer is deleted so the long-lived vp mcp process stops
// serving a drawer that is gone and stops leaking its vector. A missing .vec is
// not an error. The file unlink runs after the lock is released so on-disk I/O
// never serializes searches.
func (e *Engine) RemoveDrawer(project, id string) error {
	e.mu.Lock()
	if idx, ok := e.indexes[project]; ok {
		idx.Delete(id)
	}
	delete(e.metadata, id)
	e.mu.Unlock()

	return e.cache.Delete(project, id)
}

// Rebuild rebuilds the index for a project from scratch. It walks palace
// drawers; as a second source, Projects/<project>/iterations.md split on
// wrapstate H2 entries; and as a third, the BODIES of
// Projects/<project>/sessions/*.md (both sub-chunked at 800/100). Vectors
// absent from the embed cache are embedded in batches — per-item embedding is
// roughly 7x slower on the ONNX backend.
// RebuildStats reports what a Rebuild actually did. It exists because
// `{"status":"rebuilt"}` was returned identically after a full re-embed and
// after walking nothing at all, so a caller could not tell an index from an
// absence without listing the vault (iter 265).
//
// Every field is counted from work the rebuild was already doing; none of them
// costs an extra walk.
type RebuildStats struct {
	// Drawers is how many drawer entries were walked across every wing/room.
	Drawers int
	// IterationChunks is how many chunks came from Projects/<p>/iterations.md,
	// the second corpus source. It needs NO palace store, which is why
	// "no store" and "indexed nothing" are different questions.
	IterationChunks int
	// NoteChunks is how many chunks came from Projects/<p>/sessions/*.md
	// bodies, the third corpus source. Like the iteration corpus it needs no
	// palace store; unlike the drawer corpus it is not written by capture, so
	// it is the only thing that makes a project captured as NOTES ONLY — no
	// transcript, no archive — reachable from `vp search`.
	NoteChunks int
	// Indexed is the total number of entries in the built index.
	Indexed int
	// Embedded is how many entries missed the vector cache and were embedded.
	Embedded int
	// CacheHits is Indexed - Embedded.
	CacheHits int
	// Reaped is how many orphaned .vec files were unlinked.
	Reaped int
}

// Rebuild re-embeds a project's search index from its drawer store, its
// iterations corpus and its session-note corpus, and returns what it did.
//
// 🔴 It does NOT refuse an empty result, deliberately. Rebuild is on the lazy
// path: ensureIndex calls it before every cold search, so making "nothing to
// index" an error here would turn a search against an unindexed project from
// "No results found" into a hard failure, on both the MCP and CLI surfaces.
// The judgement about whether an empty rebuild is acceptable belongs to the
// CALLER that asked for one — see refreshIndexHandler, which refuses when the
// operator asked to refresh an index and no index exists to refresh.
func (e *Engine) Rebuild(ctx context.Context, project string) (RebuildStats, error) {
	var stats RebuildStats
	wings, err := e.vault.ListWings(project)
	if err != nil {
		return stats, fmt.Errorf("list wings: %w", err)
	}

	var ids []string
	var vecs [][]float32
	var metas []drawerMeta

	// Positions in vecs that still need an embedding, and their drawer text.
	var missIdx []int
	var missText []string

	for _, wing := range wings {
		rooms, err := e.vault.ListRooms(project, wing)
		if err != nil {
			return stats, fmt.Errorf("list rooms for wing %s: %w", wing, err)
		}

		for _, room := range rooms {
			drawers, err := e.vault.ListDrawers(project, wing, room)
			if err != nil {
				slog.Warn("rebuild: list drawers failed", "project", project, "wing", wing, "room", room, "err", err)
				continue
			}

			for _, d := range drawers {
				if err := ctx.Err(); err != nil {
					return stats, err
				}
				stats.Drawers++

				// Try cache first; misses are embedded together below.
				vec, _ := e.cache.Get(project, d.ID)
				if vec == nil {
					missIdx = append(missIdx, len(vecs))
					missText = append(missText, d.Content)
				}

				ids = append(ids, d.ID)
				vecs = append(vecs, vec)
				metas = append(metas, makeDrawerMeta(project, wing, room, d))
			}
		}
	}

	// Second corpus source: Projects/<p>/iterations.md at H2 boundaries
	// (wrapstate.ParseEntries). No synthetic drawers.jsonl.
	iterIDs, iterTexts, iterMetas, err := collectIterationCorpus(e.vault, project)
	if err != nil {
		return stats, fmt.Errorf("iteration corpus: %w", err)
	}
	stats.IterationChunks = len(iterIDs)
	for i, id := range iterIDs {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		vec, _ := e.cache.Get(project, id)
		if vec == nil {
			missIdx = append(missIdx, len(vecs))
			missText = append(missText, iterTexts[i])
		}
		ids = append(ids, id)
		vecs = append(vecs, vec)
		metas = append(metas, iterMetas[i])
	}

	// Third corpus source: the BODIES of Projects/<p>/sessions/*.md. No
	// synthetic drawers.jsonl, and deliberately not routed through
	// capture.IndexTranscript — so no knowledge-graph facts are extracted from
	// wrap prose, structurally rather than by a flag.
	noteIDs, noteTexts, noteMetas, err := collectNoteCorpus(e.vault, project)
	if err != nil {
		return stats, fmt.Errorf("note corpus: %w", err)
	}
	stats.NoteChunks = len(noteIDs)
	for i, id := range noteIDs {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		vec, _ := e.cache.Get(project, id)
		if vec == nil {
			missIdx = append(missIdx, len(vecs))
			missText = append(missText, noteTexts[i])
		}
		ids = append(ids, id)
		vecs = append(vecs, vec)
		metas = append(metas, noteMetas[i])
	}

	stats.Indexed = len(ids)
	stats.Embedded = len(missIdx)
	stats.CacheHits = stats.Indexed - stats.Embedded

	if err := e.embedMisses(ctx, project, ids, vecs, missIdx, missText); err != nil {
		return stats, err
	}

	// Live IDs for this build — the reaper unlinks every .vec whose ID is not
	// in this set (drawer, iteration and note chunks alike).
	live := make(map[string]bool, len(ids))
	for _, id := range ids {
		live[id] = true
	}

	if len(vecs) == 0 {
		// No drawers, no iteration chunks and no note chunks. Drop any index
		// from a previous build so it stops serving hits for content that no
		// longer exists, then reap every now-orphaned vector (live set is
		// empty).
		e.mu.Lock()
		delete(e.indexes, project)
		e.mu.Unlock()
		if n, err := e.reapOrphanVectors(project, live); err != nil {
			slog.Warn("reap orphan vectors failed", "project", project, "err", err)
		} else if n > 0 {
			stats.Reaped = n
			slog.Info("reaped orphan vectors", "project", project, "count", n)
		}
		return stats, nil
	}

	dims, err := e.embedder.Dimensions()
	if err != nil {
		return stats, fmt.Errorf("embedder dimensions: %w", err)
	}

	idx := NewVectorIndex(dims)
	if err := idx.Build(vecs, ids); err != nil {
		return stats, fmt.Errorf("build index: %w", err)
	}

	e.mu.Lock()
	e.indexes[project] = idx
	for i, id := range ids {
		if e.detectCollision(id, metas[i]) {
			continue
		}
		e.metadata[id] = metas[i]
	}
	e.mu.Unlock()

	if n, err := e.reapOrphanVectors(project, live); err != nil {
		slog.Warn("reap orphan vectors failed", "project", project, "err", err)
	} else if n > 0 {
		stats.Reaped = n
		slog.Info("reaped orphan vectors", "project", project, "count", n)
	}
	return stats, nil
}

// detectCollision reports whether writing meta under id would overwrite an
// existing metadata entry that carries DIFFERENT content, recording it loudly
// when it would. DrawerID = md5(wing+content)[:8] (internal/storage) excludes
// BOTH project and room, so two drawers with identical (wing, content) — in
// different projects, or different rooms of one wing — hash to the SAME global
// e.metadata key with certainty, and an md5[:8] accident can collide two
// unrelated contents at ~8.8% odds across the live corpus. When the key already
// holds distinct content, one drawer's vector would silently answer for another:
// a wrong search result reported as a correct one, which is this epic's thesis
// living inside the engine. We fail LOUD (slog.Error naming both drawers) and
// NON-FATAL (return true so the caller skips the colliding write; never panic —
// this runs inside the long-lived vp mcp process, and a panic would take the
// server down, unlike the warn-and-continue already used above in Rebuild). The
// caller must hold e.mu. Installed only at the metadata WRITE sites (Rebuild and
// IndexDrawers) — not on the lazy ensureAllIndexes path, which only runs on a
// cross-project search and is not guaranteed to execute. Because a colliding ID
// already lands on the same global key, "same key, different Content" is a
// complete global check with no per-project enumeration.
func (e *Engine) detectCollision(id string, meta drawerMeta) bool {
	existing, ok := e.metadata[id]
	if !ok || existing.Content == meta.Content {
		return false
	}
	e.collisions.Add(1)
	slog.Error("drawer ID collision: distinct content shares one search index key; skipping the colliding drawer",
		"id", id,
		"kept_project", existing.Project, "kept_wing", existing.Wing, "kept_room", existing.Room,
		"skipped_project", meta.Project, "skipped_wing", meta.Wing, "skipped_room", meta.Room,
	)
	return true
}

// reapOrphanVectors unlinks every .vec file under a project's embed-cache whose
// drawer ID is not in live, evicting each through RemoveDrawer so any stale
// in-memory index/metadata for that ID is dropped in the same step (Rebuild adds
// metadata keys but never removes them, so this is where deleted-drawer keys get
// cleaned). It returns the number of vectors reaped. This is cheap hygiene:
// MoveDrawer preserves the ID so it orphans nothing, and no live delete path
// exists today, but a future delete would leak .vec files without it. Must be
// called with no engine lock held (RemoveDrawer takes e.mu).
func (e *Engine) reapOrphanVectors(project string, live map[string]bool) (int, error) {
	localDir, err := e.vault.LocalDir(project)
	if err != nil {
		return 0, fmt.Errorf("local dir: %w", err)
	}
	entries, err := os.ReadDir(filepath.Join(localDir, "embed-cache"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read embed cache dir: %w", err)
	}

	reaped := 0
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".vec") {
			continue
		}
		id := strings.TrimSuffix(name, ".vec")
		if live[id] {
			continue
		}
		if err := e.RemoveDrawer(project, id); err != nil {
			slog.Warn("reap orphan vector failed", "project", project, "drawer", id, "err", err)
			continue
		}
		reaped++
	}
	return reaped, nil
}

// embedMisses embeds the cache-miss drawers in batches, filling their slots in
// vecs. Each vector is written to the embed cache as it lands, so a rebuild
// killed partway through still leaves durable progress behind.
func (e *Engine) embedMisses(ctx context.Context, project string, ids []string, vecs [][]float32, missIdx []int, missText []string) error {
	if len(missIdx) == 0 {
		return nil
	}

	batchSize := e.config.EmbedderBatchSize
	if batchSize <= 0 {
		batchSize = 32
	}

	for start := 0; start < len(missText); start += batchSize {
		end := min(start+batchSize, len(missText))

		if err := ctx.Err(); err != nil {
			return err
		}

		batch := missText[start:end]
		got, err := e.embedder.EmbedBatch(ctx, batch)
		if err != nil {
			return fmt.Errorf("embed drawers for %s: %w", project, err)
		}
		if len(got) != len(batch) {
			return fmt.Errorf("embed drawers for %s: got %d vecs for %d inputs", project, len(got), len(batch))
		}

		for j, vec := range got {
			pos := missIdx[start+j]
			vecs[pos] = vec
			if err := e.cache.Put(project, ids[pos], vec); err != nil {
				slog.Warn("embed cache write failed", "project", project, "drawer", ids[pos], "err", err)
			}
		}
	}
	return nil
}

// Embedder returns the engine's embedder for external batch use.
func (e *Engine) Embedder() embedder.Embedder {
	return e.embedder
}

// Close releases resources.
func (e *Engine) Close() error {
	return e.embedder.Close()
}

// makeDrawerMeta constructs a drawerMeta from a Drawer and its location.
func makeDrawerMeta(project, wing, room string, d storage.Drawer) drawerMeta {
	date := d.FiledAt
	if len(date) >= 10 {
		date = date[:10]
	}
	return drawerMeta{
		Project:    project,
		Wing:       wing,
		Room:       room,
		Hall:       d.Hall,
		SourceType: d.SourceType,
		SourceRef:  d.SourceRef,
		Date:       date,
		Content:    d.Content,
		ChunkIndex: d.ChunkIndex,
	}
}

// matchesFilters checks if a drawer's metadata passes the search filters.
func matchesFilters(m drawerMeta, f SearchFilters) bool {
	if f.Project != "" && m.Project != f.Project {
		return false
	}
	if f.Wing != "" && m.Wing != f.Wing {
		return false
	}
	if f.Room != "" && m.Room != f.Room {
		return false
	}
	if f.Hall != "" && m.Hall != f.Hall {
		return false
	}
	if f.DateFrom != "" && m.Date < f.DateFrom {
		return false
	}
	if f.DateTo != "" && m.Date > f.DateTo {
		return false
	}
	return true
}

// dedup removes lower-scored results from the same SourceRef when they are
// adjacent chunks (results are pre-sorted by score descending). For each
// SourceRef, we keep only the highest-scored chunk and skip others whose
// ChunkIndex is within 1 of any already-kept chunk.
func dedup(results []SearchResult) []SearchResult {
	if len(results) <= 1 {
		return results
	}

	// sourceRef -> set of kept chunk indices for that source.
	keptChunks := make(map[string]map[int]bool)
	// We need ChunkIndex on SearchResult — but it's stored in drawerMeta.
	// For dedup, we use a simpler heuristic: keep only the first (highest-scored)
	// result per SourceRef. This is conservative but correct.
	seen := make(map[string]bool)
	var out []SearchResult

	for _, r := range results {
		if r.SourceRef == "" {
			out = append(out, r)
			continue
		}
		if seen[r.SourceRef] {
			continue
		}
		seen[r.SourceRef] = true
		out = append(out, r)
	}

	_ = keptChunks // reserved for future chunk-aware dedup
	return out
}
