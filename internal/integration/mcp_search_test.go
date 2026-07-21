// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// TestIntegrationMCPSearchEndToEnd proves the full MCP protocol path:
// JSON-RPC tools/call → vp_search → search engine → results returned via JSON-RPC.
func TestIntegrationMCPSearchEndToEnd(t *testing.T) {
	h := newHarness(t, true) // real ONNX
	h.registerAllTools(t)
	ctx := context.Background()

	// Seed content.
	h.addDrawer(t, "proj", "dev", "go", "Go goroutines and channels enable lightweight concurrency", "facts", "2026-04-01")
	h.addDrawer(t, "proj", "dev", "python", "Python asyncio uses async/await for cooperative multitasking", "facts", "2026-04-01")
	h.addDrawer(t, "proj", "cooking", "italian", "Fresh pasta is made from eggs and tipo 00 flour", "facts", "2026-04-01")

	if err := h.Engine.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}

	// Call vp_search via the MCP JSON-RPC protocol.
	result := h.callTool(t, "vp_search", map[string]any{
		"project": "proj",
		"query":   "goroutines concurrency Go",
	})

	// Parse search results.
	var sr struct {
		Items []search.SearchResult `json:"items"`
	}
	if err := json.Unmarshal([]byte(result), &sr); err != nil {
		t.Fatalf("parse search results: %v (raw: %s)", err, result)
	}
	results := sr.Items

	if len(results) == 0 {
		t.Fatal("expected search results via MCP")
	}

	// Top result should be about Go concurrency.
	if results[0].Wing != "dev" || results[0].Room != "go" {
		t.Errorf("top result = %s/%s, want dev/go", results[0].Wing, results[0].Room)
	}

	// Search for cooking content via MCP.
	cookingResult := h.callTool(t, "vp_search", map[string]any{
		"project": "proj",
		"query":   "making homemade pasta dough",
	})

	var cookingWrap struct {
		Items []search.SearchResult `json:"items"`
	}
	if err := json.Unmarshal([]byte(cookingResult), &cookingWrap); err != nil {
		t.Fatalf("parse cooking results: %v (raw: %s)", err, cookingResult)
	}
	cookingResults := cookingWrap.Items

	if len(cookingResults) == 0 {
		t.Fatal("expected cooking results via MCP")
	}
	if cookingResults[0].Wing != "cooking" {
		t.Errorf("cooking query top result wing = %s, want cooking", cookingResults[0].Wing)
	}

	// Cross-project search via MCP.
	crossResult := h.callTool(t, "vp_search_cross_project", map[string]any{
		"query": "concurrency patterns",
	})

	var crossWrap struct {
		Items []search.SearchResult `json:"items"`
	}
	if err := json.Unmarshal([]byte(crossResult), &crossWrap); err != nil {
		t.Fatalf("parse cross-project results: %v (raw: %s)", err, crossResult)
	}
	crossResults := crossWrap.Items

	if len(crossResults) == 0 {
		t.Fatal("expected cross-project results via MCP")
	}
}

// TestIntegrationMCPSearchValidation proves that invalid tool parameters
// return proper JSON-RPC error responses through the MCP protocol.
func TestIntegrationMCPSearchValidation(t *testing.T) {
	h := newHarness(t, false) // mock embedder
	h.registerAllTools(t)
	h.initMCP(t)

	// Call vp_search with missing required "query" field.
	msg := json.RawMessage(`{
		"jsonrpc": "2.0", "id": 10, "method": "tools/call",
		"params": {"name": "vp_search", "arguments": {"project": "proj"}}
	}`)

	resp := h.Server.HandleMessage(context.Background(), msg)
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		// Might be an error response — that's also acceptable.
		_, isErr := resp.(mcplib.JSONRPCError)
		if !isErr {
			t.Fatalf("expected JSONRPCResponse or JSONRPCError, got %T", resp)
		}
		return // error response is correct for missing required field
	}

	// If we got a response, it should indicate an error.
	raw, _ := json.Marshal(rpcResp.Result)
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(raw, &result)

	if !result.IsError {
		t.Error("expected isError=true for missing required query parameter")
	}
}
