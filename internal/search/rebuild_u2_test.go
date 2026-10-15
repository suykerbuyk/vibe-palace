// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

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

// --- the generation-witnessed disk load (ADR-016 decision 4) ----------------

// seedMeta publishes placeholder metadata for ids, so the witnessed load's
// "metadata present for every graph id" guard passes.
func (e *Engine) seedMeta(ids ...string) {
	e.mu.Lock()
	for _, id := range ids {
		e.metadata[id] = drawerMeta{Project: "graphproj", Content: "x " + id}
	}
	e.mu.Unlock()
}

func graphIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%06d", i)
	}
	return ids
}

// TestWitnessedLoadInstallsDiskGraph: with a graph on disk whose fingerprint
// matches and whose ids this engine already has metadata for, the witnessed load
// installs it off the commit lock and remembers the counter it matches.
func TestWitnessedLoadInstallsDiskGraph(t *testing.T) {
	v, project := newGraphVault(t) // "graphproj"
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	h := buildHNSW(t, 384, 5, provisionalHNSWParams)
	saveBuilt(t, v, project, h) // writes hnsw.idx and bumps the generation
	eng.seedMeta(graphIDs(5)...)

	g1, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if !eng.tryWitnessedLoad(project, g1) {
		t.Fatal("witnessed load did not install a consistent on-disk graph")
	}
	eng.mu.RLock()
	idx, ok := eng.indexes[project]
	eng.mu.RUnlock()
	if !ok || idx == nil || idx.Len() != 5 {
		t.Fatalf("installed index missing or wrong size: ok=%v len=%d", ok, func() int {
			if idx == nil {
				return -1
			}
			return idx.Len()
		}())
	}
	eng.buildMu.Lock()
	lg, loaded := eng.loaded[project]
	eng.buildMu.Unlock()
	if !loaded || lg != g1 {
		t.Fatalf("loaded counter = (%v, %v), want %v", lg, loaded, g1)
	}
}

// TestWitnessedLoadRefusesOnGenMove: if the counter moved between the caller's
// pre-load read and the post-load re-read, the graph may be mid-write or paired
// with a different store — the load installs nothing and retries next search.
func TestWitnessedLoadRefusesOnGenMove(t *testing.T) {
	v, project := newGraphVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	h := buildHNSW(t, 384, 5, provisionalHNSWParams)
	saveBuilt(t, v, project, h)
	eng.seedMeta(graphIDs(5)...)

	cur, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-load counter that does not equal the current one models a write that
	// landed during the load: the post-load re-read differs, so the install is
	// refused.
	stale := indexstore.Gen{Gen: cur.Gen + 100, Epoch: cur.Epoch}
	if eng.tryWitnessedLoad(project, stale) {
		t.Fatal("witnessed load installed a graph despite a moved generation")
	}
	eng.mu.RLock()
	_, ok := eng.indexes[project]
	eng.mu.RUnlock()
	if ok {
		t.Fatal("a refused witnessed load must install nothing")
	}
}

// TestWitnessedLoadDefersWhenMetadataMissing: a graph whose ids this engine has
// no metadata for is left to the full rebuild (installing it would return hits
// with no renderable row), so the witnessed load declines.
func TestWitnessedLoadDefersWhenMetadataMissing(t *testing.T) {
	v, project := newGraphVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	h := buildHNSW(t, 384, 5, provisionalHNSWParams)
	saveBuilt(t, v, project, h)
	// No seedMeta: the engine holds metadata for none of the graph's ids.

	g1, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if eng.tryWitnessedLoad(project, g1) {
		t.Fatal("witnessed load installed a graph with no metadata for its ids")
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
	if eng.tryWitnessedLoad(project, g1) {
		t.Fatal("witnessed load claimed to install from a project with no graph")
	}
}
