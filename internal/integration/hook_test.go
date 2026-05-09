// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// fakeTranscript returns minimal Claude Code JSONL with the given session ID.
func fakeTranscript(t *testing.T, sessionID string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "transcript-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"permission-mode","permissionMode":"default","sessionId":"` + sessionID + `"}`,
		`{"type":"user","message":{"role":"user","content":"implement feature X"}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"Here is the implementation..."}}`,
	}
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	return f.Name()
}

// initGitRepo creates a git repo with a few commits so AutoSummary works.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	// Create a few commits.
	for i := 0; i < 3; i++ {
		f := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(f, []byte("v"+string(rune('0'+i))), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "add", "-A")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add: %s", out)
		}
		cmd = exec.Command("git", "commit", "-m", "commit "+string(rune('0'+i)))
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %s", out)
		}
	}
}

// prepareVaultProject creates the project directory structure needed by
// archive.Create and storage.WriteSession.
func prepareVaultProject(t *testing.T, vaultRoot, slug string) {
	t.Helper()
	for _, sub := range []string{"sessions", "transcripts"} {
		dir := filepath.Join(vaultRoot, "Projects", slug, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHookPipeline_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping hook pipeline integration test in short mode")
	}

	ctx := context.Background()
	vaultRoot := t.TempDir()
	cwd := t.TempDir()
	slug := "test-proj"

	// Setup: git repo + vault project dirs + transcript.
	initGitRepo(t, cwd)
	prepareVaultProject(t, vaultRoot, slug)
	transcript1 := fakeTranscript(t, "hook-e2e-session")

	claimDir := filepath.Join(cwd, ".vibe-palace")

	// ── First hook run ──────────────────────────────────────────────
	res, err := hook.Run(ctx, hook.Payload{
		SessionID:      "hook-e2e-session",
		TranscriptPath: transcript1,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, hook.RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: slug,
		VPVersion:   "0.1.0-test",
	})
	if err != nil {
		t.Fatalf("first hook.Run: %v", err)
	}
	if res.ClaimedSkip {
		t.Error("first run should not be a claimed skip")
	}
	if res.SessionNoteID == "" {
		t.Error("first run: SessionNoteID is empty")
	}
	if res.ArchivePath == "" {
		t.Error("first run: ArchivePath is empty")
	}
	if _, err := os.Stat(res.ArchivePath); err != nil {
		t.Errorf("archive file does not exist: %v", err)
	}

	// Verify session note in vault.
	vault := storage.NewVault(vaultRoot)
	sessions, err := vault.ListSessions(slug, "", "", 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Tag != "auto-capture" {
		t.Errorf("tag = %q, want %q", sessions[0].Tag, "auto-capture")
	}
	if sessions[0].FrictionScore < 0 {
		t.Errorf("friction_score = %d, want >= 0", sessions[0].FrictionScore)
	}

	// Verify claim sentinel.
	if !hook.IsClaimed(claimDir, "hook-e2e-session") {
		t.Error("claim sentinel not written after first run")
	}

	// ── Second hook run (idempotency) ───────────────────────────────
	res2, err := hook.Run(ctx, hook.Payload{
		SessionID:      "hook-e2e-session",
		TranscriptPath: transcript1,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, hook.RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: slug,
		VPVersion:   "0.1.0-test",
	})
	if err != nil {
		t.Fatalf("second hook.Run: %v", err)
	}
	if !res2.ClaimedSkip {
		t.Error("second run should be a claimed skip")
	}

	sessions2, err := vault.ListSessions(slug, "", "", 10)
	if err != nil {
		t.Fatalf("ListSessions after second run: %v", err)
	}
	if len(sessions2) != 1 {
		t.Errorf("expected 1 session after idempotent re-run, got %d", len(sessions2))
	}

	// ── Third hook run (different session) ──────────────────────────
	transcript2 := fakeTranscript(t, "hook-e2e-session-2")
	res3, err := hook.Run(ctx, hook.Payload{
		SessionID:      "hook-e2e-session-2",
		TranscriptPath: transcript2,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, hook.RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: slug,
		VPVersion:   "0.1.0-test",
	})
	if err != nil {
		t.Fatalf("third hook.Run: %v", err)
	}
	if res3.ClaimedSkip {
		t.Error("third run (new session) should not be a claimed skip")
	}

	sessions3, err := vault.ListSessions(slug, "", "", 10)
	if err != nil {
		t.Fatalf("ListSessions after third run: %v", err)
	}
	if len(sessions3) != 2 {
		t.Errorf("expected 2 sessions after new session, got %d", len(sessions3))
	}
}

func TestHookInstall_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping hook install integration test in short mode")
	}

	// Create a temp settings file with legacy vv hook entries.
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	initial := map[string]any{
		"hooks": map[string]any{
			"SessionEnd": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "vv hook",
							"timeout": 30,
						},
					},
				},
			},
		},
	}
	raw, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Install: should replace vv hook with vp hook.
	changed, err := hook.InstallAt(settingsPath)
	if err != nil {
		t.Fatalf("InstallAt: %v", err)
	}
	if !changed {
		t.Error("InstallAt should report changed=true")
	}

	// 2. Status: should be installed, legacy preserved (coexistence).
	status, err := hook.StatusAt(settingsPath)
	if err != nil {
		t.Fatalf("StatusAt: %v", err)
	}
	if !status.Installed {
		t.Error("expected Installed=true after install")
	}
	if !status.LegacyPresent {
		t.Error("expected LegacyPresent=true (vv hook preserved for coexistence)")
	}

	// 3. Uninstall: should remove vp hook.
	removed, err := hook.UninstallAt(settingsPath)
	if err != nil {
		t.Fatalf("UninstallAt: %v", err)
	}
	if !removed {
		t.Error("UninstallAt should report changed=true")
	}

	// 4. Status after uninstall: should not be installed.
	status2, err := hook.StatusAt(settingsPath)
	if err != nil {
		t.Fatalf("StatusAt after uninstall: %v", err)
	}
	if status2.Installed {
		t.Error("expected Installed=false after uninstall")
	}
}
