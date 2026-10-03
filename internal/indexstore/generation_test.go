// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
)

func readGen(t *testing.T, v interface {
	IndexGenerationPath(string) (string, error)
}, p string) Gen {
	t.Helper()
	path, err := v.IndexGenerationPath(p)
	if err != nil {
		t.Fatal(err)
	}
	g, err := readGenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// The store counter: a Tx that wrote nothing leaves it alone; an append moves
// gen by one and keeps the epoch; a write that is more than an append moves gen
// and changes the epoch; writes recorded before a Release count as before a
// Commit; the counter survives the deletion of index/<p>/; and a counter
// deleted by hand is recreated with a different epoch.
func TestStoreCounter(t *testing.T) {
	v := newVault(t)
	if g, err := ReadGeneration(v, "alpha"); err != nil || g != (Gen{}) {
		t.Fatalf("ReadGeneration before any Lock = %+v, %v; want the zero Gen", g, err)
	}

	commit := func(writes ...bool) Gen {
		t.Helper()
		tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
		for _, more := range writes {
			tx.noteWrite(more)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		g, err := ReadGeneration(v, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		return g
	}

	g0 := commit()
	if g0.Gen != 0 || g0.Epoch == 0 {
		t.Fatalf("a Tx that wrote nothing: %+v, want gen 0 and a non-zero epoch", g0)
	}
	if g := commit(); g != g0 {
		t.Fatalf("a second empty Tx moved the counter: %+v -> %+v", g0, g)
	}
	g1 := commit(false, false)
	if g1.Gen != 1 || g1.Epoch != g0.Epoch {
		t.Fatalf("one append commit: %+v -> %+v, want gen+1 and the same epoch", g0, g1)
	}
	g2 := commit(false, true)
	if g2.Gen != 2 || g2.Epoch == g1.Epoch {
		t.Fatalf("a commit that did more than append: %+v -> %+v, want gen+1 and a new epoch", g1, g2)
	}

	tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
	tx.noteWrite(false)
	if err := tx.Release(); err != nil {
		t.Fatal(err)
	}
	if g := readGen(t, v, "alpha"); g.Gen != 3 || g.Epoch != g2.Epoch {
		t.Fatalf("writes then Release: %+v, want gen 3 and the same epoch", g)
	}
	if err := tx.Release(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit after Release succeeded")
	}

	dir, _ := v.IndexDir("alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if g := readGen(t, v, "alpha"); g.Gen != 3 {
		t.Fatalf("deleting index/alpha/ reset the counter: %+v", g)
	}

	// A counter deleted by hand and recreated never matches the epoch an
	// engine remembers. Checked on beta from a fresh counter, so an epoch that
	// merely counts from a fixed start would recreate the same value.
	fresh := func() Gen {
		t.Helper()
		tx := mustLock(t)(Lock(context.Background(), v, "beta", NoTimeout))
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return readGen(t, v, "beta")
	}
	betaPath, _ := v.IndexGenerationPath("beta")
	old := fresh()
	if err := os.Remove(betaPath); err != nil {
		t.Fatal(err)
	}
	if g := fresh(); g.Epoch == old.Epoch || g.Epoch == 0 {
		t.Fatalf("a recreated counter has epoch %d, the deleted one had %d; it must differ", g.Epoch, old.Epoch)
	}
}

// The counter file is "gen epoch" in decimal; a malformed one is an error, not
// a silent zero, and nothing but the counter is left in .generation/.
func TestGenerationFileFormat(t *testing.T) {
	v := newVault(t)
	tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
	tx.noteWrite(true)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	path, _ := v.IndexGenerationPath("alpha")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	g := readGen(t, v, "alpha")
	if want := []byte(formatGen(g)); string(data) != string(want) {
		t.Fatalf("counter file = %q, want %q", data, want)
	}
	ents, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "alpha" {
		t.Fatalf(".generation/ holds %v, want only alpha (no temp files left behind)", ents)
	}
	for _, bad := range []string{"", "1", "1 0\n", "x 2\n", "1 2 3\n"} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadGeneration(v, "alpha"); err == nil {
			t.Fatalf("ReadGeneration accepted %q", bad)
		}
	}
}

// A malformed counter does not wedge the project: a lock-free reader reports
// it, and the next Lock replaces it with Gen 0 and a new epoch, which matches
// nothing an engine remembers, so every engine reloads in full.
func TestMalformedCounterIsReplacedUnderTheLock(t *testing.T) {
	v := newVault(t)
	tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
	tx.noteWrite(false)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := tx.Generation()
	path, _ := v.IndexGenerationPath("alpha")
	for _, bad := range []string{"", "7 ", "x 2\n"} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadGeneration(v, "alpha"); err == nil {
			t.Fatalf("ReadGeneration accepted %q", bad)
		}
		tx, err := Lock(context.Background(), v, "alpha", 0)
		if err != nil {
			t.Fatalf("Lock with the counter %q: %v", bad, err)
		}
		g := tx.Generation()
		_ = tx.Release()
		if g.Gen != 0 || g.Epoch == 0 || g.Epoch == before.Epoch {
			t.Fatalf("counter %q replaced by %+v, want gen 0 and an epoch other than %d", bad, g, before.Epoch)
		}
		if disk := readGen(t, v, "alpha"); disk != g {
			t.Fatalf("the replacement was not written: disk %+v, Tx %+v", disk, g)
		}
	}
}

// Each counter write is durable: the temp file is fsynced before its rename
// and the directory after it. The same holds for the holder record.
func TestCounterAndHolderWritesAreFsynced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("atomicfile does not fsync directories on windows")
	}
	v := newVault(t)
	var mu sync.Mutex
	var synced []string
	restore := atomicfile.SetSyncObserver(func(p string) { mu.Lock(); synced = append(synced, p); mu.Unlock() })
	defer restore()

	check := func(what, target string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		dir := filepath.Dir(target)
		var temp, dsync bool
		for _, s := range synced {
			if s == dir {
				dsync = true
			} else if filepath.Dir(s) == dir && strings.HasPrefix(filepath.Base(s), ".vp-atomic-") {
				temp = true
			}
		}
		if !temp || !dsync {
			t.Fatalf("%s: temp file fsynced=%v, directory fsynced=%v (synced %v); want both", what, temp, dsync, synced)
		}
		synced = nil
	}

	tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
	tx.noteWrite(true)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	genPath, _ := v.IndexGenerationPath("alpha")
	check("counter", genPath)

	rl := mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
	check("holder record", v.IndexRunHolderPath())
	_ = rl.Release()
}
