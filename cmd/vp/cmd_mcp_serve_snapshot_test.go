// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcplib "github.com/mark3labs/mcp-go/mcp"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func snapWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// callToolText initializes an MCP client against handler, calls one tool, and
// returns (isError, the concatenated text content).
func callToolText(t *testing.T, handler http.Handler, token, name string, args map[string]any) (bool, string) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c, err := client.NewStreamableHttpClient(ts.URL,
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}
	initReq := mcplib.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcplib.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcplib.Implementation{Name: "test-client", Version: "0.1.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("client initialize: %v", err)
	}

	req := mcplib.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("call tool %s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcplib.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return res.IsError, b.String()
}

// TestMCPServeInstallsProjectVaultSnapshot proves `vp mcp serve`'s handler
// installs the stack's [project_vaults] snapshot on its (second) server's
// registry: a mutating tools/call for a project bound to ANOTHER vault is
// refused with the cross-vault message. Were the snapshot not installed, the
// gate would instead report an unbuilt snapshot — so asserting the "wrong
// vault" + bound-root message pins the specific built snapshot to the gate.
func TestMCPServeInstallsProjectVaultSnapshot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	vaultA := filepath.Join(home, "vault-a") // the server's startup vault
	vaultB := filepath.Join(home, "vault-b") // qa is bound here
	manifest := fmt.Sprintf("format = %d\n", surface.RequiredDataFormat)
	snapWrite(t, filepath.Join(vaultA, ".vibe-palace", "vault.toml"), manifest)
	snapWrite(t, filepath.Join(vaultB, ".vibe-palace", "vault.toml"), manifest)
	cfg, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	snapWrite(t, cfg, "vault_path = \""+vaultA+"\"\n\n[project_vaults]\nqa = \""+vaultB+"\"\n")

	snap, err := storage.BuildProjectVaultSnapshot(context.Background())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	v := storage.NewVault(vaultA)
	cfgVal := storage.Config{SearchDefaultLimit: 10}
	emb := embedder.NewMock(384)
	eng := search.NewEngine(emb, v, cfgVal)
	t.Cleanup(func() { eng.Close() })
	stack := &serverStack{
		vault:    v,
		cfg:      cfgVal,
		emb:      emb,
		eng:      eng,
		resolver: vpctx.NewResolver(v.Root),
		snap:     snap,
	}

	handler := buildMCPServeHandler(stack, mcpServeTestToken, true /* allowWrites */)
	isErr, text := callToolText(t, handler, mcpServeTestToken, "vp_update_resume", map[string]any{
		"project":         "qa",
		"content":         "x",
		"expected_sha256": "",
	})
	if !isErr {
		t.Fatalf("expected a cross-vault refusal error result, got success: %s", text)
	}
	if !strings.Contains(text, "wrong vault") || !strings.Contains(text, vaultB) {
		t.Fatalf("serve handler did not install the cross-vault snapshot; result was: %s", text)
	}
}
