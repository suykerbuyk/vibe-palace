// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// stageAheadVault writes a Projects/<p>/.surface stamp one version ahead of
// this binary under a fresh temp vault, mirroring the staging helper in
// internal/mcp/surface_gate_test.go. It returns the vault root.
func stageAheadVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	stampDir := filepath.Join(root, "Projects", "p")
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion+1, "tester"); err != nil {
		t.Fatalf("stage ahead stamp: %v", err)
	}
	return root
}

func TestCheckSurface(t *testing.T) {
	t.Run("empty vault passes", func(t *testing.T) {
		// A vault directory that exists but holds no .surface stamps.
		r := CheckSurface(t.TempDir())
		if r.Status != Pass {
			t.Fatalf("status = %v, want Pass (%s)", r.Status, r.Summary)
		}
		if r.Name != "Surface" {
			t.Errorf("name = %q, want Surface", r.Name)
		}
	})

	t.Run("empty path passes (vault unreachable)", func(t *testing.T) {
		r := CheckSurface("")
		if r.Status != Pass {
			t.Fatalf("status = %v, want Pass (%s)", r.Status, r.Summary)
		}
	})

	t.Run("compatible vault passes", func(t *testing.T) {
		root := t.TempDir()
		stampDir := filepath.Join(root, "Projects", "p")
		if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion, "tester"); err != nil {
			t.Fatal(err)
		}
		r := CheckSurface(root)
		if r.Status != Pass {
			t.Fatalf("status = %v, want Pass (%s)", r.Status, r.Summary)
		}
	})

	t.Run("ahead vault fails with remediation", func(t *testing.T) {
		r := CheckSurface(stageAheadVault(t))
		if r.Status != Fail {
			t.Fatalf("status = %v, want Fail (%s)", r.Status, r.Summary)
		}
		if !strings.Contains(r.Summary, "binary v") || !strings.Contains(r.Summary, "vault v") {
			t.Errorf("summary missing version verdict: %q", r.Summary)
		}
		if len(r.Details) == 0 {
			t.Error("expected remediation Details on Fail")
		}
		if r.Err == nil {
			t.Error("expected Err set on Fail")
		}
	})
}
