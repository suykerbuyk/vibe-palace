// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	vault := storage.NewVault(t.TempDir())
	return NewServer(vault)
}

// TestServerInstructionsConst guards the package-level bootstrap directive
// against accidental emptying — it must be non-empty and point clients at
// vp_bootstrap_context.
func TestServerInstructionsConst(t *testing.T) {
	if ServerInstructions == "" {
		t.Fatal("ServerInstructions is empty")
	}
	if !strings.Contains(ServerInstructions, "vp_bootstrap_context") {
		t.Errorf("ServerInstructions missing vp_bootstrap_context: %q", ServerInstructions)
	}
}

func TestNewServer(t *testing.T) {
	srv := testServer(t)
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
	if srv.mcp == nil {
		t.Fatal("NewServer: mcp field is nil")
	}
	if srv.vault == nil {
		t.Fatal("NewServer: vault field is nil")
	}
}

func TestHandleInitialize(t *testing.T) {
	srv := testServer(t)

	msg := json.RawMessage(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "initialize",
		"params": {
			"protocolVersion": "2025-03-26",
			"capabilities": {},
			"clientInfo": {"name": "test-client", "version": "0.1.0"}
		}
	}`)

	resp := srv.HandleMessage(context.Background(), msg)
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	// Marshal result to inspect fields.
	raw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Capabilities struct {
			Tools *struct{} `json:"tools"`
		} `json:"capabilities"`
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// The server-level bootstrap directive must reach clients in the
	// initialize response so hosts that surface it (Grok, etc.) prompt the
	// agent to load project context on connect.
	if result.Instructions == "" {
		t.Error("initialize result: instructions is empty")
	}
	if !strings.Contains(result.Instructions, "vp_bootstrap_context") {
		t.Errorf("initialize instructions missing vp_bootstrap_context: %q", result.Instructions)
	}

	if result.ServerInfo.Name != "vibe-palace" {
		t.Errorf("serverInfo.name = %q, want %q", result.ServerInfo.Name, "vibe-palace")
	}
	if result.ServerInfo.Version != "0.1.0" {
		t.Errorf("serverInfo.version = %q, want %q", result.ServerInfo.Version, "0.1.0")
	}
	if result.Capabilities.Tools == nil {
		t.Error("capabilities.tools is nil, expected it to be present")
	}
}

func TestHandleUnknownMethod(t *testing.T) {
	srv := testServer(t)

	msg := json.RawMessage(`{
		"jsonrpc": "2.0",
		"id": 2,
		"method": "foo/bar",
		"params": {}
	}`)

	resp := srv.HandleMessage(context.Background(), msg)
	rpcErr, ok := resp.(mcplib.JSONRPCError)
	if !ok {
		t.Fatalf("expected JSONRPCError, got %T: %+v", resp, resp)
	}
	if rpcErr.Error.Code != mcplib.METHOD_NOT_FOUND {
		t.Errorf("error code = %d, want %d (METHOD_NOT_FOUND)", rpcErr.Error.Code, mcplib.METHOD_NOT_FOUND)
	}
}

func TestHandleMalformedJSON(t *testing.T) {
	srv := testServer(t)

	resp := srv.HandleMessage(context.Background(), json.RawMessage(`{invalid json`))
	rpcErr, ok := resp.(mcplib.JSONRPCError)
	if !ok {
		t.Fatalf("expected JSONRPCError, got %T: %+v", resp, resp)
	}
	if rpcErr.Error.Code != mcplib.PARSE_ERROR {
		t.Errorf("error code = %d, want %d (PARSE_ERROR)", rpcErr.Error.Code, mcplib.PARSE_ERROR)
	}
}

func TestHandleNotification(t *testing.T) {
	srv := testServer(t)

	// Notifications have no "id" field and must not produce a response.
	msg := json.RawMessage(`{
		"jsonrpc": "2.0",
		"method": "notifications/initialized"
	}`)

	resp := srv.HandleMessage(context.Background(), msg)
	if resp != nil {
		t.Errorf("expected nil response for notification, got %T: %+v", resp, resp)
	}
}

func TestListen(t *testing.T) {
	srv := testServer(t)

	// Create pipes to simulate stdin/stdout.
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run server in background.
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Listen(ctx, serverReader, serverWriter)
	}()

	// Send initialize request.
	initMsg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0.1.0"}}}` + "\n"
	if _, err := io.WriteString(clientWriter, initMsg); err != nil {
		t.Fatalf("write initialize: %v", err)
	}

	// Read response.
	buf := make([]byte, 4096)
	n, err := clientReader.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Result  struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Result.ServerInfo.Name != "vibe-palace" {
		t.Errorf("serverInfo.name = %q, want %q", resp.Result.ServerInfo.Name, "vibe-palace")
	}

	// Close client writer to signal EOF, then cancel context.
	clientWriter.Close()
	cancel()

	if err := <-errCh; err != nil {
		t.Logf("Listen returned: %v (expected on pipe close)", err)
	}
}

func TestVaultFromContext(t *testing.T) {
	t.Run("empty context", func(t *testing.T) {
		v := VaultFromContext(context.Background())
		if v != nil {
			t.Errorf("expected nil from empty context, got %+v", v)
		}
	})

	t.Run("with vault", func(t *testing.T) {
		vault := storage.NewVault("/tmp/test-vault")
		ctx := context.WithValue(context.Background(), vaultKey, vault)
		v := VaultFromContext(ctx)
		if v != vault {
			t.Errorf("VaultFromContext returned %p, want %p", v, vault)
		}
	})
}
