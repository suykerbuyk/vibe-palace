// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

func TestIntegrationCodexSessionEndArchivesExactTranscriptAndLinksReceipt(t *testing.T) {
	env := setupFreshEnv(t)
	bin := buildVPBinary(t)
	runVP(t, bin, env, nil, "init", env.projectDir, "--name", "codexproj", "--vault-path", env.vaultPath, "--no-git")
	content := []byte("{\"codex_record\":\"kept exactly\"}\n")
	source := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(map[string]string{"session_id": "thr_integration", "cwd": env.projectDir, "transcript_path": source, "hook_event_name": "SessionEnd"})
	run := testinfra.RunCLI(t, env.iso.Environ(), env.projectDir, event, "codex", "hook", "session-end")
	if run.ExitCode != 0 || !strings.Contains(run.Stdout, "archived:") {
		t.Fatalf("SessionEnd exit=%d stdout=%s stderr=%s", run.ExitCode, run.Stdout, run.Stderr)
	}
	entries, err := archive.ListEntries(env.vaultPath, "codexproj")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries=%d", len(entries))
	}
	sum := sha256.Sum256(content)
	if entries[0].Manifest.SourceSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("source_sha256=%s want %x", entries[0].Manifest.SourceSHA256, sum)
	}
	if entries[0].Manifest.VaultRelSessionNote != "" {
		t.Fatalf("stub receipt back-link=%q", entries[0].Manifest.VaultRelSessionNote)
	}
	v := storage.NewVault(env.vaultPath)
	rows, _, err := v.ListSessions("codexproj", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ArchiveSessionID == "thr_integration" && row.Host == storage.HostCodex && row.HostSource == storage.HostSourceCodexHookEvent && row.Archive != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("linked Codex receipt not persisted: %+v", rows)
	}
}

func TestIntegrationCodexSessionEndOversizeTranscriptFailsWithoutReceipt(t *testing.T) {
	env := setupFreshEnv(t)
	bin := buildVPBinary(t)
	runVP(t, bin, env, nil, "init", env.projectDir, "--name", "codexproj", "--vault-path", env.vaultPath, "--no-git")
	content := make([]byte, archive.DefaultMaxArchiveBytes+1)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "large.jsonl")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(map[string]string{"session_id": "thr_large", "cwd": env.projectDir, "transcript_path": source, "hook_event_name": "SessionEnd"})
	run := testinfra.RunCLI(t, env.iso.Environ(), env.projectDir, event, "codex", "hook", "session-end")
	if run.ExitCode == 0 || strings.Contains(run.Stdout, "archived:") {
		t.Fatalf("large archive should fail honestly: exit=%d stdout=%s stderr=%s", run.ExitCode, run.Stdout, run.Stderr)
	}
	entries, err := archive.ListEntries(env.vaultPath, "codexproj")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed large source left manifest: %+v", entries)
	}
}

func TestIntegrationCodexUnavailableTranscriptReportsFailureAndWritesNoReceipt(t *testing.T) {
	env := setupFreshEnv(t)
	bin := buildVPBinary(t)
	runVP(t, bin, env, nil, "init", env.projectDir, "--name", "codexproj", "--vault-path", env.vaultPath, "--no-git")
	event, _ := json.Marshal(map[string]string{"session_id": "thr_missing", "cwd": env.projectDir, "transcript_path": filepath.Join(t.TempDir(), "missing"), "hook_event_name": "SessionEnd"})
	run := testinfra.RunCLI(t, env.iso.Environ(), env.projectDir, event, "codex", "hook", "session-end")
	if run.ExitCode == 0 || strings.Contains(run.Stdout, "archived:") || !strings.Contains(run.Stderr, "transcript unavailable") {
		t.Fatalf("missing transcript result exit=%d stdout=%s stderr=%s", run.ExitCode, run.Stdout, run.Stderr)
	}
	entries, err := archive.ListEntries(env.vaultPath, "codexproj")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("missing source left archive: %+v", entries)
	}
}
