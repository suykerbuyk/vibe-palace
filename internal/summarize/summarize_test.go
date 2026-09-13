// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package summarize

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeSummarizer is a test-only Summarizer. fn defaults to an always-success
// stub echoing the item's Kind; set fn to override behavior (e.g. always
// fail, or record calls).
type fakeSummarizer struct {
	mu    sync.Mutex
	calls []SummaryItem
	fn    func(ctx context.Context, item SummaryItem) (*SummaryResult, error)
}

func (f *fakeSummarizer) Summarize(ctx context.Context, item SummaryItem) (*SummaryResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, item)
	f.mu.Unlock()

	if f.fn != nil {
		return f.fn(ctx, item)
	}
	return &SummaryResult{Kind: item.Kind}, nil
}

func (f *fakeSummarizer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func queueFiles(t *testing.T, cwd string) []string {
	t.Helper()
	dir := QueueDir(cwd)
	matches, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatalf("glob queue dir: %v", err)
	}
	return matches
}

func TestEnqueueDrainRoundTrip_Iteration(t *testing.T) {
	cwd := t.TempDir()

	if err := EnqueueIterationSummary(cwd, "proj-a", 7); err != nil {
		t.Fatalf("EnqueueIterationSummary: %v", err)
	}

	fake := &fakeSummarizer{}
	drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1", drained)
	}
	if fake.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1", fake.callCount())
	}

	got := fake.calls[0]
	if got.Kind != KindIteration {
		t.Errorf("Kind = %q, want %q", got.Kind, KindIteration)
	}
	if got.Project != "proj-a" {
		t.Errorf("Project = %q, want proj-a", got.Project)
	}
	if got.Iteration != 7 {
		t.Errorf("Iteration = %d, want 7", got.Iteration)
	}

	if remaining := queueFiles(t, cwd); len(remaining) != 0 {
		t.Errorf("queue dir not empty after successful drain: %v", remaining)
	}
}

func TestEnqueueDrainRoundTrip_SessionNote(t *testing.T) {
	cwd := t.TempDir()

	if err := EnqueueSessionSummary(cwd, "proj-b", "2026-09-13", "fp123", 3, "/vault/proj-b/notes/foo.md"); err != nil {
		t.Fatalf("EnqueueSessionSummary: %v", err)
	}

	fake := &fakeSummarizer{}
	drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1", drained)
	}

	got := fake.calls[0]
	if got.Kind != KindSessionNote {
		t.Errorf("Kind = %q, want %q", got.Kind, KindSessionNote)
	}
	if got.Project != "proj-b" || got.Date != "2026-09-13" || got.Fingerprint != "fp123" ||
		got.Iteration != 3 || got.NotePath != "/vault/proj-b/notes/foo.md" {
		t.Errorf("unexpected item: %+v", got)
	}
}

func TestDrain_DeadLetterOnRepeatedFailure(t *testing.T) {
	cwd := t.TempDir()

	if err := EnqueueIterationSummary(cwd, "proj-c", 1); err != nil {
		t.Fatalf("EnqueueIterationSummary: %v", err)
	}

	fake := &fakeSummarizer{
		fn: func(ctx context.Context, item SummaryItem) (*SummaryResult, error) {
			return nil, errAlwaysFails
		},
	}

	drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 0 {
		t.Fatalf("drained = %d, want 0", drained)
	}

	// The single job should have been retried until maxSummaryAttempts was
	// reached, then dead-lettered — all within this one call, so the loop
	// must not have spun forever.
	if fake.callCount() != maxSummaryAttempts {
		t.Fatalf("callCount = %d, want %d", fake.callCount(), maxSummaryAttempts)
	}

	dir := QueueDir(cwd)
	failed, err := filepath.Glob(filepath.Join(dir, "*.failed"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("failed files = %v, want exactly one", failed)
	}
}

func TestDrain_NilSummarizerIsNoop(t *testing.T) {
	cwd := t.TempDir()

	if err := EnqueueIterationSummary(cwd, "proj-d", 1); err != nil {
		t.Fatalf("EnqueueIterationSummary: %v", err)
	}

	drained, err := DrainSummarizationQueue(context.Background(), cwd, nil, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 0 {
		t.Fatalf("drained = %d, want 0", drained)
	}

	// Nothing on disk should have been touched: the original *.json job
	// file must still be there, untouched.
	remaining := queueFiles(t, cwd)
	if len(remaining) != 1 {
		t.Fatalf("queue dir = %v, want exactly the original untouched job file", remaining)
	}
	if filepath.Ext(remaining[0]) != ".json" {
		t.Fatalf("remaining file %q was touched (not still *.json)", remaining[0])
	}
}

func TestDrain_MaxBoundsProcessedCount(t *testing.T) {
	cwd := t.TempDir()

	const total = 5
	for i := range total {
		if err := EnqueueIterationSummary(cwd, "proj-e", i); err != nil {
			t.Fatalf("EnqueueIterationSummary(%d): %v", i, err)
		}
	}

	fake := &fakeSummarizer{}
	const max = 2
	drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, max)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != max {
		t.Fatalf("drained = %d, want %d", drained, max)
	}
	if fake.callCount() != max {
		t.Fatalf("callCount = %d, want %d", fake.callCount(), max)
	}

	remaining := queueFiles(t, cwd)
	if len(remaining) != total-max {
		t.Fatalf("remaining queue files = %d, want %d", len(remaining), total-max)
	}
}

func TestDrain_ContextCancellationStopsEarly(t *testing.T) {
	cwd := t.TempDir()

	for i := range 3 {
		if err := EnqueueIterationSummary(cwd, "proj-f", i); err != nil {
			t.Fatalf("EnqueueIterationSummary(%d): %v", i, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before the drain even starts

	fake := &fakeSummarizer{
		fn: func(ctx context.Context, item SummaryItem) (*SummaryResult, error) {
			// If the drain loop ever calls this despite the context already
			// being canceled, that is the bug under test: fail loudly and
			// deterministically instead of hanging.
			t.Fatal("Summarize called despite canceled context")
			return nil, nil
		},
	}

	drained, err := DrainSummarizationQueue(ctx, cwd, fake, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 0 {
		t.Fatalf("drained = %d, want 0", drained)
	}
	if fake.callCount() != 0 {
		t.Fatalf("callCount = %d, want 0", fake.callCount())
	}

	remaining := queueFiles(t, cwd)
	if len(remaining) != 3 {
		t.Fatalf("remaining queue files = %d, want 3 (untouched)", len(remaining))
	}
}

// TestEnqueue_UnknownKindFailsLoudly pins a real fix: queueFileName's switch
// used to fall through any non-KindSessionNote value (including a future
// third kind, a zero-value SummaryItem, or a typo) to the KindIteration
// naming branch, silently colliding with that scheme instead of failing
// loudly at the one place that dispatches on Kind.
func TestEnqueue_UnknownKindFailsLoudly(t *testing.T) {
	cwd := t.TempDir()
	err := enqueue(cwd, SummaryItem{Kind: SummaryJobKind("bogus"), Project: "proj"})
	if err == nil {
		t.Fatal("enqueue: want an error for an unrecognized Kind, got nil")
	}
	if remaining := queueFiles(t, cwd); len(remaining) != 0 {
		t.Errorf("queue files = %v, want none written for a failed enqueue", remaining)
	}
}

// errAlwaysFails is a stand-in summarization error for the dead-letter test.
type sentinelError string

func (e sentinelError) Error() string { return string(e) }

const errAlwaysFails = sentinelError("fake summarizer always fails")

// TestEnqueue_DeduplicatesByIdentity pins the fix for a real duplicate-job
// bug: re-enqueuing the SAME logical job (identical coordinates — e.g. a
// session note captured twice, resolved by WriteSession's own
// UpsertSessionByKey to the same key) must overwrite the one queue file for
// that identity, never pile up a second one. This mirrors
// internal/capture/enrichqueue.go's EnqueueEnrichment, whose queue file name
// is itself deterministic (storage.SessionStem-derived), not random/time-based.
func TestEnqueue_DeduplicatesByIdentity(t *testing.T) {
	t.Run("iteration", func(t *testing.T) {
		cwd := t.TempDir()
		if err := EnqueueIterationSummary(cwd, "proj-g", 9); err != nil {
			t.Fatalf("first EnqueueIterationSummary: %v", err)
		}
		if err := EnqueueIterationSummary(cwd, "proj-g", 9); err != nil {
			t.Fatalf("second EnqueueIterationSummary: %v", err)
		}
		if remaining := queueFiles(t, cwd); len(remaining) != 1 {
			t.Fatalf("queue files = %v, want exactly 1 (overwritten, not duplicated)", remaining)
		}
	})

	t.Run("session_note", func(t *testing.T) {
		cwd := t.TempDir()
		if err := EnqueueSessionSummary(cwd, "proj-h", "2026-09-13", "fp999", 2, "/vault/proj-h/notes/a.md"); err != nil {
			t.Fatalf("first EnqueueSessionSummary: %v", err)
		}
		if err := EnqueueSessionSummary(cwd, "proj-h", "2026-09-13", "fp999", 2, "/vault/proj-h/notes/a.md"); err != nil {
			t.Fatalf("second EnqueueSessionSummary: %v", err)
		}
		if remaining := queueFiles(t, cwd); len(remaining) != 1 {
			t.Fatalf("queue files = %v, want exactly 1 (overwritten, not duplicated)", remaining)
		}
	})

	// Different identities must NOT collide with each other.
	t.Run("distinct identities do not collide", func(t *testing.T) {
		cwd := t.TempDir()
		if err := EnqueueIterationSummary(cwd, "proj-i", 1); err != nil {
			t.Fatal(err)
		}
		if err := EnqueueIterationSummary(cwd, "proj-i", 2); err != nil {
			t.Fatal(err)
		}
		if err := EnqueueSessionSummary(cwd, "proj-i", "2026-09-13", "fp1", 1, "/n/a.md"); err != nil {
			t.Fatal(err)
		}
		if remaining := queueFiles(t, cwd); len(remaining) != 3 {
			t.Fatalf("queue files = %v, want 3 distinct jobs", remaining)
		}
	})
}

// TestDrain_SummarizeContextErrorDoesNotConsumeAttempt pins the fix for a
// real bug: a Summarize call interrupted by THIS DRAIN'S OWN ctx is not a
// genuine summarization failure and must not bump Attempts or consume
// maxSummaryAttempts's retry budget the way a real failure does — the job
// never got a fair, uninterrupted attempt. The classification is
// deliberately keyed on ctx.Err() alone (checked after the call), not on
// pattern-matching the Summarizer's returned error — see
// TestDrain_SummarizeReturnsContextShapedErrorButOuterCtxHealthyIsAGenuineFailure
// for why matching on the error's shape instead would misclassify an
// unrelated case.
func TestDrain_SummarizeContextErrorDoesNotConsumeAttempt(t *testing.T) {
	cwd := t.TempDir()
	if err := EnqueueIterationSummary(cwd, "proj-j", 4); err != nil {
		t.Fatalf("EnqueueIterationSummary: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeSummarizer{
		fn: func(ctx context.Context, item SummaryItem) (*SummaryResult, error) {
			// Simulate the drain's OWN ctx being canceled while this call was
			// in flight (e.g. the process is shutting down) — the
			// Summarizer here deliberately does NOT propagate any
			// context-shaped error, to prove classification does not depend
			// on it doing so.
			cancel()
			return nil, nil
		},
	}

	drained, err := DrainSummarizationQueue(ctx, cwd, fake, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 0 {
		t.Fatalf("drained = %d, want 0", drained)
	}
	if fake.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1 (drain must stop after a ctx-interrupted call, not retry inline)", fake.callCount())
	}

	// The job must still be requeued as a plain *.json (not dead-lettered —
	// a single ctx interruption is nowhere near maxSummaryAttempts), and its
	// Attempts field must still read 0: the interruption must not have been
	// charged against the retry budget.
	dir := QueueDir(cwd)
	remaining, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("remaining *.json files = %v, want exactly 1", remaining)
	}
	data, err := os.ReadFile(remaining[0])
	if err != nil {
		t.Fatalf("read requeued job: %v", err)
	}
	var item SummaryItem
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatalf("unmarshal requeued job: %v", err)
	}
	if item.Attempts != 0 {
		t.Errorf("Attempts = %d, want 0 (ctx interruption must not consume a retry)", item.Attempts)
	}

	if failed, _ := filepath.Glob(filepath.Join(dir, "*.failed")); len(failed) != 0 {
		t.Errorf("job was dead-lettered on a single ctx interruption: %v", failed)
	}
}

// TestDrain_SummarizeReturnsContextShapedErrorButOuterCtxHealthyIsAGenuineFailure
// pins the other half of the same fix: a Summarizer that returns
// context.DeadlineExceeded (e.g. from its OWN internal per-call
// context.WithTimeout) while THIS DRAIN'S OWN ctx is perfectly healthy must
// be treated as a genuine, Attempts-consuming failure — not misclassified as
// an interruption just because the error happens to look like a context
// error. Only the drain's own ctx.Err() is authoritative for "was this call
// interrupted"; the error's shape is not.
func TestDrain_SummarizeReturnsContextShapedErrorButOuterCtxHealthyIsAGenuineFailure(t *testing.T) {
	cwd := t.TempDir()
	if err := EnqueueIterationSummary(cwd, "proj-k", 9); err != nil {
		t.Fatalf("EnqueueIterationSummary: %v", err)
	}

	fake := &fakeSummarizer{
		fn: func(ctx context.Context, item SummaryItem) (*SummaryResult, error) {
			return nil, context.DeadlineExceeded
		},
	}

	// context.Background() is never done — the outer ctx is healthy
	// throughout, even though the Summarizer's own returned error happens to
	// be context.DeadlineExceeded.
	drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 0 {
		t.Fatalf("drained = %d, want 0", drained)
	}
	// A genuine failure must keep retrying inline (not break the loop after
	// one call the way the ctx-interruption branch does) until dead-lettered.
	if fake.callCount() != maxSummaryAttempts {
		t.Fatalf("callCount = %d, want %d (a healthy outer ctx must not stop the drain early)", fake.callCount(), maxSummaryAttempts)
	}

	dir := QueueDir(cwd)
	failed, err := filepath.Glob(filepath.Join(dir, "*.failed"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("failed files = %v, want exactly one (this must consume the retry budget like any other failure)", failed)
	}
}

// TestDrain_SuccessBeatsCanceledCtxInSameCall pins a real fix: a genuine
// success (sErr == nil && res != nil) must be recorded (Done, drained++)
// EVEN IF this drain's own ctx becomes canceled/deadline-exceeded in the
// same instant Summarize returns — the success must never be discarded and
// requeued just because ctx happened to be done by the time the drain loop
// checks it. Without this, a future Summarizer with real side effects (e.g.
// writing a vault-committed summary) would silently duplicate that work on
// the next drain, since the job would be requeued and reprocessed despite
// already having succeeded once.
func TestDrain_SuccessBeatsCanceledCtxInSameCall(t *testing.T) {
	cwd := t.TempDir()
	if err := EnqueueIterationSummary(cwd, "proj-l", 3); err != nil {
		t.Fatalf("EnqueueIterationSummary: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeSummarizer{
		fn: func(ctx context.Context, item SummaryItem) (*SummaryResult, error) {
			// Simulate ctx becoming canceled in the exact same instant this
			// call succeeds — e.g. a shutdown signal racing a real, already-
			// completed Summarize call.
			cancel()
			return &SummaryResult{Kind: item.Kind}, nil
		},
	}

	drained, err := DrainSummarizationQueue(ctx, cwd, fake, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1 (a genuine success must count even though ctx was canceled in the same call)", drained)
	}
	if fake.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1", fake.callCount())
	}

	// The job must be gone entirely (Done), NOT requeued as a *.json or
	// *.json.processing file — requeuing a completed success would cause it
	// to be reprocessed (and, with a real side-effecting Summarizer,
	// duplicated) on a later drain.
	remaining := queueFiles(t, cwd)
	if len(remaining) != 0 {
		t.Fatalf("queue files = %v, want none (successful job must be Done, not requeued because ctx was canceled)", remaining)
	}
}
