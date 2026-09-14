// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package summarize provides a host-local background queue for summarization
// jobs (per-iteration and per-session-note), plus the generic drain plumbing
// that processes them against a caller-supplied Summarizer. It is kept
// separate from internal/capture (whose enrichqueue.go it is modeled on)
// because summarization is a distinct concern from enrichment: it has its
// own queue directory, its own job shape, and its own injection point.
//
// This package defines the Summarizer interface but deliberately does not
// implement it — a later, separate piece of work supplies real (e.g.
// LLM-backed) implementations. DrainSummarizationQueue only knows how to
// move jobs through internal/jobqueue's Claim/Requeue/Done primitives and
// call whatever Summarizer it is given.
package summarize

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/jobqueue"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// SummaryJobKind identifies what a queued SummaryItem summarizes.
type SummaryJobKind string

const (
	// KindIteration summarizes a single iteration of a project.
	KindIteration SummaryJobKind = "iteration"
	// KindSessionNote summarizes a single captured session note.
	KindSessionNote SummaryJobKind = "session_note"
)

const (
	// maxSummaryAttempts caps drain retries before a job is dead-lettered to
	// a ".failed" file. Mirrors internal/capture/enrichqueue.go's
	// maxEnrichAttempts value so both queues share the same retry budget.
	maxSummaryAttempts = 5

	// staleSummaryProcessingAge is how long a claimed (.processing) job may
	// sit before it is presumed orphaned by a crashed drainer and reclaimed
	// for retry. Mirrors enrichqueue.go's staleProcessingAge.
	staleSummaryProcessingAge = 15 * time.Minute
)

// SummaryItem is one queued summarization job. It doubles as both the
// persisted queue-item shape (marshaled to JSON in the queue directory) and
// the parameter type of Summarizer.Summarize — there is deliberately only
// one type for both roles.
//
// Kind selects which of the kind-specific fields are populated:
//
//   - KindIteration uses Project and Iteration.
//   - KindSessionNote uses Project, Date, Fingerprint, Iteration and
//     NotePath, mirroring the session-coordinate fields
//     internal/capture/enrichqueue.go's enrichmentItem uses to locate a
//     note (minus anything enrichment-specific, such as a prompt payload).
type SummaryItem struct {
	// Kind selects which fields below apply.
	Kind SummaryJobKind `json:"kind"`
	// Project is the project slug this job belongs to. Used by both kinds.
	Project string `json:"project"`

	// Iteration is the iteration number. For KindIteration it is the
	// iteration being summarized; for KindSessionNote it is the iteration
	// component of the session's coordinates (reused rather than
	// duplicated).
	Iteration int `json:"iteration,omitempty"`

	// Date is the session's date, used only for KindSessionNote.
	Date string `json:"date,omitempty"`
	// Fingerprint is the writer fingerprint scoping the session note's
	// host-scoped filename, used only for KindSessionNote. See
	// surface.WriterFingerprint and enrichmentItem.Fingerprint.
	Fingerprint string `json:"fingerprint,omitempty"`
	// NotePath is the session note's path, used only for KindSessionNote.
	NotePath string `json:"note_path,omitempty"`

	// Attempts counts how many times the drain has tried and failed this
	// job. Once it reaches maxSummaryAttempts the job is dead-lettered
	// instead of retried, so a deterministically-failing job cannot loop
	// forever. This field is queue mechanics, shared by both kinds.
	Attempts int `json:"attempts,omitempty"`
}

// SummaryResult is an intentionally thin, opaque placeholder for whatever a
// concrete Summarizer produces. This package's drain loop only receives and
// discards it (aside from the counting/logging use of Kind) — it never
// inspects any other field, because the real result shape belongs to a
// later piece of work.
type SummaryResult struct {
	// Kind echoes the job kind that produced this result, useful for
	// logging or counting by callers. It carries no other meaning here.
	Kind SummaryJobKind
}

// Summarizer is the injection point for real summarization work — the
// summarize-package equivalent of enrichment.Enricher in
// internal/capture/enrichqueue.go. A later, separate piece of work supplies
// concrete implementations (e.g. LLM-backed); this package only defines the
// interface and the generic drain plumbing that calls it.
type Summarizer interface {
	Summarize(ctx context.Context, item SummaryItem) (*SummaryResult, error)
}

// QueueDir returns the host-local summarization queue directory,
// a sibling of internal/capture's enrichment-queue dir under the same
// .vibe-palace root. It is already covered by the canonical /.vibe-palace/
// gitignore pattern, so queued jobs are never committed to the project.
func QueueDir(cwd string) string {
	return filepath.Join(cwd, ".vibe-palace", "summarization-queue")
}

// queueFileName returns a DETERMINISTIC name for item, derived from its own
// identity rather than the time it was enqueued — mirroring
// internal/capture/enrichqueue.go's EnqueueEnrichment, whose queue file name
// is storage.SessionStem(date, fp, iteration) + ".json", not a random or
// time-based name. This is deliberate, not incidental: it is what makes
// re-enqueuing the same logical job (a session note captured twice at the
// same coordinates — e.g. an auto-capture followed by a later explicit
// capture that WriteSession's own UpsertSessionByKey resolves to the same
// key, "a retry carrying a known key rewrites that note in place") an
// idempotent overwrite of the SAME queue file, rather than a second,
// redundant queued job that a real Summarizer would separately re-process.
// Content is never embedded in a job item (only coordinates), so an
// overwritten job is not a lossy one: whichever copy runs re-reads the
// current content at drain time regardless.
func queueFileName(item SummaryItem) (string, error) {
	switch item.Kind {
	case KindSessionNote:
		return fmt.Sprintf("session-%s.json", storage.SessionStem(item.Date, item.Fingerprint, item.Iteration)), nil
	case KindIteration:
		return fmt.Sprintf("iteration-%d.json", item.Iteration), nil
	default:
		// Deliberately explicit, not a catch-all default falling through to
		// KindIteration's naming: an unrecognized Kind (a future third kind
		// added without updating this switch, a zero-value SummaryItem, a
		// typo) must fail loudly here rather than silently collide with the
		// iteration-<N>.json naming scheme using whatever stale Iteration
		// value it happens to carry.
		return "", fmt.Errorf("summarize: unknown SummaryJobKind %q", item.Kind)
	}
}

// enqueue is the shared write path for both Enqueue* helpers: marshal item to
// JSON and write it as a file in the summarization queue dir (creating the
// dir if needed), named deterministically by queueFileName so a repeat
// enqueue of the same job identity overwrites rather than duplicates.
func enqueue(cwd string, item SummaryItem) error {
	dir := QueueDir(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("summarize: create queue dir: %w", err)
	}

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("summarize: marshal item: %w", err)
	}

	name, err := queueFileName(item)
	if err != nil {
		return err
	}

	path := filepath.Join(dir, name)
	if err := jobqueue.AtomicWrite(path, data); err != nil {
		return fmt.Errorf("summarize: write item: %w", err)
	}
	return nil
}

// EnqueueIterationSummary persists a best-effort, cheap, synchronous
// KindIteration summarization job to the host-local queue. Any error is
// returned for the caller to log non-fatally; a failed enqueue must never
// fail the caller's own operation.
func EnqueueIterationSummary(cwd, project string, iter int) error {
	return enqueue(cwd, SummaryItem{
		Kind:      KindIteration,
		Project:   project,
		Iteration: iter,
	})
}

// EnqueueSessionSummary persists a best-effort, cheap, synchronous
// KindSessionNote summarization job to the host-local queue.
func EnqueueSessionSummary(cwd, project, date, fp string, iteration int, notePath string) error {
	return enqueue(cwd, SummaryItem{
		Kind:        KindSessionNote,
		Project:     project,
		Date:        date,
		Fingerprint: fp,
		Iteration:   iteration,
		NotePath:    notePath,
	})
}

// DrainSummarizationQueue processes queued summarization jobs by claiming
// each one (via internal/jobqueue) and calling s.Summarize. It is built
// directly on jobqueue's Claim/Requeue/Done primitives, so its own queue
// mechanics (staleness, atomic claiming, dead-lettering) are exactly
// jobqueue's.
//
// A nil Summarizer is a no-op returning (0, nil) immediately, mirroring
// DrainEnrichmentQueue's nil-enricher no-op in
// internal/capture/enrichqueue.go.
//
// Unlike DrainEnrichmentQueue, this function does not take a *storage.Vault:
// there is nothing vault-related for this generic loop to do. Any
// vault-cache write a concrete Summarizer needs happens inside that
// Summarizer, supplied later — not here.
//
// The loop claims up to max jobs (max <= 0 processes all claimable jobs) and
// stops early if ctx is canceled BEFORE claiming another job. On a
// successful Summarize (sErr == nil && res != nil), the job is marked Done
// and drained is incremented UNCONDITIONALLY — a success is never discarded
// or requeued merely because ctx happened to be canceled/deadline-exceeded
// in that same instant, since a future Summarizer with real side effects
// (e.g. writing a vault-committed summary) would otherwise silently
// duplicate that work on the next drain; ctx is only consulted AFTER
// recording the success, to decide whether to keep draining further jobs.
// On any non-success outcome, ctx.Err() is checked next: if ctx is done, the
// job is requeued with Attempts unchanged (it never got a fair,
// uninterrupted attempt) and the loop stops; otherwise it is a genuine
// failure — Requeue'd with a bumped Attempts count (capped at
// maxSummaryAttempts, beyond which it is dead-lettered by jobqueue) — and is
// NOT counted as drained.
func DrainSummarizationQueue(ctx context.Context, cwd string, s Summarizer, max int) (drained int, err error) {
	if s == nil {
		return 0, nil
	}

	dir := QueueDir(cwd)

	processed := 0
	for {
		if max > 0 && processed >= max {
			break
		}
		if ctx.Err() != nil {
			break
		}

		procPath, data, claimErr := jobqueue.Claim(dir, staleSummaryProcessingAge)
		if claimErr != nil {
			return drained, fmt.Errorf("summarize: claim: %w", claimErr)
		}
		if procPath == "" {
			// Nothing claimable: queue empty or every job already claimed.
			break
		}
		processed++

		var item SummaryItem
		if umErr := json.Unmarshal(data, &item); umErr != nil {
			// Corrupt item: nothing sane to retry with. Drop the claim
			// permanently rather than loop on it forever.
			_ = jobqueue.Done(procPath)
			continue
		}

		res, sErr := s.Summarize(ctx, item)
		if sErr == nil && res != nil {
			// SUCCESS TAKES PRIORITY OVER ctx's POST-CALL STATE. If this
			// drain's own ctx was canceled/deadline-exceeded in the same
			// instant Summarize finished successfully, the successful
			// result must still be honored — not discarded and requeued —
			// or a future Summarizer with real side effects (writing a
			// vault-committed summary, per this package's own design) would
			// silently DUPLICATE that work on the next drain. ctx is only
			// consulted AFTER the success is recorded, to decide whether to
			// keep draining further jobs, never to retroactively undo one
			// that already landed.
			//
			// A Done failure here is a bookkeeping cleanup issue, not a
			// summarization failure — the job's real work already succeeded.
			// Mirrors DrainEnrichmentQueue's own choice (internal/capture/
			// enrichqueue.go's `_ = os.Remove(procPath)`) to discard this
			// error and keep processing the rest of the batch, rather than
			// aborting every remaining claimable job in this call over one
			// transient I/O hiccup on a single already-completed job.
			_ = jobqueue.Done(procPath)
			drained++
			if ctx.Err() != nil {
				// Still respect ctx for not STARTING another claim — just
				// not for discarding the work that already finished above.
				break
			}
			continue
		}

		if ctx.Err() != nil {
			// This drain's OWN ctx was canceled/deadline-exceeded during (or
			// before) this NON-successful call — the job never got a fair,
			// uninterrupted attempt, so it must not consume a retry from
			// maxSummaryAttempts's budget (unlike the real-failure branch
			// below). Deliberately checked as ctx.Err() alone, independent of
			// whatever sErr happens to be, in both directions:
			//   - Not sErr == nil: a Summarizer that swallows the
			//     interruption and returns (nil, nil) instead of propagating
			//     it must still be recognized as interrupted, not charged as
			//     an ordinary failure.
			//   - Not errors.Is(sErr, context.Canceled/DeadlineExceeded): a
			//     Summarizer using its OWN internal per-call
			//     context.WithTimeout can return a context.DeadlineExceeded
			//     for a genuinely slow-but-failing job while THIS drain's own
			//     ctx is perfectly healthy — matching on the error's shape
			//     alone would misclassify that as an interruption. ctx.Err()
			//     is the one signal that is authoritative for "was THIS
			//     call's own ctx the reason it didn't finish," and can't be
			//     lost or spoofed in translation the way an error value can.
			// Requeue with Attempts unchanged and stop draining: ctx is no
			// longer usable for anything further in this call.
			_ = requeueItem(procPath, item)
			break
		}

		// Neither a success nor a ctx interruption: a genuine summarization
		// failure, which does consume a retry from maxSummaryAttempts's
		// budget.
		item.Attempts++
		_ = requeueItem(procPath, item)
		continue
	}

	return drained, nil
}

// requeueItem re-marshals item (whatever its caller has already set Attempts
// to) and hands it to jobqueue.Requeue. Shared by both DrainSummarizationQueue
// call sites (the ctx-interruption branch and the genuine-failure branch) so
// a future change to what gets persisted on a requeue cannot silently apply
// to only one of them.
func requeueItem(procPath string, item SummaryItem) error {
	reencode := func(int) ([]byte, error) {
		return json.Marshal(item)
	}
	_, err := jobqueue.Requeue(procPath, item.Attempts, maxSummaryAttempts, reencode)
	return err
}
