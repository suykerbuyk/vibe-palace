// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// driftedServer stages the #10 incident: a server bound to vault A at startup,
// and a launch directory whose .vibe-palace.toml has since been repointed at
// vault B. It returns the launch dir, the two vault roots, and a context
// carrying the STARTUP binding — exactly what contextFunc injects on every
// transport.
func driftedServer(t *testing.T) (launchCwd, vaultA, vaultB string, ctx context.Context) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	vaultA = filepath.Join(home, "vault-a")
	vaultB = filepath.Join(home, "vault-b")
	launchCwd = filepath.Join(home, "code", "proj")
	for _, d := range []string{vaultA, vaultB, launchCwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := filepath.Join(launchCwd, ".vibe-palace.toml")
	if err := os.WriteFile(cfg, []byte("vault_path = \""+vaultA+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The server booted here, so vault A is the binding every handler holds.
	ctx = context.WithValue(context.Background(), vaultKey, storage.NewVault(vaultA))

	// The operator repoints the config mid-session. Nothing re-binds.
	if err := os.WriteFile(cfg, []byte("vault_path = \""+vaultB+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return launchCwd, vaultA, vaultB, ctx
}

// 🔴 THE INCIDENT TEST. Not "does the guard return an error" — does the server
// still write this project's history into the vault the operator moved away
// from. Deleting the drift branch in gateIfMutating must make this go RED with
// vault A's contents named.
func TestStaleBindingRefusesWriteIntoTheStaleVault(t *testing.T) {
	launchCwd, vaultA, _, ctx := driftedServer(t)

	reg := testRegistry(t)
	// A handler that writes where the server is BOUND, which is what every real
	// mutating tool does: they captured the vault at RegisterAll time.
	if err := reg.Register(Tool{
		Name:     "write_history",
		Mutating: true,
		Handler: func(context.Context, json.RawMessage) (any, error) {
			p := filepath.Join(vaultA, "iteration-note.md")
			if err := os.WriteFile(p, []byte("work"), 0o644); err != nil {
				return nil, err
			}
			return "written", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	reg.WatchVaultBinding(launchCwd)

	_, err := reg.Dispatch(ctx, "write_history", json.RawMessage(`{}`))

	if entries, rerr := os.ReadDir(vaultA); rerr == nil && len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the server wrote into the STALE vault: %s contains %v "+
			"(the config had already been repointed; err was %v)", vaultA, names, err)
	}

	if err == nil {
		t.Fatal("mutating tool admitted under a stale binding, want refusal")
	}
	var sbe *storage.StaleBindingError
	if !errors.As(err, &sbe) {
		t.Fatalf("want *storage.StaleBindingError, got %T: %v", err, err)
	}
}

// Reads stay open: refusing them would take vp_bootstrap_context away exactly
// when the agent needs it to discover what is wrong.
func TestStaleBindingLeavesReadsOpen(t *testing.T) {
	launchCwd, _, _, ctx := driftedServer(t)

	reg := testRegistry(t)
	if err := reg.Register(Tool{
		Name:    "read_state",
		Handler: func(context.Context, json.RawMessage) (any, error) { return "state", nil },
	}); err != nil {
		t.Fatal(err)
	}
	reg.WatchVaultBinding(launchCwd)

	if _, err := reg.Dispatch(ctx, "read_state", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("read refused under a stale binding: %v", err)
	}
}

// 🔴 CLAUSE 3 ON THE PRODUCTION DISPATCH PATH.
//
// The unit test below exercises staleBindingNotice directly, and the read test
// above goes through Dispatch, which by design never stamps — so BOTH stay
// green with `out = staleBindingNotice(out, drift)` deleted from makeHandler,
// while vp_bootstrap_context over stdio and HTTP goes silent. That is exactly
// the mask this project keeps re-earning (272/290/293): a test that passes
// without exercising the defect proves nothing.
//
// This drives the real transport seam. HandleMessage applies contextFunc and
// hands off to mcp-go, which invokes the handler makeHandler registered — the
// same path stdio and streamable-HTTP take.
func TestStaleBindingNoticeReachesTheAgentOverTheTransport(t *testing.T) {
	launchCwd, vaultA, vaultB, _ := driftedServer(t)

	srv := NewServer(storage.NewVault(vaultA))
	srv.WatchVaultBinding(launchCwd)
	if err := srv.Registry().Register(Tool{
		Name:    "read_json",
		Handler: func(context.Context, json.RawMessage) (any, error) { return map[string]any{"resume": "body"}, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Registry().Register(Tool{
		Name:    "read_fails",
		Handler: func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("boom") },
	}); err != nil {
		t.Fatal(err)
	}

	srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0"}}
	}`))
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	for _, tc := range []struct{ name, tool string }{
		{"successful JSON read", "read_json"},
		{"handler error", "read_fails"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := srv.HandleMessage(context.Background(), json.RawMessage(
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+tc.tool+`","arguments":{}}}`))
			rpcResp, ok := resp.(mcplib.JSONRPCResponse)
			if !ok {
				t.Fatalf("want JSONRPCResponse, got %T: %+v", resp, resp)
			}
			raw, err := json.Marshal(rpcResp.Result)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			var got struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			if len(got.Content) == 0 {
				t.Fatalf("no content in result: %s", raw)
			}
			lead := got.Content[0].Text
			if !strings.HasPrefix(lead, "⚠ vault binding is stale") {
				t.Fatalf("the drift notice did not reach the agent: leading content is %q\nfull result: %s", lead, raw)
			}
			for _, want := range []string{vaultA, vaultB, "restart the MCP server"} {
				if !strings.Contains(lead, want) {
					t.Errorf("notice missing %q:\n%s", want, lead)
				}
			}
		})
	}
}

// A healthy binding must not stamp anything on the transport either.
func TestStaleBindingNoNoticeOverTransportWhenHealthy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	vault := filepath.Join(home, "vault")
	launchCwd := filepath.Join(home, "code", "proj")
	for _, d := range []string{vault, launchCwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(launchCwd, ".vibe-palace.toml"),
		[]byte("vault_path = \""+vault+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(storage.NewVault(vault))
	srv.WatchVaultBinding(launchCwd)
	if err := srv.Registry().Register(Tool{
		Name:    "read_json",
		Handler: func(context.Context, json.RawMessage) (any, error) { return map[string]any{"resume": "body"}, nil },
	}); err != nil {
		t.Fatal(err)
	}
	srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0"}}
	}`))
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	resp := srv.HandleMessage(context.Background(), json.RawMessage(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_json","arguments":{}}}`))
	raw, err := json.Marshal(resp.(mcplib.JSONRPCResponse).Result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "vault binding is stale") {
		t.Fatalf("healthy binding stamped a drift notice: %s", raw)
	}
}

// Clause 3 on the READ path: a read that succeeds from the startup vault must
// say so in its own result, and it must LEAD — a host that truncates a large
// MCP result keeps the head, and the reads that matter here are the big ones.
func TestStaleBindingNoticeLeadsTheResult(t *testing.T) {
	launchCwd, vaultA, vaultB, _ := driftedServer(t)

	drift := &storage.StaleBindingError{Bound: vaultA, Resolved: vaultB, Source: "cwd:" + launchCwd}
	out := staleBindingNotice(mcplib.NewToolResultText("payload"), drift)

	if len(out.Content) != 2 {
		t.Fatalf("want 2 content items (notice + payload), got %d", len(out.Content))
	}
	first, ok := out.Content[0].(mcplib.TextContent)
	if !ok {
		t.Fatalf("leading content is %T, want TextContent", out.Content[0])
	}
	for _, want := range []string{vaultA, vaultB, "restart the MCP server"} {
		if !strings.Contains(first.Text, want) {
			t.Errorf("notice missing %q:\n%s", want, first.Text)
		}
	}
	last, ok := out.Content[1].(mcplib.TextContent)
	if !ok || last.Text != "payload" {
		t.Fatalf("original payload not preserved after the notice: %#v", out.Content[1])
	}
}

// A healthy binding must be completely inert — no notice, no refusal. A guard
// that fires when nothing is wrong teaches the reader to skim it.
func TestStaleBindingSilentWhenConfigUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	vault := filepath.Join(home, "vault")
	launchCwd := filepath.Join(home, "code", "proj")
	for _, d := range []string{vault, launchCwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(launchCwd, ".vibe-palace.toml"),
		[]byte("vault_path = \""+vault+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(vault))

	reg := testRegistry(t)
	reg.WatchVaultBinding(launchCwd)
	if drift := reg.staleBinding(ctx); drift != nil {
		t.Fatalf("unchanged config reported drift: %v", drift)
	}

	out := staleBindingNotice(mcplib.NewToolResultText("payload"), nil)
	if len(out.Content) != 1 {
		t.Fatalf("healthy result was stamped anyway: %d content items", len(out.Content))
	}
}

// An unarmed registry — every NewServer(vault) in the tree — must behave
// exactly as before this guard existed.
func TestStaleBindingUnarmedRegistryIsInert(t *testing.T) {
	_, _, _, ctx := driftedServer(t)

	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT armed.
	if _, err := reg.Dispatch(ctx, "mut", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unarmed registry refused a mutating tool: %v", err)
	}
	if !ran {
		t.Fatal("unarmed registry blocked the handler")
	}
}

// A rejected or absent config is not drift — it governs nothing, so the bound
// vault is still the only one in effect and the session keeps working.
func TestStaleBindingSwallowedConfigDoesNotRefuse(t *testing.T) {
	launchCwd, vaultA, _, ctx := driftedServer(t)

	// Repoint again, this time swallowed by a table — a different condition,
	// handled by a different guard.
	if err := os.WriteFile(filepath.Join(launchCwd, ".vibe-palace.toml"),
		[]byte("[project]\nname = \"throwaway\"\nvault_path = \"/tmp/elsewhere\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	reg.WatchVaultBinding(launchCwd)

	_, err := reg.Dispatch(ctx, "mut", json.RawMessage(`{}`))
	var sbe *storage.StaleBindingError
	if errors.As(err, &sbe) {
		t.Fatalf("a swallowed config was reported as drift, stranding a session "+
			"bound to a still-valid %s: %v", vaultA, err)
	}
	if !ran {
		t.Fatalf("handler blocked by a config that governs nothing: %v", err)
	}
}

// A standing re-resolution failure must log the CONDITION, not one line per
// tool call: vplog aggregates warn counts by op, so a per-call flood would
// swamp vp_health's summary with a single unchanging fault.
func TestStaleBindingWarnsOncePerDistinctFailure(t *testing.T) {
	reg := testRegistry(t)

	if !reg.markBindErr("parse foo: swallowed") {
		t.Fatal("first occurrence of a failure did not report as new")
	}
	if reg.markBindErr("parse foo: swallowed") {
		t.Error("an unchanged failure reported as new; vp.log would flood")
	}
	if !reg.markBindErr("parse foo: unreadable") {
		t.Error("a CHANGED failure did not report as new; the operator edited the file and nothing said so")
	}
}
