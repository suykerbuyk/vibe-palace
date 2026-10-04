// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
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

	// SummaryAvailable is set true only on a RAW row's metadata, only when the
	// collector also emitted a summary row for the same entity in this same
	// Rebuild pass. It is the suppression signal for Search's default
	// raw-hiding behavior — checked as a boolean, never derived from
	// SourceType string comparison (the un-suffixed SourceType literal means
	// SUMMARY for the iteration corpus but RAW for the note corpus, so a
	// string-based check would be silently wrong for one of them).
	SummaryAvailable bool
}

// Engine implements hybrid semantic + structural search.
type Engine struct {
	embedder embedder.Embedder
	vault    *storage.Vault
	cache    *EmbedCache
	config   storage.Config

	mu       sync.RWMutex
	indexes  map[string]VectorIndex // project -> index
	metadata map[string]drawerMeta  // drawerID -> metadata

	// collisions counts distinct-content DrawerID collisions observed at
	// index-build time. It is zero in a healthy vault; a nonzero value is the
	// concrete signal that the 32-bit ID has actually collided and re-keying is
	// finally justified. See detectCollision.
	collisions atomic.Int64

	// Lazy per-project index construction. buildMu guards both maps; it is
	// never held across a Rebuild, so builds for different projects run
	// concurrently and never serialize behind one another.
	//
	// loaded is the store change counter (indexstore.ReadGeneration) each
	// project's in-memory index was built at. A project is current only while
	// the counter still reads that value: any change to gen or epoch means
	// another process (or this one, out of step) wrote the store, and the next
	// search rebuilds the project in full (Scope 7's reload rule; there is no
	// incremental load). A project with no entry has never been built, or was
	// invalidated (a capture insert that timed out), and the next search
	// builds it.
	//
	// invalidated counts, per project, the marks "out of date" a capture insert
	// made when its lock wait ran out (insertSkipped). A build records the
	// count when it starts and does not remember itself as current if the
	// count moved while it ran: the insert it raced may be a drawer the build
	// had already listed past, and the counter does not move for it.
	buildMu     sync.Mutex
	loaded      map[string]indexstore.Gen // project -> counter its index was built at
	buildsRun   map[string]*projectBuild  // project -> in-flight build
	invalidated map[string]uint64         // project -> capture invalidations so far

	// sems are the per-project mutexes (see lock.go). semMu guards the map.
	semMu sync.Mutex
	sems  map[string]chan struct{}

	// beforeIndexClose, when set, runs just before a replaced index is
	// closed. Tests use it to prove e.mu is not held there.
	beforeIndexClose func()

	// indexSweep runs the host-local index sweep (indexstore.ReapGoneProjects)
	// once per engine, from reap, before reap takes any commit lock.
	indexSweep sync.Once
}

// projectBuild is a single in-flight lazy index build. Concurrent searches for
// the same project join the same build and share its outcome.
type projectBuild struct {
	done chan struct{}
	err  error
}

// NewEngine creates a search engine.
func NewEngine(emb embedder.Embedder, vault *storage.Vault, cfg storage.Config) *Engine {
	cache := NewEmbedCache(vault)
	if emb != nil {
		// Only an engine that can embed validates the cache's regime: one with
		// no embedder (vp check's) must never invalidate vectors it cannot
		// replace.
		cache.fingerprint = embedder.Fingerprint(cfg.EmbedderModel, cfg.EmbedderMaxSeqLen)
	}
	return &Engine{
		embedder:    emb,
		vault:       vault,
		cache:       cache,
		config:      cfg,
		indexes:     make(map[string]VectorIndex),
		metadata:    make(map[string]drawerMeta),
		loaded:      make(map[string]indexstore.Gen),
		buildsRun:   make(map[string]*projectBuild),
		invalidated: make(map[string]uint64),
		sems:        make(map[string]chan struct{}),
	}
}

// ErrIndexNotReady is the first-build answer: a search reached a project that
// has no in-memory index yet, and could not build one because another writer
// in this process holds the project (it is waiting on an index commit another
// process holds). search-first-build-and-empty-corpus-answer maps it to the
// user-facing answer.
var ErrIndexNotReady = errors.New("search index not built yet: the project is busy")

// joinBuildHook, when a test sets it, runs when a search joins another
// search's in-flight build of the same project.
var joinBuildHook func(project string)

// ensureIndex builds or reloads a project's index when the store says it must,
// and returns once the project can be searched. It must be called with no
// engine lock held: Rebuild acquires e.mu for write.
//
// The reload rule (Scope 7): the store change counter is read first. The
// project is current only while its in-memory index was built at exactly that
// counter value; anything else (never built, invalidated, another gen or
// epoch, a counter that is missing now or cannot be read) rebuilds the project
// in full. Concurrent searches for one project join the same build.
//
// The build waits at most searchLockTimeout for the project's mutex. When that
// runs out, a project that is already in memory is served as it is and stays
// marked out of date, so the next search retries; a project with nothing in
// memory returns ErrIndexNotReady. A failed build is not remembered, and its
// error is returned rather than swallowed into an empty result.
func (e *Engine) ensureIndex(ctx context.Context, project string) error {
	g, gerr := indexstore.ReadGeneration(e.vault, project)
	e.buildMu.Lock()
	lg, ok := e.loaded[project]
	if ok && gerr == nil && lg == g {
		e.buildMu.Unlock()
		return nil
	}
	if b, ok := e.buildsRun[project]; ok {
		e.buildMu.Unlock()
		if joinBuildHook != nil {
			joinBuildHook(project)
		}
		<-b.done
		return b.err
	}
	b := &projectBuild{done: make(chan struct{})}
	e.buildsRun[project] = b
	e.buildMu.Unlock()

	e.mu.RLock()
	_, inMemory := e.indexes[project]
	e.mu.RUnlock()

	// However the build ends, the in-flight entry is cleared and every joined
	// search released: a build that panicked must not leave later searches of
	// the project waiting on it for ever. The panic itself propagates.
	finished := false
	defer func() {
		if finished {
			return
		}
		r := recover()
		b.err = fmt.Errorf("build index for %s did not finish: %v", project, r)
		e.endBuild(project, b)
		if r != nil {
			panic(r)
		}
	}()

	_, b.err = e.rebuildAndRemember(ctx, project, searchLockTimeout)
	finished = true
	if isLockTimeout(b.err) {
		if ok || inMemory {
			slog.Info("search serves the loaded index: the project is busy", "project", project)
			b.err = nil
		} else {
			b.err = fmt.Errorf("%w: %s", ErrIndexNotReady, project)
		}
	}
	e.endBuild(project, b)
	return b.err
}

// endBuild clears project's in-flight build and releases every search that
// joined it.
func (e *Engine) endBuild(project string, b *projectBuild) {
	e.buildMu.Lock()
	delete(e.buildsRun, project)
	e.buildMu.Unlock()
	close(b.done)
}

// ensureAllIndexes materializes every known project's index. Cross-project
// search iterates e.indexes directly, so without this a cold engine would
// silently return no results.
//
// "Every known project" is the union of both trees (ListAllProjects), not just
// the palace/ stores. Rebuild reads two Projects/-resident corpora —
// iterations.md and session-note bodies — that need no palace/ store, so a
// project captured as notes only is searchable, and a cross-project search that
// enumerated palace/ alone never reached it. (It used to reach one only on a
// host where someone had searched that project directly first, because that
// search conjured a palace/<slug>/ holding the embed cache. The cache no longer
// lives there.)
//
// A build failure for any one project still fails the whole search, naming the
// project. A cross-project result that silently dropped a project would present
// itself as complete.
//
// It returns the set it listed, and that set — not e.indexes — is what the
// cross-project search reads. A long-lived engine keeps the in-memory index of
// a project that has since moved to another vault (ListAllProjects stops
// listing it; nothing evicts the index), and searching every index would serve
// it.
func (e *Engine) ensureAllIndexes(ctx context.Context) (map[string]bool, error) {
	projects, err := e.vault.ListAllProjects()
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	listed := make(map[string]bool, len(projects))
	for _, p := range projects {
		if err := e.ensureIndex(ctx, p.Slug); err != nil {
			return nil, fmt.Errorf("build index for %s: %w", p.Slug, err)
		}
		listed[p.Slug] = true
	}
	return listed, nil
}

// HasIndex reports whether project already has a complete, non-empty
// in-memory index: one a build made, never one a cold insert started.
// It never triggers ensureIndex/Rebuild — bootstrap uses this to refuse a
// semantic path that would block on a cold corpus build.
func (e *Engine) HasIndex(project string) bool {
	if e == nil {
		return false
	}
	e.buildMu.Lock()
	_, built := e.loaded[project]
	e.buildMu.Unlock()
	if !built {
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
// Pipeline: check project exists → embed query → vector search → filter →
// boost → deduplicate → top-N.
func (e *Engine) Search(ctx context.Context, query string, f SearchFilters) ([]SearchResult, error) {
	// A project-scoped search must refuse an unknown project before anything
	// else runs — in particular before ensureIndex/Rebuild, and therefore
	// before searchReady ever forces embedder construction. This is the one
	// check both the MCP and CLI surfaces inherit from; see ProjectExists. The
	// cross-project path (f.Project == "") needs no such check: ensureAllIndexes
	// below only ever iterates projects ListAllProjects itself reports, so it
	// can never be asked about one that doesn't exist.
	if f.Project != "" {
		exists, err := ProjectExists(e.vault, f.Project)
		if err != nil {
			return nil, fmt.Errorf("check project exists: %w", err)
		}
		if !exists {
			return nil, &UnknownProjectError{Project: f.Project}
		}
	}

	// Build the index(es) this search reads, on first use. Must happen before
	// e.mu is taken — Rebuild acquires it for write.
	if f.Project != "" {
		if err := e.ensureIndex(ctx, f.Project); err != nil {
			return nil, fmt.Errorf("build index for %s: %w", f.Project, err)
		}
		return e.searchReady(ctx, query, f, nil)
	}
	listed, err := e.ensureAllIndexes(ctx)
	if err != nil {
		return nil, err
	}
	return e.searchReady(ctx, query, f, listed)
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
	// The same membership predicate Search applies, so a long-lived engine
	// never serves the in-memory index of a project that has since left the
	// vault. Unlike Search, "not a member" is no results, not an error: this is
	// bootstrap's best-effort path, which degrades rather than refuses.
	exists, err := ProjectExists(e.vault, f.Project)
	if err != nil || !exists {
		return nil, err
	}
	return e.searchReady(ctx, query, f, nil)
}

// searchReady searches the in-memory indexes. A project-scoped search reads
// f.Project's index; a cross-project search reads the indexes of exactly the
// projects in listed (ensureAllIndexes' result) and refuses to run without it,
// so no caller can reach every index unfiltered.
func (e *Engine) searchReady(ctx context.Context, query string, f SearchFilters, listed map[string]bool) ([]SearchResult, error) {
	if f.Project == "" && listed == nil {
		return nil, fmt.Errorf("cross-project search needs the listed project set")
	}
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
		c, err := idx.Search(queryVec, candidateCount(limit))
		if err != nil {
			return nil, fmt.Errorf("vector search: %w", err)
		}
		candidates = c
	} else {
		for project, idx := range e.indexes {
			if !listed[project] {
				continue
			}
			c, err := idx.Search(queryVec, candidateCount(limit))
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
		if !f.IncludeRaw && meta.SummaryAvailable {
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
//
// The batch is split by project and committed one project at a time, in
// sorted slug order: the project's mutex, its commit lock, the vectors
// written to the embed cache, release, then the next project. The vectors are
// committed before the in-memory insert, so a chunk this inserts is a cache
// hit for every later build.
//
// It is the capture path's writer, so it waits at most searchLockTimeout for
// each project (ADR-014 decision 7: note-time writers never stall behind an
// ingest). When a project's wait runs out, its entries are neither committed
// nor inserted, and the project is marked out of date, so the next search
// rebuilds it from the tracked drawers capture wrote first and embeds them
// again. On a vault that carries the migration marker there is no tracked
// drawer to rebuild from, so a timeout is an error instead (capture never
// calls this on such a vault: the capture child deletes IndexTranscript before
// the marker can be written).
//
// On a project whose index is not in memory yet the insert is deferred: the
// vectors are committed, and the next search builds the project (Scope 6, the
// cold insert), so a partial insert never makes a project look built.
func (e *Engine) IndexDrawers(ctx context.Context, batch []DrawerInput) error {
	return e.indexDrawers(ctx, batch, searchLockTimeout)
}

// IndexDrawersWait is IndexDrawers with no lock timeout, for an explicit
// operator run that must not drop vectors: the mempalace import. It is
// transitional: importers-write-the-frozen-tracked-corpus moves the importer
// to the store's CommitBatch and deletes it.
func (e *Engine) IndexDrawersWait(ctx context.Context, batch []DrawerInput) error {
	return e.indexDrawers(ctx, batch, indexstore.NoTimeout)
}

func (e *Engine) indexDrawers(ctx context.Context, batch []DrawerInput, timeout time.Duration) error {
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

	byProject := map[string][]DrawerInput{}
	for _, in := range batch {
		byProject[in.Project] = append(byProject[in.Project], in)
	}
	projects := make([]string, 0, len(byProject))
	for p := range byProject {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	for _, p := range projects {
		if err := e.indexProjectDrawers(ctx, p, byProject[p], timeout); err != nil {
			return err
		}
	}
	return nil
}

// indexProjectDrawers commits and inserts one project's share of a batch.
func (e *Engine) indexProjectDrawers(ctx context.Context, project string, ins []DrawerInput, timeout time.Duration) error {
	pl, err := e.lockProject(ctx, project, timeout)
	if err != nil {
		if isLockTimeout(err) {
			return e.insertSkipped(project, err)
		}
		return err
	}
	defer pl.release()
	tx, err := pl.Tx(ctx, timeout)
	if errors.Is(err, indexstore.ErrProjectGone) {
		return nil
	}
	if err != nil {
		if isLockTimeout(err) {
			return e.insertSkipped(project, err)
		}
		return err
	}
	vecs := make(map[string][]float32, len(ins))
	for _, in := range ins {
		vecs[in.Drawer.ID] = in.Vec
	}
	e.putVectorsLocked(tx, vecs)
	before, after, err := pl.finishTx()
	if err != nil {
		slog.Warn("index commit failed", "project", project, "err", err)
	}

	e.buildMu.Lock()
	lg, warm := e.loaded[project]
	if warm && err == nil && chainsFrom(lg, before) {
		e.loaded[project] = after
	}
	e.buildMu.Unlock()
	if !warm {
		return nil // deferred: the next search builds the project
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, in := range ins {
		idx, ok := e.indexes[in.Project]
		if !ok {
			dims, err := e.embedder.Dimensions()
			if err != nil {
				return fmt.Errorf("embedder dimensions: %w", err)
			}
			idx, err = newIndex(kindBrute, dims, provisionalHNSWParams)
			if err != nil {
				return err
			}
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

// insertSkipped handles a capture insert whose lock wait ran out: the project
// is marked out of date, so the next search rebuilds it from the tracked
// drawers. With the migration marker there are none, so it is an error.
func (e *Engine) insertSkipped(project string, cause error) error {
	migrated, err := storage.VaultMigrated(e.vault.Root)
	if err != nil {
		return fmt.Errorf("index drawers for %s: %w (and the migration marker could not be read: %v)", project, cause, err)
	}
	if migrated {
		return fmt.Errorf("index drawers for %s: %w", project, cause)
	}
	e.buildMu.Lock()
	delete(e.loaded, project)
	e.invalidated[project]++
	e.buildMu.Unlock()
	slog.Info("index insert skipped: the project is busy; the next search rebuilds it", "project", project, "err", cause)
	return nil
}

// putVectorsLocked writes vecs to project's embed cache under tx. A cache
// that holds another embedding regime refuses the writer: the vectors then
// serve this process from memory only (ADR-014 decision 3), and nothing is
// written. Any other failure is logged and is not fatal: a missing vector is
// embedded again by a later build.
func (e *Engine) putVectorsLocked(tx *indexstore.Tx, vecs map[string][]float32) {
	w, err := e.cache.Writer(tx)
	if err != nil {
		if !errors.Is(err, ErrEmbedRegimeMismatch) {
			slog.Warn("embed cache writer failed", "project", tx.Project(), "err", err)
		}
		return
	}
	if err := tx.PutVectors(w, vecs); err != nil {
		slog.Warn("embed cache write failed", "project", tx.Project(), "err", err)
	}
}

// evictLocked evicts a drawer completely, for a caller that ALREADY HOLDS
// the project's index commit lock (tx): the reaper. It drops the vector from
// the in-memory index, drops the global metadata entry, and unlinks the cached
// .vec (a missing one is not an error). It never takes a project mutex or a
// commit lock itself; taking the commit lock again here would deadlock,
// because a second acquisition by the same process blocks.
func (e *Engine) evictLocked(tx *indexstore.Tx, project, id string) error {
	e.evictMemory(project, id)
	return e.cache.deleteLocked(tx, id)
}

// evictMemory drops id from project's in-memory index and the metadata.
func (e *Engine) evictMemory(project, id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if idx, ok := e.indexes[project]; ok {
		idx.Delete(id)
	}
	delete(e.metadata, id)
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
	// DecisionChunks is how many decision chunks came from the host-local
	// store (notes tier: their misses are embedded).
	DecisionChunks int
	// LocalChunks is how many local-tier chunks (chunks of ledgered sources)
	// were loaded, as embed-cache hits; LocalMisses is how many had no vector
	// and were left out (they set the missing-vector stale reason).
	LocalChunks int
	LocalMisses int
	// DedupedDrawers is how many tracked drawers were skipped because their
	// content is already a store chunk (the glide-path dedup).
	DedupedDrawers int
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
//
// It is the explicit entry (vp_refresh_index, vp search --rebuild), so it waits
// for the project's mutex and commit lock with no timeout. The lazy search
// path (ensureIndex) builds through the same code with searchLockTimeout.
func (e *Engine) Rebuild(ctx context.Context, project string) (RebuildStats, error) {
	return e.rebuildAndRemember(ctx, project, indexstore.NoTimeout)
}

// rebuildAndRemember rebuilds project and remembers the store counter its
// index now matches. The counter is read BEFORE the build (the reader rule,
// indexstore.ReadGeneration): a write that lands during the build moves the
// counter past the remembered value, and the next search rebuilds again. The
// cache forgets its remembered regime, so a regime another process replaced
// is read afresh.
func (e *Engine) rebuildAndRemember(ctx context.Context, project string, timeout time.Duration) (RebuildStats, error) {
	e.buildMu.Lock()
	inv := e.invalidated[project]
	e.buildMu.Unlock()
	g, gerr := indexstore.ReadGeneration(e.vault, project)
	e.cache.Forget(project)
	return e.rebuild(ctx, project, timeout, buildStart{gen: g, readable: gerr == nil, invalidated: inv})
}

// buildStart is what a build knew when it started: the store counter (and
// whether it could be read) and the project's capture-invalidation count.
type buildStart struct {
	gen         indexstore.Gen
	readable    bool
	invalidated uint64
}

// remember records the counter project's in-memory index now matches. It is
// called while the build still holds the project's mutex, so a writer waiting
// on that mutex (IndexDrawers) sees the project as current the moment it gets
// in, and inserts instead of deferring. A counter that could not be read is
// not remembered, and neither is a build that skipped a write because the
// commit lock was busy (a vector batch, the completeness record): the next
// search rebuilds and tries the write again (ADR-014 decision 7: a skipped
// search-path write is redone by the next search that finds the same
// condition). Nor is a build during which a capture insert timed out and
// marked the project out of date (Engine.invalidated): the drawer it carried
// may postdate the build's listing, and no counter change will announce it.
func (e *Engine) remember(project string, chain *genChain, start buildStart) {
	e.buildMu.Lock()
	defer e.buildMu.Unlock()
	if !start.readable || chain.skipped || e.invalidated[project] != start.invalidated {
		delete(e.loaded, project)
		return
	}
	e.loaded[project] = chain.final(start.gen)
}

// genChain follows a build's own commits from the counter it started at. A
// build that saw every commit since then (its own Txs, each starting where the
// previous one left the counter) may remember the counter its last commit left;
// one that did not must keep the starting value, so the next search rebuilds
// and picks up the other writer's change.
type genChain struct {
	cur     indexstore.Gen
	broken  bool
	skipped bool // a write was skipped because the commit lock was busy
}

func (c *genChain) step(before, after indexstore.Gen) {
	if !chainsFrom(c.cur, before) {
		c.broken = true
	}
	c.cur = after
}

// chainsFrom reports whether a Tx that found the counter at before follows
// directly from remembered: the same value, or, when there was no counter
// yet, the counter Lock just created (gen 0, a fresh epoch), which no write
// has moved.
func chainsFrom(remembered, before indexstore.Gen) bool {
	if remembered == (indexstore.Gen{}) {
		return before.Gen == 0
	}
	return before == remembered
}

func (c *genChain) final(start indexstore.Gen) indexstore.Gen {
	if c.broken {
		return start
	}
	return c.cur
}

// rebuild is Rebuild's body. It holds the project's mutex for the whole run,
// so it never interleaves with IndexDrawers on the project
// (their inserts can no longer be dropped by its swap, nor their vectors
// reaped). It embeds without the commit lock, commits each embed batch in its
// own short Tx, and takes one final Tx for the reap. A commit lock that is busy
// past timeout skips that write: the batch serves this process from memory and
// is embedded again later, and the reap waits for the next build. Only a busy
// mutex fails the build, with an error wrapping vaultlock.ErrLockWaitTimeout.
func (e *Engine) rebuild(ctx context.Context, project string, timeout time.Duration, start buildStart) (RebuildStats, error) {
	var stats RebuildStats
	pl, err := e.lockProject(ctx, project, timeout)
	if err != nil {
		return stats, err
	}
	defer pl.release()
	chain := genChain{cur: start.gen}

	// The embed cache's directory first: resolving it runs the cache's one-time
	// layout sweep (legacy caches migrated, husks healed, orphan caches reaped)
	// before any commit lock is held, and before the reap reads the layout. For
	// a project with no corpus this is the only cache access a Rebuild makes.
	if _, err := e.cache.dir(project); err != nil {
		return stats, fmt.Errorf("embed cache dir: %w", err)
	}

	var ids []string
	var vecs [][]float32
	var metas []drawerMeta

	// Positions in vecs that still need an embedding, and their drawer text.
	var missIdx []int
	var missText []string

	facts := buildFacts{tiers: map[string]indexstore.TierRecord{}}

	// First corpus source: the host-local store, ledgered sources only
	// (indexstore's Chunks(true): an archive that is a session's live archive,
	// a ledgered import batch, or a note). A chunk left by a killed run before
	// its ledger entry is invisible here, and never sets stale. Decision chunks
	// are notes-tier: their misses are embedded. Every other chunk is
	// local-tier: embed-cache hits only; a miss is counted and left for the
	// ingester's repair pass or an explicit rebuild, never embedded here.
	st, err := readStoreFn(e.vault, project)
	if err != nil {
		return stats, fmt.Errorf("read index store: %w", err)
	}
	storeIDs := map[string]bool{}
	decisionNotes, localOwners := map[string]bool{}, map[string]bool{}
	for _, c := range st.Chunks(true) {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		storeIDs[c.ID] = true
		vec, err := e.cachedVector(project, c.ID)
		if err != nil {
			return stats, err
		}
		if c.SourceType == storage.SourceTypeDecision {
			stats.DecisionChunks++
			if c.Selected != nil {
				decisionNotes[c.Selected.ID] = true
			}
			if vec == nil {
				missIdx = append(missIdx, len(vecs))
				missText = append(missText, c.Content)
			}
		} else {
			if vec == nil {
				facts.localMisses++
				continue
			}
			facts.localHits++
			if c.Selected != nil {
				localOwners[c.Selected.Kind+"\x00"+c.Selected.SHA+c.Selected.ID] = true
			}
		}
		ids = append(ids, c.ID)
		vecs = append(vecs, vec)
		metas = append(metas, storedChunkMeta(project, c))
	}
	stats.LocalChunks = facts.localHits
	stats.LocalMisses = facts.localMisses
	if stats.DecisionChunks > 0 {
		facts.tiers[tierDecisions] = indexstore.TierRecord{Sources: len(decisionNotes)}
	}
	if facts.localHits > 0 || facts.localMisses > 0 {
		facts.tiers[tierLocal] = indexstore.TierRecord{Sources: len(localOwners)}
	}

	// Second corpus source: the tracked drawers, the glide path, read only
	// while the vault carries no migration marker (ADR-014 decision 11). A
	// drawer whose content is already a store chunk is skipped, so the store's
	// record (its wing, room and date) wins: the match is on the content hash
	// alone (index.ChunkID), never on the drawer's 32-bit id, which collides at
	// corpus scale, and never on the wing, so a reclassified copy is the same
	// chunk.
	migrated, err := storage.VaultMigrated(e.vault.Root)
	if err != nil {
		return stats, err
	}
	var wings []string
	if !migrated {
		if wings, err = e.vault.ListWings(project); err != nil {
			return stats, fmt.Errorf("list wings: %w", err)
		}
	}

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
				if storeIDs[index.ChunkID(d.Content)] {
					stats.DedupedDrawers++
					continue
				}
				stats.Drawers++

				// Try cache first; misses are embedded together below.
				vec, err := e.cachedVector(project, d.ID)
				if err != nil {
					return stats, err
				}
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

	if stats.Drawers > 0 {
		facts.tiers[tierTrackedDrawers] = indexstore.TierRecord{Sources: stats.Drawers}
	}

	// Third corpus source: Projects/<p>/iterations.md at H2 boundaries
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
		vec, err := e.cachedVector(project, id)
		if err != nil {
			return stats, err
		}
		if vec == nil {
			missIdx = append(missIdx, len(vecs))
			missText = append(missText, iterTexts[i])
		}
		ids = append(ids, id)
		vecs = append(vecs, vec)
		metas = append(metas, iterMetas[i])
	}

	if n := distinctSources(iterMetas, "/raw"); n > 0 {
		facts.tiers[tierIterations] = indexstore.TierRecord{Sources: n}
	}

	// Fourth corpus source: the BODIES of Projects/<p>/sessions/*.md. No
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
		vec, err := e.cachedVector(project, id)
		if err != nil {
			return stats, err
		}
		if vec == nil {
			missIdx = append(missIdx, len(vecs))
			missText = append(missText, noteTexts[i])
		}
		ids = append(ids, id)
		vecs = append(vecs, vec)
		metas = append(metas, noteMetas[i])
	}

	if n := distinctSources(noteMetas, "/summary"); n > 0 {
		facts.tiers[tierNotes] = indexstore.TierRecord{Sources: n}
	}

	stats.Indexed = len(ids)
	stats.Embedded = len(missIdx)
	stats.CacheHits = stats.Indexed - stats.Embedded

	commit := func(batch map[string][]float32) { e.commitVectors(ctx, pl, timeout, batch, &chain) }
	if err := e.embedMisses(ctx, project, ids, vecs, missIdx, missText, commit); err != nil {
		return stats, err
	}

	if len(vecs) == 0 {
		// No drawers, no iteration chunks and no note chunks. Drop any index
		// from a previous build so it stops serving hits for content that no
		// longer exists, then reap every now-orphaned vector.
		e.mu.Lock()
		old := e.indexes[project]
		delete(e.indexes, project)
		e.mu.Unlock()
		e.closeReplaced(old)
		stats.Reaped = e.finalCommit(ctx, pl, timeout, &chain, facts)
		e.remember(project, &chain, start)
		return stats, nil
	}

	dims, err := e.embedder.Dimensions()
	if err != nil {
		return stats, fmt.Errorf("embedder dimensions: %w", err)
	}

	idx, err := newIndex(kindBrute, dims, provisionalHNSWParams)
	if err != nil {
		return stats, err
	}
	if err := idx.Build(vecs, ids); err != nil {
		return stats, fmt.Errorf("build index: %w", err)
	}

	e.mu.Lock()
	old := e.indexes[project]
	e.indexes[project] = idx
	for i, id := range ids {
		if e.detectCollision(id, metas[i]) {
			continue
		}
		e.metadata[id] = metas[i]
	}
	e.mu.Unlock()
	e.closeReplaced(old)

	stats.Reaped = e.finalCommit(ctx, pl, timeout, &chain, facts)
	e.remember(project, &chain, start)
	return stats, nil
}

// cachedVector is a build's embed-cache lookup. A vector file that is
// corrupt (a size that is no whole vector, written by an older binary's
// non-atomic write) is a miss: the vector is embedded or counted missing like
// any other. Any other error (an unreadable cache directory or file) fails the
// build, naming the cache: reading it as "no vector" would report every chunk
// as missing (the missing_vectors reason) for a fault that no repair pass or
// rebuild can mend.
func (e *Engine) cachedVector(project, id string) ([]float32, error) {
	vec, err := e.cache.Get(project, id)
	if errors.Is(err, errCorruptVector) {
		slog.Warn("embed cache: corrupt vector read as a miss", "project", project, "id", id, "err", err)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the embed cache of %s: %w", project, err)
	}
	return vec, nil
}

// distinctSources counts the distinct sources among metas: their SourceRefs
// with suffix (the raw or summary row's marker) trimmed, so one note or one
// iteration entry counts once.
func distinctSources(metas []drawerMeta, suffix string) int {
	seen := map[string]bool{}
	for _, m := range metas {
		seen[strings.TrimSuffix(m.SourceRef, suffix)] = true
	}
	return len(seen)
}

// storedChunkMeta is a host-local store chunk's search metadata, taken from
// the store's fold (its selected owner's source fields and day).
func storedChunkMeta(project string, c indexstore.StoredChunk) drawerMeta {
	date := c.FiledAt
	if len(date) >= 10 {
		date = date[:10]
	}
	return drawerMeta{
		Project:    project,
		Wing:       c.Wing,
		Room:       c.Room,
		Hall:       c.Hall,
		SourceType: c.SourceType,
		SourceRef:  c.SourceRef,
		Date:       date,
		Content:    c.Content,
		ChunkIndex: c.ChunkIndex,
	}
}

// commitVectors commits one embed batch in its own short Tx. A busy commit
// lock, a gone project or another embedding regime skips the write; the batch
// still serves this process from memory. Once one batch of a timed build has
// been skipped, the rest try the lock once each.
func (e *Engine) commitVectors(ctx context.Context, pl *projectLock, timeout time.Duration, batch map[string][]float32, chain *genChain) {
	if chain.skipped && timeout != indexstore.NoTimeout {
		// An earlier batch of this build already found the lock busy: try
		// once, instead of waiting the full timeout again for every batch.
		timeout = 0
	}
	tx, err := pl.Tx(ctx, timeout)
	if err != nil {
		if isLockTimeout(err) {
			chain.skipped = true
		} else if !errors.Is(err, indexstore.ErrProjectGone) {
			slog.Warn("embed cache commit skipped", "project", pl.project, "err", err)
		}
		return
	}
	e.putVectorsLocked(tx, batch)
	before, after, err := pl.finishTx()
	if err != nil {
		slog.Warn("embed cache commit failed", "project", pl.project, "err", err)
		chain.broken = true
		return
	}
	chain.step(before, after)
}

// finalCommit takes the build's last Tx, records what the build found in the
// completeness record (writeCompletenessLocked) and reaps orphans under it
// (reapLocked). It returns the number of vectors reaped. A busy commit lock
// skips both: the next search that finds the same condition records it, and
// the next build reaps.
func (e *Engine) finalCommit(ctx context.Context, pl *projectLock, timeout time.Duration, chain *genChain, facts buildFacts) int {
	tx, err := pl.Tx(ctx, timeout)
	if isLockTimeout(err) {
		chain.skipped = true
		slog.Info("completeness record and reap skipped: the index commit lock is busy", "project", pl.project)
		return 0
	}
	if errors.Is(err, indexstore.ErrProjectGone) {
		return 0
	}
	if err != nil {
		slog.Warn("reap skipped", "project", pl.project, "err", err)
		return 0
	}
	werr := e.writeCompletenessLocked(tx, pl.project, facts)
	if werr != nil {
		slog.Warn("completeness record not written", "project", pl.project, "err", werr)
		chain.skipped = true
	}
	n, rerr := e.reapLocked(tx, pl.project)
	before, after, cerr := pl.finishTx()
	if rerr != nil || cerr != nil {
		slog.Warn("reap orphan vectors failed", "project", pl.project, "err", errors.Join(rerr, cerr))
		chain.broken = true
		return n
	}
	chain.step(before, after)
	if n > 0 {
		slog.Info("reaped orphan vectors", "project", pl.project, "count", n)
	}
	return n
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

// reapLocked collects the project's orphan vectors (and orphaned store
// records) through indexstore's Tx.Reap, under the index commit lock the
// caller ALREADY HOLDS (tx), and evicts every reaped id from the in-memory
// index and metadata through evictLocked. It returns the number of vectors
// reaped. It never takes a project mutex or a commit lock itself.
//
// The live set is read under the lock (liveSet), never taken from the
// set this Rebuild started with: another process may have committed chunks
// since, and their vectors must survive. Tx.Reap adds every chunk id in the
// host-local store itself.
func (e *Engine) reapLocked(tx *indexstore.Tx, project string) (int, error) {
	reaped, err := tx.Reap(func() (map[string]bool, error) { return e.liveSet(project) })
	if err != nil {
		return 0, err
	}
	for _, id := range reaped {
		if err := e.evictLocked(tx, project, id); err != nil {
			slog.Warn("evict reaped vector failed", "project", project, "drawer", id, "err", err)
		}
	}
	return len(reaped), nil
}

// liveSet is the orphan reaper's live set (Scope 2, XC3b): the union of the
// ids every tier would contain on an explicit rebuild, re-read from disk under
// the commit lock: notes and iterations, and tracked drawers while the vault
// carries no migration marker. Tx.Reap adds every chunk id in the host-local
// store itself, ledgered or not, which covers decision chunks and the local
// tier, and keeps the vectors of a source whose ledger entry is not written
// yet. It is never the subset a lazy build happened to embed. It embeds
// nothing.
func (e *Engine) liveSet(project string) (map[string]bool, error) {
	live := map[string]bool{}
	migrated, err := storage.VaultMigrated(e.vault.Root)
	if err != nil {
		return nil, err
	}
	var wings []string
	if !migrated {
		if wings, err = e.vault.ListWings(project); err != nil {
			return nil, fmt.Errorf("list wings: %w", err)
		}
	}
	for _, wing := range wings {
		rooms, err := e.vault.ListRooms(project, wing)
		if err != nil {
			return nil, fmt.Errorf("list rooms for wing %s: %w", wing, err)
		}
		for _, room := range rooms {
			drawers, err := e.vault.ListDrawers(project, wing, room)
			if err != nil {
				// Rebuild skips an unreadable room; the reaper must not
				// treat its drawers as gone.
				return nil, fmt.Errorf("list drawers %s/%s: %w", wing, room, err)
			}
			for _, d := range drawers {
				live[d.ID] = true
			}
		}
	}
	iterIDs, _, _, err := collectIterationCorpus(e.vault, project)
	if err != nil {
		return nil, fmt.Errorf("iteration corpus: %w", err)
	}
	noteIDs, _, _, err := collectNoteCorpus(e.vault, project)
	if err != nil {
		return nil, fmt.Errorf("note corpus: %w", err)
	}
	for _, id := range append(iterIDs, noteIDs...) {
		live[id] = true
	}
	return live, nil
}

// embedMisses embeds the cache-miss drawers in batches, filling their slots in
// vecs. Each batch is handed to commit as it lands, so a rebuild killed
// partway through still leaves durable progress behind.
func (e *Engine) embedMisses(ctx context.Context, project string, ids []string, vecs [][]float32, missIdx []int, missText []string, commit func(map[string][]float32)) error {
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

		landed := make(map[string][]float32, len(got))
		for j, vec := range got {
			pos := missIdx[start+j]
			vecs[pos] = vec
			landed[ids[pos]] = vec
		}
		commit(landed)
	}
	return nil
}

// Embedder returns the engine's embedder for external batch use.
func (e *Engine) Embedder() embedder.Embedder {
	return e.embedder
}

// closeReplaced closes an index that is no longer published. The caller must
// not hold e.mu: Close waits for the index's background rebuild, and holding
// the engine lock there would stall every search meanwhile.
func (e *Engine) closeReplaced(old VectorIndex) {
	if old == nil {
		return
	}
	if e.beforeIndexClose != nil {
		e.beforeIndexClose()
	}
	if err := old.Close(); err != nil {
		slog.Warn("close replaced vector index failed", "err", err)
	}
}

// candidateCount is how many vector candidates a search fetches for limit
// results: three times as many, because metadata filters drop some. It
// saturates instead of overflowing on an absurd limit.
func candidateCount(limit int) int {
	if limit > math.MaxInt/3 {
		return math.MaxInt
	}
	return limit * 3
}

// Close releases resources: every index (cancelling any background rebuild),
// then the embedder. The remembered counters are cleared with the index map,
// so no project reads as built once its index is gone.
func (e *Engine) Close() error {
	e.mu.Lock()
	indexes := e.indexes
	e.indexes = make(map[string]VectorIndex)
	e.mu.Unlock()
	e.buildMu.Lock()
	clear(e.loaded)
	e.buildMu.Unlock()
	for _, idx := range indexes {
		e.closeReplaced(idx)
	}
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
