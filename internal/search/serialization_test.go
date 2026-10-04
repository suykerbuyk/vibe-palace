// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// Tests for the per-project serialization of the engine's writers (task
// search-index-completeness-and-build-serialization, Scope 9 and the plan
// revisions of 2026-10-04). No row asserts a duration: blocking is observed
// through the lock recorder and hooks, never through sleeps.

// withSearchLockTimeout sets the search path's lock timeout for one test.
func withSearchLockTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := searchLockTimeout
	searchLockTimeout = d
	t.Cleanup(func() { searchLockTimeout = old })
}

// recordLocks installs the lock recorder and returns a snapshot function.
func recordLocks(t *testing.T) func() []lockEvent {
	t.Helper()
	var mu sync.Mutex
	var evs []lockEvent
	lockRecorder = func(ev lockEvent) {
		mu.Lock()
		evs = append(evs, ev)
		mu.Unlock()
	}
	t.Cleanup(func() { lockRecorder = nil })
	return func() []lockEvent {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(evs)
	}
}

// waitFor polls the recorder until want appears; the generous bound is a
// deadlock detector, never a timing assertion.
func waitFor(t *testing.T, snap func() []lockEvent, want lockEvent) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(snap(), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("lock event %+v never happened", want)
}

// blockingEmbedder blocks every EmbedBatch until release is closed, and
// reports the first call on started.
type blockingEmbedder struct {
	embedder.Embedder
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.Embedder.EmbedBatch(ctx, texts)
}

// TestRebuildAndIndexDrawersInterleave: a Rebuild held mid-embed by a blocking
// embedder and an IndexDrawers on the same project are serialized by the
// project mutex. The insert waits for the build, lands after it, and is
// searchable with its vector on disk; the build's swap can no longer drop it,
// nor its reap unlink the vector. A regression guard (2-N1): never run at the
// old HEAD.
func TestRebuildAndIndexDrawersInterleave(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "proj")
	addDrawer(t, v, "proj", "wing", "room", "a drawer the build embeds", "facts")
	be := &blockingEmbedder{Embedder: embedder.NewMock(384), started: make(chan struct{}), release: make(chan struct{})}
	eng := NewEngine(be, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })
	snap := recordLocks(t)
	ctx := context.Background()

	var rerr error
	built := make(chan struct{})
	go func() {
		defer close(built)
		_, rerr = eng.Rebuild(ctx, "proj")
	}()
	<-be.started

	inserted := addDrawer(t, v, "proj", "wing", "room", "a drawer captured during the build", "facts")
	mock := embedder.NewMock(384)
	vec, _ := mock.Embed(ctx, inserted.Content)
	var ierr error
	indexed := make(chan struct{})
	go func() {
		defer close(indexed)
		ierr = eng.indexDrawers(ctx, []DrawerInput{{Project: "proj", Wing: "wing", Room: "room", Drawer: inserted, Vec: vec}}, indexstore.NoTimeout)
	}()
	waitFor(t, snap, lockEvent{"mutex-wait", "proj"})
	close(be.release)
	<-built
	<-indexed
	if rerr != nil || ierr != nil {
		t.Fatalf("Rebuild: %v; IndexDrawers: %v", rerr, ierr)
	}

	p, _ := eng.cache.path("proj", inserted.ID)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the inserted drawer's vector is gone: %v", err)
	}
	res, err := eng.Search(ctx, inserted.Content, SearchFilters{Project: "proj", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].DrawerID != inserted.ID {
		t.Fatalf("the drawer inserted during the build is not searchable: %+v", res)
	}
}

// TestLockOrder: every multi-project batch takes its projects one at a time,
// in sorted slug order; each project's mutex before its commit lock; each
// commit lock released before the next project's mutex is taken. At no point
// are two commit locks held (the leaf rule).
func TestLockOrder(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	for _, p := range []string{"a", "b", "c"} {
		warmProject(t, eng, v, p)
	}
	in := func(p string) DrawerInput {
		return DrawerInput{Project: p, Wing: "w", Room: "r", Drawer: storage.Drawer{ID: "id-" + p, Content: "content of " + p}, Vec: unitVec(0)}
	}
	for _, batch := range [][]string{{"b", "a"}, {"c", "a", "b"}, {"a"}} {
		snap := recordLocks(t)
		var inputs []DrawerInput
		for _, p := range batch {
			inputs = append(inputs, in(p))
		}
		if err := eng.IndexDrawers(ctx, inputs); err != nil {
			t.Fatal(err)
		}
		var order []string
		mutexHeld, commitHeld := map[string]bool{}, 0
		for _, ev := range snap() {
			switch ev.kind {
			case "mutex-acquired":
				if len(mutexHeld) > 0 {
					t.Fatalf("batch %v: %s's mutex taken while %v held: %+v", batch, ev.project, mutexHeld, snap())
				}
				mutexHeld[ev.project] = true
				order = append(order, ev.project)
			case "commit-acquired":
				if !mutexHeld[ev.project] {
					t.Fatalf("batch %v: %s's commit lock taken before its mutex", batch, ev.project)
				}
				if commitHeld > 0 {
					t.Fatalf("batch %v: two commit locks held at once", batch)
				}
				commitHeld++
			case "commit-released":
				commitHeld--
			case "mutex-released":
				if commitHeld > 0 {
					t.Fatalf("batch %v: %s's mutex released while its commit lock is held", batch, ev.project)
				}
				delete(mutexHeld, ev.project)
			}
		}
		want := slices.Sorted(slices.Values(batch))
		if !slices.Equal(order, want) {
			t.Fatalf("batch %v: projects taken in order %v, want %v", batch, order, want)
		}
	}
}

// TestSemaphoreStormLeavesItFree: many acquirers whose context is cancelled
// or whose timeout runs out race a holder that releases. Whatever each one
// saw, an acquire that returned an error never holds the semaphore and one
// that returned nil releases it, so afterwards a zero-timeout acquire succeeds.
func TestSemaphoreStormLeavesItFree(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // the holder
	var wg sync.WaitGroup
	var got atomic.Int64
	for i := range 200 {
		wg.Go(func() {
			ctx, cancel := context.WithCancel(context.Background())
			if i%2 == 0 {
				cancel()
			} else {
				defer cancel()
			}
			if err := acquireSem(ctx, sem, time.Duration(i%3)*time.Microsecond); err == nil {
				got.Add(1)
				<-sem
			}
		})
	}
	<-sem // the holder releases mid-storm
	wg.Wait()
	if err := acquireSem(context.Background(), sem, 0); err != nil {
		t.Fatalf("after the storm (%d acquired) the semaphore is not free: %v", got.Load(), err)
	}
}

// holdMutexBlockedOnCommit makes engine eng hold project's mutex while it is
// blocked on the project's commit lock, which the test holds: the shape of an
// waiting IndexDrawers (no lock timeout) behind another process's ingest.
// It returns once that state is reached, and a function that releases the
// commit lock and waits for the blocked writer to finish.
func holdMutexBlockedOnCommit(t *testing.T, eng *Engine, v *storage.Vault, project string, d storage.Drawer) func() {
	t.Helper()
	ctx := context.Background()
	held, err := indexstore.Lock(ctx, v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	var once sync.Once
	beforeCommitLockHook = func(p string) {
		if p == project {
			once.Do(func() { close(blocked) })
		}
	}
	t.Cleanup(func() { beforeCommitLockHook = nil })
	done := make(chan error, 1)
	go func() {
		done <- eng.indexDrawers(ctx, []DrawerInput{{Project: project, Wing: "wing", Room: "room", Drawer: d, Vec: unitVec(2)}}, indexstore.NoTimeout)
	}()
	<-blocked
	return func() {
		if err := held.Release(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// TestZeroTimeoutSearchDoesNotWaitOnTheProjectMutex: the engine is warm on P,
// and another process has since committed to P (the counter moved), so the
// next search must attempt a rebuild. Engine A's own waiting IndexDrawers holds
// P's mutex, blocked on the commit lock another process holds. A search with a
// zero lock timeout returns the loaded index at once instead of waiting, and
// keeps P marked out of date: once the writer is through, the next search
// rebuilds and sees what the other process committed.
func TestZeroTimeoutSearchDoesNotWaitOnTheProjectMutex(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	old := addDrawer(t, v, "proj", "wing", "room", "a drawer loaded before the busy spell", "facts")
	warmProject(t, eng, v, "proj")
	g0, _ := indexstore.ReadGeneration(v, "proj")

	// Another process commits: a drawer on disk and a vector batch, which
	// moves the counter.
	late := addDrawer(t, v, "proj", "wing", "room", "a drawer another process added", "facts")
	other := NewEngine(embedder.NewMock(384), v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { other.Close() })
	if _, err := other.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	if g1, _ := indexstore.ReadGeneration(v, "proj"); g1 == g0 {
		t.Fatal("precondition: the other process's commit did not move the counter; the search would not try to rebuild")
	}

	release := holdMutexBlockedOnCommit(t, eng, v, "proj", storage.Drawer{ID: "blocked", Content: "x"})
	withSearchLockTimeout(t, 0)
	snap := recordLocks(t)
	res, err := eng.Search(ctx, old.Content, SearchFilters{Project: "proj", Limit: 5})
	if err != nil {
		t.Fatalf("a busy project failed the search: %v", err)
	}
	if len(res) == 0 || res[0].DrawerID != old.ID {
		t.Fatalf("the loaded index was not served: %+v", res)
	}
	if !slices.Contains(snap(), lockEvent{"mutex-wait", "proj"}) {
		t.Fatal("the search never tried to rebuild: the row would pass vacuously")
	}
	if _, found := findHitContaining(mustSearch(t, eng, late.Content), late.Content); found {
		t.Fatal("the busy search rebuilt anyway")
	}

	release()
	withSearchLockTimeout(t, time.Minute)
	if _, found := findHitContaining(mustSearch(t, eng, late.Content), late.Content); !found {
		t.Fatal("after the writer finished, the next search did not rebuild: the project was marked current while busy")
	}
}

// unitVec is a 384-dimension test vector, the mock embedder's size.
func unitVec(i int) []float32 {
	v := make([]float32, 384)
	v[i] = 1
	return v
}

func mustSearch(t *testing.T, eng *Engine, q string) []SearchResult {
	t.Helper()
	res, err := eng.Search(context.Background(), q, SearchFilters{Project: "proj", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestColdSearchOnABusyProject: with nothing of P in memory and P's mutex
// held by a writer blocked on another process's commit, a zero-timeout search
// returns the first-build answer, ErrIndexNotReady, without waiting.
func TestColdSearchOnABusyProject(t *testing.T) {
	eng, v := testEngine(t)
	ensureProjectDir(t, v, "proj")
	addDrawer(t, v, "proj", "wing", "room", "content", "facts")
	release := holdMutexBlockedOnCommit(t, eng, v, "proj", storage.Drawer{ID: "blocked", Content: "x"})
	defer release()
	withSearchLockTimeout(t, 0)
	_, err := eng.Search(context.Background(), "content", SearchFilters{Project: "proj"})
	if !errors.Is(err, ErrIndexNotReady) {
		t.Fatalf("cold search on a busy project: %v, want ErrIndexNotReady", err)
	}
}

// TestColdSearchJoinsAnInFlightBuild: a cold search that arrives while
// another search is building the project joins that build instead of
// returning the first-build answer, even with a zero lock timeout.
func TestColdSearchJoinsAnInFlightBuild(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "proj")
	d := addDrawer(t, v, "proj", "wing", "room", "content being built", "facts")
	be := &blockingEmbedder{Embedder: embedder.NewMock(384), started: make(chan struct{}), release: make(chan struct{})}
	eng := NewEngine(be, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })
	withSearchLockTimeout(t, 0)
	ctx := context.Background()

	first := make(chan error, 1)
	go func() {
		_, err := eng.Search(ctx, d.Content, SearchFilters{Project: "proj"})
		first <- err
	}()
	<-be.started
	joined := make(chan struct{})
	joinBuildHook = func(string) { close(joined) }
	t.Cleanup(func() { joinBuildHook = nil })
	second := make(chan error, 1)
	go func() {
		_, err := eng.Search(ctx, d.Content, SearchFilters{Project: "proj"})
		second <- err
	}()
	<-joined
	close(be.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatalf("the joining search: %v, want the joined build's outcome", err)
	}
}

// TestATimedOutCaptureInsertIsFoundByTheNextSearch: P is warm; another
// process holds P's commit lock, so capture's IndexDrawers times out. The
// timeout marks P out of date, so after the lock is released the next search
// rebuilds and finds the drawer capture wrote to disk first, although the
// counter never moved.
func TestATimedOutCaptureInsertIsFoundByTheNextSearch(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	addDrawer(t, v, "proj", "wing", "room", "an earlier drawer", "facts")
	warmProject(t, eng, v, "proj")

	held, err := indexstore.Lock(ctx, v, "proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	g0 := held.Generation()
	withSearchLockTimeout(t, 0)
	d := addDrawer(t, v, "proj", "wing", "room", "a drawer captured while the project was busy", "facts")
	if err := eng.IndexDrawers(ctx, []DrawerInput{{Project: "proj", Wing: "wing", Room: "room", Drawer: d}}); err != nil {
		t.Fatalf("a timed-out capture insert must not fail capture: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if g, _ := indexstore.ReadGeneration(v, "proj"); g != g0 {
		t.Fatalf("precondition: the counter moved (%v -> %v); the row needs it unchanged", g0, g)
	}
	if _, found := findHitContaining(mustSearch(t, eng, d.Content), d.Content); !found {
		t.Fatal("the drawer whose insert timed out is not found by the next search")
	}
}

// TestATimedOutInsertOnAMigratedVaultIsAnError: with the migration marker
// there is no tracked drawer for a later build to read, so a capture insert
// that times out must fail loudly instead of being dropped.
func TestATimedOutInsertOnAMigratedVaultIsAnError(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	ensureProjectDir(t, v, "proj")
	if err := os.MkdirAll(filepath.Join(v.Root, ".vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.Root, ".vibe-palace", "vault.toml"), []byte("format = 2\nauthored_only = \"2026-10-03\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := indexstore.Lock(ctx, v, "proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	withSearchLockTimeout(t, 0)
	err = eng.IndexDrawers(ctx, []DrawerInput{{Project: "proj", Wing: "w", Room: "r", Drawer: storage.Drawer{ID: "d", Content: "x"}, Vec: []float32{1}}})
	if !errors.Is(err, vaultlock.ErrLockWaitTimeout) {
		t.Fatalf("timed-out insert on a migrated vault: %v, want an error wrapping the lock timeout", err)
	}
}

// TestCaptureBeforeTheFirstSearch: three drawers on disk, IndexDrawers on one
// of them on a cold engine. The cold insert must not make the project look
// built: HasIndex stays false, and the next search builds every drawer.
func TestCaptureBeforeTheFirstSearch(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	ensureProjectDir(t, v, "proj")
	var ds []storage.Drawer
	for _, c := range []string{"first drawer", "second drawer", "third drawer"} {
		ds = append(ds, addDrawer(t, v, "proj", "wing", "room", c, "facts"))
	}
	if err := eng.IndexDrawers(ctx, []DrawerInput{{Project: "proj", Wing: "wing", Room: "room", Drawer: ds[0]}}); err != nil {
		t.Fatal(err)
	}
	if eng.HasIndex("proj") {
		t.Fatal("a cold insert made the project look built")
	}
	res, err := eng.Search(ctx, "drawer", SearchFilters{Project: "proj", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("search after a cold insert returned %d drawers, want all 3", len(res))
	}
}

// TestColdInsertWritesTheCache: a cold insert is deferred, but its vector is
// committed to the embed cache at once, so the chunk is a cache hit for the
// next build before any search has run.
func TestColdInsertWritesTheCache(t *testing.T) {
	eng, v := testEngine(t)
	ensureProjectDir(t, v, "proj")
	d := addDrawer(t, v, "proj", "wing", "room", "cold content", "facts")
	vec := []float32{0.25, 0.5, 0.75}
	if err := eng.IndexDrawers(context.Background(), []DrawerInput{{Project: "proj", Wing: "wing", Room: "room", Drawer: d, Vec: vec}}); err != nil {
		t.Fatal(err)
	}
	got, err := eng.cache.Get("proj", d.ID)
	if err != nil || !slices.Equal(got, vec) {
		t.Fatalf("cache after a cold insert = %v, %v; want %v", got, err, vec)
	}
}

// TestEmbedCacheWritesWaitForTheCommitLock: while another process holds the
// project's commit lock, a Rebuild's vector commit (waiting with no timeout)
// blocks; it is seen reaching the lock through the hook, and only the release
// lets it through.
func TestEmbedCacheWritesWaitForTheCommitLock(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	ensureProjectDir(t, v, "proj")
	d := addDrawer(t, v, "proj", "wing", "room", "content", "facts")
	held, err := indexstore.Lock(ctx, v, "proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{}, 4)
	beforeCommitLockHook = func(string) { reached <- struct{}{} }
	t.Cleanup(func() { beforeCommitLockHook = nil })

	rebuilt := make(chan error, 1)
	go func() { _, err := eng.Rebuild(ctx, "proj"); rebuilt <- err }()
	<-reached
	p, _ := eng.cache.path("proj", d.ID)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("the Rebuild wrote its vector while another process held the commit lock")
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-rebuilt; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the vector was not written once the lock was free: %v", err)
	}

}

// TestCaptureTimeoutDuringABuildIsNotLost (code review R1): a capture insert
// that times out on the project MUTEX while a build holds it, after the build
// has already listed the drawers, marks the project out of date. The build
// must not overwrite that mark when it finishes: the counter never moved, so
// only the mark makes the next search rebuild and find the captured drawer.
func TestCaptureTimeoutDuringABuildIsNotLost(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "proj")
	addDrawer(t, v, "proj", "wing", "room", "a drawer the build embeds", "facts")
	be := &blockingEmbedder{Embedder: embedder.NewMock(384), started: make(chan struct{}), release: make(chan struct{})}
	eng := NewEngine(be, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })
	ctx := context.Background()

	built := make(chan error, 1)
	go func() { _, err := eng.Rebuild(ctx, "proj"); built <- err }()
	<-be.started // the build has listed the drawers and holds the mutex

	withSearchLockTimeout(t, 0)
	d := addDrawer(t, v, "proj", "wing", "room", "a drawer captured during the build", "facts")
	if err := eng.IndexDrawers(ctx, []DrawerInput{{Project: "proj", Wing: "wing", Room: "room", Drawer: d, Vec: unitVec(3)}}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	close(be.release)
	if err := <-built; err != nil {
		t.Fatal(err)
	}
	withSearchLockTimeout(t, time.Minute)
	if _, found := findHitContaining(mustSearch(t, eng, d.Content), d.Content); !found {
		t.Fatal("the drawer whose capture insert timed out during a build is never found: the build overwrote the out-of-date mark")
	}
}
