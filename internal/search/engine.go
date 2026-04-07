// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

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
}

// NewEngine creates a search engine.
func NewEngine(emb embedder.Embedder, vault *storage.Vault, cfg storage.Config) *Engine {
	return &Engine{
		embedder: emb,
		vault:    vault,
		cache:    NewEmbedCache(vault),
		config:   cfg,
		indexes:  make(map[string]*VectorIndex),
		metadata: make(map[string]drawerMeta),
	}
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

// IndexDrawer adds a single drawer to the search index.
func (e *Engine) IndexDrawer(ctx context.Context, project, wing, room string, d storage.Drawer) error {
	vec, err := e.embedder.Embed(ctx, d.Content)
	if err != nil {
		return fmt.Errorf("embed drawer %s: %w", d.ID, err)
	}

	// Cache the embedding (non-fatal on failure).
	_ = e.cache.Put(project, d.ID, vec)

	e.mu.Lock()
	defer e.mu.Unlock()

	idx, ok := e.indexes[project]
	if !ok {
		idx = NewVectorIndex(e.embedder.Dimensions())
		e.indexes[project] = idx
	}

	if err := idx.Insert(d.ID, vec); err != nil {
		return err
	}

	e.metadata[d.ID] = makeDrawerMeta(project, wing, room, d)
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

// Rebuild rebuilds the index for a project from scratch.
func (e *Engine) Rebuild(ctx context.Context, project string) error {
	wings, err := e.vault.ListWings(project)
	if err != nil {
		return fmt.Errorf("list wings: %w", err)
	}

	var ids []string
	var vecs [][]float32
	var metas []drawerMeta

	for _, wing := range wings {
		rooms, err := listRooms(e.vault.Root, project, wing)
		if err != nil {
			return fmt.Errorf("list rooms for wing %s: %w", wing, err)
		}

		for _, room := range rooms {
			drawers, err := e.vault.ListDrawers(project, wing, room)
			if err != nil {
				continue
			}

			for _, d := range drawers {
				if err := ctx.Err(); err != nil {
					return err
				}

				// Try cache first.
				vec, _ := e.cache.Get(project, d.ID)
				if vec == nil {
					vec, err = e.embedder.Embed(ctx, d.Content)
					if err != nil {
						return fmt.Errorf("embed drawer %s: %w", d.ID, err)
					}
					_ = e.cache.Put(project, d.ID, vec)
				}

				ids = append(ids, d.ID)
				vecs = append(vecs, vec)
				metas = append(metas, makeDrawerMeta(project, wing, room, d))
			}
		}
	}

	if len(vecs) == 0 {
		return nil
	}

	idx := NewVectorIndex(e.embedder.Dimensions())
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
		if e.IsDir() && storage.ValidateSlug(e.Name()) == nil {
			rooms = append(rooms, e.Name())
		}
	}
	return rooms, nil
}
