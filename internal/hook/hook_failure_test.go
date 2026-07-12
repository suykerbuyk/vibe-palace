// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRun_ClaimWrittenDespitePeripheralLoss pins the claim sentinel to the
// SUCCESS path (DoD 2d).
//
// The claim asserts "this session has a note", and that stays true when a
// peripheral stage — here the archive, whose transcript path does not exist —
// was lost. Gating the claim on a clean run instead would mean the next hook
// event finds the session unclaimed and captures it AGAIN: a duplicate note per
// turn, forever, over a missing archive link. That is strictly worse than the
// silent `_ =` this work is replacing, which is why it gets its own test.
func TestRun_ClaimWrittenDespitePeripheralLoss(t *testing.T) {
	vaultRoot := t.TempDir()
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "initial commit")

	// A transcript path that does not exist: the archive fails, so this run
	// loses a peripheral stage while still writing its note.
	res, err := Run(context.Background(), Payload{
		SessionID:      "peripheral-loss-session",
		TranscriptPath: filepath.Join(t.TempDir(), "nonexistent.jsonl"),
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("a peripheral loss was raised as fatal: %v", err)
	}
	if res.SessionNoteID == "" {
		t.Fatal("no note was written")
	}

	// The loss must be visible...
	if res.Error == "" && len(res.Failures) == 0 {
		t.Error("the archive was lost but the result reports neither an error nor a failure")
	}

	// ...and the claim must nonetheless exist, or every later hook event
	// re-captures this session.
	if !IsClaimed(claimDir, "peripheral-loss-session") {
		t.Fatal("claim sentinel was NOT written after a peripheral loss — the next hook event will duplicate this note")
	}
}

// TestRun_CaptureFailureDoesNotError proves the hook never turns a capture loss
// into a run error — the error that cmd_hook would translate into exit 2.
//
// cli.ExitSystem is 2, and 2 is Claude Code's reserved BLOCKING-error code. The
// hook is installed on Stop, which fires once per assistant turn, so a
// deterministically-failing capture that exits 2 would block the first turn of
// every session and feed its own stderr back into the model: a loop. A capture
// failure is reported through the result and vp.log instead.
//
// The capture is forced to fail by making the vault's sessions directory
// unwritable — a plain FILE where the sessions/ directory must be — which is the
// one genuinely fatal error in the capture pipeline.
func TestRun_CaptureFailureDoesNotError(t *testing.T) {
	vaultRoot := t.TempDir()
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "initial commit")

	// Booby-trap the note's destination: Projects/<slug>/sessions is a FILE, so
	// EnsureDir — and therefore the vault write — cannot succeed.
	projDir := filepath.Join(vaultRoot, "Projects", "test-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "sessions"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), Payload{
		SessionID:     "doomed-capture-session",
		CWD:           cwd,
		HookEventName: "Stop",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
	})

	// THE ASSERTION. A capture that could not write its note must still return a
	// result and a nil error, so cmd_hook exits 0 rather than 2.
	if err != nil {
		t.Fatalf("capture failure surfaced as a run error — cmd_hook would exit 2 and BLOCK the Stop: %v", err)
	}
	if res == nil {
		t.Fatal("no result returned; cmd_hook has nothing to report")
	}
	if res.Error == "" {
		t.Error("the note was lost entirely and the result says nothing about it")
	}
	if res.SessionNoteID != "" {
		t.Errorf("SessionNoteID = %q, but no note was written", res.SessionNoteID)
	}

	// And no claim: there is no note, so the session has NOT been captured and a
	// later hook event must be free to try again.
	if IsClaimed(claimDir, "doomed-capture-session") {
		t.Error("a claim was written for a session that has no note — the capture is now unrecoverable")
	}
}
