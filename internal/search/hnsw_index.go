// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"cmp"
	"context"
	"log/slog"
	"math"
	"math/rand"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/hnsw"
	"github.com/viterin/vek/vek32"
)

// hnswParams configures an hnswIndex.
type hnswParams struct {
	// M is the maximum number of neighbours kept per node.
	M int
	// EfSearch is the search breadth. The library has no efConstruction: Add
	// uses EfSearch too, so it also sets build quality and build cost.
	EfSearch int
	// Seed seeds the graph's level generator. It does NOT make a build
	// reproducible: the library picks each layer's entry node by ranging over
	// a Go map (coder/hnsw graph.go, layer.entry), whose order is randomised.
	// Nothing tests the seed's effect, deliberately — there is none to pin.
	Seed int64
	// TombstoneRatio starts a background rebuild once the graph's tombstones
	// exceed this fraction of its live ids. Zero or less disables rebuilds.
	TombstoneRatio float64
	// MinTombstones is the fewest tombstones that can start a rebuild, so a
	// small index is not rebuilt for every handful of deletes.
	MinTombstones int

	// distance overrides the graph's distance function. Nil means
	// cosineNaNGuard, which is the only distance production ever uses; tests
	// install a counting distance here. Any distance set here must be
	// registered with hnsw.RegisterDistanceFunc in an init(), or Export fails.
	distance hnsw.DistanceFunc
	// afterSnapshot, when set, runs on the rebuild goroutine after the
	// snapshot is taken and before any graph work. Tests hold a rebuild here.
	afterSnapshot func(ctx context.Context)
	// beforeCatchUpRound, when set, runs on the rebuild goroutine, holding no
	// lock, before each off-lock catch-up round. Tests land writes here to
	// outpace the catch-up deterministically.
	beforeCatchUpRound func(round int)
}

// provisionalHNSWParams are PROVISIONAL: (M, EfSearch) = (16, 100), the values
// implementor3's probe measured, until task
// hnsw-parameters-from-real-vector-recall-and-production-wiring records values
// measured on real vectors. The library's default EfSearch of 20 is too low.
// The tombstone threshold is this task's measurement (see the measurement
// table in task vector-index-interface-and-coder-hnsw-wrapper).
var provisionalHNSWParams = hnswParams{
	M:              16,
	EfSearch:       100,
	Seed:           1,
	TombstoneRatio: 0.20,
	MinTombstones:  64,
}

// hnswLibraryVersion is the github.com/coder/hnsw version this package is built
// against: the `require` pseudo-version in go.mod, not the `replace` target that
// points at the vendored copy (scripts/check-hnsw-vendor.sh ties that copy to
// this version). It is an input of the graph fingerprint, so a graph saved by
// one library version is never loaded by another (ADR-014 decision 3).
// TestHNSWLibraryVersionMatchesGoMod keeps it equal to go.mod.
const hnswLibraryVersion = "v0.6.2-0.20260622133054-36cab6028fed"

// rebuildBatch is how many nodes a rebuild adds between cancellation checks.
const rebuildBatch = 256

// Catch-up bounds for a finished rebuild. Writes keep landing (and being
// logged) while a rebuild builds, and replaying an upsert is a full graph
// insertion: 4,700 writes logged during a 20k rebuild took 13.4 s to replay.
// So the rebuild replays the log in rounds WITHOUT the lock, onto its private
// graph, and takes the write lock to swap only once at most finalReplayMax
// mutations remain. If writes still outpace the catch-up after catchUpRounds
// rounds, the rebuild is ABANDONED: nothing is swapped, the old graph keeps
// serving, and the next rebuild waits out a backoff (rebuildBackoffGens). The
// write lock is therefore never held for more than finalReplayMax replays.
const (
	finalReplayMax = 64
	catchUpRounds  = 8
)

// The backoff after an abandoned rebuild is counted in WRITES (generations),
// not time: a rebuild is only ever triggered by a write, so a clock would add
// nothing but nondeterminism. After the n-th abandon in a row, no rebuild
// starts until the index has taken rebuildBackoffGens(n) more writes. It starts
// at MinTombstones (at least 1) and grows ×4 per consecutive abandon, the
// escalation internal/llm/retry.go uses, capped at maxRebuildBackoffGens. A
// successful swap resets it.
const maxRebuildBackoffGens = 1 << 24

func rebuildBackoffGens(minTombstones, abandons int) uint64 {
	b := uint64(max(minTombstones, 1))
	for i := 1; i < abandons && b < maxRebuildBackoffGens; i++ {
		b *= 4
	}
	return min(b, maxRebuildBackoffGens)
}

// distanceName is the name cosineNaNGuard is registered under. A saved graph
// records it, and a load resolves it, so renaming it makes every saved graph
// fail to Import. TestMain pins it.
const distanceName = "vp-cosine-nanguard"

// The library resolves a distance function by name when it imports a graph,
// and by code pointer when it exports one, through a package-level map that
// has no lock (coder/hnsw distance.go). Registering at run time would race every
// concurrent Export, so the one registration happens here, at package init, and
// nothing outside an init() calls RegisterDistanceFunc.
func init() {
	hnsw.RegisterDistanceFunc(distanceName, cosineNaNGuard)
}

// cosineNaNGuard is cosine distance with a guard on the score: a NaN distance
// (a zero vector on either side) becomes +Inf, which sorts last, and
// hnswIndex.Search drops every non-finite result. It must stay one named
// package-level function: the library tells distances apart by code pointer,
// and closures of one literal share a pointer.
func cosineNaNGuard(a, b []float32) float32 {
	d := 1 - vek32.CosineSimilarity(a, b)
	if math.IsNaN(float64(d)) {
		return float32(math.Inf(1))
	}
	return d
}

// hnswEntry is the current internal key and vector of one live id.
type hnswEntry struct {
	key uint64
	vec []float32
}

// hnswState is one graph and the maps that give it meaning. An index swaps its
// whole state at once when a rebuild finishes.
type hnswState struct {
	g *hnsw.Graph[uint64]
	// live maps each live id to its current key and vector.
	live map[string]hnswEntry
	// ids maps each live key back to its id. A key in the graph and absent
	// here is a tombstone.
	ids     map[uint64]string
	nextKey uint64
}

func newHNSWState(p hnswParams) hnswState {
	return hnswState{g: p.newGraph(), live: make(map[string]hnswEntry), ids: make(map[uint64]string)}
}

// put stores vec under id: a fresh key, after tombstoning the id's old key if
// it had one. Keys are never reused, so no key is ever added to the library
// twice (re-adding a key panics upstream, issue #15).
func (s *hnswState) put(id string, vec []float32) {
	if old, ok := s.live[id]; ok {
		delete(s.ids, old.key)
	}
	key := s.nextKey
	s.nextKey++
	s.g.Add(hnsw.MakeNode(key, vec))
	s.live[id] = hnswEntry{key: key, vec: vec}
	s.ids[key] = id
}

// remove tombstones id's key. Returns true if id was live.
func (s *hnswState) remove(id string) bool {
	old, ok := s.live[id]
	if !ok {
		return false
	}
	delete(s.live, id)
	delete(s.ids, old.key)
	return true
}

// tombstones returns the number of tombstoned keys in the graph.
func (s *hnswState) tombstones() int { return s.g.Len() - len(s.live) }

// mutation is one Insert (vec set) or Delete (vec nil) made while a rebuild
// was running, at generation gen.
type mutation struct {
	gen uint64
	id  string
	vec []float32
}

// hnswRebuild is one in-flight tombstone rebuild.
type hnswRebuild struct {
	// g0 is the index's generation when the snapshot was taken.
	g0     uint64
	cancel context.CancelFunc
	// done is closed when the rebuild goroutine has exited.
	done chan struct{}
	// log holds every mutation made after the snapshot, in generation order.
	log []mutation
}

// hnswIndex wraps a github.com/coder/hnsw graph so it meets the VectorIndex
// contract. Each rule answers a library behaviour at the pinned commit
// (ADR-014, decision 6):
//
//   - The library has no lock, and a search during an Add is a fatal concurrent
//     map access. Every library call on the live graph runs under mu.
//   - Library Delete leaves one-way edges behind; deleted keys come back in
//     results and searches panic. Nothing here calls it: a deleted id's key
//     becomes a TOMBSTONE, a key absent from ids, which Search filters out.
//   - Re-adding an existing key panics (upstream issue #15; PRs #23 and #25 are
//     unmerged). An upsert therefore tombstones the id's old key and adds the
//     vector under a fresh internal key, so no key is ever added twice.
//   - Nothing reaches the library unvalidated: dimensions, k, unusable vectors
//     and duplicate ids are all checked first (vector_index.go).
//
// Drop each workaround when its upstream fix merges.
//
// Tombstones cost memory and search breadth, so once they pass the threshold
// (hnswParams.TombstoneRatio) the index rebuilds a fresh graph from its live
// vectors. The rebuild runs on its own goroutine holding no lock; writes keep
// landing on the live graph meanwhile and are logged. The finished graph
// catches up on the log off-lock, then replays the small remainder under the
// write lock and is swapped in. Close cancels
// a running rebuild and waits for it, so no rebuild outlives its index.
type hnswIndex struct {
	mu     sync.RWMutex
	dims   int
	params hnswParams
	st     hnswState
	// gen counts mutations (Build, a storing or deleting Insert, a Delete).
	gen uint64
	// rebuild is the in-flight tombstone rebuild, or nil.
	rebuild *hnswRebuild
	closed  bool
	// rebuilders tracks every rebuild goroutine, including one Build has
	// cancelled and unhooked, so Close can wait for all of them.
	rebuilders sync.WaitGroup
	// abandons counts consecutive abandoned rebuilds, and abandonedAt is the
	// generation of the last one; together they set the backoff.
	abandons    int
	abandonedAt uint64

	// Counters for tests and the measurement table. None of them is read on
	// a production path.
	librarySearches   atomic.Int64 // library search calls
	rebuildsStarted   atomic.Int64 // rebuild goroutines started
	rebuildsRunning   atomic.Int64 // rebuild goroutines not yet exited
	swaps             atomic.Int64 // rebuilt graphs swapped in
	abandoned         atomic.Int64 // rebuilds abandoned because writes outpaced the catch-up
	lastReplayLen     atomic.Int64 // mutations replayed under the lock by the last swap
	lastReplayNanos   atomic.Int64 // time that replay held the lock
	lastCatchUpRounds atomic.Int64 // off-lock catch-up rounds before the last swap
}

// newHNSWIndex creates an empty HNSW index for the given dimensionality.
func newHNSWIndex(dims int, params hnswParams) *hnswIndex {
	return &hnswIndex{dims: dims, params: params, st: newHNSWState(params)}
}

// newGraph returns an empty library graph with every parameter applied
// explicitly, including the level generator's seed.
func (p hnswParams) newGraph() *hnsw.Graph[uint64] {
	g := hnsw.NewGraph[uint64]()
	g.M = p.M
	g.EfSearch = p.EfSearch
	g.Rng = rand.New(rand.NewSource(p.Seed))
	g.Distance = cosineNaNGuard
	if p.distance != nil {
		g.Distance = p.distance
	}
	return g
}

// Build replaces the index's contents with a freshly built graph. The graph is
// built before the lock is taken, so searches keep answering from the old
// contents meanwhile. A rebuild in flight is cancelled: its snapshot is of
// contents Build has just replaced. The rebuild backoff is reset too: it was
// earned by writes against the graph Build discards.
func (h *hnswIndex) Build(vectors [][]float32, ids []string) error {
	keep, err := dedupBatch(vectors, ids, h.dims)
	if err != nil {
		return err
	}

	st := hnswState{
		g:       h.params.newGraph(),
		live:    make(map[string]hnswEntry, len(keep)),
		ids:     make(map[uint64]string, len(keep)),
		nextKey: uint64(len(keep)),
	}
	nodes := make([]hnsw.Node[uint64], len(keep))
	for n, i := range keep {
		key := uint64(n)
		vec := slices.Clone(vectors[i])
		nodes[n] = hnsw.MakeNode(key, vec)
		st.live[ids[i]] = hnswEntry{key: key, vec: vec}
		st.ids[key] = ids[i]
	}
	st.g.Add(nodes...)

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rebuild != nil {
		h.rebuild.cancel()
		h.rebuild = nil
	}
	h.abandons = 0
	h.abandonedAt = 0
	h.st = st
	h.gen++
	return nil
}

// Insert adds or replaces one vector. Replacing tombstones the id's old key and
// adds the vector under a new one; an identical vector changes nothing; an
// unusable vector deletes the id.
func (h *hnswIndex) Insert(id string, vector []float32) error {
	ok, err := checkVector(vector, h.dims)
	if err != nil {
		return err
	}
	if !ok {
		slog.Warn("vector index: skipping an unusable vector (zero, non-finite, or a norm out of range); deleting any stored value", "id", id)
		h.Delete(id)
		return nil
	}
	vector = slices.Clone(vector)

	h.mu.Lock()
	defer h.mu.Unlock()

	if old, ok := h.st.live[id]; ok {
		if equalVectors(old.vec, vector) {
			return nil
		}
	}
	h.st.put(id, vector)
	h.gen++
	h.logLocked(id, vector)
	h.maybeRebuildLocked()
	return nil
}

// Search returns the k nearest live neighbours of query, nearest first.
//
// The graph still holds tombstoned keys, so asking it for exactly k could
// return fewer than k live results. Asking for k plus the tombstone count
// instead would raise the library's search breadth with every tombstone, and
// query cost with it. So Search asks for max(k, EfSearch), never more than the
// graph holds, drops tombstones and non-finite distances, and doubles the
// request only while fewer than k live results remain and the graph has more
// to give.
func (h *hnswIndex) Search(query []float32, k int) ([]VectorResult, error) {
	if err := checkQuery(query, k, h.dims); err != nil {
		return nil, err
	}
	if k == 0 {
		return nil, nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.st.live) == 0 {
		return nil, nil
	}
	total := h.st.g.Len()
	want := min(max(k, h.st.g.EfSearch), total)
	var out []VectorResult
	for {
		out = out[:0]
		h.librarySearches.Add(1)
		for _, r := range h.st.g.SearchWithDistance(query, want) {
			id, live := h.st.ids[r.Key]
			if !live {
				continue
			}
			d := float64(r.Distance)
			if math.IsNaN(d) || math.IsInf(d, 0) {
				continue
			}
			out = append(out, VectorResult{ID: id, Distance: r.Distance})
		}
		if len(out) >= k || want >= total {
			break
		}
		want = min(want*2, total)
	}

	slices.SortStableFunc(out, func(a, b VectorResult) int {
		return cmp.Compare(a.Distance, b.Distance)
	})
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// Delete tombstones id's key. The key stays in the graph, unreachable through
// Search. Returns true if id was live.
func (h *hnswIndex) Delete(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.st.remove(id) {
		return false
	}
	h.gen++
	h.logLocked(id, nil)
	h.maybeRebuildLocked()
	return true
}

// Len returns the number of live ids. Tombstones are not counted.
func (h *hnswIndex) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.st.live)
}

// Close cancels a running rebuild and returns once every rebuild goroutine has
// exited. A cancelled rebuild never swaps, and no rebuild starts after Close.
// Close never holds mu while it waits, so a caller that closes an index it has
// already unpublished stalls no search.
func (h *hnswIndex) Close() error {
	h.mu.Lock()
	h.closed = true
	if h.rebuild != nil {
		h.rebuild.cancel()
		h.rebuild = nil
	}
	h.mu.Unlock()
	h.rebuilders.Wait()
	return nil
}

// logLocked records a mutation for the in-flight rebuild to replay. The caller
// holds mu for writing and has already bumped gen.
func (h *hnswIndex) logLocked(id string, vec []float32) {
	if h.rebuild != nil {
		h.rebuild.log = append(h.rebuild.log, mutation{gen: h.gen, id: id, vec: vec})
	}
}

// maybeRebuildLocked starts a rebuild when the tombstones have passed the
// threshold and none is running. It returns at once: the rebuild runs on its own
// goroutine, so the caller, which may itself hold the engine's lock, is never
// stalled by it. The caller holds mu for writing.
func (h *hnswIndex) maybeRebuildLocked() {
	p := h.params
	if h.rebuild != nil || h.closed || p.TombstoneRatio <= 0 {
		return
	}
	tomb := h.st.tombstones()
	if tomb < p.MinTombstones || float64(tomb) <= p.TombstoneRatio*float64(len(h.st.live)) {
		return
	}
	if h.abandons > 0 && h.gen-h.abandonedAt < rebuildBackoffGens(p.MinTombstones, h.abandons) {
		return
	}

	// Vectors are copies the index owns and never mutates, so the snapshot
	// shares them. Ordering by key re-adds them in their original order.
	// COST: this copy and sort are O(n log n) and run under h.mu, and the
	// engine's callers (RemoveDrawer, IndexDrawers) also hold e.mu here. At
	// today's sizes that is milliseconds; revisit for 100k-vector indexes
	// (recorded in task hnsw-parameters-from-real-vector-recall-and-production-wiring).
	snap := make([]rebuildItem, 0, len(h.st.live))
	for id, e := range h.st.live {
		snap = append(snap, rebuildItem{key: e.key, id: id, vec: e.vec})
	}
	slices.SortFunc(snap, func(a, b rebuildItem) int { return cmp.Compare(a.key, b.key) })

	ctx, cancel := context.WithCancel(context.Background())
	b := &hnswRebuild{g0: h.gen, cancel: cancel, done: make(chan struct{})}
	h.rebuild = b
	h.rebuildsStarted.Add(1)
	h.rebuildsRunning.Add(1)
	h.rebuilders.Add(1)
	go h.runRebuild(ctx, b, snap)
}

// rebuildItem is one live id in a rebuild's snapshot.
type rebuildItem struct {
	key uint64
	id  string
	vec []float32
}

// runRebuild builds a fresh graph from snap, holding no lock, then replays the
// mutations logged since the snapshot and swaps the result in under the write
// lock. A cancelled or superseded rebuild exits without swapping.
func (h *hnswIndex) runRebuild(ctx context.Context, b *hnswRebuild, snap []rebuildItem) {
	defer h.rebuilders.Done()
	defer close(b.done)
	defer h.rebuildsRunning.Add(-1)
	defer b.cancel()

	if hook := h.params.afterSnapshot; hook != nil {
		hook(ctx)
	}

	st := hnswState{
		g:    h.params.newGraph(),
		live: make(map[string]hnswEntry, len(snap)),
		ids:  make(map[uint64]string, len(snap)),
	}
	for start := 0; start < len(snap); start += rebuildBatch {
		if ctx.Err() != nil {
			return
		}
		batch := snap[start:min(start+rebuildBatch, len(snap))]
		nodes := make([]hnsw.Node[uint64], len(batch))
		for i, it := range batch {
			key := st.nextKey
			st.nextKey++
			nodes[i] = hnsw.MakeNode(key, it.vec)
			st.live[it.id] = hnswEntry{key: key, vec: it.vec}
			st.ids[key] = it.id
		}
		st.g.Add(nodes...)
	}

	// Catch up off-lock, round by round, until the unreplayed remainder is
	// small; then replay that remainder and swap under the write lock.
	applied := 0
	for round := 0; ; round++ {
		h.mu.Lock()
		if ctx.Err() != nil || h.rebuild != b {
			h.mu.Unlock()
			return
		}
		pending := b.log[applied:]
		if len(pending) > finalReplayMax && round == catchUpRounds {
			// Writes are outpacing the catch-up. Swapping would hold the
			// lock for the whole remainder, so give up and back off.
			h.abandons++
			h.abandonedAt = h.gen
			h.rebuild = nil
			h.abandoned.Add(1)
			h.mu.Unlock()
			slog.Warn("hnsw: tombstone rebuild abandoned; writes outpaced its catch-up",
				"unreplayed", len(pending), "rounds", round,
				"backoff_writes", rebuildBackoffGens(h.params.MinTombstones, h.abandons))
			return
		}
		if len(pending) <= finalReplayMax {
			start := time.Now()
			replayed := replayLog(&st, pending, b.g0)
			h.lastReplayNanos.Store(int64(time.Since(start)))
			h.lastReplayLen.Store(int64(replayed))
			h.lastCatchUpRounds.Store(int64(round))
			h.st = st
			h.rebuild = nil
			h.abandons = 0
			h.swaps.Add(1)
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		if hook := h.params.beforeCatchUpRound; hook != nil {
			hook(round)
		}
		// pending's elements are never rewritten: appends after this point
		// land beyond its length, so reading it unlocked is safe.
		for start := 0; start < len(pending); start += rebuildBatch {
			if ctx.Err() != nil {
				return
			}
			replayLog(&st, pending[start:min(start+rebuildBatch, len(pending))], b.g0)
		}
		applied += len(pending)
	}
}

// replayLog applies the logged mutations newer than g0 to st, in order, and
// returns how many it applied.
func replayLog(st *hnswState, log []mutation, g0 uint64) int {
	n := 0
	for _, m := range log {
		if m.gen <= g0 {
			continue
		}
		if m.vec == nil {
			st.remove(m.id)
		} else {
			st.put(m.id, m.vec)
		}
		n++
	}
	return n
}
