// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// requestIDKey is a context-key type for optional MCP request IDs that
// callers may stash on the context for correlation in handler logs.
type requestIDKey struct{}

// WithRequestID returns a derived context carrying the given request ID.
// The MCP dispatch handler logs this ID if present so a single tool call
// can be correlated across its entry, exit, and any recovered panic.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// requestIDFromContext returns the request ID previously attached via
// WithRequestID, or "" if none is present.
func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

// HandlerFunc is the handler signature for vibe-palace tools.
// It receives the request context and raw JSON parameters, and returns
// an arbitrary result value or an error.
type HandlerFunc func(ctx context.Context, params json.RawMessage) (any, error)

// Tool describes an MCP tool with its handler and parameter schema.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema for parameters (type: object)
	Handler     HandlerFunc
}

// ToolInfo is a read-only summary returned by Registry.List.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

// registeredTool holds a tool definition alongside its compiled schema.
type registeredTool struct {
	tool     Tool
	compiled *jsonschema.Schema
}

// Registry manages tool registration and dispatch with JSON Schema validation.
// It wraps the underlying mcp-go MCPServer for protocol-level integration.
type Registry struct {
	srv   *server.MCPServer
	mu    sync.RWMutex
	tools map[string]*registeredTool
}

// NewRegistry creates a Registry backed by the given MCPServer.
func NewRegistry(srv *server.MCPServer) *Registry {
	return &Registry{
		srv:   srv,
		tools: make(map[string]*registeredTool),
	}
}

// Register adds a tool to the registry. It compiles the JSON Schema at
// registration time so validation is fast at dispatch time. Returns an error
// if the tool name is already registered or the schema is invalid.
func (r *Registry) Register(tool Tool) error {
	if tool.Name == "" {
		return fmt.Errorf("tool name must not be empty")
	}
	if tool.Handler == nil {
		return fmt.Errorf("tool %q: handler must not be nil", tool.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("tool %q is already registered", tool.Name)
	}

	// Compile the JSON Schema for parameter validation.
	compiled, err := compileSchema(tool.Name, tool.Schema)
	if err != nil {
		return fmt.Errorf("tool %q: invalid schema: %w", tool.Name, err)
	}

	// Build the mcp-go Tool from our Schema.
	mcpTool, err := buildMCPTool(tool)
	if err != nil {
		return fmt.Errorf("tool %q: %w", tool.Name, err)
	}

	rt := &registeredTool{tool: tool, compiled: compiled}
	r.tools[tool.Name] = rt

	// Register with mcp-go so it appears in tools/list and tools/call.
	r.srv.AddTool(mcpTool, r.makeHandler(rt))

	return nil
}

// MustRegister calls Register and panics on error.
func (r *Registry) MustRegister(tool Tool) {
	if err := r.Register(tool); err != nil {
		panic(err)
	}
}

// List returns a snapshot of all registered tools.
func (r *Registry) List() []ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ToolInfo, 0, len(r.tools))
	for _, rt := range r.tools {
		out = append(out, ToolInfo{
			Name:        rt.tool.Name,
			Description: rt.tool.Description,
			Schema:      rt.tool.Schema,
		})
	}
	return out
}

// Dispatch validates params against the tool's schema, then calls the handler.
// It returns a ToolNotFound error if the tool does not exist, or a
// ValidationError if the parameters fail schema validation.
func (r *Registry) Dispatch(ctx context.Context, name string, params json.RawMessage) (any, error) {
	r.mu.RLock()
	rt, ok := r.tools[name]
	r.mu.RUnlock()

	if !ok {
		return nil, &ToolNotFoundError{Name: name}
	}

	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}

	if err := validateParams(rt.compiled, params); err != nil {
		return nil, err
	}

	return rt.tool.Handler(ctx, params)
}

// makeHandler creates a mcp-go ToolHandlerFunc that validates parameters
// and bridges to our HandlerFunc signature. It logs entry, exit, and any
// recovered panic at the MCP dispatch boundary — this is the highest
// leverage log site in the project because every agent tool call flows
// through it. Entries log the tool name and optional request ID. Exits
// log elapsed time plus either an error or the response byte size.
func (r *Registry) makeHandler(rt *registeredTool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (result *mcplib.CallToolResult, err error) {
		start := time.Now()
		toolName := rt.tool.Name
		reqID := requestIDFromContext(ctx)

		slog.Debug("mcp.makeHandler: enter",
			"op", "mcp.makeHandler",
			"tool", toolName,
			"request_id", reqID,
		)

		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("mcp.makeHandler: panic recovered",
					"op", "mcp.makeHandler",
					"tool", toolName,
					"request_id", reqID,
					"elapsed_ms", time.Since(start).Milliseconds(),
					"panic", fmt.Sprintf("%v", rec),
				)
				result = mcplib.NewToolResultError(fmt.Sprintf("handler panic: %v", rec))
				err = nil
			}
		}()

		// Marshal arguments back to JSON for schema validation and handler.
		params, perr := json.Marshal(req.GetArguments())
		if perr != nil {
			slog.Warn("mcp.makeHandler: marshal args failed",
				"op", "mcp.makeHandler",
				"tool", toolName,
				"request_id", reqID,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"err", perr,
			)
			return mcplib.NewToolResultError(fmt.Sprintf("invalid arguments: %v", perr)), nil
		}

		// Validate against compiled schema.
		if vErr := validateParams(rt.compiled, params); vErr != nil {
			slog.Warn("mcp.makeHandler: validation failed",
				"op", "mcp.makeHandler",
				"tool", toolName,
				"request_id", reqID,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"err", vErr,
			)
			return mcplib.NewToolResultError(vErr.Error()), nil
		}

		// Call the application handler.
		raw, hErr := rt.tool.Handler(ctx, params)
		if hErr != nil {
			slog.Warn("mcp.makeHandler: handler error",
				"op", "mcp.makeHandler",
				"tool", toolName,
				"request_id", reqID,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"err", hErr,
			)
			return mcplib.NewToolResultError(hErr.Error()), nil
		}

		// Convert the result to a CallToolResult.
		out, mErr := marshalResult(raw)
		if mErr != nil {
			slog.Warn("mcp.makeHandler: marshal result failed",
				"op", "mcp.makeHandler",
				"tool", toolName,
				"request_id", reqID,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"err", mErr,
			)
			return out, mErr
		}

		resultSize := 0
		if out != nil {
			if b, jerr := json.Marshal(out); jerr == nil {
				resultSize = len(b)
			}
		}
		slog.Debug("mcp.makeHandler: exit",
			"op", "mcp.makeHandler",
			"tool", toolName,
			"request_id", reqID,
			"elapsed_ms", time.Since(start).Milliseconds(),
			"result_bytes", resultSize,
		)
		return out, nil
	}
}

// marshalResult converts an arbitrary handler return value into a CallToolResult.
func marshalResult(v any) (*mcplib.CallToolResult, error) {
	switch val := v.(type) {
	case nil:
		return mcplib.NewToolResultText(""), nil
	case string:
		return mcplib.NewToolResultText(val), nil
	case *mcplib.CallToolResult:
		return val, nil
	default:
		return mcplib.NewToolResultJSON(v)
	}
}

// compileSchema compiles a JSON Schema from raw bytes. If schema is nil or
// empty, a permissive object schema is used (no validation).
func compileSchema(name string, schema json.RawMessage) (*jsonschema.Schema, error) {
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}

	c := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(schema)))
	if err != nil {
		return nil, fmt.Errorf("unmarshal schema: %w", err)
	}

	url := fmt.Sprintf("vibe-palace://tools/%s/schema.json", name)
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("add resource: %w", err)
	}

	compiled, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	return compiled, nil
}

// validateParams validates JSON parameters against a compiled schema.
// Returns nil if validation passes. Returns a *ValidationError on failure.
func validateParams(compiled *jsonschema.Schema, params json.RawMessage) error {
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}

	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(params)))
	if err != nil {
		return &ValidationError{Detail: fmt.Sprintf("invalid JSON: %v", err)}
	}

	if err := compiled.Validate(doc); err != nil {
		return &ValidationError{Detail: err.Error()}
	}
	return nil
}

// buildMCPTool converts our Tool definition into an mcp-go Tool.
func buildMCPTool(t Tool) (mcplib.Tool, error) {
	tool := mcplib.Tool{
		Name:        t.Name,
		Description: t.Description,
	}

	// Parse the schema into the InputSchema structure.
	if len(t.Schema) > 0 {
		var schema mcplib.ToolInputSchema
		if err := json.Unmarshal(t.Schema, &schema); err != nil {
			return tool, fmt.Errorf("unmarshal input schema: %w", err)
		}
		// Ensure the type is "object" as required by MCP.
		if schema.Type == "" {
			schema.Type = "object"
		}
		tool.InputSchema = schema
	} else {
		tool.InputSchema = mcplib.ToolInputSchema{Type: "object"}
	}

	return tool, nil
}

// ToolNotFoundError is returned when dispatching to an unregistered tool.
type ToolNotFoundError struct {
	Name string
}

func (e *ToolNotFoundError) Error() string {
	return fmt.Sprintf("tool not found: %q", e.Name)
}

// ValidationError is returned when parameters fail schema validation.
type ValidationError struct {
	Detail string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("parameter validation failed: %s", e.Detail)
}
