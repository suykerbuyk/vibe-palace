// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testEngine(t *testing.T) (*search.Engine, *storage.Vault) {
	t.Helper()
	v := testVault(t)
	cfg := storage.Config{
		SearchDefaultLimit: 10,
		BoostWing:          0.12,
		BoostHall:          0.24,
		BoostRoom:          0.34,
	}
	emb := embedder.NewMock(384)
	eng := search.NewEngine(emb, v, cfg)
	t.Cleanup(func() { eng.Close() })
	return eng, v
}

func TestRunSearchNoResults(t *testing.T) {
	eng, v := testEngine(t)
	// runSearch calls eng.Search directly, which now refuses an unknown
	// project before anything else; make "test-proj" a real (empty) member of
	// the vault so this pins the known-but-empty case runSearch's caller
	// (cmdSearch, via requireSearchProject) is meant to actually reach.
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", "test-proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "nonexistent query", "", "", 10, false, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "No results found") {
		t.Errorf("expected no results message: %s", buf.String())
	}
}

// TestRunSearchUnknownProjectExitCode proves runSearch's own error handling —
// not just its sole production caller, cmdSearch (which never reaches
// runSearch with an unknown project, since requireSearchProject refuses one
// first) — maps a *search.UnknownProjectError from eng.Search to ExitUser,
// not the generic ExitSystem every other eng.Search error gets. This is the
// defense-in-depth path: a future caller, a reordering, or a TOCTOU race
// between requireSearchProject's check and eng.Search would otherwise surface
// the wrong exit code for a user-caused error.
func TestRunSearchUnknownProjectExitCode(t *testing.T) {
	eng, _ := testEngine(t)
	var buf bytes.Buffer
	code := runSearch(eng, "nosuchproject", "query", "", "", 10, false, false, &buf)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); output: %s", code, cli.ExitUser, buf.String())
	}
}

func TestRunSearchWithResults(t *testing.T) {
	eng, v := testEngine(t)

	// Add content to the vault.
	v.AppendDrawer("test-proj", "code", "auth", storage.Drawer{
		Content:    "Authentication middleware handles JWT token validation and refresh",
		Hall:       "facts",
		SourceType: "session",
		SourceRef:  "session-2026-04-01-01",
		FiledAt:    "2026-04-01T10:00:00Z",
	})
	v.AppendDrawer("test-proj", "code", "database", storage.Drawer{
		Content:    "Database migration system uses versioned SQL files",
		Hall:       "facts",
		SourceType: "session",
		SourceRef:  "session-2026-04-01-02",
		FiledAt:    "2026-04-01T10:00:00Z",
	})

	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "authentication", "", "", 10, false, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	// With mock embedder, results are deterministic but non-semantic.
	// We should get results (both drawers will have some similarity).
	if strings.Contains(out, "No results found") {
		t.Error("expected some results from search")
	}
}

func TestRunSearchJSON(t *testing.T) {
	eng, v := testEngine(t)

	v.AppendDrawer("test-proj", "code", "api", storage.Drawer{
		Content:    "REST API endpoint for user management",
		Hall:       "facts",
		SourceType: "session",
		FiledAt:    "2026-04-01T10:00:00Z",
	})

	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "api endpoint", "", "", 10, false, true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var results []search.SearchResult
	if err := json.Unmarshal(buf.Bytes(), &results); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Should have at least one result.
	if len(results) == 0 {
		t.Error("expected at least one result in JSON")
	}
}

func TestRunSearchLimit(t *testing.T) {
	eng, v := testEngine(t)

	// Add several drawers.
	for range 5 {
		v.AppendDrawer("test-proj", "code", "misc", storage.Drawer{
			Content:    "Some content for search testing purposes",
			Hall:       "facts",
			SourceType: "session",
			FiledAt:    "2026-04-01T10:00:00Z",
		})
	}

	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "content testing", "", "", 2, false, true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var results []search.SearchResult
	json.Unmarshal(buf.Bytes(), &results)
	if len(results) > 2 {
		t.Errorf("expected max 2 results, got %d", len(results))
	}
}

func TestRunSearchWingFilter(t *testing.T) {
	eng, v := testEngine(t)

	v.AppendDrawer("test-proj", "code", "api", storage.Drawer{
		Content: "Code API content", Hall: "facts", SourceType: "session",
		FiledAt: "2026-04-01T10:00:00Z",
	})
	v.AppendDrawer("test-proj", "docs", "api", storage.Drawer{
		Content: "Docs API content", Hall: "facts", SourceType: "session",
		FiledAt: "2026-04-01T10:00:00Z",
	})

	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "api content", "code", "", 10, false, true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var results []search.SearchResult
	json.Unmarshal(buf.Bytes(), &results)
	for _, r := range results {
		if r.Wing != "code" {
			t.Errorf("expected wing=code, got %q", r.Wing)
		}
	}
}

// TestRunSearchRawFlag is this plan's full-stack integration proof for the
// --raw flag: a real iterations.md written to disk, read through
// collectIterationCorpus, indexed by Engine.Rebuild, filtered by
// searchReady's IncludeRaw suppression, threaded through runSearch's
// includeRaw parameter (which fv.Bool("--raw") ultimately supplies from
// cmdSearch), and rendered as JSON on stdout — proving the whole path end to
// end, not just that a bool field parses.
//
// runSearch calls eng.Rebuild itself on every invocation, so calling it twice
// against the same *search.Engine correctly picks up the on-disk state
// (including the cached iteration summary written before either call).
func TestRunSearchRawFlag(t *testing.T) {
	eng, v := testEngine(t)
	const project = "raw-flag-e2e"
	const rawMarker = "PLATYPUS_RAW_FLAG_E2E_MARKER"

	// Write a real iterations.md by hand: this test file's package is `main`,
	// not `search`, so it cannot reuse the unexported writeIterationsMD helper
	// from internal/search's own tests.
	path, err := v.IterationsFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := "## Iteration 1 — first\n\nBody containing " + rawMarker + ".\n\n---\n"
	if err := os.WriteFile(path, []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := v.WriteIterationSummary(project, storage.IterationSummary{
		N:          1,
		MatchIndex: 0,
		Summary:    "A summary that never mentions the raw-only marker.",
	}); err != nil {
		t.Fatal(err)
	}

	// Default: --raw NOT passed (includeRaw=false). runSearch's own
	// eng.Rebuild call builds the index from the files written above.
	var buf bytes.Buffer
	code := runSearch(eng, project, rawMarker, "", "", 10, false, true, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d: %s", code, buf.String())
	}
	var results []search.SearchResult
	if err := json.Unmarshal(buf.Bytes(), &results); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, r := range results {
		// "iteration_raw" is internal/search's unexported
		// iterationRawSourceType constant, unreachable from package main;
		// the literal is used deliberately here.
		if r.SourceType == "iteration_raw" {
			t.Fatalf("expected raw row hidden by default, got it: %+v", r)
		}
	}

	// With --raw (includeRaw=true): raw row restored alongside the summary.
	buf.Reset()
	code = runSearch(eng, project, rawMarker, "", "", 10, true, true, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d: %s", code, buf.String())
	}
	results = nil
	if err := json.Unmarshal(buf.Bytes(), &results); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var sawRaw bool
	for _, r := range results {
		if r.SourceType == "iteration_raw" {
			sawRaw = true
		}
	}
	if !sawRaw {
		t.Errorf("expected --raw to restore the raw row; results=%+v", results)
	}
}

func TestSearchMissingQueryExitCode(t *testing.T) {
	cmd := cmdSearch()
	code := cmd.Run(nil)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestSearchBadFlags(t *testing.T) {
	cmd := cmdSearch()
	code := cmd.Run([]string{"--unknown-flag"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

// ── Project existence: checked before the model loads ──────────────────────
//
// Every test below runs under setupTestVaultEnv, so forbidVaultEmbedder is on,
// and each chdirs into a temp dir first: the package's own cwd is inside this
// repo, where detection resolves "vibe-palace".

// seedNotesOnlyProject creates Projects/<slug>/sessions/x.md with no palace
// store — a project search indexes from its notes alone.
func seedNotesOnlyProject(t *testing.T, vaultDir, slug string) {
	t.Helper()
	dir := filepath.Join(vaultDir, "Projects", slug, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("# A note\n\nSome notes."), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearchInvalidProjectSlugBuildsNoEmbedder(t *testing.T) {
	setupTestVaultEnv(t)
	t.Chdir(t.TempDir())
	var code int
	stderr := captureStderr(t, func() { code = cmdSearch().Run([]string{"hello", "-p", "../Bad Slug"}) })
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, "vp search: --project:") {
		t.Errorf("stderr lacks the slug refusal:\n%s", stderr)
	}
}

func TestSearchUnknownProjectBuildsNoEmbedder(t *testing.T) {
	setupTestVaultEnv(t)
	t.Chdir(t.TempDir())
	var code int
	stderr := captureStderr(t, func() { code = cmdSearch().Run([]string{"hello", "-p", "nosuchproject"}) })
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, `--project "nosuchproject": no such project`) {
		t.Errorf("stderr does not name the unknown project:\n%s", stderr)
	}
}

// TestSearchNotesOnlyProjectReachesEmbedder pins that the existence check
// admits exactly what search indexes: a project with notes and no palace store
// is a real search target, not an unknown one.
func TestSearchNotesOnlyProjectReachesEmbedder(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	t.Chdir(t.TempDir())
	seedNotesOnlyProject(t, vaultDir, "notesonly")
	constructed := stubVaultEmbedder(t, embedder.NewMock(384))
	var code int
	stderr := captureStderr(t, func() { code = cmdSearch().Run([]string{"hello", "-p", "notesonly", "--json"}) })
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK; stderr:\n%s", code, stderr)
	}
	if *constructed != 1 {
		t.Errorf("embedder constructed %d times, want 1", *constructed)
	}
}

// TestSearchUnknownDetectedProjectBuildsNoEmbedder: no -p, and the cwd names a
// project (by its basename) that the vault does not hold. The chdir is what
// makes this test mean anything — see the section comment.
func TestSearchUnknownDetectedProjectBuildsNoEmbedder(t *testing.T) {
	setupTestVaultEnv(t)
	dir := filepath.Join(t.TempDir(), "unknownrepo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var code int
	stderr := captureStderr(t, func() { code = cmdSearch().Run([]string{"hello"}) })
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
	}
	for _, want := range []string{`no project "unknownrepo"`, "detected from the current directory", "vp init", "--project"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

func TestSearchDetectedProjectReachesEmbedder(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	seedNotesOnlyProject(t, vaultDir, "notesonly")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".vibe-palace.toml"), []byte("[project]\nname = \"notesonly\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	constructed := stubVaultEmbedder(t, embedder.NewMock(384))
	var code int
	stderr := captureStderr(t, func() { code = cmdSearch().Run([]string{"hello", "--json"}) })
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK; stderr:\n%s", code, stderr)
	}
	if *constructed != 1 {
		t.Errorf("embedder constructed %d times, want 1", *constructed)
	}
}

// TestSearchUnreadableProjectsIsSystemError: when the vault's Projects/ exists
// but cannot be listed, "I could not look" is not "absent" — ExitSystem, not
// the unknown-project ExitUser.
func TestSearchUnreadableProjectsIsSystemError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not deny directory listing on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root lists a mode-000 directory")
	}
	vaultDir := setupTestVaultEnv(t)
	t.Chdir(t.TempDir())
	projects := filepath.Join(vaultDir, "Projects")
	if err := os.Mkdir(projects, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(projects, 0o755) })
	var code int
	stderr := captureStderr(t, func() { code = cmdSearch().Run([]string{"hello", "-p", "anyproject"}) })
	if code != cli.ExitSystem {
		t.Errorf("exit code = %d, want ExitSystem (%d); stderr:\n%s", code, cli.ExitSystem, stderr)
	}
}
