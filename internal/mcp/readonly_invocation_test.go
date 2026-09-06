// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// multiplexTool is the shape the param-aware gate exists for: one registered
// Mutating tool whose params decide whether THIS call writes. `mode:"read"` is
// the non-writing invocation; anything else writes.
func multiplexTool(name string, ran *bool) Tool {
	return Tool{
		Name:     name,
		Mutating: true,
		ReadOnlyWhen: func(params json.RawMessage) bool {
			var p struct {
				Mode string `json:"mode"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return false
			}
			return p.Mode == "read"
		},
		Handler: func(context.Context, json.RawMessage) (any, error) { *ran = true; return "ok", nil },
	}
}

// TestParamAwareGateBothDirectionsOnDispatch is the whole bar in one test, on
// the in-process path: against a vault AHEAD of this binary, the read-only
// invocation passes the gate and the writing invocation of the SAME tool is
// still refused.
//
// 🔴 THE SECOND HALF IS THE LOAD-BEARING ONE. A test that only proved the read
// passes would pass with the gate deleted entirely, which is the failure mode
// this whole change could ship as.
func TestParamAwareGateBothDirectionsOnDispatch(t *testing.T) {
	ctx := aheadVaultCtx(t)

	t.Run("read_only_invocation_passes", func(t *testing.T) {
		reg := testRegistry(t)
		ran := false
		if err := reg.Register(multiplexTool("mux", &ran)); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Dispatch(ctx, "mux", json.RawMessage(`{"mode":"read"}`)); err != nil {
			t.Fatalf("read-only invocation refused by the surface gate: %v", err)
		}
		if !ran {
			t.Fatal("read-only invocation should have reached the handler")
		}
	})

	t.Run("writing_invocation_still_refused", func(t *testing.T) {
		reg := testRegistry(t)
		ran := false
		if err := reg.Register(multiplexTool("mux", &ran)); err != nil {
			t.Fatal(err)
		}
		_, err := reg.Dispatch(ctx, "mux", json.RawMessage(`{"mode":"write"}`))
		if err == nil {
			t.Fatal("writing invocation must still be refused against an ahead vault")
		}
		var ie *surface.IncompatibleError
		if !errors.As(err, &ie) {
			t.Fatalf("err = %T (%v), want *surface.IncompatibleError", err, err)
		}
		if ran {
			t.Fatal("writing invocation reached the handler despite the gate")
		}
	})
}

// TestParamAwareGateBothDirectionsOnMakeHandler pins the SECOND dispatch path.
//
// gateIfMutating has two callers — Dispatch (in-process) and makeHandler (the
// JSON-RPC tools/call path every real MCP client uses). Making only one
// param-aware would half-apply the fix in the direction that matters least:
// every host reaches the gate through makeHandler, so a refusal that survives
// only there is a refusal that survives in production.
func TestParamAwareGateBothDirectionsOnMakeHandler(t *testing.T) {
	newAheadServer := func(t *testing.T) (*Server, *bool) {
		t.Helper()
		root := t.TempDir()
		if err := surface.WriteStamp(filepath.Join(root, "Projects", "p"), surface.MCPSurfaceVersion+1, "tester"); err != nil {
			t.Fatalf("stage ahead stamp: %v", err)
		}
		srv := NewServer(storage.NewVault(root))
		ran := new(bool)
		if err := srv.Registry().Register(multiplexTool("mux", ran)); err != nil {
			t.Fatal(err)
		}
		initServer(t, srv)
		return srv, ran
	}

	callMux := func(t *testing.T, srv *Server, args string) (text string, isErr bool) {
		t.Helper()
		msg := json.RawMessage(`{
			"jsonrpc": "2.0", "id": 7, "method": "tools/call",
			"params": {"name": "mux", "arguments": ` + args + `}
		}`)
		resp, ok := srv.HandleMessage(context.Background(), msg).(mcplib.JSONRPCResponse)
		if !ok {
			t.Fatalf("expected a JSONRPCResponse for tools/call")
		}
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var out struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal result: %v (%s)", err, raw)
		}
		if len(out.Content) == 0 {
			t.Fatalf("empty content in tools/call result: %s", raw)
		}
		return out.Content[0].Text, out.IsError
	}

	t.Run("read_only_invocation_passes", func(t *testing.T) {
		srv, ran := newAheadServer(t)
		text, isErr := callMux(t, srv, `{"mode":"read"}`)
		if isErr {
			t.Fatalf("read-only invocation refused over tools/call: %s", text)
		}
		if !*ran {
			t.Fatal("read-only invocation should have reached the handler")
		}
	})

	t.Run("writing_invocation_still_refused", func(t *testing.T) {
		srv, ran := newAheadServer(t)
		text, isErr := callMux(t, srv, `{"mode":"write"}`)
		if !isErr {
			t.Fatalf("writing invocation must still be refused over tools/call, got: %s", text)
		}
		if !strings.Contains(text, "MCP surface") {
			t.Errorf("refusal should carry the surface remediation, got: %s", text)
		}
		if *ran {
			t.Fatal("writing invocation reached the handler despite the gate")
		}
	})
}

// TestParamAwareGateFailsClosed pins the property the whole design rests on:
// every way the question can be unanswerable GATES.
//
// Each case here is a payload that a predicate written the other way round —
// "return true unless I can see a write" — would admit. They are cheap to write
// and impossible to notice missing, which is why they are pinned rather than
// asserted in a comment.
func TestParamAwareGateFailsClosed(t *testing.T) {
	ctx := aheadVaultCtx(t)

	cases := []struct {
		name   string
		tool   func(ran *bool) Tool
		params json.RawMessage
	}{
		{
			// The default for every tool that never opted in: no predicate at
			// all. Whole-tool gating must be exactly what it was.
			name:   "no_predicate_declared",
			tool:   func(ran *bool) Tool { tl := multiplexTool("t", ran); tl.ReadOnlyWhen = nil; return tl },
			params: json.RawMessage(`{"mode":"read"}`),
		},
		{
			// A JSON null decodes cleanly into ANY struct and leaves every field
			// zero — so a predicate reading "not a write" off zero values would
			// admit it. readOnlyInvocation refuses it before the predicate runs.
			name:   "json_null_params",
			tool:   func(ran *bool) Tool { return multiplexTool("t", ran) },
			params: json.RawMessage(`null`),
		},
		{
			name:   "empty_params",
			tool:   func(ran *bool) Tool { return multiplexTool("t", ran) },
			params: json.RawMessage(``),
		},
		{
			// Unparseable: the predicate's own fail-closed branch.
			name:   "unparseable_params",
			tool:   func(ran *bool) Tool { return multiplexTool("t", ran) },
			params: json.RawMessage(`{"mode":`),
		},
		{
			// A discriminator the predicate does not recognise is not a
			// read-only mode; it is an unknown one.
			name:   "unrecognised_discriminator",
			tool:   func(ran *bool) Tool { return multiplexTool("t", ran) },
			params: json.RawMessage(`{"mode":"reindex"}`),
		},
		{
			// The discriminator is absent entirely.
			name:   "discriminator_absent",
			tool:   func(ran *bool) Tool { return multiplexTool("t", ran) },
			params: json.RawMessage(`{}`),
		},
		{
			// A predicate that panics must not be swallowed into "read-only".
			name: "panicking_predicate",
			tool: func(ran *bool) Tool {
				tl := multiplexTool("t", ran)
				tl.ReadOnlyWhen = func(json.RawMessage) bool { panic("predicate is broken") }
				return tl
			},
			params: json.RawMessage(`{"mode":"read"}`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			tl := tc.tool(&ran)

			// The panicking case must not be admitted; whether it surfaces as a
			// refusal or a panic, the one unacceptable outcome is the handler
			// running.
			defer func() {
				_ = recover()
				if ran {
					t.Fatalf("%s: handler ran — the gate was bypassed by an unanswerable payload", tc.name)
				}
			}()

			// Bypass Register's `{}` substitution for the empty-params case by
			// calling the gate directly: the point is the helper's own contract.
			rt := &registeredTool{tool: tl}
			if _, err := (&Registry{}).gateIfMutating(ctx, rt, tc.params, nil); err == nil {
				t.Fatalf("%s: gate admitted an unanswerable payload", tc.name)
			}
		})
	}
}

// TestParamAwareGateCannotGateANonMutatingTool pins the refinement's direction.
// ReadOnlyWhen may only ever RELAX the gate for a tool that is already Mutating;
// a predicate returning false on a read-only tool must not start refusing reads.
func TestParamAwareGateCannotGateANonMutatingTool(t *testing.T) {
	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:         "ro",
		Mutating:     false,
		ReadOnlyWhen: func(json.RawMessage) bool { return false },
		Handler:      func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Dispatch(aheadVaultCtx(t), "ro", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("a non-mutating tool must pass regardless of its predicate: %v", err)
	}
	if !ran {
		t.Fatal("read-only handler should have run")
	}
}

// TestParamAwareGateReadPassesUnderStaleBinding pins the drift half of the same
// decision: a read-only invocation is a READ, and reads are deliberately left
// open under vault-binding drift (they carry the banner in their own result
// instead). The writing invocation of the same tool is still refused.
func TestParamAwareGateReadPassesUnderStaleBinding(t *testing.T) {
	drift := &storage.StaleBindingError{Bound: "/vault-a", Resolved: "/vault-b", Source: "cwd:/code/proj/.vibe-palace.toml"}

	// A compatible vault, so the only thing that could refuse is the drift.
	root := t.TempDir()
	ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(root))

	ran := false
	rt := &registeredTool{tool: multiplexTool("mux", &ran)}
	reg := &Registry{}

	if _, err := reg.gateIfMutating(ctx, rt, json.RawMessage(`{"mode":"read"}`), drift); err != nil {
		t.Fatalf("read-only invocation refused under drift: %v", err)
	}
	if _, err := reg.gateIfMutating(ctx, rt, json.RawMessage(`{"mode":"write"}`), drift); err == nil {
		t.Fatal("writing invocation must still be refused under drift")
	}
}

// TestParamAwareToolNamesReportsPredicateCarriers guards the accessor the
// declaration list is pinned against (tools.ParamAwareToolNames): it must report
// exactly the registered tools carrying a predicate, so a tool that grows one
// silently cannot pass that pin.
func TestParamAwareToolNamesReportsPredicateCarriers(t *testing.T) {
	reg := testRegistry(t)
	ran := false
	if err := reg.Register(multiplexTool("mux", &ran)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(Tool{
		Name:     "plain_mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(Tool{
		Name:    "plain_ro",
		Handler: func(context.Context, json.RawMessage) (any, error) { return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}

	got := reg.ParamAwareToolNames()
	if len(got) != 1 || got[0] != "mux" {
		t.Fatalf("ParamAwareToolNames() = %v, want [mux]", got)
	}
}
