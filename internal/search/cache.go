// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// cacheWriteFn is the one file write of the embed cache: a vector or the
// regime sidecar, written atomically (a temp file in the same directory,
// fsynced, renamed over the final name), so a crash leaves either the old file
// or the whole new one, never a truncated vector under its final name. It is a
// variable only so a test can fail a chosen write, or make one meet a directory
// that vanished under it.
var cacheWriteFn = func(path string, data []byte) error {
	return atomicfile.Write("", path, data, atomicfile.WithFsync())
}

// EmbedCache manages on-disk embedding vector cache files at
// palace/.local/embed-cache/{project}/{drawerID}.vec.
// Each file stores raw little-endian float32 values.
//
// 🔴 THE INVARIANT: the cache writes only under palace/.local/, which no project
// enumerator walks and the vault's gitignore keeps out of the repository. So a
// Put can never create or change anything under palace/{project}/ or
// Projects/{project}/, for any slug, with no existence check on the embedding
// path. The cache used to live at palace/{project}/.local/embed-cache/, inside
// the project's synced directory, where it outlived every pulled deletion of
// the project and left a directory that every enumerator counted as a store.
//
// The first cache operation on each instance runs storage.SweepEmbedCaches
// once, which migrates that legacy layout, removes the directories the move
// empties and reaps caches for slugs in neither tree. That includes a Get from
// a read-only vp_search: operator decision 2026-09-10 rules the sweep exempt
// from the read-only contract, because everything it touches is host-local,
// regenerable derived state that git does not track (the sweep asks git before
// moving a legacy cache, and leaves a slug with tracked files alone).
//
// The Once is per INSTANCE, not per package: a fresh engine sweeps again, which
// matches process semantics and gives each test a clean start. It is lazy, so
// constructing an engine (the tool registry, the surface golden) does no I/O.
//
// The embedding regime (ADR-014 decision 3). A cache with a fingerprint (set
// by NewEngine when it has an embedder) compares each project directory's
// storage.EmbedCacheFingerprintFile sidecar with it:
//
//   - equal: the vectors are this regime's;
//   - absent, and no vector in the directory: not built yet; the first write
//     writes the sidecar before its vector;
//   - different, or absent with vectors present: a mismatch. Those vectors
//     cannot be this regime's (or cannot be attributed to any), so every Get
//     for the project is a miss and every Writer is refused. Nothing is
//     removed and the sidecar is not rewritten: only `vp index rebuild`
//     discards a regime's vectors.
//
// Get classifies lock-free and memoizes per project until Forget (the engine
// forgets on every reload of the project); Writer classifies afresh under the
// index commit lock every time, so a sidecar another process rewrote is never
// written past. A cache with no fingerprint (vp check's engine, with no
// embedder) never classifies and never writes a sidecar.
//
// Writes happen only through Writer, under a held index commit lock
// (indexstore.Tx): there is no exported Put or Delete.
type EmbedCache struct {
	vault     *storage.Vault
	sweepOnce sync.Once

	fingerprint string
	regimeMu    sync.Mutex
	regimes     map[string]cacheRegime // Get's memo; see Forget
}

// cacheRegime is how a project's cache directory compares with the cache's
// fingerprint.
type cacheRegime int

const (
	regimeUnbuilt  cacheRegime = iota // no sidecar and no vector
	regimeMatch                       // the sidecar names this regime
	regimeMismatch                    // another regime, or vectors with no sidecar
)

// ErrEmbedRegimeMismatch is returned by Writer when the project's cache holds
// another embedding regime's vectors, or vectors with no sidecar. The project
// is stale for a fingerprint reason until `vp index rebuild`.
var ErrEmbedRegimeMismatch = errors.New("embed cache holds another embedding regime's vectors")

// NewEmbedCache creates a new embedding cache backed by the vault.
func NewEmbedCache(vault *storage.Vault) *EmbedCache {
	return &EmbedCache{vault: vault, regimes: map[string]cacheRegime{}}
}

// Get returns a cached embedding vector, or nil if not cached. It takes no
// lock and writes nothing. On a project whose cache is another regime's (see
// EmbedCache) every Get is a miss.
func (c *EmbedCache) Get(project, drawerID string) ([]float32, error) {
	path, err := c.path(project, drawerID)
	if err != nil {
		return nil, err
	}
	if c.fingerprint != "" {
		r, err := c.memoRegime(project, filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		if r == regimeMismatch {
			return nil, nil
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read embed cache: %w", err)
	}

	if len(data)%4 != 0 {
		return nil, fmt.Errorf("corrupt cache file %s: size %d not divisible by 4", path, len(data))
	}

	vec := make([]float32, len(data)/4)
	for i := range vec {
		bits := binary.LittleEndian.Uint32(data[i*4:])
		vec[i] = math.Float32frombits(bits)
	}
	return vec, nil
}

// putVector writes one vector atomically. It is unexported: the only caller is the
// writer Writer returns, which holds the project's index commit lock.
//
// The write is retried ONCE after re-creating the directory when it fails with
// ENOENT. The directory can vanish between EnsureDir and the write: a sweep in
// another process that saw the target absent renames the legacy cache onto the
// path, and rename(2) silently replaces an EMPTY directory — the one EnsureDir
// just made — so the temp file lands in a directory that no longer exists.
// After the rename the path is a real directory again, and the retry lands the
// vector in it. (A concurrent orphan reap of a slug in neither tree can do the
// same.)
func (c *EmbedCache) putVector(dir, drawerID string, vec []float32) error {
	data := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(v))
	}
	return writeRetrying(filepath.Join(dir, drawerID+".vec"), data)
}

// writeRetrying is putVector's write, shared with the sidecar write.
func writeRetrying(path string, data []byte) error {
	for attempt := 0; ; attempt++ {
		if err := storage.EnsureDir(filepath.Dir(path)); err != nil {
			return fmt.Errorf("ensure embed cache dir: %w", err)
		}
		err := cacheWriteFn(path, data)
		if err == nil || attempt == 1 || !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
}

// Writer returns the writer for the project whose index commit lock tx
// holds: the only way to write or delete a vector. It refuses a finished Tx,
// and it re-reads the project's regime here, under the lock, never from Get's
// memo: on a mismatch it returns ErrEmbedRegimeMismatch and nothing can be
// written. On a directory that is not built yet, the writer's first Put writes
// the sidecar (atomically, with a directory fsync) before the vector, so the
// directory never holds a vector with no sidecar.
//
// The writer satisfies indexstore.VectorWriter and indexstore.VectorFlusher:
// Tx.PutVectors and the store's commit steps write through it and flush once
// per batch.
func (c *EmbedCache) Writer(tx *indexstore.Tx) (*CacheWriter, error) {
	if tx == nil || !tx.Held() {
		return nil, errors.New("embed cache: writer needs a held index commit lock")
	}
	project := tx.Project()
	dir, err := c.dir(project)
	if err != nil {
		return nil, err
	}
	w := &CacheWriter{c: c, tx: tx, project: project, dir: dir}
	if c.fingerprint == "" {
		return w, nil
	}
	r, err := c.classify(dir)
	if err != nil {
		return nil, err
	}
	c.setRegime(project, r)
	switch r {
	case regimeMismatch:
		return nil, fmt.Errorf("%w: project %s", ErrEmbedRegimeMismatch, project)
	case regimeUnbuilt:
		w.needSidecar = true
	}
	return w, nil
}

// CacheWriter writes one project's vectors under a held index commit lock.
// Get one from EmbedCache.Writer.
type CacheWriter struct {
	c           *EmbedCache
	tx          *indexstore.Tx
	project     string
	dir         string
	needSidecar bool
	dirty       bool
}

// usable refuses a write for another project or after the Tx finished.
func (w *CacheWriter) usable(project string) error {
	if project != w.project {
		return fmt.Errorf("embed cache: writer for %s asked to write %s", w.project, project)
	}
	if !w.tx.Held() {
		return errors.New("embed cache: the writer's index commit lock was released")
	}
	return nil
}

// Put writes one vector atomically (indexstore.VectorWriter).
func (w *CacheWriter) Put(project, drawerID string, vec []float32) error {
	if err := w.usable(project); err != nil {
		return err
	}
	if w.needSidecar {
		if err := writeRetrying(filepath.Join(w.dir, storage.EmbedCacheFingerprintFile), []byte(w.c.fingerprint+"\n")); err != nil {
			return fmt.Errorf("write embed cache fingerprint: %w", err)
		}
		if err := atomicfile.SyncDir(w.dir); err != nil {
			return fmt.Errorf("sync embed cache dir: %w", err)
		}
		w.needSidecar = false
		w.c.setRegime(w.project, regimeMatch)
	}
	if err := w.c.putVector(w.dir, drawerID, vec); err != nil {
		return err
	}
	w.dirty = true
	return nil
}

// Flush makes every vector written since the last Flush durable with one
// directory fsync (indexstore.VectorFlusher).
func (w *CacheWriter) Flush() error {
	if !w.dirty {
		return nil
	}
	if err := atomicfile.SyncDir(w.dir); err != nil {
		return fmt.Errorf("sync embed cache dir: %w", err)
	}
	w.dirty = false
	return nil
}

// deleteLocked unlinks a cached vector under the index commit lock tx holds.
// A missing file is not an error. It is the engine's eviction primitive
// (evictLocked); it does not check the regime, because removing a vector is
// never a regime mix. The orphan reaper (indexstore's Tx.Reap), storage's
// layout sweep and vp_vault_split's purge remove vectors directly, because they
// act on directories this type has no handle on.
func (c *EmbedCache) deleteLocked(tx *indexstore.Tx, drawerID string) error {
	if tx == nil || !tx.Held() {
		return errors.New("embed cache: delete needs a held index commit lock")
	}
	path, err := c.path(tx.Project(), drawerID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete embed cache: %w", err)
	}
	return nil
}

// sweep runs the one-time layout sweep for this instance. A failure is logged
// and never returned: the cache still works in the new layout, and whatever the
// sweep could not move is reported by the palace-local-only check.
func (c *EmbedCache) sweep() {
	c.sweepOnce.Do(func() {
		res, err := c.vault.SweepEmbedCaches()
		if err != nil {
			slog.Warn("embed cache sweep failed", "err", err)
			return
		}
		if res.Changed() {
			slog.Info("embed cache sweep",
				"moved", res.Moved, "merged", res.Merged, "dropped", res.Dropped,
				"healed", res.Healed, "reaped", res.Reaped, "departed", res.Departed)
		}
		for _, s := range res.Tracked {
			slog.Warn("embed cache sweep: legacy cache is tracked by git; left in place", "project", s)
		}
		for _, e := range res.Errors {
			slog.Warn("embed cache sweep", "err", e)
		}
	})
}

// dir returns palace/.local/embed-cache/{project}, the one definition of where
// a project's vectors live. Every other path in this package builds on it.
func (c *EmbedCache) dir(project string) (string, error) {
	c.sweep()
	return c.vault.EmbedCacheDir(project)
}

// classify reads one project directory's regime from disk.
func (c *EmbedCache) classify(dir string) (cacheRegime, error) {
	return classifyRegime(dir, c.fingerprint)
}

// classifyRegime reads a cache directory's regime against fingerprint: the
// sidecar, and whether any vector exists. It writes nothing and loads no
// model.
func classifyRegime(dir, fingerprint string) (cacheRegime, error) {
	cur, err := os.ReadFile(filepath.Join(dir, storage.EmbedCacheFingerprintFile))
	if err == nil {
		if strings.TrimSpace(string(cur)) == fingerprint {
			return regimeMatch, nil
		}
		return regimeMismatch, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("read embed cache fingerprint: %w", err)
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return regimeUnbuilt, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read embed cache dir: %w", err)
	}
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".vec") {
			return regimeMismatch, nil
		}
	}
	return regimeUnbuilt, nil
}

// memoRegime is Get's classification. A match or a mismatch is remembered
// until Forget; "not built" is re-read each time, because another process can
// build the directory at any moment.
func (c *EmbedCache) memoRegime(project, dir string) (cacheRegime, error) {
	c.regimeMu.Lock()
	r, ok := c.regimes[project]
	c.regimeMu.Unlock()
	if ok {
		return r, nil
	}
	r, err := c.classify(dir)
	if err != nil {
		return 0, err
	}
	if r != regimeUnbuilt {
		c.setRegime(project, r)
	}
	return r, nil
}

func (c *EmbedCache) setRegime(project string, r cacheRegime) {
	c.regimeMu.Lock()
	defer c.regimeMu.Unlock()
	if r == regimeUnbuilt {
		delete(c.regimes, project)
		return
	}
	c.regimes[project] = r
}

// Forget drops Get's remembered regime for project, so the next Get reads the
// sidecar again. The engine calls it on every reload of the project: a
// rebuild in another process that replaced the regime moves the store
// counter, and the reload must see the new sidecar.
func (c *EmbedCache) Forget(project string) {
	c.setRegime(project, regimeUnbuilt)
}

// path returns palace/.local/embed-cache/{project}/{drawerID}.vec.
func (c *EmbedCache) path(project, drawerID string) (string, error) {
	dir, err := c.dir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, drawerID+".vec"), nil
}
