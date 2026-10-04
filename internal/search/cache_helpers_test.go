// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ensureProjectDir makes project exist in the vault (Projects/<project>/), as
// indexstore.Lock requires.
func ensureProjectDir(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", project), 0o755); err != nil {
		t.Fatal(err)
	}
}

// cachePut writes one vector the way production does: under the project's
// index commit lock, through the cache's Writer and Tx.PutVectors.
func cachePut(t *testing.T, c *EmbedCache, project, id string, vec []float32) error {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), c.vault, project, indexstore.NoTimeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	w, err := c.Writer(tx)
	if err != nil {
		return err
	}
	if err := tx.PutVectors(w, map[string][]float32{id: vec}); err != nil {
		return err
	}
	return tx.Commit()
}

// cacheDelete unlinks one vector under the project's index commit lock.
func cacheDelete(t *testing.T, c *EmbedCache, project, id string) error {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), c.vault, project, indexstore.NoTimeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	return c.deleteLocked(tx, id)
}

// reapProject runs the engine's reap the way a Rebuild's final commit does:
// the project's lock, one Tx, reapLocked.
func reapProject(t *testing.T, eng *Engine, project string) (int, error) {
	t.Helper()
	ctx := context.Background()
	pl, err := eng.lockProject(ctx, project, indexstore.NoTimeout)
	if err != nil {
		return 0, err
	}
	defer pl.release()
	tx, err := pl.Tx(ctx, indexstore.NoTimeout)
	if errors.Is(err, indexstore.ErrProjectGone) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := eng.reapLocked(tx, project)
	if err != nil {
		return n, err
	}
	_, _, err = pl.finishTx()
	return n, err
}

// mustWriter is the cache's writer for tx, failing the test if it is refused.
func mustWriter(t *testing.T, c *EmbedCache, tx *indexstore.Tx) indexstore.VectorWriter {
	t.Helper()
	w, err := c.Writer(tx)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// warmProject makes project exist and builds it, so the engine holds it as
// current: an IndexDrawers that follows inserts in memory at once instead of
// deferring to the next search's build (Scope 6, the cold insert). Tests that
// index drawers which are not on disk need this, because a later build reads
// only what is on disk.
func warmProject(t *testing.T, eng *Engine, v *storage.Vault, project string) {
	t.Helper()
	ensureProjectDir(t, v, project)
	if _, err := eng.Rebuild(context.Background(), project); err != nil {
		t.Fatal(err)
	}
}

// markLoaded records project as current at the store's present counter, for
// a test that installs an in-memory index by hand.
func markLoaded(t *testing.T, eng *Engine, project string) {
	t.Helper()
	g, err := indexstore.ReadGeneration(eng.vault, project)
	if err != nil {
		t.Fatal(err)
	}
	eng.buildMu.Lock()
	eng.loaded[project] = g
	eng.buildMu.Unlock()
}
