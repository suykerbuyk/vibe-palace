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

// commitDrawerGraph adds n distinct drawers to project and writes an on-disk
// hnsw.idx built from their (id, mock-embedded-vector) pairs — exactly what a
// detached `vp index rebuild` would commit — bumping the generation. The engine
// under test keeps its own cold embed cache, so a later search must consume this
// graph without re-embedding. Returns the drawer ids.
func commitDrawerGraph(t *testing.T, v *storage.Vault, project string, n int) []string {
	t.Helper()
	mock := embedder.NewMock(384)
	var ids []string
	var vecs [][]float32
	for i := 0; i < n; i++ {
		d := addDrawer(t, v, project, "wing", "room", fmt.Sprintf("distinct chunk number %d", i), "facts")
		vec, err := mock.Embed(context.Background(), d.Content)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
		vecs = append(vecs, vec)
	}
	h := newHNSWIndex(384, provisionalHNSWParams)
	if err := h.Build(vecs, ids); err != nil {
		t.Fatal(err)
	}
	saveBuilt(t, v, project, h) // writes hnsw.idx and bumps the generation
	return ids
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

// TestWitnessedLoadDefersOnCorpusDrift (U2, B2b): a graph that does not cover the
// current corpus — a drawer/note added after the build, the shape that does not
// move Gen's pairing — must NOT be installed or recorded as current. Recording
// e.loaded here would pin the new content invisible, suppressing the rebuild
// that would index it.
func TestWitnessedLoadDefersOnCorpusDrift(t *testing.T) {
	v := testVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	commitDrawerGraph(t, v, "proj", 5)
	// New content the committed graph does not hold (its id is not in the graph).
	addDrawer(t, v, "proj", "wing", "room", "a chunk added after the build committed", "facts")

	g1, err := indexstore.ReadGeneration(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if eng.tryWitnessedLoad(context.Background(), "proj", g1) {
		t.Fatal("witnessed load installed a graph stale relative to the corpus")
	}
	eng.buildMu.Lock()
	_, pinned := eng.loaded["proj"]
	eng.buildMu.Unlock()
	if pinned {
		t.Fatal("corpus drift must not record e.loaded — it would pin the new content invisible until some other write bumps Gen")
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
