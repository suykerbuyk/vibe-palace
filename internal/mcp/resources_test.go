// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// initServer initializes srv through the protocol handshake so resource and
// tool requests are accepted.
func initServer(t *testing.T, srv *Server) {
	t.Helper()
	srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": {
			"protocolVersion": "2025-03-26",
			"capabilities": {},
			"clientInfo": {"name": "test", "version": "0.1.0"}
		}
	}`))
	srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "method": "notifications/initialized"
	}`))
}

// readResource sends a resources/read request for uri and returns the decoded
// response message.
func readResource(t *testing.T, srv *Server, uri string) mcplib.JSONRPCMessage {
	t.Helper()
	msg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "resources/read",
		"params":  map[string]any{"uri": uri},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return srv.HandleMessage(context.Background(), msg)
}

// TestAddContentResourceRoundTrip registers a content resource backed by a
// closed-over value, then reads it back through the protocol layer and asserts
// the URI, MIME type, and text are byte-identical.
func TestAddContentResourceRoundTrip(t *testing.T) {
	srv := testServer(t)

	const body = "# Title\n\nbyte-identical body\n"
	srv.AddContentResource("vibe-palace://test/{name}", "test", "text/markdown",
		func(_ context.Context, vars map[string]string) (string, string, error) {
			if vars["name"] != "alpha" {
				t.Errorf("vars[name] = %q, want %q", vars["name"], "alpha")
			}
			return body, "text/markdown", nil
		})

	initServer(t, srv)

	resp := readResource(t, srv, "vibe-palace://test/alpha")
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	raw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	var result struct {
		Contents []struct {
			URI      string `json:"uri"`
			MIMEType string `json:"mimeType"`
			Text     string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(result.Contents) != 1 {
		t.Fatalf("expected 1 content entry, got %d", len(result.Contents))
	}
	c := result.Contents[0]
	if c.URI != "vibe-palace://test/alpha" {
		t.Errorf("uri = %q, want %q", c.URI, "vibe-palace://test/alpha")
	}
	if c.MIMEType != "text/markdown" {
		t.Errorf("mimeType = %q, want %q", c.MIMEType, "text/markdown")
	}
	if c.Text != body {
		t.Errorf("text = %q, want %q", c.Text, body)
	}
}

// TestReadResourceUnknownURI verifies that reading a URI with no matching
// template yields a JSON-RPC error rather than a result.
func TestReadResourceUnknownURI(t *testing.T) {
	srv := testServer(t)
	srv.AddContentResource("vibe-palace://test/{name}", "test", "text/markdown",
		func(_ context.Context, _ map[string]string) (string, string, error) {
			return "unused", "text/markdown", nil
		})

	initServer(t, srv)

	resp := readResource(t, srv, "vibe-palace://nonexistent/thing")
	if _, ok := resp.(mcplib.JSONRPCError); !ok {
		t.Fatalf("expected JSONRPCError for unknown URI, got %T: %+v", resp, resp)
	}
}

// TestResourceCapabilityAdvertised confirms the initialize response advertises
// the resources capability enabled in NewServer.
func TestResourceCapabilityAdvertised(t *testing.T) {
	srv := testServer(t)

	resp := srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": {
			"protocolVersion": "2025-03-26",
			"capabilities": {},
			"clientInfo": {"name": "test", "version": "0.1.0"}
		}
	}`))
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T", resp)
	}
	raw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result struct {
		Capabilities struct {
			Resources *struct{} `json:"resources"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Capabilities.Resources == nil {
		t.Error("initialize result: resources capability not advertised")
	}
}
