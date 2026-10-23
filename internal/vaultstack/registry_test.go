// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultstack

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// TestMain runs the package hermetically (sourceaudit's hermetic-test-main rule
// requires every test package to isolate XDG before touching host config).
func TestMain(m *testing.M) { os.Exit(testutil.RunHermetic(m)) }

func bg() context.Context { return context.Background() }

const testModel = "sentence-transformers/all-MiniLM-L6-v2"

// fakeHandle is a close-counting embedder.Handle used to prove the stack/engine
// decrement the shared handle EXACTLY once. Unlike the real shared handle it is
// NOT idempotent — it counts every Close — so a double-close is visible.
type fakeHandle struct {
	mu          sync.Mutex
	closedCount int
}

func (h *fakeHandle) Embed(context.Context, string) ([]float32, error) { return []float32{1}, nil }
func (h *fakeHandle) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1}
	}
	return out, nil
}
func (h *fakeHandle) Dimensions() (int, error) { return 384, nil }
func (h *fakeHandle) Ready() bool              { return true }
func (h *fakeHandle) Close() error {
	h.mu.Lock()
	h.closedCount++
	h.mu.Unlock()
	return nil
}
func (h *fakeHandle) getClosedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closedCount
}

// fakeModel is an embedder.Embedder construct result for shared-cache tests; its
// closedCount is the "destroyed once" spy.
type fakeModel struct {
	mu          sync.Mutex
	closedCount int
}

func (f *fakeModel) Embed(context.Context, string) ([]float32, error)          { return []float32{1}, nil }
func (f *fakeModel) EmbedBatch(_ context.Context, t []string) ([][]float32, error) { return make([][]float32, len(t)), nil }
func (f *fakeModel) Dimensions() (int, error)                                  { return 384, nil }
func (f *fakeModel) Close() error {
	f.mu.Lock()
	f.closedCount++
	f.mu.Unlock()
	return nil
}
func (f *fakeModel) getClosedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closedCount
}

// newStack builds a VaultStack with a real engine holding the given handle. Used
// by the fake-build tests; the root is a real temp dir so EmbedCache setup is happy.
func newStack(root string, h embedder.Handle) *VaultStack {
	v := storage.NewVault(root)
	cfg := storage.Config{}
	eng := search.NewEngine(h, v, cfg)
	return NewVaultStack(root, v, cfg, eng, vpctx.NewResolver(v.Root), h)
}

// TestRegistryT4SharedModelAcrossStacks (T4): StackFor is memoized per root;
// distinct roots yield distinct stacks that SHARE one model (one construct), and
// Registry.Close destroys that model exactly once.
func TestRegistryT4SharedModelAcrossStacks(t *testing.T) {
	models := embedder.NewSharedModelCache()
	var n int32
	var built *fakeModel
	key := embedder.ModelKey{Backend: embedder.BackendGo, Model: testModel, MaxSeq: 256}
	rootA, rootB := t.TempDir(), t.TempDir()

	build := func(ctx context.Context, root string) (*VaultStack, error) {
		h, err := models.Acquire(ctx, key, func() (embedder.Embedder, error) {
			atomic.AddInt32(&n, 1)
			built = &fakeModel{}
			return built, nil
		})
		if err != nil {
			return nil, err
		}
		if _, err := h.Dimensions(); err != nil { // force the (shared) lazy construct now
			return nil, err
		}
		return newStack(root, h), nil
	}
	reg := NewRegistry(build, models)

	sA1, err := reg.StackFor(bg(), rootA)
	if err != nil {
		t.Fatalf("StackFor A: %v", err)
	}
	sA2, err := reg.StackFor(bg(), rootA)
	if err != nil {
		t.Fatalf("StackFor A again: %v", err)
	}
	if sA1 != sA2 {
		t.Fatalf("StackFor(rootA) returned different stacks; want memoized identity")
	}
	sB, err := reg.StackFor(bg(), rootB)
	if err != nil {
		t.Fatalf("StackFor B: %v", err)
	}
	if sB == sA1 {
		t.Fatalf("StackFor(rootB) returned rootA's stack; want distinct")
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("model constructed %d times across 2 stacks, want 1 (shared)", got)
	}

	if err := reg.Close(); err != nil {
		t.Fatalf("Registry.Close: %v", err)
	}
	if got := built.getClosedCount(); got != 1 {
		t.Fatalf("shared model closedCount = %d after Registry.Close, want exactly 1", got)
	}
}

// TestRegistryT5FailureThenRetry (T5): a failed build is NOT memoized (the key is
// retryable and nothing is published); a later StackFor rebuilds and succeeds.
func TestRegistryT5FailureThenRetry(t *testing.T) {
	var attempts int32
	root := t.TempDir()
	build := func(ctx context.Context, r string) (*VaultStack, error) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			return nil, errors.New("boom")
		}
		return newStack(r, &fakeHandle{}), nil
	}
	reg := NewRegistry(build, nil)
	defer reg.Close()

	s, err := reg.StackFor(bg(), root)
	if err == nil || s != nil {
		t.Fatalf("first StackFor = (%v, %v), want (nil, error)", s, err)
	}
	// Nothing published for a failed build, and the key is not wedged.
	reg.mu.Lock()
	_, published := reg.stacks[root]
	reg.mu.Unlock()
	if published {
		t.Fatalf("failed build published a stack")
	}

	s2, err2 := reg.StackFor(bg(), root)
	if err2 != nil || s2 == nil {
		t.Fatalf("retry StackFor = (%v, %v), want a non-nil stack and no error", s2, err2)
	}
}

// TestRegistryT5ConcurrentJoiner (T5): a joiner that arrives while the owner is
// inside build observes the owner's result from the captured pointer and starts
// NO second build.
func TestRegistryT5ConcurrentJoiner(t *testing.T) {
	root := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	var buildN int32
	build := func(ctx context.Context, r string) (*VaultStack, error) {
		atomic.AddInt32(&buildN, 1)
		entered <- struct{}{} // signal the owner is inside build (building[r] already set)
		<-release
		return newStack(r, &fakeHandle{}), nil
	}
	reg := NewRegistry(build, nil)
	defer reg.Close()

	type res struct {
		s *VaultStack
		e error
	}
	ownerCh := make(chan res, 1)
	joinCh := make(chan res, 1)

	go func() { s, e := reg.StackFor(bg(), root); ownerCh <- res{s, e} }()
	<-entered // owner is now building

	go func() { s, e := reg.StackFor(bg(), root); joinCh <- res{s, e} }()
	time.Sleep(50 * time.Millisecond) // let the joiner reach the join wait

	close(release)
	o := <-ownerCh
	j := <-joinCh

	if o.e != nil || j.e != nil {
		t.Fatalf("owner/join errors: %v / %v", o.e, j.e)
	}
	if o.s == nil || o.s != j.s {
		t.Fatalf("joiner did not observe the owner's stack (owner=%p join=%p)", o.s, j.s)
	}
	if got := atomic.LoadInt32(&buildN); got != 1 {
		t.Fatalf("build ran %d times, want 1 (joiner must not start a second build)", got)
	}
}

// TestRegistryT8OwnerSingleDecrement (T8): the stack and its engine share ONE
// handle, so VaultStack.Close decrements it exactly once (never twice).
func TestRegistryT8OwnerSingleDecrement(t *testing.T) {
	fh := &fakeHandle{}
	st := newStack(t.TempDir(), fh)
	if err := st.Close(); err != nil {
		t.Fatalf("VaultStack.Close: %v", err)
	}
	if got := fh.getClosedCount(); got != 1 {
		t.Fatalf("handle closed %d times, want exactly 1 (a second close = double-decrement)", got)
	}
}

// TestRegistryT9CtxCancel (T9): a cancelled ctx makes StackFor fail without
// running build or publishing a stack.
func TestRegistryT9CtxCancel(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	build := func(context.Context, string) (*VaultStack, error) {
		t.Error("build must not run under a cancelled ctx")
		return newStack(root, &fakeHandle{}), nil
	}
	reg := NewRegistry(build, nil)
	s, err := reg.StackFor(ctx, root)
	if err == nil || s != nil {
		t.Fatalf("StackFor(cancelled) = (%v, %v), want (nil, error)", s, err)
	}
	reg.mu.Lock()
	_, published := reg.stacks[root]
	reg.mu.Unlock()
	if published {
		t.Fatalf("cancelled StackFor published a stack")
	}
}

// TestRegistryT10RealSharedModel (T10, model-gated): two stacks for two roots
// with identical embedder config resolve to ONE real *ONNXEmbedder
// (construct-count==1, both 384 dims); Registry.Close is clean. End-to-end
// N×→1× proof. Uses the warm project model cache (never a cold download).
func TestRegistryT10RealSharedModel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-model test in short mode")
	}
	dir := testutil.ProjectCacheDir(t)
	models := embedder.NewSharedModelCache()
	var n int32
	key := embedder.ModelKey{Backend: embedder.BackendGo, Model: testModel, MaxSeq: 256}

	build := func(ctx context.Context, root string) (*VaultStack, error) {
		h, err := models.Acquire(ctx, key, func() (embedder.Embedder, error) {
			atomic.AddInt32(&n, 1)
			return embedder.NewONNXBackend(embedder.BackendGo, testModel, dir, 256, 32)
		})
		if err != nil {
			return nil, err
		}
		d, err := h.Dimensions() // force the real construct and read dims
		if err != nil {
			return nil, err
		}
		if d != 384 {
			t.Errorf("dims = %d, want 384", d)
		}
		return newStack(root, h), nil
	}
	reg := NewRegistry(build, models)

	if _, err := reg.StackFor(bg(), t.TempDir()); err != nil {
		t.Fatalf("StackFor A: %v", err)
	}
	if _, err := reg.StackFor(bg(), t.TempDir()); err != nil {
		t.Fatalf("StackFor B: %v", err)
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("real model constructed %d times across 2 stacks, want 1 (shared)", got)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Registry.Close: %v", err)
	}
}
