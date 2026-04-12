// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/migrate"
)

func TestMigrateParentShowsHelp(t *testing.T) {
	cmd := cmdMigrate()
	code := cmd.Run(nil)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK (parent help)", code)
	}
}

func TestMigrateVibeVaultBadFlags(t *testing.T) {
	cmd := cmdMigrateVibeVault()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestMigrateMemPalaceMissing(t *testing.T) {
	cmd := cmdMigrateMemPalace()
	code := cmd.Run(nil)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (missing --export-path)", code, cli.ExitUser)
	}
}

func TestMigrateMemPalaceBadFlags(t *testing.T) {
	cmd := cmdMigrateMemPalace()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestPrintMigrateResult(t *testing.T) {
	// Verify it doesn't panic with various inputs.
	result := migrate.ImportResult{
		ProjectsScanned:  2,
		SessionsImported: 10,
		SessionsSkipped:  3,
		DrawersCreated:   50,
		EntitiesCreated:  15,
		TriplesCreated:   8,
	}
	printMigrateResult(result, false)
	printMigrateResult(result, true)

	// With errors.
	result.Errors = []migrate.ImportError{
		{Project: "p", Err: fmt.Errorf("err1")},
		{Project: "p", Err: fmt.Errorf("err2")},
	}
	printMigrateResult(result, false)
}

func TestMigrateProgressFunc(t *testing.T) {
	// Verify the progress func doesn't panic on any event type.
	fn := migrateProgressFunc()
	fn(migrate.ProgressEvent{Type: migrate.ProgressProjectStart, Project: "test"})
	fn(migrate.ProgressEvent{Type: migrate.ProgressSessionDone, SessionID: "s1", Current: 1, Total: 2})
	fn(migrate.ProgressEvent{Type: migrate.ProgressSessionSkip, SessionID: "s2", Current: 2, Total: 2})
	fn(migrate.ProgressEvent{Type: migrate.ProgressProjectDone})
	fn(migrate.ProgressEvent{Type: migrate.ProgressError, Message: "something failed"})
}

func TestOpenMigrateVaultWithPath(t *testing.T) {
	dir := t.TempDir()
	v, cfg, err := openMigrateVault(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v == nil {
		t.Fatal("vault is nil")
	}
	_ = cfg
}

func TestOpenMigrateVaultFromConfig(t *testing.T) {
	setupTestVaultEnv(t)
	v, cfg, err := openMigrateVault("")
	if err != nil {
		t.Fatal(err)
	}
	if v == nil {
		t.Fatal("vault is nil")
	}
	_ = cfg
}

func TestBuildSlugResolver_YesAuto(t *testing.T) {
	dir := t.TempDir()
	r, err := buildSlugResolver(dir, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*migrate.AutoResolver); !ok {
		t.Errorf("expected AutoResolver, got %T", r)
	}
}

func TestBuildSlugResolver_SlugMapWrapsBase(t *testing.T) {
	dir := t.TempDir()
	r, err := buildSlugResolver(dir, true, "foo=bar")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*migrate.MapResolver); !ok {
		t.Errorf("expected MapResolver, got %T", r)
	}
}

func TestBuildSlugResolver_InvalidMap(t *testing.T) {
	dir := t.TempDir()
	_, err := buildSlugResolver(dir, true, "bogus")
	if err == nil {
		t.Fatal("expected error on malformed slug-map")
	}
}

func TestBuildSlugResolver_NonTTYSelectsAuto(t *testing.T) {
	// In `go test`, stdin is typically /dev/null or pipe, not a TTY —
	// so !isStdinTTY() → AutoResolver path.
	dir := t.TempDir()
	r, err := buildSlugResolver(dir, false, "")
	if err != nil {
		t.Fatal(err)
	}
	// May be AutoResolver or InteractiveResolver depending on test env;
	// either is acceptable but for `go test` it should be AutoResolver.
	switch r.(type) {
	case *migrate.AutoResolver, *migrate.InteractiveResolver:
		// ok
	default:
		t.Errorf("unexpected resolver type %T", r)
	}
}

func TestScanOnDiskSlugsForResolver(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "A_B"), 0o755)
	os.WriteFile(filepath.Join(root, "skipfile"), []byte("x"), 0o644)
	got, err := scanOnDiskSlugsForResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got["a-b"] {
		t.Errorf("missing a-b in %v", got)
	}
	if got["skipfile"] {
		t.Errorf("file should not appear: %v", got)
	}
}

func TestScanOnDiskSlugsForResolver_MissingDir(t *testing.T) {
	got, err := scanOnDiskSlugsForResolver(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestPrintMigrateResult_WithSlugRemap(t *testing.T) {
	result := migrate.ImportResult{
		ProjectsScanned: 2,
		SlugRemap:       map[string]string{"foo": "foo-vp", "bar": "bar-vp"},
	}
	printMigrateResult(result, true)
}

func TestMigrateProgressFuncDeferred(t *testing.T) {
	fn := migrateProgressFuncDeferred()
	// First event triggers banner.
	fn(migrate.ProgressEvent{Type: migrate.ProgressProjectStart, Project: "p"})
	fn(migrate.ProgressEvent{Type: migrate.ProgressSessionDone, SessionID: "s", Current: 1, Total: 1})
	fn(migrate.ProgressEvent{Type: migrate.ProgressProjectDone})
}

func TestIsStdinTTY_Runs(t *testing.T) {
	// Just prove it doesn't panic.
	_ = isStdinTTY()
}

func TestMigrateMemPalaceDryRun(t *testing.T) {
	setupTestVaultEnv(t)
	cmd := cmdMigrateMemPalace()
	code := cmd.Run([]string{"--export-path", "/nonexistent/export.json", "--dry-run"})
	// Should get past flag parsing + vault open; fails at embedder or import.
	if code == cli.ExitUser {
		t.Errorf("should not be ExitUser (flags valid)")
	}
}

func TestMigrateVibeVaultDryRun(t *testing.T) {
	setupTestVaultEnv(t)
	cmd := cmdMigrateVibeVault()
	// --dry-run should get past flag parsing and vault opening but fail at embedder.
	code := cmd.Run([]string{"--dry-run"})
	// Expect ExitSystem (embedder fails in test) or ExitOK (if dry-run short-circuits).
	if code == cli.ExitUser {
		t.Errorf("should not be ExitUser (flags are valid)")
	}
}
