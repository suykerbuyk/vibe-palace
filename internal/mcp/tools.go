// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/google/uuid"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// requestIDKey is a context-key type for optional MCP request IDs that
// callers may stash on the context for correlation in handler logs.
type requestIDKey struct{}

// WithRequestID returns a derived context carrying the given request ID.
// The MCP dispatch handler logs this ID so a single tool call can be correlated
// across its entry, exit, and any recovered panic.
//
// 🔴 UNTIL 209 NOTHING CALLED THIS, AND THAT WAS THE BUG — not the dead symbol,
// but what its deadness meant: requestIDFromContext runs on EVERY dispatch and
// logs its result as `request_id` at what this file's own comment calls "the
// highest leverage log site in the project". With no setter, that field was ""
// on every MCP dispatch ever logged. The correlation feature was 100%
// non-functional for its entire life, and the logs looked fine.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// newRequestID mints the per-dispatch correlation ID.
//
// It is MINTED, not echoed from the client's JSON-RPC id, and that is forced by
// the library rather than chosen: mcp-go never hands the request id to a tool
// handler (CallToolRequest embeds only Method+Params), the hooks API that does
// see it cannot inject into the handler's context, and stdio's contextFunc runs
// ONCE before the read loop — so an id minted there would be constant for the
// whole process, which is worse than empty because it would LOOK like a
// correlation id while correlating nothing. Echoing would mean forking the
// transport to own the envelope parse. Verified against mcp-go v0.47.0 at 205.
func newRequestID() string { return uuid.NewString() }

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
	// Mutating marks a tool that writes to the vault (or project-root state).
	// The dispatch choke-point (makeHandler/Dispatch) refuses mutating tools
	// when the vault's MCP surface version exceeds this binary's, so a stale
	// host cannot corrupt a newer vault. Read-only tools leave this false.
	Mutating bool
	// ReadOnlyWhen refines Mutating for ONE INVOCATION. When it is non-nil and
	// returns true for this call's schema-validated params, the surface gate
	// passes even though the tool is registered Mutating. It never works the
	// other way: it cannot gate a tool that is not already Mutating.
	//
	// It exists because the gate is per-TOOL while some tools multiplex a
	// writing action with a non-writing one — vp_vault_sync's action:"pull",
	// vp_vault_tidy's dry_run:true, vp_audit_vault's write:false. Refused whole,
	// vp_vault_sync took the RECOVERY PATH with it: restart pulls the vault over
	// this call, so a host whose binary was behind the vault could not pull the
	// fix that would repair it. The failure ran in the direction that prevents
	// its own repair. See doc/adr/010-surface-gate-at-the-dispatch-seam.md, the
	// Amendment's finding 2, which rules this the mechanism.
	//
	// 🔴 FAIL-CLOSED, AND THAT IS THE PROPERTY TO PRESERVE. A predicate declares
	// the NARROW case it can prove writes nothing, and everything else gates: a
	// nil predicate, an unparseable payload, a JSON null, a discriminator that
	// is absent or carries a value the predicate does not recognise. Never
	// invert one to say "not a write" — an unrecognised payload would then be
	// admitted, which is the one answer that must never be inferred from
	// missing information. readOnlyInvocation owns the first three; the
	// predicate owns the rest (internal/tools/readonly_invocation.go).
	//
	// 🔴 THIS DOES NOT REACH THE READ-ONLY SERVE FILTER, and must not be made
	// to. That filter strips tools at REGISTRATION (cmd/vp/cmd_mcp_serve.go),
	// where no params exist and no invocation has happened, so a sometimes-
	// read-only tool must stay stripped there — see the asymmetry note on
	// tools.ReadOnlyServeToolNames. The gate answers "may this call proceed?"
	// per request; the serve filter answers "may this tool be reachable at
	// all?" once. Only the first can be refined by params.
	ReadOnlyWhen func(params json.RawMessage) bool
}

// ToolInfo is a read-only summary returned by Registry.List.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Mutating    bool            `json:"mutating,omitempty"`
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

	// launchCwd is the directory this server's vault root was RESOLVED FROM,
	// set by WatchVaultBinding. Empty leaves the stale-binding gate unarmed —
	// a Registry whose vault was injected directly never had a launch
	// directory, so there is nothing to compare against.
	launchCwd string
	// lastBindErr fingerprints the last re-resolution failure so a session with
	// a permanently broken config logs the condition, not one line per tool
	// call: vplog aggregates warn counts by op, and a per-call flood would
	// swamp vp_health's summary with a single standing fault.
	lastBindErr string
}

// WatchVaultBinding arms the stale-binding gate with the directory this
// server's vault root was resolved from.
//
// Pass the SAME cwd that produced the bound vault, not a fresh os.Getwd(): the
// guard's whole claim is "the config this binding came from still says the same
// thing", and that claim is only meaningful against the directory the binding
// actually came from.
func (r *Registry) WatchVaultBinding(launchCwd string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.launchCwd = launchCwd
}

// staleBinding reports drift between the vault bound on ctx and what this
// server's launch directory resolves to now, or nil when there is none.
//
// A resolution FAILURE is not drift — see storage.CheckVaultBinding. It is
// reported and the call proceeds: a config that cannot be resolved governs
// nothing, so the bound vault is still the only one in effect.
func (r *Registry) staleBinding(ctx context.Context) *storage.StaleBindingError {
	r.mu.RLock()
	cwd := r.launchCwd
	r.mu.RUnlock()
	if cwd == "" {
		return nil
	}
	v := VaultFromContext(ctx)
	if v == nil {
		return nil
	}
	err := storage.CheckVaultBinding(v.Root, cwd)
	if err == nil {
		return nil
	}
	var sbe *storage.StaleBindingError
	if errors.As(err, &sbe) {
		return sbe
	}
	if r.markBindErr(err.Error()) {
		slog.Warn("mcp.staleBinding: vault re-resolution failed; keeping the startup binding",
			"op", "mcp.staleBinding",
			"bound", v.Root,
			"launch_cwd", cwd,
			"fault", "operational",
			"err", err,
		)
	}
	return nil
}

// markBindErr records msg as the current re-resolution failure and reports
// whether it is NEW. A standing fault logs once per distinct message rather
// than once per tool call; a change in the message (the operator edited the
// file again) logs afresh.
func (r *Registry) markBindErr(msg string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastBindErr == msg {
		return false
	}
	r.lastBindErr = msg
	return true
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
			Mutating:    rt.tool.Mutating,
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

	// params are schema-validated above, so the gate can read a discriminator
	// out of them without trusting the wire. Both dispatch paths pass them.
	if gErr := r.gateIfMutating(ctx, rt, params, r.staleBinding(ctx)); gErr != nil {
		return nil, gErr
	}

	return rt.tool.Handler(ctx, params)
}

// gateIfMutating refuses a mutating tool when the vault's MCP surface version
// exceeds this binary's, returning the IncompatibleError remediation. Read-only
// tools pass.
//
// It also refuses a mutating tool with NO vault in context (root == "", which
// surface.CheckCompatible reports as ErrNoVault) or one whose vault root is
// unreachable (*VaultUnreachableError). Both used to pass silently — the gate
// treated an absent vault as "nothing to check" — which meant a mutating tool
// could be admitted with no vault at all, and a write could proceed against a
// vault root that had been deleted out from under the server.
// VP_SURFACE_GATE=warn does not bypass either; see surface.EnforceFailStop.
//
// There is deliberately NO MCP startup gate — vp is MCP-primary, so the server
// stays up and the remediation surfaces in-band as a tool error payload. Both
// dispatch paths (makeHandler and Dispatch) route through here so neither can
// bypass the gate.
//
// drift, when non-nil, refuses the same set of tools for a second reason: the
// vault this server bound at startup is no longer the one its launch directory
// resolves to. Reads are deliberately left open — refusing them would take
// vp_bootstrap_context away exactly when the agent needs to find out what is
// wrong — and instead carry the disagreement in their own result; see
// staleBindingNotice.
//
// params are this invocation's schema-validated parameters. A Mutating tool
// that declares a ReadOnlyWhen predicate is refined by it here, BEFORE either
// refusal: an invocation that provably writes nothing is a read, and both
// reasons this gate refuses — a vault ahead of this binary, and a vault that is
// not the one this server bound — are reasons to refuse a WRITE. A read under
// drift is treated exactly like any other read: admitted, and carrying the
// drift banner in its own result.
func (r *Registry) gateIfMutating(ctx context.Context, rt *registeredTool, params json.RawMessage, drift *storage.StaleBindingError) error {
	if !rt.tool.Mutating {
		return nil
	}
	if readOnlyInvocation(rt.tool, params) {
		return nil
	}
	if drift != nil {
		return drift
	}
	root := ""
	if v := VaultFromContext(ctx); v != nil {
		root = v.Root
	}
	return surface.EnforceFailStop(root)
}

// staleBindingNotice prepends the drift banner to a tool result.
//
// It LEADS the content array, and both halves of that shape are load-bearing:
//
//   - A content item, not a field on the payload, because marshalResult emits
//     three shapes and a stamped field would reach only one of them.
//   - LEADING, because a host that truncates a large MCP result keeps the head,
//     so a trailing notice would be cut precisely on the big reads where acting
//     on the wrong vault costs most.
//
// Known limits: structuredContent is not stamped, so a client reading only that
// field does not see this; and native resources/read bypasses the registry, so
// a resource read is unstamped (vp_read_resource, being a tool, is covered).
func staleBindingNotice(out *mcplib.CallToolResult, drift *storage.StaleBindingError) *mcplib.CallToolResult {
	if out == nil || drift == nil {
		return out
	}
	banner := mcplib.NewTextContent("⚠ " + drift.Error() +
		"\n\nThis result was READ from the vault bound at startup. Mutating tools are refused until the server is reloaded.")
	out.Content = append([]mcplib.Content{banner}, out.Content...)
	return out
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

		// MINT THE CORRELATION ID HERE. This is the single chokepoint every tool
		// call crosses on BOTH transports, which is why the id belongs here and not
		// at the call sites: a call site that forgets is a dispatch that cannot be
		// followed, and for this feature's entire life EVERY call site forgot,
		// because there were none.
		//
		// An id already on the context wins, so an in-process caller (or a test) can
		// supply its own and see it honored end-to-end.
		reqID := requestIDFromContext(ctx)
		if reqID == "" {
			reqID = newRequestID()
			ctx = WithRequestID(ctx, reqID)
		}

		// The session id correlates a RUN; the request id correlates one DISPATCH.
		// Neither substitutes for the other — "which session did these 40 dispatches
		// belong to" is not a question a per-call UUID can answer. mcp-go already
		// flows the session on both transports and nothing in this repo was reading it.
		sessionID := ""
		if s := server.ClientSessionFromContext(ctx); s != nil {
			sessionID = s.SessionID()
		}

		slog.Debug("mcp.makeHandler: enter",
			"op", "mcp.makeHandler",
			"tool", toolName,
			"request_id", reqID,
			"session_id", sessionID,
		)

		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("mcp.makeHandler: panic recovered",
					"op", "mcp.makeHandler",
					"tool", toolName,
					"request_id", reqID,
					"session_id", sessionID,
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
				"session_id", sessionID,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"err", perr,
			)
			return mcplib.NewToolResultError(fmt.Sprintf("invalid arguments: %v", perr)), nil
		}

		// Validate against compiled schema.
		if vErr := validateParams(rt.compiled, params); vErr != nil {
			// Schema rejection is a CALLER error: the tool did its job. Stamped
			// fault="caller" so vplog.Summarize counts it as friction without
			// moving health status off healthy. This is the single most common
			// "caller passed a bad parameter" class and it fires BEFORE the
			// handler runs, so it needs its own stamp.
			slog.Warn("mcp.makeHandler: validation failed",
				"op", "mcp.makeHandler",
				"tool", toolName,
				"request_id", reqID,
				"session_id", sessionID,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"fault", "caller",
				"err", vErr,
			)
			return mcplib.NewToolResultError(vErr.Error()), nil
		}

		// Resolved ONCE per call and shared by the gate and the result banner, so
		// a mutating call does not pay for two upward config walks.
		drift := r.staleBinding(ctx)

		// Surface gate: refuse mutating tools when the vault is ahead of this
		// binary, returning the IncompatibleError remediation in-band. Also
		// refuses every mutating tool when drift is non-nil.
		//
		// params are the SAME schema-validated bytes the handler will receive,
		// so this path is param-aware on exactly the terms Dispatch is: the two
		// dispatch paths must agree about what a single invocation is allowed
		// to do, or a tool refused over the wire would be admitted in-process.
		if gErr := r.gateIfMutating(ctx, rt, params, drift); gErr != nil {
			// OPERATIONAL, not caller: a vault-ahead-of-this-binary mismatch is a
			// real condition an operator SHOULD see amber (operator decision), so
			// this stays out of the caller-friction bucket and keeps health amber.
			slog.Warn("mcp.makeHandler: surface gate refused",
				"op", "mcp.makeHandler",
				"tool", toolName,
				"request_id", reqID,
				"session_id", sessionID,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"fault", "operational",
				"err", gErr,
			)
			return mcplib.NewToolResultError(gErr.Error()), nil
		}

		// Call the application handler.
		raw, hErr := rt.tool.Handler(ctx, params)
		if hErr != nil {
			// Classify at the seam: a handler error that IS (or wraps) an
			// apperr.CallerError is the caller's fault — a guard that worked — and
			// gets fault="caller" (counted as friction, not health). Anything else
			// defaults to fault="internal" (amber), so an unclassified error stays
			// honestly amber rather than falsely green.
			fault := "internal"
			if apperr.IsCaller(hErr) {
				fault = "caller"
			}
			slog.Warn("mcp.makeHandler: handler error",
				"op", "mcp.makeHandler",
				"tool", toolName,
				"request_id", reqID,
				"session_id", sessionID,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"fault", fault,
				"err", hErr,
			)
			// A handler that failed under drift may have failed BECAUSE of it —
			// the notice rides the error result for the same reason it rides a
			// successful one.
			return staleBindingNotice(mcplib.NewToolResultError(hErr.Error()), drift), nil
		}

		// Convert the result to a CallToolResult.
		out, mErr := marshalResult(raw)
		if mErr != nil {
			slog.Warn("mcp.makeHandler: marshal result failed",
				"op", "mcp.makeHandler",
				"tool", toolName,
				"request_id", reqID,
				"session_id", sessionID,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"err", mErr,
			)
			return out, mErr
		}
		out = staleBindingNotice(out, drift)

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
			"session_id", sessionID,
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
		// MCP requires `structuredContent` to be a JSON object. A handler that
		// returns a top-level array (or any non-object) sets StructuredContent
		// to that value verbatim (NewToolResultJSON), which strict clients
		// reject with "expected record, received array". Guard the whole tool
		// surface here — the one choke-point every handler result passes
		// through — so a bare-collection return can never reach a client again:
		// wrap non-objects under an "items" key. Object-shaped returns (structs,
		// maps) pass through untouched, so tools that already return a named
		// object keep their field names.
		if !marshalsToJSONObject(v) {
			v = map[string]any{"items": v}
		}
		return mcplib.NewToolResultJSON(v)
	}
}

// marshalsToJSONObject reports whether v serializes to a JSON object ("{...}"),
// the shape the MCP spec requires for structuredContent. It marshals v and
// inspects the first non-space byte, so it is faithful to what the client
// actually receives — a type's custom MarshalJSON is honored — rather than
// guessing from Go kinds. A marshal error returns false so the caller wraps;
// NewToolResultJSON then surfaces the same error rather than swallowing it.
func marshalsToJSONObject(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	b = bytes.TrimSpace(b)
	return len(b) > 0 && b[0] == '{'
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
