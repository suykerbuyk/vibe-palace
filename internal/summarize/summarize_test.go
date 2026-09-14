// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package summarize

import (
	"context"
	"encoding/json"
	"errors"
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

	// With the pendingRequeue fairness fix, a failed item's claim is held
	// (as ".processing") for the REST of the call it failed in, so a lone
	// failing item now gets exactly ONE attempt per DrainSummarizationQueue
	// call, not maxSummaryAttempts attempts within a single call. This
	// mirrors internal/capture/enrichqueue.go's own
	// TestDrainDeadLettersAfterMaxAttempts, which drains repeatedly in a
	// loop for the same reason. Drain repeatedly until dead-lettered.
	for i := range maxSummaryAttempts + 2 {
		drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, 0)
		if err != nil {
			t.Fatalf("drain %d: DrainSummarizationQueue: %v", i, err)
		}
		if drained != 0 {
			t.Fatalf("drain %d: drained = %d, want 0", i, drained)
		}
	}

	// The single job should have been retried until maxSummaryAttempts was
	// reached, then dead-lettered, one attempt per call.
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

// TestQueueFileName_IterationOrdersNumericallyNotLexically pins the fix for
// zero-pad-summarization-queue-filenames: jobqueue.Claim lists a queue
// directory via os.ReadDir, which sorts by filename — lexically, not
// numerically. Before the fix, "iteration-10.json" < "iteration-2.json"
// lexically (the actual bug); zero-padding must make the filename order
// agree with the numeric order for every iteration this test checks,
// including the pre-fix failure pair and a same-length pair the unpadded
// scheme already got right (to prove the fix doesn't just get lucky).
func TestQueueFileName_IterationOrdersNumericallyNotLexically(t *testing.T) {
	pairs := []struct{ lower, higher int }{
		{2, 10},    // the actual bug: unpadded, "10" < "2" lexically
		{9, 100},   // one digit vs three
		{99, 100},  // the classic zero-pad boundary case
		{413, 414}, // consecutive, both already 3 digits
		{1, 2},     // same-length pair the unpadded scheme already ordered correctly
	}
	for _, p := range pairs {
		lowerName, err := queueFileName(SummaryItem{Kind: KindIteration, Iteration: p.lower})
		if err != nil {
			t.Fatalf("queueFileName(%d): %v", p.lower, err)
		}
		higherName, err := queueFileName(SummaryItem{Kind: KindIteration, Iteration: p.higher})
		if err != nil {
			t.Fatalf("queueFileName(%d): %v", p.higher, err)
		}
		if !(lowerName < higherName) {
			t.Errorf("queueFileName(%d)=%q, queueFileName(%d)=%q — want the lower iteration's filename to sort first lexically",
				p.lower, lowerName, p.higher, higherName)
		}
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
	//
	// With the pendingRequeue fairness fix, a failed item's claim is held
	// (as ".processing") for the REST of the call it failed in, so a lone
	// failing item now gets exactly ONE attempt per DrainSummarizationQueue
	// call rather than maxSummaryAttempts attempts within a single call (see
	// TestDrain_DeadLetterOnRepeatedFailure's own comment for the same
	// point). Drain repeatedly until dead-lettered; the genuine-failure
	// branch (unlike the ctx-interruption branch) still consumes a retry on
	// every one of these attempts.
	for i := range maxSummaryAttempts + 2 {
		drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, 0)
		if err != nil {
			t.Fatalf("drain %d: DrainSummarizationQueue: %v", i, err)
		}
		if drained != 0 {
			t.Fatalf("drain %d: drained = %d, want 0", i, drained)
		}
	}
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

// TestDrain_FairnessFixSecondItemNotStarved pins the port of
// internal/capture/enrichqueue.go's pendingRequeue fairness fix onto
// DrainSummarizationQueue. Without it, a persistently-failing,
// lexicographically-first-claimed item is rewritten back to its claimable
// name IMMEDIATELY on every failed attempt — and since jobqueue.Claim always
// claims the lexicographically-first claimable "*.json" file, that item
// re-claims itself on every subsequent Claim call within this SAME
// invocation, consuming this call's entire `max` processed budget on itself
// alone and starving every other, distinct queued item.
//
// "iteration-0.json" sorts before "iteration-1.json", so item 0 (which
// always fails) is guaranteed to be claimed first. max is set to exactly the
// number of queued items (2): just enough budget for both items to get ONE
// fair attempt each, but only if a failed item does NOT re-claim itself
// ahead of item 1.
func TestDrain_FairnessFixSecondItemNotStarved(t *testing.T) {
	cwd := t.TempDir()
	if err := EnqueueIterationSummary(cwd, "proj-fair", 0); err != nil {
		t.Fatalf("EnqueueIterationSummary(0): %v", err)
	}
	if err := EnqueueIterationSummary(cwd, "proj-fair", 1); err != nil {
		t.Fatalf("EnqueueIterationSummary(1): %v", err)
	}

	fake := &fakeSummarizer{
		fn: func(ctx context.Context, item SummaryItem) (*SummaryResult, error) {
			if item.Iteration == 0 {
				return nil, errAlwaysFails
			}
			return &SummaryResult{Kind: item.Kind}, nil
		},
	}

	drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, 2)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1 (item 1 must still be claimed and processed in this same call, not starved by item 0's repeated failure)", drained)
	}

	// item 1 must have actually been handed to Summarize (Done'd, so it is
	// no longer in the queue dir at all); item 0 must be back to a plain
	// *.json (requeued, not dead-lettered after a single failure) with
	// Attempts == 1.
	dir := QueueDir(cwd)
	remaining, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("remaining *.json files = %v, want exactly 1 (item 0, requeued)", remaining)
	}
	data, err := os.ReadFile(remaining[0])
	if err != nil {
		t.Fatalf("read requeued job: %v", err)
	}
	var item SummaryItem
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatalf("unmarshal requeued job: %v", err)
	}
	if item.Iteration != 0 {
		t.Fatalf("requeued item.Iteration = %d, want 0", item.Iteration)
	}
	if item.Attempts != 1 {
		t.Fatalf("requeued item.Attempts = %d, want 1", item.Attempts)
	}
}

// TestDrain_UnsupportedKindContinuesNotBreaks pins ErrUnsupportedKind's
// handling as a per-ITEM property, not a call-level interruption: the drain
// loop must `continue` past an unsupported-kind item, not `break` out of the
// whole call. Two unsupported-kind items are queued ahead of one
// supported-kind item (by filename sort order: "iteration-*.json" sorts
// before "session-*.json") so a `break` bug is exposed on the very FIRST
// unsupported item, regardless of any incidental ordering coincidence.
func TestDrain_UnsupportedKindContinuesNotBreaks(t *testing.T) {
	cwd := t.TempDir()

	// Two unsupported-kind (KindIteration; no Iteration handler registered
	// below) items, claimed first by filename sort order.
	if err := EnqueueIterationSummary(cwd, "proj-uk", 0); err != nil {
		t.Fatalf("EnqueueIterationSummary(0): %v", err)
	}
	if err := EnqueueIterationSummary(cwd, "proj-uk", 1); err != nil {
		t.Fatalf("EnqueueIterationSummary(1): %v", err)
	}
	// One supported-kind (KindSessionNote) item, claimed last by filename
	// sort order ("session-..." > "iteration-...").
	if err := EnqueueSessionSummary(cwd, "proj-uk", "2026-09-13", "fpuk", 0, "/vault/proj-uk/notes/a.md"); err != nil {
		t.Fatalf("EnqueueSessionSummary: %v", err)
	}

	sessionHandler := &fakeSummarizer{}
	dispatch := &DispatchSummarizer{SessionNote: sessionHandler} // Iteration left nil: unsupported

	drained, err := DrainSummarizationQueue(context.Background(), cwd, dispatch, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1 (the supported session-note item must still be reached and processed in this same call)", drained)
	}
	if sessionHandler.callCount() != 1 {
		t.Fatalf("session handler callCount = %d, want 1", sessionHandler.callCount())
	}

	// Both unsupported items must still be present, unchanged, as plain
	// *.json files with Attempts == 0 (deferred back to claimable, never
	// charged a retry).
	dir := QueueDir(cwd)
	remaining, err := filepath.Glob(filepath.Join(dir, "iteration-*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("remaining iteration-*.json files = %v, want exactly 2 (both unsupported items returned to claimable)", remaining)
	}
	for _, p := range remaining {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var item SummaryItem
		if err := json.Unmarshal(data, &item); err != nil {
			t.Fatalf("unmarshal %s: %v", p, err)
		}
		if item.Kind != KindIteration {
			t.Errorf("%s: Kind = %q, want %q", p, item.Kind, KindIteration)
		}
		if item.Attempts != 0 {
			t.Errorf("%s: Attempts = %d, want 0 (unsupported kind must never consume a retry)", p, item.Attempts)
		}
	}

	// The session-note item must be gone entirely (Done'd), not left behind.
	if remaining, _ := filepath.Glob(filepath.Join(dir, "session-*.json")); len(remaining) != 0 {
		t.Fatalf("remaining session-*.json files = %v, want none (supported item must be Done'd)", remaining)
	}
}

// writeRawQueueFile plants a job file directly on disk under cwd's queue
// dir, bypassing enqueue/queueFileName entirely — used to simulate a legacy,
// pre-fix unpadded "iteration-<N>.json" file left over from before
// zero-pad-summarization-queue-filenames, or a hand-placed collision, rather
// than whatever the CURRENT queueFileName would produce.
func writeRawQueueFile(t *testing.T, cwd, name string, item SummaryItem) {
	t.Helper()
	dir := QueueDir(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir queue dir: %v", err)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestDrain_MigratesLegacyUnpaddedIterationFilename pins the migration half
// of zero-pad-summarization-queue-filenames: a legacy "iteration-<N>.json"
// file (unpadded — exactly the shape a pre-fix EnqueueIterationSummary call
// would have left on disk, and exactly the shape found in real, live,
// undrained queues during that task's investigation) must still be picked
// up and processed by a drain call, not silently ignored, and must have been
// renamed to the current padded form before jobqueue.Claim ever lists the
// directory.
func TestDrain_MigratesLegacyUnpaddedIterationFilename(t *testing.T) {
	cwd := t.TempDir()
	writeRawQueueFile(t, cwd, "iteration-7.json", SummaryItem{
		Kind:      KindIteration,
		Project:   "proj-legacy",
		Iteration: 7,
	})

	fake := &fakeSummarizer{}
	drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, 0)
	if err != nil {
		t.Fatalf("DrainSummarizationQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1 (the legacy-named file must still be claimed and processed)", drained)
	}
	if fake.callCount() != 1 || fake.calls[0].Project != "proj-legacy" || fake.calls[0].Iteration != 7 {
		t.Fatalf("calls = %+v, want exactly one call for proj-legacy iteration 7", fake.calls)
	}
	if remaining := queueFiles(t, cwd); len(remaining) != 0 {
		t.Errorf("queue dir not empty after successful drain: %v", remaining)
	}
}

// TestMigrateLegacyIterationNames_SkipsWhenPaddedTargetAlreadyExists pins the
// clobber guard directly against migrateLegacyIterationNames, in isolation
// from jobqueue.Claim's own independent per-file processing (which would
// otherwise legitimately claim and drain BOTH files in one call — a
// migration miss does not stop them from being two distinct claimable
// *.json files, it only risks os.Rename silently destroying one of them,
// which is the one thing this guard exists to prevent).
//
// When BOTH a legacy unpadded file and its padded counterpart already exist
// for the same iteration (a newer re-enqueue already landed in the current
// form after the fix shipped, while the stale pre-fix duplicate was never
// cleaned up), migration must never os.Rename the legacy file onto the
// padded one — POSIX rename silently overwrites its destination, which
// would destroy the newer, correct copy's bytes with stale content.
func TestMigrateLegacyIterationNames_SkipsWhenPaddedTargetAlreadyExists(t *testing.T) {
	cwd := t.TempDir()
	legacyName := "iteration-7.json"
	paddedName, err := queueFileName(SummaryItem{Kind: KindIteration, Iteration: 7})
	if err != nil {
		t.Fatalf("queueFileName: %v", err)
	}
	if legacyName == paddedName {
		t.Fatalf("test setup is broken: legacy name %q must differ from the current padded name %q", legacyName, paddedName)
	}

	writeRawQueueFile(t, cwd, legacyName, SummaryItem{Kind: KindIteration, Project: "proj-stale", Iteration: 7})
	writeRawQueueFile(t, cwd, paddedName, SummaryItem{Kind: KindIteration, Project: "proj-current", Iteration: 7})

	dir := QueueDir(cwd)
	migrateLegacyIterationNames(dir)

	// Both files must still exist, at their ORIGINAL names, with their
	// ORIGINAL content — migration must have renamed neither, since renaming
	// the legacy one onto the padded name would have clobbered it.
	for name, wantProject := range map[string]string{legacyName: "proj-stale", paddedName: "proj-current"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: want it left in place, got: %v", name, err)
		}
		var item SummaryItem
		if err := json.Unmarshal(data, &item); err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}
		if item.Project != wantProject {
			t.Errorf("%s content Project = %q, want %q (untouched)", name, item.Project, wantProject)
		}
	}
}

// TestDispatchSummarizer covers DispatchSummarizer's Kind-based routing:
// a registered handler is called for its own Kind, and ErrUnsupportedKind is
// returned for any Kind whose handler field is nil.
func TestDispatchSummarizer(t *testing.T) {
	t.Run("only Iteration registered", func(t *testing.T) {
		iter := &fakeSummarizer{}
		d := &DispatchSummarizer{Iteration: iter}

		iterItem := SummaryItem{Kind: KindIteration, Project: "p", Iteration: 1}
		res, err := d.Summarize(context.Background(), iterItem)
		if err != nil {
			t.Fatalf("Summarize(KindIteration): unexpected err %v", err)
		}
		if res == nil || res.Kind != KindIteration {
			t.Fatalf("Summarize(KindIteration): res = %+v, want Kind %q", res, KindIteration)
		}
		if iter.callCount() != 1 {
			t.Fatalf("iter.callCount() = %d, want 1", iter.callCount())
		}

		noteItem := SummaryItem{Kind: KindSessionNote, Project: "p"}
		res, err = d.Summarize(context.Background(), noteItem)
		if !errors.Is(err, ErrUnsupportedKind) {
			t.Fatalf("Summarize(KindSessionNote): err = %v, want ErrUnsupportedKind", err)
		}
		if res != nil {
			t.Fatalf("Summarize(KindSessionNote): res = %+v, want nil", res)
		}
		// The unregistered SessionNote handler must not have been invoked
		// (nil, so calling it would have panicked).
		if iter.callCount() != 1 {
			t.Fatalf("iter.callCount() after unsupported call = %d, want still 1", iter.callCount())
		}
	})

	t.Run("both registered route independently", func(t *testing.T) {
		iter := &fakeSummarizer{}
		note := &fakeSummarizer{}
		d := &DispatchSummarizer{Iteration: iter, SessionNote: note}

		if _, err := d.Summarize(context.Background(), SummaryItem{Kind: KindIteration}); err != nil {
			t.Fatalf("Summarize(KindIteration): unexpected err %v", err)
		}
		if _, err := d.Summarize(context.Background(), SummaryItem{Kind: KindSessionNote}); err != nil {
			t.Fatalf("Summarize(KindSessionNote): unexpected err %v", err)
		}
		if iter.callCount() != 1 {
			t.Fatalf("iter.callCount() = %d, want 1", iter.callCount())
		}
		if note.callCount() != 1 {
			t.Fatalf("note.callCount() = %d, want 1", note.callCount())
		}
	})
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

// readQueueItemAttempts unmarshals the Attempts field of a plain *.json queue
// file, failing the test on any I/O or unmarshal error.
func readQueueItemAttempts(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var item SummaryItem
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return item.Attempts
}

// plainJSONQueueFiles returns the sorted list of plain "*.json" files (never
// ".processing" or ".failed") currently sitting in cwd's summarization queue
// directory.
func plainJSONQueueFiles(t *testing.T, cwd string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(QueueDir(cwd), "*.json"))
	if err != nil {
		t.Fatalf("glob *.json: %v", err)
	}
	return matches
}

// TestDrain_DisabledConfigThenLaterFixedIsZeroLoss pins the CORE zero-loss
// guarantee a project relies on while [summarization] is unconfigured: N
// queued KindIteration jobs must survive ANY NUMBER of drain calls made with
// no registered Iteration handler (the DispatchSummarizer shape
// cmd/vp/cmd_drain.go builds when NewIterationSummarizerFromConfig returns
// (nil, nil) for a disabled config) with drained==0, the files still present
// as plain *.json, and Attempts genuinely UNCHANGED at 0 every single time —
// per ErrUnsupportedKind's own doc comment, this is a per-ITEM property, not
// a retry-consuming failure. Once a working Summarizer is later substituted
// (the config got fixed), every item must still drain, exactly once each,
// with nothing lost or duplicated.
func TestDrain_DisabledConfigThenLaterFixedIsZeroLoss(t *testing.T) {
	cwd := t.TempDir()

	const n = 4
	for i := range n {
		if err := EnqueueIterationSummary(cwd, "proj-disabled", 100+i); err != nil {
			t.Fatalf("EnqueueIterationSummary(%d): %v", i, err)
		}
	}

	disabled := &DispatchSummarizer{Iteration: nil} // simulates disabled/unresolvable config
	const m = 3
	for run := range m {
		drained, err := DrainSummarizationQueue(context.Background(), cwd, disabled, 0)
		if err != nil {
			t.Fatalf("run %d: DrainSummarizationQueue: %v", run, err)
		}
		if drained != 0 {
			t.Fatalf("run %d: drained = %d, want 0", run, drained)
		}

		remaining := plainJSONQueueFiles(t, cwd)
		if len(remaining) != n {
			t.Fatalf("run %d: plain *.json files = %v, want exactly %d", run, remaining, n)
		}
		// No ".processing" or ".failed" artifacts must be left behind once
		// DrainSummarizationQueue has RETURNED — pendingRequeue is flushed via
		// its own defer before the call returns, so every deferred item must
		// already be back to a plain claimable *.json by the time we observe
		// it here.
		if all := queueFiles(t, cwd); len(all) != n {
			t.Fatalf("run %d: total queue files = %v, want exactly %d plain *.json and nothing else", run, all, n)
		}
		for _, p := range remaining {
			if attempts := readQueueItemAttempts(t, p); attempts != 0 {
				t.Errorf("run %d: %s Attempts = %d, want 0 (unsupported kind must never consume a retry)", run, p, attempts)
			}
		}
	}

	// Now the config is "fixed": swap in a working stub Summarizer and drain
	// once more.
	fake := &fakeSummarizer{}
	fixed := &DispatchSummarizer{Iteration: fake}
	drained, err := DrainSummarizationQueue(context.Background(), cwd, fixed, 0)
	if err != nil {
		t.Fatalf("final drain: DrainSummarizationQueue: %v", err)
	}
	if drained != n {
		t.Fatalf("final drain: drained = %d, want %d", drained, n)
	}
	if remaining := queueFiles(t, cwd); len(remaining) != 0 {
		t.Fatalf("final drain: queue dir = %v, want empty", remaining)
	}

	// Track by the SET of distinct Iteration numbers seen, not just a call
	// counter, so a duplicate-vs-drop bug (e.g. the same item claimed twice
	// while another is silently dropped) would be caught even though the
	// total call count alone would look correct.
	seen := map[int]int{}
	for _, item := range fake.calls {
		seen[item.Iteration]++
	}
	if len(seen) != n {
		t.Fatalf("distinct iterations summarized = %d, want %d; seen=%v", len(seen), n, seen)
	}
	for i := range n {
		iter := 100 + i
		if seen[iter] != 1 {
			t.Errorf("iteration %d was summarized %d time(s), want exactly 1; seen=%v", iter, seen[iter], seen)
		}
	}
}

// TestDrain_EnabledUnresolvableThenLaterFixedIsZeroLoss covers the OTHER
// "not ready yet" shape: unlike TestDrain_DisabledConfigThenLaterFixedIsZeroLoss
// (an unsupported Kind, which never consumes a retry), a Summarizer that is
// registered for the Kind but genuinely FAILS (e.g. summarization is enabled
// but the provider is briefly unreachable) DOES consume a retry via
// Attempts on every failing drain — this is the correct, existing behavior
// for a real failure, not a bug. This test proves that switching to a
// working Summarizer stub BEFORE maxSummaryAttempts is exhausted still
// drains every item losslessly, exactly once each.
//
// NOTE: unlike the disabled-config path above, this genuine-failure path
// COULD dead-letter a job if the fix arrives too late (after
// maxSummaryAttempts genuine failures) — that is precisely why the read-only
// diagnostic (CheckSummarizationQueue, internal/check) rather than an
// Attempts-bumping change is the correct fix for the "config not ready yet"
// case specifically: bumping Attempts here is desired behavior for a REAL
// failure, and must stay intact.
func TestDrain_EnabledUnresolvableThenLaterFixedIsZeroLoss(t *testing.T) {
	cwd := t.TempDir()

	const n = 3
	for i := range n {
		if err := EnqueueIterationSummary(cwd, "proj-unresolvable", 200+i); err != nil {
			t.Fatalf("EnqueueIterationSummary(%d): %v", i, err)
		}
	}

	// A distinguishable, genuine (non-ErrUnsupportedKind) transient-looking
	// failure — e.g. "enabled but the provider timed out" — for the first
	// two drain calls.
	genuineErr := errors.New("summarize: transient provider timeout (simulated)")
	failing := &fakeSummarizer{
		fn: func(ctx context.Context, item SummaryItem) (*SummaryResult, error) {
			return nil, genuineErr
		},
	}

	const failingRuns = 2 // well under maxSummaryAttempts
	if failingRuns >= maxSummaryAttempts {
		t.Fatalf("test setup is broken: failingRuns (%d) must be under maxSummaryAttempts (%d)", failingRuns, maxSummaryAttempts)
	}
	for run := range failingRuns {
		drained, err := DrainSummarizationQueue(context.Background(), cwd, failing, 0)
		if err != nil {
			t.Fatalf("run %d: DrainSummarizationQueue: %v", run, err)
		}
		if drained != 0 {
			t.Fatalf("run %d: drained = %d, want 0", run, drained)
		}

		remaining := plainJSONQueueFiles(t, cwd)
		if len(remaining) != n {
			t.Fatalf("run %d: plain *.json files = %v, want exactly %d", run, remaining, n)
		}
		// Unlike the unsupported-kind path, Attempts DOES increment here: a
		// genuine failure consumes a retry from maxSummaryAttempts's budget.
		for _, p := range remaining {
			want := run + 1
			if attempts := readQueueItemAttempts(t, p); attempts != want {
				t.Errorf("run %d: %s Attempts = %d, want %d (a genuine failure must consume a retry)", run, p, attempts, want)
			}
		}
		if failed, _ := filepath.Glob(filepath.Join(QueueDir(cwd), "*.failed")); len(failed) != 0 {
			t.Fatalf("run %d: unexpected dead-lettered files: %v", run, failed)
		}
	}

	// The config is "fixed" before maxSummaryAttempts is exhausted: swap in a
	// working stub Summarizer and drain the rest of the way.
	fake := &fakeSummarizer{}
	drained, err := DrainSummarizationQueue(context.Background(), cwd, fake, 0)
	if err != nil {
		t.Fatalf("final drain: DrainSummarizationQueue: %v", err)
	}
	if drained != n {
		t.Fatalf("final drain: drained = %d, want %d", drained, n)
	}
	if remaining := queueFiles(t, cwd); len(remaining) != 0 {
		t.Fatalf("final drain: queue dir = %v, want empty", remaining)
	}

	seen := map[int]int{}
	for _, item := range fake.calls {
		seen[item.Iteration]++
	}
	if len(seen) != n {
		t.Fatalf("distinct iterations summarized = %d, want %d; seen=%v", len(seen), n, seen)
	}
	for i := range n {
		iter := 200 + i
		if seen[iter] != 1 {
			t.Errorf("iteration %d was summarized %d time(s), want exactly 1; seen=%v", iter, seen[iter], seen)
		}
	}
}
