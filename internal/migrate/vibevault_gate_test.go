// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// gateSourceVault is a source tree with one project holding a good session
// (which reaches the vault writers and, on success, the imported marker) and a
// bad-frontmatter one (which goes straight to the parse_failed marker).
func gateSourceVault(t *testing.T) *storage.Vault {
	t.Helper()
	root := t.TempDir()
	sessDir := filepath.Join(root, "Projects", "test-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"2026-04-01-01.md": testSession1,
		"bad-session.md":   testSessionBadFrontmatter,
	} {
		if err := os.WriteFile(filepath.Join(sessDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return storage.NewVault(root)
}

func gateImport(t *testing.T, source, destination *storage.Vault) ImportResult {
	t.Helper()
	result, err := ImportVibeVault(context.Background(), source, destination, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportVibeVault: %v", err)
	}
	return result
}

// Row 18. When the destination scaffold fails, the project is not initialised,
// so nothing the import does for it may leave a palace/<slug>/ behind: its
// vault writes are refused by their primitives, and its markers live in the
// host-local palace/.local/imports/<slug>/, which is no project tree. The
// scaffold is made to fail with regular FILES at Projects/test-project/commands
// and /skills in the destination, which fails for root too and leaves the
// project a phantom (a directory with no marker).
func TestImportVibeVault_FailedScaffoldLeavesNoPalaceTree(t *testing.T) {
	source := gateSourceVault(t)
	destination := storage.NewVault(t.TempDir())
	projDir := filepath.Join(destination.Root, "Projects", "test-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"commands", "skills"} {
		if err := os.WriteFile(filepath.Join(projDir, kind), []byte("not a directory\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result := gateImport(t, source, destination)

	if result.SessionsImported != 0 {
		t.Errorf("SessionsImported = %d, want 0 into an uninitialised project", result.SessionsImported)
	}
	var refused bool
	for _, e := range result.Errors {
		if e.Project != "test-project" {
			t.Errorf("error for unexpected project %q: %v", e.Project, e.Err)
		}
		if errors.Is(e.Err, projectdir.ErrUninitialisedProject) {
			refused = true
		}
	}
	if !refused {
		t.Errorf("result.Errors must carry ErrUninitialisedProject for test-project, got %v", result.Errors)
	}

	// A classifier error (a marker path that is not a directory) counts as
	// uninitialised, as it does for the gate.
	if state, err := storage.ClassifyProjectDir(destination.Root, "test-project"); err == nil && state.Initialised() {
		t.Errorf("fixture: the scaffold must have failed, leaving test-project uninitialised (state %v)", state)
	}
	if _, err := os.Lstat(filepath.Join(destination.Root, "palace", "test-project")); !os.IsNotExist(err) {
		t.Errorf("palace/test-project must not exist after a failed scaffold (lstat err: %v)", err)
	}
}

// The counterpart, with a separate destination as above: a scaffold that
// succeeds leaves both markers in the project's marker file (markerFile).
// (TestImportVibeVault_SlugMapping and TestImportVibeVault_BadFrontmatter pin
// the marker with source == destination.)
func TestImportVibeVault_ScaffoldedDestinationGetsTheMarker(t *testing.T) {
	source := gateSourceVault(t)
	destination := storage.NewVault(t.TempDir())

	result := gateImport(t, source, destination)

	if result.SessionsImported != 1 {
		t.Errorf("SessionsImported = %d, want 1 (errors: %v)", result.SessionsImported, result.Errors)
	}
	for _, e := range result.Errors {
		if errors.Is(e.Err, projectdir.ErrUninitialisedProject) {
			t.Errorf("an initialised destination was refused: %v", e.Err)
		}
	}
	marker, err := markerFile(destination, "test-project")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	for _, want := range []string{`"session_id":"2026-04-01-01"`, `"reason":"parse_failed"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("marker must record %s, got %q", want, data)
		}
	}
}
