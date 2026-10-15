// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// launchSpy records the projects a RebuildLauncher was asked to ensure.
type launchSpy struct {
	mu   sync.Mutex
	seen []string
}

func (s *launchSpy) launcher() RebuildLauncher {
	return func(project string) {
		s.mu.Lock()
		s.seen = append(s.seen, project)
		s.mu.Unlock()
	}
}

func (s *launchSpy) projects() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// commitGraphFromDrawers adds n distinct drawers to project, then writes an
// on-disk hnsw.idx — as a detached rebuild / heal would — built from the first
// `keep` drawers' (id, mock-embedded-vector) pairs PLUS any extraIDs (fabricated
// ids with a vector, to model a graph still holding content the corpus no longer
// has). keep<n models a heal / unusable-vector graph that correctly omits a
// walked id; extraIDs models a genuinely stale graph. The engine under test keeps
// its own cold embed cache, so a later search must consume this graph without
// re-embedding. Returns the drawer ids.
func commitGraphFromDrawers(t *testing.T, v *storage.Vault, project string, n, keep int, extraIDs ...string) []string {
	t.Helper()
	mock := embedder.NewMock(384)
	var drawerIDs, ids []string
	var vecs [][]float32
	for i := 0; i < n; i++ {
		d := addDrawer(t, v, project, "wing", "room", fmt.Sprintf("distinct chunk number %d", i), "facts")
		drawerIDs = append(drawerIDs, d.ID)
		if i < keep {
			vec, err := mock.Embed(context.Background(), d.Content)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, d.ID)
			vecs = append(vecs, vec)
		}
	}
	for _, gid := range extraIDs {
		vec, err := mock.Embed(context.Background(), "ghost "+gid)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, gid)
		vecs = append(vecs, vec)
	}
	h := newHNSWIndex(384, provisionalHNSWParams)
	if err := h.Build(vecs, ids); err != nil {
		t.Fatal(err)
	}
	saveBuilt(t, v, project, h) // writes hnsw.idx and bumps the generation
	return drawerIDs
}

// commitDrawerGraph is commitGraphFromDrawers with a full, exactly-matching graph.
func commitDrawerGraph(t *testing.T, v *storage.Vault, project string, n int) []string {
	t.Helper()
	return commitGraphFromDrawers(t, v, project, n, n)
}

// TestColdDegradeEnsuresDetachedRebuild (U2 / ADR-016 decision 4): a cold corpus
// whose miss count exceeds one embed batch degrades (no inline corpus embed) and
// the engine ensures the detached rebuild via the injected launcher. The engine
// itself never try-locks the run lock; it only calls the seam.
func TestColdDegradeEnsuresDetachedRebuild(t *testing.T) {
	v := testVault(t)
	spy := &launchSpy{}
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32}, WithRebuildLauncher(spy.launcher()))
	t.Cleanup(func() { eng.Close() })
	for i := 0; i < 40; i++ {
		addDrawer(t, v, "proj", "wing", "room", fmt.Sprintf("distinct chunk number %d", i), "facts")
	}

	_, err := eng.Search(context.Background(), "query", SearchFilters{Project: "proj"})
	var nb *IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("want a degraded *IndexNotBuiltError, got %v", err)
	}
	if got := spy.projects(); len(got) != 1 || got[0] != "proj" {
		t.Fatalf("cold degrade did not ensure the detached rebuild for proj: launched=%v", got)
	}
}

// TestBuiltProjectDoesNotLaunch: a tiny project that builds fully within the one-
// batch budget must NOT spawn a detached rebuild — the launcher is only for a
// corpus that could not build inline.
func TestBuiltProjectDoesNotLaunch(t *testing.T) {
	v := testVault(t)
	spy := &launchSpy{}
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32}, WithRebuildLauncher(spy.launcher()))
	t.Cleanup(func() { eng.Close() })
	addDrawer(t, v, "proj", "wing", "room", "one small chunk of content", "facts")

	if _, err := eng.Search(context.Background(), "content", SearchFilters{Project: "proj"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := spy.projects(); len(got) != 0 {
		t.Fatalf("a project that built inline still launched a detached rebuild: %v", got)
	}
}

// TestColdEngineConsumesCommittedGraph (U2 / ADR-016 decision 4, B1): the load-
// bearing case. A long-lived engine with a COLD embed cache and a corpus larger
// than one embed batch — the exact case the search-path rebuild refuses — must,
// on the NEXT search after a detached rebuild committed hnsw.idx and bumped Gen,
// CONSUME that graph and serve real HNSW hits, WITHOUT an inline corpus embed.
// Driven through Search, not by calling tryWitnessedLoad directly.
func TestColdEngineConsumesCommittedGraph(t *testing.T) {
	v := testVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32})
	t.Cleanup(func() { eng.Close() })

	commitDrawerGraph(t, v, "proj", 40) // 40 > one 32-batch: the rebuild would refuse

	results, err := eng.Search(context.Background(), "distinct chunk number 7", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatalf("search did not consume the committed graph: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("cold engine returned no results — the committed graph was not consumed (B1 regression)")
	}
	if _, bc := emb.counts(); bc != 0 {
		t.Fatalf("the corpus was embedded inline (%d EmbedBatch calls); the consume must be a disk load, not a rebuild", bc)
	}
	if !eng.HasIndex("proj") {
		t.Fatal("the project was not marked built after the witnessed consume")
	}
}

// TestWitnessedLoadWitnessCatchesInterleavedWrite (U2, B2a): a writer that bumps
// the generation BETWEEN the pre-load read and the re-read must make the load
// refuse — the loaded graph may be paired with a different store snapshot. This
// interleaves a real write mid-call (not a fabricated g1).
func TestWitnessedLoadWitnessCatchesInterleavedWrite(t *testing.T) {
	v := testVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	commitDrawerGraph(t, v, "proj", 5)
	g1, err := indexstore.ReadGeneration(v, "proj")
	if err != nil {
		t.Fatal(err)
	}

	// A writer lands during the load (after the walk, before the re-read): it
	// bumps the generation, so the re-read differs from g1 and the install is
	// refused.
	var once sync.Once
	witnessInterleaveHook = func() {
		once.Do(func() {
			tx, lerr := indexstore.Lock(context.Background(), v, "proj", indexstore.NoTimeout)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if werr := tx.WriteGraph([]byte("x")); werr != nil { // any write; Commit bumps Gen
				_ = tx.Release()
				t.Fatal(werr)
			}
			if cerr := tx.Commit(); cerr != nil {
				t.Fatal(cerr)
			}
		})
	}
	t.Cleanup(func() { witnessInterleaveHook = nil })

	if eng.tryWitnessedLoad(context.Background(), "proj", g1) {
		t.Fatal("witnessed load installed a graph although a write interleaved between the two generation reads")
	}
	eng.mu.RLock()
	_, inMem := eng.indexes["proj"]
	eng.mu.RUnlock()
	if inMem {
		t.Fatal("a refused witnessed load must install nothing")
	}
}

// TestColdEngineConsumesSubsetGraph (U2, N1): a HEAL-style committed graph whose
// live-id set is a STRICT SUBSET of the current corpus — one chunk the writer
// omitted (a heal's cache miss, an unusable vector) — must still be CONSUMED by a
// cold long-lived engine through Search, serving HNSW hits for the ids it has.
// A mutant that kept the set-EQUALITY witness refuses this and never consumes.
func TestColdEngineConsumesSubsetGraph(t *testing.T) {
	v := testVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32})
	t.Cleanup(func() { eng.Close() })

	// 40 drawers in the corpus (> one 32-batch), but the committed graph holds
	// only 39 of them — the 40th is an id the writer legitimately omitted.
	commitGraphFromDrawers(t, v, "proj", 40, 39)

	results, err := eng.Search(context.Background(), "distinct chunk number 5", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatalf("cold engine did not consume a correctly-subset graph: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results — a heal/subset graph was refused instead of consumed (N1 regression)")
	}
	if _, bc := emb.counts(); bc != 0 {
		t.Fatalf("the corpus was embedded inline (%d EmbedBatch calls); the consume must be a disk load", bc)
	}
	if !eng.HasIndex("proj") {
		t.Fatal("the project was not marked built after consuming the subset graph")
	}
}

// TestWitnessedLoadConsumesGraphMissingAWalkedID (U2, N1, unusable-vector shape):
// a graph the writer built without one walked id (dedupBatch dropped an unusable
// vector) is installed, not refused. Driven at the witness directly.
func TestWitnessedLoadConsumesGraphMissingAWalkedID(t *testing.T) {
	v := testVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	commitGraphFromDrawers(t, v, "proj", 5, 4) // the 5th id is in the corpus, not the graph
	g1, err := indexstore.ReadGeneration(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if !eng.tryWitnessedLoad(context.Background(), "proj", g1) {
		t.Fatal("witnessed load refused a graph that correctly omitted a walked id (N1)")
	}
	eng.mu.RLock()
	idx, inMem := eng.indexes["proj"]
	eng.mu.RUnlock()
	if !inMem || idx == nil || idx.Len() != 4 {
		t.Fatalf("installed index missing or wrong size: inMem=%v", inMem)
	}
	eng.buildMu.Lock()
	_, loaded := eng.loaded["proj"]
	eng.buildMu.Unlock()
	if !loaded {
		t.Fatal("a consumed subset graph must record e.loaded")
	}
}

// TestWitnessedLoadRefusesTrueStaleGraph (U2, B2b true positive): a graph that
// serves an id the corpus NO LONGER has — removed or in-place-edited content, the
// stale direction — must be REFUSED and nothing installed. It refuses because the
// GRAPH holds a removed id, not because the walk has an extra one.
func TestWitnessedLoadRefusesTrueStaleGraph(t *testing.T) {
	v := testVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	// The graph holds all 5 drawer ids PLUS a ghost id that is not (any longer) in
	// the corpus — content removed or edited after the build.
	commitGraphFromDrawers(t, v, "proj", 5, 5, "deadbeefdeadbeef")
	g1, err := indexstore.ReadGeneration(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if eng.tryWitnessedLoad(context.Background(), "proj", g1) {
		t.Fatal("witnessed load installed a graph that serves content the corpus no longer has (stale)")
	}
	eng.mu.RLock()
	_, inMem := eng.indexes["proj"]
	eng.mu.RUnlock()
	eng.buildMu.Lock()
	_, pinned := eng.loaded["proj"]
	eng.buildMu.Unlock()
	if inMem || pinned {
		t.Fatalf("a refused stale graph must install nothing and not pin: inMem=%v pinned=%v", inMem, pinned)
	}
}

// TestWitnessedLoadNoGraphStaysNotesOnly (ADR-016 decision 4): a never-built
// project has no hnsw.idx, so the witnessed load installs nothing and the
// degraded answer stays as it is until the detached rebuild commits.
func TestWitnessedLoadNoGraphStaysNotesOnly(t *testing.T) {
	v, project := newGraphVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	g1, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if eng.tryWitnessedLoad(context.Background(), project, g1) {
		t.Fatal("witnessed load claimed to install from a project with no graph")
	}
}
