// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// countNotes returns how many session notes exist for a project.
func countNotes(t *testing.T, vault *storage.Vault, project string) int {
	t.Helper()
	dir, err := vault.SessionDir(project)
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return len(matches)
}

func readNote(t *testing.T, vault *storage.Vault, relPath string) (storage.SessionMeta, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(vault.Root, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read note %s: %v", relPath, err)
	}
	meta, body, err := storage.ParseFrontmatter(data)
	if err != nil {
		t.Fatalf("parse note %s: %v", relPath, err)
	}
	return meta, body
}

// TestCaptureMintsAKeyAndReturnsIt pins the push-don't-derive contract: a caller
// that supplies no key gets one minted and HANDED BACK, which is the only way a
// retry can name the attempt it is retrying.
func TestCaptureMintsAKeyAndReturnsIt(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "first attempt",
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if result.SessionKey == "" {
		t.Fatal("no session_key returned — a caller cannot retry an attempt it cannot name")
	}
	if result.SessionKeySource != KeySourceMinted {
		t.Errorf("key source = %q, want %q", result.SessionKeySource, KeySourceMinted)
	}
	if result.Updated {
		t.Error("Updated = true on a fresh capture")
	}

	// And it must be PERSISTED, or the next call cannot resolve it.
	meta, _ := readNote(t, vault, result.NotePath)
	if meta.SessionKey != result.SessionKey {
		t.Errorf("frontmatter session_key = %q, want the returned key %q", meta.SessionKey, result.SessionKey)
	}
}

// TestCaptureRetryWithSameKeyUpdatesInPlace is the reason idempotency had to land
// before the MCP path was allowed to fail hard: hard-failing a call that has
// ALREADY written its note invites a retry, and a retry that mints a second note
// turns one lost archive link into two conflicting session records.
func TestCaptureRetryWithSameKeyUpdatesInPlace(t *testing.T) {
	vault := testVault(t)

	first, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "first attempt",
	})
	if err != nil {
		t.Fatalf("first capture: %v", err)
	}
	if n := countNotes(t, vault, "test-proj"); n != 1 {
		t.Fatalf("after first capture: %d notes, want 1", n)
	}

	second, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "second attempt, better narrative",
		SessionKey: first.SessionKey,
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	// THE ASSERTION: one note, not two.
	if n := countNotes(t, vault, "test-proj"); n != 1 {
		t.Fatalf("after retry with the same key: %d notes, want 1 — the retry DUPLICATED the note", n)
	}
	if !second.Updated {
		t.Error("Updated = false, but an existing note was rewritten")
	}
	if second.SessionKeySource != KeySourceCaller {
		t.Errorf("key source = %q, want %q", second.SessionKeySource, KeySourceCaller)
	}
	if second.NotePath != first.NotePath {
		t.Errorf("retry wrote a different note: %q vs %q", second.NotePath, first.NotePath)
	}
	if second.SessionID != first.SessionID {
		t.Errorf("retry changed the session ID: %q vs %q", second.SessionID, first.SessionID)
	}

	// The retry's narrative wins — that is the point of retrying.
	meta, _ := readNote(t, vault, second.NotePath)
	if meta.Summary != "second attempt, better narrative" {
		t.Errorf("summary = %q, want the retry's text", meta.Summary)
	}
}

// TestCaptureWithoutKeyMintsANewNote is the other half, and it is what keeps
// work-unit granularity alive. An agent captures once per WORK UNIT and a session
// holds several; keying on the session (the design this supersedes) would have made
// the second work unit overwrite the first. No key means new work means new note.
func TestCaptureWithoutKeyMintsANewNote(t *testing.T) {
	vault := testVault(t)

	for i := range 3 {
		if _, err := WriteSession(context.Background(), vault, nil, SessionParams{
			Project: "test-proj",
			Summary: "work unit",
		}); err != nil {
			t.Fatalf("capture %d: %v", i, err)
		}
	}
	if n := countNotes(t, vault, "test-proj"); n != 3 {
		t.Fatalf("%d notes after 3 keyless captures, want 3 — distinct work units were merged", n)
	}
}

// TestCaptureRetryPreservesEnrichmentArchiveAndFriction is the data-destruction
// test, and it guards the trap the plan flagged as the worst in this design.
//
// RewriteSession marshals EXACTLY what it is handed, and every SessionMeta field is
// omitempty — so a field absent from the incoming meta is not "left alone", it is
// DELETED. A naive "resolve the key, then RewriteSession with the retry's params"
// would therefore silently destroy: the archive: link, the friction_breakdown
// (whose nil means "never scored", so dropping it does not merely lose data — it
// LIES, converting a measured session into one that looks like a legacy note), and
// the LLM-synthesized narrative the async enrichment drain may have written AFTER
// the note was first captured. That last one is a LIVE race, not a theoretical one.
//
// So: capture, then simulate the drain enriching the note in place, then re-capture
// with the same key, and assert everything survives.
func TestCaptureRetryPreservesEnrichmentArchiveAndFriction(t *testing.T) {
	vault := testVault(t)

	first, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic auto-summary",
		Transcript: enrichTranscript, // gives the note a real friction breakdown
	})
	if err != nil {
		t.Fatalf("first capture: %v", err)
	}

	meta, _ := readNote(t, vault, first.NotePath)
	if meta.Breakdown == nil {
		t.Fatal("first capture recorded no friction breakdown; the test cannot prove it survives")
	}

	// Stand in for the async drain + a successful archive link: enrich the note in
	// place and give it an archive, exactly as those paths do.
	meta.Summary = "LLM-synthesized narrative"
	meta.Decisions = []string{"LLM decision"}
	meta.EnrichedBy = "test-model"
	meta.EnrichedAt = "2026-07-12T00:00:00Z"
	meta.Archive = "Projects/test-proj/transcripts/abc.manifest.json"
	if err := vault.RewriteSession("test-proj", meta.Date, parseFPFromID(first.SessionID), meta.Iteration,
		meta, buildSessionBody(paramsFromMeta(meta))); err != nil {
		t.Fatalf("seed enriched note: %v", err)
	}

	// Now RE-CAPTURE with the same key and NO enricher — the plain heuristic path.
	second, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic auto-summary",
		SessionKey: first.SessionKey,
	})
	if err != nil {
		t.Fatalf("re-capture: %v", err)
	}
	if !second.Updated {
		t.Fatal("re-capture minted a new note instead of updating the enriched one")
	}

	got, body := readNote(t, vault, second.NotePath)

	if got.EnrichedBy != "test-model" {
		t.Errorf("enriched_by = %q, want test-model — the re-capture REVERTED an LLM narrative to a heuristic summary", got.EnrichedBy)
	}
	if got.Summary != "LLM-synthesized narrative" {
		t.Errorf("summary = %q, want the LLM narrative — enrichment was destroyed", got.Summary)
	}
	if got.Archive != "Projects/test-proj/transcripts/abc.manifest.json" {
		t.Errorf("archive = %q, want it preserved — the transcript link was destroyed", got.Archive)
	}
	if got.Breakdown == nil {
		t.Error("friction_breakdown is nil after the re-capture — a MEASURED session now looks like one that was never scored")
	}
	if body == "" {
		t.Error("body is empty after the re-capture")
	}
}

// TestConcurrentCapturesDoNotClobber proves the single lock closed a race that
// PREDATES idempotency and was losing notes silently.
//
// NextIteration globs the sessions directory and takes max(NN)+1, and it used to do
// that OUTSIDE any lock — the lock was taken later, on the already-decided path. So
// two concurrent captures on one host+date both computed the same N+1, both locked
// the SAME note path, and the second overwrote the first. One of the two sessions
// simply vanished, with both calls reporting success.
//
// Bolting key-resolution in front of an unlocked mint would have inherited that
// race; holding one lock over resolve+mint+write removes it. N keyless captures must
// produce N notes.
func TestConcurrentCapturesDoNotClobber(t *testing.T) {
	vault := testVault(t)
	const n = 8

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := WriteSession(context.Background(), vault, nil, SessionParams{
				Project: "test-proj",
				Summary: fmt.Sprintf("concurrent capture %d", i),
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent capture failed: %v", err)
	}

	if got := countNotes(t, vault, "test-proj"); got != n {
		t.Fatalf("%d notes after %d concurrent captures — %d session(s) were CLOBBERED", got, n, n-got)
	}
}

// parseFPFromID is a test-local helper mirroring ParseFingerprint, kept explicit so
// the test does not silently depend on capture's own parsing being correct.
func parseFPFromID(sessionID string) string { return ParseFingerprint(sessionID) }
