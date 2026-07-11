// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// callSurfaceCheck runs the handler and asserts a clean SurfaceCheckResult back.
func callSurfaceCheck(t *testing.T, vault *storage.Vault, params map[string]any) SurfaceCheckResult {
	t.Helper()
	tool := SurfaceCheckTool(vault)
	raw, _ := json.Marshal(params)
	res, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out, ok := res.(SurfaceCheckResult)
	if !ok {
		t.Fatalf("result type = %T, want SurfaceCheckResult", res)
	}
	return out
}

func TestSurfaceCheckTool_NotMutating(t *testing.T) {
	if got := SurfaceCheckTool(storage.NewVault(t.TempDir())).Mutating; got {
		t.Fatalf("vp_surface_check must be read-only (Mutating=false), got %v", got)
	}
}

// A compatible vault (no stamps) passes: CheckCompatible treats an empty vault
// as best-effort compatible.
func TestSurfaceCheckTool_PassEmptyVault(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	out := callSurfaceCheck(t, vault, map[string]any{})

	if out.Status != "pass" {
		t.Errorf("status = %q, want pass", out.Status)
	}
	if out.BinarySurface != surface.MCPSurfaceVersion {
		t.Errorf("binary_surface = %d, want %d", out.BinarySurface, surface.MCPSurfaceVersion)
	}
	if out.VaultSurface != 0 {
		t.Errorf("vault_surface = %d, want 0 on pass", out.VaultSurface)
	}
	if len(out.Details) != 0 {
		t.Errorf("details = %v, want empty on pass", out.Details)
	}
}

// A stamp at surface <= MCPSurfaceVersion is compatible → pass.
func TestSurfaceCheckTool_PassCompatibleStamp(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	stampDir := filepath.Join(vault.Root, "Projects", "demo")
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion, ""); err != nil {
		t.Fatal(err)
	}

	out := callSurfaceCheck(t, vault, map[string]any{"project": "demo"})

	if out.Status != "pass" {
		t.Errorf("status = %q, want pass", out.Status)
	}
}

// A stamp newer than this binary (surface = MCPSurfaceVersion+1) makes the vault
// appear to have been written by a newer client → fail with remediation.
func TestSurfaceCheckTool_FailNewerVault(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	newer := surface.MCPSurfaceVersion + 1
	stampDir := filepath.Join(vault.Root, "Projects", "demo")
	if err := surface.WriteStamp(stampDir, newer, ""); err != nil {
		t.Fatal(err)
	}

	out := callSurfaceCheck(t, vault, map[string]any{"project": "demo"})

	if out.Status != "fail" {
		t.Fatalf("status = %q, want fail\n%+v", out.Status, out)
	}
	if out.VaultSurface != newer {
		t.Errorf("vault_surface = %d, want %d", out.VaultSurface, newer)
	}
	if out.BinarySurface != surface.MCPSurfaceVersion {
		t.Errorf("binary_surface = %d, want %d", out.BinarySurface, surface.MCPSurfaceVersion)
	}
	if out.StampDir != stampDir {
		t.Errorf("stamp_dir = %q, want %q", out.StampDir, stampDir)
	}
	joined := strings.Join(out.Details, "\n")
	if !strings.Contains(joined, "git pull && make install") {
		t.Errorf("details missing upgrade line: %v", out.Details)
	}
	if !strings.Contains(joined, "VP_SURFACE_GATE=warn") {
		t.Errorf("details missing override line: %v", out.Details)
	}
}

// An empty/unreachable vault path is best-effort compatible → pass.
func TestSurfaceCheckTool_EmptyVaultPath(t *testing.T) {
	vault := storage.NewVault("")

	out := callSurfaceCheck(t, vault, map[string]any{})

	if out.Status != "pass" {
		t.Errorf("status = %q, want pass for empty vault path", out.Status)
	}
	if out.VaultSurface != 0 {
		t.Errorf("vault_surface = %d, want 0", out.VaultSurface)
	}
}
