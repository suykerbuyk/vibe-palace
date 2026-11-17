// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func cacheAmbientSnapshot(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			snap[p] = fmt.Sprintf("%v %d %v %d", d.IsDir(), info.Size(), info.Mode(), info.ModTime().UnixNano())
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot %s: %v", root, err)
		}
	}
	return snap
}

func mustRefuseCacheWrite(t *testing.T, roots []string, op func()) {
	t.Helper()
	before := cacheAmbientSnapshot(t, roots...)
	var rec any
	func() {
		defer func() { rec = recover() }()
		op()
	}()
	if rec == nil {
		t.Fatal("expected the ambient-vault refusal, got none")
	}
	if msg := fmt.Sprint(rec); !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("refusal came from the wrong place: %v", rec)
	}
	if after := cacheAmbientSnapshot(t, roots...); !reflect.DeepEqual(before, after) {
		t.Fatalf("the refusal came after a filesystem effect: %v", snapDiff(before, after))
	}
}

// The cache writes under a held Tx, and its vault need not be the Tx's. Here
// the cache's vault is the (temp) ambient one and the Tx's is another temp
// vault, so the Tx acquires cleanly and the refusal has to come from the cache
// write itself, ahead of the directory it would create.
func TestCachePutRefusedWhenCacheVaultIsAmbient(t *testing.T) {
	cacheVault := storage.NewVault(t.TempDir())
	ensureProjectDir(t, cacheVault, "proj")
	txVault := storage.NewVault(t.TempDir())
	ensureProjectDir(t, txVault, "proj")

	tx, err := indexstore.Lock(context.Background(), txVault, "proj", 0)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	released := false
	defer func() {
		if !released {
			_ = tx.Release()
		}
	}()
	c := NewEmbedCache(cacheVault)
	w, err := c.Writer(tx)
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	restore := surface.SetAmbientVaultRoot(cacheVault.Root)
	t.Cleanup(restore)

	mustRefuseCacheWrite(t, []string{cacheVault.Root, txVault.Root}, func() {
		_ = w.Put("proj", "d1", []float32{0.1, 0.2})
	})
	if _, err := os.Lstat(filepath.Join(cacheVault.Root, "palace", ".local", "embed-cache")); err == nil {
		t.Error("the embed-cache directory was created before the refusal")
	}

	restore()
	if err := tx.Release(); err != nil {
		t.Fatalf("the Tx did not release cleanly after the refusal: %v", err)
	}
	released = true
	tx2, err := indexstore.Lock(context.Background(), txVault, "proj", 0)
	if err != nil {
		t.Fatalf("the commit lock was stranded: %v", err)
	}
	_ = tx2.Release()
}

// deleteLocked removes a vector inside the vault, so it is refused too and the
// file survives.
func TestCacheDeleteRefusedWhenCacheVaultIsAmbient(t *testing.T) {
	cacheVault := storage.NewVault(t.TempDir())
	ensureProjectDir(t, cacheVault, "proj")
	txVault := storage.NewVault(t.TempDir())
	ensureProjectDir(t, txVault, "proj")
	c := NewEmbedCache(cacheVault)
	path, err := c.path("proj", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx, err := indexstore.Lock(context.Background(), txVault, "proj", 0)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer tx.Release()
	restore := surface.SetAmbientVaultRoot(cacheVault.Root)
	t.Cleanup(restore)

	mustRefuseCacheWrite(t, []string{cacheVault.Root}, func() { _ = c.deleteLocked(tx, "d1") })
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the vector was removed despite the refusal: %v", err)
	}
}

// snapDiff names the entries that differ between two snapshots, so a failure
// prints what changed rather than the whole tree.
func snapDiff(before, after map[string]string) []string {
	var out []string
	for k, v := range after {
		if b, ok := before[k]; !ok {
			out = append(out, "created: "+k)
		} else if b != v {
			out = append(out, "changed: "+k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, "removed: "+k)
		}
	}
	sort.Strings(out)
	return out
}
