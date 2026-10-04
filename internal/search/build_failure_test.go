// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// panicOnceEmbedder panics on its first EmbedBatch and embeds normally after.
type panicOnceEmbedder struct {
	embedder.Embedder
	calls atomic.Int64
}

func (p *panicOnceEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if p.calls.Add(1) == 1 {
		panic("the embedder blew up mid-build")
	}
	return p.Embedder.EmbedBatch(ctx, texts)
}

// TestABuildThatPanicsDoesNotWedgeTheProject (code review F1): a lazy build
// that panics propagates the panic, and still clears its in-flight entry and
// the project mutex, so the next search builds the project instead of waiting
// on the dead build for ever.
func TestABuildThatPanicsDoesNotWedgeTheProject(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "proj")
	d := addDrawer(t, v, "proj", "wing", "room", "content after the panic", "facts")
	eng := NewEngine(&panicOnceEmbedder{Embedder: embedder.NewMock(384)}, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("the build's panic did not propagate")
			}
		}()
		_, _ = eng.Search(context.Background(), d.Content, SearchFilters{Project: "proj"})
	}()

	done := make(chan []SearchResult, 1)
	go func() {
		res, err := eng.Search(context.Background(), d.Content, SearchFilters{Project: "proj"})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	select {
	case res := <-done:
		if !hasContent(res, d.Content) {
			t.Fatalf("the search after the panic did not build the project: %+v", res)
		}
	case <-time.After(60 * time.Second): // a deadlock detector, not a timing assertion
		t.Fatal("the search after a panicked build hangs: its in-flight entry was never cleared")
	}
}

// TestAnUnreadableCacheFailsTheBuild (code review F2): an embed cache that
// cannot be read is an error naming the cache, never a silent "no vector",
// which would record every chunk as missing (missing_vectors) for a fault no
// repair can mend. A corrupt vector file, by contrast, is a miss.
func TestAnUnreadableCacheFailsTheBuild(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	eng, v := testEngine(t)
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{"a local chunk"}, true)
	dir, _ := v.EmbedCacheDir("proj")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := eng.Search(context.Background(), "a local chunk", SearchFilters{Project: "proj"})
	if err == nil || !strings.Contains(err.Error(), "embed cache") {
		t.Fatalf("search over an unreadable cache: %v, want an error naming the embed cache", err)
	}
	if slices.Contains(record(t, v, "proj").Stale, missingVectors) {
		t.Fatal("an unreadable cache was recorded as missing vectors")
	}
}

// TestACorruptVectorIsAMiss (code review F2): a vector file of a size that is
// no whole vector is read as a miss and embedded again, not a failed build.
func TestACorruptVectorIsAMiss(t *testing.T) {
	eng, v := testEngine(t)
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note body")
	search(t, eng, "proj", "a note body")
	ids, _, _, _ := collectNoteCorpus(v, "proj")
	dir, _ := v.EmbedCacheDir("proj")
	if err := os.WriteFile(filepath.Join(dir, ids[0]+".vec"), []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := eng.Rebuild(context.Background(), "proj")
	if err != nil || st.Embedded != 1 {
		t.Fatalf("Rebuild over a corrupt vector: %+v, %v; want it embedded again", st, err)
	}
}
