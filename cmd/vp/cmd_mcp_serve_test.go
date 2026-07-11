// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

const mcpServeTestToken = "test-bearer-token"

// newServeTestStack builds a serverStack backed by a temp vault and a mock
// embedder so buildMCPServeHandler can register the REAL tool set without an
// ONNX model. Only the fields buildMCPServeHandler reads are populated.
func newServeTestStack(t *testing.T) *serverStack {
	t.Helper()
	v := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	emb := embedder.NewMock(384)
	eng := search.NewEngine(emb, v, cfg)
	t.Cleanup(func() { eng.Close() })
	return &serverStack{
		vault:    v,
		cfg:      cfg,
		emb:      emb,
		eng:      eng,
		resolver: vpctx.NewResolver(v.Root),
	}
}

// listToolNames initializes an MCP client against handler (served via
// httptest) and returns the set of tool names from tools/list. token must match
// what the handler was built with.
func listToolNames(t *testing.T, handler http.Handler, token string) map[string]bool {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c, err := client.NewStreamableHttpClient(ts.URL,
		transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + token,
		}),
	)
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

	listed, err := c.ListTools(ctx, mcplib.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := make(map[string]bool, len(listed.Tools))
	for _, tl := range listed.Tools {
		names[tl.Name] = true
	}
	return names
}

// TestMCPServeCommandConstructor checks the command's identity and flag set.
func TestMCPServeCommandConstructor(t *testing.T) {
	cmd := cmdMCPServe()
	if cmd.Name != "mcp serve" {
		t.Errorf("name = %q, want %q", cmd.Name, "mcp serve")
	}
	if cmd.MutatesVault {
		t.Error("mcp serve must NOT be marked MutatesVault: it is read-only by " +
			"default and write tools carry their own surface gate")
	}
	want := []string{"--port", "--addr", "--allow-writes", "--bearer-token-env"}
	for _, name := range want {
		found := false
		for _, f := range cmd.Flags {
			if f.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("missing flag %q", name)
		}
	}
}

// TestMCPServeBadFlags verifies an unknown flag is a user error.
func TestMCPServeBadFlags(t *testing.T) {
	cmd := cmdMCPServe()
	if code := cmd.Run([]string{"--unknown"}); code != 1 {
		t.Errorf("exit code = %d, want 1 (ExitUser)", code)
	}
}

// TestMCPServeReadOnlyFiltersMutatingTools is the core security assertion: with
// the REAL RegisterAll tool set, the default (read-only) handler exposes none of
// the 20 MutatingToolNames over tools/list, while read tools remain present.
func TestMCPServeReadOnlyFiltersMutatingTools(t *testing.T) {
	stack := newServeTestStack(t)
	handler := buildMCPServeHandler(stack, mcpServeTestToken, false /* allowWrites */)

	names := listToolNames(t, handler, mcpServeTestToken)

	for _, m := range tools.MutatingToolNames {
		if names[m] {
			t.Errorf("mutating tool %q must be ABSENT in read-only mode", m)
		}
	}
	// A representative read tool must survive the filtering.
	for _, r := range []string{"vp_search", "vp_vault_read"} {
		if !names[r] {
			t.Errorf("read tool %q missing from read-only server", r)
		}
	}
}

// TestMCPServeAllowWritesExposesMutatingTools verifies that, without filtering
// (--allow-writes), every MutatingToolName is present over tools/list.
func TestMCPServeAllowWritesExposesMutatingTools(t *testing.T) {
	stack := newServeTestStack(t)
	handler := buildMCPServeHandler(stack, mcpServeTestToken, true /* allowWrites */)

	names := listToolNames(t, handler, mcpServeTestToken)

	for _, m := range tools.MutatingToolNames {
		if !names[m] {
			t.Errorf("mutating tool %q must be PRESENT with --allow-writes", m)
		}
	}
}

// callToolHTTP initializes an MCP client against handler (served via httptest)
// and invokes one tool, returning the result and any transport/handler error.
func callToolHTTP(t *testing.T, handler http.Handler, token, tool string, args map[string]any) (*mcplib.CallToolResult, error) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c, err := client.NewStreamableHttpClient(ts.URL,
		transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + token,
		}),
	)
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
	req.Params.Name = tool
	req.Params.Arguments = args
	return c.CallTool(ctx, req)
}

// TestMCPServeAllowWritesIsSurfaceGated pins the transport-parity hole: the
// streamable-HTTP transport ran no contextFunc, so VaultFromContext was nil,
// gateIfMutating saw root == "", and EVERY mutating tool served over
// `vp mcp serve --allow-writes` bypassed the surface gate entirely — writing to
// a vault stamped by a newer binary was silently permitted.
//
// With the vault injected into the request context (StreamableHTTPHandler), an
// ahead vault must now refuse the write with the standard remediation, exactly
// as it does over stdio.
func TestMCPServeAllowWritesIsSurfaceGated(t *testing.T) {
	stack := newServeTestStack(t)

	// Stamp the vault one surface version ahead of this binary.
	stampDir := filepath.Join(stack.vault.Root, "Projects", "p")
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion+1, "tester"); err != nil {
		t.Fatal(err)
	}

	handler := buildMCPServeHandler(stack, mcpServeTestToken, true /* allowWrites */)

	res, err := callToolHTTP(t, handler, mcpServeTestToken, "vp_manage_task", map[string]any{
		"project": "p",
		"action":  "create",
		"task":    "should-never-be-created",
	})

	// The gate surfaces in-band as a tool error (there is deliberately no
	// startup gate), so accept either a transport error or an IsError result —
	// what matters is that the write was refused and the remediation is present.
	var text string
	switch {
	case err != nil:
		text = err.Error()
	case res != nil && res.IsError:
		for _, c := range res.Content {
			if tc, ok := c.(mcplib.TextContent); ok {
				text += tc.Text
			}
		}
	default:
		t.Fatal("mutating tool over HTTP against an ahead vault must be refused, but it succeeded")
	}

	if !strings.Contains(text, "git pull && make install") {
		t.Errorf("refusal should carry the surface remediation, got: %s", text)
	}
}

// TestMCPServeRoundTrip drives a full initialize + tools/list over HTTP with a
// valid bearer, asserting a read tool is present and a mutating tool absent in
// the default read-only configuration — the end-to-end production path.
func TestMCPServeRoundTrip(t *testing.T) {
	stack := newServeTestStack(t)
	handler := buildMCPServeHandler(stack, mcpServeTestToken, false)

	names := listToolNames(t, handler, mcpServeTestToken)

	if !names["vp_search"] {
		t.Error("expected read tool vp_search over HTTP round-trip")
	}
	if names["vp_vault_write"] {
		t.Error("mutating tool vp_vault_write must not be reachable in read-only mode")
	}
}

// TestMCPServeHandlerEnforcesAuth confirms buildMCPServeHandler wires the bearer
// token through: a request with no Authorization header is rejected with 401.
func TestMCPServeHandlerEnforcesAuth(t *testing.T) {
	stack := newServeTestStack(t)
	handler := buildMCPServeHandler(stack, mcpServeTestToken, false)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no bearer: status = %d, want 401", resp.StatusCode)
	}
}

// parseServeFlags is a test helper that parses args against the command's flag
// set, failing the test on a parse error.
func parseServeFlags(t *testing.T, args []string) *cli.FlagValues {
	t.Helper()
	fv, err := cli.ParseFlags(mcpServeFlags, args)
	if err != nil {
		t.Fatalf("parse flags %v: %v", args, err)
	}
	return fv
}

// TestResolveMCPServeConfigDefaults checks the default address (127.0.0.1:7423),
// the default token env name, and read-only mode when no flags are given.
func TestResolveMCPServeConfigDefaults(t *testing.T) {
	t.Setenv("VP_MCP_BEARER_TOKEN", "")
	sc := resolveMCPServeConfig(parseServeFlags(t, nil), storage.Config{})
	if sc.addr != "127.0.0.1:7423" {
		t.Errorf("addr = %q, want 127.0.0.1:7423", sc.addr)
	}
	if sc.tokenEnv != "VP_MCP_BEARER_TOKEN" {
		t.Errorf("tokenEnv = %q, want VP_MCP_BEARER_TOKEN", sc.tokenEnv)
	}
	if sc.allowWrites {
		t.Error("allowWrites should default false")
	}
}

// TestResolveMCPServeConfigPortFallback verifies --port falls back to
// cfg.HTTPPort when the flag is absent, and that an explicit --port wins.
func TestResolveMCPServeConfigPortFallback(t *testing.T) {
	sc := resolveMCPServeConfig(parseServeFlags(t, nil), storage.Config{HTTPPort: 9999})
	if sc.addr != "127.0.0.1:9999" {
		t.Errorf("addr = %q, want 127.0.0.1:9999 (cfg fallback)", sc.addr)
	}
	sc = resolveMCPServeConfig(parseServeFlags(t, []string{"--port", "8080"}), storage.Config{HTTPPort: 9999})
	if sc.addr != "127.0.0.1:8080" {
		t.Errorf("addr = %q, want 127.0.0.1:8080 (flag wins)", sc.addr)
	}
}

// TestResolveMCPServeConfigTokenAndAddr verifies --addr binding, --allow-writes,
// and that the token value is read from the env var named by --bearer-token-env.
func TestResolveMCPServeConfigTokenAndAddr(t *testing.T) {
	t.Setenv("MY_TOKEN_VAR", "hunter2")
	fv := parseServeFlags(t, []string{
		"--addr", "0.0.0.0", "--port", "7000",
		"--allow-writes", "--bearer-token-env", "MY_TOKEN_VAR",
	})
	sc := resolveMCPServeConfig(fv, storage.Config{})
	if sc.addr != "0.0.0.0:7000" {
		t.Errorf("addr = %q, want 0.0.0.0:7000", sc.addr)
	}
	if !sc.allowWrites {
		t.Error("allowWrites should be true")
	}
	if sc.tokenEnv != "MY_TOKEN_VAR" || sc.token != "hunter2" {
		t.Errorf("token resolution = (%q, %q), want (MY_TOKEN_VAR, hunter2)", sc.tokenEnv, sc.token)
	}
}

// TestStartupLines covers the three startup-line branches: the unauthenticated
// warning, the allow-writes warning, and the read-only/read-write mode line.
func TestStartupLines(t *testing.T) {
	// Unauthenticated, read-only.
	lines := mcpServeConfig{addr: "127.0.0.1:7423", tokenEnv: "VP_MCP_BEARER_TOKEN"}.startupLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "UNAUTHENTICATED") {
		t.Errorf("missing unauthenticated warning: %q", joined)
	}
	if !strings.Contains(joined, "(read-only)") {
		t.Errorf("missing read-only mode: %q", joined)
	}
	if strings.Contains(joined, "--allow-writes is set") {
		t.Errorf("unexpected allow-writes warning: %q", joined)
	}

	// Authenticated, writes exposed.
	lines = mcpServeConfig{addr: "127.0.0.1:7423", token: "x", allowWrites: true}.startupLines()
	joined = strings.Join(lines, "\n")
	if strings.Contains(joined, "UNAUTHENTICATED") {
		t.Errorf("unexpected unauthenticated warning: %q", joined)
	}
	if !strings.Contains(joined, "--allow-writes is set") {
		t.Errorf("missing allow-writes warning: %q", joined)
	}
	if !strings.Contains(joined, "(read-write)") {
		t.Errorf("missing read-write mode: %q", joined)
	}
}

// TestServeHTTPGracefulShutdown binds an ephemeral port, confirms the handler is
// reachable, then cancels the context and asserts serveHTTP returns nil (clean
// shutdown) rather than an error.
func TestServeHTTPGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveHTTP(ctx, addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}()

	// Wait until the server accepts a connection, then trigger shutdown.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, derr := http.Get("http://" + addr + "/")
		if derr == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server never came up: %v", derr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("serveHTTP returned %v, want nil on graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveHTTP did not return after context cancel")
	}
}
