// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

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
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// These rows pin the under-test vault tripwire for EVERY indexstore mutation
// primitive. Everything the package writes lives under {vault}/palace/.local,
// and every one of its writers passes an empty vaultPath to atomicfile or goes
// around atomicfile altogether, so the vault-root arm of the tripwire never saw
// any of it.
//
// 🔴 NEVER THE REAL VAULT. Each row registers a t.TempDir() vault as the
// "ambient" configured root with surface.SetAmbientVaultRoot and restores it on
// cleanup. Nothing here resolves or touches the operator's configuration.

// ambientVault returns a temp vault (projects alpha and beta) installed as the
// ambient configured vault, and the function that restores the previous root.
func ambientVault(t *testing.T) (*storage.Vault, func()) {
	t.Helper()
	v := newVault(t)
	restore := surface.SetAmbientVaultRoot(v.Root)
	t.Cleanup(restore)
	return v, restore
}

// treeSnapshot records every entry under each root: kind, size, mode and mtime.
// A directory's mtime moves when a child is created or removed, so an entry
// created and removed again is still seen.
func treeSnapshot(t *testing.T, roots ...string) map[string]string {
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

// mustRefuseCreatingNothing runs op and fails unless it panics with the
// ambient-vault refusal AND the trees under roots are byte-for-byte the shape
// they were before: nothing created, removed or changed ahead of the refusal.
func mustRefuseCreatingNothing(t *testing.T, roots []string, op func()) {
	t.Helper()
	before := treeSnapshot(t, roots...)
	var rec any
	func() {
		defer func() { rec = recover() }()
		op()
	}()
	if rec == nil {
		t.Fatal("expected a panic refusing a write inside the ambient vault, got none")
	}
	if msg := fmt.Sprint(rec); !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("panic came from the wrong place: %v", rec)
	}
	if after := treeSnapshot(t, roots...); !reflect.DeepEqual(before, after) {
		t.Fatalf("the refusal came after a filesystem effect: %v", snapDiff(before, after))
	}
}

func TestIndexstoreRefusesAmbientVault(t *testing.T) {
	ctx := context.Background()

	t.Run("writeFile", func(t *testing.T) {
		v := newVault(t)
		path, _ := v.IndexGenerationPath("alpha")
		restore := surface.SetAmbientVaultRoot(v.Root)
		defer restore()
		mustRefuseCreatingNothing(t, []string{v.Root}, func() { _ = writeFile(path, []byte("1\n")) })
	})

	t.Run("appendFile", func(t *testing.T) {
		v, _ := ambientVault(t)
		dir, _ := v.IndexDir("alpha")
		mustRefuseCreatingNothing(t, []string{v.Root}, func() { _ = appendFile(filepath.Join(dir, "x.jsonl"), []byte("x\n")) })
	})

	t.Run("renameDir", func(t *testing.T) {
		v := newVault(t)
		src := filepath.Join(v.Root, "palace", ".local", "index", "src")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(v.Root, "palace", ".local", "index", "dst")
		restore := surface.SetAmbientVaultRoot(v.Root)
		defer restore()
		mustRefuseCreatingNothing(t, []string{v.Root}, func() { _ = renameDir(src, dst) })
	})

	// One-sided rows: each argument must be checked on its own, so a rename out
	// of, or into, the ambient vault from or to an ordinary temp dir is refused.
	t.Run("renameDir_from_ambient", func(t *testing.T) {
		v, other := newVault(t), t.TempDir()
		src := filepath.Join(v.Root, "palace", ".local", "index", "src")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		restore := surface.SetAmbientVaultRoot(v.Root)
		defer restore()
		mustRefuseCreatingNothing(t, []string{v.Root, other}, func() { _ = renameDir(src, filepath.Join(other, "dst")) })
	})

	t.Run("renameDir_to_ambient", func(t *testing.T) {
		v, other := newVault(t), t.TempDir()
		src := filepath.Join(other, "src")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		restore := surface.SetAmbientVaultRoot(v.Root)
		defer restore()
		mustRefuseCreatingNothing(t, []string{v.Root, other}, func() { _ = renameDir(src, filepath.Join(v.Root, "palace", "dst")) })
	})

	t.Run("removeFile", func(t *testing.T) {
		v := newVault(t)
		f := filepath.Join(v.Root, "palace", ".local", "index", "victim")
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		touch(t, f)
		restore := surface.SetAmbientVaultRoot(v.Root)
		defer restore()
		mustRefuseCreatingNothing(t, []string{v.Root}, func() { _ = removeFile(f) })
	})

	t.Run("removeTree", func(t *testing.T) {
		v := newVault(t)
		d := filepath.Join(v.Root, "palace", ".local", "index", "tomb")
		if err := os.MkdirAll(filepath.Join(d, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		touch(t, filepath.Join(d, "sub", "f"))
		restore := surface.SetAmbientVaultRoot(v.Root)
		defer restore()
		mustRefuseCreatingNothing(t, []string{v.Root}, func() { _ = removeTree(d) })
	})

	t.Run("mkdirAll", func(t *testing.T) {
		v, _ := ambientVault(t)
		mustRefuseCreatingNothing(t, []string{v.Root}, func() { _ = mkdirAll(v.IndexLocksDir()) })
	})

	t.Run("WriteRunCompleted_mkdir", func(t *testing.T) {
		v, _ := ambientVault(t)
		mustRefuseCreatingNothing(t, []string{v.Root}, func() { _ = WriteRunCompleted(v, time.Now()) })
	})

	t.Run("writeInboxContents_mkdir", func(t *testing.T) {
		v, _ := ambientVault(t)
		dir := filepath.Join(v.Root, "palace", ".local", "inbox")
		mustRefuseCreatingNothing(t, []string{v.Root}, func() {
			_ = writeInboxContents(filepath.Join(dir, "alpha"), dir, inboxContents{rebuild: true})
		})
	})

	t.Run("Lock", func(t *testing.T) {
		v, _ := ambientVault(t)
		mustRefuseCreatingNothing(t, []string{v.Root}, func() {
			if tx, err := Lock(ctx, v, "alpha", 0); err == nil {
				_ = tx.Release()
			}
		})
	})

	t.Run("LockLifecycle", func(t *testing.T) {
		v, _ := ambientVault(t)
		mustRefuseCreatingNothing(t, []string{v.Root}, func() {
			if lt, err := LockLifecycle(ctx, v, "alpha", 0); err == nil {
				_ = lt.Release()
			}
		})
	})

	t.Run("TryRunLock", func(t *testing.T) {
		v, _ := ambientVault(t)
		mustRefuseCreatingNothing(t, []string{v.Root}, func() {
			if rl, ok, err := TryRunLock(v, KindIngest, "alpha"); err == nil && ok {
				_ = rl.Release()
			}
		})
	})

	t.Run("tryRunLockFile", func(t *testing.T) {
		v, _ := ambientVault(t)
		mustRefuseCreatingNothing(t, []string{v.Root}, func() {
			if release, ok, err := tryRunLockFile(v); err == nil && ok {
				_ = release()
			}
		})
	})

	t.Run("runLockHeld", func(t *testing.T) {
		v, _ := ambientVault(t)
		mustRefuseCreatingNothing(t, []string{v.Root}, func() { _ = runLockHeld(v) })
	})

	t.Run("DeleteLegacyLedgerIfUntracked", func(t *testing.T) {
		v := newVault(t)
		ledger, _ := v.IngestedArchivesFile("alpha")
		if err := os.MkdirAll(filepath.Dir(ledger), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ledger, []byte(`{"source_sha256":"A"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		restore := surface.SetAmbientVaultRoot(v.Root)
		defer restore()
		mustRefuseCreatingNothing(t, []string{v.Root}, func() { _, _ = DeleteLegacyLedgerIfUntracked(v, "alpha") })
	})
}

// A refusal must leave NO lock held. Each row takes the lock, recovers the
// refusal, restores the real ambient root, and then takes the same lock again
// with timeout 0: ErrLockWaitTimeout would mean the refusal stranded a flock.
func TestAmbientRefusalLeavesNoLockHeld(t *testing.T) {
	ctx := context.Background()

	refuse := func(t *testing.T, op func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatal("expected the ambient-vault refusal, got none")
			}
		}()
		op()
	}

	t.Run("commit_lock", func(t *testing.T) {
		v, restore := ambientVault(t)
		refuse(t, func() {
			if tx, err := Lock(ctx, v, "alpha", 0); err == nil {
				_ = tx.Release()
			}
		})
		restore()
		tx, err := Lock(ctx, v, "alpha", 0)
		if err != nil {
			t.Fatalf("the commit lock was stranded by the refusal: %v", err)
		}
		_ = tx.Release()
	})

	t.Run("lifecycle_commit_lock", func(t *testing.T) {
		v, restore := ambientVault(t)
		refuse(t, func() {
			if lt, err := LockLifecycle(ctx, v, "alpha", 0); err == nil {
				_ = lt.Release()
			}
		})
		restore()
		lt, err := LockLifecycle(ctx, v, "alpha", 0)
		if err != nil {
			t.Fatalf("the lifecycle commit lock was stranded by the refusal: %v", err)
		}
		_ = lt.Release()
	})

	t.Run("run_lock", func(t *testing.T) {
		v, restore := ambientVault(t)
		refuse(t, func() {
			if rl, ok, err := TryRunLock(v, KindIngest, "alpha"); err == nil && ok {
				_ = rl.Release()
			}
		})
		restore()
		rl, ok, err := TryRunLock(v, KindIngest, "alpha")
		if err != nil || !ok {
			t.Fatalf("the run lock was stranded by the refusal: ok=%v err=%v", ok, err)
		}
		_ = rl.Release()
	})
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

// The lock directory is a SIBLING of the index root, not a child, so a guard on
// the index root alone does not judge the path a lock acquirer creates. Here
// vault B's palace/.local/locks is a symlink into the ambient vault A's, and B's
// own index root is an ordinary temp path: only a guard on the locks directory
// sees that tryRunLockFile and runLockHeld are about to create a file inside A.
func TestLockAcquirersRefuseALocksDirAliasedIntoTheAmbientVault(t *testing.T) {
	a, b := newVault(t), newVault(t)
	if err := os.MkdirAll(a.IndexLocksDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(b.IndexLocksDir()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a.IndexLocksDir(), b.IndexLocksDir()); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	restore := surface.SetAmbientVaultRoot(a.Root)
	t.Cleanup(restore)

	t.Run("tryRunLockFile", func(t *testing.T) {
		mustRefuseCreatingNothing(t, []string{a.Root, b.Root}, func() {
			if release, ok, err := tryRunLockFile(b); err == nil && ok {
				_ = release()
			}
		})
	})
	t.Run("runLockHeld", func(t *testing.T) {
		mustRefuseCreatingNothing(t, []string{a.Root, b.Root}, func() { _ = runLockHeld(b) })
	})
	t.Run("TryRunLock", func(t *testing.T) {
		mustRefuseCreatingNothing(t, []string{a.Root, b.Root}, func() {
			if rl, ok, err := TryRunLock(b, KindIngest, "alpha"); err == nil && ok {
				_ = rl.Release()
			}
		})
	})
}
