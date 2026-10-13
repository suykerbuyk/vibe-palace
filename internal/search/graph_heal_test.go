// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// countEmb wraps an embedder and counts Embed/EmbedBatch AND, separately,
// Dimensions — the existing spies miss Dimensions, which a heal that copied
// rebuild's e.embedder.Dimensions() would call (loading ONNX in production,
// S1). The heal must make zero of all three.
type countEmb struct {
	inner                 embedder.Embedder
	embeds, batches, dims atomic.Int64
}

func (c *countEmb) Embed(ctx context.Context, text string) ([]float32, error) {
	c.embeds.Add(1)
	return c.inner.Embed(ctx, text)
}

func (c *countEmb) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	c.batches.Add(1)
	return c.inner.EmbedBatch(ctx, texts)
}

func (c *countEmb) Dimensions() (int, error) {
	c.dims.Add(1)
	return c.inner.Dimensions()
}

func (c *countEmb) Close() error { return c.inner.Close() }

func (c *countEmb) reset() {
	c.embeds.Store(0)
	c.batches.Store(0)
	c.dims.Store(0)
}

func (c *countEmb) assertQuiet(t *testing.T) {
	t.Helper()
	if e, b, d := c.embeds.Load(), c.batches.Load(), c.dims.Load(); e != 0 || b != 0 || d != 0 {
		t.Fatalf("the heal touched the embedder: Embed=%d EmbedBatch=%d Dimensions=%d, want 0/0/0", e, b, d)
	}
}

// healBase builds an engine on a fresh vault, commits one ledgered archive of
// contents as LOCAL-TIER store chunks (with their vectors in the cache when
// withVecs), lowers the selection band, and resets the embedder spy. It does
// NOT write a graph file.
func healBase(t *testing.T, contents []string, withVecs bool, tHi, lLo int) (*Engine, *countEmb, *storage.Vault, string) {
	t.Helper()
	withThresholds(t, tHi, lLo)
	v := testVault(t)
	const project = "healproj"
	ce := &countEmb{inner: embedder.NewMock(384)}
	e := NewEngine(ce, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { _ = e.Close() })
	commitArchive(t, e.cache, v, project, "sess-1", "sha-1", contents, withVecs)
	ce.reset()
	return e, ce, v, project
}

// writeValidGraph writes a structurally valid hnsw.idx whose fingerprint is the
// CURRENT provisionalHNSWParams' (its body is irrelevant to the heal, which
// reads only the header and rebuilds from the corpus). It returns the bytes.
func writeValidGraph(t *testing.T, v *storage.Vault, project string) []byte {
	t.Helper()
	h := buildHNSW(t, 384, 3, provisionalHNSWParams)
	return saveBuilt(t, v, project, h)
}

// bumpEfSearch raises the configured EfSearch by one, so a graph saved under
// the old value now mismatches the configured fingerprint. Restored on cleanup.
func bumpEfSearch(t *testing.T) {
	t.Helper()
	old := provisionalHNSWParams
	provisionalHNSWParams.EfSearch = old.EfSearch + 1
	t.Cleanup(func() { provisionalHNSWParams = old })
}

func readGraphBytes(t *testing.T, v *storage.Vault, project string) []byte {
	t.Helper()
	data, err := indexstore.ReadGraph(v, project)
	if err != nil {
		t.Fatalf("ReadGraph: %v", err)
	}
	return data
}

func embedCacheDir(t *testing.T, v *storage.Vault, project string) string {
	t.Helper()
	dir, err := v.EmbedCacheDir(project)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// hashCacheDir hashes every .vec file's name and bytes, so a re-committed
// vector changes the digest (test 2). WriteGraph never touches these files.
func hashCacheDir(t *testing.T, dir string) [32]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return [32]byte{}
		}
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		h.Write([]byte(n))
		h.Write(b)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func removeVec(t *testing.T, v *storage.Vault, project, content string) {
	t.Helper()
	if err := os.Remove(filepath.Join(embedCacheDir(t, v, project), index.ChunkID(content)+".vec")); err != nil {
		t.Fatal(err)
	}
}

// Test 1: a fingerprint mismatch heals — the rebuilt file carries the new
// fingerprint and round-trips through Load — with no embedding. Mutants:
// "saveGraph reverted" (header keeps the old fp → loadGraph mismatch); "heal
// embeds / loads the model" (the spy, incl. Dimensions, is nonzero).
func TestHealMismatchRebuildsAndRoundTrips(t *testing.T) {
	ctx := context.Background()
	e, ce, v, project := healBase(t, []string{"a one", "a two", "a three", "a four"}, true, 2, 1)
	writeValidGraph(t, v, project)
	bumpEfSearch(t)

	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Rebuilt || out.Deleted || out.Reason != "fingerprint mismatch" {
		t.Fatalf("out = %+v, want {Rebuilt:true, Reason:\"fingerprint mismatch\"}", out)
	}
	fp, ok := readGraphHeader(readGraphBytes(t, v, project))
	if !ok {
		t.Fatal("healed file has no readable header")
	}
	if want := computeFingerprint(384, provisionalHNSWParams.M, provisionalHNSWParams.EfSearch); fp != want {
		t.Fatal("healed file does not carry the configured fingerprint (saveGraph reverted?)")
	}
	idx, res, err := newHNSWIndexFromDisk(v, project, 384, provisionalHNSWParams)
	if err != nil || res != loaded || idx == nil {
		t.Fatalf("healed graph not re-loadable: res=%v err=%v", res, err)
	}
	ce.assertQuiet(t)
}

// Test 2: the heal re-saves only the graph; it never re-commits vectors. The
// embed-cache directory is byte-unchanged. Mutant: a heal that calls
// PutVectors changes a .vec (WriteGraph never does, discard.go).
func TestHealDoesNotRecommitVectors(t *testing.T) {
	ctx := context.Background()
	e, ce, v, project := healBase(t, []string{"b one", "b two", "b three"}, true, 2, 1)
	writeValidGraph(t, v, project)
	bumpEfSearch(t)

	before := hashCacheDir(t, embedCacheDir(t, v, project))
	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Rebuilt {
		t.Fatalf("out = %+v, want Rebuilt", out)
	}
	if after := hashCacheDir(t, embedCacheDir(t, v, project)); after != before {
		t.Fatal("the heal changed the embed-cache directory (re-committed vectors?)")
	}
	ce.assertQuiet(t)
}

// Test 3: no graph file → no-op, before the dims probe. Mutant: a heal that
// converts a no-graph project.
func TestHealNoGraphNoOp(t *testing.T) {
	ctx := context.Background()
	e, ce, v, project := healBase(t, []string{"c one"}, true, 2, 1)
	// no graph written
	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if out != (HealOutcome{Reason: "no graph"}) {
		t.Fatalf("out = %+v, want {Reason:\"no graph\"}", out)
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("the heal created a graph file for a no-graph project")
	}
	ce.assertQuiet(t)
}

// Test 4: a matching fingerprint → "current" no-op; the file is byte-unchanged
// and the embedder is untouched. Mutant: a heal that always rebuilds.
func TestHealMatchIsCurrent(t *testing.T) {
	ctx := context.Background()
	e, ce, v, project := healBase(t, []string{"d one", "d two", "d three"}, true, 2, 1)
	before := writeValidGraph(t, v, project) // fingerprint matches (no bump)

	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if out != (HealOutcome{Reason: "current"}) {
		t.Fatalf("out = %+v, want {Reason:\"current\"}", out)
	}
	if after := readGraphBytes(t, v, project); string(after) != string(before) {
		t.Fatal("the heal rewrote a current graph file")
	}
	ce.assertQuiet(t)
}

// Test 5: a corrupt header → deleted, before the dims probe (so an empty cache
// cannot leave it on disk). Mutant: a heal that leaves it or tries to Import it.
func TestHealCorruptHeaderDeletes(t *testing.T) {
	ctx := context.Background()
	e, _, v, project := healBase(t, []string{"e one"}, true, 2, 1)
	garbage := make([]byte, 128)
	for i := range garbage {
		garbage[i] = 0xAB
	}
	writeGraphFile(t, v, project, garbage)

	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if out != (HealOutcome{Deleted: true, Reason: "corrupt header"}) {
		t.Fatalf("out = %+v, want {Deleted:true, Reason:\"corrupt header\"}", out)
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("the heal left a corrupt-header file on disk")
	}
}

// Test 6: a mismatch whose EXPECTED id count is below L → deleted. Mutant: a
// heal that rebuilds below the expected-L floor.
func TestHealBelowLDeletes(t *testing.T) {
	ctx := context.Background()
	e, ce, v, project := healBase(t, []string{"f one", "f two"}, true, 10, 5) // nExpected=2 < L=5
	writeValidGraph(t, v, project)
	bumpEfSearch(t)

	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if out != (HealOutcome{Deleted: true, Reason: "below L"}) {
		t.Fatalf("out = %+v, want {Deleted:true, Reason:\"below L\"}", out)
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("the below-L heal did not delete the file")
	}
	ce.assertQuiet(t)
}

// Test 6b: the below-L delete sits behind the gen witness. A raw second writer
// commits in beforeCommitLockHook, advancing the counter, so the heal SKIPS the
// delete and the file survives. Mutant: "below-L delete ignores the gen witness
// and deletes anyway" (and "reports Deleted:true for a surviving file").
func TestHealBelowLRespectsGenWitness(t *testing.T) {
	ctx := context.Background()
	e, _, v, project := healBase(t, []string{"g one", "g two"}, true, 10, 5)
	before := writeValidGraph(t, v, project)
	bumpEfSearch(t)

	restore, gatherRan := installRaceHooks(t, e, v, project)
	defer restore()

	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if !*gatherRan {
		t.Fatal("the gather-time gen witness never fired (heal read gen only under the lock?)")
	}
	if out != (HealOutcome{Reason: "store advanced under heal; next run retries"}) {
		t.Fatalf("out = %+v, want the gen-witness skip (Deleted must be false for a surviving file)", out)
	}
	if after := readGraphBytes(t, v, project); string(after) != string(before) {
		t.Fatal("the below-L heal deleted/changed the file despite the advanced counter")
	}
}

// Test 7: a local-tier chunk whose vector is missing is left out of the graph,
// and the heal records StaleMissingVectors (a cache miss IS a vector miss,
// N3). Mutants: "heal embeds the miss" (spy), "heal includes the missing id"
// (Len), "heal reports Rebuilt:true with coverage silently current".
func TestHealMissingVectorLeftOutAndStale(t *testing.T) {
	ctx := context.Background()
	contents := []string{"h one", "h two", "h three", "h four"}
	e, ce, v, project := healBase(t, contents, true, 2, 1)
	removeVec(t, v, project, "h four") // one local-tier miss
	writeValidGraph(t, v, project)
	bumpEfSearch(t)

	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Rebuilt {
		t.Fatalf("out = %+v, want Rebuilt", out)
	}
	idx, res, err := newHNSWIndexFromDisk(v, project, 384, provisionalHNSWParams)
	if err != nil || res != loaded {
		t.Fatalf("healed graph not loadable: res=%v err=%v", res, err)
	}
	if got := idx.Len(); got != len(contents)-1 {
		t.Fatalf("healed graph Len = %d, want %d (the missing-vector id must be left out)", got, len(contents)-1)
	}
	rec := record(t, v, project)
	if !containsReason(rec.Stale, missingVectors) {
		t.Fatal("the heal did not set StaleMissingVectors for the cache-missed local-tier chunk")
	}
	ce.assertQuiet(t)
}

// Test 8: the gen witness. A raw indexstore.Lock second writer commits a vector
// in beforeCommitLockHook, advancing the counter; the heal's locked gen-compare
// skips and the mismatched file is byte-unchanged. Mutants: "heal omits the
// locked gen-compare and writes anyway" (file changes) and "heal omits the
// gather-time gen witness" (gatherRan assertion).
func TestHealGenWitnessSkipsClobber(t *testing.T) {
	ctx := context.Background()
	e, _, v, project := healBase(t, []string{"i one", "i two", "i three", "i four"}, true, 2, 1)
	before := writeValidGraph(t, v, project)
	bumpEfSearch(t)

	restore, gatherRan := installRaceHooks(t, e, v, project)
	defer restore()

	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if !*gatherRan {
		t.Fatal("the gather-time gen witness never fired (heal read gen only under the lock?)")
	}
	if out != (HealOutcome{Reason: "store advanced under heal; next run retries"}) {
		t.Fatalf("out = %+v, want the gen-witness skip", out)
	}
	if after := readGraphBytes(t, v, project); string(after) != string(before) {
		t.Fatal("the heal clobbered the file despite a concurrent commit (gen witness missing?)")
	}
}

// installRaceHooks wires the gather-time witness and the second writer. The
// gather hook records that builtGen was read at the lock-free gather. The
// commit-lock hook (fires while the heal holds the mutex and is about to take
// the commit lock) commits a vector through a RAW indexstore.Lock — NOT a
// capture, which calls lockProject and would deadlock on the held semaphore —
// advancing the store counter so the heal's gen-compare skips.
func installRaceHooks(t *testing.T, e *Engine, v *storage.Vault, project string) (restore func(), gatherRan *bool) {
	t.Helper()
	ran := false
	oldGather := healGatherHook
	healGatherHook = func(p string) {
		if p == project {
			ran = true
		}
	}
	var once sync.Once
	oldCommit := beforeCommitLockHook
	beforeCommitLockHook = func(p string) {
		if p != project {
			return
		}
		once.Do(func() {
			if !ran {
				t.Error("second writer ran before the gather-time gen witness")
			}
			tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
			if err != nil {
				t.Errorf("race writer lock: %v", err)
				return
			}
			w, err := e.CacheWriter(tx)
			if err != nil {
				_ = tx.Release()
				t.Errorf("race writer: %v", err)
				return
			}
			if err := tx.PutVectors(w, map[string][]float32{"heal-race-id": oneHotVec(384, 7)}); err != nil {
				_ = tx.Release()
				t.Errorf("race writer PutVectors: %v", err)
				return
			}
			if err := tx.Commit(); err != nil {
				t.Errorf("race writer commit: %v", err)
			}
		})
	}
	return func() { healGatherHook, beforeCommitLockHook = oldGather, oldCommit }, &ran
}

// Test 10: the heal takes the index run lock ZERO times. The commit-lock-only
// half is covered by TestLockDiscipline (a raw indexstore.Lock from the heal
// fails that package audit); lockRecorder fires inside pl.Tx and cannot see a
// raw indexstore.Lock, so it is not used here (S4).
func TestHealTakesNoRunLock(t *testing.T) {
	ctx := context.Background()
	e, _, v, project := healBase(t, []string{"j one", "j two", "j three"}, true, 2, 1)
	writeValidGraph(t, v, project)
	bumpEfSearch(t)

	var runLockEvents int64
	restore := indexstore.ObserveRunLocks(func(indexstore.RunLockEvent) { atomic.AddInt64(&runLockEvents, 1) })
	defer restore()

	if _, err := e.HealGraph(ctx, v.Root, project); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt64(&runLockEvents); n != 0 {
		t.Fatalf("the heal caused %d run-lock event(s), want 0", n)
	}
}

// Test 11: after the heal commits (bumping the store counter), a running engine
// that had remembered the pre-heal counter rebuilds on its next ensureIndex and
// warm-starts the healed file (loaded; no library Add). Mutant: "heal writes
// without the Tx commit" — no gen bump, ensureIndex no-ops, nothing is
// installed.
func TestHealPickedUpAfterGenBump(t *testing.T) {
	ctx := context.Background()
	e, _, v, project := healBase(t, []string{"k one", "k two", "k three", "k four"}, true, 2, 1)
	writeValidGraph(t, v, project)
	bumpEfSearch(t)

	// The engine has remembered the pre-heal counter, so lg==g is reachable
	// (an engine with no entry would rebuild regardless and mask the mutant).
	pre, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	e.buildMu.Lock()
	e.loaded[project] = pre
	e.buildMu.Unlock()

	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil || !out.Rebuilt {
		t.Fatalf("heal: out=%+v err=%v", out, err)
	}
	if err := e.ensureIndex(ctx, project); err != nil {
		t.Fatal(err)
	}
	idx := installedIndex(t, e, project)
	h, ok := idx.(*hnswIndex)
	if !ok {
		t.Fatalf("after the heal's gen bump, ensureIndex did not install the healed HNSW (got %T)", idx)
	}
	if got := h.libraryAdds.Load(); got != 0 {
		t.Fatalf("libraryAdds = %d, want 0 (the healed file must warm-start, not rebuild)", got)
	}
}

// Test 12 (S3): a mismatch with EXPECTED id count >= L but cache HITS < L is
// REBUILT from the hits, not deleted, and marked stale for the misses. Mutant:
// "below-L applied to the hit count deletes a graph the selection table keeps".
func TestHealExpectedAboveLHitsBelowLRebuilds(t *testing.T) {
	ctx := context.Background()
	contents := []string{"l one", "l two", "l three", "l four"} // nExpected=4
	e, ce, v, project := healBase(t, contents, true, 5, 3)      // L=3
	removeVec(t, v, project, "l three")                         // hits=2 < L=3, nExpected=4 >= L
	removeVec(t, v, project, "l four")
	writeValidGraph(t, v, project)
	bumpEfSearch(t)

	out, err := e.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Rebuilt || out.Deleted {
		t.Fatalf("out = %+v, want Rebuilt (expected>=L even though hits<L)", out)
	}
	if !hasGraphFile(t, v, project) {
		t.Fatal("the heal deleted a graph whose expected count is >= L")
	}
	fp, ok := readGraphHeader(readGraphBytes(t, v, project))
	if !ok || fp != computeFingerprint(384, provisionalHNSWParams.M, provisionalHNSWParams.EfSearch) {
		t.Fatal("rebuilt file does not carry the configured fingerprint")
	}
	if !containsReason(record(t, v, project).Stale, missingVectors) {
		t.Fatal("the heal did not set StaleMissingVectors for the dropped hits")
	}
	ce.assertQuiet(t)
}
