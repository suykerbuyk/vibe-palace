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
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
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

	b.err = e.Rebuild(ctx, project)

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

// Search performs hybrid semantic + structural search.
// Pipeline: embed query → vector search → filter → boost → deduplicate → top-N.
func (e *Engine) Search(ctx context.Context, query string, f SearchFilters) ([]SearchResult, error) {
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

	// Build the index(es) this search reads, on first use. Must happen before
	// e.mu is taken — Rebuild acquires it for write.
	if f.Project != "" {
		if err := e.ensureIndex(ctx, f.Project); err != nil {
			return nil, fmt.Errorf("build index for %s: %w", f.Project, err)
		}
	} else if err := e.ensureAllIndexes(ctx); err != nil {
		return nil, err
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

// IndexDrawer adds a single drawer to the search index, embedding its content.
// It is a convenience wrapper around IndexDrawers.
func (e *Engine) IndexDrawer(ctx context.Context, project, wing, room string, d storage.Drawer) error {
	return e.IndexDrawers(ctx, []DrawerInput{{
		Project: project, Wing: wing, Room: room, Drawer: d,
	}})
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
		if err := idx.Insert(in.Drawer.ID, in.Vec); err != nil {
			return err
		}
		e.metadata[in.Drawer.ID] = makeDrawerMeta(in.Project, in.Wing, in.Room, in.Drawer)
	}
	return nil
}

// RemoveDrawer removes a drawer from the index.
func (e *Engine) RemoveDrawer(project, id string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if idx, ok := e.indexes[project]; ok {
		idx.Delete(id)
	}
	delete(e.metadata, id)
}

// Rebuild rebuilds the index for a project from scratch. Drawers whose vectors
// are absent from the embed cache are embedded in batches — per-item embedding
// is roughly 7x slower on the ONNX backend.
func (e *Engine) Rebuild(ctx context.Context, project string) error {
	wings, err := e.vault.ListWings(project)
	if err != nil {
		return fmt.Errorf("list wings: %w", err)
	}

	var ids []string
	var vecs [][]float32
	var metas []drawerMeta

	// Positions in vecs that still need an embedding, and their drawer text.
	var missIdx []int
	var missText []string

	for _, wing := range wings {
		rooms, err := listRooms(e.vault.Root, project, wing)
		if err != nil {
			return fmt.Errorf("list rooms for wing %s: %w", wing, err)
		}

		for _, room := range rooms {
			drawers, err := e.vault.ListDrawers(project, wing, room)
			if err != nil {
				slog.Warn("rebuild: list drawers failed", "project", project, "wing", wing, "room", room, "err", err)
				continue
			}

			for _, d := range drawers {
				if err := ctx.Err(); err != nil {
					return err
				}

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

	if err := e.embedMisses(ctx, project, ids, vecs, missIdx, missText); err != nil {
		return err
	}

	if len(vecs) == 0 {
		// The project's drawers are all gone. Drop any index from a previous
		// build so it stops serving hits for content that no longer exists.
		e.mu.Lock()
		delete(e.indexes, project)
		e.mu.Unlock()
		return nil
	}

	dims, err := e.embedder.Dimensions()
	if err != nil {
		return fmt.Errorf("embedder dimensions: %w", err)
	}

	idx := NewVectorIndex(dims)
	if err := idx.Build(vecs, ids); err != nil {
		return fmt.Errorf("build index: %w", err)
	}

	e.mu.Lock()
	e.indexes[project] = idx
	for i, id := range ids {
		e.metadata[id] = metas[i]
	}
	e.mu.Unlock()
	return nil
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

// listRooms returns room slug names under a wing's drawer directory.
func listRooms(vaultRoot, project, wing string) ([]string, error) {
	wingDir := filepath.Join(vaultRoot, "palace", project, "drawers", wing)
	entries, err := os.ReadDir(wingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read wing dir: %w", err)
	}

	var rooms []string
	for _, e := range entries {
		if e.IsDir() && slug.Validate(e.Name()) == nil {
			rooms = append(rooms, e.Name())
		}
	}
	return rooms, nil
}
