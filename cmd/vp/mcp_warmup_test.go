// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// spawnStartupEmbedderWarmup kicks the engine's detached embedder construction
// when enabled, and is a no-op when the gate is off (the constrained-hardware /
// `vp mcp serve` posture). A fresh lazy embedder is not ready until warmed.
func TestStartupEmbedderWarmupGate(t *testing.T) {
	newStack := func() (*serverStack, *int) {
		constructs := 0
		lazy := embedder.NewLazy(func() (embedder.Embedder, error) {
			constructs++
			return embedder.NewMock(384), nil
		})
		v := storage.NewVault(t.TempDir())
		eng := search.NewEngine(lazy, v, storage.Config{SearchDefaultLimit: 10})
		t.Cleanup(func() { eng.Close() })
		return &serverStack{vault: v, eng: eng}, &constructs
	}

	waitReady := func(eng *search.Engine) bool {
		deadline := time.Now().Add(2 * time.Second)
		for !eng.EmbedderReady() {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(5 * time.Millisecond)
		}
		return true
	}

	t.Run("enabled warms the model", func(t *testing.T) {
		old := startupEmbedderWarmupEnabled
		startupEmbedderWarmupEnabled = true
		t.Cleanup(func() { startupEmbedderWarmupEnabled = old })

		stack, _ := newStack()
		if stack.eng.EmbedderReady() {
			t.Fatal("embedder ready before warm-up")
		}
		spawnStartupEmbedderWarmup(stack)
		if !waitReady(stack.eng) {
			t.Fatal("warm-up did not construct the embedder")
		}
	})

	t.Run("disabled is a no-op", func(t *testing.T) {
		old := startupEmbedderWarmupEnabled
		startupEmbedderWarmupEnabled = false
		t.Cleanup(func() { startupEmbedderWarmupEnabled = old })

		stack, _ := newStack()
		spawnStartupEmbedderWarmup(stack)
		// Give any errant goroutine a moment; readiness must stay false.
		time.Sleep(50 * time.Millisecond)
		if stack.eng.EmbedderReady() {
			t.Fatal("warm-up ran while disabled")
		}
	})
}

// A failed warm-up construct still lets the search path return the typed
// not-ready error (proving the warm-up and the search guarantee compose), rather
// than block on the cold model.
func TestWarmupFailureStillRefusesSearch(t *testing.T) {
	lazy := embedder.NewLazy(func() (embedder.Embedder, error) {
		return nil, errors.New("model missing")
	})
	v := storage.NewVault(t.TempDir())
	eng := search.NewEngine(lazy, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })
	eng.WarmEmbedder()

	// A project that exists with content (so it is not truly empty).
	testutil.InitProject(t, v.Root, "proj")
	if err := v.AppendDrawer("proj", "wing", "room", storage.Drawer{Content: "x", Hall: "facts", SourceType: "session", FiledAt: "2026-04-07T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	_, err := eng.Search(context.Background(), "q", search.SearchFilters{Project: "proj"})
	var nb *search.IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("want *search.IndexNotBuiltError after a failed warm-up, got %v", err)
	}
}
