// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// countingLazyEmbedder returns a LazyEmbedder over a MockEmbedder, plus the
// counter recording how many times the underlying embedder was CONSTRUCTED.
//
// Construction count — not wall-clock time — is the observable this file asserts
// on. Timing assertions are unworkable here: the suite runs under -race in CI,
// where a "fast" startup can take arbitrarily long, and a "slow" model load is
// only slow on a cold model cache. The count is exact and deterministic.
func countingLazyEmbedder() (embedder.Embedder, *atomic.Int32) {
	var constructions atomic.Int32
	lazy := embedder.NewLazy(func() (embedder.Embedder, error) {
		constructions.Add(1)
		return embedder.NewMock(384), nil
	})
	return lazy, &constructions
}

// listTools drives a real JSON-RPC tools/list request and returns the tool names.
func listTools(t *testing.T, h *testHarness) []string {
	t.Helper()

	resp := h.Server.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}
	}`))

	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("tools/list: expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	raw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal tools/list result: %v", err)
	}
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal tools/list result: %v (raw: %s)", err, raw)
	}

	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestIntegration_HandshakeDoesNotConstructEmbedder is the acceptance test for
// the lazy embedder.
//
// WHY IT EXISTS: the server used to construct the ONNX embedder inside
// bootstrap(), BEFORE the MCP initialize handshake was answered. Loading that
// model takes tens of seconds — and downloads ~90MB on a cold cache — so the
// host's initialize timeout fired first and the session came up with ZERO tools.
// cmd/vp/bootstrap.go now wraps the NewONNX call in embedder.NewLazy, so the
// model is not touched until something actually needs a vector.
//
// This drives the REAL handshake — initialize and tools/list as JSON-RPC
// messages through mcp.Server.HandleMessage, with the production tool surface
// registered by tools.RegisterAll — and counts embedder CONSTRUCTIONS across it.
// The unit test (internal/embedder/lazy_test.go) proves LazyEmbedder defers; only
// this test proves nothing on the SERVER's startup path forces it anyway. A
// Dimensions() call added to tool registration, a health probe wired into
// initialize, or a restored eager index rebuild would each re-break startup while
// leaving every lazy_test.go assertion green.
//
// WHAT BREAKS IT: constructing the embedder eagerly (un-lazying bootstrap is not
// visible here, but any Dimensions/Embed/EmbedBatch call reached during
// registration, initialize, or tools/list is) drives the count above 0.
func TestIntegration_HandshakeDoesNotConstructEmbedder(t *testing.T) {
	emb, constructions := countingLazyEmbedder()

	h := newHarnessWithEmbedder(t, emb)
	h.registerAllTools(t)

	if got := constructions.Load(); got != 0 {
		t.Fatalf("registering the tool surface constructed the embedder %d times, want 0", got)
	}

	// The handshake itself: initialize + notifications/initialized.
	h.initMCP(t)

	if got := constructions.Load(); got != 0 {
		t.Fatalf("the initialize handshake constructed the embedder %d times, want 0 — startup is stalling on the model load again", got)
	}

	// tools/list is the other call every host makes before it will use the
	// session. It must not force the model either.
	names := listTools(t, h)
	if len(names) == 0 {
		t.Fatal("tools/list returned no tools; the handshake did not actually bring the surface up")
	}
	if !slices.Contains(names, "vp_search") {
		t.Fatalf("tools/list did not advertise vp_search (%d tools listed); the surface is not the production one", len(names))
	}

	if got := constructions.Load(); got != 0 {
		t.Fatalf("tools/list constructed the embedder %d times, want 0", got)
	}

	// NON-VACUITY: the count is 0 above because nothing needed a vector, not
	// because the counter is dead. The first search must force construction —
	// exactly once — and it must actually work.
	h.addDrawer(t, "proj", "dev", "go", "goroutines are cheap", "facts", "2026-04-01")

	h.callTool(t, "vp_search", map[string]any{
		"project": "proj",
		"query":   "goroutines are cheap",
	})

	if got := constructions.Load(); got != 1 {
		t.Errorf("after the first search the embedder was constructed %d times, want exactly 1", got)
	}

	// A second search reuses the constructed embedder; the model is loaded once
	// per process, not once per query.
	h.callTool(t, "vp_search", map[string]any{
		"project": "proj",
		"query":   "goroutines are cheap",
	})

	if got := constructions.Load(); got != 1 {
		t.Errorf("after a second search the embedder was constructed %d times, want 1 (construction is not memoized)", got)
	}
}

// TestIntegration_ColdSearchBuildsIndexLazily is the acceptance test for lazy
// per-project index construction.
//
// WHY IT EXISTS: deleting the eager full-vault reindex from bootstrap() removed
// the only thing that ever populated Engine.indexes on a fresh server. Before
// internal/search.ensureIndex existed, Search on a project with no index took
// the `idx, ok := e.indexes[project]; if !ok { return nil, nil }` branch — it
// returned an EMPTY RESULT AND A NIL ERROR. Every agent on every fresh session
// would have been told, truthfully-looking and quietly, that the vault contains
// nothing. That is the worst possible failure shape: silent, plausible, and
// invisible to any test that only asserts "no error".
//
// So this asserts the positive: a project that has NEVER been rebuilt — no
// Rebuild call, no IndexDrawer call, nothing but drawers on disk — returns REAL,
// CORRECT hits through a real JSON-RPC vp_search / vp_search_cross_project.
//
// WHAT BREAKS IT (mutation-proven): remove the `ensureIndex` / `ensureAllIndexes`
// calls from Engine.Search in internal/search/engine.go. Search then finds no
// index for the project, falls into the `return nil, nil` branch, and the tool
// hands back an empty JSON array — the assertions below fail with "cold vp_search
// returned 0 results".
//
// The mock embedder is deterministic on exact text (it hashes), so a query that
// is verbatim drawer content is an exact vector match. That is deliberate: this
// test is about whether the index EXISTS, not about semantic quality, which
// TestIntegrationMCPSearchEndToEnd covers with the real ONNX model.
func TestIntegration_ColdSearchBuildsIndexLazily(t *testing.T) {
	const goContent = "Go goroutines and channels enable lightweight concurrency"
	const pastaContent = "Fresh pasta is made from eggs and tipo 00 flour"

	t.Run("project-scoped search", func(t *testing.T) {
		h := newHarness(t, false)
		h.registerAllTools(t)
		h.initMCP(t)

		// Drawers on disk, and NOTHING else. No Rebuild, no IndexDrawer(s).
		h.addDrawer(t, "proj", "dev", "go", goContent, "facts", "2026-04-01")
		h.addDrawer(t, "proj", "cooking", "italian", pastaContent, "facts", "2026-04-01")

		raw := h.callTool(t, "vp_search", map[string]any{
			"project": "proj",
			"query":   goContent,
		})
		var sr struct {
			Items []search.SearchResult `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw), &sr); err != nil {
			t.Fatalf("parse vp_search results: %v (raw: %s)", err, raw)
		}
		results := sr.Items

		if len(results) == 0 {
			t.Fatalf("cold vp_search returned 0 results on a never-rebuilt project — the lazy index build is gone and every agent now sees an empty vault (raw: %s)", raw)
		}
		if results[0].Content != goContent {
			t.Errorf("top result content = %q, want %q", results[0].Content, goContent)
		}
		if results[0].Wing != "dev" || results[0].Room != "go" {
			t.Errorf("top result = %s/%s, want dev/go", results[0].Wing, results[0].Room)
		}

		// The build is memoized, not repeated: a second cold-path search still
		// works and still sees the same content.
		raw = h.callTool(t, "vp_search", map[string]any{
			"project": "proj",
			"query":   pastaContent,
		})
		var secondWrap struct {
			Items []search.SearchResult `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw), &secondWrap); err != nil {
			t.Fatalf("parse second vp_search results: %v (raw: %s)", err, raw)
		}
		second := secondWrap.Items
		if len(second) == 0 {
			t.Fatalf("the second search on the same project returned 0 results (raw: %s)", raw)
		}
		if second[0].Content != pastaContent {
			t.Errorf("second search top result content = %q, want %q", second[0].Content, pastaContent)
		}
	})

	t.Run("cross-project search", func(t *testing.T) {
		// A fresh harness: this engine has never built ANY index, so the
		// unfiltered path must build every known project (ensureAllIndexes).
		// Cross-project search iterates e.indexes directly — with no build it
		// iterates an empty map and returns nothing, with no error.
		h := newHarness(t, false)
		h.registerAllTools(t)
		h.initMCP(t)

		h.addDrawer(t, "alpha", "dev", "go", goContent, "facts", "2026-04-01")
		h.addDrawer(t, "beta", "cooking", "italian", pastaContent, "facts", "2026-04-01")

		raw := h.callTool(t, "vp_search_cross_project", map[string]any{
			"query": pastaContent,
		})
		var cr struct {
			Items []search.SearchResult `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw), &cr); err != nil {
			t.Fatalf("parse vp_search_cross_project results: %v (raw: %s)", err, raw)
		}
		results := cr.Items
		if len(results) == 0 {
			t.Fatalf("cold vp_search_cross_project returned 0 results across two never-rebuilt projects (raw: %s)", raw)
		}
		if results[0].Content != pastaContent {
			t.Errorf("top result content = %q, want %q", results[0].Content, pastaContent)
		}
		if results[0].Project != "beta" {
			t.Errorf("top result project = %q, want beta", results[0].Project)
		}

		// Both projects were built, not just the first one listed.
		var sawAlpha bool
		for _, r := range results {
			if r.Project == "alpha" {
				sawAlpha = true
			}
		}
		if !sawAlpha {
			t.Errorf("cross-project search only built one project; %q never appeared in %d results", "alpha", len(results))
		}
	})
}
