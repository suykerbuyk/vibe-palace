// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testVault(t *testing.T) *storage.Vault {
	t.Helper()
	return storage.NewVault(t.TempDir())
}

func TestCachePutGet(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	vec := []float32{0.1, 0.2, 0.3, -0.5}
	if err := c.Put("proj", "drawer-1", vec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := c.Get("proj", "drawer-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != len(vec) {
		t.Fatalf("got len %d, want %d", len(got), len(vec))
	}
	for i := range vec {
		if got[i] != vec[i] {
			t.Errorf("index %d: got %f, want %f", i, got[i], vec[i])
		}
	}
}

func TestCacheMiss(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	got, err := c.Get("proj", "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for cache miss, got %v", got)
	}
}

func TestCacheBinaryFormat(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	dims := 384
	vec := make([]float32, dims)
	for i := range vec {
		vec[i] = float32(i) / float32(dims)
	}

	if err := c.Put("proj", "drawer-1", vec); err != nil {
		t.Fatal(err)
	}

	path, _ := c.path("proj", "drawer-1")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	expectedSize := int64(dims * 4) // 384 * 4 = 1536 bytes
	if info.Size() != expectedSize {
		t.Errorf("file size = %d, want %d", info.Size(), expectedSize)
	}
}

func TestCacheDelete(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	if err := c.Put("proj", "d1", []float32{1, 2, 3}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	path, _ := c.path("proj", "d1")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: .vec should exist: %v", err)
	}

	if err := c.Delete("proj", "d1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected .vec unlinked, stat err = %v", err)
	}

	// A missing file is not an error.
	if err := c.Delete("proj", "d1"); err != nil {
		t.Errorf("Delete of missing file returned %v, want nil", err)
	}
	if err := c.Delete("proj", "never-cached"); err != nil {
		t.Errorf("Delete of never-cached returned %v, want nil", err)
	}
}

func TestCacheOverwrite(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	v1 := []float32{1.0, 2.0, 3.0}
	v2 := []float32{4.0, 5.0, 6.0}

	_ = c.Put("proj", "d1", v1)
	_ = c.Put("proj", "d1", v2)

	got, _ := c.Get("proj", "d1")
	for i := range v2 {
		if got[i] != v2[i] {
			t.Errorf("index %d: got %f, want %f (should be overwritten)", i, got[i], v2[i])
		}
	}
}

// TestEmbedCachePut_NeverCreatesAProjectTree pins the invariant the move exists
// for: a Put on a slug with no tree at all lands under palace/.local/ and
// creates nothing under palace/<slug>/ or Projects/<slug>/. Pointing path()
// back at storage.LocalDir turns this red.
func TestEmbedCachePut_NeverCreatesAProjectTree(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	if err := c.Put("ghost", "d1", []float32{1, 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	want := filepath.Join(v.Root, "palace", ".local", "embed-cache", "ghost", "d1.vec")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("vector not at %s: %v", want, err)
	}
	for _, rel := range []string{"palace/ghost", "Projects/ghost"} {
		if _, err := os.Stat(filepath.Join(v.Root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("a Put created %s (stat err %v) — the cache must write only under palace/.local/", rel, err)
		}
	}
}

// TestEmbedCache_RefusesInvalidSlug: the slug is a path segment, so an invalid
// one is refused on every operation rather than joined into a path.
func TestEmbedCache_RefusesInvalidSlug(t *testing.T) {
	c := NewEmbedCache(testVault(t))
	if err := c.Put("../escape", "d1", []float32{1}); err == nil {
		t.Error("Put accepted an invalid slug")
	}
	if _, err := c.Get("Not A Slug", "d1"); err == nil {
		t.Error("Get accepted an invalid slug")
	}
	if err := c.Delete("", "d1"); err == nil {
		t.Error("Delete accepted an invalid slug")
	}
}

// writeLegacyVector writes vec at the pre-move path
// palace/<project>/.local/embed-cache/<id>.vec, which only a binary from before
// the move ever wrote.
func writeLegacyVector(t *testing.T, root, project, id string, vec []float32) string {
	t.Helper()
	dir := filepath.Join(root, "palace", project, ".local", "embed-cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len(vec)*4)
	for i, f := range vec {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(f))
	}
	p := filepath.Join(dir, id+".vec")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestEmbedCache_LegacyVectorsAreCacheHitsAfterMigration: the move must not
// cost a re-embed. Vectors cached at the legacy path by an older binary are
// migrated by the first cache operation and served as hits by the same Rebuild.
func TestEmbedCache_LegacyVectorsAreCacheHitsAfterMigration(t *testing.T) {
	eng, v, emb := countingEngine(t, storage.Config{})
	d1 := addDrawer(t, v, "proj", "wing-a", "room-1", "legacy content one", "facts")
	d2 := addDrawer(t, v, "proj", "wing-a", "room-1", "legacy content two", "facts")

	mock := embedder.NewMock(384)
	for _, d := range []storage.Drawer{d1, d2} {
		vec, err := mock.Embed(context.Background(), d.Content)
		if err != nil {
			t.Fatal(err)
		}
		writeLegacyVector(t, v.Root, "proj", d.ID, vec)
	}

	stats, err := eng.Rebuild(context.Background(), "proj")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Embedded != 0 || stats.CacheHits != 2 {
		t.Fatalf("stats = %+v, want 0 embedded and 2 cache hits — the migration cost a re-embed", stats)
	}
	if _, batches := emb.counts(); batches != 0 {
		t.Errorf("embedder ran %d batches, want 0", batches)
	}
	if _, err := os.Stat(filepath.Join(v.Root, "palace", "proj", ".local")); !os.IsNotExist(err) {
		t.Errorf("the emptied legacy .local must be healed away (stat err %v)", err)
	}
	for _, d := range []storage.Drawer{d1, d2} {
		if _, err := os.Stat(filepath.Join(v.Root, "palace", ".local", "embed-cache", "proj", d.ID+".vec")); err != nil {
			t.Errorf("vector %s not at the new path: %v", d.ID, err)
		}
	}
}

// TestEmbedCache_SweepsOncePerInstance: the Once is per EmbedCache. A second
// operation on the same instance does not sweep again; a fresh instance does.
func TestEmbedCache_SweepsOncePerInstance(t *testing.T) {
	v := testVault(t)
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	c1 := NewEmbedCache(v)
	if _, err := c1.Get("keep", "x"); err != nil {
		t.Fatal(err)
	}

	legacy := writeLegacyVector(t, v.Root, "keep", "d1", []float32{1, 2})
	if got, _ := c1.Get("keep", "d1"); got != nil {
		t.Fatal("the same instance must not sweep twice, so the legacy vector must still be a miss")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy vector moved without a sweep: %v", err)
	}

	c2 := NewEmbedCache(v)
	got, err := c2.Get("keep", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("a fresh instance must sweep and serve the migrated vector, got %v", got)
	}
}

// TestEmbedCache_SweepFailureIsNotFatal: an unreadable palace/ makes the sweep
// fail, and the cache still works in the new layout.
func TestEmbedCache_SweepFailureIsNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	v := testVault(t)
	c := NewEmbedCache(v)
	if err := c.Put("proj", "d1", []float32{3}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	palace := filepath.Join(v.Root, "palace")
	if err := os.Chmod(palace, 0o300); err != nil { // writable and traversable, not listable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(palace, 0o755) })

	fresh := NewEmbedCache(v)
	got, err := fresh.Get("proj", "d1")
	if err != nil || len(got) != 1 || got[0] != 3 {
		t.Fatalf("Get after a failed sweep = %v, %v; want the cached vector", got, err)
	}
}

// TestEmbedCache_ConcurrentInstancesConverge: several EmbedCache instances over
// one vault — each its own sweep Once, as separate engines and processes are —
// sweep and Put at the same moment, under -race. Every legacy vector ends at the
// new path with its bytes, every Put is readable, and no legacy cache survives.
// The storage package cannot import EmbedCache, so this is where real Puts race
// the sweep (storage's own concurrency test races the Put shape).
func TestEmbedCache_ConcurrentInstancesConverge(t *testing.T) {
	v := testVault(t)
	for _, p := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(v.Root, "Projects", p), 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range 10 {
			writeLegacyVector(t, v.Root, p, "legacy"+string(rune('a'+i)), []float32{float32(i), 1})
		}
	}

	const workers = 6
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := range workers {
		wg.Go(func() {
			c := NewEmbedCache(v)
			p := []string{"alpha", "beta"}[w%2]
			for i := range 5 {
				if err := c.Put(p, "put"+string(rune('a'+w))+string(rune('a'+i)), []float32{float32(w), float32(i)}); err != nil {
					errs[w] = err
					return
				}
			}
		})
	}
	wg.Wait()
	for w, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", w, err)
		}
	}

	check := NewEmbedCache(v)
	for _, p := range []string{"alpha", "beta"} {
		for i := range 10 {
			got, err := check.Get(p, "legacy"+string(rune('a'+i)))
			if err != nil || len(got) != 2 || got[0] != float32(i) {
				t.Errorf("%s legacy %d = %v, %v", p, i, got, err)
			}
		}
		if _, err := os.Stat(filepath.Join(v.Root, "palace", p)); !os.IsNotExist(err) {
			t.Errorf("palace/%s must be healed away (stat err %v)", p, err)
		}
	}
	for w := range workers {
		p := []string{"alpha", "beta"}[w%2]
		for i := range 5 {
			got, err := check.Get(p, "put"+string(rune('a'+w))+string(rune('a'+i)))
			if err != nil || len(got) != 2 || got[0] != float32(w) || got[1] != float32(i) {
				t.Errorf("put %d/%d = %v, %v", w, i, got, err)
			}
		}
	}
}

// TestEmbedCachePut_RetriesWhenItsDirectoryVanishes drives Put's own retry. The
// first write finds its directory gone — as when a sweep's rename replaced the
// empty directory Put had just made — and fails with ENOENT; Put must re-create
// the directory and land the vector. Deleting the retry from Put turns this red.
func TestEmbedCachePut_RetriesWhenItsDirectoryVanishes(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)
	calls := 0
	old := cacheWriteFile
	cacheWriteFile = func(name string, data []byte, perm os.FileMode) error {
		calls++
		if calls == 1 {
			if err := os.Remove(filepath.Dir(name)); err != nil {
				t.Fatalf("remove the directory under the first write: %v", err)
			}
		}
		return os.WriteFile(name, data, perm)
	}
	t.Cleanup(func() { cacheWriteFile = old })

	if err := c.Put("proj", "d1", []float32{7, 8}); err != nil {
		t.Fatalf("Put failed after its directory vanished once: %v", err)
	}
	if calls != 2 {
		t.Fatalf("writes = %d, want exactly one retry", calls)
	}
	got, err := c.Get("proj", "d1")
	if err != nil || len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Fatalf("Get = %v, %v; want the retried vector", got, err)
	}
}
