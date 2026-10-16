// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func (c *countingEmbedder) batchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.batchCalls
}

// --- site (d): the search-path corpus embed honours the cooldown -------------

// TestSearchPathColdEmbedThrottled (U4 B1 / site d): a small cold project whose
// miss count is WITHIN the one-batch budget — which would otherwise embed
// inline on the search path — degrades instead while the cooldown is active. It
// embeds nothing (EmbedBatch is never called), serves the degraded
// *IndexNotBuiltError, and routes the durable want through the launcher.
func TestSearchPathColdEmbedThrottled(t *testing.T) {
	v := testVault(t)
	spy := &launchSpy{}
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32},
		WithRebuildLauncher(spy.launcher()))
	eng.indexRunCooldown = time.Hour
	t.Cleanup(func() { eng.Close() })
	for i := 0; i < 5; i++ { // 5 << 32: without the cooldown this builds inline
		addDrawer(t, v, "proj", "wing", "room", fmt.Sprintf("distinct chunk number %d", i), "facts")
	}
	// Arm the cooldown: a run completed just now.
	if err := indexstore.WriteRunCompleted(v, time.Now()); err != nil {
		t.Fatal(err)
	}

	_, err := eng.Search(context.Background(), "query", SearchFilters{Project: "proj"})
	var nb *IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("under an active cooldown a cold search must degrade, got %v", err)
	}
	if emb.batchCount() != 0 {
		t.Fatalf("the search path must NOT embed the corpus under an active cooldown; EmbedBatch ran %d times", emb.batchCount())
	}
	if got := spy.projects(); len(got) != 1 || got[0] != "proj" {
		t.Fatalf("the throttled cold search must route the want through the launcher, launched=%v", got)
	}
}

// TestSearchPathWarmBuildNotThrottled (U4 site d, the stats.Embedded>0 guard): a
// project whose vectors are already cached rebuilds with ZERO misses, so it
// installs inline even under an active cooldown — a warm search is never
// degraded, and the launcher is not called.
func TestSearchPathWarmBuildNotThrottled(t *testing.T) {
	v := testVault(t)
	for i := 0; i < 5; i++ {
		addDrawer(t, v, "proj", "wing", "room", fmt.Sprintf("distinct chunk number %d", i), "facts")
	}
	// Engine A warms the per-vault embed cache (cooldown disabled).
	warm := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32})
	warm.indexRunCooldown = 0
	if _, err := warm.RebuildExplicit(context.Background(), "proj"); err != nil {
		t.Fatalf("warm rebuild: %v", err)
	}
	warm.Close()

	// Engine B is fresh: no in-memory index, no graph on disk (brute project), so
	// its first search rebuilds — but every vector is a cache hit (zero misses).
	spy := &launchSpy{}
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32},
		WithRebuildLauncher(spy.launcher()))
	eng.indexRunCooldown = time.Hour
	t.Cleanup(func() { eng.Close() })
	if err := indexstore.WriteRunCompleted(v, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Search(context.Background(), "distinct chunk number 1", SearchFilters{Project: "proj"}); err != nil {
		t.Fatalf("a warm (zero-miss) build must install even under an active cooldown, got %v", err)
	}
	if emb.batchCount() != 0 {
		t.Fatalf("a warm build embeds nothing; EmbedBatch ran %d times", emb.batchCount())
	}
	if got := spy.projects(); len(got) != 0 {
		t.Fatalf("a warm build must not degrade or launch a rebuild, launched=%v", got)
	}
}

// TestExplicitRebuildNotThrottled (U4 Test 1): an explicit RebuildExplicit
// (refuse=false) under an active cooldown STILL embeds and installs — the
// cooldown check lives INSIDE the `if refuse {` block, so an operator / detached
// explicit rebuild is never throttled (OQ-4 / ADR-016 decision 1).
func TestExplicitRebuildNotThrottled(t *testing.T) {
	v := testVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32})
	eng.indexRunCooldown = time.Hour
	t.Cleanup(func() { eng.Close() })
	for i := 0; i < 5; i++ {
		addDrawer(t, v, "proj", "wing", "room", fmt.Sprintf("distinct chunk number %d", i), "facts")
	}
	if err := indexstore.WriteRunCompleted(v, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.RebuildExplicit(context.Background(), "proj"); err != nil {
		t.Fatalf("RebuildExplicit must not be throttled by the cooldown, got %v", err)
	}
	if emb.batchCount() == 0 {
		t.Fatal("an explicit rebuild under a cooldown must still embed the corpus (refuse=false is not throttled)")
	}
	if _, ok := installedIndex(t, eng, "proj").(*bruteIndex); !ok {
		t.Fatalf("explicit rebuild must install an index, got %T", installedIndex(t, eng, "proj"))
	}
}

// --- site (c): the in-session converter honours the cooldown -----------------

// TestConvertDefersWithinCooldown (U4 site c / NB-3): a convert triggered while
// the cooldown is active defers — it never tries the run lock, leaves the brute
// index in place, and records NO backoff (convertAbandon must not fire, since a
// cooldown skip never advances the project's Gen).
func TestConvertDefersWithinCooldown(t *testing.T) {
	withThresholds(t, 4, 2)
	v := testVault(t)
	const project = "u4conv"
	e := NewEngine(embedder.NewMock(384), v, selCfg(), WithBackgroundConverter())
	e.indexRunCooldown = time.Hour
	t.Cleanup(func() { e.Close() })
	seedBruteProject(t, v, e, project, 5) // builds brute BEFORE the cooldown is armed

	if err := indexstore.WriteRunCompleted(v, time.Now()); err != nil {
		t.Fatal(err)
	}
	var tries int
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		if ev == indexstore.RunTry {
			tries++
		}
	})
	convertSync(e, project)
	restore()

	if tries != 0 {
		t.Fatalf("a cooldown-deferred convert must not try the run lock, got %d RunTry", tries)
	}
	if _, ok := installedIndex(t, e, project).(*bruteIndex); !ok {
		t.Fatalf("a deferred convert must leave the brute index in place, got %T", installedIndex(t, e, project))
	}
	e.convertMu.Lock()
	abandons := e.convertBackoff[project].abandons
	e.convertMu.Unlock()
	if abandons != 0 {
		t.Fatalf("a cooldown-deferred convert must not record a backoff (NB-3), got abandons=%d", abandons)
	}
}

// TestConvertRefiresAfterCooldown (U4 site c re-fire): with no active cooldown
// the same trigger converts — it DOES take the run lock and installs HNSW, so
// the deferral is a skip, not a permanent drop.
func TestConvertRefiresAfterCooldown(t *testing.T) {
	withThresholds(t, 4, 2)
	withConvertBounds(t, 64, 8)
	v := testVault(t)
	const project = "u4refire"
	e := NewEngine(embedder.NewMock(384), v, selCfg(), WithBackgroundConverter())
	e.indexRunCooldown = time.Hour
	t.Cleanup(func() { e.Close() })
	seedBruteProject(t, v, e, project, 5)
	// No cooldown marker written -> the window is inactive.

	var tries int
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		if ev == indexstore.RunTry {
			tries++
		}
	})
	convertSync(e, project)
	restore()

	if tries == 0 {
		t.Fatal("with no active cooldown the convert must take the run lock (a RunTry event)")
	}
	if _, ok := installedIndex(t, e, project).(*hnswIndex); !ok {
		t.Fatalf("the convert must install an HNSW index once the cooldown is inactive, got %T", installedIndex(t, e, project))
	}
}
