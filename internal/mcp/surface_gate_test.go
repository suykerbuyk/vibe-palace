// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// aheadVaultCtx builds a vault whose Projects/<p>/.surface stamp is one version
// ahead of this binary, plus a context carrying that vault (as the MCP server
// would), so the surface gate sees an incompatible vault.
func aheadVaultCtx(t *testing.T) context.Context {
	t.Helper()
	root := t.TempDir()
	stampDir := filepath.Join(root, "Projects", "p")
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion+1, "tester"); err != nil {
		t.Fatalf("stage ahead stamp: %v", err)
	}
	return context.WithValue(context.Background(), vaultKey, storage.NewVault(root))
}

func TestSurfaceGateRefusesMutatingTool(t *testing.T) {
	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(Tool{
		Name:    "ro",
		Handler: func(context.Context, json.RawMessage) (any, error) { return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}

	ctx := aheadVaultCtx(t)

	// Mutating tool is refused in-band with the IncompatibleError remediation,
	// and the handler does NOT run.
	_, err := reg.Dispatch(ctx, "mut", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("mutating tool: expected surface gate error, got nil")
	}
	var ie *surface.IncompatibleError
	if !errors.As(err, &ie) {
		t.Fatalf("mutating tool: want *surface.IncompatibleError, got %T: %v", err, err)
	}
	if ran {
		t.Fatal("mutating tool handler ran despite the gate")
	}

	// Read-only tool is unaffected.
	if _, err := reg.Dispatch(ctx, "ro", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("read-only tool refused by gate: %v", err)
	}
}

func TestSurfaceGateWarnOverrideProceeds(t *testing.T) {
	t.Setenv("VP_SURFACE_GATE", "warn")
	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := reg.Dispatch(aheadVaultCtx(t), "mut", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("VP_SURFACE_GATE=warn should let the write proceed, got: %v", err)
	}
	if !ran {
		t.Fatal("handler should have run under VP_SURFACE_GATE=warn")
	}
}

func TestSurfaceGateNoVaultOnContextProceeds(t *testing.T) {
	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	// No vault on the context → root "" → gate no-ops.
	if _, err := reg.Dispatch(context.Background(), "mut", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("no-vault context should not gate: %v", err)
	}
	if !ran {
		t.Fatal("handler should run when there is no vault to check")
	}
}

func TestSurfaceGateCompatibleVaultProceeds(t *testing.T) {
	root := t.TempDir()
	// Stamp at the binary's own version (compatible) — not ahead.
	stampDir := filepath.Join(root, "Projects", "p")
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion, "tester"); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(root))

	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Dispatch(ctx, "mut", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("compatible vault should not gate: %v", err)
	}
	if !ran {
		t.Fatal("handler should run against a compatible vault")
	}
}
