// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// testHarness bundles all layers for full-stack integration tests.
type testHarness struct {
	Vault    *storage.Vault
	Engine   *search.Engine
	Embedder embedder.Embedder
	Indexer  *capture.Indexer
	Server   *mcp.Server
	Resolver *vpctx.Resolver
	Config   storage.Config

	mcpReady bool // true after initMCP called
}

// newHarness creates a full-stack test harness.
// If useRealEmbedder is true, requires ONNX model (skips in short mode).
func newHarness(t *testing.T, useRealEmbedder bool, cfgOverrides ...func(*storage.Config)) *testHarness {
	t.Helper()

	if useRealEmbedder && testing.Short() {
		t.Skip("skipping integration test in short mode (requires ONNX model)")
	}

	root := t.TempDir()
	vault := storage.NewVault(root)

	cfg := storage.Config{
		SearchDefaultLimit: 10,
		BoostWing:          0.12,
		BoostHall:          0.24,
		BoostRoom:          0.34,
		ChunkMaxChars:      800,
		ChunkOverlap:       100,
	}
	for _, override := range cfgOverrides {
		override(&cfg)
	}

	var emb embedder.Embedder
	if useRealEmbedder {
		var err error
		emb, err = embedder.NewONNX("sentence-transformers/all-MiniLM-L6-v2", testutil.ProjectCacheDir(t), 256, 32)
		if err != nil {
			t.Fatalf("NewONNX: %v", err)
		}
	} else {
		emb = embedder.NewMock(384)
	}
	t.Cleanup(func() { emb.Close() })

	eng := search.NewEngine(emb, vault, cfg)
	t.Cleanup(func() { eng.Close() })

	indexer := capture.NewIndexer(vault, eng, emb, cfg)
	resolver := vpctx.NewResolver(root)
	srv := mcp.NewServer(vault)

	return &testHarness{
		Vault:    vault,
		Engine:   eng,
		Embedder: emb,
		Indexer:  indexer,
		Server:   srv,
		Resolver: resolver,
		Config:   cfg,
	}
}

// registerAllTools registers all MCP tools on the harness server.
func (h *testHarness) registerAllTools(t *testing.T) {
	t.Helper()
	tools.RegisterAll(h.Server.Registry(), h.Resolver, h.Vault, h.Engine, h.Config)
}

// initMCP sends the initialize + notifications/initialized handshake.
func (h *testHarness) initMCP(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	h.Server.HandleMessage(ctx, json.RawMessage(`{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": {
			"protocolVersion": "2025-03-26",
			"capabilities": {},
			"clientInfo": {"name": "integration-test", "version": "0.1.0"}
		}
	}`))

	h.Server.HandleMessage(ctx, json.RawMessage(`{
		"jsonrpc": "2.0", "method": "notifications/initialized"
	}`))

	h.mcpReady = true
}

// callTool sends a tools/call JSON-RPC request and returns the text content.
// Fails the test on protocol errors.
func (h *testHarness) callTool(t *testing.T, name string, args any) string {
	t.Helper()

	if !h.mcpReady {
		h.initMCP(t)
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	msg := json.RawMessage(fmt.Sprintf(`{
		"jsonrpc": "2.0", "id": 99, "method": "tools/call",
		"params": {"name": %q, "arguments": %s}
	}`, name, argsJSON))

	resp := h.Server.HandleMessage(context.Background(), msg)

	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	raw, _ := json.Marshal(rpcResp.Result)
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v (raw: %s)", err, raw)
	}

	if result.IsError {
		t.Fatalf("tool %q returned error: %s", name, result.Content[0].Text)
	}
	if len(result.Content) == 0 {
		t.Fatalf("tool %q returned empty content", name)
	}

	return result.Content[0].Text
}

// addDrawer is a convenience for writing a drawer and returning it with the generated ID.
func (h *testHarness) addDrawer(t *testing.T, project, wing, room, content, hall, date string) storage.Drawer {
	t.Helper()
	d := storage.Drawer{
		Content:    content,
		Hall:       hall,
		SourceType: "manual",
		FiledAt:    date + "T10:00:00Z",
	}
	if err := h.Vault.AppendDrawer(project, wing, room, d); err != nil {
		t.Fatalf("AppendDrawer: %v", err)
	}
	drawers, err := h.Vault.ListDrawers(project, wing, room)
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range drawers {
		if stored.Content == content {
			return stored
		}
	}
	t.Fatal("drawer not found after append")
	return storage.Drawer{}
}
