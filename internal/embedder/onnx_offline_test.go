// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build offlinewarm

package embedder

import (
	"os"
	"testing"
	"time"
)

// TestONNXOfflineWarmupPopulateCache is `make model-test-offline`'s phase 1:
// run with real network access and a scratch HOME/XDG_CACHE_HOME/modelCacheDir,
// it does exactly what the pre-fix bug relied on happening once — a real
// hugot.DownloadModel — so that the destination directory is a fully
// complete snapshot before phase 2 runs with the network cut off. It makes
// no timing or offline assertion of its own; TestONNXOfflineWarmCacheNeedsNoNetwork
// is the one that verifies the actual fix.
func TestONNXOfflineWarmupPopulateCache(t *testing.T) {
	cacheDir := os.Getenv("VP_OFFLINE_MODEL_CACHE_DIR")
	if cacheDir == "" {
		t.Skip("VP_OFFLINE_MODEL_CACHE_DIR not set; run via `make model-test-offline`")
	}

	emb, err := NewONNX("sentence-transformers/all-MiniLM-L6-v2", cacheDir, 256, 32)
	if err != nil {
		t.Fatalf("NewONNX warmup (network expected here): %v", err)
	}
	emb.Close()
}

// TestONNXOfflineWarmCacheNeedsNoNetwork is the offline warm-cache regression
// test for the bug where NewONNX contacted huggingface.co (and destroyed its
// own cached revision-info file on the failed attempt) even when the
// destination already held a complete model snapshot. It is excluded from
// the default `go test ./...` and from `-short` by its build tag, since it
// depends on a specific pre-warmed scratch HOME/XDG_CACHE_HOME and a
// specific modelCacheDir rather than the normal testutil.ProjectCacheDir —
// see `make model-test-offline`, which sets VP_OFFLINE_MODEL_CACHE_DIR and
// runs this inside a network-isolated namespace.
func TestONNXOfflineWarmCacheNeedsNoNetwork(t *testing.T) {
	cacheDir := os.Getenv("VP_OFFLINE_MODEL_CACHE_DIR")
	if cacheDir == "" {
		t.Skip("VP_OFFLINE_MODEL_CACHE_DIR not set; run via `make model-test-offline`")
	}

	start := time.Now()
	emb, err := NewONNX("sentence-transformers/all-MiniLM-L6-v2", cacheDir, 256, 32)
	if err != nil {
		t.Fatalf("NewONNX on a warm cache with no network: %v", err)
	}
	defer emb.Close()

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("NewONNX took %s on a warm cache — expected near-instant, no network round trip", elapsed)
	}
}
