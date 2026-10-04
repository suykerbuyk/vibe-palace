// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Tests for guards the code review found untested (F3) and the deterministic
// semaphore hand-off (F4).

// TestTheSweepOutlivesACancelledFirstRequest: the engine's index sweep runs
// once (sync.Once never retries), so it must not run under the first request's
// context: a first request that is already cancelled must still leave the
// sweep done, the gone project's store removed.
func TestTheSweepOutlivesACancelledFirstRequest(t *testing.T) {
	eng, v := testEngine(t)
	ensureProjectDir(t, v, "proj")
	orphan, _ := v.IndexDir("gone")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "chunks.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = eng.Rebuild(ctx, "proj")
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("a cancelled first request cut the once-only sweep short: the gone store is still there (stat err %v)", err)
	}
}

// TestTheCacheDirectoryIsSyncedForTheSidecarAndEachBatch: on a directory not
// built yet, the sidecar is made durable (a directory fsync) before the first
// vector, and each batch is made durable by one more directory fsync (Flush);
// a later batch costs one.
func TestTheCacheDirectoryIsSyncedForTheSidecarAndEachBatch(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "proj")
	c := NewEmbedCache(v)
	c.fingerprint = embedder.Fingerprint("model-a", 0)
	dir, _ := v.EmbedCacheDir("proj")
	var mu sync.Mutex
	dirSyncs := 0
	restore := atomicfile.SetSyncObserver(func(path string) {
		if path == dir {
			mu.Lock()
			dirSyncs++
			mu.Unlock()
		}
	})
	defer restore()
	putBatch := func(ids ...string) int {
		mu.Lock()
		dirSyncs = 0
		mu.Unlock()
		tx, err := indexstore.Lock(context.Background(), v, "proj", indexstore.NoTimeout)
		if err != nil {
			t.Fatal(err)
		}
		vecs := map[string][]float32{}
		for _, id := range ids {
			vecs[id] = []float32{1, 2}
		}
		if err := tx.PutVectors(mustWriter(t, c, tx), vecs); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return dirSyncs
	}
	if n := putBatch("a", "b", "c"); n != 2 {
		t.Fatalf("first batch on an unbuilt directory: %d directory syncs, want 2 (the sidecar, then the batch)", n)
	}
	if n := putBatch("d", "e"); n != 1 {
		t.Fatalf("a later batch: %d directory syncs, want 1 (the batch)", n)
	}
}

// TestABusyWarmProjectServesTheIndexItHolds: after a capture insert timed out
// (the project is marked out of date but its index is still in memory), a
// search that finds the project's mutex busy serves that index instead of
// returning the first-build answer.
func TestABusyWarmProjectServesTheIndexItHolds(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	old := addDrawer(t, v, "proj", "wing", "room", "a drawer in the loaded index", "facts")
	warmProject(t, eng, v, "proj")

	held, err := indexstore.Lock(ctx, v, "proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	withSearchLockTimeout(t, 0)
	d := addDrawer(t, v, "proj", "wing", "room", "a captured drawer", "facts")
	if err := eng.IndexDrawers(ctx, []DrawerInput{{Project: "proj", Wing: "wing", Room: "room", Drawer: d, Vec: unitVec(5)}}); err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	eng.buildMu.Lock()
	_, current := eng.loaded["proj"]
	eng.buildMu.Unlock()
	if current {
		t.Fatal("precondition: the timed-out insert did not mark the project out of date")
	}

	release := holdMutexBlockedOnCommit(t, eng, v, "proj", storage.Drawer{ID: "blocked", Content: "x"})
	defer release()
	res, err := eng.Search(ctx, old.Content, SearchFilters{Project: "proj", Limit: 5})
	if errors.Is(err, ErrIndexNotReady) || err != nil {
		t.Fatalf("a busy project with its index in memory: %v, want the loaded index served", err)
	}
	if !hasContent(res, old.Content) {
		t.Fatalf("the loaded index was not served: %+v", res)
	}
}

// TestSemaphoreHandOffNeverLeaks (F4): the semaphore is handed over only once
// every acquirer is waiting in acquireSem's second select (the semWaitHook),
// so each round's winner takes it there. Acquirer 0 waits far longer than any
// hand-off and is never cancelled, so a winner in the second select is certain; the others
// have millisecond timeouts, and half of them have their context cancelled
// mid-wait (after they are known to be waiting). An acquirer that took the
// semaphore and still returned an error would leak it: after every round a
// zero-timeout acquire must succeed.
func TestSemaphoreHandOffNeverLeaks(t *testing.T) {
	const acquirers = 8
	for round := range 50 {
		sem := make(chan struct{}, 1)
		sem <- struct{}{} // the holder
		waiting := make(chan struct{}, acquirers)
		semWaitHook = func() { waiting <- struct{}{} }
		var wg sync.WaitGroup
		var won atomic.Int64
		cancels := make([]context.CancelFunc, acquirers)
		for i := range acquirers {
			ctx, cancel := context.WithCancel(context.Background())
			cancels[i] = cancel
			timeout := time.Duration(5+i) * time.Millisecond
			if i == 0 {
				// Far longer than any hand-off: certain to be waiting when the
				// holder releases, yet finite, so a leaked semaphore fails the
				// final check instead of hanging the test.
				timeout = 10 * time.Second
			}
			wg.Go(func() {
				defer cancel()
				if err := acquireSem(ctx, sem, timeout); err == nil {
					won.Add(1)
					<-sem
				}
			})
		}
		for range acquirers {
			<-waiting // every acquirer is past its first try and about to wait
		}
		for i := 2; i < acquirers; i += 2 {
			go func() { time.Sleep(time.Duration(i/2) * time.Millisecond); cancels[i]() }()
		}
		<-sem // hand over
		wg.Wait()
		semWaitHook = nil
		if won.Load() == 0 {
			t.Fatalf("round %d: no acquirer took the handed-over semaphore", round)
		}
		if err := acquireSem(context.Background(), sem, 0); err != nil {
			t.Fatalf("round %d: the semaphore leaked (%d acquired): %v", round, won.Load(), err)
		}
	}
}
