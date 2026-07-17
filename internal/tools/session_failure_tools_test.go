// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// seedMismatchedArchive writes a REAL claude-code archive and returns its session
// id, so a capture that asks to resolve it as some OTHER adapter loses the archive
// link for real.
//
// These three tests used to synthesize their loss with a session id no archive
// existed for. That stopped being a loss at 210: a missing archive is DEFERRED,
// not lost — the hook does not archive until SessionEnd, so at capture time the
// archive ordinarily does not exist yet, and hook.go closes the loop later. Had
// these tests kept passing on that input they would have been asserting a warning
// the product no longer owes anyone.
//
// The ADAPTER MISMATCH is still a true, loud loss, and it is the one that matters:
// it is how a Zed transcript goes missing while capture reports success. So the
// contracts below (fail hard, retry idempotently, claim on the error path) are now
// driven by the loss that actually still exists.
func seedMismatchedArchive(t *testing.T, vault *storage.Vault) string {
	t.Helper()
	const sessionID = "adapter-mismatch-session"
	srcPath := filepath.Join(t.TempDir(), "src.jsonl")
	if err := os.WriteFile(srcPath, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Create(archive.CreateOptions{
		Adapter:     archive.ClaudeCodeAdapterName,
		SessionID:   sessionID,
		SourcePath:  srcPath,
		VaultRoot:   vault.Root,
		ProjectSlug: "test-proj",
	}); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	return sessionID
}

// TestCaptureSessionFailsHardOnLoss is the headline assertion of this whole task:
// vp_capture_session must NEVER report `status: ok` for work it did not do.
//
// The capture below resolves a real archive but demands the WRONG ADAPTER, so the
// archive link is genuinely lost. Before this, that returned
// {"status":"ok","note_path":...} — the loss logged a warning and the agent was
// told everything was fine. It must be an ERROR.
func TestCaptureSessionFailsHardOnLoss(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)
	sessionID := seedMismatchedArchive(t, vault)
	// The session id reaches the handler by server-side derivation only.
	stubHostSessionID(t, sessionID)

	params, _ := json.Marshal(map[string]any{
		"project":         "test-proj",
		"summary":         "a capture whose archive resolves as the wrong adapter",
		"archive_adapter": "zed",
	})

	result, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err == nil {
		t.Fatalf("capture LOST the archive link and returned success: %+v", result)
	}

	// The error must be machine-parseable, not prose: mcp turns a handler error
	// into a single text block and DISCARDS the result body, so the payload has to
	// ride inside the error string (the resumeConflictError precedent).
	msg := err.Error()
	brace := strings.Index(msg, "{")
	if brace < 0 {
		t.Fatalf("error carries no JSON payload, so a retry cannot be mechanical: %s", msg)
	}
	var payload struct {
		Captured   bool     `json:"captured"`
		NotePath   string   `json:"note_path"`
		SessionKey string   `json:"session_key"`
		Lost       []string `json:"lost"`
		Remedy     string   `json:"remedy"`
	}
	if uerr := json.Unmarshal([]byte(msg[brace:]), &payload); uerr != nil {
		t.Fatalf("error payload is not JSON: %v (%s)", uerr, msg)
	}

	// THE NOTE STILL LANDED, and the payload must say so — otherwise an agent
	// reading "error" concludes the session was lost and captures it again.
	if !payload.Captured {
		t.Error(`payload says captured=false, but the note WAS written`)
	}
	if payload.NotePath == "" {
		t.Fatal("payload names no note_path, so the agent cannot find the note that DID land")
	}
	abs := filepath.Join(vault.Root, filepath.FromSlash(payload.NotePath))
	if _, statErr := os.Stat(abs); statErr != nil {
		t.Errorf("payload's note_path does not exist: %v", statErr)
	}

	// And it must name the key, or the retry duplicates the note.
	if payload.SessionKey == "" {
		t.Error("payload carries no session_key — a retry cannot address the note that already exists")
	}
	if len(payload.Lost) == 0 {
		t.Error("payload does not say WHAT was lost")
	}
	if payload.Remedy == "" {
		t.Error("payload carries no remedy")
	}
}

// TestCaptureSessionRetryAfterFailureDoesNotDuplicate closes the loop the hard
// failure opens. Failing hard is only safe BECAUSE the retry is idempotent: an
// isError on a call that already wrote its note is an invitation to re-capture, and
// without a key that re-capture writes a SECOND note. This drives the exact sequence
// an agent would follow — fail, read the key out of the error, retry with it.
func TestCaptureSessionRetryAfterFailureDoesNotDuplicate(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)
	sessionID := seedMismatchedArchive(t, vault)

	stubHostSessionID(t, sessionID)

	firstParams, _ := json.Marshal(map[string]any{
		"project":         "test-proj",
		"summary":         "first attempt",
		"archive_adapter": "zed",
	})
	_, err := tool.Handler(context.Background(), json.RawMessage(firstParams))
	if err == nil {
		t.Fatal("expected the first capture to fail hard")
	}

	var payload struct {
		SessionKey string `json:"session_key"`
	}
	msg := err.Error()
	if uerr := json.Unmarshal([]byte(msg[strings.Index(msg, "{"):]), &payload); uerr != nil {
		t.Fatalf("parse payload: %v", uerr)
	}

	// The agent retries with the key the error handed it, and this time without
	// asking for the adapter that does not match.
	retryParams, _ := json.Marshal(map[string]any{
		"project":     "test-proj",
		"summary":     "retry, having dropped the bad adapter",
		"session_key": payload.SessionKey,
	})
	out, retryErr := tool.Handler(context.Background(), json.RawMessage(retryParams))
	if retryErr != nil {
		t.Fatalf("clean retry still failed: %v", retryErr)
	}
	res, ok := out.(captureSessionResult)
	if !ok {
		t.Fatalf("unexpected result type %T", out)
	}
	if !res.Updated {
		t.Error("the retry minted a NEW note instead of updating the one the failed call already wrote")
	}

	dir, derr := vault.SessionDir("test-proj")
	if derr != nil {
		t.Fatal(derr)
	}
	notes, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	if len(notes) != 1 {
		t.Fatalf("%d notes on disk, want 1 — failing hard produced a DUPLICATE session record", len(notes))
	}
}

// TestCaptureSessionClaimSurvivesTheErrorPath pins the ordering inside the handler.
//
// The claim sentinel is written BEFORE the hard failure is raised, and that is not
// incidental. The claim asserts "this session has a note", which is still true when
// a peripheral stage was lost. Raising the error first — the obvious-looking tidy —
// would leave every hard-failing capture unclaimed, so the SessionEnd hook would
// capture the same session AGAIN and duplicate the note, over a missing archive link.
func TestCaptureSessionClaimSurvivesTheErrorPath(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)
	cwd := t.TempDir()
	sessionID := seedMismatchedArchive(t, vault)

	stubHostSessionID(t, sessionID)

	params, _ := json.Marshal(map[string]any{
		"project": "test-proj",
		"summary": "a capture that loses its archive but must still claim",
		// A claim needs cwd AND a derivable session id (stubbed above).
		"cwd":             cwd,
		"archive_adapter": "zed",
	})

	if _, err := tool.Handler(context.Background(), json.RawMessage(params)); err == nil {
		t.Fatal("expected a hard failure on the mismatched archive adapter")
	}

	claimDir := filepath.Join(cwd, ".vibe-palace")
	if !hook.IsClaimed(claimDir, sessionID) {
		t.Fatal("no claim was written on the error path — the SessionEnd hook will now " +
			"re-capture this session and duplicate its note")
	}
}
