// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

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
