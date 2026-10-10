// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckCompatibleAsGatesOnEveryStampDir is the gating seam the authored-only
// migration's rows depend on. A stamp at MCPSurfaceVersion in ANY of the four
// stamp directories the gate scans must refuse a binary one surface behind, and
// admit a binary at the current surface. The Audits/ case is the one the
// empty-vault path relies on: it writes the vault's only stamp there.
func TestCheckCompatibleAsGatesOnEveryStampDir(t *testing.T) {
	stampDirs := []string{
		filepath.Join("Projects", "alpha"),
		filepath.Join("palace", "alpha"),
		"Templates",
		"Audits",
	}
	for _, rel := range stampDirs {
		t.Run(rel, func(t *testing.T) {
			vault := t.TempDir()
			dir := filepath.Join(vault, rel)
			if err := WriteStamp(dir, MCPSurfaceVersion, ""); err != nil {
				t.Fatalf("WriteStamp(%s): %v", dir, err)
			}
			// A one-older binary is gated out.
			if err := checkCompatibleAs(vault, MCPSurfaceVersion-1); err == nil {
				t.Errorf("checkCompatibleAs(vault, %d) admitted a vault stamped at %d in %s; want refusal",
					MCPSurfaceVersion-1, MCPSurfaceVersion, rel)
			}
			// The current binary is admitted.
			if err := checkCompatibleAs(vault, MCPSurfaceVersion); err != nil {
				t.Errorf("checkCompatibleAs(vault, %d) refused a vault stamped at %d in %s: %v",
					MCPSurfaceVersion, MCPSurfaceVersion, rel, err)
			}
		})
	}
}

// TestCheckCompatibleAsNoStampAdmits confirms a stampless vault admits even an
// older binary (absence means pass on the surface axis) — the property the
// empty-vault path removes by writing Audits/.surface.
func TestCheckCompatibleAsNoStampAdmits(t *testing.T) {
	vault := t.TempDir()
	if err := checkCompatibleAs(vault, MCPSurfaceVersion-1); err != nil {
		t.Errorf("a stampless vault refused a one-older binary: %v", err)
	}
}

// TestCheckCompatibleAsEmptyVaultRevertUngates models the empty-vault path's
// gate transition across a revert at the surface level (12-S4; ADR-014 lines
// 1195-1198): with Audits/.surface — the vault's only stamp — present, a
// one-older binary is gated out; after the revert removes it, the same call no
// longer refuses. This is why the empty-vault rollback MUST re-stamp
// Audits/.surface in the same push, a requirement the command's rollback text
// states (tied to the command-level TestAOEmptyVaultSurfaceGoneAfterRevert).
func TestCheckCompatibleAsEmptyVaultRevertUngates(t *testing.T) {
	vault := t.TempDir()
	audits := filepath.Join(vault, "Audits")
	// Migration state: the empty-vault path wrote Audits/.surface at the current
	// surface, so an older binary is gated out.
	if err := WriteStamp(audits, MCPSurfaceVersion, ""); err != nil {
		t.Fatalf("WriteStamp: %v", err)
	}
	if err := checkCompatibleAs(vault, MCPSurfaceVersion-1); err == nil {
		t.Fatal("with Audits/.surface present, a one-older binary must be gated out")
	}
	// Reverted state: the revert removed the vault's only stamp.
	if err := os.Remove(filepath.Join(audits, ".surface")); err != nil {
		t.Fatal(err)
	}
	if err := checkCompatibleAs(vault, MCPSurfaceVersion-1); err != nil {
		t.Errorf("after the revert removed Audits/.surface, the one-older binary must NOT be refused: %v", err)
	}
}
