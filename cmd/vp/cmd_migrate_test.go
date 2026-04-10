// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
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
