// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// TestApplyStampsVaultWrites proves the absorb writer leaves a .surface stamp
// at the project root for the vault files it creates (doc/knowledge/workflow
// via appendOrCreate and the resume scratch via appendScratch — all under
// Projects/<slug>). The rewritten source file lives in the project repo, not
// the vault, so it is deliberately not stamped.
func TestApplyStampsVaultWrites(t *testing.T) {
	v := seedVaultProject(t, "checkers01")

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte(checkersFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	fixedTime, _ := time.Parse("2006-01-02", "2026-04-12")
	if _, err := Apply(plan, WriteOptions{
		Vault:       v,
		Project:     "checkers01",
		ProjectRoot: repo,
		Now:         fixedTime,
	}); err != nil {
		t.Fatal(err)
	}

	stampDir := filepath.Join(v.Root, "Projects", "checkers01")
	s, err := surface.ReadStamp(stampDir)
	if err != nil {
		t.Fatalf("ReadStamp(%s): %v", stampDir, err)
	}
	if s.Surface != surface.MCPSurfaceVersion {
		t.Fatalf("stamp at %s = surface %d, want %d", stampDir, s.Surface, surface.MCPSurfaceVersion)
	}
}
