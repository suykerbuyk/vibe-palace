// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// countingEmbedder wraps the mock embedder to count calls, and can be made to
// fail EmbedBatch (which is the only embedding a Rebuild performs).
type countingEmbedder struct {
	inner     *embedder.MockEmbedder
	batchFail bool

	mu         sync.Mutex
	embedCalls int
	batchCalls int
	batchSizes []int
}

func newCountingEmbedder(dims int) *countingEmbedder {
	return &countingEmbedder{inner: embedder.NewMock(dims)}
}

func (c *countingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	c.mu.Lock()
	c.embedCalls++
	c.mu.Unlock()
	return c.inner.Embed(ctx, text)
}

func (c *countingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	c.mu.Lock()
	c.batchCalls++
	c.batchSizes = append(c.batchSizes, len(texts))
	fail := c.batchFail
	c.mu.Unlock()
	if fail {
		return nil, errors.New("embed batch exploded")
	}
	return c.inner.EmbedBatch(ctx, texts)
}

func (c *countingEmbedder) Dimensions() (int, error) { return c.inner.Dimensions() }
func (c *countingEmbedder) Close() error             { return c.inner.Close() }

func (c *countingEmbedder) counts() (embedCalls, batchCalls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.embedCalls, c.batchCalls
}

// countingEngine builds an engine over a fresh vault with a call-counting embedder.
func countingEngine(t *testing.T, cfg storage.Config) (*Engine, *storage.Vault, *countingEmbedder) {
	t.Helper()
	v := testVault(t)
	if cfg.SearchDefaultLimit == 0 {
		cfg.SearchDefaultLimit = 10
	}
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, cfg)
	t.Cleanup(func() { eng.Close() })
	return eng, v, emb
}

func testEngine(t *testing.T) (*Engine, *storage.Vault) {
	t.Helper()
	v := testVault(t)
	cfg := storage.Config{
		SearchDefaultLimit: 10,
		BoostWing:          0.12,
		BoostHall:          0.24,
		BoostRoom:          0.34,
	}
	emb := embedder.NewMock(384)
	eng := NewEngine(emb, v, cfg)
	t.Cleanup(func() { eng.Close() })
	return eng, v
}

func addDrawer(t *testing.T, v *storage.Vault, project, wing, room, content, hall string) storage.Drawer {
	t.Helper()
	d := storage.Drawer{
		Content:    content,
		Hall:       hall,
		SourceType: "session",
		SourceRef:  "",
		FiledAt:    "2026-04-07T10:00:00Z",
	}
	if err := v.AppendDrawer(project, wing, room, d); err != nil {
		t.Fatalf("AppendDrawer: %v", err)
	}
	// Re-read to get the generated ID.
	drawers, err := v.ListDrawers(project, wing, room)
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range drawers {
		if stored.Content == content {
			return stored
		}
	}
	t.Fatal("drawer not found after append")
	return storage.Drawer{}
}

func TestSearchBasic(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d := addDrawer(t, v, "proj", "wing-a", "room-1", "Go concurrency patterns", "facts")
	if err := eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", d); err != nil {
		t.Fatal(err)
	}

	results, err := eng.Search(ctx, "concurrency", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].DrawerID != d.ID {
		t.Errorf("got drawer %q, want %q", results[0].DrawerID, d.ID)
	}
	if results[0].Score <= 0 {
		t.Error("score should be positive")
	}
}

func TestSearchFilters(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d1 := addDrawer(t, v, "proj", "wing-a", "room-1", "alpha content", "facts")
	d2 := addDrawer(t, v, "proj", "wing-b", "room-1", "beta content", "facts")

	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", d1)
	_ = eng.IndexDrawer(ctx, "proj", "wing-b", "room-1", d2)

	// Filter by wing.
	results, _ := eng.Search(ctx, "content", SearchFilters{Project: "proj", Wing: "wing-a"})
	if len(results) != 1 {
		t.Fatalf("wing filter: got %d results, want 1", len(results))
	}
	if results[0].Wing != "wing-a" {
		t.Errorf("expected wing-a, got %s", results[0].Wing)
	}

	// Filter by hall.
	d3 := addDrawer(t, v, "proj", "wing-a", "room-2", "gamma content", "opinions")
	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-2", d3)

	results, _ = eng.Search(ctx, "content", SearchFilters{Project: "proj", Hall: "opinions"})
	if len(results) != 1 {
		t.Fatalf("hall filter: got %d results, want 1", len(results))
	}
	if results[0].Hall != "opinions" {
		t.Errorf("expected opinions, got %s", results[0].Hall)
	}
}

func TestSearchDateFilter(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d1 := storage.Drawer{Content: "old stuff", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-15T00:00:00Z"}
	_ = v.AppendDrawer("proj", "wing-a", "room-1", d1)
	stored1, _ := v.ListDrawers("proj", "wing-a", "room-1")
	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", stored1[0])

	d2 := storage.Drawer{Content: "new stuff", Hall: "facts", SourceType: "manual", FiledAt: "2026-06-15T00:00:00Z"}
	_ = v.AppendDrawer("proj", "wing-a", "room-2", d2)
	stored2, _ := v.ListDrawers("proj", "wing-a", "room-2")
	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-2", stored2[0])

	results, _ := eng.Search(ctx, "stuff", SearchFilters{Project: "proj", DateFrom: "2026-03-01"})
	if len(results) != 1 {
		t.Fatalf("date filter: got %d results, want 1", len(results))
	}
	if results[0].Date != "2026-06-15" {
		t.Errorf("expected 2026-06-15, got %s", results[0].Date)
	}
}

func TestStructuralBoosts(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d1 := addDrawer(t, v, "proj", "wing-a", "room-1", "content in target wing", "facts")
	d2 := addDrawer(t, v, "proj", "wing-b", "room-1", "content in other wing", "facts")

	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", d1)
	_ = eng.IndexDrawer(ctx, "proj", "wing-b", "room-1", d2)

	// Search with wing filter — matching drawer should get boosted.
	results, _ := eng.Search(ctx, "content", SearchFilters{Project: "proj", Wing: "wing-a"})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (wing filter)", len(results))
	}
	// The matching result gets wing boost.
	if results[0].Wing != "wing-a" {
		t.Errorf("expected wing-a first due to boost")
	}
}

func TestDeduplication(t *testing.T) {
	eng, _ := testEngine(t)
	ctx := context.Background()

	// Index two drawers with the same SourceRef (simulating adjacent chunks).
	d1 := storage.Drawer{
		ID: "chunk-1", Content: "first chunk of document", Hall: "facts",
		SourceType: "session", SourceRef: "session-2026-04-07",
		ChunkIndex: 0, FiledAt: "2026-04-07T00:00:00Z",
	}
	d2 := storage.Drawer{
		ID: "chunk-2", Content: "second chunk of document", Hall: "facts",
		SourceType: "session", SourceRef: "session-2026-04-07",
		ChunkIndex: 1, FiledAt: "2026-04-07T00:00:00Z",
	}

	eng.mu.Lock()
	idx := NewVectorIndex(384)
	eng.indexes["proj"] = idx
	mock := embedder.NewMock(384)
	v1, _ := mock.Embed(ctx, d1.Content)
	v2, _ := mock.Embed(ctx, d2.Content)
	_ = idx.Insert(d1.ID, v1)
	_ = idx.Insert(d2.ID, v2)
	eng.metadata[d1.ID] = makeDrawerMeta("proj", "wing-a", "room-1", d1)
	eng.metadata[d2.ID] = makeDrawerMeta("proj", "wing-a", "room-1", d2)
	eng.mu.Unlock()

	results, err := eng.Search(ctx, "chunk of document", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}

	// Should deduplicate: only 1 result from the same SourceRef.
	if len(results) != 1 {
		t.Errorf("expected 1 result after dedup, got %d", len(results))
	}
}

func TestRebuild(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	_ = addDrawer(t, v, "proj", "wing-a", "room-1", "first document", "facts")
	_ = addDrawer(t, v, "proj", "wing-a", "room-2", "second document", "opinions")
	_ = addDrawer(t, v, "proj", "wing-b", "room-1", "third document", "facts")

	if err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	results, err := eng.Search(ctx, "document", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("got %d results after rebuild, want 3", len(results))
	}
}

func TestEmptySearch(t *testing.T) {
	eng, _ := testEngine(t)
	results, err := eng.Search(context.Background(), "anything", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil results from empty engine, got %v", results)
	}
}

func TestCrossProjectSearch(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d1 := addDrawer(t, v, "proj-a", "wing-1", "room-1", "alpha project content", "facts")
	d2 := addDrawer(t, v, "proj-b", "wing-1", "room-1", "beta project content", "facts")

	_ = eng.IndexDrawer(ctx, "proj-a", "wing-1", "room-1", d1)
	_ = eng.IndexDrawer(ctx, "proj-b", "wing-1", "room-1", d2)

	// Cross-project search (no project filter).
	results, _ := eng.Search(ctx, "content", SearchFilters{})
	if len(results) != 2 {
		t.Errorf("cross-project: got %d results, want 2", len(results))
	}
}

func TestIndexAndRemoveDrawer(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d := addDrawer(t, v, "proj", "wing-a", "room-1", "removable content", "facts")
	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", d)

	results, _ := eng.Search(ctx, "removable", SearchFilters{Project: "proj"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result before remove, got %d", len(results))
	}

	eng.RemoveDrawer("proj", d.ID)

	results, _ = eng.Search(ctx, "removable", SearchFilters{Project: "proj"})
	if len(results) != 0 {
		t.Errorf("expected 0 results after remove, got %d", len(results))
	}
}

func TestSearchDefaultLimit(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	// Add 15 drawers.
	for i := range 15 {
		d := storage.Drawer{
			Content:    string(rune('A'+i)) + " unique content item",
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-07T00:00:00Z",
		}
		_ = v.AppendDrawer("proj", "wing-a", "room-1", d)
	}

	_ = eng.Rebuild(ctx, "proj")

	// Default limit is 10.
	results, _ := eng.Search(ctx, "content", SearchFilters{Project: "proj"})
	if len(results) > 10 {
		t.Errorf("expected at most 10 results with default limit, got %d", len(results))
	}
}

func TestEmbedderGetter(t *testing.T) {
	eng, _ := testEngine(t)
	emb := eng.Embedder()
	if emb == nil {
		t.Fatal("Embedder() returned nil")
	}
	dims, err := emb.Dimensions()
	if err != nil {
		t.Fatalf("Dimensions: %v", err)
	}
	if dims != 384 {
		t.Errorf("Dimensions() = %d, want 384", dims)
	}
}

func TestIndexDrawersWithPrecomputedVec(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d := addDrawer(t, v, "proj", "wing-a", "room-1", "pre-computed vector content", "facts")

	// Generate a vector manually.
	vec, err := eng.Embedder().Embed(ctx, d.Content)
	if err != nil {
		t.Fatal(err)
	}

	// Index with pre-computed vector via the batch API.
	if err := eng.IndexDrawers(ctx, []DrawerInput{{
		Project: "proj", Wing: "wing-a", Room: "room-1", Drawer: d, Vec: vec,
	}}); err != nil {
		t.Fatalf("IndexDrawers: %v", err)
	}

	// Should be searchable.
	results, err := eng.Search(ctx, "pre-computed", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].DrawerID != d.ID {
		t.Errorf("got drawer %q, want %q", results[0].DrawerID, d.ID)
	}
}

// TestSearchLazyBuildsIndex verifies that searching a project whose index was
// never built returns real results — the engine builds it on demand. Disabling
// the lazy build in Search makes this fail with "returned 0 results".
func TestSearchLazyBuildsIndex(t *testing.T) {
	eng, v, _ := countingEngine(t, storage.Config{})
	ctx := context.Background()

	addDrawer(t, v, "proj", "wing-a", "room-1", "first document", "facts")
	addDrawer(t, v, "proj", "wing-a", "room-2", "second document", "opinions")

	// No IndexDrawer, no Rebuild — the engine's index map is empty.
	eng.mu.RLock()
	n := len(eng.indexes)
	eng.mu.RUnlock()
	if n != 0 {
		t.Fatalf("precondition: engine has %d indexes, want 0", n)
	}

	results, err := eng.Search(ctx, "document", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("lazy build: returned %d results, want 2", len(results))
	}
}

// TestCrossProjectSearchLazyBuildsAllIndexes verifies that a cross-project
// search over a cold engine builds every known project rather than silently
// returning nothing.
func TestCrossProjectSearchLazyBuildsAllIndexes(t *testing.T) {
	eng, v, _ := countingEngine(t, storage.Config{})
	ctx := context.Background()

	addDrawer(t, v, "proj-a", "wing-1", "room-1", "alpha project content", "facts")
	addDrawer(t, v, "proj-b", "wing-1", "room-1", "beta project content", "facts")

	results, err := eng.Search(ctx, "content", SearchFilters{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("cross-project lazy build: returned %d results, want 2", len(results))
	}

	seen := map[string]bool{}
	for _, r := range results {
		seen[r.Project] = true
	}
	if !seen["proj-a"] || !seen["proj-b"] {
		t.Errorf("expected hits from both projects, got %v", seen)
	}
}

// TestLazyBuildRunsOnce verifies that concurrent searches for the same project
// trigger exactly one index build.
func TestLazyBuildRunsOnce(t *testing.T) {
	eng, v, emb := countingEngine(t, storage.Config{})
	ctx := context.Background()

	for i := range 5 {
		addDrawer(t, v, "proj", "wing-a", "room-1", fmt.Sprintf("document %d", i), "facts")
	}

	const searchers = 8
	var wg sync.WaitGroup
	errs := make([]error, searchers)
	counts := make([]int, searchers)
	for i := range searchers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := eng.Search(ctx, "document", SearchFilters{Project: "proj"})
			errs[i] = err
			counts[i] = len(res)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("searcher %d: %v", i, err)
		}
		if counts[i] != 5 {
			t.Errorf("searcher %d got %d results, want 5", i, counts[i])
		}
	}

	// One build of 5 cache-miss drawers = exactly one EmbedBatch call.
	_, batchCalls := emb.counts()
	if batchCalls != 1 {
		t.Errorf("build ran %d times under concurrent search, want exactly 1", batchCalls)
	}
}

// TestRebuildBatchesEmbeddings verifies that Rebuild embeds cache misses via
// EmbedBatch in chunks of EmbedderBatchSize, never one at a time.
func TestRebuildBatchesEmbeddings(t *testing.T) {
	eng, v, emb := countingEngine(t, storage.Config{EmbedderBatchSize: 32})
	ctx := context.Background()

	const drawers = 70 // ceil(70/32) = 3 batches
	for i := range drawers {
		addDrawer(t, v, "proj", "wing-a", "room-1", fmt.Sprintf("document number %d", i), "facts")
	}

	if err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	embedCalls, batchCalls := emb.counts()
	if embedCalls != 0 {
		t.Errorf("Rebuild made %d single Embed calls, want 0", embedCalls)
	}
	if batchCalls != 3 {
		t.Fatalf("Rebuild made %d EmbedBatch calls, want 3", batchCalls)
	}
	if want := []int{32, 32, 6}; !slices.Equal(emb.batchSizes, want) {
		t.Errorf("batch sizes = %v, want %v", emb.batchSizes, want)
	}

	// Every vector should now be durable in the embed cache.
	stored, err := v.ListDrawers("proj", "wing-a", "room-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range stored {
		vec, err := eng.cache.Get("proj", d.ID)
		if err != nil {
			t.Fatalf("cache.Get %s: %v", d.ID, err)
		}
		if vec == nil {
			t.Fatalf("drawer %s not written to embed cache", d.ID)
		}
	}
}

// TestSearchPropagatesBuildError verifies that a failed lazy build surfaces as
// an error, not as an empty result set.
func TestSearchPropagatesBuildError(t *testing.T) {
	eng, v, emb := countingEngine(t, storage.Config{})
	emb.batchFail = true
	ctx := context.Background()

	addDrawer(t, v, "proj", "wing-a", "room-1", "unreachable content", "facts")

	results, err := eng.Search(ctx, "content", SearchFilters{Project: "proj"})
	if err == nil {
		t.Fatalf("expected build error, got %d results and nil error", len(results))
	}
	if results != nil {
		t.Errorf("expected nil results on build failure, got %v", results)
	}

	// The failure is not memoized: a subsequent search retries and succeeds.
	emb.mu.Lock()
	emb.batchFail = false
	emb.mu.Unlock()

	results, err = eng.Search(ctx, "content", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatalf("retry after failed build: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("retry returned %d results, want 1", len(results))
	}
}

// TestRebuildClearsStaleIndex verifies that rebuilding a project whose drawers
// have all been deleted drops the previous index instead of serving hits for
// content that no longer exists.
func TestRebuildClearsStaleIndex(t *testing.T) {
	eng, v, _ := countingEngine(t, storage.Config{})
	ctx := context.Background()

	addDrawer(t, v, "proj", "wing-a", "room-1", "doomed content", "facts")

	results, err := eng.Search(ctx, "doomed", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results before deletion, want 1", len(results))
	}

	if err := os.RemoveAll(filepath.Join(v.Root, "palace", "proj", "drawers")); err != nil {
		t.Fatal(err)
	}
	if err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	results, err = eng.Search(ctx, "doomed", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("stale index still serving %d results after all drawers deleted", len(results))
	}
}

// TestIndexDrawersMixedBatch verifies that a batch with a mix of pre-computed
// and nil vectors embeds only the missing ones and indexes all entries.
func TestIndexDrawersMixedBatch(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d1 := addDrawer(t, v, "proj", "wing-a", "room-1", "alpha content precomputed", "facts")
	d2 := addDrawer(t, v, "proj", "wing-a", "room-1", "beta content embedded by engine", "facts")

	vec1, err := eng.Embedder().Embed(ctx, d1.Content)
	if err != nil {
		t.Fatal(err)
	}

	err = eng.IndexDrawers(ctx, []DrawerInput{
		{Project: "proj", Wing: "wing-a", Room: "room-1", Drawer: d1, Vec: vec1},
		{Project: "proj", Wing: "wing-a", Room: "room-1", Drawer: d2}, // Vec nil → engine embeds
	})
	if err != nil {
		t.Fatalf("IndexDrawers: %v", err)
	}

	results, err := eng.Search(ctx, "content", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}
