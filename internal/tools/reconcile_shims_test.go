// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/project"
)

// TestReconcileHostShims_Guard proves the cwd guard on the bootstrap
// self-heal: the reconcile runs ONLY when the server's working directory
// resolves to the same project the bootstrap is for. A mismatch (server
// started outside the project tree) must decline silently rather than scatter
// shim files into the wrong directory.
func TestReconcileHostShims_Guard(t *testing.T) {
	vault := t.TempDir()
	resolver := vpctx.NewResolver(vault)

	// A project dir with a known-good slug basename, so DetectProject's
	// basename fallback yields a deterministic slug regardless of the temp
	// path's leaf name.
	projectDir := filepath.Join(t.TempDir(), "guarded-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectDir)

	want, err := project.DetectProject(projectDir)
	if err != nil {
		t.Fatalf("DetectProject: %v", err)
	}

	// Matching project → the guard passes and the reconcile materializes the
	// embedded command shims (fresh emit → non-empty report).
	if rep := reconcileHostShims(resolver, want); rep.Empty() {
		t.Errorf("guard should have run reconcile for matching project %q", want)
	}

	// Non-matching project → the guard declines before writing anything.
	if rep := reconcileHostShims(resolver, want+"-different"); !rep.Empty() {
		t.Errorf("guard must decline when cwd project != bootstrap project; got %+v", rep)
	}
}
