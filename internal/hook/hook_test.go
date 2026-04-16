// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeTranscript is a minimal Claude Code JSONL transcript.
const fakeTranscript = `{"type":"permission-mode","permissionMode":"default","sessionId":"test-session"}
{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"hi there"}}
`

func TestRun_HappyPath(t *testing.T) {
	vaultRoot := t.TempDir()
	cwd := t.TempDir()

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

	// Initialize git repo in cwd so AutoSummary works and archive.Create
	// can resolve gitHead.
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

func TestRun_ClaimedSkip(t *testing.T) {
	vaultRoot := t.TempDir()
	cwd := t.TempDir()
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
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Initialize git repo so AutoSummary produces output.
	initGitRepo(t, cwd, "init")

	// Use a non-existent transcript path to trigger archive failure.
	res, err := Run(context.Background(), Payload{
		SessionID:      "s-nofail",
		TranscriptPath: filepath.Join(t.TempDir(), "nonexistent.jsonl"),
		CWD:            cwd,
		HookEventName:  "Stop",
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
