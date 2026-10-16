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

	// pendingInsert reserves a drawer id a capture is mid-publishing (Scope 3):
	// between the collision check (under mu) and the metadata publish (under mu),
	// the vector is inserted with mu released, so a concurrent capture of the
	// same global drawer id (DrawerID excludes project, so two projects can share
	// one) sees the id taken and does not also insert. Search never reads it.
	// Guarded by mu.
	pendingInsert map[string]struct{}

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
	// once per engine, from lockProject, before the engine takes any project
	// mutex or commit lock.
	indexSweep sync.Once

	// Proactive embedder warm-up (B1.1). A search needs the embedding model to
	// embed its query, and constructing a LazyEmbedder's ONNX model runs inside
	// a sync.Once that cannot be interrupted by a ctx. So the construction is
	// kicked ONCE, off the caller's goroutine, and the search path waits a
	// bounded time for it (waitEmbedderReady) instead of ever calling Embed on a
	// cold model. warmDone is closed when the construct returns (success OR
	// failure); warmFailed records a failure so the wait can fast-fail. warmWG
	// lets Close wait for an in-flight construct before closing the embedder, so
	// no construct publishes a live model session into a torn-down process (S-D1).
	warmOnce   sync.Once
	warmDone   chan struct{}
	warmFailed atomic.Bool
	warmWG     sync.WaitGroup

	// Background brute→HNSW converter (task
	// hnsw-kind-swap-dual-write-conversion). convertEnabled is set ONLY by the
	// WithBackgroundConverter option, which only bootstrap() passes, so the
	// converter runs only in the long-lived MCP serving engine and never in a
	// per-invocation CLI engine or the detached ingester (S3). convertCtx is
	// cancelled by Close before it unpublishes the indexes, and convertWG lets
	// Close wait for every in-flight conversion to return. The converter's
	// lockProject waits are bounded by searchLockTimeout and honour convertCtx; an
	// in-progress h.Build (the minutes-long g.Add, which takes no ctx) is awaited
	// to completion — bounded by one build that runs off all engine locks with no
	// swap pending (making Build cancellable is a separate wrapper follow-on).
	// converting (guarded by convertMu) is the in-flight set,
	// and convertBackoff (also convertMu) the per-project write-counted backoff
	// after an abandoned conversion. runConvert holds the (host,vault) index run
	// lock across the whole convert (ADR-016 decision 3), released when the
	// goroutine returns, so convertWG.Wait() in Close also waits out that hold.
	convertEnabled bool
	convertCtx     context.Context
	convertCancel  context.CancelFunc
	convertWG      sync.WaitGroup
	convertMu      sync.Mutex
	converting     map[string]struct{}
	convertBackoff map[string]convBackoff

	// rebuildLauncher, when set, ensures a detached `vp index rebuild <project>`
	// is running after a cold/stale search degraded (ADR-016 decision 4). It is
	// the spawn seam that lives OUTSIDE this package: the cold-search launcher path
	// never try-locks the run lock to decide whether to build (the spawned child's
	// own try-lock is the authority), and never spawns a process itself. (The one
	// engine path that DOES take the run lock is runConvert, decision 3.)
	// Only the long-lived MCP serving engine injects one (WithRebuildLauncher from
	// bootstrap); a CLI/one-shot engine leaves it nil and only serves degraded.
	rebuildLauncher RebuildLauncher

	// indexRunCooldown is the inter-run cooldown window (ADR-016 decision 6)
	// consulted by runConvert (decision 3) and the search-path corpus embed
	// (the cold-corpus gate). NewEngine sets it to indexstore.DefaultRunCooldown;
	// a value <= 0 disables the throttle (in-package tests set the field). The
	// check is a lock-free marker read, never a try-lock.
	indexRunCooldown time.Duration
}

// RebuildLauncher records a durable corpus-rebuild want and ensures a detached
// `vp index rebuild <project>` is running (ADR-016 decisions 4 and 5). The
// engine calls it when a cold/stale search could not build the corpus inline and
// served degraded. It MUST NOT try-lock the index run lock: the spawned child's
// own try-lock is the authority. Implemented in the tools layer, above the
// engine, so no run-lock or spawn logic enters internal/search.
type RebuildLauncher func(project string)

// convBackoff records, per project, the store generation at the last abandoned
// conversion and how many abandons have run in a row, so a hot project is not
// reconverted on a tight loop (mirrors the wrapper's rebuildBackoffGens).
type convBackoff struct {
	abandonedAt indexstore.Gen
	abandons    int
}

// EngineOption configures a new Engine.
type EngineOption func(*Engine)

// WithBackgroundConverter enables the brute→HNSW background converter on this
// engine. Only bootstrap() (the long-lived MCP serving engine) passes it; every
// other search.NewEngine caller leaves it off, so no CLI invocation or the
// detached ingester process ever runs the in-memory kind swap (S3).
func WithBackgroundConverter() EngineOption {
	return func(e *Engine) { e.convertEnabled = true }
}

// WithRebuildLauncher injects the detached-rebuild spawn seam (ADR-016 decision
// 4). Only bootstrap() (the long-lived MCP serving engine) passes it; a CLI or
// one-shot engine leaves it off and a cold search there only serves degraded.
func WithRebuildLauncher(l RebuildLauncher) EngineOption {
	return func(e *Engine) { e.rebuildLauncher = l }
}

// projectBuild is a single in-flight lazy index build. Concurrent searches for
// the same project join the same build and share its outcome.
type projectBuild struct {
	done chan struct{}
	err  error
}

// NewEngine creates a search engine. opts are applied after construction;
// WithBackgroundConverter is the only one, and only bootstrap() passes it.
func NewEngine(emb embedder.Embedder, vault *storage.Vault, cfg storage.Config, opts ...EngineOption) *Engine {
	cache := NewEmbedCache(vault)
	if emb != nil {
		// Only an engine that can embed validates the cache's regime: one with
		// no embedder (vp check's) must never invalidate vectors it cannot
		// replace.
		cache.fingerprint = embedder.FingerprintBackend(embedder.ResolveBackend(cfg.EmbedderBackend), cfg.EmbedderModel, cfg.EmbedderMaxSeqLen)
	}
	convertCtx, convertCancel := context.WithCancel(context.Background())
	e := &Engine{
		embedder:       emb,
		vault:          vault,
		cache:          cache,
		config:         cfg,
		indexes:        make(map[string]VectorIndex),
		metadata:       make(map[string]drawerMeta),
		pendingInsert:  make(map[string]struct{}),
		loaded:         make(map[string]indexstore.Gen),
		buildsRun:      make(map[string]*projectBuild),
		invalidated:    make(map[string]uint64),
		sems:           make(map[string]chan struct{}),
		convertCtx:     convertCtx,
		convertCancel:  convertCancel,
		converting:     make(map[string]struct{}),
		convertBackoff: make(map[string]convBackoff),
		warmDone:       make(chan struct{}),
		// ADR-016 decision 6: the inter-run cooldown defaults to the measured
		// window. There is no engine option for it; in-package tests set the field
		// directly, so it is set here before opts are applied.
		indexRunCooldown: indexstore.DefaultRunCooldown,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// ErrIndexNotReady is the first-build answer: a search reached a project that
// has no in-memory index yet, and could not build one because another writer
// in this process holds the project (it is waiting on an index commit another
// process holds). search-first-build-and-empty-corpus-answer maps it to the
// user-facing answer.
var ErrIndexNotReady = errors.New("search index not built yet: the project is busy")

// errEmbedRefused is the internal sentinel a search-path build returns when it
// WOULD embed more than missBudget cache misses and nothing servable is in
// memory: the build refuses before touching the embedder, so the caller never
// blocks on a whole-corpus embed (the hang this fix closes). ensureIndex maps it
// to serve-as-is (HasIndex) or *IndexNotBuiltError. It is never surfaced raw.
var errEmbedRefused = errors.New("search: build would embed a cold corpus on the search path")

// Search-path bounds (all vars so a test can shorten them). None of them is the
// guarantee on its own; together they ensure no cold/uninterruptible path blocks
// the caller unboundedly.
var (
	// readyWait is how long the search path waits for the embedding model to
	// finish constructing before it refuses, rather than ever calling Embed on a
	// cold model (which blocks uninterruptibly inside the LazyEmbedder once).
	readyWait = 15 * time.Second
	// joinBuildTimeout bounds how long a search waits to join another search's
	// in-flight build of the same project before it serves-as-is or refuses.
	joinBuildTimeout = 5 * time.Second
	// embedBudget is a wall-clock backstop around embedMisses on the search path
	// (a build already admitted past the miss-count gate), a child of the
	// caller's ctx. It never wraps lockProject nor the commit closure.
	embedBudget = 10 * time.Second
)

// joinBuildHook, when a test sets it, runs when a search joins another
// search's in-flight build of the same project.
var joinBuildHook func(project string)

// witnessInterleaveHook, when a test sets it, runs inside tryWitnessedLoad after
// the corpus walk and BEFORE the generation re-read, so a test can interleave a
// writer (bump the counter / rewrite the graph) and prove the witness refuses a
// load that straddled a concurrent commit. Production leaves it nil.
var witnessInterleaveHook func()

// WarmEmbedder kicks a one-time, detached construction of the embedding model so
// it is ready (or has failed) by the time the first search needs it, moving the
// tens-of-seconds ONNX load (and ~90MB first-ever download) off the caller's
// goroutine. It returns immediately. The construct runs under warmWG so Close
// can wait for it (S-D1). Safe to call repeatedly and from any surface; a
// non-lazy or already-ready embedder just closes the signal.
func (e *Engine) WarmEmbedder() {
	if e == nil || e.embedder == nil {
		return
	}
	e.warmOnce.Do(func() {
		if e.EmbedderReady() {
			close(e.warmDone)
			return
		}
		e.warmWG.Add(1)
		go func() {
			defer e.warmWG.Done()
			// Dimensions forces the lazy construct; its result is memoized, so a
			// later on-demand kick or a real Embed is a no-op construct.
			if _, err := e.embedder.Dimensions(); err != nil || !e.EmbedderReady() {
				e.warmFailed.Store(true)
			}
			close(e.warmDone)
		}()
	})
}

// waitEmbedderReady returns nil once the embedding model is ready, or
// *IndexNotBuiltError if it is not ready within budget, has already failed to
// construct, or the caller ctx is cancelled first. It NEVER calls
// Embed/EmbedBatch/Dimensions on the caller's goroutine — the construct runs
// only under WarmEmbedder's detached goroutine — so a cold model can never block
// the caller here (B1.1). A ready embedder (warm, or a non-lazy test embedder)
// returns nil on entry with no wait.
func (e *Engine) waitEmbedderReady(ctx context.Context, project string, budget time.Duration) error {
	if e.EmbedderReady() {
		return nil
	}
	e.WarmEmbedder() // idempotent kick: covers a surface that did not warm at startup
	select {
	case <-e.warmDone:
		if e.EmbedderReady() {
			return nil
		}
		return &IndexNotBuiltError{Project: project} // construct finished but failed: fast-fail
	case <-time.After(budget):
		return &IndexNotBuiltError{Project: project}
	case <-ctx.Done():
		return &IndexNotBuiltError{Project: project}
	}
}

// mapBuildErr turns a build's raw error into the search path's answer. A "soft"
// condition — a busy lock, a refused cold-corpus embed, or a build-budget
// deadline — serves the project's in-memory index as-is when one is actually
// servable (servableInMemory: a non-empty index in memory, S1), so a healthy
// index still answers while the project is momentarily unbuildable. The
// predicate deliberately does NOT require e.loaded (as HasIndex does): a
// timed-out capture insert clears e.loaded to mark the project out of date, yet
// its already-built in-memory index must still answer (ADR-014 decision 7); only
// an empty / cold-insert index (Len()==0) refuses. With nothing servable, a busy
// lock stays ErrIndexNotReady (retry), while a refused embed or a deadline
// becomes *IndexNotBuiltError (run `vp index rebuild`). Every other error passes
// through unchanged.
func (e *Engine) mapBuildErr(project string, err error) error {
	if err == nil {
		return nil
	}
	soft := isLockTimeout(err) || errors.Is(err, errEmbedRefused) || errors.Is(err, context.DeadlineExceeded)
	if !soft {
		return err
	}
	if e.servableInMemory(project) {
		slog.Info("search serves the loaded index as-is: the project cannot rebuild right now", "project", project)
		return nil
	}
	if isLockTimeout(err) {
		return fmt.Errorf("%w: %s", ErrIndexNotReady, project)
	}
	return &IndexNotBuiltError{Project: project}
}

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
	// A search that touches a brute-served project past the HNSW threshold is the
	// converter's trigger (task hnsw-kind-swap-dual-write-conversion, Step 1). The
	// probe is cheap and gated: it no-ops unless the converter is enabled (only in
	// the MCP serving engine) and the project is a brute index over threshold with
	// no conversion already running and the backoff elapsed. Deferred so it runs
	// once the index is in a known state, on every return path.
	if e.convertEnabled {
		defer e.maybeStartConvert(project)
	}
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
		// Bound the join (B1.2): a joiner must never wait unboundedly on another
		// search's build — the owner may be a long build, and the caller ctx
		// (MCP) has no deadline. On the bound or on ctx cancellation, serve the
		// index as-is when one is servable (S1), else refuse. b.err was already
		// mapped by the owner, so a received value is returned directly.
		select {
		case <-b.done:
			return b.err
		case <-time.After(joinBuildTimeout):
			if e.servableInMemory(project) {
				return nil
			}
			return &IndexNotBuiltError{Project: project}
		case <-ctx.Done():
			if e.servableInMemory(project) {
				return nil
			}
			return &IndexNotBuiltError{Project: project}
		}
	}
	b := &projectBuild{done: make(chan struct{})}
	e.buildsRun[project] = b
	e.buildMu.Unlock()

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

	// ADR-016 decision 4: before building, try to CONSUME a fuller/newer index
	// another process (the detached `vp index rebuild`, a heal, a catch-up)
	// committed to disk, by a generation-witnessed disk LOAD — no corpus embed,
	// no commit lock, no run lock. Only the build owner does this (serialized by
	// buildsRun), so no two loads race and it never races a full rebuild. When it
	// installs, the project is current and no build runs.
	if gerr == nil && e.tryWitnessedLoad(ctx, project, g) {
		finished = true
		b.err = nil
		e.endBuild(project, b)
		return nil
	}

	_, rerr := e.rebuildAndRemember(ctx, project, searchLockTimeout, modeSearch, true)
	finished = true
	// ADR-016 decision 4/5: the build refused to embed a cold corpus inline (or
	// ran out its embed budget), so it served degraded. Record the durable
	// corpus-rebuild want and ensure the detached singleton `vp index rebuild` is
	// running — never a try-lock from here; the spawn seam is the injected
	// launcher. A busy lock is transient contention, not a cold corpus, so it does
	// not coalesce.
	if errors.Is(rerr, errEmbedRefused) || errors.Is(rerr, context.DeadlineExceeded) {
		e.ensureDetachedRebuild(project)
	}
	// Map the raw build outcome once, here, so every joined search receives the
	// same already-mapped answer from b.err (a busy lock, a refused cold embed,
	// or a build-budget deadline → serve-as-is or the typed not-ready error).
	b.err = e.mapBuildErr(project, rerr)
	e.endBuild(project, b)
	return b.err
}

// ensureDetachedRebuild invokes the injected rebuild launcher (ADR-016 decision
// 4) to record the durable rebuild-corpus want and ensure a detached
// `vp index rebuild` is running. It is a no-op on an engine with no launcher (a
// CLI/one-shot engine), which then only serves degraded. The engine never
// try-locks the run lock here — the spawned child's own try-lock is the
// authority. (The one engine path that DOES take the run lock is runConvert,
// ADR-016 decision 3.)
func (e *Engine) ensureDetachedRebuild(project string) {
	if e.rebuildLauncher == nil {
		return
	}
	e.rebuildLauncher(project)
}

// indexRunCooldownActive reports whether an embedding index run completed for
// this vault within the cooldown window (ADR-016 decision 6). It is a lock-free
// marker read — never a try-lock, so it stays legal inside the engine build
// body (decision 1 / TestLockDiscipline) — and fail-open: a missing or torn
// marker is not active, so a bad marker never suppresses a build.
func (e *Engine) indexRunCooldownActive() bool {
	return indexstore.RunCooldownActive(e.vault, time.Now(), e.indexRunCooldown)
}

// tryWitnessedLoad consumes a graph another process (the detached
// `vp index rebuild`, a heal, a catch-up) committed to disk, WITHOUT an inline
// embed and taking neither the index commit lock nor the run lock (ADR-016
// decision 4). g1 is the counter the caller read BEFORE this call (the reader
// rule). The steps:
//
//  1. load hnsw.idx off-lock (newHNSWIndexFromDisk → indexstore.ReadGraph, which
//     is atomic-rename-safe, CRC-checked and lock-free); a no-graph / mismatch /
//     corrupt file installs nothing, so a never-built project stays notes-only;
//  2. walk the four corpus sources with NO embed (walkCorpus, the exact walk
//     rebuild uses) to produce the metadata rows search needs — this populates
//     e.metadata for the graph's ids so the installed graph serves REAL hits, not
//     empty rows, on a long-lived engine whose embed cache is cold;
//  3. the WITNESS: re-read the counter and require g1 == g2 (so the corpus walk
//     was a consistent store snapshot, no commit landed during it), AND require
//     the graph's live-id set to be a SUBSET of the walked corpus's id set. The
//     subset check is the pairing test ADR-016's hazard names directly ("a graph
//     newer or older than the chunk store it is paired with"): a graph id the
//     corpus lacks is removed or edited content (ids are content-addressed), so
//     the graph is stale → refuse; a corpus id the graph lacks is content the
//     writer legitimately omitted (a heal's cache miss, an unusable vector, a
//     not-yet-built addition), so the graph is still correctly paired → consume
//     degraded. It is stronger than a Gen stamp, which is blind to the vault-tree
//     edits notes and iterations are, and it cannot straddle the two-file (graph +
//     counter) rename window because it compares the loaded graph against the
//     freshly-walked corpus, not against a proxy.
//
// Install happens only when all three hold; otherwise nothing is installed and
// the build path serves this search and retries next time. The install swaps
// e.indexes and populates e.metadata under e.mu briefly (never held across the
// load or the walk) and records the counter under buildMu, honouring the
// capture-invalidation guard remember() uses.
func (e *Engine) tryWitnessedLoad(ctx context.Context, project string, g1 indexstore.Gen) bool {
	if e.embedder == nil {
		return false
	}
	dims, err := e.embedder.Dimensions()
	if err != nil {
		return false // cannot size the graph; let the build path handle it
	}
	e.buildMu.Lock()
	inv0 := e.invalidated[project]
	e.buildMu.Unlock()

	idx, res, lerr := newHNSWIndexFromDisk(e.vault, project, dims, provisionalHNSWParams)
	if lerr != nil || res != loaded || idx == nil {
		if idx != nil {
			_ = idx.Close()
		}
		return false
	}

	// Walk the corpus (no embed) for the metadata rows, using modeExplicit so a
	// cache miss is still emitted (its vector comes from the loaded graph, not
	// the cache). This is the ONLY way to populate e.metadata on a cold engine,
	// and walking also lets the set witness below see notes/iterations edits.
	w, werr := e.walkCorpus(ctx, project, modeExplicit)
	if werr != nil {
		_ = idx.Close()
		return false
	}

	if witnessInterleaveHook != nil {
		witnessInterleaveHook() // a test writes here, between the pre-load read and the re-read
	}

	g2, gerr := indexstore.ReadGeneration(e.vault, project)
	if gerr != nil || g1 != g2 {
		_ = idx.Close()
		return false // a commit landed during the walk: the snapshot is not consistent
	}
	// The pairing witness (N1): every id the GRAPH serves must still be present in
	// the walked corpus (graph live-ids ⊆ corpus). A graph id the corpus no longer
	// has is removed or edited content — ids are content-addressed, so an in-place
	// edit drops the old id from the corpus — i.e. the graph is older than the
	// store it is paired with: REFUSE. A corpus id the graph LACKS is the opposite
	// and is NOT stale: it is content the writer was allowed to omit — a heal's
	// cache miss, an unusable vector dedupBatch dropped, a not-yet-built addition —
	// so the graph is still correctly paired, just not yet complete. Consuming it
	// serves the cheaply-available answer now (decision 4): a missing result, never
	// a wrong one; the durable marker and the detached rebuild catch the rest up.
	// Subset, not equality: equality false-rejects every such correctly-subset
	// graph and would never consume it (round-3 N1).
	graphIDs := idx.liveIDs()
	metaByID := make(map[string]drawerMeta, len(w.ids))
	for i, id := range w.ids {
		metaByID[id] = w.metas[i] // last occurrence wins, matching dedupBatch's keep-last
	}
	for _, id := range graphIDs {
		if _, ok := metaByID[id]; !ok {
			_ = idx.Close()
			return false // the graph serves an id the corpus no longer has: genuinely stale
		}
	}

	e.mu.Lock()
	old := e.indexes[project]
	e.indexes[project] = idx
	// N2: write metadata for exactly the ids the installed graph serves — not for
	// every walked id (an id the graph lacks would be inert), and never skipping a
	// served one — so e.metadata matches what the graph returns at query time.
	for _, id := range graphIDs {
		e.metadata[id] = metaByID[id]
	}
	e.mu.Unlock()
	e.closeReplaced(old)

	// Remember the counter this index now matches, unless a capture insert raced
	// the load (the same guard remember() uses): then leave the project out of
	// date so the next search rebuilds and picks the insert up.
	e.buildMu.Lock()
	if e.invalidated[project] == inv0 {
		e.loaded[project] = g1
	} else {
		delete(e.loaded, project)
	}
	e.buildMu.Unlock()
	return true
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
func (e *Engine) ensureAllIndexes(ctx context.Context) (map[string]bool, bool, error) {
	projects, err := e.vault.ListAllProjects()
	if err != nil {
		return nil, false, fmt.Errorf("list projects: %w", err)
	}
	listed := make(map[string]bool, len(projects))
	skippedNotReady := false
	for _, p := range projects {
		// A truly empty project (a bare Projects/<slug>/ scaffold, a project
		// with only tracked archives, etc.) is skipped, never built: the skip
		// is reported only through coverage (ADR-014 decision 8) and the result
		// array shape is unchanged. Checked before ensureIndex so a skipped
		// project constructs no embedder and pays no model load.
		empty, err := e.TrulyEmpty(p.Slug)
		if err != nil {
			return nil, false, fmt.Errorf("check project emptiness for %s: %w", p.Slug, err)
		}
		if empty {
			continue
		}
		if err := e.ensureIndex(ctx, p.Slug); err != nil {
			// One project's cold/unbuilt index must not fail the whole
			// cross-project search and drop every warm project (B1 §6): skip it
			// and answer from the rest. Only a genuine error fails hard.
			var nb *IndexNotBuiltError
			if errors.As(err, &nb) {
				skippedNotReady = true
				continue
			}
			return nil, false, fmt.Errorf("build index for %s: %w", p.Slug, err)
		}
		listed[p.Slug] = true
	}
	return listed, skippedNotReady, nil
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

// servableInMemory reports whether project has a non-empty index in memory right
// now, regardless of whether it is still marked current (e.loaded). It is the
// serve-as-is predicate for a soft build failure (busy lock, refused cold embed,
// or a build-budget deadline): a project whose in-memory index was marked out of
// date by a timed-out capture insert still answers from that index (ADR-014
// decision 7), while an empty or cold-insert index (Len()==0) does NOT — that
// refuses with *IndexNotBuiltError rather than returning an empty success (S1).
// Unlike HasIndex it does not require e.loaded, because the whole point of
// serving as-is is a project that cannot rebuild right now.
func (e *Engine) servableInMemory(project string) bool {
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

	// A truly empty project — nothing any tier could ever index — is an error
	// that names `vp index rebuild`, not zero hits (ADR-014 decision 8). This
	// is checked before ensureIndex/Rebuild, so a truly empty search never
	// constructs the embedder or pays a model load (search-N4). TrulyEmpty is
	// child 2's predicate; the search path only consumes it.
	if f.Project != "" {
		empty, err := e.TrulyEmpty(f.Project)
		if err != nil {
			return nil, fmt.Errorf("check project emptiness: %w", err)
		}
		if empty {
			return nil, &NothingIndexableError{Project: f.Project}
		}
	}

	// Every semantic search embeds its query, so the embedding model must be
	// ready before either the build (which may embed a notes miss) or the query
	// embed runs. Wait a bounded time for the proactively-warmed model instead of
	// ever calling Embed on a cold one (which blocks uninterruptibly); on expiry
	// or a failed construct, refuse with the typed not-ready error (B1.1). A warm
	// model returns immediately. Bootstrap's SearchReady does NOT reach here — it
	// short-circuits on !EmbedderReady() and never waits.
	if err := e.waitEmbedderReady(ctx, f.Project, readyWait); err != nil {
		return nil, err
	}

	// Build the index(es) this search reads, on first use. Must happen before
	// e.mu is taken — Rebuild acquires it for write.
	if f.Project != "" {
		if err := e.ensureIndex(ctx, f.Project); err != nil {
			// The typed not-ready error names `vp index rebuild`; surface it
			// unwrapped (like NothingIndexableError) so the handler/CLI get the
			// clean remediation text, not a `build index for …` prefix (S2).
			var nb *IndexNotBuiltError
			if errors.As(err, &nb) {
				return nil, nb
			}
			return nil, fmt.Errorf("build index for %s: %w", f.Project, err)
		}
		return e.searchReady(ctx, query, f, nil)
	}
	listed, skippedNotReady, err := e.ensureAllIndexes(ctx)
	if err != nil {
		return nil, err
	}
	// Cross-project search skips truly empty projects (and not-ready ones) and
	// still answers; it errors only when every project in the vault dropped out.
	// ensureAllIndexes has already removed both from listed, so an empty set with
	// no not-ready skip means nothing in the vault is indexable; an empty set
	// where a project was skipped not-ready names `vp index rebuild` instead (S2).
	if len(listed) == 0 {
		if skippedNotReady {
			return nil, &IndexNotBuiltError{}
		}
		return nil, &NothingIndexableError{}
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

// CacheWriter returns the embed cache's writer for the project whose index
// commit lock tx holds: a thin exposure of EmbedCache.Writer, so an importer
// that commits vectors through the store (Tx.CommitBatch) writes under this
// engine's embedding regime. It is the same writer, with the same rules: the
// regime is re-read under the lock (ErrEmbedRegimeMismatch on another regime
// or unattributed vectors), a finished Tx is refused, and each vector is
// written atomically. It takes no lock itself.
func (e *Engine) CacheWriter(tx *indexstore.Tx) (*CacheWriter, error) {
	return e.cache.Writer(tx)
}

// errNoRegime: CachedVector on an engine that has no embedder, whose cache
// therefore checks no regime.
var errNoRegime = errors.New("search: CachedVector needs an engine with an embedder: its cache checks no embedding regime")

// CachedVector returns project's cached vector for chunk id under this
// engine's embedding regime, and whether it was a hit. The pending-archive
// ingester embeds only the misses (plan revision D6). On a project whose cache
// holds another regime's vectors, or vectors with no sidecar, every lookup is
// a miss, so another regime's vector is never reused. It loads no model (the
// regime is a function of the config) and takes no lock. An engine with no
// embedder checks no regime, so it refuses.
func (e *Engine) CachedVector(project, id string) ([]float32, bool, error) {
	if e.cache.fingerprint == "" {
		return nil, false, errNoRegime
	}
	vec, err := e.cache.Get(project, id)
	if err != nil {
		return nil, false, err
	}
	return vec, vec != nil, nil
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

	// Scope 3: publish off e.mu in three phases — check-and-reserve under e.mu,
	// insert the vectors with NO engine lock held (g.Add runs under the index's
	// own lock), then publish metadata under e.mu. Collision stays BEFORE the
	// insert (no insert-then-Delete retract: on an hnswIndex Delete only
	// tombstones). The capture path never reads a per-project kind: when the
	// index exists its kind is whatever the last rebuild installed; when it does
	// not, kindBrute is constructed exactly as before (Unit B does not widen
	// this hole, nor consult a kind map a rebuild wrote).
	type reservation struct {
		id   string
		vec  []float32
		meta drawerMeta
	}

	e.mu.Lock()
	idx, ok := e.indexes[project]
	if !ok {
		dims, derr := e.embedder.Dimensions()
		if derr != nil {
			e.mu.Unlock()
			return fmt.Errorf("embedder dimensions: %w", derr)
		}
		idx, err = newIndex(kindBrute, dims, provisionalHNSWParams)
		if err != nil {
			e.mu.Unlock()
			return err
		}
		e.indexes[project] = idx
	}
	var toInsert []reservation
	for _, in := range ins {
		id := in.Drawer.ID
		meta := makeDrawerMeta(in.Project, in.Wing, in.Room, in.Drawer)
		if e.detectCollision(id, meta) {
			continue // never insert a colliding drawer
		}
		if _, published := e.metadata[id]; published {
			continue // already indexed (an identical re-capture is a no-op anyway)
		}
		if _, reserved := e.pendingInsert[id]; reserved {
			continue // a duplicate within this batch, or another publisher is mid-insert
		}
		e.pendingInsert[id] = struct{}{}
		toInsert = append(toInsert, reservation{id: id, vec: in.Vec, meta: meta})
	}
	e.mu.Unlock()

	// Phase 2: insert with NO e.mu held. An unusable vector makes Insert call
	// Delete (tombstones); the capture path tolerates that.
	insErr := func() error {
		for _, r := range toInsert {
			if err := idx.Insert(r.id, r.vec); err != nil {
				return err
			}
		}
		return nil
	}()

	// Phase 3: publish metadata under e.mu, dropping each reservation.
	e.mu.Lock()
	for _, r := range toInsert {
		delete(e.pendingInsert, r.id)
		if insErr != nil {
			continue // released the reservation; the failed insert is surfaced below
		}
		if e.detectCollision(r.id, r.meta) {
			// The global pendingInsert reservation (phase 1) makes this
			// unreachable in the normal path: a same-project publisher holds the
			// project mutex across all three phases, and a cross-project publisher
			// of the same global drawer id is skipped at phase 1 (reserved), or
			// skipped by e.metadata after publish. If it somehow fired anyway, we
			// simply do NOT publish metadata (S-CR2) — search drops a
			// metadata-less vector. We never idx.Delete here: on an hnswIndex that
			// tombstones THIS project's just-inserted or prior-live vector, which
			// is harmful, not a retract.
			continue
		}
		e.metadata[r.id] = r.meta
	}
	e.mu.Unlock()
	return insErr
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
	return e.rebuildAndRemember(ctx, project, indexstore.NoTimeout, modeSearch, false)
}

// rebuildMode selects how a rebuild treats cache misses, tier by tier (the
// explicit/lazy option of explicit-resumable-index-rebuild-with-disk-watchdog,
// Scope 5). The search and ingest paths use the default (modeSearch); only the
// `vp index rebuild` driver asks for the other two.
type rebuildMode struct {
	embedLocal  bool // embed local-tier (ledgered-source) misses too
	embedOthers bool // embed decision/drawer/iteration/note misses
	// converts is true only for the explicit `vp index rebuild` drivers. It
	// gates the one HNSW conversion Unit B performs: a project with no valid
	// graph and n>=T becomes HNSW only here, never on the lazy search path
	// (Scope 1 forbids a search-path conversion).
	converts bool
}

var (
	// modeSearch is the lazy search/refresh default: the notes, decision,
	// iteration and drawer tiers embed their misses; a local-tier miss is left
	// to the ingester's repair pass or an explicit rebuild, and sets the
	// missing-vector stale reason.
	modeSearch = rebuildMode{embedLocal: false, embedOthers: true, converts: false}
	// modeExplicit (`vp index rebuild`) also embeds local-tier misses, so a
	// completed rebuild repairs every missing vector.
	modeExplicit = rebuildMode{embedLocal: true, embedOthers: true, converts: true}
	// modeNoEmbed (`vp index rebuild --no-embed`) embeds nothing: the index is
	// built from cache hits only, and every miss is left out — a local-tier
	// miss still sets the missing-vector stale reason.
	modeNoEmbed = rebuildMode{embedLocal: false, embedOthers: false, converts: true}
)

// RebuildExplicit rebuilds every tier like Rebuild, and additionally embeds
// every local-tier cache miss, so a completed `vp index rebuild` leaves no
// missing vector. The rebuild driver calls it.
func (e *Engine) RebuildExplicit(ctx context.Context, project string) (RebuildStats, error) {
	return e.rebuildAndRemember(ctx, project, indexstore.NoTimeout, modeExplicit, false)
}

// RebuildNoEmbed rebuilds from embed-cache hits only, embedding nothing. A
// local-tier miss is counted and sets the missing-vector stale reason, so a
// `--no-embed` run can never read current with no vectors. The rebuild driver
// calls it for `--no-embed`.
func (e *Engine) RebuildNoEmbed(ctx context.Context, project string) (RebuildStats, error) {
	return e.rebuildAndRemember(ctx, project, indexstore.NoTimeout, modeNoEmbed, false)
}

// rebuildAndRemember rebuilds project and remembers the store counter its
// index now matches. The counter is read BEFORE the build (the reader rule,
// indexstore.ReadGeneration): a write that lands during the build moves the
// counter past the remembered value, and the next search rebuilds again. The
// cache forgets its remembered regime, so a regime another process replaced
// is read afresh.
func (e *Engine) rebuildAndRemember(ctx context.Context, project string, timeout time.Duration, mode rebuildMode, refuse bool) (RebuildStats, error) {
	e.buildMu.Lock()
	inv := e.invalidated[project]
	e.buildMu.Unlock()
	g, gerr := indexstore.ReadGeneration(e.vault, project)
	e.cache.Forget(project)
	return e.rebuild(ctx, project, timeout, mode, refuse, buildStart{gen: g, readable: gerr == nil, invalidated: inv})
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

// corpusWalk is the result of reading a project's four corpus sources (the
// host-local store, tracked drawers, iterations, session notes) into the
// parallel id/vec/meta slices a build or a witnessed load installs. It is the
// LOCK-FREE half of a rebuild — it takes no commit lock and no e.mu — so the
// witnessed consume (tryWitnessedLoad) can reuse the exact same walk rather than
// duplicating it.
type corpusWalk struct {
	ids      []string
	vecs     [][]float32
	metas    []drawerMeta
	missIdx  []int
	missText []string
	facts    buildFacts
	stats    RebuildStats
}

// walkCorpus reads a project's four corpus sources into a corpusWalk, using only
// embed-cache hits (never embedding) and never taking the commit lock or e.mu.
// It is called by rebuild (which then embeds the misses and installs under the
// locks) and by tryWitnessedLoad (which uses only ids+metas, taking the vectors
// from the on-disk graph). It must NOT regenerate decision chunks — that is a
// commit-lock WRITE rebuild does before this walk; the walk reads the store as
// it stands.
func (e *Engine) walkCorpus(ctx context.Context, project string, mode rebuildMode) (corpusWalk, error) {
	var ids []string
	var vecs [][]float32
	var metas []drawerMeta

	// Positions in vecs that still need an embedding, and their drawer text.
	var missIdx []int
	var missText []string

	var stats RebuildStats
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
		return corpusWalk{stats: stats}, fmt.Errorf("read index store: %w", err)
	}
	storeIDs := map[string]bool{}
	decisionNotes, localOwners := map[string]bool{}, map[string]bool{}
	for _, c := range st.Chunks(true) {
		if err := ctx.Err(); err != nil {
			return corpusWalk{stats: stats}, err
		}
		storeIDs[c.ID] = true
		vec, err := e.cachedVector(project, c.ID)
		if err != nil {
			return corpusWalk{stats: stats}, err
		}
		if c.SourceType == storage.SourceTypeDecision {
			if vec == nil && !mode.embedOthers {
				continue // --no-embed: a decision miss is left out (cache hits only)
			}
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
				if !mode.embedLocal {
					// The default and --no-embed: leave the miss out and count
					// it, so it sets the missing-vector stale reason.
					facts.localMisses++
					continue
				}
				// Explicit: embed this local-tier miss; it becomes a hit with a
				// vector, so no missing-vector reason is set.
				missIdx = append(missIdx, len(vecs))
				missText = append(missText, c.Content)
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
		return corpusWalk{stats: stats}, err
	}
	var wings []string
	if !migrated {
		if wings, err = e.vault.ListWings(project); err != nil {
			return corpusWalk{stats: stats}, fmt.Errorf("list wings: %w", err)
		}
	}

	for _, wing := range wings {
		rooms, err := e.vault.ListRooms(project, wing)
		if err != nil {
			return corpusWalk{stats: stats}, fmt.Errorf("list rooms for wing %s: %w", wing, err)
		}

		for _, room := range rooms {
			drawers, err := e.vault.ListDrawers(project, wing, room)
			if err != nil {
				slog.Warn("rebuild: list drawers failed", "project", project, "wing", wing, "room", room, "err", err)
				continue
			}

			for _, d := range drawers {
				if err := ctx.Err(); err != nil {
					return corpusWalk{stats: stats}, err
				}
				if storeIDs[index.ChunkID(d.Content)] {
					stats.DedupedDrawers++
					continue
				}
				stats.Drawers++

				// Try cache first; misses are embedded together below.
				vec, err := e.cachedVector(project, d.ID)
				if err != nil {
					return corpusWalk{stats: stats}, err
				}
				if vec == nil {
					if !mode.embedOthers {
						continue // --no-embed: cache hits only
					}
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
		return corpusWalk{stats: stats}, fmt.Errorf("iteration corpus: %w", err)
	}
	stats.IterationChunks = len(iterIDs)
	for i, id := range iterIDs {
		if err := ctx.Err(); err != nil {
			return corpusWalk{stats: stats}, err
		}
		vec, err := e.cachedVector(project, id)
		if err != nil {
			return corpusWalk{stats: stats}, err
		}
		if vec == nil {
			if !mode.embedOthers {
				continue // --no-embed: cache hits only
			}
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
		return corpusWalk{stats: stats}, fmt.Errorf("note corpus: %w", err)
	}
	stats.NoteChunks = len(noteIDs)
	for i, id := range noteIDs {
		if err := ctx.Err(); err != nil {
			return corpusWalk{stats: stats}, err
		}
		vec, err := e.cachedVector(project, id)
		if err != nil {
			return corpusWalk{stats: stats}, err
		}
		if vec == nil {
			if !mode.embedOthers {
				continue // --no-embed: cache hits only
			}
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
	return corpusWalk{ids: ids, vecs: vecs, metas: metas, missIdx: missIdx, missText: missText, facts: facts, stats: stats}, nil
}

// rebuild is Rebuild's body. It holds the project's mutex for the whole run,
// so it never interleaves with IndexDrawers on the project
// (their inserts can no longer be dropped by its swap, nor their vectors
// reaped). It embeds without the commit lock, commits each embed batch in its
// own short Tx, and takes one final Tx for the reap. A commit lock that is busy
// past timeout skips that write: the batch serves this process from memory and
// is embedded again later, and the reap waits for the next build. Only a busy
// mutex fails the build, with an error wrapping vaultlock.ErrLockWaitTimeout.
func (e *Engine) rebuild(ctx context.Context, project string, timeout time.Duration, mode rebuildMode, refuse bool, start buildStart) (RebuildStats, error) {
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

	// The notes tier's decision-chunk writer, BEFORE the store read below: it
	// makes the store hold exactly the decision chunks of the session notes on
	// disk, so the read then loads them. The same writer the capture path uses,
	// so a rebuilt decision matches a captured one (ADR-014; this child's Scope
	// 2). It takes the commit lock in its own short Tx and skips on a busy lock.
	if err := e.regenerateDecisionChunks(ctx, pl, timeout, &chain, project); err != nil {
		return stats, fmt.Errorf("note corpus: regenerate decision chunks: %w", err)
	}

	// The four-source corpus walk, lock-free (shared with the witnessed consume,
	// tryWitnessedLoad). It embeds nothing; the misses it reports are embedded
	// below, under this build's held locks.
	w, err := e.walkCorpus(ctx, project, mode)
	if err != nil {
		return w.stats, err
	}
	ids, vecs, metas := w.ids, w.vecs, w.metas
	missIdx, missText := w.missIdx, w.missText
	facts := w.facts
	stats = w.stats

	// Corpus gate (§1): on the search path, refuse to embed a cold corpus rather
	// than block the caller for the whole embed. stats.Embedded is the miss count
	// the walk above already produced (archives are excluded by embedLocal), so a
	// warm cache (zero misses) still builds. A budget of one embed batch lets a
	// tiny never-built project build; anything larger returns the sentinel, which
	// ensureIndex maps to serve-as-is (HasIndex) or *IndexNotBuiltError. Nothing
	// has been installed yet, so e.indexes/e.loaded are left untouched.
	if refuse {
		budget := e.config.EmbedderBatchSize
		if budget <= 0 {
			budget = 32
		}
		if stats.Embedded > budget {
			return stats, errEmbedRefused
		}
		// ADR-016 decision 6, U4 site (d): even a within-budget cold corpus embed
		// is throttled on the search path during the inter-run cooldown, so N
		// engines do not keep the host hot embedding note/decision misses on every
		// search. A warm build (stats.Embedded == 0) still installs — never degrade
		// a warm search. The degrade reuses the errEmbedRefused path: ensureIndex
		// then calls ensureDetachedRebuild, whose launcher records the durable
		// corpus want (NoteRebuild) and itself honours the cooldown. The check is a
		// lock-free marker read (no try-lock in the build body), fail-open.
		if stats.Embedded > 0 && e.indexRunCooldownActive() {
			return stats, errEmbedRefused
		}
	}

	commit := func(batch map[string][]float32) { e.commitVectors(ctx, pl, timeout, batch, &chain) }
	// embedBudget (§5) is a wall-clock backstop around embedMisses ONLY, on the
	// search path with misses to embed — a child of the caller ctx, never passed
	// to lockProject (already held) nor to the commit closure (which keeps the
	// outer ctx + timeout). embedMisses honors ctx between batches, so a deadline
	// stops it before the index is installed below; ensureIndex maps the deadline
	// like a refusal.
	embedCtx := ctx
	if refuse && embedBudget > 0 && stats.Embedded > 0 {
		var cancel context.CancelFunc
		embedCtx, cancel = context.WithTimeout(ctx, embedBudget)
		defer cancel()
	}
	if err := e.embedMisses(embedCtx, project, ids, vecs, missIdx, missText, commit); err != nil {
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
		// A project that shrank to nothing while a graph file is present is the
		// table's n<L delete row: drop hnsw.idx on its own gen-only Tx (N4).
		// ReadGraph is lock-free; a missing file needs no Tx.
		if data, rerr := indexstore.ReadGraph(e.vault, project); rerr == nil && data != nil {
			e.commitGraphAction(ctx, pl, timeout, project, nil, graphDelete, &chain)
		}
		e.remember(project, &chain, start)
		return stats, nil
	}

	dims, err := e.embedder.Dimensions()
	if err != nil {
		return stats, fmt.Errorf("embedder dimensions: %w", err)
	}

	// Unit B selection + warm-start. n is the post-dedup keep-set length (B4) —
	// the count the built graph actually holds (dims+usableVector drop applied),
	// NOT len(ids) nor the distinct-id count — so selection and the Unit C
	// catch-up reconcile against exactly the ids the graph would hold.
	params := provisionalHNSWParams
	keep, err := dedupBatch(vecs, ids, dims)
	if err != nil {
		return stats, fmt.Errorf("select index kind: %w", err)
	}
	n := len(keep)

	idx0, res, lerr := newHNSWIndexFromDisk(e.vault, project, dims, params)
	if lerr != nil {
		// B2: an I/O fault (not a corrupt classification) is Unit A's
		// do-not-delete rule — fall back to brute for THIS build; do not fail
		// the rebuild and do not delete the file.
		slog.Warn("hnsw warm-start load failed; serving brute for this build", "project", project, "err", lerr)
		if idx0 != nil {
			_ = idx0.Close()
			idx0 = nil
		}
		res = noGraph
	}
	kind, action := selectKind(res, n, mode.converts)

	var idx VectorIndex
	switch kind {
	case kindHNSW:
		if res == loaded && idx0 != nil {
			// Unit C: incrementally catch the loaded graph up to the corpus
			// (insert new/changed ids, tombstone removed) instead of a full
			// rebuild. finishCatchUp runs on EVERY exit of rebuild (R1): it
			// clears the suppression flag and re-arms the tombstone auto-rebuild.
			// The re-arm happens whatever the exit — a durable save, a debounced
			// or stale skip (no new save to wait for), or a catch-up error — so
			// an over-ratio graph still self-heals.
			idx0.beginCatchUp()
			defer idx0.finishCatchUp()
			changed, cerr := e.catchUpGraph(idx0, keep, ids, vecs, metas)
			if cerr != nil {
				// N-CR1: a failed insert would leave a partial graph; fail the
				// rebuild (like a Build error) so remember is never reached and
				// nothing is installed or persisted.
				return stats, fmt.Errorf("catch-up: %w", cerr)
			}
			idx = idx0
			if changed {
				action = graphSave // persist the caught-up graph (debounced in commitGraphAction)
			}
		} else {
			if idx0 != nil {
				_ = idx0.Close()
			}
			h := newHNSWIndex(dims, params)
			if err := h.Build(vecs, ids); err != nil {
				return stats, fmt.Errorf("build index: %w", err)
			}
			idx = h
			action = graphSave // persist the just-built graph
		}
	default: // kindBrute
		if idx0 != nil {
			_ = idx0.Close()
		}
		b := newBruteIndex(dims)
		if err := b.Build(vecs, ids); err != nil {
			return stats, fmt.Errorf("build index: %w", err)
		}
		idx = b
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

	// R1: the graph file action rides its OWN short, gen-only Tx — NEVER
	// finalCommit's Tx, whose reap (reapLocked → beginDestructive) pre-bumps gen
	// WITH an epoch change. It runs AFTER finalCommit because that is the correct
	// side of the reap: a pre-finalCommit save would let the reap's epoch change
	// land AFTER the save's gen-only commit, and then another engine's epoch
	// re-check (saveGraph's builtAt.Epoch compare) would drop a save that had
	// just succeeded. index/<p>/ must already exist and saveGraph never creates
	// it; on a converting rebuild the first completeness write (completeness.go,
	// writeCompleteness) is what creates it — not every finalCommit does. In the
	// no-reap case the graph Tx chains from finalCommit's gen and remember (next)
	// records it, so warm-start holds; in the reap case finalCommit has already
	// broken the chain, so remember keeps start.gen and the next search rebuilds
	// — the same fallback as any reaping rebuild today.
	stats.Reaped = e.finalCommit(ctx, pl, timeout, &chain, facts)
	e.commitGraphAction(ctx, pl, timeout, project, idx, action, &chain)
	e.remember(project, &chain, start)
	return stats, nil
}

// catchUpErrHook, when a test sets it, forces catchUpGraph's next Insert to
// return an error, at a real Insert site — so N-CR1's "a failed insert fails the
// rebuild" path can be exercised (the real path's vectors are dims/usable-checked
// by dedupBatch, so Insert does not otherwise error). Production leaves it nil.
var catchUpErrHook func() error

// catchUpGraph reconciles a warm-start-loaded graph to the current corpus
// incrementally (Unit C), instead of a full rebuild on drift. It Inserts EVERY
// expected id and Deletes every removed id, and reports whether anything
// actually changed:
//
//   - expected = the keep-set ids minus ids that would collide on the global
//     drawer-id key (those carry no metadata and are dropped by search; excluding
//     them matches B's sameIDSet, and does not claim same id ⇒ same content —
//     DrawerID is a 32-bit md5[:8] the engine documents can collide).
//   - Insert of every expected id relies on the wrapper's equal-vector no-op
//     (an unchanged id mutates nothing); a same-id changed vector upserts
//     (tombstone old, add new), so the caught-up graph is vector-identical in
//     served results to a full Build of keep (B2).
//   - removed = the graph's live ids absent from expected; each is tombstoned.
//
// It runs under the index's beginCatchUp suppression, so its churn starts no
// background rebuild (which would make saveGraph refuse the save). The caller
// saves only when it returns changed==true. The new/changed vectors come from
// vecs (the walk's embed-cache reads) — no re-embed, no full g.Add.
//
// A failed Insert is returned as an error (N-CR1), not swallowed: a partial
// graph must fail the rebuild so the caller never installs or remembers it. On
// the real path this is defensive — the vectors already passed dedupBatch's
// dims/usable check, so Insert does not error — but a corrupt vector must never
// be silently remembered. Delete cannot fail (it returns bool).
func (e *Engine) catchUpGraph(idx0 *hnswIndex, keep []int, ids []string, vecs [][]float32, metas []drawerMeta) (bool, error) {
	expected := make(map[string]struct{}, len(keep))
	expectedPos := make([]int, 0, len(keep))
	e.mu.RLock()
	for _, i := range keep {
		if e.wouldCollide(ids[i], metas[i]) {
			continue
		}
		if _, dup := expected[ids[i]]; dup {
			continue
		}
		expected[ids[i]] = struct{}{}
		expectedPos = append(expectedPos, i)
	}
	e.mu.RUnlock()

	var removed []string
	for _, id := range idx0.liveIDs() {
		if _, ok := expected[id]; !ok {
			removed = append(removed, id)
		}
	}

	changed := len(removed) > 0
	startAdds := idx0.libraryAdds.Load()
	for _, id := range removed {
		idx0.Delete(id)
	}
	for _, i := range expectedPos {
		err := idx0.Insert(ids[i], vecs[i])
		if err == nil && catchUpErrHook != nil {
			err = catchUpErrHook() // test seam: simulate an Insert failure at a real Insert site
		}
		if err != nil {
			return changed, fmt.Errorf("insert %s: %w", ids[i], err)
		}
	}
	if idx0.libraryAdds.Load() != startAdds {
		changed = true // a new or changed id did a g.Add; an unchanged id is a no-op
	}
	return changed, nil
}

// wouldCollide is detectCollision's read-only predicate: the global drawer-id
// key already holds DIFFERENT content. Unlike detectCollision it neither counts
// nor logs (the real detectCollision still fires at the metadata write). The
// caller must hold e.mu (read or write).
func (e *Engine) wouldCollide(id string, meta drawerMeta) bool {
	existing, ok := e.metadata[id]
	return ok && existing.Content != meta.Content
}

// --- background brute→HNSW converter (task hnsw-kind-swap-dual-write-conversion)

// maybeStartConvert is the converter's trigger (Step 1). It is called from
// ensureIndex on the serving engine only (convertEnabled). It launches one
// detached conversion for project when the project is served by a *bruteIndex
// over the HNSW threshold, no conversion is already running, and the
// write-counted backoff from any previous abandon has elapsed. Every real check
// is repeated under the project mutex inside runConvert; this is only a cheap,
// throttled gate so a hot search loop does not spawn goroutines unboundedly.
func (e *Engine) maybeStartConvert(project string) {
	if !e.convertEnabled || e.convertCtx.Err() != nil {
		return
	}

	e.mu.RLock()
	b, ok := e.indexes[project].(*bruteIndex)
	e.mu.RUnlock()
	if !ok || b.Len() < hnswSelectT {
		return // not brute, or below the switch threshold
	}

	e.convertMu.Lock()
	if _, running := e.converting[project]; running {
		e.convertMu.Unlock()
		return
	}
	if bo, ok := e.convertBackoff[project]; ok && bo.abandons > 0 {
		// A write-counted backoff after the last abandon: skip until the store has
		// taken rebuildBackoffGens(1, abandons) more writes. ReadGeneration is
		// lock-free; only read it on the backoff path.
		if cur, err := indexstore.ReadGeneration(e.vault, project); err == nil {
			if cur.Gen-bo.abandonedAt.Gen < rebuildBackoffGens(1, bo.abandons) {
				e.convertMu.Unlock()
				return
			}
		}
	}
	e.converting[project] = struct{}{}
	e.convertMu.Unlock()

	e.convertWG.Add(1)
	go e.runConvert(project)
}

// clearConverting drops project from the in-flight set.
func (e *Engine) clearConverting(project string) {
	e.convertMu.Lock()
	delete(e.converting, project)
	e.convertMu.Unlock()
}

// convertAbandon records a write-counted backoff for project after an abandoned
// conversion, so the next trigger waits (mirrors the wrapper's rebuild backoff).
func (e *Engine) convertAbandon(project string) {
	cur, err := indexstore.ReadGeneration(e.vault, project)
	if err != nil {
		return // cannot time a backoff without the counter; the trigger will re-check
	}
	e.convertMu.Lock()
	bo := e.convertBackoff[project]
	bo.abandons++
	bo.abandonedAt = cur
	e.convertBackoff[project] = bo
	e.convertMu.Unlock()
}

// convertResetBackoff clears project's backoff after a successful conversion.
func (e *Engine) convertResetBackoff(project string) {
	e.convertMu.Lock()
	delete(e.convertBackoff, project)
	e.convertMu.Unlock()
}

// convertVecsFromCache reads the embed-cache vector of each id. ok is false if
// any id missed the cache (the converter cannot reconcile a want-id it cannot
// embed, so it abandons rather than publish a graph short that id). Every id a
// brute index holds was cache-committed before it was inserted
// (indexProjectDrawers commits vectors at :817 before the insert at :884), so a
// miss here is the rare torn-cache case.
func (e *Engine) convertVecsFromCache(project string, ids []string) (map[string][]float32, bool) {
	out := make(map[string][]float32, len(ids))
	for _, id := range ids {
		vec, err := e.cachedVector(project, id)
		if err != nil || vec == nil {
			return nil, false
		}
		out[id] = vec
	}
	return out, true
}

// convertSetDiff reports, against h's current live ids, the ids in want that h
// lacks (adds) and the ids h holds that want no longer has (dels).
func convertSetDiff(h *hnswIndex, want map[string]struct{}) (adds, dels []string) {
	have := make(map[string]struct{})
	for _, id := range h.liveIDs() {
		have[id] = struct{}{}
	}
	for id := range want {
		if _, ok := have[id]; !ok {
			adds = append(adds, id)
		}
	}
	for id := range have {
		if _, ok := want[id]; !ok {
			dels = append(dels, id)
		}
	}
	return adds, dels
}

// convertReconcileFull makes h's live set exactly want, off the project mutex
// (Step 4). It Inserts EVERY want id with its CURRENT cached vector — so a
// changed vector for an id h already holds upserts (hnswIndex.Insert's
// equal-vector guard makes an unchanged one a no-op), closing the upsert race —
// and Deletes every h id not in want. Vectors come from the embed cache; a miss
// abandons (false). h is private to the converter, so only its own lock applies.
func (e *Engine) convertReconcileFull(project string, h *hnswIndex, want map[string]struct{}) bool {
	_, dels := convertSetDiff(h, want)
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	vecs, ok := e.convertVecsFromCache(project, ids)
	if !ok {
		return false
	}
	for _, id := range dels {
		h.Delete(id)
	}
	for _, id := range ids {
		if err := h.Insert(id, vecs[id]); err != nil {
			return false // a corrupt vector must never be silently published
		}
	}
	return true
}

// convertReconcileBounded is the under-mutex final reconcile (Step 5.3): b0 is
// frozen (the caller holds the project mutex), so only the set difference can
// remain — an id captured or reaped in the off-lock window between the last
// Step-4 round and here. It applies at most bound mutations (Insert the adds
// from the cache, Delete the dels) and abandons (false) if the difference
// exceeds bound or an add has no cached vector. It does NOT re-Insert ids h
// already holds: any changed vector was applied in Step 4, and b0 cannot change
// under the mutex. Under-mutex work is one b0 enumeration (the caller's want)
// plus at most bound mutations, never an O(corpus) read (S1).
func (e *Engine) convertReconcileBounded(project string, h *hnswIndex, want map[string]struct{}, bound int) bool {
	adds, dels := convertSetDiff(h, want)
	if len(adds)+len(dels) > bound {
		return false // writes outpaced the catch-up
	}
	vecs, ok := e.convertVecsFromCache(project, adds)
	if !ok {
		return false
	}
	for _, id := range dels {
		h.Delete(id)
	}
	for _, id := range adds {
		if err := h.Insert(id, vecs[id]); err != nil {
			return false
		}
	}
	return true
}

// convertWantLocked is the completeness oracle's target set (Step 5.3): the ids
// the brute index b0 holds that also have a published metadata row. The caller
// holds e.mu. b0 is frozen (the caller holds the project mutex), so its LiveIDs
// are a stable snapshot. An id in b0 with NO metadata row is a publish-skipped
// collision loser (indexProjectDrawers phase 3 at :898-908) or metadata-less
// hit, which search drops anyway (:625-627); excluding it is the collision rule
// AS A FILTER (never a veto — N-D1), realised through e.metadata because a
// bruteIndex carries no per-id content for wouldCollide and e.metadata is the
// authoritative published meta.
func (e *Engine) convertWantLocked(b0 *bruteIndex) map[string]struct{} {
	want := make(map[string]struct{})
	for _, id := range b0.LiveIDs() {
		if _, ok := e.metadata[id]; ok {
			want[id] = struct{}{}
		}
	}
	return want
}

// sameIDSet reports whether a (an id slice) and b (an id set) hold the same ids.
func sameIDSet(a []string, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for _, id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}

// runConvert performs the brute→HNSW conversion for one project (Steps 2-7). It
// builds a detached HNSW graph off every engine lock while the authoritative
// brute index keeps serving and absorbing writes (the dual-write), reconciles
// the graph to the brute index, and — under ONE project-mutex hold — proves the
// graph holds exactly what brute holds, swaps it in, and persists it behind a
// generation witness. It loses no write: nothing is published until the gate
// proves the graph equals brute, and brute stays authoritative until the swap.
func (e *Engine) runConvert(project string) {
	defer e.convertWG.Done()
	defer e.clearConverting(project)
	ctx := e.convertCtx

	// ADR-016 decision 6, U4 site (c): the convert is a CPU-heavy build pass, so
	// it honours the inter-run cooldown. A lock-free marker read before the
	// TryRunLock below; while an embedding run completed within the window, this
	// convert defers. Like the decline branch, it records NO backoff and NO
	// marker and does NOT call convertAbandon (that keys on this project's Gen,
	// which a cooldown skip never advances, so it would wrongly suppress the
	// convert until an unrelated write) — it simply re-fires on the next
	// maybeStartConvert trigger (a later search of this project).
	if e.indexRunCooldownActive() {
		return
	}

	// ADR-016 decision 3: the converter is a corpus-building embed pass, so it
	// runs under the (host,vault) index run lock — at most one convert per vault.
	// Taken HERE, before the Step-2 lockProject below, it keeps the documented
	// order (run lock → project mutex → commit lock; see lock.go and ADR-014
	// decision 7). This is a FRESH, non-blocking TryRunLock: runConvert holds no
	// run lock of its own to pass in (unlike the detached builder's RunHeld path).
	// The one per-vault run lock has other openers in THIS process too — a
	// lifecycle rename (indexstore.AdoptRenamedProject) and the lockProject sweep's
	// runLockHeld probe — so contention here may be cross-process (a detached
	// vp index rebuild / vp drain archives) OR in-process; either way it surfaces
	// as ok=false (a second same-process open does not hang), and the decline
	// branch handles it.
	held, ok, err := indexstore.TryRunLock(e.vault, indexstore.KindRebuild, project)
	if err != nil {
		// Infra failure, not contention: the locks dir could not be created or the
		// lock file could not be opened/flocked for a reason other than a peer
		// holding it (runlock.go:81-91). Log and return; a later trigger retries.
		// NOT a backoff — this is not the lock being held.
		slog.Warn("hnsw convert: run lock unavailable; a later trigger retries", "project", project, "err", err)
		return
	}
	if !ok {
		// Declined: a detached rebuild/ingest, or an in-process lifecycle/rename,
		// holds the vault run lock. Per ADR-016 decision 3, re-fire on the NEXT
		// maybeStartConvert trigger — record NO backoff and write NO inbox marker.
		// (convertAbandon is WRONG here: it keys the backoff on THIS project's Gen,
		// which a convert that lost the vault-wide lock to another project's build
		// never advances.) While a foreign holder keeps the vault lock, each search
		// of a brute-over-threshold project re-fires a fresh runConvert that does one
		// cheap non-blocking TryRunLock-and-return (a single flock syscall — no
		// lockProject, no build/embed), so a persistent holder means repeated CHEAP
		// re-attempts, not repeated embeds. (clearConverting runs on the way out and
		// `converting` only collapses CONCURRENT launches, so it does NOT bound these
		// sequential per-search re-fires; a bounded holder-liveness decline mark —
		// re-fire once, then wait until the holder is gone — is the filed follow-on
		// `converter-decline-needs-holder-liveness-bound`.)
		return
	}
	defer func() { _ = held.Release() }() // panic-safe: Release returns an error

	// Step 2: snapshot b0 and its id set under the project mutex, then release.
	pl, err := e.lockProject(ctx, project, searchLockTimeout)
	if err != nil {
		return // busy or cancelled; a later trigger retries
	}
	// A graph file already present means this is a mismatch/corrupt file (a valid
	// one would have made the project HNSW, not brute) — that is Unit E heal's
	// job, not a conversion. Skip.
	if data, rerr := indexstore.ReadGraph(e.vault, project); rerr != nil || data != nil {
		pl.release()
		return
	}
	e.mu.RLock()
	b0, ok := e.indexes[project].(*bruteIndex)
	var ids0 []string
	if ok {
		ids0 = b0.LiveIDs()
	}
	e.mu.RUnlock()
	pl.release()
	if !ok || len(ids0) < hnswSelectT {
		return
	}

	dims, derr := e.embedder.Dimensions()
	if derr != nil {
		return
	}

	// Step 3: build the detached graph off ALL locks, from cache-hit vectors.
	vecs0, ok := e.convertVecsFromCache(project, ids0)
	if !ok {
		return // a torn cache; a later rebuild repairs vectors, then retry
	}
	buildVecs := make([][]float32, 0, len(ids0))
	buildIDs := make([]string, 0, len(ids0))
	for _, id := range ids0 {
		buildVecs = append(buildVecs, vecs0[id])
		buildIDs = append(buildIDs, id)
	}
	h := newHNSWIndex(dims, provisionalHNSWParams)
	// B3: begin suppression on h ONLY, and disarm it with a defer that runs at
	// function return — i.e. strictly AFTER the Step-5 save — on every exit path
	// (abandon, gate failure, save skip, panic). finishCatchUp is never called
	// before the save, and never on b0 (a *bruteIndex has no such method).
	h.beginCatchUp()
	defer h.finishCatchUp()
	if err := h.Build(buildVecs, buildIDs); err != nil {
		return
	}
	if convertAfterBuild != nil {
		convertAfterBuild()
	}

	// Step 4: off-lock catch-up rounds. Reconcile h toward b0's live set without
	// any lock, narrowing the delta each round; proceed once at most
	// convertFinalReplayMax mutations remain, abandon if still outpaced after
	// convertCatchUpRounds rounds.
	converged := false
	for round := 0; round < convertCatchUpRounds; round++ {
		if ctx.Err() != nil {
			return
		}
		e.mu.RLock()
		cur, stillB0 := e.indexes[project].(*bruteIndex)
		changed := !stillB0 || cur != b0
		var want map[string]struct{}
		if !changed {
			want = e.convertWantLocked(b0)
		}
		e.mu.RUnlock()
		if changed {
			e.convertAbandon(project)
			return // b0 was replaced (rebuild / drop / Close): abandon
		}
		if convertEachRound != nil {
			convertEachRound(round)
		}
		// Reconcile h to this round's want. The full reconcile also upserts a
		// changed vector landed during the build window.
		if !e.convertReconcileFull(project, h, want) {
			e.convertAbandon(project) // S1: a persistent cache miss must back off, not thrash
			return
		}
		// Judge convergence on the delta to b0 AS IT STANDS AFTER the reconcile
		// (S2): a pre-reconcile snapshot (or one against a stale want) could break
		// the loop with h more than the bound behind b0 and waste the Step-5 try.
		e.mu.RLock()
		cur2, still := e.indexes[project].(*bruteIndex)
		var want2 map[string]struct{}
		if still && cur2 == b0 {
			want2 = e.convertWantLocked(b0)
		}
		e.mu.RUnlock()
		if !still || cur2 != b0 {
			e.convertAbandon(project)
			return // b0 replaced mid-round
		}
		adds, dels := convertSetDiff(h, want2)
		if len(adds)+len(dels) <= convertFinalReplayMax {
			converged = true
			break
		}
	}
	if !converged {
		e.convertAbandon(project) // writes outpaced the catch-up
		return
	}
	if convertBeforeFinal != nil {
		convertBeforeFinal()
	}

	// Step 5: THE CRITICAL SECTION — one project-mutex hold through the save and
	// remember. Lock order: project mutex → e.mu (for the identity/want and the
	// swap, released before the commit lock) → commit lock via pl.Tx.
	pl, err = e.lockProject(ctx, project, searchLockTimeout)
	if err != nil {
		return // busy/cancelled: no swap happened; a later trigger retries
	}
	defer pl.release()

	// 5.1 generation witness, read before the final reconcile.
	builtGen, gerr := indexstore.ReadGeneration(e.vault, project)
	if gerr != nil {
		return
	}
	e.buildMu.Lock()
	invStart := e.invalidated[project]
	e.buildMu.Unlock()

	// 5.2 identity + eligibility, and 5.3 the want set — all under one e.mu hold.
	e.mu.Lock()
	if cur, ok := e.indexes[project].(*bruteIndex); !ok || cur != b0 {
		e.mu.Unlock()
		e.convertAbandon(project) // b0 replaced during Steps 2-4
		return
	}
	if b0.Len() < hnswSelectT {
		e.mu.Unlock()
		e.convertAbandon(project) // a reaper dropped it below the band (S2)
		return
	}
	want := e.convertWantLocked(b0)
	e.mu.Unlock()

	// 5.3 final bounded reconcile, and 5.4 the completeness gate.
	if !e.convertReconcileBounded(project, h, want, convertFinalReplayMax) {
		e.convertAbandon(project)
		return
	}
	if convertBeforeGate != nil {
		convertBeforeGate(h)
	}
	if !sameIDSet(h.liveIDs(), want) {
		e.convertAbandon(project) // the child-16 proof: h must equal b0's want exactly
		return
	}
	if convertBeforeSwap != nil {
		convertBeforeSwap()
	}

	// 5.5 atomic kind swap under e.mu (not held across the commit lock).
	e.mu.Lock()
	if cur, ok := e.indexes[project].(*bruteIndex); !ok || cur != b0 {
		e.mu.Unlock()
		e.convertAbandon(project)
		return
	}
	e.indexes[project] = h
	e.mu.Unlock()
	e.closeReplaced(b0)
	if convertBeforeSave != nil {
		convertBeforeSave()
	}

	// 5.6 gen-guarded save on its own short commit-lock Tx. Skip the file unless
	// the store generation still equals builtGen (heal's witness,
	// graph_heal.go:311-313): a gen change means another process committed since
	// the reconcile, so the graph may omit its write.
	chain := genChain{cur: builtGen}
	out, written := e.convertSave(ctx, pl, project, h, builtGen, &chain)

	// 5.7 remember ONLY on a written save at the witnessed gen; otherwise drop
	// e.loaded directly so the next search rebuilds and the trigger retries (N-D2
	// — remember would record builtGen as current on a skip, an in-process loss).
	if written {
		e.remember(project, &chain, buildStart{gen: builtGen, readable: true, invalidated: invStart})
		e.convertResetBackoff(project)
		// ADR-016 decision 6: a completed, persisted convert is an embedding-class
		// build pass, so it arms the inter-run cooldown for the next trigger.
		if err := indexstore.WriteRunCompleted(e.vault, time.Now()); err != nil {
			slog.Warn("hnsw convert: could not stamp the index-run cooldown marker", "project", project, "err", err)
		}
	} else {
		e.buildMu.Lock()
		delete(e.loaded, project)
		e.buildMu.Unlock()
		slog.Info("hnsw conversion swapped but did not persist; next search rebuilds", "project", project, "outcome", out.String())
	}
}

// convertSave writes h as hnsw.idx on its own commit-lock Tx, guarded by the
// generation witness. It returns the save outcome and whether the file was
// actually written (so the caller remembers only a durable, witnessed save).
// The caller holds the project mutex (pl); this takes and releases only the
// commit lock, through pl.Tx, so TestLockDiscipline holds (no direct
// indexstore.Lock from internal/search).
func (e *Engine) convertSave(ctx context.Context, pl *projectLock, project string, h *hnswIndex, builtGen indexstore.Gen, chain *genChain) (saveOutcome, bool) {
	tx, err := pl.Tx(ctx, searchLockTimeout)
	if err != nil {
		// The swap already happened in memory; the file waits for the next trigger
		// or a restart's warm-start reconcile. Not written.
		return skipTimeout, false
	}
	if tx.Generation() != builtGen {
		_, _, _ = pl.finishTx()
		return skipEpoch, false // the store moved under us; abandon the file
	}
	data, w := h.snapshot()
	builtAt := tx.Generation()
	out, serr := saveGraph(tx, e.vault, project, h, data, w, builtAt)
	before, after, cerr := pl.finishTx()
	if serr != nil {
		slog.Warn("hnsw conversion save failed", "project", project, "outcome", out.String(), "err", serr)
		return out, false
	}
	if cerr != nil {
		slog.Warn("hnsw conversion commit failed", "project", project, "err", cerr)
		chain.broken = true
		return out, false
	}
	if out == written {
		chain.step(before, after)
		return out, true
	}
	return out, false
}

// commitGraphAction performs a rebuild's graph file action (save or delete) on
// its OWN short, gen-only Tx, taken and committed here (R1). A busy lock marks
// the chain skipped (the build is not remembered; the next explicit rebuild
// retries); a gone project is a no-op. On a genuinely successful write it steps
// the chain by the single gen-only bump the Commit made; that stepped gen is
// what remember (called next, after this returns) records, so the project is
// current and warm-start holds for a no-reap build (N-CR1: there is no following
// finalCommit — it already ran). A skip outcome that wrote nothing makes no bump
// and does not break the chain. A genuine write error (skipWriteErr, B-CR1) is
// NOT stepped and is logged as a real failure.
func (e *Engine) commitGraphAction(ctx context.Context, pl *projectLock, timeout time.Duration, project string, idx VectorIndex, action graphAction, chain *genChain) {
	if action == graphNone {
		return
	}
	tx, err := pl.Tx(ctx, timeout)
	if isLockTimeout(err) {
		chain.skipped = true
		slog.Info("hnsw graph action skipped: the index commit lock is busy", "project", project, "action", action.String())
		return
	}
	if errors.Is(err, indexstore.ErrProjectGone) {
		return
	}
	if err != nil {
		slog.Warn("hnsw graph action skipped", "project", project, "action", action.String(), "err", err)
		return
	}

	var out saveOutcome
	switch action {
	case graphSave:
		h, ok := idx.(*hnswIndex)
		if !ok {
			_, _, _ = pl.finishTx() // defensive: only an hnswIndex is ever saved
			return
		}
		// Debounce (Unit C): under the commit lock we just took, so two processes
		// serialize and the second reads the first's fresh mtime. Within the
		// window, write nothing — the in-memory index stays current and the next
		// trigger past the window (or a restart's catch-up) persists it. A missing
		// file is not debounced (the first convert always saves); a stat error
		// falls through to saveGraph (the safe direction), never a silent skip.
		if mt, present, serr := graphFileModTime(e.vault, project); serr == nil && present && nowFn().Sub(mt) < graphSaveDebounce {
			out = skipDebounce
			break
		}
		data, w := h.snapshot()
		builtAt := tx.Generation()
		out, err = saveGraph(tx, e.vault, project, h, data, w, builtAt)
	case graphDelete:
		out, err = deleteGraph(tx)
	}
	before, after, cerr := pl.finishTx()
	if err != nil {
		// A genuine write error (skipWriteErr) or a save that chose not to write:
		// either way the bytes are not trustworthy, so do not step the chain.
		slog.Warn("hnsw graph action failed", "project", project, "action", action.String(), "outcome", out.String(), "err", err)
	}
	if cerr != nil {
		slog.Warn("hnsw graph commit failed", "project", project, "err", cerr)
		chain.broken = true
		return
	}
	if err == nil && out == written {
		chain.step(before, after) // the single gen-only bump (a no-op (cur,cur) when nothing was written)
	}
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
	// Cancel and wait for any in-flight background conversion BEFORE the index
	// map is replaced below, so no converter performs its Step-5 swap into a map
	// this Close has just cleared. convertCtx cancellation unblocks a converter
	// waiting in lockProject (those waits are also bounded by searchLockTimeout);
	// a converter already inside h.Build is awaited to completion, because g.Add
	// takes no ctx — bounded by one build, off all engine locks, with no swap
	// pending (S3; making Build cancellable is a separate wrapper follow-on).
	if e.convertCancel != nil {
		e.convertCancel()
	}
	e.convertWG.Wait()

	// Wait for any in-flight proactive warm-up construct to finish before closing
	// the embedder, so a construct goroutine never publishes a live model session
	// into a process whose embedder has already been closed (S-D1).
	e.warmWG.Wait()
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
