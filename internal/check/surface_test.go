// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"errors"
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

	t.Run("no vault configured is Info, not Pass", func(t *testing.T) {
		// A host that has not run `vp init` yet: reported, but non-halting —
		// /vpc-restart only halts on status=fail.
		r := CheckSurface("")
		if r.Status != Info {
			t.Fatalf("status = %v, want Info (%s)", r.Status, r.Summary)
		}
		if !errors.Is(r.Err, surface.ErrNoVault) {
			t.Errorf("Err = %v, want ErrNoVault", r.Err)
		}
	})

	t.Run("configured but absent vault root fails", func(t *testing.T) {
		// The dangerous case: a vault_path pointing at a root that has been
		// moved, deleted, or unmounted. This must be a hard Fail so the restart
		// template halts before bootstrapping context or mutating tasks.
		missing := filepath.Join(t.TempDir(), "vanished-vault")
		r := CheckSurface(missing)
		if r.Status != Fail {
			t.Fatalf("status = %v, want Fail (%s)", r.Status, r.Summary)
		}
		if !strings.Contains(r.Summary, "unreachable") {
			t.Errorf("summary should name the condition, got %q", r.Summary)
		}
		if len(r.Details) == 0 {
			t.Error("expected remediation details on an unreachable vault")
		}
		var ue *surface.VaultUnreachableError
		if !errors.As(r.Err, &ue) {
			t.Errorf("Err = %v, want *surface.VaultUnreachableError", r.Err)
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
