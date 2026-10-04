// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// fpVault holds three drawers in project "proj".
func fpVault(t *testing.T) *storage.Vault {
	t.Helper()
	v := testVault(t)
	for _, c := range []string{"alpha drawer text", "beta drawer text", "gamma drawer text"} {
		addDrawer(t, v, "proj", "proj", "room-1", c, "facts")
	}
	return v
}

// fpRebuild runs one fresh engine with model m over v and returns its stats.
func fpRebuild(t *testing.T, v *storage.Vault, model string) RebuildStats {
	t.Helper()
	eng := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderModel: model})
	t.Cleanup(func() { eng.Close() })
	stats, err := eng.Rebuild(context.Background(), "proj")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	return stats
}

func fpCacheDir(v *storage.Vault) string {
	return filepath.Join(v.Root, "palace", ".local", "embed-cache", "proj")
}

func fpSidecar(t *testing.T, v *storage.Vault) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fpCacheDir(v), storage.EmbedCacheFingerprintFile))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// fpVecs hashes every *.vec in the project cache, keyed by name.
func fpVecs(t *testing.T, v *storage.Vault) map[string]string {
	t.Helper()
	out := map[string]string{}
	ents, err := os.ReadDir(fpCacheDir(v))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".vec") && e.Type().IsRegular() {
			b, _ := os.ReadFile(filepath.Join(fpCacheDir(v), e.Name()))
			s := sha256.Sum256(b)
			out[e.Name()] = hex.EncodeToString(s[:])
		}
	}
	return out
}

// T1: an embedder change keeps every vector (ADR-014 decision 3: only `vp
// index rebuild` discards). Under regime B the A vectors are byte-identical,
// the sidecar still names A, every lookup is a miss, and nothing B embeds is
// written: the notes are embedded in memory only, on every build, so two
// regimes never mix. Going back to A serves the A vectors as hits again.
func TestEmbedCache_RegimeChangeKeepsVectors(t *testing.T) {
	v := fpVault(t)
	if st := fpRebuild(t, v, "model-a"); st.Embedded != 3 {
		t.Fatalf("first build embedded %d, want 3", st.Embedded)
	}
	if got, want := fpSidecar(t, v), embedder.Fingerprint("model-a", 0); got != want {
		t.Fatalf("sidecar %q, want %q", got, want)
	}
	before := fpVecs(t, v)

	for range 2 {
		if st := fpRebuild(t, v, "model-b"); st.Embedded != 3 || st.CacheHits != 0 {
			t.Fatalf("under another regime stats = %+v, want all 3 embedded in memory and no hit", st)
		}
		if got, want := fpSidecar(t, v), embedder.Fingerprint("model-a", 0); got != want {
			t.Fatalf("sidecar %q after a build under B, want it untouched (%q)", got, want)
		}
		if after := fpVecs(t, v); !maps.Equal(after, before) {
			t.Fatalf("vectors changed under another regime: %v -> %v", before, after)
		}
	}

	if st := fpRebuild(t, v, "model-a"); st.Embedded != 0 || st.CacheHits != 3 {
		t.Errorf("back under A: stats %+v, want 3 hits and no re-embed", st)
	}
}

// T2: a matching fingerprint is a hit: no re-embed, vectors byte-identical.
func TestEmbedCache_FingerprintMatchIsAHit(t *testing.T) {
	v := fpVault(t)
	fpRebuild(t, v, "model-a")
	before := fpVecs(t, v)
	if st := fpRebuild(t, v, "model-a"); st.Embedded != 0 || st.CacheHits != 3 {
		t.Fatalf("same fingerprint: stats %+v, want 3 hits", st)
	}
	after := fpVecs(t, v)
	if len(after) != 3 {
		t.Fatalf("vectors %v", after)
	}
	for k, h := range before {
		if after[k] != h {
			t.Errorf("vector %s changed under a matching fingerprint", k)
		}
	}
}

// T3: vectors with no sidecar cannot be attributed to any regime, so they are
// a mismatch (ADR-014 decision 3): every lookup is a miss, the vectors stay
// byte-identical, and no sidecar is written that would adopt them.
func TestEmbedCache_VectorsWithNoSidecarAreAMismatch(t *testing.T) {
	v := fpVault(t)
	fpRebuild(t, v, "model-a")
	side := filepath.Join(fpCacheDir(v), storage.EmbedCacheFingerprintFile)
	if err := os.Remove(side); err != nil {
		t.Fatal(err)
	}
	before := fpVecs(t, v)
	for range 2 {
		if st := fpRebuild(t, v, "model-a"); st.Embedded != 3 || st.CacheHits != 0 {
			t.Fatalf("unattributed vectors: stats %+v, want all 3 embedded in memory and no hit", st)
		}
	}
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Fatalf("a sidecar was written over unattributed vectors (stat err %v)", err)
	}
	if after := fpVecs(t, v); !maps.Equal(after, before) {
		t.Fatalf("unattributed vectors changed: %v -> %v", before, after)
	}
}

// A directory with no sidecar AND no vector is simply not built: the first
// write writes the sidecar, then its vector, and the vectors are hits after.
func TestEmbedCache_EmptyDirectoryIsNotAMismatch(t *testing.T) {
	v := fpVault(t)
	if err := os.MkdirAll(fpCacheDir(v), 0o755); err != nil {
		t.Fatal(err)
	}
	if st := fpRebuild(t, v, "model-a"); st.Embedded != 3 {
		t.Fatalf("first build embedded %d, want 3", st.Embedded)
	}
	if got, want := fpSidecar(t, v), embedder.Fingerprint("model-a", 0); got != want {
		t.Fatalf("sidecar %q, want %q", got, want)
	}
	if st := fpRebuild(t, v, "model-a"); st.CacheHits != 3 {
		t.Fatalf("second build stats %+v, want 3 hits", st)
	}
}

// Writer re-reads the regime under the commit lock every time; it never
// trusts the per-instance memo Get keeps. Here engine A's cache has already
// classified the project as its own regime; another writer (a rebuild under
// another regime, holding the commit lock) then replaces the sidecar. A's next
// Writer is refused, and nothing is written.
func TestEmbedCache_WriterRereadsTheRegimeUnderTheLock(t *testing.T) {
	v := fpVault(t)
	eng := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderModel: "model-a"})
	t.Cleanup(func() { eng.Close() })
	if _, err := eng.Rebuild(context.Background(), "proj"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.cache.Get("proj", "anything"); err != nil {
		t.Fatal(err)
	}
	if r := eng.cache.regimes["proj"]; r != regimeMatch {
		t.Fatalf("precondition: A's memo = %v, want a match", r)
	}

	tx, err := indexstore.Lock(context.Background(), v, "proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fpCacheDir(v), storage.EmbedCacheFingerprintFile),
		[]byte(embedder.Fingerprint("model-b", 0)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.Release(); err != nil {
		t.Fatal(err)
	}
	before := fpVecs(t, v)

	err = cachePut(t, eng.cache, "proj", "new-vector", []float32{1, 2, 3})
	if !errors.Is(err, ErrEmbedRegimeMismatch) {
		t.Fatalf("A's write after the regime changed: %v, want ErrEmbedRegimeMismatch", err)
	}
	if after := fpVecs(t, v); !maps.Equal(after, before) {
		t.Fatalf("a vector was written past another regime's sidecar: %v -> %v", before, after)
	}
}

// The sidecar is written before the first vector of a directory, and made
// durable first. A crash between the two (the vector's write fails) leaves the
// sidecar and no vector: a built, empty directory of this regime, not a
// mismatch, and the next write succeeds.
func TestEmbedCache_SidecarBeforeTheFirstVector(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "proj")
	c := NewEmbedCache(v)
	c.fingerprint = embedder.Fingerprint("model-a", 0)
	old := cacheWriteFn
	cacheWriteFn = func(path string, data []byte) error {
		if strings.HasSuffix(path, ".vec") {
			return errors.New("injected crash before the first vector")
		}
		return old(path, data)
	}
	err := cachePut(t, c, "proj", "d1", []float32{1, 2})
	cacheWriteFn = old
	if err == nil {
		t.Fatal("the injected vector failure did not fail the write")
	}
	dir := filepath.Join(v.Root, "palace", ".local", "embed-cache", "proj")
	if got, err := os.ReadFile(filepath.Join(dir, storage.EmbedCacheFingerprintFile)); err != nil || strings.TrimSpace(string(got)) != c.fingerprint {
		t.Fatalf("sidecar after the crash = %q, %v; want this regime's, written first", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "d1.vec")); !os.IsNotExist(err) {
		t.Fatalf("a vector exists after its write failed (stat err %v)", err)
	}
	if r, err := c.classify(dir); err != nil || r == regimeMismatch {
		t.Fatalf("the directory after the crash reads %v, %v; want it built, not a mismatch", r, err)
	}
	if err := cachePut(t, c, "proj", "d1", []float32{1, 2}); err != nil {
		t.Fatalf("the next write: %v", err)
	}
}

// A writer is bound to its Tx: once the Tx is committed, a Put through it is
// refused and writes nothing.
func TestEmbedCache_WriterRefusesAFinishedTx(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "proj")
	c := NewEmbedCache(v)
	tx, err := indexstore.Lock(context.Background(), v, "proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	w := mustWriter(t, c, tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := w.Put("proj", "late", []float32{1}); err == nil {
		t.Fatal("a Put through a writer whose Tx was committed succeeded")
	}
	if _, err := c.Writer(tx); err == nil {
		t.Fatal("Writer accepted a committed Tx")
	}
	p, _ := c.path("proj", "late")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("the refused Put wrote %s (stat err %v)", p, err)
	}
}

// An atomic Put never exposes a short vector: while one writer replaces a
// vector over and over (each under the commit lock, as production writes),
// lock-free readers only ever read one of the two whole vectors. An in-place
// os.WriteFile truncates before it writes, so a reader can see a short or
// empty file.
func TestEmbedCache_PutIsAtomicForReaders(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "proj")
	c := NewEmbedCache(v)
	a := make([]float32, 384)
	b := make([]float32, 384)
	for i := range a {
		a[i], b[i] = 1, 2
	}
	if err := cachePut(t, c, "proj", "d", a); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var bad atomic.Int64
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := c.Get("proj", "d")
				if err != nil || len(got) != 384 || (got[0] != 1 && got[0] != 2) || got[383] != got[0] {
					bad.Add(1)
				}
			}
		})
	}
	for i := range 300 {
		vec := a
		if i%2 == 0 {
			vec = b
		}
		if err := cachePut(t, c, "proj", "d", vec); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if n := bad.Load(); n > 0 {
		t.Fatalf("%d reads saw a short, empty or mixed vector", n)
	}
}

// T4: an engine with NO embedder never validates the regime, so it never
// deletes a vector or writes a sidecar. This is exactly the engine vp check
// builds in registeredToolCount (cmd/vp/cmd_check.go:
// search.NewEngine(nil, v, storage.Config{})), driven here through the cache
// operations a tool would reach.
func TestEmbedCache_NilEmbedderEngineNeverInvalidates_CmdCheckRegisteredToolCount(t *testing.T) {
	v := fpVault(t)
	fpRebuild(t, v, "model-a")
	side := filepath.Join(fpCacheDir(v), storage.EmbedCacheFingerprintFile)
	// A foreign regime AND, in the second pass, no sidecar at all: neither may
	// provoke a nil-embedder engine into removing anything.
	for _, sidecar := range []string{"vp-embed behaviour=0 model=other max_seq_len=0\n", ""} {
		if sidecar == "" {
			_ = os.Remove(side)
		} else if err := os.WriteFile(side, []byte(sidecar), 0o644); err != nil {
			t.Fatal(err)
		}
		before := fpVecs(t, v)

		eng := NewEngine(nil, v, storage.Config{})
		if eng.cache.fingerprint != "" {
			t.Fatalf("a nil-embedder engine carries fingerprint %q, want none", eng.cache.fingerprint)
		}
		ids := make([]string, 0, len(before))
		for k := range before {
			ids = append(ids, strings.TrimSuffix(k, ".vec"))
		}
		sort.Strings(ids)
		for _, id := range ids {
			if vec, err := eng.cache.Get("proj", id); err != nil || vec == nil {
				t.Errorf("Get %s through a nil-embedder engine: %v, %v", id, vec, err)
			}
		}
		if _, err := eng.cache.dir("proj"); err != nil {
			t.Fatal(err)
		}
		// Not closed: vp check never closes this engine, and Close would reach
		// the nil embedder.

		after := fpVecs(t, v)
		if len(after) != len(before) {
			t.Errorf("sidecar %q: vectors %d -> %d", sidecar, len(before), len(after))
		}
		got, err := os.ReadFile(side)
		switch {
		case sidecar == "" && !os.IsNotExist(err):
			t.Errorf("a nil-embedder engine wrote a sidecar: %q", got)
		case sidecar != "" && string(got) != sidecar:
			t.Errorf("a nil-embedder engine rewrote the sidecar: %q", got)
		}
	}
}
