// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// testHTTPServer creates an httptest.Server backed by a real MCP server with
// an echo tool registered.
func testHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := testServer(t)
	srv.Registry().MustRegister(Tool{
		Name:        "echo",
		Description: "echoes input",
		Schema:      echoSchema,
		Handler:     echoHandler,
	})
	h := NewHTTPServer(srv, ":0")
	return httptest.NewServer(h.Handler())
}

func TestHealthEndpoint(t *testing.T) {
	ts := testHTTPServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	if body["status"] != "ok" {
		t.Errorf("want status ok, got %q", body["status"])
	}
	if body["version"] != "0.1.0" {
		t.Errorf("want version 0.1.0, got %q", body["version"])
	}
}

func TestListToolsEndpoint(t *testing.T) {
	ts := testHTTPServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var tools []ToolInfo
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		t.Fatal(err)
	}

	if len(tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(tools))
	}

	if tools[0].Name != "echo" {
		t.Errorf("want tool name echo, got %q", tools[0].Name)
	}
	if tools[0].Description != "echoes input" {
		t.Errorf("want description 'echoes input', got %q", tools[0].Description)
	}
	if len(tools[0].Schema) == 0 {
		t.Error("want non-empty schema")
	}
}

func TestCallToolEndpoint(t *testing.T) {
	ts := testHTTPServer(t)
	defer ts.Close()

	body := bytes.NewBufferString(`{"message":"hello"}`)
	resp, err := http.Post(ts.URL+"/tools/echo", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}

	inner, ok := result["result"].(map[string]any)
	if !ok {
		t.Fatalf("want result object, got %T", result["result"])
	}
	if inner["message"] != "hello" {
		t.Errorf("want message hello, got %v", inner["message"])
	}
}

func TestCallToolNotFound(t *testing.T) {
	ts := testHTTPServer(t)
	defer ts.Close()

	body := bytes.NewBufferString(`{}`)
	resp, err := http.Post(ts.URL+"/tools/nonexistent", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestCallToolValidationError(t *testing.T) {
	ts := testHTTPServer(t)
	defer ts.Close()

	// echo tool requires "message" (string), send wrong type
	body := bytes.NewBufferString(`{"message":123}`)
	resp, err := http.Post(ts.URL+"/tools/echo", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestCallToolHandlerError(t *testing.T) {
	srv := testServer(t)
	srv.Registry().MustRegister(Tool{
		Name:        "fail",
		Description: "always fails",
		Handler: func(ctx context.Context, params json.RawMessage) (any, error) {
			return nil, fmt.Errorf("intentional failure")
		},
	})
	h := NewHTTPServer(srv, ":0")
	ts := httptest.NewServer(h.Handler())
	defer ts.Close()

	body := bytes.NewBufferString(`{}`)
	resp, err := http.Post(ts.URL+"/tools/fail", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

func TestCallToolInvalidJSON(t *testing.T) {
	ts := testHTTPServer(t)
	defer ts.Close()

	body := bytes.NewBufferString(`{not valid json}`)
	resp, err := http.Post(ts.URL+"/tools/echo", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestCORSHeaders(t *testing.T) {
	ts := testHTTPServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("want CORS origin *, got %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got != "GET, POST, OPTIONS" {
		t.Errorf("want CORS methods 'GET, POST, OPTIONS', got %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Errorf("want CORS headers 'Content-Type', got %q", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	ts := testHTTPServer(t)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodOptions, ts.URL+"/tools", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("want CORS origin *, got %q", got)
	}
}

func TestCallToolEmptyBody(t *testing.T) {
	srv := testServer(t)
	srv.Registry().MustRegister(Tool{
		Name:        "noop",
		Description: "no params needed",
		Handler: func(ctx context.Context, params json.RawMessage) (any, error) {
			return "ok", nil
		},
	})
	h := NewHTTPServer(srv, ":0")
	ts := httptest.NewServer(h.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/tools/noop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestListenAndServe(t *testing.T) {
	srv := testServer(t)
	h := NewHTTPServer(srv, "127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.ListenAndServe(ctx)
	}()

	// Cancel immediately to test graceful shutdown path.
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("ListenAndServe returned error: %v", err)
	}
}

func TestContentTypeJSON(t *testing.T) {
	ts := testHTTPServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("want Content-Type application/json, got %q", ct)
	}
}
