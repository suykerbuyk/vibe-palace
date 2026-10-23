// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// TestONNXClosedStateFailsClosed (T0, plan §2.0) proves the lifecycle hardening
// that makes a shared *ONNXEmbedder safe: after Close, every use method fails
// closed with ErrEmbedderClosed rather than running RunPipeline on a destroyed
// gomlx executor, and a second Close is a no-op (no second session.Destroy).
//
// Model-gated like the other real-model tests in this package (skipped in
// -short; uses the already-cached model on this host, never a cold download).
func TestONNXClosedStateFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ONNX test in short mode (requires model download)")
	}
	emb, err := NewONNX("sentence-transformers/all-MiniLM-L6-v2", testutil.ProjectCacheDir(t), 256, 32)
	if err != nil {
		t.Fatalf("NewONNX: %v", err)
	}

	// Sanity: live before Close.
	if _, err := emb.Dimensions(); err != nil {
		t.Fatalf("Dimensions before Close: unexpected error %v", err)
	}

	if err := emb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A second Close must be a safe no-op: sync.Once guarantees no second
	// session.Destroy. A double-free would panic here.
	if err := emb.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	ctx := context.Background()

	if _, err := emb.Dimensions(); !errors.Is(err, ErrEmbedderClosed) {
		t.Errorf("Dimensions after Close = %v, want ErrEmbedderClosed", err)
	}
	if _, err := emb.Embed(ctx, "probe"); !errors.Is(err, ErrEmbedderClosed) {
		t.Errorf("Embed after Close = %v, want ErrEmbedderClosed", err)
	}
	if _, err := emb.EmbedBatch(ctx, []string{"probe"}); !errors.Is(err, ErrEmbedderClosed) {
		t.Errorf("EmbedBatch after Close = %v, want ErrEmbedderClosed", err)
	}
}

// TestONNXDimensionsEmbedRaceClean exercises the Dimensions-under-lock change
// (Dimensions used to be lock-free): concurrent Dimensions + Embed on one
// embedder must be data-race-free. Run with `go test -race` to be meaningful.
func TestONNXDimensionsEmbedRaceClean(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ONNX test in short mode (requires model download)")
	}
	emb, err := NewONNX("sentence-transformers/all-MiniLM-L6-v2", testutil.ProjectCacheDir(t), 256, 32)
	if err != nil {
		t.Fatalf("NewONNX: %v", err)
	}
	defer emb.Close()

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := emb.Dimensions(); err != nil {
					t.Errorf("Dimensions: %v", err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := emb.Embed(ctx, "concurrent probe"); err != nil {
					t.Errorf("Embed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
