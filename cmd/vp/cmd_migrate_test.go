// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
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

func TestMigrateVibeVault_ForceRequiresAgentctx(t *testing.T) {
	// --force only applies to --agentctx; on its own it is a usage error,
	// rejected before any vault is opened.
	code := cmdMigrateVibeVault().Run([]string{"--force"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (--force without --agentctx)", code, cli.ExitUser)
	}
}

func TestMigrateVibeVault_NoSessionsRequiresAgentctx(t *testing.T) {
	// --no-sessions with nothing to copy is a usage error.
	code := cmdMigrateVibeVault().Run([]string{"--no-sessions"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (--no-sessions without --agentctx)", code, cli.ExitUser)
	}
}

func TestPrintAgentctxResult(t *testing.T) {
	// Zero-valued result is a no-op (no --agentctx run).
	printAgentctxResult(migrate.AgentctxResult{}, false)

	a := migrate.AgentctxResult{
		Copied:            3,
		Skipped:           2,
		Bytes:             4096,
		CopiedPaths:       []string{"Projects/p/resume.md", "Projects/p/iterations.md"},
		SkippedPaths:      []string{"Projects/p/workflow.md"},
		CrownJewelSkipped: []string{"Projects/p/workflow.md"},
	}
	// Real-run rendering (crown-jewel warning) and dry-run rendering
	// (planned/skipped path lists) must both run without panicking.
	printAgentctxResult(a, false)
	printAgentctxResult(a, true)
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
	// With a real cli.IsTerminal(os.Stdin) check (golang.org/x/term-backed,
	// not the old char-device check), a real /dev/null stdin deterministically
	// resolves to AutoResolver — no more "either is acceptable" hedging.
	oldStdin := os.Stdin
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	os.Stdin = devNull
	t.Cleanup(func() { os.Stdin = oldStdin; _ = devNull.Close() })

	dir := t.TempDir()
	r, err := buildSlugResolver(dir, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*migrate.AutoResolver); !ok {
		t.Errorf("expected AutoResolver with /dev/null stdin, got %T", r)
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
	// isStdinTTY was replaced by the shared cli.IsTerminal helper
	// (commands-upgrade-treats-dev-null-stdin-as-a-terminal). Just prove the
	// call site this file now uses doesn't panic.
	_ = cli.IsTerminal(os.Stdin)
}

// ── Model-free migrate tests ────────────────────────────────────────────────
//
// Every test below runs under setupTestVaultEnv, so forbidVaultEmbedder is on:
// a test that reaches the ONNX model fails, which is the "no construction"
// assertion none of them has to spell out. A test that needs an embedder
// substitutes a mock with stubVaultEmbedder and asserts its count.

// writeMemPalaceExportFile writes a MemPalace JSON export and returns its path.
func writeMemPalaceExportFile(t *testing.T, dir string, drawers, entities, triples []map[string]any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"exported_at": "2026-09-10T00:00:00Z",
		"drawers":     drawers,
		"entities":    entities,
		"triples":     triples,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "export.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	testExportDrawers = []map[string]any{
		{"id": "d1", "wing": "technical", "room": "go", "content": "Wrote a worker pool.", "filed_at": "2026-09-01T00:00:00Z"},
		{"id": "d2", "wing": "memory", "room": "", "content": "Remembered the garden.", "filed_at": "2026-09-02T00:00:00Z"},
	}
	testExportEntities = []map[string]any{{"id": "e1", "name": "Go", "type": "language"}}
	testExportTriples  = []map[string]any{{"subject": "go", "predicate": "used_in", "object": "pool", "confidence": 0.9}}
)

// seedVibeVaultSource creates Projects/p/sessions/s1.md (and knowledge.md)
// under root, the minimal vibevault source with one importable session.
func seedVibeVaultSource(t *testing.T, root string) {
	t.Helper()
	sessDir := filepath.Join(root, "Projects", "p", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	session := "---\nsession_id: \"s1\"\nproject: p\ndate: \"2026-09-01\"\ntitle: \"One\"\nsummary: \"A session\"\ntag: implementation\n---\n## Transcript\n\nImplemented the worker pool and its tests.\n"
	if err := os.WriteFile(filepath.Join(sessDir, "s1.md"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}
	knowledge := "# Knowledge\n\nThe pool is bounded."
	if err := os.WriteFile(filepath.Join(root, "Projects", "p", "knowledge.md"), []byte(knowledge), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withPipeStdin swaps os.Stdin for the read end of a closed pipe for the rest
// of the test: non-TTY, and EOF on read, so a prompt can never block.
func withPipeStdin(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		r.Close()
	})
}

func TestMigrateMemPalaceMissingExportBuildsNoEmbedder(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"dry run", []string{"--dry-run"}},
		{"real run", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupTestVaultEnv(t)
			args := append([]string{"--export-path", filepath.Join(t.TempDir(), "nope.json")}, tc.args...)
			var code int
			stderr := captureStderr(t, func() { code = cmdMigrateMemPalace().Run(args) })
			if code != cli.ExitUser {
				t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
			}
			if !strings.Contains(stderr, "read export file") {
				t.Errorf("stderr lacks %q:\n%s", "read export file", stderr)
			}
		})
	}
}

func TestMigrateMemPalaceDirectoryExportIsUserError(t *testing.T) {
	setupTestVaultEnv(t)
	var code int
	stderr := captureStderr(t, func() {
		code = cmdMigrateMemPalace().Run([]string{"--export-path", t.TempDir()})
	})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, "is a directory") {
		t.Errorf("stderr lacks %q:\n%s", "is a directory", stderr)
	}
}

func TestMigrateMemPalaceMalformedExportBuildsNoEmbedder(t *testing.T) {
	setupTestVaultEnv(t)
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := captureStderr(t, func() { code = cmdMigrateMemPalace().Run([]string{"--export-path", p}) })
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, "parse export JSON") {
		t.Errorf("stderr lacks %q:\n%s", "parse export JSON", stderr)
	}
}

// TestMigrateMemPalaceUnreadableExportIsSystemError pins the other side of
// the exit mapping: a file that exists but cannot be read is not a typo, so it
// stays ExitSystem.
func TestMigrateMemPalaceUnreadableExportIsSystemError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not deny reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	setupTestVaultEnv(t)
	p := writeMemPalaceExportFile(t, t.TempDir(), testExportDrawers, nil, nil)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0o644) })
	var code int
	stderr := captureStderr(t, func() { code = cmdMigrateMemPalace().Run([]string{"--export-path", p}) })
	if code != cli.ExitSystem {
		t.Errorf("exit code = %d, want ExitSystem (%d); stderr:\n%s", code, cli.ExitSystem, stderr)
	}
}

func TestMigrateMemPalaceDryRunBuildsNoEmbedder(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	p := writeMemPalaceExportFile(t, t.TempDir(), testExportDrawers, testExportEntities, testExportTriples)
	var code int
	stderr := captureStderr(t, func() {
		code = cmdMigrateMemPalace().Run([]string{"--export-path", p, "--dry-run"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{
		"the embedding model is not loaded",
		"Would import: 0 projects, 0 sessions imported, 0 skipped, 2 drawers, 1 entities, 1 triples",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "palace", "mempalace")); !os.IsNotExist(err) {
		t.Errorf("dry run wrote palace/mempalace (stat err %v)", err)
	}
}

func TestMigrateMemPalaceRealRunBuildsEmbedderOnce(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	constructed := stubVaultEmbedder(t, embedder.NewMock(384))
	p := writeMemPalaceExportFile(t, t.TempDir(), testExportDrawers, testExportEntities, testExportTriples)
	var code int
	stderr := captureStderr(t, func() { code = cmdMigrateMemPalace().Run([]string{"--export-path", p}) })
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK; stderr:\n%s", code, stderr)
	}
	if *constructed != 1 {
		t.Errorf("embedder constructed %d times, want 1", *constructed)
	}
	wings, err := storage.NewVault(vaultDir).ListWings("mempalace")
	if err != nil || len(wings) == 0 {
		t.Errorf("no drawers on disk after a real import: wings=%v err=%v", wings, err)
	}
}

// TestMigrateMemPalaceNoEmbeddableDrawersBuildsNoEmbedder: an export whose only
// drawer is blank has nothing to embed, so a REAL run still loads no model —
// entities and triples need no vectors.
func TestMigrateMemPalaceNoEmbeddableDrawersBuildsNoEmbedder(t *testing.T) {
	setupTestVaultEnv(t)
	constructed := stubVaultEmbedder(t, embedder.NewMock(384))
	blank := []map[string]any{{"id": "blank", "wing": "memory", "room": "x", "content": "   "}}
	p := writeMemPalaceExportFile(t, t.TempDir(), blank, testExportEntities, testExportTriples)
	var code int
	stderr := captureStderr(t, func() { code = cmdMigrateMemPalace().Run([]string{"--export-path", p}) })
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK; stderr:\n%s", code, stderr)
	}
	if *constructed != 0 {
		t.Errorf("embedder constructed %d times, want 0 (nothing to embed)", *constructed)
	}
	if want := "0 drawers, 1 entities, 1 triples"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
}

func TestMigrateVibeVaultDryRunBuildsNoEmbedder(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	seedVibeVaultSource(t, vaultDir)
	var code int
	stderr := captureStderr(t, func() { code = cmdMigrateVibeVault().Run([]string{"--dry-run"}) })
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{
		"the embedding model is not loaded",
		"    s1 [1/1]",
		"1 sessions imported",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	marker := filepath.Join(vaultDir, "palace", "p", ".local", "imported-sessions.jsonl")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("dry run wrote the import marker (stat err %v)", err)
	}
}

// TestMigrateVibeVaultMissingSourceBuildsNoEmbedder refuses a source with no
// Projects/ before anything else — including the cross-vault confirmation.
// The "prompt path" row is the one that pins that ORDER: neither --dry-run nor
// --yes, stdin a non-TTY pipe. Were the check below confirmCrossVaultWrite,
// that row would still exit ExitUser, but with the gate's "requires --yes"
// refusal — so only stderr tells the two orders apart.
func TestMigrateVibeVaultMissingSourceBuildsNoEmbedder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		pipedStdin bool
	}{
		{"dry run", []string{"--dry-run"}, false},
		{"real run with --yes", []string{"--yes"}, false},
		{"real run, prompt path", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupTestVaultEnv(t)
			if tc.pipedStdin {
				withPipeStdin(t)
			}
			args := append([]string{"--vault-path", filepath.Join(t.TempDir(), "nope")}, tc.args...)
			var code int
			stderr := captureStderr(t, func() { code = cmdMigrateVibeVault().Run(args) })
			if code != cli.ExitUser {
				t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
			}
			if !strings.Contains(stderr, "has no Projects/") || !strings.Contains(stderr, "(check --vault-path)") {
				t.Errorf("stderr lacks the missing-source refusal:\n%s", stderr)
			}
			if strings.Contains(stderr, "requires --yes") {
				t.Errorf("the confirmation gate ran before the source check:\n%s", stderr)
			}
		})
	}
}

func TestMigrateVibeVaultInPlaceEmptyVaultMessage(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	var code int
	stderr := captureStderr(t, func() { code = cmdMigrateVibeVault().Run([]string{"--dry-run"}) })
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
	}
	wantAbs, _ := expandAndAbsPath(vaultDir)
	if want := "configured vault " + wantAbs + " has no Projects/"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
	if strings.Contains(stderr, "--vault-path") {
		t.Errorf("an in-place run blamed --vault-path, which was never given:\n%s", stderr)
	}
}

// TestMigrateVibeVaultSourceProjectsIsFileIsUserError: a Projects that is a
// file, not a directory, is the same operator mistake as a missing one.
func TestMigrateVibeVaultSourceProjectsIsFileIsUserError(t *testing.T) {
	setupTestVaultEnv(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "Projects"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := captureStderr(t, func() {
		code = cmdMigrateVibeVault().Run([]string{"--vault-path", src, "--dry-run"})
	})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, "has no Projects/") {
		t.Errorf("stderr lacks the missing-source refusal:\n%s", stderr)
	}
}

// TestMigrateVibeVaultSourceStatFailureIsSystemError: a source whose Projects/
// cannot even be stat'd (here ENOTDIR: the --vault-path is a file) is not
// "absent", so it stays ExitSystem — the exit-1 mapping covers not-found and
// not-a-directory only.
func TestMigrateVibeVaultSourceStatFailureIsSystemError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stat through a regular file reports not-found on Windows")
	}
	setupTestVaultEnv(t)
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := captureStderr(t, func() {
		code = cmdMigrateVibeVault().Run([]string{"--vault-path", file, "--dry-run"})
	})
	if code != cli.ExitSystem {
		t.Errorf("exit code = %d, want ExitSystem (%d); stderr:\n%s", code, cli.ExitSystem, stderr)
	}
}

func TestMigrateVibeVaultRealRunBuildsEmbedderOnce(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	seedVibeVaultSource(t, vaultDir)
	constructed := stubVaultEmbedder(t, embedder.NewMock(384))
	var code int
	stderr := captureStderr(t, func() { code = cmdMigrateVibeVault().Run([]string{"--yes"}) })
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK; stderr:\n%s", code, stderr)
	}
	if *constructed != 1 {
		t.Errorf("embedder constructed %d times, want 1", *constructed)
	}
	if !strings.Contains(stderr, "1 sessions imported") {
		t.Errorf("stderr lacks %q:\n%s", "1 sessions imported", stderr)
	}
}

func TestMigrateVibeVaultNoSessionsBuildsNoEmbedder(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	seedVibeVaultSource(t, vaultDir)
	constructed := stubVaultEmbedder(t, embedder.NewMock(384))
	var code int
	stderr := captureStderr(t, func() {
		code = cmdMigrateVibeVault().Run([]string{"--agentctx", "--no-sessions", "--yes"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK; stderr:\n%s", code, stderr)
	}
	if *constructed != 0 {
		t.Errorf("embedder constructed %d times, want 0 (--no-sessions)", *constructed)
	}
}

// TestMigrateVibeVaultCrossVaultGateRefusesBeforeEmbedder: a real cross-vault
// run with a VALID source, no --yes and a non-TTY stdin is refused by the
// confirmation gate — before the model loads.
func TestMigrateVibeVaultCrossVaultGateRefusesBeforeEmbedder(t *testing.T) {
	setupTestVaultEnv(t)
	withPipeStdin(t)
	src := t.TempDir()
	seedVibeVaultSource(t, src)
	var code int
	stderr := captureStderr(t, func() { code = cmdMigrateVibeVault().Run([]string{"--vault-path", src}) })
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, "requires --yes") {
		t.Errorf("stderr lacks the gate's refusal:\n%s", stderr)
	}
}

// TestMigrateVibeVaultCrossVaultDryRunWarnsOrphanMarkers: a cross-vault dry
// run whose source carries idempotency markers the destination lacks warns,
// and still loads no model.
func TestMigrateVibeVaultCrossVaultDryRunWarnsOrphanMarkers(t *testing.T) {
	setupTestVaultEnv(t)
	src := t.TempDir()
	seedVibeVaultSource(t, src)
	markerDir := filepath.Join(src, "palace", "p", ".local")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "imported-sessions.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := captureStderr(t, func() {
		code = cmdMigrateVibeVault().Run([]string{"--vault-path", src, "--dry-run"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "WARNING: prior runs left idempotency markers") {
		t.Errorf("stderr lacks the orphan-marker warning:\n%s", stderr)
	}
}

func TestMigrateVibeVaultBadSlugMapIsUserError(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	seedVibeVaultSource(t, vaultDir)
	var code int
	stderr := captureStderr(t, func() {
		code = cmdMigrateVibeVault().Run([]string{"--dry-run", "--slug-map", "bogus"})
	})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); stderr:\n%s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, "--slug-map") {
		t.Errorf("stderr lacks the --slug-map refusal:\n%s", stderr)
	}
}
