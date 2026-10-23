// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// errFakeClosed mirrors ErrEmbedderClosed at the fake level so the handle/
// refcount tests exercise the same fail-closed semantics the real *ONNXEmbedder
// has, without loading a 310 MB model.
var errFakeClosed = errors.New("fakeModel: closed")

// fakeModel is a counting, fail-closed stand-in for *ONNXEmbedder. Its mutex
// mirrors ONNXEmbedder.e.mu: Embed holds it (optionally blocking, for the
// mutex-backstop test) and Close takes it, so a Close races a use exactly as the
// real type does. It is safe for concurrent use.
type fakeModel struct {
	mu          sync.Mutex
	closed      bool
	closedCount int // how many times Close actually destroyed (want exactly 1)
	embedCount  int
	dims        int

	// Optional blocking hooks for T6a: when release != nil, Embed signals
	// `entered` (while holding mu) and waits on `release` before returning, so a
	// concurrent Close blocks on mu until the in-flight Embed completes.
	entered chan struct{}
	release chan struct{}
}

func newFakeModel(dims int) *fakeModel { return &fakeModel{dims: dims} }

func (f *fakeModel) Embed(ctx context.Context, _ string) ([]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errFakeClosed
	}
	f.embedCount++
	if f.release != nil {
		if f.entered != nil {
			f.entered <- struct{}{}
		}
		<-f.release
	}
	return []float32{1}, nil
}

func (f *fakeModel) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errFakeClosed
	}
	out := make([][]float32, len(texts))
	for i := range out {
		f.embedCount++
		out[i] = []float32{1}
	}
	return out, nil
}

func (f *fakeModel) Dimensions() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errFakeClosed
	}
	return f.dims, nil
}

func (f *fakeModel) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		f.closedCount++
	}
	return nil
}

func (f *fakeModel) getClosedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closedCount
}

// countingConstruct returns a construct closure that increments *n and records
// the single fake it builds into *built. It builds a fresh fake each call, so a
// construct-count > 1 is observable as two different *built values AND as n > 1.
func countingConstruct(n *int32, built **fakeModel, dims int) func() (Embedder, error) {
	return func() (Embedder, error) {
		atomic.AddInt32(n, 1)
		f := newFakeModel(dims)
		*built = f
		return f, nil
	}
}

func bg() context.Context { return context.Background() }

// TestSharedT1SingleConstructSharedModel (T1): two Acquire with the SAME key
// construct the model exactly once, and both handles delegate to the SAME
// underlying model.
func TestSharedT1SingleConstructSharedModel(t *testing.T) {
	c := NewSharedModelCache()
	var n int32
	var built *fakeModel
	k := ModelKey{Backend: "go", Model: "m", MaxSeq: 256}

	h1, err := c.Acquire(bg(), k, countingConstruct(&n, &built, 384))
	if err != nil {
		t.Fatalf("Acquire h1: %v", err)
	}
	h2, err := c.Acquire(bg(), k, countingConstruct(&n, &built, 999)) // different dims if wrongly reconstructed
	if err != nil {
		t.Fatalf("Acquire h2: %v", err)
	}

	// Force lazy construction through each handle.
	d1, err := h1.Dimensions()
	if err != nil {
		t.Fatalf("h1.Dimensions: %v", err)
	}
	d2, err := h2.Dimensions()
	if err != nil {
		t.Fatalf("h2.Dimensions: %v", err)
	}

	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("construct ran %d times, want exactly 1", got)
	}
	if d1 != 384 || d2 != 384 {
		t.Fatalf("dims = (%d,%d), want (384,384) — a second construct would have given 999", d1, d2)
	}
	// Same underlying: embeds through both handles land on the one fake.
	if _, err := h1.Embed(bg(), "a"); err != nil {
		t.Fatalf("h1.Embed: %v", err)
	}
	if _, err := h2.Embed(bg(), "b"); err != nil {
		t.Fatalf("h2.Embed: %v", err)
	}
	if built.embedCount != 2 {
		t.Fatalf("shared fake saw %d embeds, want 2 (proves both handles share one model)", built.embedCount)
	}
}

// TestSharedT2aKeyDistinctness (T2a): different Backend / Model / MaxSeq produce
// distinct entries and distinct constructs; equal keys collapse. ModelKey has NO
// Batch field (the struct literals below are the compile-time proof), so two
// acquisitions that would differ only by batch size share one model.
func TestSharedT2aKeyDistinctness(t *testing.T) {
	c := NewSharedModelCache()
	var n int32
	mk := func(b, m string, s int) func() (Embedder, error) {
		return func() (Embedder, error) {
			atomic.AddInt32(&n, 1)
			return newFakeModel(384), nil
		}
	}
	keys := []ModelKey{
		{Backend: "go", Model: "m", MaxSeq: 256},
		{Backend: "ort", Model: "m", MaxSeq: 256},    // different backend
		{Backend: "go", Model: "other", MaxSeq: 256}, // different model
		{Backend: "go", Model: "m", MaxSeq: 512},     // different maxseq
		{Backend: "go", Model: "m", MaxSeq: 256},     // DUPLICATE of keys[0]
	}
	for _, k := range keys {
		h, err := c.Acquire(bg(), k, mk(k.Backend, k.Model, k.MaxSeq))
		if err != nil {
			t.Fatalf("Acquire %+v: %v", k, err)
		}
		if _, err := h.Dimensions(); err != nil { // force construct
			t.Fatalf("Dimensions %+v: %v", k, err)
		}
	}
	// 4 distinct keys → 4 constructs (the 5th is a dup of the 1st).
	if got := atomic.LoadInt32(&n); got != 4 {
		t.Fatalf("construct ran %d times, want 4 distinct keys", got)
	}
	// The duplicate key shares the first entry's model (refcount 2, one model).
	if _, ok := c.entries[ModelKey{Backend: "go", Model: "m", MaxSeq: 256}]; !ok {
		t.Fatalf("shared entry for duplicate key missing")
	}
	if c.entries[ModelKey{Backend: "go", Model: "m", MaxSeq: 256}].refs != 2 {
		t.Fatalf("duplicate key refs = %d, want 2", c.entries[ModelKey{Backend: "go", Model: "m", MaxSeq: 256}].refs)
	}
}

// TestSharedT3RefcountLifecycle (T3): closing a non-last handle does not destroy
// the model; the last close destroys it exactly once; double-close is a no-op.
func TestSharedT3RefcountLifecycle(t *testing.T) {
	c := NewSharedModelCache()
	var n int32
	var built *fakeModel
	k := ModelKey{Backend: "go", Model: "m", MaxSeq: 256}

	h1, _ := c.Acquire(bg(), k, countingConstruct(&n, &built, 384))
	h2, _ := c.Acquire(bg(), k, countingConstruct(&n, &built, 384))
	if _, err := h1.Dimensions(); err != nil { // force construct so there is a real model to close
		t.Fatalf("Dimensions: %v", err)
	}

	if err := h1.Close(); err != nil {
		t.Fatalf("h1.Close: %v", err)
	}
	if built.getClosedCount() != 0 {
		t.Fatalf("underlying closed after first (non-last) close; want still open")
	}
	if _, err := h2.Dimensions(); err != nil {
		t.Fatalf("h2 unusable after h1 close: %v", err)
	}

	if err := h2.Close(); err != nil {
		t.Fatalf("h2.Close: %v", err)
	}
	if got := built.getClosedCount(); got != 1 {
		t.Fatalf("underlying closedCount = %d after last close, want exactly 1", got)
	}
	// Double-close either handle is a no-op.
	_ = h1.Close()
	_ = h2.Close()
	if got := built.getClosedCount(); got != 1 {
		t.Fatalf("underlying closedCount = %d after double-close, want 1", got)
	}
	// Entry removed from the map at last close.
	if _, ok := c.entries[k]; ok {
		t.Fatalf("entry still present after last close")
	}
}

// TestSharedT6aMutexBackstop (T6a): an in-flight Embed holds the model's lock; a
// concurrent last Close blocks on that lock, the Embed completes returning its
// vector (NOT the closed error), the model is then destroyed exactly once, and a
// SUBSEQUENT call returns the closed error. One handle is used because refs can
// reach zero during an in-flight Embed only when the embedding handle is the one
// being closed.
func TestSharedT6aMutexBackstop(t *testing.T) {
	c := NewSharedModelCache()
	f := newFakeModel(384)
	f.entered = make(chan struct{}, 1)
	f.release = make(chan struct{})
	k := ModelKey{Backend: "go", Model: "m", MaxSeq: 256}

	h, _ := c.Acquire(bg(), k, func() (Embedder, error) { return f, nil })
	if _, err := h.Dimensions(); err != nil { // build lazy before we block Embed
		t.Fatalf("Dimensions: %v", err)
	}

	embErrCh := make(chan error, 1)
	go func() {
		_, err := h.Embed(bg(), "blocking") // enters fake, holds f.mu, waits on release
		embErrCh <- err
	}()
	<-f.entered // Embed is now holding the model lock

	closeDone := make(chan struct{})
	go func() {
		_ = h.Close() // last ref → will block on f.mu inside fake.Close
		close(closeDone)
	}()
	// Give Close time to reach (and block on) the model lock.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-closeDone:
		t.Fatalf("Close completed while Embed still held the model lock — backstop broken")
	default:
	}

	close(f.release) // let the in-flight Embed finish
	if err := <-embErrCh; err != nil {
		t.Fatalf("in-flight Embed returned %v, want its vector (not closed)", err)
	}
	<-closeDone

	if got := f.getClosedCount(); got != 1 {
		t.Fatalf("closedCount = %d, want exactly 1 (no mid-embed destroy)", got)
	}
	// A subsequent call observes the destroyed model.
	if _, err := h.Embed(bg(), "after"); !errors.Is(err, errFakeClosed) {
		t.Fatalf("Embed after destroy = %v, want errFakeClosed", err)
	}
}

// TestSharedT6bFailClosedAfterDestroy (T6b): after the last handle closes and
// the shared model is destroyed, a further call through any still-held handle
// returns the closed error rather than a stale value or a panic.
func TestSharedT6bFailClosedAfterDestroy(t *testing.T) {
	c := NewSharedModelCache()
	var n int32
	var built *fakeModel
	k := ModelKey{Backend: "go", Model: "m", MaxSeq: 256}

	hA, _ := c.Acquire(bg(), k, countingConstruct(&n, &built, 384))
	hB, _ := c.Acquire(bg(), k, countingConstruct(&n, &built, 384))
	if _, err := hA.Dimensions(); err != nil {
		t.Fatalf("Dimensions: %v", err)
	}

	_ = hA.Close()
	_ = hB.Close() // last close destroys the model
	if got := built.getClosedCount(); got != 1 {
		t.Fatalf("closedCount = %d, want 1", got)
	}
	if _, err := hA.Embed(bg(), "x"); !errors.Is(err, errFakeClosed) {
		t.Fatalf("hA.Embed after destroy = %v, want errFakeClosed", err)
	}
	if _, err := hB.Dimensions(); !errors.Is(err, errFakeClosed) {
		t.Fatalf("hB.Dimensions after destroy = %v, want errFakeClosed", err)
	}
}

// TestSharedT7ConcurrentHandlesRaceClean (T7): N handles to ONE shared entry are
// acquired up front (so the entry is stable and genuinely shared, refs never
// drops to zero mid-test), then used and closed concurrently. Run with -race.
// Exactly one construct, exactly one destroy, entry gone at the end. This
// exercises the cache mutex and refcount under concurrency without the
// acquire/close interleaving (where refs legitimately cycles to zero and the
// entry is rebuilt — not what this test is isolating).
func TestSharedT7ConcurrentHandlesRaceClean(t *testing.T) {
	c := NewSharedModelCache()
	var n int32
	var built *fakeModel
	k := ModelKey{Backend: "go", Model: "m", MaxSeq: 256}

	const N = 16
	handles := make([]Handle, N)
	for i := range handles {
		h, err := c.Acquire(bg(), k, countingConstruct(&n, &built, 384))
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		handles[i] = h
	}
	// Build the shared model once, before the goroutines fan out.
	if _, err := handles[0].Dimensions(); err != nil {
		t.Fatalf("Dimensions: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(h Handle) {
			defer wg.Done()
			if _, err := h.Dimensions(); err != nil {
				t.Errorf("Dimensions: %v", err)
			}
			if _, err := h.Embed(bg(), "x"); err != nil {
				t.Errorf("Embed: %v", err)
			}
			if !h.Ready() {
				t.Errorf("Ready = false after construct, want true")
			}
			if err := h.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}(handles[i])
	}
	wg.Wait()

	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("construct ran %d times, want exactly 1", got)
	}
	if got := built.getClosedCount(); got != 1 {
		t.Fatalf("closedCount = %d, want exactly 1", got)
	}
	c.mu.Lock()
	_, ok := c.entries[k]
	c.mu.Unlock()
	if ok {
		t.Fatalf("entry still present after all handles closed")
	}
}
