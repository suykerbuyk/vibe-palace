// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// linkFixture stands up a project + transcript and returns the pieces the link
// tests drive. It seeds NOTHING that the assertions later check: every archive:
// field, archive_session_id and back-link under test is produced by the real
// Run() pipeline. That is deliberate. The note_path bug (198) survived six
// months because the only test touching it SEEDED THE VALUE ITSELF before
// asserting it round-tripped, and 202 found eight more fields in the same struct
// with the same fake coverage. A test that writes the answer it is grading is
// worse than no test: it reports green for a field the product never sets.
type linkFixture struct {
	vaultRoot  string
	cwd        string
	claimDir   string
	transcript string
	opts       RunOptions
}

func newLinkFixture(t *testing.T) linkFixture {
	t.Helper()
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

	return linkFixture{
		vaultRoot:  vaultRoot,
		cwd:        cwd,
		claimDir:   claimDir,
		transcript: transcriptPath,
		opts: RunOptions{
			VaultRoot:   vaultRoot,
			ProjectSlug: "test-project",
			VPVersion:   "test-0.1",
			ClaimDir:    claimDir,
		},
	}
}

func (f linkFixture) run(t *testing.T, sessionID, event string) *Result {
	t.Helper()
	res, err := Run(context.Background(), Payload{
		SessionID:      sessionID,
		TranscriptPath: f.transcript,
		CWD:            f.cwd,
		HookEventName:  event,
	}, f.opts)
	if err != nil {
		t.Fatalf("Run(%s): %v", event, err)
	}
	return res
}

// tagOfNote reports the tag of the note at a vault-relative path, for failure
// messages that need to name WHICH note a manifest wrongly pointed at.
func tagOfNote(notes []storage.SessionMeta, notePath string) string {
	for _, n := range notes {
		if n.NotePath == notePath {
			return n.Tag
		}
	}
	return "unknown"
}

// TestRun_StopThenSessionEnd_ClosesTheArchiveLink is the regression test for
// capture-note-archive-link-never-closes.
//
// It reproduces the EXACT ordering that stranded 105 of 417 manifests, which no
// existing test did: capture happens at the first Stop, when no archive exists
// yet, and the claim it writes then short-circuits capture forever — so the
// archive created at SessionEnd used to be linked to nothing at all.
//
// The assertions run against state the pipeline produced on its own.
func TestRun_StopThenSessionEnd_ClosesTheArchiveLink(t *testing.T) {
	f := newLinkFixture(t)
	const sessionID = "sess-stop-then-end"

	// --- The first Stop: capture with NO archive in existence. ---
	stop := f.run(t, sessionID, "Stop")
	if stop.SessionNoteID == "" {
		t.Fatal("Stop: expected a session note to be written")
	}
	if stop.ArchivePath != "" {
		t.Fatal("Stop must NOT archive: dedup keys on the transcript hash, which grows every turn")
	}

	vault := storage.NewVault(f.vaultRoot)
	notes, err := vault.ListSessions("test-project", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 note after Stop, got %d", len(notes))
	}

	// PRECONDITION — this is the bug's setup, and it must remain true: at Stop
	// there is no archive to point at, so the note is written unlinked. If this
	// ever starts passing, the test below is no longer proving anything.
	if notes[0].Archive != "" {
		t.Fatalf("precondition: note should be UNLINKED at Stop (no archive exists yet), got archive=%q", notes[0].Archive)
	}
	// ...but it must record WHICH SESSION it came from, or nothing can ever find
	// it again. This is the field whose absence was the entire bug.
	if notes[0].ArchiveSessionID != sessionID {
		t.Fatalf("note must record its host session id: got %q, want %q", notes[0].ArchiveSessionID, sessionID)
	}

	// --- SessionEnd: the archive is created, and the loop must close. ---
	end := f.run(t, sessionID, "SessionEnd")

	// The claim gate DID fire — capture did not run again. That is precisely why
	// the link has to be closed from the archive side, before the gate.
	if !end.ClaimedSkip {
		t.Fatal("expected the claim written at Stop to short-circuit capture at SessionEnd")
	}
	if end.LinkedNotes != 1 {
		t.Fatalf("expected 1 note linked at SessionEnd, got %d", end.LinkedNotes)
	}

	// Forward link: note -> transcript.
	linked, err := vault.ListSessions("test-project", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(linked) != 1 {
		t.Fatalf("expected still 1 note (the link must not mint a second), got %d", len(linked))
	}
	if linked[0].Archive == "" {
		t.Fatal("note's archive: field is still empty — the loop did not close")
	}
	if _, err := os.Stat(filepath.Join(f.vaultRoot, linked[0].Archive)); err != nil {
		t.Fatalf("note's archive: names a manifest that does not exist: %v", err)
	}

	// Back-link: transcript -> note. This is the half that made a transcript
	// unreachable from the archive side.
	entry, err := archive.ResolveEntry(f.vaultRoot, "test-project", sessionID)
	if err != nil {
		t.Fatalf("ResolveEntry: %v", err)
	}
	if entry.Manifest.VaultRelSessionNote == "" {
		t.Fatal("manifest's vault_rel_session_note is empty — the transcript cannot reach its note")
	}
	if got, want := entry.Manifest.VaultRelSessionNote, linked[0].NotePath; got != want {
		t.Fatalf("manifest points at %q, want the note at %q", got, want)
	}
}

// TestRun_SessionEnd_LinkIsIdempotent proves a re-run (a second SessionEnd, or a
// PreCompact followed by a SessionEnd) converges instead of thrashing: the note
// is already correct, so nothing is rewritten and no second note is minted.
func TestRun_SessionEnd_LinkIsIdempotent(t *testing.T) {
	f := newLinkFixture(t)
	const sessionID = "sess-idempotent"

	f.run(t, sessionID, "Stop")
	first := f.run(t, sessionID, "SessionEnd")
	if first.LinkedNotes != 1 {
		t.Fatalf("first SessionEnd: expected 1 note linked, got %d", first.LinkedNotes)
	}

	second := f.run(t, sessionID, "SessionEnd")
	if second.LinkedNotes != 0 {
		t.Fatalf("second SessionEnd: expected 0 notes rewritten (already linked), got %d", second.LinkedNotes)
	}

	vault := storage.NewVault(f.vaultRoot)
	notes, err := vault.ListSessions("test-project", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("re-running the link must not mint a note: got %d", len(notes))
	}
	if notes[0].Archive == "" {
		t.Fatal("re-run must not CLEAR the link it already made")
	}
}

// TestRun_SessionEnd_ManifestPointsAtTheWrapNote_NotTheStub encodes the operator's
// canonical-note decision (210).
//
// A session produces TWO notes: the hook's auto-capture stub (a git-log summary,
// written unattended at the first Stop) and the agent's wrap note (the decisions,
// the narrative — the thing a human following the link actually wants). The
// manifest has ONE back-link field. It must point at the wrap note.
//
// Today 43 of 48 linked manifests point at the stub, which is the wrong end of
// the only link a human ever follows.
func TestRun_SessionEnd_ManifestPointsAtTheWrapNote_NotTheStub(t *testing.T) {
	f := newLinkFixture(t)
	const sessionID = "sess-two-notes"

	// The hook's stub, written at the first Stop.
	f.run(t, sessionID, "Stop")

	// The agent's wrap note, mid-session, over the MCP path — same host session,
	// its own capture attempt. Note it passes ArchiveSessionID and NOT SessionKey:
	// reusing the host session id as the capture key would resolve the stub and
	// REWRITE IT IN PLACE, collapsing two notes into one.
	vault := storage.NewVault(f.vaultRoot)
	if _, err := capture.WriteSession(context.Background(), vault, nil, capture.SessionParams{
		Project:          "test-project",
		Summary:          "the real narrative of this session",
		Tag:              "implementation",
		ArchiveSessionID: sessionID,
		CWD:              f.cwd,
	}); err != nil {
		t.Fatalf("wrap capture: %v", err)
	}

	notes, err := vault.ListSessions("test-project", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes (stub + wrap), got %d — the wrap capture must not overwrite the stub", len(notes))
	}

	// SessionEnd: archive, then close the loop for BOTH notes.
	end := f.run(t, sessionID, "SessionEnd")
	if end.LinkedNotes != 2 {
		t.Fatalf("expected BOTH notes linked to the transcript, got %d", end.LinkedNotes)
	}

	// Every note of the session reaches the transcript...
	var wrapPath string
	linked, err := vault.ListSessions("test-project", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	for _, n := range linked {
		if n.Archive == "" {
			t.Errorf("note %s (tag=%s) is not linked to the transcript", n.ID, n.Tag)
		}
		if n.Tag == "implementation" {
			wrapPath = n.NotePath
		}
	}
	if wrapPath == "" {
		t.Fatal("could not find the wrap note")
	}

	// ...but the transcript points back at the WRAP note, not the stub.
	entry, err := archive.ResolveEntry(f.vaultRoot, "test-project", sessionID)
	if err != nil {
		t.Fatalf("ResolveEntry: %v", err)
	}
	if got := entry.Manifest.VaultRelSessionNote; got != wrapPath {
		t.Fatalf("manifest points at the %q note (%s); it must point at the wrap note (%s)",
			tagOfNote(linked, got), got, wrapPath)
	}
}

// TestRun_SessionEnd_ScoresWrapNoteNotStub is the ACP residual: Stop writes an
// auto-capture stub (never scored), MCP wrap lands with no transcript (never
// scored), SessionEnd archives and used to return at the claim gate without
// scoring anyone. Friction must land on the wrap from the complete transcript
// and must not land on the stub. A second SessionEnd must not rewrite.
func TestRun_SessionEnd_ScoresWrapNoteNotStub(t *testing.T) {
	f := newLinkFixture(t)
	const sessionID = "sess-friction-backfill"

	high := []byte(`{"type":"user","message":{"role":"user","content":"that is wrong"}}` + "\n")
	if err := os.WriteFile(f.transcript, high, 0o644); err != nil {
		t.Fatal(err)
	}

	if stop := f.run(t, sessionID, "Stop"); stop.SessionNoteID == "" {
		t.Fatal("Stop: expected a session note")
	}

	vault := storage.NewVault(f.vaultRoot)
	if _, err := capture.WriteSession(context.Background(), vault, nil, capture.SessionParams{
		Project:          "test-project",
		Summary:          "the wrap with no transcript",
		Tag:              "implementation",
		ArchiveSessionID: sessionID,
		CWD:              f.cwd,
	}); err != nil {
		t.Fatalf("wrap capture: %v", err)
	}

	end := f.run(t, sessionID, "SessionEnd")
	if !end.ClaimedSkip {
		t.Fatal("expected the Stop claim to skip recapture at SessionEnd")
	}
	if end.FrictionScored != 1 {
		t.Fatalf("FrictionScored=%d, want 1", end.FrictionScored)
	}

	notes, err := vault.ListSessions("test-project", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	var stub, wrap storage.SessionMeta
	for _, n := range notes {
		if n.Tag == "implementation" {
			wrap = n
			continue
		}
		stub = n
	}
	if stub.ID == "" || wrap.ID == "" {
		t.Fatalf("missing notes: stub=%q wrap=%q tags stub=%q wrap=%q", stub.ID, wrap.ID, stub.Tag, wrap.Tag)
	}
	if stub.Breakdown != nil {
		t.Fatal("auto-capture stub must stay never-scored")
	}
	if wrap.Breakdown == nil {
		t.Fatal("wrap note must carry a breakdown after SessionEnd")
	}
	if wrap.FrictionScore < 8 {
		t.Fatalf("wrap FrictionScore=%d, want >= 8 from 'wrong'", wrap.FrictionScore)
	}

	again := f.run(t, sessionID, "SessionEnd")
	if again.FrictionScored != 0 {
		t.Fatalf("second SessionEnd scored %d, want 0", again.FrictionScored)
	}
}
