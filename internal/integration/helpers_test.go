// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// taskBody builds a vp_manage_task create body that satisfies BOTH doors a real
// create body must pass:
//
//   - the vp_manage_task handler's minimum-content floor (a body shorter than
//     minTaskContentBytes is refused as a pointer-to-a-plan rather than a plan);
//   - storage.validateTaskBody, which rejects a body carrying its own H1 or
//     "**Status:**" line, because CreateTask writes the header itself.
//
// It takes a caller-supplied lead line so failures still name the test that
// produced them, then pads with plain prose to clear the floor.
func taskBody(lead string) string {
	body := lead + "\n\n" +
		"Steps:\n" +
		"- Establish the fixture and register the tool surface.\n" +
		"- Drive the action under test through the MCP registry.\n" +
		"- Assert the on-disk result, not just the tool response.\n\n" +
		"Notes: this body exists to clear the create content floor with real prose.\n"
	for len(body) < 260 {
		body += "Additional plan detail so the body reads as a plan, not a pointer.\n"
	}
	return body
}

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

	return newHarnessWithEmbedder(t, emb, cfgOverrides...)
}

// newHarnessWithEmbedder builds the same full stack as newHarness around a
// caller-supplied embedder. Tests that need to observe WHEN the embedder is
// constructed — rather than what it returns — wrap their own constructor in
// embedder.NewLazy and pass it here; the harness must not construct it for them.
func newHarnessWithEmbedder(t *testing.T, emb embedder.Embedder, cfgOverrides ...func(*storage.Config)) *testHarness {
	t.Helper()

	root := t.TempDir()
	// Stamp the fresh harness vault born-current (same stamp-on-creation
	// semantics a real fresh vault gets) so its KG object-side reads pass the
	// armed data-format gate.
	if err := surface.WriteFormat(root, surface.RequiredDataFormat); err != nil {
		t.Fatalf("stamp born-current vault: %v", err)
	}
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
	tools.RegisterAll(h.Server.Registry(), h.Resolver, h.Vault, h.Engine, tools.WithConfig(h.Config))
}

// initMCP sends the initialize + notifications/initialized handshake.
// Note: HandleMessage does not register a transport session, so the
// clientInfo in this handshake is NOT visible to ClientInfoFromContext.
// Tests that need handshake-derived host attribution must use the stdio
// transport (callToolStdio) instead.
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

// seedProject materializes Projects/<slug>/ in the harness vault.
//
// vp_manage_task's create is gated on the project already existing: the slug is
// a free string with no accompanying path, so an unknown one is a typo or a
// hallucination rather than a new project. These fixtures previously relied on
// CreateTask lazily scaffolding the tree, which is the defect that gate closes.
func (h *testHarness) seedProject(t *testing.T, slug string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(h.Vault.Root, "Projects", slug), 0o755); err != nil {
		t.Fatalf("seed project %s: %v", slug, err)
	}
}

// callToolRaw sends a tools/call JSON-RPC request through the REAL MCP server —
// so schema validation (internal/mcp, validateParams) runs BEFORE the handler,
// exactly as it does in production — and returns the text content along with
// whether the call was refused.
//
// Tests that need to assert a REFUSAL must use this rather than the handler
// directly: a handler-level test would pass even if the schema were wrong, and
// the schema is the thing under test.
func (h *testHarness) callToolRaw(t *testing.T, name string, args any) (text string, isErr bool) {
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
	if len(result.Content) == 0 {
		t.Fatalf("tool %q returned empty content", name)
	}

	return result.Content[0].Text, result.IsError
}

// captureLogs installs a debug-level slog JSON handler that writes to a
// buffer for the duration of the test, restoring the previous default on
// cleanup. Used to assert zero mcp.makeHandler WARN on the happy path.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	handler := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// callToolStdio drives initialize + tools/call through the REAL stdio
// transport (Server.Listen). Unlike HandleMessage, stdio records clientInfo
// on the session at initialize, so ClientInfoFromContext — and therefore
// handshake-derived host attribution for capture-defaults — works.
//
// clientName/clientVersion become the initialize clientInfo (e.g. "grok").
// Returns the tool result text; fails the test on protocol or tool errors.
func (h *testHarness) callToolStdio(t *testing.T, clientName, clientVersion, name string, args any) string {
	t.Helper()

	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Server.Listen(ctx, serverReader, serverWriter)
	}()
	// Ensure the server goroutine exits even on early fatal.
	t.Cleanup(func() {
		_ = clientWriter.Close()
		_ = serverReader.Close()
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})

	writeLine := func(v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal stdio message: %v", err)
		}
		if _, err := clientWriter.Write(append(b, '\n')); err != nil {
			t.Fatalf("write stdio message: %v", err)
		}
	}
	readRPC := func() map[string]any {
		t.Helper()
		// Bound the read so a hung server fails the test instead of hanging CI.
		type res struct {
			line string
			err  error
		}
		ch := make(chan res, 1)
		go func() {
			// Read one newline-delimited JSON-RPC response.
			var buf []byte
			tmp := make([]byte, 1)
			for {
				n, err := clientReader.Read(tmp)
				if n > 0 {
					buf = append(buf, tmp[0])
					if tmp[0] == '\n' {
						ch <- res{line: string(buf), err: nil}
						return
					}
				}
				if err != nil {
					ch <- res{err: err}
					return
				}
			}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("read stdio response: %v", r.err)
			}
			var msg map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(r.line)), &msg); err != nil {
				t.Fatalf("unmarshal stdio response %q: %v", r.line, err)
			}
			return msg
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for stdio response")
			return nil
		}
	}

	if clientName == "" {
		clientName = "integration-test"
	}
	if clientVersion == "" {
		clientVersion = "0.1.0"
	}

	writeLine(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": clientName, "version": clientVersion},
		},
	})
	initResp := readRPC()
	if initResp["error"] != nil {
		t.Fatalf("initialize error: %v", initResp["error"])
	}

	writeLine(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})

	writeLine(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	})
	callResp := readRPC()
	if callResp["error"] != nil {
		t.Fatalf("tools/call protocol error: %v", callResp["error"])
	}
	result, _ := callResp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("tools/call missing result: %v", callResp)
	}
	if isErr, _ := result["isError"].(bool); isErr {
		// Surface tool error text.
		content, _ := result["content"].([]any)
		if len(content) > 0 {
			if c0, ok := content[0].(map[string]any); ok {
				t.Fatalf("tool %q returned error: %v", name, c0["text"])
			}
		}
		t.Fatalf("tool %q returned isError without content: %v", name, result)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tool %q returned empty content", name)
	}
	c0, _ := content[0].(map[string]any)
	text, _ := c0["text"].(string)
	if text == "" {
		t.Fatalf("tool %q returned empty text: %v", name, content[0])
	}
	return text
}

// callTool sends a tools/call JSON-RPC request and returns the text content.
// Fails the test on protocol errors and on any tool refusal.
func (h *testHarness) callTool(t *testing.T, name string, args any) string {
	t.Helper()

	text, isErr := h.callToolRaw(t, name, args)
	if isErr {
		t.Fatalf("tool %q returned error: %s", name, text)
	}
	return text
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
