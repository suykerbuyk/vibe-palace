// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// fakeTranscript is a minimal Claude Code JSONL transcript.
const fakeTranscript = `{"type":"permission-mode","permissionMode":"default","sessionId":"test-session"}
{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"hi there"}}
`

func TestRun_HappyPath(t *testing.T) {
	vaultRoot := t.TempDir()
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)

	// Create .vibe-palace dir for claim checks.
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write fake transcript.
	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}

	// Initialize git repo in cwd so archive.Create can resolve gitHead.
	initGitRepo(t, cwd, "initial commit")

	res, err := Run(context.Background(), Payload{
		SessionID:      "test-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.ClaimedSkip {
		t.Error("expected ClaimedSkip=false")
	}
	if res.ArchivePath == "" {
		t.Error("expected ArchivePath to be set")
	}
	if res.SessionNoteID == "" {
		t.Error("expected SessionNoteID to be set")
	}
	if res.Event != "SessionEnd" {
		t.Errorf("expected event=SessionEnd, got %q", res.Event)
	}

	// Verify the session note was written in the vault.
	sessDir := filepath.Join(vaultRoot, "Projects", "test-project", "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("read sessions dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected at least one session file in vault")
	}
}

// TestRun_SessionEndWithNoPriorStopLinksInline is the F2 regression guard from
// capture-note-archive-link-never-closes: when SessionEnd arrives with NO prior
// Stop (no claim), archive.Create runs first and capture runs in the SAME hook
// invocation — so capture's inline ResolveEntry must link the fresh note to the
// archive that already exists. This case is saved only by that inline link (the
// SessionEnd LinkArchiveToSessions pass runs BEFORE capture writes the note), so
// a refactor that drops it would strand every short session silently.
func TestRun_SessionEndWithNoPriorStopLinksInline(t *testing.T) {
	vaultRoot := t.TempDir()
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "initial commit")

	res, err := Run(context.Background(), Payload{
		SessionID:      "test-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ArchivePath == "" {
		t.Fatal("no archive was created")
	}

	// The note must carry archive: → the manifest that exists.
	sessDir := filepath.Join(vaultRoot, "Projects", "test-project", "sessions")
	notes, err := filepath.Glob(filepath.Join(sessDir, "*.md"))
	if err != nil || len(notes) != 1 {
		t.Fatalf("want exactly one session note, got %v (err %v)", notes, err)
	}
	note, err := os.ReadFile(notes[0])
	if err != nil {
		t.Fatal(err)
	}
	head := string(note)
	if !strings.Contains(head, "\narchive: ") {
		t.Errorf("note carries no archive: link — the inline ResolveEntry link did not fire:\n%s", head)
	}
	if !strings.Contains(head, "\narchive_session_id: test-session") {
		t.Errorf("note carries no archive_session_id — the identity stamp is missing:\n%s", head)
	}

	// And the manifest must point back at the note.
	manifests, err := filepath.Glob(filepath.Join(
		vaultRoot, "Projects", "test-project", "transcripts", "*.manifest.json"))
	if err != nil || len(manifests) != 1 {
		t.Fatalf("want exactly one manifest, got %v (err %v)", manifests, err)
	}
	m, err := archive.ReadManifest(manifests[0])
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote == "" {
		t.Error("manifest vault_rel_session_note is empty — the back-link never closed")
	}
}

func TestRun_ClaimedSkip(t *testing.T) {
	vaultRoot := t.TempDir()
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write claim sentinel.
	sentinel := ClaimPath(claimDir, "claimed-session")
	if err := os.WriteFile(sentinel, []byte("claimed"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), Payload{
		SessionID:     "claimed-session",
		CWD:           cwd,
		HookEventName: "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.ClaimedSkip {
		t.Error("expected ClaimedSkip=true")
	}
}

func TestRun_InvalidEvent(t *testing.T) {
	_, err := Run(context.Background(), Payload{
		SessionID:     "s1",
		CWD:           "/tmp",
		HookEventName: "BadEvent",
	}, RunOptions{
		VaultRoot:   "/tmp",
		ProjectSlug: "p",
	})
	if err == nil {
		t.Fatal("expected error for invalid event")
	}
}

func TestRun_MissingSessionID(t *testing.T) {
	_, err := Run(context.Background(), Payload{
		CWD:           "/tmp",
		HookEventName: "Stop",
	}, RunOptions{
		VaultRoot:   "/tmp",
		ProjectSlug: "p",
	})
	if err == nil {
		t.Fatal("expected error for missing session_id")
	}
}

func TestRun_MissingCWD(t *testing.T) {
	_, err := Run(context.Background(), Payload{
		SessionID:     "s1",
		HookEventName: "Stop",
	}, RunOptions{
		VaultRoot:   "/tmp",
		ProjectSlug: "p",
	})
	if err == nil {
		t.Fatal("expected error for missing cwd")
	}
}

func TestRun_ArchiveFailureNonFatal(t *testing.T) {
	vaultRoot := t.TempDir()
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Initialize git repo so archive.Create can resolve gitHead.
	initGitRepo(t, cwd, "init")

	// Use a non-existent transcript path to trigger archive failure. SessionEnd
	// is one of the archiving events (archive no longer runs on Stop).
	res, err := Run(context.Background(), Payload{
		SessionID:      "s-nofail",
		TranscriptPath: filepath.Join(t.TempDir(), "nonexistent.jsonl"),
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run should not fail when archive fails: %v", err)
	}

	// Archive failed, but session was still captured.
	if res.ArchivePath != "" {
		t.Error("expected empty ArchivePath when archive fails")
	}
	if res.Error == "" {
		t.Error("expected Error field to be set when archive fails")
	}
	if res.SessionNoteID == "" {
		t.Error("expected session to be captured despite archive failure")
	}
}

// TestRun_EnrichmentEnabled drives the SessionEnd auto-capture path with
// [enrichment] enabled in the project config, pointing base_url at an httptest
// server returning a canned OpenAI-compatible completion. It asserts the
// captured note is LLM-enriched (summary replaced, fence + enriched_by).
func TestRun_EnrichmentEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"summary\":\"Auto-enriched summary.\",\"decisions\":[\"Picked the synchronous path\"],\"open_threads\":[\"Add drain telemetry\"],\"tag\":\"implementation\"}"}}]}`))
	}))
	defer srv.Close()

	t.Setenv("VP_TEST_HOOK_ENRICH_KEY", "sk-hook-test")

	vaultRoot := t.TempDir()
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Project config enabling enrichment against the test server.
	vault := storage.NewVault(vaultRoot)
	cfgPath, err := vault.ProjectConfigFile("test-project")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgBody := "[enrichment]\n" +
		"enabled = true\n" +
		"provider = \"openai\"\n" +
		"model = \"hook-enrich-model\"\n" +
		"api_key_env = \"VP_TEST_HOOK_ENRICH_KEY\"\n" +
		"base_url = \"" + srv.URL + "\"\n" +
		"max_tokens = 512\n" +
		"timeout_seconds = 10\n"
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}

	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "initial commit")

	res, err := Run(context.Background(), Payload{
		SessionID:      "enrich-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SessionNoteID == "" {
		t.Fatal("expected SessionNoteID set")
	}

	// Locate and read the captured note.
	sessDir := filepath.Join(vaultRoot, "Projects", "test-project", "sessions")
	matches, err := filepath.Glob(filepath.Join(sessDir, "*.md"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no session note written: matches=%v err=%v", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"Auto-enriched summary.",
		"<!-- enriched -->",
		"enriched by hook-enrich-model",
		"hook-enrich-model", // enriched_by frontmatter
	} {
		if !strings.Contains(body, want) {
			t.Errorf("enriched note missing %q\n---\n%s", want, body)
		}
	}
}

// newGitVault returns a fresh git-initialized temp dir usable as a vault root.
// Harvest commits the routed memory files, so the vault must be a git repo.
func newGitVault(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "-C", dir, "init", "-b", "main"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// fakeClaudeProject builds a fake Claude project dir layout under tmp:
// <tmp>/projects/<enc>/ holding a transcript.jsonl AND a memory/ subdir
// populated with the canonical native memory fixture. It returns the transcript
// path and the native memory dir.
func fakeClaudeProject(t *testing.T) (transcriptPath, nativeDir string) {
	t.Helper()
	projDir := filepath.Join(t.TempDir(), "projects", "-home-johns-code-vibe-palace")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath = filepath.Join(projDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	nativeDir = filepath.Join(projDir, "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatal(err)
	}
	return transcriptPath, nativeDir
}

func TestRun_SessionEndHarvests(t *testing.T) {
	vaultRoot := newGitVault(t)
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "init")

	transcriptPath, nativeDir := fakeClaudeProject(t)

	res, err := Run(context.Background(), Payload{
		SessionID:      "harvest-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "vibe-palace",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.MemoryHarvest == nil {
		t.Fatal("expected MemoryHarvest to be set on SessionEnd")
	}
	if !res.MemoryHarvest.Committed {
		t.Errorf("expected harvest Committed=true for git vault, got %+v", res.MemoryHarvest)
	}

	// The 3 typed memories should now live in the vault.
	vault := storage.NewVault(vaultRoot)
	metas, err := vault.ListMemories("vibe-palace", 0)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(metas) != 3 {
		t.Errorf("expected 3 routed memories, got %d", len(metas))
	}

	// The native memory dir must be drained (MEMORY.md + typed files gone).
	if n := countMD(t, nativeDir); n != 0 {
		t.Errorf("expected native memory dir drained, found %d *.md files", n)
	}
}

func TestRun_StopIsHarvestNoop(t *testing.T) {
	testHarvestNoop(t, "Stop")
}

func TestRun_PreCompactIsHarvestNoop(t *testing.T) {
	testHarvestNoop(t, "PreCompact")
}

// testHarvestNoop asserts that the given event neither harvests (MemoryHarvest
// nil) nor drains the native memory dir.
func testHarvestNoop(t *testing.T, event string) {
	t.Helper()
	vaultRoot := newGitVault(t)
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "init")

	transcriptPath, nativeDir := fakeClaudeProject(t)

	res, err := Run(context.Background(), Payload{
		SessionID:      "noop-session-" + event,
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  event,
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "vibe-palace",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MemoryHarvest != nil {
		t.Errorf("%s: expected MemoryHarvest=nil, got %+v", event, res.MemoryHarvest)
	}
	// 4 fixture files: MEMORY.md + 3 typed — all still present.
	if n := countMD(t, nativeDir); n != 4 {
		t.Errorf("%s: expected native memory dir untouched (4 *.md), found %d", event, n)
	}
}

// TestRun_StopDoesNotArchive guards the regression where archive ran on every
// Stop event (once per assistant turn), re-hashing + re-compressing the growing
// transcript and leaking a manifest .bak per turn. Archive must only run on the
// "preserve now" events (SessionEnd / PreCompact), never on Stop.
func TestRun_StopDoesNotArchive(t *testing.T) {
	vaultRoot := newGitVault(t)
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "init")
	transcriptPath, _ := fakeClaudeProject(t)

	res, err := Run(context.Background(), Payload{
		SessionID:      "stop-no-archive",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "Stop",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "vibe-palace",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ArchivePath != "" || res.ArchiveSkipped {
		t.Errorf("Stop must not archive: ArchivePath=%q ArchiveSkipped=%v", res.ArchivePath, res.ArchiveSkipped)
	}
}

// TestRun_StopAutoCaptureHonestAndUnscored is the end-to-end guard for
// auto-capture-notes-…: Stop must write the honest placeholder (never a git
// log of fix/revert subjects) and must not friction-score the transcript.
func TestRun_StopAutoCaptureHonestAndUnscored(t *testing.T) {
	vaultRoot := newGitVault(t)
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pin enrichment OFF at the project layer: LoadConfig merges the operator's
	// vault-level config (which may enable a live enricher), and this test
	// asserts the raw auto-capture write — not a post-hoc LLM rewrite.
	vault := storage.NewVault(vaultRoot)
	cfgPath, err := vault.ProjectConfigFile("test-project")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[enrichment]\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	initGitRepoMulti(t, cwd, []string{
		"fix revert wrong undo never mind",
		"failed error exception fatal",
	})

	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	rich := `{"type":"user","message":{"role":"user","content":"wrong undo revert never mind try again error failed"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"fix the failed path and revert"}}
`
	if err := os.WriteFile(transcriptPath, []byte(rich), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), Payload{
		SessionID:      "stop-honest-unscored",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "Stop",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SessionNoteID == "" {
		t.Fatal("expected a session note")
	}

	sessDir := filepath.Join(vaultRoot, "Projects", "test-project", "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("session notes = %d, want 1", len(entries))
	}
	body, err := os.ReadFile(filepath.Join(sessDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "tag: auto-capture") {
		t.Errorf("note missing tag: auto-capture:\n%s", text)
	}
	if !strings.Contains(text, autoSummaryHonest) {
		t.Errorf("note missing honest summary %q:\n%s", autoSummaryHonest, text)
	}
	for _, banned := range []string{"Recent:", "friction_score:", "friction_breakdown:", "enriched_by:"} {
		if strings.Contains(text, banned) {
			t.Errorf("note must not contain %q:\n%s", banned, text)
		}
	}
	for _, banned := range []string{"fix revert", "failed error"} {
		if strings.Contains(text, banned) {
			t.Errorf("note must not echo git subjects %q:\n%s", banned, text)
		}
	}
}

// TestRun_PreCompactArchives confirms PreCompact still archives (the
// pre-compaction snapshot), so the Stop exclusion did not over-narrow.
func TestRun_PreCompactArchives(t *testing.T) {
	vaultRoot := newGitVault(t)
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "init")
	transcriptPath, _ := fakeClaudeProject(t)

	res, err := Run(context.Background(), Payload{
		SessionID:      "precompact-archives",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "PreCompact",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "vibe-palace",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ArchivePath == "" {
		t.Errorf("PreCompact should archive: ArchivePath empty, Error=%q", res.Error)
	}
}

// countMD returns the number of *.md files in dir.
func countMD(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	return len(matches)
}

func TestRun_ClaimDecoupling_ArchiveAndHarvestRunWhenClaimed(t *testing.T) {
	vaultRoot := newGitVault(t)
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "init")

	transcriptPath, nativeDir := fakeClaudeProject(t)

	// Pre-write a claim sentinel: an MCP capture already claimed this session.
	if err := WriteClaim(claimDir, "claimed-decoupled", "prior"); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), Payload{
		SessionID:      "claimed-decoupled",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "vibe-palace",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !res.ClaimedSkip {
		t.Error("expected ClaimedSkip=true")
	}
	// Rich capture must be skipped when claimed.
	if res.SessionNoteID != "" {
		t.Errorf("expected capture skipped (empty SessionNoteID), got %q", res.SessionNoteID)
	}
	// But archive still ran (path set or attempted) — NOT the old early return.
	if res.ArchivePath == "" {
		t.Error("expected archive to run even when claimed (ArchivePath set)")
	}
	// And harvest still ran independent of the claim gate.
	if res.MemoryHarvest == nil {
		t.Error("expected MemoryHarvest set when claimed at SessionEnd")
	}
	if n := countMD(t, nativeDir); n != 0 {
		t.Errorf("expected native memory dir drained when claimed, found %d *.md", n)
	}
}

// TestRun_SkipsNonProjectCWD guards the regression where auto-capture misrouted
// into a stray Projects/<basename>/ scaffold and wrote a claim sentinel when the
// CWD was a non-project directory. The CWD may be a git repo (SignalGit), so the
// gate keys strictly on the .vibe-palace.toml opt-in marker (SignalVibeConfig):
// without it, Run must short-circuit before archive/claim — scaffolding nothing
// and claiming nothing.
func TestRun_SkipsNonProjectCWD(t *testing.T) {
	assertSkipped := func(t *testing.T, vaultRoot, cwd, claimDir string) {
		t.Helper()
		res, err := Run(context.Background(), Payload{
			SessionID:      "skip-session",
			TranscriptPath: "",
			CWD:            cwd,
			HookEventName:  "SessionEnd",
		}, RunOptions{
			VaultRoot:   vaultRoot,
			ProjectSlug: "test-project",
			ClaimDir:    claimDir,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !res.SkippedNoProject {
			t.Error("expected SkippedNoProject=true for non-project CWD")
		}
		if res.ArchivePath != "" {
			t.Errorf("expected empty ArchivePath, got %q", res.ArchivePath)
		}
		if res.SessionNoteID != "" {
			t.Errorf("expected empty SessionNoteID, got %q", res.SessionNoteID)
		}
		// No stray Projects/ scaffold under the vault root.
		if _, err := os.Stat(filepath.Join(vaultRoot, "Projects")); !os.IsNotExist(err) {
			t.Errorf("expected no Projects/ scaffold under vault root, stat err=%v", err)
		}
		// No claim sentinel written into the (non-project) claim dir.
		matches, err := filepath.Glob(filepath.Join(claimDir, "claimed-*"))
		if err != nil {
			t.Fatalf("glob claim dir: %v", err)
		}
		if len(matches) != 0 {
			t.Errorf("expected no claim sentinel, found %v", matches)
		}
	}

	// git repo, no marker — the general un-init'd code repo case.
	t.Run("git repo without marker", func(t *testing.T) {
		vaultRoot := t.TempDir()
		cwd := t.TempDir()
		initGitRepo(t, cwd, "init")
		assertSkipped(t, vaultRoot, cwd, filepath.Join(cwd, ".vibe-palace"))
	})

	// CWD == vault root, where the vault root is itself the git repo — the exact
	// self-referential scenario the bug misrouted into a stray scaffold.
	t.Run("cwd is vault root", func(t *testing.T) {
		vaultRoot := newGitVault(t)
		assertSkipped(t, vaultRoot, vaultRoot, filepath.Join(vaultRoot, ".vibe-palace"))
	})
}

// writeVibeMarker writes a minimal valid .vibe-palace.toml into dir so that
// project.DetectSignal(dir) returns SignalVibeConfig — the opt-in marker the
// hook capture gate requires. Distinct from cmd_init_test's markProjectDir,
// which writes a go.mod (SignalGoMod) that does NOT satisfy the gate.
func writeVibeMarker(t *testing.T, dir string) {
	t.Helper()
	marker := filepath.Join(dir, ".vibe-palace.toml")
	body := "[project]\nname = \"test-project\"\n"
	if err := os.WriteFile(marker, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initGitRepo creates a git repo with one commit in dir.
func initGitRepo(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", msg},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
}
