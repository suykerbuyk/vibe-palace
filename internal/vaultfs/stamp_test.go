// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// assertStamped fails unless stampDir/.surface records the current surface.
// Replicated from the sibling stamp_tests (internal/storage, internal/archive,
// internal/reconcile): the helper there lives in an unexported _test.go and is
// not importable across packages, so the minimal .surface-exists + version
// check is inlined here.
func assertStamped(t *testing.T, stampDir string) {
	t.Helper()
	s, err := surface.ReadStamp(stampDir)
	if err != nil {
		t.Fatalf("ReadStamp(%s): %v", stampDir, err)
	}
	if s.Surface != surface.MCPSurfaceVersion {
		t.Fatalf("stamp at %s = surface %d, want %d", stampDir, s.Surface, surface.MCPSurfaceVersion)
	}
}

// seedFileDirect writes a precondition file straight to disk, bypassing the
// stamping writers, so a test can prove the writer/op-under-test does (or does
// not) do its own stamping — and so the per-process stamp cache is not warmed
// by a setup helper. Mirrors writeFileDirect in internal/storage/stamp_test.go.
func seedFileDirect(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestVaultfsWritersStamp is the surface-handshake guard for internal/vaultfs.
//
// vaultfs.Write and vaultfs.Edit route content through atomicfile.Write, which
// best-effort stamps the MCP surface version when the write path resolves under
// a recognized stamp root (see internal/surface.ResolveStampDir: Projects/<p>,
// palace/<p>, Templates). vaultfs.Delete and vaultfs.Move deliberately use
// os.Remove / os.Rename directly — they remove or relocate content rather than
// writing new content — so they must NOT touch a .surface stamp.
//
// Each subtest uses its own t.TempDir() vault so the unique (absolute) stamp
// dir is never warmed in surface's per-process stampedDirs cache. That keeps
// the assertions meaningful: a missing stamp is a real missing stamp (not a
// cache no-op), and a present stamp on Delete/Move would be a real regression.
//
// Stampable paths are constructed under Projects/<slug>/… so ResolveStampDir
// returns <vault>/Projects/<slug>; writing to a bare temp path (no recognized
// top-level root) resolves to no stamp dir and would never stamp.
func TestVaultfsWritersStamp(t *testing.T) {
	const slug = "demo"

	t.Run("Write stamps under Projects root", func(t *testing.T) {
		vault := t.TempDir()
		// Recognized stamp root: Projects/<slug>/… → <vault>/Projects/<slug>.
		if _, err := Write(vault, "Projects/"+slug+"/notes/x.md", "hello", ""); err != nil {
			t.Fatalf("Write: %v", err)
		}
		assertStamped(t, filepath.Join(vault, "Projects", slug))
	})

	t.Run("Edit stamps under Projects root", func(t *testing.T) {
		vault := t.TempDir()
		rel := "Projects/" + slug + "/notes/x.md"
		// Seed the target directly (no stamp, no cache warm) so the stamp that
		// appears must be produced by the Edit-under-test, not by setup.
		seedFileDirect(t, filepath.Join(vault, filepath.FromSlash(rel)), []byte("alpha bravo"))
		stampDir := filepath.Join(vault, "Projects", slug)
		if _, err := os.Stat(filepath.Join(stampDir, ".surface")); !os.IsNotExist(err) {
			t.Fatalf("precondition: .surface should not exist before Edit (err=%v)", err)
		}
		if _, err := Edit(vault, rel, "bravo", "DELTA", false, ""); err != nil {
			t.Fatalf("Edit: %v", err)
		}
		assertStamped(t, stampDir)
	})

	// Delete and Move remove/relocate content rather than writing it, so they
	// route around atomicfile and must not create or refresh a .surface stamp.
	// Each uses a fresh (uncached) vault and seeds its target directly: if the
	// op wrongly stamped, .surface WOULD appear at the recognized stamp root,
	// so its absence is a genuine signal, not a cache artifact.
	t.Run("Delete does not stamp", func(t *testing.T) {
		vault := t.TempDir()
		rel := "Projects/" + slug + "/notes/victim.md"
		seedFileDirect(t, filepath.Join(vault, filepath.FromSlash(rel)), []byte("x"))
		stampPath := filepath.Join(vault, "Projects", slug, ".surface")
		if _, err := Delete(vault, rel, ""); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Stat(stampPath); !os.IsNotExist(err) {
			t.Fatalf("Delete created/refreshed a .surface stamp (err=%v); Delete must not stamp", err)
		}
	})

	t.Run("Move does not stamp", func(t *testing.T) {
		vault := t.TempDir()
		from := "Projects/" + slug + "/notes/from.md"
		to := "Projects/" + slug + "/notes/to.md"
		seedFileDirect(t, filepath.Join(vault, filepath.FromSlash(from)), []byte("data"))
		stampPath := filepath.Join(vault, "Projects", slug, ".surface")
		if _, err := Move(vault, from, to); err != nil {
			t.Fatalf("Move: %v", err)
		}
		if _, err := os.Stat(stampPath); !os.IsNotExist(err) {
			t.Fatalf("Move created/refreshed a .surface stamp (err=%v); Move must not stamp", err)
		}
	})

	// Belt-and-suspenders for the "no refresh" wording in the task: a stamp that
	// already exists must be left untouched by Delete/Move (they never re-write
	// it). Seed a stamp via Write, snapshot it, remove it, then run a Delete on a
	// sibling and confirm Delete did not recreate the stamp.
	t.Run("Delete does not refresh an existing stamp", func(t *testing.T) {
		vault := t.TempDir()
		// First write creates the stamp at the project root.
		if _, err := Write(vault, "Projects/"+slug+"/notes/a.md", "a", ""); err != nil {
			t.Fatalf("Write: %v", err)
		}
		stampPath := filepath.Join(vault, "Projects", slug, ".surface")
		assertStamped(t, filepath.Join(vault, "Projects", slug))
		// Seed a sibling directly (no stamp), drop the stamp, then Delete the
		// sibling: Delete must not bring the stamp back.
		sib := "Projects/" + slug + "/notes/b.md"
		seedFileDirect(t, filepath.Join(vault, filepath.FromSlash(sib)), []byte("b"))
		if err := os.Remove(stampPath); err != nil {
			t.Fatalf("remove stamp: %v", err)
		}
		if _, err := Delete(vault, sib, ""); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Stat(stampPath); !os.IsNotExist(err) {
			t.Fatalf("Delete refreshed a removed .surface stamp (err=%v); Delete must not stamp", err)
		}
	})
}
