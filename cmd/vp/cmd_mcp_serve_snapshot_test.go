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
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultstack"
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

// TestMCPServeRoutesProjectKeyedWriteToBoundVault proves `vp mcp serve`'s
// handler ROUTES (U5b): a mutating tools/call for `qa`, bound to vault B, is NOT
// refused — it is served against B. The serve stack carries a real vault-stack
// registry whose BuildFunc returns a stack over the requested root; the snapshot
// drives the route (qa→B). Proof of landing: the write succeeds AND vault A — the
// startup vault — never gains a `qa` tree; a resolver that failed to route would
// write A (or fail, qa not existing in A).
func TestMCPServeRoutesProjectKeyedWriteToBoundVault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	vaultA := filepath.Join(home, "vault-a") // the server's startup vault
	vaultB := filepath.Join(home, "vault-b") // qa is bound here
	manifest := fmt.Sprintf("format = %d\n", surface.RequiredDataFormat)
	snapWrite(t, filepath.Join(vaultA, ".vibe-palace", "vault.toml"), manifest)
	snapWrite(t, filepath.Join(vaultB, ".vibe-palace", "vault.toml"), manifest)
	testutil.InitProject(t, vaultB, "qa") // qa exists only in B
	cfg, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	snapWrite(t, cfg, "vault_path = \""+vaultA+"\"\n\n[project_vaults]\nqa = \""+vaultB+"\"\n")

	snap, err := storage.BuildProjectVaultSnapshot(context.Background())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	cfgVal := storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"}
	reg := vaultstack.NewRegistry(routeTestBuildFunc(t, cfgVal, ""), embedder.NewSharedModelCache())
	startup, err := reg.StackFor(context.Background(), vaultA)
	if err != nil {
		t.Fatalf("build startup stack: %v", err)
	}
	stack := &serverStack{
		vault:        startup.Vault,
		cfg:          cfgVal,
		eng:          startup.Engine,
		resolver:     startup.Resolver,
		snap:         snap,
		reg:          reg,
		startupStack: startup,
	}
	t.Cleanup(func() { reg.Close() })

	handler := buildMCPServeHandler(stack, mcpServeTestToken, true /* allowWrites */)
	isErr, text := callToolText(t, handler, mcpServeTestToken, "vp_update_resume", map[string]any{
		"project":         "qa",
		"content":         "routed resume body",
		"expected_sha256": "",
	})
	if isErr {
		t.Fatalf("a routed BoundElsewhere write must succeed, got error: %s", text)
	}
	if _, statErr := os.Stat(filepath.Join(vaultA, "Projects", "qa")); statErr == nil {
		t.Fatal("the write landed in the STARTUP vault A (Projects/qa exists there) — it was not routed to B")
	}
	if _, statErr := os.Stat(filepath.Join(vaultB, "Projects", "qa", "resume.md")); statErr != nil {
		t.Fatalf("the routed resume did not land in vault B: %v", statErr)
	}
}

// routeTestBuildFunc returns a vaultstack.BuildFunc that builds a stack over any
// requested root with a mock-embedder engine (no ONNX). If failRoot is non-empty
// and matches, it returns an error — to exercise the fail-closed StackFor path.
func routeTestBuildFunc(t *testing.T, cfg storage.Config, failRoot string) vaultstack.BuildFunc {
	t.Helper()
	return func(ctx context.Context, root string) (*vaultstack.VaultStack, error) {
		if failRoot != "" && root == failRoot {
			return nil, fmt.Errorf("test: refusing to build a stack for %q", root)
		}
		v := storage.NewVault(root)
		eng := search.NewEngine(embedder.NewMock(384), v, cfg)
		return vaultstack.NewVaultStack(root, v, cfg, eng, vpctx.NewResolver(root), nil), nil
	}
}
