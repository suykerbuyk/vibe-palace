// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// orderProbe is a probe embedder that records the ORDER of two events: the
// warm-up construct completing ("warm-done") and its own Close ("embedder-
// closed"). Its Dimensions (the call WarmEmbedder forces on its detached,
// warmWG-tracked goroutine) signals `started` on entry and then BLOCKS, so the
// warm goroutine is genuinely in flight when Engine.Close is called. Ready()
// stays false until the construct completes, so WarmEmbedder actually spawns the
// goroutine (EmbedderReady consults Ready()).
type orderProbe struct {
	mu      sync.Mutex
	events  []string
	ready   atomic.Bool
	started chan struct{}
	dims    int
}

func (p *orderProbe) record(e string) {
	p.mu.Lock()
	p.events = append(p.events, e)
	p.mu.Unlock()
}

func (p *orderProbe) order() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.events)
}

func (p *orderProbe) Ready() bool { return p.ready.Load() }

func (p *orderProbe) Dimensions() (int, error) {
	close(p.started) // warm goroutine has entered the construct
	// Stay in flight across Engine.Close's entry, so Close must join warmWG
	// before it may close the embedder.
	time.Sleep(50 * time.Millisecond)
	p.ready.Store(true)
	p.record("warm-done")
	return p.dims, nil
}

func (p *orderProbe) Embed(context.Context, string) ([]float32, error) {
	return make([]float32, p.dims), nil
}

func (p *orderProbe) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, p.dims)
	}
	return out, nil
}

func (p *orderProbe) Close() error {
	p.record("embedder-closed")
	return nil
}

// TestEngineCloseJoinsWarmBeforeClosingEmbedder (U6/G1) pins the Engine.Close
// ordering invariant: Close joins the warm/convert goroutines (warmWG.Wait)
// BEFORE it closes the embedder handle. Without that order, a routed stack's
// Close could Destroy the shared model while this engine's warm goroutine is
// still constructing against it. The probe records "warm-done" (the warmWG
// goroutine finishing) and "embedder-closed" (embedder.Close); the invariant is
// warm-done < embedder-closed. FAILS if e.embedder.Close() is moved above
// warmWG.Wait() (the recorded order reverses). Run under -race.
func TestEngineCloseJoinsWarmBeforeClosingEmbedder(t *testing.T) {
	v := testVault(t)
	p := &orderProbe{started: make(chan struct{}), dims: 384}
	e := NewEngine(p, v, storage.Config{SearchDefaultLimit: 10})

	e.WarmEmbedder()
	<-p.started // the warm goroutine is inside the (blocking) construct

	if err := e.Close(); err != nil {
		t.Fatalf("Engine.Close: %v", err)
	}

	order := p.order()
	wi := slices.Index(order, "warm-done")
	ci := slices.Index(order, "embedder-closed")
	if wi < 0 || ci < 0 {
		t.Fatalf("missing events (warm-done=%d embedder-closed=%d): %v", wi, ci, order)
	}
	if wi >= ci {
		t.Fatalf("Engine.Close closed the embedder BEFORE joining the warm goroutine; order=%v", order)
	}
}
