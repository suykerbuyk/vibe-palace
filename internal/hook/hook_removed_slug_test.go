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

// TestRun_SkipsRemovedSlug is the unattended resurrection route. The hook's
// opt-in gate checks only that a marker EXISTS, and archive, harvest (with
// push) and capture all lazily create Projects/<slug>/. So a SessionEnd from a
// checkout whose marker still names a renamed-away project re-created it — the
// reason the old quantum-ng checkout had to be quarantined. Now the run is
// skipped before any of it, and nothing is lost: the native memory is neither
// harvested nor deleted.
func TestRun_SkipsRemovedSlug(t *testing.T) {
	vaultRoot := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", vaultRoot}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	old := filepath.Join(vaultRoot, "Projects", "test-project")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "resume.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", "seed"},
		{"mv", "Projects/test-project", "Projects/renamed-project"},
		{"commit", "-q", "-m", "migrate: test-project -> renamed-project (1/2 rename)"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", vaultRoot}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	cwd := t.TempDir()
	writeVibeMarker(t, cwd) // names "test-project": the stale checkout
	initGitRepo(t, cwd, "initial commit")

	// The native memory dir sits beside the transcript (NativeDirFromTranscript).
	hostDir := t.TempDir()
	transcriptPath := filepath.Join(hostDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(hostDir, "memory", "note.md")
	if err := os.MkdirAll(filepath.Dir(native), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("---\nname: note\ndescription: d\nmetadata:\n  type: project\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), Payload{
		SessionID:      "test-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    filepath.Join(cwd, ".vibe-palace"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.SkippedRemovedSlug {
		t.Error("SkippedRemovedSlug must be set for a marker naming a renamed-away project")
	}
	if res.ArchivePath != "" || res.SessionNoteID != "" || res.MemoryHarvest != nil {
		t.Errorf("nothing may run for a removed slug: archive=%q note=%q harvest=%v",
			res.ArchivePath, res.SessionNoteID, res.MemoryHarvest)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("Projects/test-project/ was resurrected")
	}
	if _, err := os.Stat(native); err != nil {
		t.Errorf("the native memory must be neither harvested nor deleted: %v", err)
	}
}
