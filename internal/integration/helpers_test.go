// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// testHarness wraps testinfra.TestHarness so every existing call site in this
// package — including the nine files that add their own methods/helpers on
// top of *testHarness outside this file — keeps compiling unchanged. The
// harness implementation itself now lives in internal/testinfra, which is
// importable from outside this package (internal/integration is otherwise all
// _test.go files with no importable surface of its own); this file is a thin
// forwarding shim over it, not a second copy of the logic.
//
// A type alias cannot substitute for this: Go has no way to alias methods, so
// every method below is a genuine one-line forwarder, not a doc comment on an
// alias.
type testHarness struct {
	*testinfra.TestHarness
}

// newHarness creates a full-stack test harness.
// If useRealEmbedder is true, requires ONNX model (skips in short mode).
func newHarness(t *testing.T, useRealEmbedder bool, cfgOverrides ...func(*storage.Config)) *testHarness {
	t.Helper()
	return &testHarness{testinfra.NewHarness(t, useRealEmbedder, cfgOverrides...)}
}

// newHarnessWithEmbedder builds the same full stack as newHarness around a
// caller-supplied embedder. Tests that need to observe WHEN the embedder is
// constructed — rather than what it returns — wrap their own constructor in
// embedder.NewLazy and pass it here; the harness must not construct it for them.
func newHarnessWithEmbedder(t *testing.T, emb embedder.Embedder, cfgOverrides ...func(*storage.Config)) *testHarness {
	t.Helper()
	return &testHarness{testinfra.NewHarnessWithEmbedder(t, emb, cfgOverrides...)}
}

// registerAllTools registers all MCP tools on the harness server.
func (h *testHarness) registerAllTools(t *testing.T) { h.TestHarness.RegisterAllTools(t) }

// initMCP sends the initialize + notifications/initialized handshake.
func (h *testHarness) initMCP(t *testing.T) { h.TestHarness.InitMCP(t) }

// seedProject materializes Projects/<slug>/ in the harness vault.
func (h *testHarness) seedProject(t *testing.T, slug string) { h.TestHarness.SeedProject(t, slug) }

// callToolRaw sends a tools/call JSON-RPC request through the REAL MCP server
// and returns the text content along with whether the call was refused.
func (h *testHarness) callToolRaw(t *testing.T, name string, args any) (text string, isErr bool) {
	return h.TestHarness.CallToolRaw(t, name, args)
}

// callToolStdio drives initialize + tools/call through the REAL stdio
// transport (Server.Listen).
func (h *testHarness) callToolStdio(t *testing.T, clientName, clientVersion, name string, args any) string {
	return h.TestHarness.CallToolStdio(t, clientName, clientVersion, name, args)
}

// callTool sends a tools/call JSON-RPC request and returns the text content.
func (h *testHarness) callTool(t *testing.T, name string, args any) string {
	return h.TestHarness.CallTool(t, name, args)
}

// addDrawer is a convenience for writing a drawer and returning it with the generated ID.
func (h *testHarness) addDrawer(t *testing.T, project, wing, room, content, hall, date string) storage.Drawer {
	return h.TestHarness.AddDrawer(t, project, wing, room, content, hall, date)
}

// captureLogs installs a debug-level slog JSON handler that writes to a
// buffer for the duration of the test, restoring the previous default on
// cleanup. Used to assert zero mcp.makeHandler WARN on the happy path.
func captureLogs(t *testing.T) *bytes.Buffer { return testinfra.CaptureLogs(t) }

// taskBody builds a vp_manage_task create body that satisfies BOTH doors a
// real create body must pass. See testinfra.TaskBody for the full doc.
func taskBody(lead string) string { return testinfra.TaskBody(lead) }
