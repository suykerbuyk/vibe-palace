// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

const testBearerToken = "s3cr3t-token"

// testStreamableServer builds a Server with an echo tool registered and returns
// an httptest.Server serving the streamable-HTTP transport wrapped in the given
// token's bearer auth. The returned *Server is exposed so tests can mutate its
// tool set (e.g. DeleteTools) before issuing requests.
func testStreamableServer(t *testing.T, token string) (*httptest.Server, *Server) {
	t.Helper()
	srv := testServer(t)
	srv.Registry().MustRegister(Tool{
		Name:        "echo",
		Description: "echoes input",
		Schema:      echoSchema,
		Handler:     echoHandler,
	})
	ts := httptest.NewServer(srv.StreamableHTTPHandler(token))
	t.Cleanup(ts.Close)
	return ts, srv
}

// postNoBody issues a bare POST to url with the given Authorization header value
// (omitted when empty) and returns the response status code. It is used to probe
// the auth middleware without speaking the MCP protocol.
func postNoBody(t *testing.T, url, authHeader string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestStreamableAuthRejectsMissingHeader verifies a request with no
// Authorization header is rejected with 401 when a token is configured.
func TestStreamableAuthRejectsMissingHeader(t *testing.T) {
	ts, _ := testStreamableServer(t, testBearerToken)
	if got := postNoBody(t, ts.URL, ""); got != http.StatusUnauthorized {
		t.Fatalf("missing header: want 401, got %d", got)
	}
}

// TestStreamableAuthRejectsWrongScheme verifies a non-Bearer scheme is rejected.
func TestStreamableAuthRejectsWrongScheme(t *testing.T) {
	ts, _ := testStreamableServer(t, testBearerToken)
	if got := postNoBody(t, ts.URL, "Basic "+testBearerToken); got != http.StatusUnauthorized {
		t.Fatalf("wrong scheme: want 401, got %d", got)
	}
}

// TestStreamableAuthRejectsWrongToken exercises the constant-time comparison
// path: correct scheme, wrong credential → 401.
func TestStreamableAuthRejectsWrongToken(t *testing.T) {
	ts, _ := testStreamableServer(t, testBearerToken)
	if got := postNoBody(t, ts.URL, "Bearer not-the-token"); got != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", got)
	}
}

// TestBearerToken unit-tests the header parser directly. The empty-credential
// case ("Bearer " with only whitespace after the scheme) cannot be exercised
// over real HTTP because net/http trims trailing header whitespace, so it is
// asserted here at the function boundary.
func TestBearerToken(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
		ok     bool
	}{
		{"empty header", "", "", false},
		{"too short", "Bear", "", false},
		{"wrong scheme", "Basic abc", "", false},
		{"empty credential", "Bearer    ", "", false},
		{"valid", "Bearer abc123", "abc123", true},
		{"case-insensitive scheme", "bEaReR abc123", "abc123", true},
		{"surrounding space trimmed", "Bearer   tok  ", "tok", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := bearerToken(tc.header)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("bearerToken(%q) = (%q, %v), want (%q, %v)", tc.header, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestStreamableAuthAcceptsValidToken verifies a correct Bearer token reaches
// the handler — the response is not a 401 (the MCP layer rejects the empty body
// with its own status, which is all we assert here).
func TestStreamableAuthAcceptsValidToken(t *testing.T) {
	ts, _ := testStreamableServer(t, testBearerToken)
	if got := postNoBody(t, ts.URL, "Bearer "+testBearerToken); got == http.StatusUnauthorized {
		t.Fatalf("valid token: request was rejected with 401, want it to reach the handler")
	}
}

// TestStreamableOpenWhenNoToken verifies an empty token disables auth: a request
// with no Authorization header reaches the handler (never 401).
func TestStreamableOpenWhenNoToken(t *testing.T) {
	ts, _ := testStreamableServer(t, "")
	if got := postNoBody(t, ts.URL, ""); got == http.StatusUnauthorized {
		t.Fatalf("open server: request was rejected with 401, want it to reach the handler")
	}
}

// newMCPClient initializes an MCP client over the streamable-HTTP transport,
// sending the given bearer token on every request.
func newMCPClient(t *testing.T, baseURL, token string) *client.Client {
	t.Helper()
	c, err := client.NewStreamableHttpClient(baseURL,
		transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + token,
		}),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	return c
}

// hasTool reports whether name appears in the tools/list result.
func hasTool(tools []mcplib.Tool, name string) bool {
	for _, tl := range tools {
		if tl.Name == name {
			return true
		}
	}
	return false
}

// TestStreamableRoundTrip drives a full initialize + tools/list + tools/call
// over HTTP with a valid bearer token, asserting the echo tool is listed and
// callable.
func TestStreamableRoundTrip(t *testing.T) {
	ts, _ := testStreamableServer(t, testBearerToken)
	c := newMCPClient(t, ts.URL, testBearerToken)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	listed, err := c.ListTools(ctx, mcplib.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if !hasTool(listed.Tools, "echo") {
		t.Fatalf("echo tool not present in tools/list: %+v", listed.Tools)
	}

	callReq := mcplib.CallToolRequest{}
	callReq.Params.Name = "echo"
	callReq.Params.Arguments = map[string]any{"message": "hello"}
	res, err := c.CallTool(ctx, callReq)
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if res.IsError {
		t.Fatalf("echo call returned error result: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("echo call returned no content")
	}
}

// TestStreamableDeleteToolsFiltersListing registers a tool, deletes it via the
// DeleteTools passthrough, and asserts it is gone from tools/list.
func TestStreamableDeleteToolsFiltersListing(t *testing.T) {
	ts, srv := testStreamableServer(t, testBearerToken)

	srv.DeleteTools("echo")

	c := newMCPClient(t, ts.URL, testBearerToken)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	listed, err := c.ListTools(ctx, mcplib.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if hasTool(listed.Tools, "echo") {
		t.Fatalf("echo tool still present after DeleteTools: %+v", listed.Tools)
	}
}

// TestStreamableResourceRoundTrip is the highest-value guard for the Phase-2
// resources layer: it proves a content resource is listed AND readable over the
// streamable-HTTP transport, which injects NO contextFunc. A handler that read
// the vault from ctx would nil-panic here; this passes only because the provider
// CLOSES OVER its body (the same contract internal/tools.RegisterResources must
// honour). The mcp package cannot import internal/tools (import cycle), so the
// resource is registered via the package-local AddContentResource primitive with
// a closure over a captured value — sufficient to assert the closes-over-vault
// contract holds across the real HTTP wire.
func TestStreamableResourceRoundTrip(t *testing.T) {
	ts, srv := testStreamableServer(t, testBearerToken)

	const (
		template = "vibe-palace://task/{project}/{slug}"
		uri      = "vibe-palace://task/demo/known-task"
		body     = "# Known Task\n\n**Status:** open — body reachable over HTTP\n"
	)
	// captured stands in for the vault the real provider closes over. Reading it
	// from ctx would fail over this transport; closing over it is the contract.
	captured := body
	srv.AddContentResource(template, "task", "text/markdown",
		func(_ context.Context, vars map[string]string) (string, string, error) {
			if vars["project"] != "demo" || vars["slug"] != "known-task" {
				t.Errorf("vars = %+v, want project=demo slug=known-task", vars)
			}
			return captured, "text/markdown", nil
		})

	c := newMCPClient(t, ts.URL, testBearerToken)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The template must be advertised over HTTP.
	tmpls, err := c.ListResourceTemplates(ctx, mcplib.ListResourceTemplatesRequest{})
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	if !hasResourceTemplate(tmpls.ResourceTemplates, template) {
		t.Fatalf("template %q not advertised over HTTP: %+v", template, tmpls.ResourceTemplates)
	}

	// The concrete URI must be readable over HTTP and byte-identical.
	if got := readResourceText(t, c, ctx, uri); got != body {
		t.Fatalf("resources/read over HTTP: text = %q, want %q", got, body)
	}
}

// TestStreamableReadResourceSurvivesDeleteTools pins the read-only HTTP serve
// shape: stripping mutating tools (as the read-only streamable server does) must
// NOT remove the non-mutating resource reader, and resources stay readable. The
// mcp package cannot import internal/tools, so a non-mutating stand-in named
// "vp_read_resource" and a mutating stand-in are registered locally; only the
// mutating one is deleted.
func TestStreamableReadResourceSurvivesDeleteTools(t *testing.T) {
	ts, srv := testStreamableServer(t, testBearerToken)

	// Non-mutating reader (the real vp_read_resource is non-mutating too).
	srv.Registry().MustRegister(Tool{
		Name:        "vp_read_resource",
		Description: "reads a resource (non-mutating stand-in)",
		Schema:      echoSchema,
		Handler:     echoHandler,
	})
	// Mutating tool that the read-only serve path would strip.
	srv.Registry().MustRegister(Tool{
		Name:        "vp_manage_task",
		Mutating:    true,
		Description: "mutates the vault (stand-in)",
		Schema:      echoSchema,
		Handler:     echoHandler,
	})

	const (
		template = "vibe-palace://resume/{project}"
		uri      = "vibe-palace://resume/demo"
		body     = "# Resume\n\nstill readable after stripping mutating tools\n"
	)
	captured := body
	srv.AddContentResource(template, "resume", "text/markdown",
		func(_ context.Context, _ map[string]string) (string, string, error) {
			return captured, "text/markdown", nil
		})

	// Strip mutating tools, as the read-only streamable serve path does.
	srv.DeleteTools("vp_manage_task")

	c := newMCPClient(t, ts.URL, testBearerToken)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	listed, err := c.ListTools(ctx, mcplib.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if hasTool(listed.Tools, "vp_manage_task") {
		t.Error("mutating vp_manage_task should be stripped from read-only listing")
	}
	if !hasTool(listed.Tools, "vp_read_resource") {
		t.Fatalf("non-mutating vp_read_resource must survive the strip: %+v", listed.Tools)
	}

	// Resources remain readable after the strip.
	if got := readResourceText(t, c, ctx, uri); got != body {
		t.Fatalf("resources/read after strip: text = %q, want %q", got, body)
	}
}

// hasResourceTemplate reports whether template appears (by raw URI template) in
// a resources/templates/list result.
func hasResourceTemplate(tmpls []mcplib.ResourceTemplate, template string) bool {
	for _, rt := range tmpls {
		if rt.URITemplate != nil && rt.URITemplate.Raw() == template {
			return true
		}
	}
	return false
}

// readResourceText issues a resources/read for uri over the client and returns
// the single text content, failing the test on any protocol or shape error.
func readResourceText(t *testing.T, c *client.Client, ctx context.Context, uri string) string {
	t.Helper()
	req := mcplib.ReadResourceRequest{}
	req.Params.URI = uri
	res, err := c.ReadResource(ctx, req)
	if err != nil {
		t.Fatalf("resources/read %q: %v", uri, err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("resources/read %q: %d contents, want 1", uri, len(res.Contents))
	}
	tc, ok := res.Contents[0].(mcplib.TextResourceContents)
	if !ok {
		t.Fatalf("resources/read %q: content type %T, want TextResourceContents", uri, res.Contents[0])
	}
	return tc.Text
}
