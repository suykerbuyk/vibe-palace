// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// TestIntegrationSurfaceCheck drives vp_surface_check through the FULL MCP stack:
// the real tool set is registered via tools.RegisterAll and the tool is
// dispatched BY NAME over the JSON-RPC tools/call path (Server.HandleMessage),
// exactly as an MCP client would, against a real on-disk vault carrying real
// .surface stamps. It proves both verdict directions round-trip and — the point
// of the read-only surface preflight — that the curated remediation survives the
// wire on the fail path.
func TestIntegrationSurfaceCheck(t *testing.T) {
	// dispatch marshals the tool's JSON text content back into the shared
	// SurfaceCheckResult schema, proving the tool and CLI share one wire contract.
	dispatch := func(t *testing.T, h *testHarness, args map[string]any) tools.SurfaceCheckResult {
		t.Helper()
		raw := h.callTool(t, "vp_surface_check", args)
		var out tools.SurfaceCheckResult
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("decode SurfaceCheckResult: %v\n%s", err, raw)
		}
		return out
	}

	// Pass: a compatible vault (fresh temp vault, no .surface stamps) dispatched
	// by name yields status="pass" and reports this binary's surface version.
	t.Run("pass_compatible_vault", func(t *testing.T) {
		h := newHarness(t, false)
		h.registerAllTools(t)

		out := dispatch(t, h, map[string]any{})

		if out.Status != "pass" {
			t.Fatalf("status = %q, want pass\n%+v", out.Status, out)
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
	})

	// Fail: stamp a known target under the vault with surface = MCPSurfaceVersion+1
	// (a vault written by a newer binary). Dispatched by name, the tool must report
	// status="fail", surface the exact newer version, and carry BOTH curated
	// remediation lines verbatim through the JSON-RPC round-trip.
	t.Run("fail_newer_vault_carries_remediation", func(t *testing.T) {
		h := newHarness(t, false)
		h.registerAllTools(t)

		newer := surface.MCPSurfaceVersion + 1
		// A recognized stamp target: <vault>/Projects/<p> maps to the .surface
		// scanned by CheckCompatible. WriteStamp emits exactly "surface = 2\n".
		stampDir := filepath.Join(h.Vault.Root, "Projects", "demo")
		if err := surface.WriteStamp(stampDir, newer, ""); err != nil {
			t.Fatalf("WriteStamp: %v", err)
		}

		out := dispatch(t, h, map[string]any{"project": "demo"})

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
			t.Errorf("details missing upgrade remediation line: %v", out.Details)
		}
		if !strings.Contains(joined, "VP_SURFACE_GATE=warn") {
			t.Errorf("details missing override remediation line: %v", out.Details)
		}
	})
}
