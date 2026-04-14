// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// captureLogs installs a debug-level slog handler that writes to a buffer
// for the duration of the test, restoring the previous default on cleanup.
// Entry logs live at Debug level, so the buffer handler must be permissive.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	srv := server.NewMCPServer("test", "0.1.0", server.WithToolCapabilities(true))
	return NewRegistry(srv)
}

func echoHandler(_ context.Context, params json.RawMessage) (any, error) {
	var m map[string]any
	if err := json.Unmarshal(params, &m); err != nil {
		return nil, err
	}
	return m, nil
}

var echoSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"message": {"type": "string"}
	},
	"required": ["message"]
}`)

func TestRegisterAndList(t *testing.T) {
	reg := testRegistry(t)

	err := reg.Register(Tool{
		Name:        "echo",
		Description: "Echoes the input",
		Schema:      echoSchema,
		Handler:     echoHandler,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	tools := reg.List()
	if len(tools) != 1 {
		t.Fatalf("List: got %d tools, want 1", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("List[0].Name = %q, want %q", tools[0].Name, "echo")
	}
	if tools[0].Description != "Echoes the input" {
		t.Errorf("List[0].Description = %q, want %q", tools[0].Description, "Echoes the input")
	}
}

func TestRegisterDuplicate(t *testing.T) {
	reg := testRegistry(t)

	tool := Tool{Name: "dup", Description: "test", Handler: echoHandler}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := reg.Register(tool); err == nil {
		t.Fatal("second Register: expected error for duplicate, got nil")
	}
}

func TestRegisterEmptyName(t *testing.T) {
	reg := testRegistry(t)

	err := reg.Register(Tool{Name: "", Handler: echoHandler})
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

func TestRegisterNilHandler(t *testing.T) {
	reg := testRegistry(t)

	err := reg.Register(Tool{Name: "bad", Handler: nil})
	if err == nil {
		t.Fatal("expected error for nil handler, got nil")
	}
}

func TestRegisterInvalidSchema(t *testing.T) {
	reg := testRegistry(t)

	err := reg.Register(Tool{
		Name:    "bad-schema",
		Schema:  json.RawMessage(`{not valid json`),
		Handler: echoHandler,
	})
	if err == nil {
		t.Fatal("expected error for invalid schema, got nil")
	}
}

func TestMustRegisterPanics(t *testing.T) {
	reg := testRegistry(t)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustRegister did not panic on invalid tool")
		}
	}()

	reg.MustRegister(Tool{Name: "", Handler: echoHandler})
}

func TestDispatchValidParams(t *testing.T) {
	reg := testRegistry(t)

	reg.MustRegister(Tool{
		Name:    "echo",
		Schema:  echoSchema,
		Handler: echoHandler,
	})

	result, err := reg.Dispatch(context.Background(), "echo", json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", result)
	}
	if m["message"] != "hello" {
		t.Errorf("result[message] = %v, want %q", m["message"], "hello")
	}
}

func TestDispatchMissingRequiredParam(t *testing.T) {
	reg := testRegistry(t)

	reg.MustRegister(Tool{
		Name:    "echo",
		Schema:  echoSchema,
		Handler: echoHandler,
	})

	_, err := reg.Dispatch(context.Background(), "echo", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected validation error for missing required param, got nil")
	}

	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
}

func TestDispatchWrongParamType(t *testing.T) {
	reg := testRegistry(t)

	reg.MustRegister(Tool{
		Name:    "echo",
		Schema:  echoSchema,
		Handler: echoHandler,
	})

	_, err := reg.Dispatch(context.Background(), "echo", json.RawMessage(`{"message":42}`))
	if err == nil {
		t.Fatal("expected validation error for wrong type, got nil")
	}

	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
}

func TestDispatchToolNotFound(t *testing.T) {
	reg := testRegistry(t)

	_, err := reg.Dispatch(context.Background(), "nonexistent", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}

	var tnf *ToolNotFoundError
	if !errors.As(err, &tnf) {
		t.Fatalf("error type = %T, want *ToolNotFoundError", err)
	}
	if tnf.Name != "nonexistent" {
		t.Errorf("ToolNotFoundError.Name = %q, want %q", tnf.Name, "nonexistent")
	}
}

func TestDispatchHandlerError(t *testing.T) {
	reg := testRegistry(t)

	reg.MustRegister(Tool{
		Name: "fail",
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return nil, errors.New("handler exploded")
		},
	})

	_, err := reg.Dispatch(context.Background(), "fail", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error from handler, got nil")
	}
	if err.Error() != "handler exploded" {
		t.Errorf("error = %q, want %q", err.Error(), "handler exploded")
	}
}

func TestDispatchHandlerReturnsString(t *testing.T) {
	reg := testRegistry(t)

	reg.MustRegister(Tool{
		Name: "greet",
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "hello world", nil
		},
	})

	result, err := reg.Dispatch(context.Background(), "greet", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result != "hello world" {
		t.Errorf("result = %v, want %q", result, "hello world")
	}
}

func TestDispatchNilParams(t *testing.T) {
	reg := testRegistry(t)

	reg.MustRegister(Tool{
		Name: "noop",
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			return string(params), nil
		},
	})

	result, err := reg.Dispatch(context.Background(), "noop", nil)
	if err != nil {
		t.Fatalf("Dispatch with nil params: %v", err)
	}
	// nil params should be treated as empty object
	if result != "{}" {
		t.Errorf("result = %v, want %q", result, "{}")
	}
}

func TestDispatchNoSchema(t *testing.T) {
	reg := testRegistry(t)

	reg.MustRegister(Tool{
		Name: "any-params",
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			return string(params), nil
		},
	})

	// With no schema, any object params should pass.
	result, err := reg.Dispatch(context.Background(), "any-params", json.RawMessage(`{"anything":"goes"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result != `{"anything":"goes"}` {
		t.Errorf("result = %v, want %q", result, `{"anything":"goes"}`)
	}
}

// TestMCPHandlerIntegration verifies that tools registered through the Registry
// are properly dispatched via the mcp-go protocol layer (tools/call).
func TestMCPHandlerIntegration(t *testing.T) {
	srv := testServer(t)

	srv.Registry().MustRegister(Tool{
		Name:        "greet",
		Description: "Greets a person",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string"}
			},
			"required": ["name"]
		}`),
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			return "Hello, " + p.Name + "!", nil
		},
	})

	// Initialize first (required by protocol).
	initMsg := json.RawMessage(`{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": {
			"protocolVersion": "2025-03-26",
			"capabilities": {},
			"clientInfo": {"name": "test", "version": "0.1.0"}
		}
	}`)
	srv.HandleMessage(context.Background(), initMsg)

	// Send initialized notification.
	srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "method": "notifications/initialized"
	}`))

	// Call the tool.
	callMsg := json.RawMessage(`{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": {"name": "greet", "arguments": {"name": "World"}}
	}`)

	resp := srv.HandleMessage(context.Background(), callMsg)
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	raw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(result.Content) == 0 {
		t.Fatal("expected content in result, got empty")
	}
	if result.Content[0].Text != "Hello, World!" {
		t.Errorf("content text = %q, want %q", result.Content[0].Text, "Hello, World!")
	}
}

// TestMCPHandlerValidationError verifies that schema validation errors are
// returned as tool errors through the protocol layer.
func TestMCPHandlerValidationError(t *testing.T) {
	srv := testServer(t)

	srv.Registry().MustRegister(Tool{
		Name:   "strict",
		Schema: echoSchema,
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "should not reach", nil
		},
	})

	// Initialize.
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

	// Call with missing required param.
	callMsg := json.RawMessage(`{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": {"name": "strict", "arguments": {}}
	}`)

	resp := srv.HandleMessage(context.Background(), callMsg)
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	raw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if !result.IsError {
		t.Error("expected isError=true for validation failure")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected error content, got empty")
	}
}

// TestMCPHandlerHandlerError verifies that handler errors are returned as tool
// errors through the protocol layer.
func TestMCPHandlerHandlerError(t *testing.T) {
	srv := testServer(t)

	srv.Registry().MustRegister(Tool{
		Name: "boom",
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return nil, errors.New("kaboom")
		},
	})

	// Initialize.
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

	callMsg := json.RawMessage(`{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": {"name": "boom", "arguments": {}}
	}`)

	resp := srv.HandleMessage(context.Background(), callMsg)
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	raw, _ := json.Marshal(rpcResp.Result)

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	json.Unmarshal(raw, &result)

	if !result.IsError {
		t.Error("expected isError=true for handler error")
	}
	if len(result.Content) > 0 && result.Content[0].Text != "kaboom" {
		t.Errorf("error text = %q, want %q", result.Content[0].Text, "kaboom")
	}
}

// TestMCPToolsList verifies that registered tools appear in tools/list.
func TestMCPToolsList(t *testing.T) {
	srv := testServer(t)

	srv.Registry().MustRegister(Tool{
		Name:        "alpha",
		Description: "First tool",
		Schema:      echoSchema,
		Handler:     echoHandler,
	})
	srv.Registry().MustRegister(Tool{
		Name:        "beta",
		Description: "Second tool",
		Handler:     echoHandler,
	})

	// Initialize.
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

	// List tools.
	listMsg := json.RawMessage(`{
		"jsonrpc": "2.0", "id": 3, "method": "tools/list", "params": {}
	}`)

	resp := srv.HandleMessage(context.Background(), listMsg)
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	raw, _ := json.Marshal(rpcResp.Result)

	var result struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(result.Tools) != 2 {
		t.Fatalf("tools/list: got %d tools, want 2", len(result.Tools))
	}

	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	if !names["alpha"] {
		t.Error("tools/list: missing 'alpha'")
	}
	if !names["beta"] {
		t.Error("tools/list: missing 'beta'")
	}
}

func TestMarshalResult(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		r, err := marshalResult(nil)
		if err != nil {
			t.Fatalf("marshalResult(nil): %v", err)
		}
		if r == nil {
			t.Fatal("expected non-nil result")
		}
	})

	t.Run("string", func(t *testing.T) {
		r, err := marshalResult("hello")
		if err != nil {
			t.Fatalf("marshalResult(string): %v", err)
		}
		raw, _ := json.Marshal(r)
		if !json.Valid(raw) {
			t.Fatal("invalid JSON from marshalResult")
		}
	})

	t.Run("struct", func(t *testing.T) {
		type result struct {
			Count int `json:"count"`
		}
		r, err := marshalResult(result{Count: 42})
		if err != nil {
			t.Fatalf("marshalResult(struct): %v", err)
		}
		if r == nil {
			t.Fatal("expected non-nil result")
		}
	})

	t.Run("passthrough CallToolResult", func(t *testing.T) {
		orig := mcplib.NewToolResultText("pass")
		r, err := marshalResult(orig)
		if err != nil {
			t.Fatalf("marshalResult(CallToolResult): %v", err)
		}
		if r != orig {
			t.Error("expected passthrough of CallToolResult pointer")
		}
	})
}

// callRegisteredHandler drives the mcp-go protocol layer to actually invoke
// makeHandler for the given tool name — the Dispatch path bypasses it.
func callRegisteredHandler(t *testing.T, srv *Server, toolName, argsJSON string) {
	t.Helper()
	ctx := context.Background()
	srv.HandleMessage(ctx, json.RawMessage(`{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": {
			"protocolVersion": "2025-03-26",
			"capabilities": {},
			"clientInfo": {"name": "test", "version": "0.1.0"}
		}
	}`))
	srv.HandleMessage(ctx, json.RawMessage(`{
		"jsonrpc": "2.0", "method": "notifications/initialized"
	}`))
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + toolName + `","arguments":` + argsJSON + `}}`
	srv.HandleMessage(ctx, json.RawMessage(call))
}

// TestMakeHandlerLogsEntryAndExit verifies that a successful tool call logs
// both an "enter" and an "exit" record tagged with the tool name via
// slog.SetDefault. Uses a buffered JSON handler captured for the test.
func TestMakeHandlerLogsEntryAndExit(t *testing.T) {
	buf := captureLogs(t)

	srv := testServer(t)
	srv.Registry().MustRegister(Tool{
		Name:    "echo-log",
		Schema:  echoSchema,
		Handler: echoHandler,
	})

	callRegisteredHandler(t, srv, "echo-log", `{"message":"hi"}`)

	out := buf.String()
	if !strings.Contains(out, `"msg":"mcp.makeHandler: enter"`) {
		t.Errorf("missing entry log record; full output:\n%s", out)
	}
	if !strings.Contains(out, `"msg":"mcp.makeHandler: exit"`) {
		t.Errorf("missing exit log record; full output:\n%s", out)
	}
	if !strings.Contains(out, `"tool":"echo-log"`) {
		t.Errorf("log records missing tool attr; full output:\n%s", out)
	}
	if !strings.Contains(out, `"op":"mcp.makeHandler"`) {
		t.Errorf("log records missing op attr; full output:\n%s", out)
	}
	if !strings.Contains(out, `"elapsed_ms"`) {
		t.Errorf("exit record missing elapsed_ms; full output:\n%s", out)
	}
}

// TestMakeHandlerLogsHandlerError asserts that a handler-returned error is
// logged at Warn level through the dispatch boundary.
func TestMakeHandlerLogsHandlerError(t *testing.T) {
	buf := captureLogs(t)

	srv := testServer(t)
	srv.Registry().MustRegister(Tool{
		Name: "boom-log",
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return nil, errors.New("kapow")
		},
	})

	callRegisteredHandler(t, srv, "boom-log", `{}`)

	out := buf.String()
	if !strings.Contains(out, `"msg":"mcp.makeHandler: handler error"`) {
		t.Errorf("missing handler-error log; full output:\n%s", out)
	}
	if !strings.Contains(out, `"tool":"boom-log"`) {
		t.Errorf("handler-error log missing tool attr; full output:\n%s", out)
	}
	if !strings.Contains(out, "kapow") {
		t.Errorf("handler-error log missing err detail; full output:\n%s", out)
	}
}

// TestMakeHandlerLogsValidationFailure verifies that a schema-validation
// failure emits a Warn-level log record with the tool name.
func TestMakeHandlerLogsValidationFailure(t *testing.T) {
	buf := captureLogs(t)

	srv := testServer(t)
	srv.Registry().MustRegister(Tool{
		Name:    "strict-log",
		Schema:  echoSchema,
		Handler: echoHandler,
	})

	callRegisteredHandler(t, srv, "strict-log", `{}`)

	out := buf.String()
	if !strings.Contains(out, `"msg":"mcp.makeHandler: validation failed"`) {
		t.Errorf("missing validation-failure log; full output:\n%s", out)
	}
	if !strings.Contains(out, `"tool":"strict-log"`) {
		t.Errorf("validation-failure log missing tool attr; full output:\n%s", out)
	}
}

// TestMakeHandlerRecoversPanic asserts that a panicking handler is caught,
// logged at Error level, and surfaced as a tool error instead of killing
// the process. Requires server.WithRecovery to not pre-empt the defer.
func TestMakeHandlerRecoversPanic(t *testing.T) {
	buf := captureLogs(t)

	// A bare Registry (no server.WithRecovery interposition) so our
	// deferred recover is the one that fires.
	srv := server.NewMCPServer("panic-test", "0.1.0", server.WithToolCapabilities(true))
	reg := NewRegistry(srv)
	reg.MustRegister(Tool{
		Name: "panic-log",
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			panic("intentional")
		},
	})

	// Directly drive Dispatch is not enough — makeHandler is only invoked
	// by the mcp-go server path. Bypass by calling the registered handler
	// through the registry's tools map.
	reg.mu.RLock()
	rt := reg.tools["panic-log"]
	reg.mu.RUnlock()
	handler := reg.makeHandler(rt)

	// Call the handler directly; recover() should catch the panic.
	// Supply an empty-object Arguments so schema validation passes and we
	// actually reach the panicking application handler.
	req := mcplib.CallToolRequest{}
	req.Params.Name = "panic-log"
	req.Params.Arguments = map[string]any{}
	result, err := handler(
		WithRequestID(context.Background(), "req-42"),
		req,
	)
	if err != nil {
		t.Fatalf("expected recovered panic to yield no error, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil tool result from recovered panic")
	}

	out := buf.String()
	if !strings.Contains(out, `"msg":"mcp.makeHandler: panic recovered"`) {
		t.Errorf("missing panic-recover log; full output:\n%s", out)
	}
	if !strings.Contains(out, `"request_id":"req-42"`) {
		t.Errorf("panic log missing request_id correlation; full output:\n%s", out)
	}
	if !strings.Contains(out, "intentional") {
		t.Errorf("panic log missing panic message; full output:\n%s", out)
	}
}

func TestWithRequestID(t *testing.T) {
	ctx := WithRequestID(context.Background(), "abc-123")
	if got := requestIDFromContext(ctx); got != "abc-123" {
		t.Errorf("requestIDFromContext = %q, want %q", got, "abc-123")
	}
	if got := requestIDFromContext(context.Background()); got != "" {
		t.Errorf("missing ID should return empty, got %q", got)
	}
	if got := requestIDFromContext(WithRequestID(context.Background(), "")); got != "" {
		t.Errorf("empty ID should be ignored, got %q", got)
	}
	//nolint:staticcheck // Explicitly exercise nil-ctx branch.
	if got := requestIDFromContext(nil); got != "" {
		t.Errorf("nil ctx should yield empty, got %q", got)
	}
}
