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
	// THE NO-MCP-NOTE CASE. No canonical note carries this session id, so the
	// receipt is the only candidate and the manifest must stay empty rather
	// than point back at a stub. Paired with
	// TestIntegrationCodexSessionEndBackLinksAPreWrittenMCPNote, which runs the
	// same binary against a vault that DOES hold one.
	if entries[0].Manifest.VaultRelSessionNote != "" {
		t.Fatalf("stub receipt back-link=%q", entries[0].Manifest.VaultRelSessionNote)
	}
	// vp_version and git_head, through the REAL binary. This is the only leg
	// that carries a real cli.BuildInfo, so it is the only one that proves the
	// registration in cmd/vp/commands.go actually passes it — a build that adds
	// the CreateOptions field but forgets the registration argument compiles and
	// writes "", which is what every Codex manifest before this change held.
	if entries[0].Manifest.VPVersion == "" {
		t.Errorf("manifest vp_version is empty: cli.BuildInfo never reached archive.Create")
	}
	// git_head is derived from the EVENT's cwd (SourceCWD), not the hook
	// process's. `vp init --no-git` leaves env.projectDir without a repo, so an
	// empty value is correct here; a NON-empty one could only have come from the
	// process cwd, which is the coincidental-correctness this unit removes.
	if got := entries[0].Manifest.GitHead; got != "" {
		t.Errorf("manifest git_head = %q, want empty: the event cwd is not a git repo", got)
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

// The paired case for the no-MCP-note assertion in
// TestIntegrationCodexSessionEndArchivesExactTranscriptAndLinksReceipt: with a
// canonical note already carrying this session's id, the REAL binary must
// close the loop and write the manifest's reverse link.
//
// Through testinfra.RunCLI rather than the library, because the whole point of
// this leg is that the compiled `vp codex hook session-end` does it — the
// install half of this task's parent shipped a defect whose unit tests all
// passed while the live path was broken.
func TestIntegrationCodexSessionEndBackLinksAPreWrittenMCPNote(t *testing.T) {
	env := setupFreshEnv(t)
	bin := buildVPBinary(t)
	runVP(t, bin, env, nil, "init", env.projectDir, "--name", "codexproj", "--vault-path", env.vaultPath, "--no-git")

	v := storage.NewVault(env.vaultPath)
	canonical, err := v.WriteSessionRef("codexproj", storage.SessionMeta{
		Date: "2026-11-02", ArchiveSessionID: "thr_joined",
		Host: storage.HostCodex, HostSource: storage.HostSourceDeclared,
		Tag: "implementation", Summary: "an MCP note that knows its host session",
	}, "mcp note body")
	if err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(source, []byte("{\"codex_record\":\"joined\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(map[string]string{"session_id": "thr_joined", "cwd": env.projectDir, "transcript_path": source, "hook_event_name": "SessionEnd"})
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
	if got := entries[0].Manifest.VaultRelSessionNote; got != canonical.NotePath {
		t.Fatalf("manifest back-link=%q, want the canonical note %q", got, canonical.NotePath)
	}
}
