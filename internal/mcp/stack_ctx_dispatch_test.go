// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultstack"
)

// stackProbeTool mirrors the production handler rebind pattern exactly: it reads
// the request stack from ctx and falls back to the value it closed over at
// registration when no stack is installed. Its result is the ROOT of whichever
// vault it resolved, so a test can prove which one a dispatch reached.
func stackProbeTool(closedOver *storage.Vault) Tool {
	return Tool{
		Name:   "vp_stack_probe",
		Schema: json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"}}}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			v := closedOver
			if st := vaultstack.StackFrom(ctx); st != nil {
				v = st.Vault
			}
			return map[string]string{"root": v.Root}, nil
		},
	}
}

// TestDispatchInstallsRequestStackOnCtx proves the in-process Dispatch seam
// installs the resolved stack on ctx, and that a handler reads it over its
// closed-over value. It FAILS if installStack is removed (the handler would
// return the closed-over root) or if the resolver is ignored.
func TestDispatchInstallsRequestStackOnCtx(t *testing.T) {
	closed := storage.NewVault("/closed-over-root")
	sentinel := &vaultstack.VaultStack{Root: "/sentinel-root", Vault: storage.NewVault("/sentinel-root")}

	t.Run("resolver installs the sentinel stack", func(t *testing.T) {
		reg := testRegistry(t)
		if err := reg.Register(stackProbeTool(closed)); err != nil {
			t.Fatal(err)
		}
		reg.SetStackResolver(func(context.Context, json.RawMessage) (*vaultstack.VaultStack, error) {
			return sentinel, nil
		})
		res, err := reg.Dispatch(context.Background(), "vp_stack_probe", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := res.(map[string]string)["root"]; got != "/sentinel-root" {
			t.Fatalf("handler served %q, want the ctx stack root /sentinel-root (handler ignored the ctx stack)", got)
		}
	})

	t.Run("no resolver falls back to the closed-over vault", func(t *testing.T) {
		reg := testRegistry(t)
		if err := reg.Register(stackProbeTool(closed)); err != nil {
			t.Fatal(err)
		}
		// SetStackResolver deliberately NOT called: installStack is a no-op, so
		// StackFrom(ctx) is nil and the handler uses its closed-over vault —
		// exactly how the existing test corpus and throwaway registries behave.
		res, err := reg.Dispatch(context.Background(), "vp_stack_probe", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := res.(map[string]string)["root"]; got != "/closed-over-root" {
			t.Fatalf("with no stack on ctx the handler served %q, want the closed-over /closed-over-root", got)
		}
	})
}

// TestHandleMessageInstallsRequestStackOnCtx is the D1 PRODUCTION-path proof:
// it drives Server.HandleMessage with a real tools/call (the JSON-RPC path every
// host uses, owned by makeHandler), not the in-process Dispatch seam. It FAILS
// if makeHandler does not install the stack (the response would carry the
// closed-over startup root instead of the sentinel).
func TestHandleMessageInstallsRequestStackOnCtx(t *testing.T) {
	srv := NewServer(storage.NewVault("/startup-root"))
	if err := srv.Registry().Register(stackProbeTool(storage.NewVault("/startup-root"))); err != nil {
		t.Fatal(err)
	}
	srv.Registry().SetStackResolver(func(context.Context, json.RawMessage) (*vaultstack.VaultStack, error) {
		return &vaultstack.VaultStack{Root: "/sentinel-root", Vault: storage.NewVault("/sentinel-root")}, nil
	})
	initServer(t, srv)

	msg := json.RawMessage(`{"jsonrpc":"2.0","id":11,"method":"tools/call",
		"params":{"name":"vp_stack_probe","arguments":{"project":"p"}}}`)
	resp, ok := srv.HandleMessage(context.Background(), msg).(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatal("expected a JSONRPCResponse")
	}
	raw, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(raw), "/sentinel-root") {
		t.Fatalf("tools/call must serve the ctx stack root; got %s", raw)
	}
	if strings.Contains(string(raw), "/startup-root") {
		t.Fatalf("tools/call served the closed-over startup root — makeHandler did not install the stack: %s", raw)
	}
}
