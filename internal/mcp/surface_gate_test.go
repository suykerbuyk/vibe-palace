// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

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

// TestSurfaceGateNoVaultOnContextRefuses pins the hole this test used to assert
// as correct behavior: a mutating tool dispatched with NO vault on the context
// (root == "") was admitted, because CheckCompatible folded an empty path into a
// nil "nothing to check" return. There is no coherent way to mutate a vault that
// was never supplied — the gate must refuse.
func TestSurfaceGateNoVaultOnContextRefuses(t *testing.T) {
	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}

	_, err := reg.Dispatch(context.Background(), "mut", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("mutating tool with no vault on context must be refused")
	}
	if !errors.Is(err, surface.ErrNoVault) {
		t.Errorf("err = %v, want surface.ErrNoVault", err)
	}
	if ran {
		t.Error("handler must not run when there is no vault to write to")
	}
}

// TestSurfaceGateNoVaultNotBypassableByWarn pins the escape-hatch scope:
// VP_SURFACE_GATE=warn is an override for a version mismatch (old binary, vault
// present — the write may still be fine), NOT a way to proceed without a vault.
func TestSurfaceGateNoVaultNotBypassableByWarn(t *testing.T) {
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

	if _, err := reg.Dispatch(context.Background(), "mut", json.RawMessage(`{}`)); err == nil {
		t.Fatal("VP_SURFACE_GATE=warn must not bypass a missing vault")
	}
	if ran {
		t.Error("handler must not run under warn with no vault")
	}
}

// TestSurfaceGateUnreachableVaultRefuses covers the operational failure that
// motivated this fix: a vault root deleted out from under a running MCP server.
// The gate previously passed (os.Stat error → nil), so the write proceeded into
// a vault that was not there.
func TestSurfaceGateUnreachableVaultRefuses(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "vanished-vault")
	ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(missing))

	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}

	_, err := reg.Dispatch(ctx, "mut", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("mutating tool against an absent vault root must be refused")
	}
	var ue *surface.VaultUnreachableError
	if !errors.As(err, &ue) {
		t.Errorf("err = %v, want *surface.VaultUnreachableError", err)
	}
	if ran {
		t.Error("handler must not run against an absent vault root")
	}
}

// TestSurfaceGateUnreachableVaultFailsFast is the bounded-timeout regression
// guard for the ~1852 s hang: a mutating tool dispatched against a deleted vault
// root must be refused by the surface gate BEFORE it reaches any write path that
// could block. TestSurfaceGateUnreachableVaultRefuses pins the error VALUE; this
// pins its TIMING. The handler here blocks until cleanup, so if the gate ever
// regresses to admitting the call (as it did when an os.Stat error folded into a
// nil "compatible" return), the dispatch hangs and this test fails on its own
// deadline rather than stalling the caller for the host's ~1852 s idle timeout.
func TestSurfaceGateUnreachableVaultFailsFast(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "vanished-vault")
	ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(missing))

	// The handler blocks until the test tears down. If the gate wrongly admits
	// the call, the dispatch parks here and the deadline below fires; cleanup then
	// releases the parked goroutine so it does not leak past the test.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	reg := testRegistry(t)
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler: func(context.Context, json.RawMessage) (any, error) {
			<-release
			return "ok", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := reg.Dispatch(ctx, "mut", json.RawMessage(`{}`))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("mutating tool against an absent vault root must be refused, not admitted")
		}
		var ue *surface.VaultUnreachableError
		if !errors.As(err, &ue) {
			t.Errorf("err = %v, want *surface.VaultUnreachableError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch against a deleted vault root hung: the surface gate failed to fail-fast (regression of the ~1852 s hang)")
	}
}

// TestSurfaceGateReadOnlyToolUnaffected guards the blast radius: closing the
// gate for mutating tools must not start refusing reads, which are explicitly
// allowed to run against an unreachable vault (they will fail on their own terms
// if they actually need the files).
func TestSurfaceGateReadOnlyToolUnaffected(t *testing.T) {
	reg := testRegistry(t)
	ran := false
	if err := reg.Register(Tool{
		Name:     "ro",
		Mutating: false,
		Handler:  func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := reg.Dispatch(context.Background(), "ro", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("read-only tool must still pass with no vault: %v", err)
	}
	if !ran {
		t.Fatal("read-only handler should have run")
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
