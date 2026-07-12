// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// countingEmbedder wraps a MockEmbedder and records whether it was closed.
type countingEmbedder struct {
	*MockEmbedder
	closed atomic.Int32
}

func (c *countingEmbedder) Close() error {
	c.closed.Add(1)
	return nil
}

// countingConstructor returns a constructor that records how many times it ran
// and the embedder it produced.
func countingConstructor() (func() (Embedder, error), *atomic.Int32, *countingEmbedder) {
	var calls atomic.Int32
	inner := &countingEmbedder{MockEmbedder: NewMock(384)}
	return func() (Embedder, error) {
		calls.Add(1)
		return inner, nil
	}, &calls, inner
}

func TestLazyDefersConstruction(t *testing.T) {
	construct, calls, _ := countingConstructor()
	l := NewLazy(construct)

	if got := calls.Load(); got != 0 {
		t.Fatalf("constructor ran %d times before first use, want 0", got)
	}

	if _, err := l.Embed(context.Background(), "hello"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("constructor ran %d times after first use, want 1", got)
	}
}

func TestLazyConstructsOnceUnderConcurrency(t *testing.T) {
	construct, calls, _ := countingConstructor()
	l := NewLazy(construct)

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			if _, err := l.Embed(context.Background(), "hello"); err != nil {
				t.Errorf("Embed: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("constructor ran %d times across %d concurrent calls, want 1", got, goroutines)
	}
}

// TestLazyCloseDoesNotConstruct is the key regression: closing an embedder that
// was never used must not load the model just to tear it down.
func TestLazyCloseDoesNotConstruct(t *testing.T) {
	construct, calls, _ := countingConstructor()
	l := NewLazy(construct)

	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("Close() triggered construction (%d calls), want 0", got)
	}
}

func TestLazyCloseDelegatesAfterUse(t *testing.T) {
	construct, _, inner := countingConstructor()
	l := NewLazy(construct)

	if _, err := l.Embed(context.Background(), "hello"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := inner.closed.Load(); got != 1 {
		t.Errorf("underlying Close called %d times, want 1", got)
	}
}

func TestLazyDimensions(t *testing.T) {
	construct, calls, _ := countingConstructor()
	l := NewLazy(construct)

	dims, err := l.Dimensions()
	if err != nil {
		t.Fatalf("Dimensions: %v", err)
	}
	if dims != 384 {
		t.Errorf("Dimensions() = %d, want 384", dims)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("constructor ran %d times, want 1", got)
	}
}

func TestLazyEmbedBatch(t *testing.T) {
	construct, _, _ := countingConstructor()
	l := NewLazy(construct)

	vecs, err := l.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 2 {
		t.Errorf("len(vecs) = %d, want 2", len(vecs))
	}
}

// TestLazyMemoizesConstructionError proves a failed load is not retried on
// every call — a hot loop of searches must not re-attempt a 90MB download.
func TestLazyMemoizesConstructionError(t *testing.T) {
	wantErr := errors.New("download failed")
	var calls atomic.Int32
	l := NewLazy(func() (Embedder, error) {
		calls.Add(1)
		return nil, wantErr
	})

	if _, err := l.Embed(context.Background(), "hello"); !errors.Is(err, wantErr) {
		t.Errorf("Embed err = %v, want %v", err, wantErr)
	}
	if _, err := l.EmbedBatch(context.Background(), []string{"hello"}); !errors.Is(err, wantErr) {
		t.Errorf("EmbedBatch err = %v, want %v", err, wantErr)
	}
	if _, err := l.Dimensions(); !errors.Is(err, wantErr) {
		t.Errorf("Dimensions err = %v, want %v", err, wantErr)
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("constructor ran %d times across 3 failing calls, want 1", got)
	}

	// A failed construction leaves nothing to close.
	if err := l.Close(); err != nil {
		t.Errorf("Close after failed construction: %v", err)
	}
}
