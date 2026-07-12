// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// hasFailure reports whether the result recorded a loss at the named stage.
func hasFailure(r *SessionResult, stage string) bool {
	for _, f := range r.Failures {
		if f.Stage == stage {
			return true
		}
	}
	return false
}

func stages(r *SessionResult) []string {
	var s []string
	for _, f := range r.Failures {
		s = append(s, f.Stage)
	}
	return s
}

// TestWriteSessionNoteLandsDespitePreWriteFailures is the load-bearing test of
// the accumulate-never-throw contract (DoD 2c).
//
// Enrichment, archive resolution and friction scoring all feed the frontmatter,
// so they run BEFORE the note is written. An early return on any of them —
// which is what "fail hard on every loss" naively means — would write no note at
// all and lose the session entirely, which the operator ruled is strictly worse
// than the bug being fixed: losing the session is worse than losing the archive
// link. So this test fails ALL THREE pre-write stages at once and asserts the
// note is nonetheless on disk, with the losses reported rather than thrown.
func TestWriteSessionNoteLandsDespitePreWriteFailures(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "work that must survive a bad enricher and a missing archive",
		Transcript: enrichTranscript,
		Enricher:   failingEnricher(),
		// No archive was ever created for this ID, so ResolveEntry fails.
		ArchiveSessionID: "no-such-archive-session",
	})
	if err != nil {
		t.Fatalf("WriteSession returned a fatal error; the note was lost entirely: %v", err)
	}

	// The note — the one artifact whose loss is unrecoverable — must exist.
	abs := filepath.Join(vault.Root, filepath.FromSlash(result.NotePath))
	data, readErr := os.ReadFile(abs)
	if readErr != nil {
		t.Fatalf("note does not exist at the path capture reported (%s): %v", result.NotePath, readErr)
	}
	if len(data) == 0 {
		t.Fatal("note exists but is empty")
	}

	// And the losses must be reported, not swallowed.
	if !result.Failed() {
		t.Fatalf("capture lost enrichment AND the archive link but reported no failures: %+v", result)
	}
	if !hasFailure(result, StageEnrichment) {
		t.Errorf("enrichment failure not reported; stages = %v", stages(result))
	}
	if !hasFailure(result, StageArchiveResolve) {
		t.Errorf("archive resolve failure not reported; stages = %v", stages(result))
	}

	// The note must carry the plain heuristic summary, not a half-applied one.
	meta, _, perr := storage.ParseFrontmatter(data)
	if perr != nil {
		t.Fatalf("parse note frontmatter: %v", perr)
	}
	if meta.Summary != "work that must survive a bad enricher and a missing archive" {
		t.Errorf("summary = %q, want the plain heuristic summary", meta.Summary)
	}
	if meta.EnrichedBy != "" {
		t.Errorf("enriched_by = %q, want empty — the enricher FAILED", meta.EnrichedBy)
	}
}

// TestWriteSessionNoteLandsDespitePostWriteFailure covers the other half of the
// contract: a stage that runs AFTER the note is on disk must not retroactively
// turn the capture into a fatal error. Here the enrichment enqueue fails,
// because .vibe-palace in the working directory is a FILE, so the queue
// directory cannot be created under it.
func TestWriteSessionNoteLandsDespitePostWriteFailure(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	// Booby-trap the queue path: .vibe-palace is a file, so MkdirAll under it fails.
	if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed blocked queue path: %v", err)
	}

	// A failing enricher with a transcript sets enqueuePending, so the (doomed)
	// enqueue is actually attempted.
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "post-write loss must not lose the note",
		Transcript: enrichTranscript,
		Enricher:   failingEnricher(),
		CWD:        cwd,
	})
	if err != nil {
		t.Fatalf("a post-write failure was raised as fatal; the note was lost: %v", err)
	}

	abs := filepath.Join(vault.Root, filepath.FromSlash(result.NotePath))
	if _, statErr := os.Stat(abs); statErr != nil {
		t.Fatalf("note missing after a post-write failure: %v", statErr)
	}
	if !hasFailure(result, StageEnrichmentEnqueue) {
		t.Errorf("enqueue failure not reported; stages = %v", stages(result))
	}
}

// TestWriteSessionCleanCaptureReportsNoFailures guards the other direction: a
// capture that loses nothing must report nothing. A failure list that is never
// empty is a failure list agents learn to ignore — the same reasoning that
// killed the "partial" status tier.
func TestWriteSessionCleanCaptureReportsNoFailures(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "a clean capture",
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if result.Failed() {
		t.Errorf("clean capture reported failures: %v", stages(result))
	}
	if result.Status != "ok" {
		t.Errorf("status = %q, want ok", result.Status)
	}
}
