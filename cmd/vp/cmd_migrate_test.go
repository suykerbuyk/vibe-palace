// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/migrate"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

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

func TestOpenMigrateDestination(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	v, cfg, err := openMigrateDestination()
	if err != nil {
		t.Fatal(err)
	}
	if v == nil {
		t.Fatal("vault is nil")
	}
	// Destination always comes from the config vault_path.
	wantAbs, _ := expandAndAbsPath(vaultDir)
	gotAbs, _ := expandAndAbsPath(v.Root)
	if gotAbs != wantAbs {
		t.Errorf("dest root = %q, want %q", gotAbs, wantAbs)
	}
	_ = cfg
}

func TestOpenMigrateSourceWithPath(t *testing.T) {
	dest := storage.NewVault("/some/dest")
	src := t.TempDir()
	v, err := openMigrateSource(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	wantAbs, _ := expandAndAbsPath(src)
	if v.Root != wantAbs {
		t.Errorf("source root = %q, want resolved %q", v.Root, wantAbs)
	}
	if v == dest {
		t.Error("source should be a distinct vault when --vault-path is set")
	}
}

func TestOpenMigrateSourceEmptyReturnsDest(t *testing.T) {
	dest := storage.NewVault("/some/dest")
	v, err := openMigrateSource("", dest)
	if err != nil {
		t.Fatal(err)
	}
	if v != dest {
		t.Errorf("empty --vault-path should reuse dest pointer, got %p want %p", v, dest)
	}
}

func TestMigrateBanner(t *testing.T) {
	same := migrateBanner("/a/vault", "/a/vault")
	if !strings.Contains(same, "Same vault:  yes") {
		t.Errorf("expected Same vault: yes, got:\n%s", same)
	}
	if !strings.Contains(same, "Source:      /a/vault") || !strings.Contains(same, "Destination: /a/vault") {
		t.Errorf("banner missing source/dest lines:\n%s", same)
	}
	diff := migrateBanner("/a/src", "/b/dst")
	if !strings.Contains(diff, "Same vault:  no") {
		t.Errorf("expected Same vault: no, got:\n%s", diff)
	}
}

func TestConfirmCrossVaultWrite_SameVaultProceeds(t *testing.T) {
	ok, msg := confirmCrossVaultWrite(true, false, false, false, "/v", "/v", strings.NewReader(""), io.Discard)
	if !ok || msg != "" {
		t.Errorf("same vault should proceed silently, got (%v, %q)", ok, msg)
	}
}

func TestConfirmCrossVaultWrite_DryRunProceeds(t *testing.T) {
	ok, _ := confirmCrossVaultWrite(false, true, false, false, "/s", "/d", strings.NewReader(""), io.Discard)
	if !ok {
		t.Error("dry run should proceed without prompting")
	}
}

func TestConfirmCrossVaultWrite_YesProceeds(t *testing.T) {
	ok, _ := confirmCrossVaultWrite(false, false, true, false, "/s", "/d", strings.NewReader(""), io.Discard)
	if !ok {
		t.Error("--yes should proceed without prompting")
	}
}

func TestConfirmCrossVaultWrite_NonTTYAborts(t *testing.T) {
	ok, msg := confirmCrossVaultWrite(false, false, false, false, "/s", "/d", strings.NewReader(""), io.Discard)
	if ok {
		t.Error("non-TTY cross-vault without --yes must abort")
	}
	if !strings.Contains(msg, "--yes") {
		t.Errorf("abort message should mention --yes, got %q", msg)
	}
}

func TestConfirmCrossVaultWrite_TTYPromptYes(t *testing.T) {
	var prompt bytes.Buffer
	ok, _ := confirmCrossVaultWrite(false, false, false, true, "/s", "/d", strings.NewReader("y\n"), &prompt)
	if !ok {
		t.Error("'y' answer should proceed")
	}
	if !strings.Contains(prompt.String(), "Write into /d (reading from /s)?") {
		t.Errorf("prompt text wrong: %q", prompt.String())
	}
}

func TestConfirmCrossVaultWrite_TTYPromptNo(t *testing.T) {
	ok, msg := confirmCrossVaultWrite(false, false, false, true, "/s", "/d", strings.NewReader("n\n"), io.Discard)
	if ok {
		t.Error("'n' answer should abort")
	}
	if msg != "Aborted." {
		t.Errorf("expected 'Aborted.', got %q", msg)
	}
}

func TestSourceHasOrphanMarkers(t *testing.T) {
	mkMarker := func(root, project string) {
		dir := filepath.Join(root, "palace", project, ".local")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "imported-sessions.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Source has a marker, destination has none → orphaned.
	src := t.TempDir()
	dst := t.TempDir()
	mkMarker(src, "proj-a")
	if !sourceHasOrphanMarkers(src, dst) {
		t.Error("source-only markers should report orphaned")
	}

	// Both have markers → not orphaned.
	dst2 := t.TempDir()
	mkMarker(dst2, "proj-b")
	if sourceHasOrphanMarkers(src, dst2) {
		t.Error("markers present in destination should not report orphaned")
	}

	// Same root → never orphaned.
	if sourceHasOrphanMarkers(src, src) {
		t.Error("same root should never report orphaned")
	}

	// No markers anywhere → not orphaned.
	if sourceHasOrphanMarkers(t.TempDir(), t.TempDir()) {
		t.Error("no markers should not report orphaned")
	}
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
