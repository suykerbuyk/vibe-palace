// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

// livenessBound is a deadlock detector, not a performance assertion: every
// passing path finishes without waiting on it. A rebuild that holds a lock it
// must not hold fails at the bound with a goroutine dump, instead of hanging
// until Go's package timeout (10 minutes by default).
const livenessBound = 60 * time.Second

// waitOrDump waits for ch to close. If it has not within livenessBound, it
// writes every goroutine's stack to stderr and panics. Not t.Fatalf: a test's
// output is printed only when the test finishes, and after a deadlock its
// cleanups (Engine.Close, hnswIndex.Close) block on the very lock the stuck
// goroutine holds, so the test never finishes and the dump would be lost. A
// panic aborts the package run at the bound, with the dump already written.
func waitOrDump(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(livenessBound):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		msg := fmt.Sprintf("%s: %s did not finish within %v (a lock held across a rebuild?)", t.Name(), what, livenessBound)
		fmt.Fprintf(os.Stderr, "%s\n%s\n", msg, buf[:n])
		panic(msg)
	}
}

// goDone runs fn on its own goroutine and returns a channel closed when it
// returns.
func goDone(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	return done
}

// rebuildGate holds a tombstone rebuild just after its snapshot: the hook
// signals snapshotted, then waits for release or for the rebuild's context to
// be cancelled.
type rebuildGate struct {
	snapshotted chan struct{}
	release     chan struct{}
	once        sync.Once
}

func newRebuildGate() *rebuildGate {
	return &rebuildGate{snapshotted: make(chan struct{}), release: make(chan struct{})}
}

func (g *rebuildGate) hook(ctx context.Context) {
	g.once.Do(func() { close(g.snapshotted) })
	select {
	case <-g.release:
	case <-ctx.Done():
	}
}

// rebuildTestParams triggers a rebuild once tombstones exceed 10% of the live
// ids, with no minimum, and holds it at gate when gate is not nil.
func rebuildTestParams(gate *rebuildGate) hnswParams {
	p := provisionalHNSWParams
	p.TombstoneRatio = 0.10
	p.MinTombstones = 1
	if gate != nil {
		p.afterSnapshot = gate.hook
	}
	return p
}

// filledIndex returns an index of n random unit vectors with ids v0..v{n-1}.
func filledIndex(t *testing.T, dims, n int, params hnswParams, rng *rand.Rand) (*hnswIndex, map[string][]float32) {
	t.Helper()
	idx := newHNSWIndex(dims, params)
	vecs := make([][]float32, n)
	ids := make([]string, n)
	live := make(map[string][]float32, n)
	for i := range vecs {
		vecs[i] = randomUnitVector(rng, dims)
		ids[i] = fmt.Sprintf("v%d", i)
		live[ids[i]] = vecs[i]
	}
	if err := idx.Build(vecs, ids); err != nil {
		t.Fatal(err)
	}
	return idx, live
}

// currentRebuild returns the in-flight rebuild, or nil.
func currentRebuild(idx *hnswIndex) *hnswRebuild {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.rebuild
}

// TestHNSWUpsertsAloneCrossTheThreshold: upserts tombstone the old key, so an
// upsert-only workload (identity-keyed note chunks re-embed on every edit) must
// trigger the rebuild too. With 200 live ids and a 10% threshold, the 21st
// upsert is the first with tombstones (21) above 10% of the live ids (20), so
// exactly that upsert triggers, nothing is logged after it, and the swapped-in
// graph holds no tombstone at all.
func TestHNSWUpsertsAloneCrossTheThreshold(t *testing.T) {
	const dims, n = 16, 200
	rng := rand.New(rand.NewSource(21))
	idx, _ := filledIndex(t, dims, n, rebuildTestParams(nil), rng)
	t.Cleanup(func() { _ = idx.Close() })

	for i := range n/10 + 1 {
		if err := idx.Insert(fmt.Sprintf("v%d", i), randomUnitVector(rng, dims)); err != nil {
			t.Fatal(err)
		}
	}
	b := currentRebuild(idx)
	if b == nil {
		if idx.swaps.Load() == 0 {
			t.Fatalf("no rebuild started after %d upserts (tombstones %d, live %d)", n/10+1, tombstoneCount(idx), idx.Len())
		}
	} else {
		waitOrDump(t, b.done, "the upsert-triggered rebuild")
	}
	if got := idx.rebuildsStarted.Load(); got != 1 {
		t.Errorf("rebuilds started = %d, want 1", got)
	}
	if got := idx.swaps.Load(); got != 1 {
		t.Errorf("swaps = %d, want 1", got)
	}
	if got := tombstoneCount(idx); got != 0 {
		t.Errorf("tombstones after the swap = %d, want 0", got)
	}
	if got := idx.Len(); got != n {
		t.Errorf("Len = %d, want %d", got, n)
	}
}

// TestHNSWWritesDuringAHeldRebuild: writes made while a rebuild is building
// land on the live graph AND in the log, and the swap replays them. A is
// deleted and B upserted while the rebuild is held after its snapshot (which
// still holds A and B's old vector); after the swap A is never returned, B
// answers with its new vector, Len is the live count, and the further deletes
// that crossed the threshold again during the build started no second build.
func TestHNSWWritesDuringAHeldRebuild(t *testing.T) {
	const dims, n = 16, 200
	rng := rand.New(rand.NewSource(22))
	gate := newRebuildGate()
	idx, live := filledIndex(t, dims, n, rebuildTestParams(gate), rng)
	t.Cleanup(func() { _ = idx.Close() })

	// Delete until the rebuild starts (the 19th delete: 19 > 10% of 181).
	next := 0
	for currentRebuild(idx) == nil {
		id := fmt.Sprintf("v%d", next)
		next++
		if !idx.Delete(id) {
			t.Fatalf("Delete(%s) = false", id)
		}
		delete(live, id)
	}
	b := currentRebuild(idx)
	waitOrDump(t, gate.snapshotted, "the rebuild's snapshot")

	a, bID := "v150", "v170"
	bOld := live[bID]
	if !idx.Delete(a) {
		t.Fatal("Delete(A) = false")
	}
	delete(live, a)
	bNew := randomUnitVector(rng, dims)
	if err := idx.Insert(bID, bNew); err != nil {
		t.Fatal(err)
	}
	live[bID] = bNew
	// Cross the threshold again while the first rebuild runs. More than
	// finalReplayMax writes, so the swap must catch up off-lock first.
	for range finalReplayMax + 36 {
		id := fmt.Sprintf("v%d", next)
		next++
		idx.Delete(id)
		delete(live, id)
	}
	close(gate.release)
	waitOrDump(t, b.done, "the held rebuild")

	if got := idx.swaps.Load(); got != 1 {
		t.Fatalf("swaps = %d, want 1", got)
	}
	if got := idx.rebuildsStarted.Load(); got != 1 {
		t.Errorf("rebuilds started = %d, want 1: a trigger during a running build must do nothing", got)
	}
	if got := idx.Len(); got != len(live) {
		t.Errorf("Len = %d, want the live count %d", got, len(live))
	}
	for _, q := range [][]float32{bOld, bNew, randomUnitVector(rng, dims)} {
		res, err := idx.Search(q, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			vec, ok := live[r.ID]
			if !ok {
				t.Fatalf("%s was returned after the swap, but it was deleted", r.ID)
			}
			if want := cosineDistanceF32(q, vec); math.Abs(float64(r.Distance-want)) > crossImplTolerance {
				t.Fatalf("%s at distance %v, but its current vector is at %v: a pre-swap vector answered", r.ID, r.Distance, want)
			}
		}
	}
	res, err := idx.Search(bNew, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].ID != bID || math.Abs(float64(res[0].Distance)) > 1e-5 {
		t.Errorf("Search(B's new vector) = %v, want B at distance 0", res)
	}
}

// TestHNSWRebuildCancelledOnClose: Close cancels a held rebuild and returns only
// once its goroutine has exited, and the cancelled rebuild never swaps. The
// gate's hook keeps the goroutine alive after cancellation until exitGate is
// closed, so a Close that did not wait would return early, which the test sees.
func TestHNSWRebuildCancelledOnClose(t *testing.T) {
	const dims, n = 16, 300
	rng := rand.New(rand.NewSource(23))
	gate := newRebuildGate()
	exitGate := make(chan struct{})
	params := rebuildTestParams(nil)
	params.afterSnapshot = func(ctx context.Context) {
		gate.hook(ctx)
		<-exitGate
	}
	idx, _ := filledIndex(t, dims, n, params, rng)

	for i := 0; currentRebuild(idx) == nil; i++ {
		idx.Delete(fmt.Sprintf("v%d", i))
	}
	b := currentRebuild(idx)
	waitOrDump(t, gate.snapshotted, "the rebuild's snapshot")

	closed := goDone(func() { _ = idx.Close() })
	select {
	case <-closed:
		t.Fatal("Close returned while the rebuild goroutine was still running")
	case <-b.done:
		t.Fatal("the rebuild goroutine exited before exitGate was closed")
	default:
	}
	close(exitGate)
	waitOrDump(t, closed, "Close")

	select {
	case <-b.done:
	default:
		t.Error("Close returned before the rebuild goroutine's done channel closed")
	}
	if got := idx.rebuildsRunning.Load(); got != 0 {
		t.Errorf("rebuilds running after Close = %d, want 0", got)
	}
	if got := idx.swaps.Load(); got != 0 {
		t.Errorf("swaps = %d, want 0: a cancelled rebuild must not swap", got)
	}
}

// TestHNSWBuildCancelsARunningRebuild: Build replaces the contents a running
// rebuild snapshotted, so it cancels that rebuild, which then never swaps, and
// Close still waits for its goroutine.
func TestHNSWBuildCancelsARunningRebuild(t *testing.T) {
	const dims, n = 16, 300
	rng := rand.New(rand.NewSource(24))
	gate := newRebuildGate()
	idx, _ := filledIndex(t, dims, n, rebuildTestParams(gate), rng)

	for i := 0; currentRebuild(idx) == nil; i++ {
		idx.Delete(fmt.Sprintf("v%d", i))
	}
	b := currentRebuild(idx)
	waitOrDump(t, gate.snapshotted, "the rebuild's snapshot")
	gen := idx.gen

	replacement := randomUnitVector(rng, dims)
	if err := idx.Build([][]float32{replacement}, []string{"only"}); err != nil {
		t.Fatal(err)
	}
	waitOrDump(t, b.done, "the cancelled rebuild")
	if idx.gen <= gen {
		t.Errorf("generation %d → %d: Build must bump it", gen, idx.gen)
	}
	if got := idx.swaps.Load(); got != 0 {
		t.Errorf("swaps = %d, want 0: the cancelled rebuild swapped stale contents over Build's", got)
	}
	if got := idx.Len(); got != 1 {
		t.Errorf("Len = %d, want 1 (Build's contents)", got)
	}
	waitOrDump(t, goDone(func() { _ = idx.Close() }), "Close")
}

// TestHNSWRebuildAbandonedWhenWritesOutpaceCatchUp: a writer landing more than
// finalReplayMax writes before every off-lock catch-up round (a flat-out
// writer, made deterministic) never lets the remainder shrink. After the last
// round the rebuild is abandoned: nothing is swapped, the write lock replays
// nothing, and the old graph keeps answering correctly. The next rebuild then
// waits out the backoff (MinTombstones writes after one abandon), and a quiet
// rebuild after it swaps, replaying at most finalReplayMax under the lock.
func TestHNSWRebuildAbandonedWhenWritesOutpaceCatchUp(t *testing.T) {
	const dims, n, perRound, minTomb = 16, 200, finalReplayMax + 1, 20
	rng := rand.New(rand.NewSource(25))
	gate := newRebuildGate()
	params := rebuildTestParams(gate)
	params.MinTombstones = minTomb

	var mu sync.Mutex // guards live and storm against the rebuild goroutine
	storm := true
	var idx *hnswIndex
	var live map[string][]float32
	upserts := make([][]float32, 0, perRound*(catchUpRounds+1))
	for range cap(upserts) {
		upserts = append(upserts, randomUnitVector(rng, dims))
	}
	next := 0
	write := func() {
		id := fmt.Sprintf("v%d", n-1-next%(n/2))
		v := upserts[next%len(upserts)]
		next++
		if err := idx.Insert(id, v); err != nil {
			t.Errorf("Insert: %v", err)
		}
		live[id] = v
	}
	params.beforeCatchUpRound = func(int) {
		mu.Lock()
		defer mu.Unlock()
		if !storm {
			return
		}
		for range perRound {
			write()
		}
	}
	idx, live = filledIndex(t, dims, n, params, rng)
	t.Cleanup(func() { _ = idx.Close() })

	mu.Lock()
	deleted := 0
	for currentRebuild(idx) == nil {
		id := fmt.Sprintf("v%d", deleted)
		deleted++
		idx.Delete(id)
		delete(live, id)
	}
	mu.Unlock()
	first := currentRebuild(idx)
	waitOrDump(t, gate.snapshotted, "the rebuild's snapshot")
	mu.Lock()
	for range perRound {
		write()
	}
	mu.Unlock()
	close(gate.release)
	waitOrDump(t, first.done, "the stormed rebuild")

	mu.Lock()
	storm = false
	mu.Unlock()
	if got := idx.swaps.Load(); got != 0 {
		t.Fatalf("swaps = %d, want 0: the stormed rebuild swapped, replaying %d mutations under the lock (budget %d)",
			got, idx.lastReplayLen.Load(), finalReplayMax)
	}
	if got := idx.abandoned.Load(); got != 1 {
		t.Fatalf("abandoned = %d, want 1", got)
	}
	checkLive := func(phase string) {
		t.Helper()
		if got := idx.Len(); got != len(live) {
			t.Errorf("%s: Len = %d, want %d", phase, got, len(live))
		}
		for _, q := range [][]float32{upserts[0], upserts[len(upserts)-1], randomUnitVector(rng, dims)} {
			res, err := idx.Search(q, 20)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range res {
				vec, ok := live[r.ID]
				if !ok {
					t.Fatalf("%s: %s returned, but it is not live", phase, r.ID)
				}
				if want := cosineDistanceF32(q, vec); math.Abs(float64(r.Distance-want)) > crossImplTolerance {
					t.Fatalf("%s: %s at %v, its current vector is at %v", phase, r.ID, r.Distance, want)
				}
			}
		}
	}
	checkLive("after the abandon")

	// The backoff: minTomb writes must land before a rebuild may start.
	for i := range minTomb - 1 {
		id := fmt.Sprintf("v%d", deleted+i)
		idx.Delete(id)
		delete(live, id)
	}
	deleted += minTomb - 1
	if got := idx.rebuildsStarted.Load(); got != 1 {
		t.Fatalf("rebuilds started = %d after %d writes; the backoff is %d writes", got, minTomb-1, minTomb)
	}
	idx.Delete(fmt.Sprintf("v%d", deleted))
	delete(live, fmt.Sprintf("v%d", deleted))
	second := currentRebuild(idx)
	if second == nil {
		t.Fatalf("no rebuild started once the %d-write backoff was served (rebuilds started %d)", minTomb, idx.rebuildsStarted.Load())
	}
	waitOrDump(t, second.done, "the quiet rebuild")
	if got := idx.swaps.Load(); got != 1 {
		t.Fatalf("swaps = %d after the quiet rebuild, want 1", got)
	}
	if got := idx.lastReplayLen.Load(); got > finalReplayMax {
		t.Errorf("the swap replayed %d mutations under the lock, above the budget %d", got, finalReplayMax)
	}
	if got := tombstoneCount(idx); got != 0 {
		t.Errorf("tombstones after the quiet swap = %d, want 0", got)
	}
	idx.mu.RLock()
	abandons := idx.abandons
	idx.mu.RUnlock()
	if abandons != 0 {
		t.Errorf("consecutive abandons = %d after a successful swap, want 0 (the backoff must reset)", abandons)
	}
	checkLive("after the quiet swap")
}

// TestRebuildBackoffGens pins the backoff rule: MinTombstones writes (at least
// one) after the first abandon, ×4 per further consecutive abandon, capped.
func TestRebuildBackoffGens(t *testing.T) {
	for _, tc := range []struct {
		minTomb, abandons int
		want              uint64
	}{
		{64, 1, 64}, {64, 2, 256}, {64, 3, 1024},
		{0, 1, 1}, {0, 3, 16},
		{64, 100, maxRebuildBackoffGens},
	} {
		if got := rebuildBackoffGens(tc.minTomb, tc.abandons); got != tc.want {
			t.Errorf("rebuildBackoffGens(%d, %d) = %d, want %d", tc.minTomb, tc.abandons, got, tc.want)
		}
	}
}

// TestHNSWSmallRemainderReplayedUnderLock: writes that fit the lock budget
// (here 5 deletes and 5 upserts, below finalReplayMax) skip the off-lock
// catch-up and are replayed under the write lock at the swap. None may be
// lost: Len is the live count, every live id is found at its own vector, and
// no deleted id comes back.
func TestHNSWSmallRemainderReplayedUnderLock(t *testing.T) {
	const dims, n = 16, 200
	rng := rand.New(rand.NewSource(26))
	gate := newRebuildGate()
	idx, live := filledIndex(t, dims, n, rebuildTestParams(gate), rng)
	t.Cleanup(func() { _ = idx.Close() })

	next := 0
	for currentRebuild(idx) == nil {
		id := fmt.Sprintf("v%d", next)
		next++
		idx.Delete(id)
		delete(live, id)
	}
	b := currentRebuild(idx)
	waitOrDump(t, gate.snapshotted, "the rebuild's snapshot")
	deleted := make(map[string]bool)
	for range 5 {
		id := fmt.Sprintf("v%d", next)
		next++
		idx.Delete(id)
		delete(live, id)
		deleted[id] = true
	}
	for i := range 5 {
		id := fmt.Sprintf("v%d", 100+i)
		v := randomUnitVector(rng, dims)
		if err := idx.Insert(id, v); err != nil {
			t.Fatal(err)
		}
		live[id] = v
	}
	close(gate.release)
	waitOrDump(t, b.done, "the held rebuild")

	if got := idx.swaps.Load(); got != 1 {
		t.Fatalf("swaps = %d, want 1", got)
	}
	if got := idx.lastReplayLen.Load(); got != 10 {
		t.Errorf("replayed under the lock = %d, want the 10 writes made during the build", got)
	}
	if got := idx.Len(); got != len(live) {
		t.Errorf("Len = %d, want the live count %d", got, len(live))
	}
	for id, v := range live {
		res, err := idx.Search(v, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 1 || res[0].ID != id || math.Abs(float64(res[0].Distance)) > 1e-5 {
			t.Errorf("%s is not found at its own vector: %v", id, res)
		}
	}
	for id := range deleted {
		res, err := idx.Search(randomUnitVector(rng, dims), n)
		if err != nil {
			t.Fatal(err)
		}
		if distanceTo(res, id) >= 0 {
			t.Errorf("deleted id %s came back after the swap", id)
		}
	}
}

// TestHNSWBuildResetsTheRebuildBackoff: a Build discards the graph whose writes
// earned a backoff, so the fresh graph's first threshold crossing starts a
// rebuild at once instead of waiting out the old backoff.
func TestHNSWBuildResetsTheRebuildBackoff(t *testing.T) {
	const dims, n, minTomb = 16, 200, 20
	rng := rand.New(rand.NewSource(27))
	params := rebuildTestParams(nil)
	params.MinTombstones = minTomb
	idx := newHNSWIndex(dims, params)
	t.Cleanup(func() { _ = idx.Close() })
	// As if three rebuilds in a row had been abandoned just now: a backoff of
	// 20 × 4² = 320 writes.
	idx.mu.Lock()
	idx.abandons, idx.abandonedAt = 3, idx.gen
	idx.mu.Unlock()

	vecs := make([][]float32, n)
	ids := make([]string, n)
	for i := range vecs {
		vecs[i] = randomUnitVector(rng, dims)
		ids[i] = fmt.Sprintf("v%d", i)
	}
	if err := idx.Build(vecs, ids); err != nil {
		t.Fatal(err)
	}
	// The threshold alone (≥ 20 tombstones, > 10% of the live ids) is crossed
	// at the 20th delete; the inherited backoff would hold off until the 320th.
	for i := 0; i < 40 && currentRebuild(idx) == nil && idx.swaps.Load() == 0; i++ {
		idx.Delete(ids[i])
	}
	if got := idx.rebuildsStarted.Load(); got != 1 {
		t.Errorf("rebuilds started = %d within 40 deletes after Build, want 1: the old backoff survived Build", got)
	}
}
