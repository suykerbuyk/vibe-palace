// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// validSessionTemplate is a minimal valid session-file body used by tests
// that need to swap a malformed file for a working one (see the
// repair-after-parse-fail flow). The %s placeholder is the session_id.
const validSessionTemplate = `---
session_id: "%s"
project: bad-project
date: "2026-04-01"
title: "Repaired Session"
summary: "Now well-formed"
tag: implementation
---
## Transcript

Repaired body content for re-import after parse_failed marker.
`

const testSession1 = `---
session_id: "2026-04-01-01"
project: test-project
date: "2026-04-01"
title: "Test Session One"
summary: "Did some testing"
tag: implementation
---
## Transcript

This is a test session about implementing authentication middleware.
It covers JWT token validation and role-based access control.
`

const testSession2 = `---
session_id: "2026-04-01-02"
project: test-project
date: "2026-04-01"
title: "Test Session Two"
summary: "Refactored storage layer"
tag: refactor
---
## Transcript

This session focused on refactoring the storage layer to use interfaces.
We introduced the Repository pattern for better testability.
`

const testSession3 = `---
session_id: "2026-04-02-01"
project: test-project
date: "2026-04-02"
title: "Test Session Three"
summary: "Added search functionality"
tag: implementation
---
## Transcript

Implemented full-text search using an inverted index approach.
The search engine supports fuzzy matching and ranking.
`

const testSessionBadFrontmatter = `---
session_id: [invalid yaml
project: test-project
---
Some body text here.
`

const testSessionEmptyBody = `---
session_id: "2026-04-03-01"
project: test-project
date: "2026-04-03"
title: "Empty Body Session"
summary: ""
tag: exploration
---
`

func setupTestVault(t *testing.T) (*storage.Vault, *search.Engine, embedder.Embedder, storage.Config) {
	t.Helper()
	tmpDir := t.TempDir()

	// Create project directory structure.
	sessDir := filepath.Join(tmpDir, "Projects", "test-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write test session files.
	for name, content := range map[string]string{
		"2026-04-01-01.md": testSession1,
		"2026-04-01-02.md": testSession2,
		"2026-04-02-01.md": testSession3,
	} {
		if err := os.WriteFile(filepath.Join(sessDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	return vault, engine, emb, cfg
}

func TestImportVibeVault_Basic(t *testing.T) {
	vault, engine, emb, cfg := setupTestVault(t)

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ProjectsScanned != 1 {
		t.Errorf("ProjectsScanned = %d, want 1", result.ProjectsScanned)
	}
	if result.SessionsImported != 3 {
		t.Errorf("SessionsImported = %d, want 3", result.SessionsImported)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}

func TestImportVibeVault_Idempotent(t *testing.T) {
	vault, engine, emb, cfg := setupTestVault(t)
	ctx := context.Background()

	// First import.
	r1, err := ImportVibeVault(ctx, vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if r1.SessionsImported != 3 {
		t.Fatalf("first run: SessionsImported = %d, want 3", r1.SessionsImported)
	}

	// Second import — everything should be skipped.
	r2, err := ImportVibeVault(ctx, vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if r2.SessionsSkipped != 3 {
		t.Errorf("second run: SessionsSkipped = %d, want 3", r2.SessionsSkipped)
	}
	if r2.SessionsImported != 0 {
		t.Errorf("second run: SessionsImported = %d, want 0", r2.SessionsImported)
	}
}

func TestImportVibeVault_DryRun(t *testing.T) {
	vault, engine, emb, cfg := setupTestVault(t)

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.SessionsImported != 3 {
		t.Errorf("SessionsImported = %d, want 3", result.SessionsImported)
	}

	// Verify nothing was actually written — marker file should not exist.
	markerFile := filepath.Join(vault.Root, "palace", "test-project", ".local", "imported-sessions.jsonl")
	if _, err := os.Stat(markerFile); !os.IsNotExist(err) {
		t.Errorf("marker file should not exist after dry run, got err: %v", err)
	}
}

// TestImportVibeVault_BadFrontmatter exercises the tolerant default
// path: a malformed-frontmatter file produces an ImportError, an
// auditable parse_failed marker, and the loop emits both ProgressError
// and ProgressSessionSkip events with the source file path so the
// operator can locate and repair the offender.
func TestImportVibeVault_BadFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "Projects", "bad-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(sessDir, "bad.md")
	if err := os.WriteFile(badPath, []byte(testSessionBadFrontmatter), 0o644); err != nil {
		t.Fatal(err)
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	var mu sync.Mutex
	var events []ProgressEvent
	opts := ImportOptions{
		Progress: func(evt ProgressEvent) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, evt)
		},
	}

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, opts)
	if err != nil {
		t.Fatalf("tolerant default should not return an error, got: %v", err)
	}

	if len(result.Errors) != 1 {
		t.Fatalf("len(result.Errors) = %d, want 1", len(result.Errors))
	}
	if got := result.Errors[0].File; got != badPath {
		t.Errorf("result.Errors[0].File = %q, want %q", got, badPath)
	}
	if result.SessionsImported != 0 {
		t.Errorf("SessionsImported = %d, want 0", result.SessionsImported)
	}
	if result.SessionsSkipped != 1 {
		t.Errorf("SessionsSkipped = %d, want 1 (parse-failed counts as skipped)", result.SessionsSkipped)
	}

	mu.Lock()
	defer mu.Unlock()

	var errEvents, skipEvents []ProgressEvent
	for _, e := range events {
		switch e.Type {
		case ProgressError:
			errEvents = append(errEvents, e)
		case ProgressSessionSkip:
			skipEvents = append(skipEvents, e)
		}
	}
	if len(errEvents) != 1 {
		t.Fatalf("ProgressError event count = %d, want 1", len(errEvents))
	}
	if errEvents[0].File != badPath {
		t.Errorf("ProgressError.File = %q, want %q", errEvents[0].File, badPath)
	}
	if errEvents[0].Message == "" {
		t.Error("ProgressError.Message should carry the parse-error string")
	}
	if len(skipEvents) != 1 {
		t.Fatalf("ProgressSessionSkip event count = %d, want 1", len(skipEvents))
	}
	if skipEvents[0].File != badPath {
		t.Errorf("ProgressSessionSkip.File = %q, want %q", skipEvents[0].File, badPath)
	}

	// Marker file must contain a parse_failed entry for the failed session.
	markerFile := filepath.Join(vault.Root, "palace", "bad-project", ".local", "imported-sessions.jsonl")
	mb, mErr := os.ReadFile(markerFile)
	if mErr != nil {
		t.Fatalf("expected parse_failed marker at %s: %v", markerFile, mErr)
	}
	if !strings.Contains(string(mb), `"reason":"parse_failed"`) {
		t.Errorf("marker file should contain a parse_failed entry, got:\n%s", string(mb))
	}
}

// TestImportVibeVault_BadFrontmatter_StrictAborts proves that --strict
// halts on the first parse error with a wrapped error that names the
// offending file, and writes no marker file.
func TestImportVibeVault_BadFrontmatter_StrictAborts(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "Projects", "bad-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(sessDir, "bad.md")
	if err := os.WriteFile(badPath, []byte(testSessionBadFrontmatter), 0o644); err != nil {
		t.Fatal(err)
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	_, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{Strict: true})
	if err == nil {
		t.Fatal("strict mode should return an error on parse failure")
	}
	if !strings.Contains(err.Error(), badPath) {
		t.Errorf("strict-mode error should name the offending file; got: %v", err)
	}

	markerFile := filepath.Join(vault.Root, "palace", "bad-project", ".local", "imported-sessions.jsonl")
	if _, statErr := os.Stat(markerFile); !os.IsNotExist(statErr) {
		t.Errorf("strict mode should not write a marker file, stat err=%v", statErr)
	}
}

// TestImportVibeVault_BadFrontmatter_RepairAfterParseFail proves the
// retry-after-repair contract: a parse_failed marker does NOT block a
// subsequent successful import once the operator has fixed the source
// file.
func TestImportVibeVault_BadFrontmatter_RepairAfterParseFail(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "Projects", "bad-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(sessDir, "bad.md")
	if err := os.WriteFile(badPath, []byte(testSessionBadFrontmatter), 0o644); err != nil {
		t.Fatal(err)
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	// First run: parse fails, parse_failed marker written.
	r1, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("first run: unexpected error: %v", err)
	}
	if r1.SessionsImported != 0 || r1.SessionsSkipped != 1 {
		t.Fatalf("first run: imported=%d skipped=%d, want 0/1", r1.SessionsImported, r1.SessionsSkipped)
	}
	// The session ID is derived from the filename when parse fails.
	failedID := "bad"
	if imp, _ := isSessionImported(vault, "bad-project", failedID); imp {
		t.Error("parse_failed entry should not count as imported")
	}

	// Repair: write a valid frontmatter at the same path so the
	// filename-derived session ID lines up with the marker entry.
	repaired := strings.Replace(validSessionTemplate, "%s", failedID, 1)
	if err := os.WriteFile(badPath, []byte(repaired), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second run: session imports cleanly.
	r2, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("second run: unexpected error: %v", err)
	}
	if r2.SessionsImported != 1 {
		t.Errorf("second run: SessionsImported = %d, want 1", r2.SessionsImported)
	}
	if imp, _ := isSessionImported(vault, "bad-project", failedID); !imp {
		t.Error("session should be marked as imported after repair")
	}
}

func TestImportVibeVault_EmptyTranscript(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "Projects", "empty-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sessDir, "empty.md"),
		[]byte(testSessionEmptyBody),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not crash — session with empty body and empty summary.
	if result.SessionsImported != 1 {
		t.Errorf("SessionsImported = %d, want 1", result.SessionsImported)
	}
}

func TestImportVibeVault_SlugMapping(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "Projects", "00_Test Project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sessDir, "session.md"),
		[]byte(testSession1),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ProjectsScanned != 1 {
		t.Fatalf("ProjectsScanned = %d, want 1", result.ProjectsScanned)
	}
	if result.SessionsImported != 1 {
		t.Errorf("SessionsImported = %d, want 1", result.SessionsImported)
	}

	// Verify the marker was written under the slugified name.
	markerFile := filepath.Join(vault.Root, "palace", "00-test-project", ".local", "imported-sessions.jsonl")
	if _, err := os.Stat(markerFile); err != nil {
		t.Errorf("expected marker file at slugified path: %v", err)
	}
}

func TestImportVibeVault_SlugCollision(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two directories that will collide when slugified.
	for _, name := range []string{"Test_Project", "Test Project"} {
		sessDir := filepath.Join(tmpDir, "Projects", name, "sessions")
		if err := os.MkdirAll(sessDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	// Default resolver (AutoResolver) should auto-rename the later-sorted dir,
	// not fatally abort.
	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error with default resolver: %v", err)
	}
	if result.ProjectsScanned != 2 {
		t.Errorf("ProjectsScanned = %d, want 2", result.ProjectsScanned)
	}
	if len(result.SlugRemap) != 1 {
		t.Errorf("SlugRemap = %v, want 1 entry", result.SlugRemap)
	}
	if got, want := result.SlugRemap["test-project"], "test-project-vp"; got != want {
		t.Errorf("SlugRemap[test-project] = %q, want %q", got, want)
	}
}

func TestImportVibeVault_Progress(t *testing.T) {
	vault, engine, emb, cfg := setupTestVault(t)

	var mu sync.Mutex
	var events []ProgressEvent

	opts := ImportOptions{
		Progress: func(evt ProgressEvent) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, evt)
		},
	}

	_, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Check that we received expected event types.
	types := make(map[ProgressType]int)
	for _, e := range events {
		types[e.Type]++
	}

	if types[ProgressProjectStart] < 1 {
		t.Error("expected at least one ProgressProjectStart event")
	}
	if types[ProgressSessionDone] < 3 {
		t.Errorf("expected at least 3 ProgressSessionDone events, got %d", types[ProgressSessionDone])
	}
	if types[ProgressProjectDone] < 1 {
		t.Error("expected at least one ProgressProjectDone event")
	}
}

func TestImportVibeVault_CancelledContext(t *testing.T) {
	vault, engine, emb, cfg := setupTestVault(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := ImportVibeVault(ctx, vault, vault, engine, emb, cfg, ImportOptions{})
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestImportVibeVault_KnowledgeMD(t *testing.T) {
	vault, engine, emb, cfg := setupTestVault(t)

	// Write a knowledge.md file in the project directory.
	knowledgePath := filepath.Join(vault.Root, "Projects", "test-project", "knowledge.md")
	if err := os.WriteFile(knowledgePath, []byte("# Project Knowledge\n\nImportant domain facts."), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All 3 sessions should import; knowledge.md is a bonus, not counted as a session.
	if result.SessionsImported != 3 {
		t.Errorf("SessionsImported = %d, want 3", result.SessionsImported)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}

func TestImportVibeVault_MissingSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "Projects", "noid-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Session file with empty session_id — should fall back to filename.
	content := `---
session_id: ""
project: noid-project
date: "2026-04-01"
title: "No ID Session"
summary: "This session has no session_id"
tag: exploration
---
## Transcript

Content without a session ID.
`
	if err := os.WriteFile(filepath.Join(sessDir, "fallback-name.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SessionsImported != 1 {
		t.Errorf("SessionsImported = %d, want 1", result.SessionsImported)
	}

	// Verify the marker was written using the filename fallback.
	imported, checkErr := isSessionImported(vault, "noid-project", "fallback-name")
	if checkErr != nil {
		t.Fatalf("isSessionImported: %v", checkErr)
	}
	if !imported {
		t.Error("session should be marked as imported under filename fallback ID")
	}
}

func TestImportVibeVault_NoProjectsDir(t *testing.T) {
	tmpDir := t.TempDir()
	// Do NOT create a Projects/ directory.
	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	_, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err == nil {
		t.Fatal("expected error for missing Projects dir, got nil")
	}
	if !strings.Contains(err.Error(), "scan projects") {
		t.Errorf("error should mention scan projects, got: %v", err)
	}
}

func TestImportVibeVault_EmptySlugSkip(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a project dir that slugifies to empty string.
	emptySlugDir := filepath.Join(tmpDir, "Projects", "---")
	if err := os.MkdirAll(emptySlugDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Also create a valid project so we can verify normal processing still works.
	sessDir := filepath.Join(tmpDir, "Projects", "valid-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "s1.md"), []byte(testSession1), 0o644); err != nil {
		t.Fatal(err)
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only the valid project should be scanned; "---" should be skipped.
	if result.ProjectsScanned != 1 {
		t.Errorf("ProjectsScanned = %d, want 1", result.ProjectsScanned)
	}
	if result.SessionsImported != 1 {
		t.Errorf("SessionsImported = %d, want 1", result.SessionsImported)
	}
}

func TestImportVibeVault_UnreadableFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "Projects", "unreadable-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a session file, then make it unreadable.
	unreadable := filepath.Join(sessDir, "noperm.md")
	if err := os.WriteFile(unreadable, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Errors) == 0 {
		t.Error("expected at least one error for unreadable file")
	}
	if result.SessionsImported != 0 {
		t.Errorf("SessionsImported = %d, want 0", result.SessionsImported)
	}
}

// TestImportVibeVault_ScaffoldsProjectAndWritesNoVaultConfig asserts that
// migrate initialises the destination project with the init scaffold
// (Projects/<slug>/{commands,skills}/README.md) and writes no
// Projects/<slug>/config.toml. The retired vault-project reconciler used to
// write that file, and tasks/{done,cancelled} beside it; the task archive
// directories are now created on first use by the task mover.
func TestImportVibeVault_ScaffoldsProjectAndWritesNoVaultConfig(t *testing.T) {
	vault, engine, emb, cfg := setupTestVault(t)

	_, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	proj := filepath.Join(vault.Root, "Projects", "test-project")
	if _, err := os.Lstat(filepath.Join(proj, "config.toml")); !os.IsNotExist(err) {
		t.Errorf("migrate created Projects/test-project/config.toml (lstat err: %v); nothing may write the retired vault config", err)
	}
	for _, rel := range []string{"commands/README.md", "skills/README.md"} {
		if _, err := os.Stat(filepath.Join(proj, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected %s scaffolded by migrate: %v", rel, err)
		}
	}
}

// TestImportVibeVault_ScaffoldIdempotent verifies that re-running migrate over
// an already-scaffolded project does not error and leaves the scaffold
// byte-identical.
func TestImportVibeVault_ScaffoldIdempotent(t *testing.T) {
	vault, engine, emb, cfg := setupTestVault(t)
	ctx := context.Background()

	if _, err := ImportVibeVault(ctx, vault, vault, engine, emb, cfg, ImportOptions{}); err != nil {
		t.Fatalf("first import: %v", err)
	}

	readme := filepath.Join(vault.Root, "Projects", "test-project", "commands", "README.md")
	before, err := os.ReadFile(readme)
	if err != nil {
		t.Fatalf("read scaffold README: %v", err)
	}

	if _, err := ImportVibeVault(ctx, vault, vault, engine, emb, cfg, ImportOptions{}); err != nil {
		t.Fatalf("second import: %v", err)
	}

	after, err := os.ReadFile(readme)
	if err != nil {
		t.Fatalf("read scaffold README after second run: %v", err)
	}
	if string(before) != string(after) {
		t.Error("scaffold README changed between idempotent migrate runs")
	}
}

// TestImportVibeVault_DryRunSkipsScaffold verifies dry-run does not scaffold
// the destination project.
func TestImportVibeVault_DryRunSkipsScaffold(t *testing.T) {
	vault, engine, emb, cfg := setupTestVault(t)

	_, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, rel := range []string{"config.toml", "commands/README.md", "skills/README.md"} {
		p := filepath.Join(vault.Root, "Projects", "test-project", filepath.FromSlash(rel))
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should not exist after dry-run, stat err=%v", rel, err)
		}
	}
}

func TestImportVibeVault_ContentDedup(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "Projects", "dedup-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Two sessions with identical body text but different IDs.
	body := `---
session_id: "%s"
project: dedup-project
date: "2026-04-01"
title: "Dedup Session"
summary: "Same content"
---
## Transcript

Identical body text for deduplication testing purposes.
`
	for _, id := range []string{"dedup-01", "dedup-02"} {
		content := strings.Replace(body, "%s", id, 1)
		if err := os.WriteFile(
			filepath.Join(sessDir, id+".md"),
			[]byte(content),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}

	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{}
	engine := search.NewEngine(emb, vault, cfg)

	result, err := ImportVibeVault(context.Background(), vault, vault, engine, emb, cfg, ImportOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.SessionsImported != 2 {
		t.Errorf("SessionsImported = %d, want 2 (both should import)", result.SessionsImported)
	}

	// Both should have idempotency markers.
	for _, id := range []string{"dedup-01", "dedup-02"} {
		imported, err := isSessionImported(vault, "dedup-project", id)
		if err != nil {
			t.Errorf("isSessionImported(%q): %v", id, err)
		}
		if !imported {
			t.Errorf("session %q should be marked as imported", id)
		}
	}
}

// TestImportVibeVault_DryRunNilEngineAndEmbedder pins the contract the CLI
// relies on: a dry run takes nil for both, never reaches the indexer (not for
// sessions, not for knowledge.md), and still counts every session.
func TestImportVibeVault_DryRunNilEngineAndEmbedder(t *testing.T) {
	vault, _, _, cfg := setupTestVault(t)
	knowledgePath := filepath.Join(vault.Root, "Projects", "test-project", "knowledge.md")
	if err := os.WriteFile(knowledgePath, []byte("# Project Knowledge\n\nImportant domain facts."), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ImportVibeVault(context.Background(), vault, vault, nil, nil, cfg, ImportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run with nil engine/embedder: %v", err)
	}
	if result.SessionsImported != 3 {
		t.Errorf("SessionsImported = %d, want 3", result.SessionsImported)
	}
	if result.DrawersCreated != 0 || result.EntitiesCreated != 0 || result.TriplesCreated != 0 {
		t.Errorf("dry run indexed: drawers/entities/triples = %d/%d/%d, want 0/0/0",
			result.DrawersCreated, result.EntitiesCreated, result.TriplesCreated)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}
