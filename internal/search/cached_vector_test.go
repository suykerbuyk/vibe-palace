// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestCachedVectorChecksTheRegime (pending-archive ingester, ruling 3): a
// vector cached under this engine's regime is a hit; under another regime
// (a mismatched sidecar) the same vector is a miss, so the ingester embeds it
// again rather than reuse another model's vector; vectors with no sidecar are
// a miss too; and an engine with no embedder, whose cache checks nothing,
// refuses.
func TestCachedVectorChecksTheRegime(t *testing.T) {
	v := fpVault(t)
	fpRebuild(t, v, "model-a")
	var id string
	for name := range fpVecs(t, v) {
		id = strings.TrimSuffix(name, ".vec")
		break
	}
	if id == "" {
		t.Fatal("precondition: the rebuild cached no vector")
	}
	lookup := func(model string) bool {
		t.Helper()
		eng := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderModel: model})
		t.Cleanup(func() { eng.Close() })
		vec, hit, err := eng.CachedVector("proj", id)
		if err != nil {
			t.Fatal(err)
		}
		if hit != (vec != nil) {
			t.Fatalf("hit %v with vector %v", hit, vec)
		}
		return hit
	}
	if !lookup("model-a") {
		t.Fatal("the same regime's vector was a miss")
	}
	if lookup("model-b") {
		t.Fatal("another regime's vector was a hit")
	}
	if err := os.Remove(filepath.Join(fpCacheDir(v), storage.EmbedCacheFingerprintFile)); err != nil {
		t.Fatal(err)
	}
	if lookup("model-a") {
		t.Fatal("a vector with no sidecar was a hit")
	}

	// Not closed: Close closes the embedder, and this engine has none.
	bare := NewEngine(nil, v, storage.Config{SearchDefaultLimit: 10, EmbedderModel: "model-a"})
	if _, _, err := bare.CachedVector("proj", id); !errors.Is(err, errNoRegime) {
		t.Fatalf("an engine with no embedder: %v, want errNoRegime", err)
	}
}
