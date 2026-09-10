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
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// cacheWriteFile is the file write Put uses. It is a variable only so a test can
// make the first write meet a directory that vanished under it, which is the
// race Put's retry exists for and which no test can otherwise reach on demand.
var cacheWriteFile = os.WriteFile

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
type EmbedCache struct {
	vault     *storage.Vault
	sweepOnce sync.Once
}

// NewEmbedCache creates a new embedding cache backed by the vault.
func NewEmbedCache(vault *storage.Vault) *EmbedCache {
	return &EmbedCache{vault: vault}
}

// Get returns a cached embedding vector, or nil if not cached.
func (c *EmbedCache) Get(project, drawerID string) ([]float32, error) {
	path, err := c.path(project, drawerID)
	if err != nil {
		return nil, err
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

// Put stores an embedding vector in the cache.
//
// The write is retried ONCE after re-creating the directory when it fails with
// ENOENT. The directory can vanish between EnsureDir and the write: a sweep in
// another process that saw the target absent renames the legacy cache onto the
// path, and rename(2) silently replaces an EMPTY directory — the one EnsureDir
// just made — so the open lands in a directory that no longer exists. After the
// rename the path is a real directory again, and the retry lands the vector in
// it. (A concurrent orphan reap of a slug in neither tree can do the same.)
func (c *EmbedCache) Put(project, drawerID string, vec []float32) error {
	path, err := c.path(project, drawerID)
	if err != nil {
		return err
	}

	data := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(v))
	}

	for attempt := 0; ; attempt++ {
		if err := storage.EnsureDir(filepath.Dir(path)); err != nil {
			return fmt.Errorf("ensure embed cache dir: %w", err)
		}
		err := cacheWriteFile(path, data, 0644)
		if err == nil || attempt == 1 || !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
}

// Delete unlinks a cached embedding vector. A missing file is not an error,
// so calling Delete for a drawer that was never cached is a no-op. It is the
// engine's eviction primitive — RemoveDrawer and the engine's orphan reaper
// (reapOrphanVectors) route the on-disk unlink through it. storage's layout
// sweep and vp_vault_split's purge also remove vectors, directly, because they
// act on whole directories this type has no handle on.
func (c *EmbedCache) Delete(project, drawerID string) error {
	path, err := c.path(project, drawerID)
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
				"healed", res.Healed, "reaped", res.Reaped)
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

// path returns palace/.local/embed-cache/{project}/{drawerID}.vec.
func (c *EmbedCache) path(project, drawerID string) (string, error) {
	dir, err := c.dir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, drawerID+".vec"), nil
}
