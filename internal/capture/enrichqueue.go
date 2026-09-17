// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/enrichment"
	"github.com/suykerbuyk/vibe-palace/internal/jobqueue"
	"github.com/suykerbuyk/vibe-palace/internal/notesummary"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
)

// enrichmentItem is one queued enrichment job. It carries everything the
// drain needs to re-run an LLM enrichment and rewrite the target note in
// place: the session coordinates, the note path (for diagnostics), and the
// extracted PromptInput captured at WriteSession time.
type enrichmentItem struct {
	Project   string                 `json:"project"`
	Date      string                 `json:"date"`
	Iteration int                    `json:"iteration"`
	NotePath  string                 `json:"note_path"`
	Prompt    enrichment.PromptInput `json:"prompt"`
	// Fingerprint is the writer fingerprint that scoped the target note's
	// host-scoped filename (see surface.WriterFingerprint). It is threaded to
	// the drain so ReadSession/RewriteSession resolve the right file. Legacy
	// queue items written before fingerprinting omit it; an empty value
	// resolves the legacy host-agnostic filename.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Attempts counts how many times the drain has tried and failed this job.
	// Once it reaches maxEnrichAttempts the job is dead-lettered instead of
	// retried, so a deterministically-failing job cannot loop forever.
	Attempts int `json:"attempts,omitempty"`
}

const (
	// maxEnrichAttempts caps drain retries before a job is moved to a .failed
	// dead-letter file. Bounds the cost of a permanently-failing job.
	maxEnrichAttempts = 5
	// staleProcessingAge is how long a claimed (.processing) job may sit before
	// it is presumed orphaned — a drainer that crashed mid-process — and is
	// reclaimed for retry. A live drain finishes an item in seconds.
	staleProcessingAge = 15 * time.Minute
)

// enrichmentQueueDir returns the host-local queue directory under the working
// directory's .vibe-palace sibling. This lives next to the hook claim
// sentinels and is already covered by the canonical /.vibe-palace/ gitignore
// pattern, so queued jobs are never committed to the project.
func enrichmentQueueDir(cwd string) string {
	return filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
}

// EnqueueEnrichment persists a single enrichment job to the host-local queue
// as <queueDir>/<date>-<fp>-<NN>.json (zero-padded iteration, matching the
// session-id format), or <queueDir>/<date>-<NN>.json when fp is empty. fp is
// the writer fingerprint scoping the target note; including it in the queue
// filename prevents the same cross-host collision in the queue directory. It
// is best-effort: the caller logs any error non-fatally so a failed enqueue
// never fails capture.
func EnqueueEnrichment(cwd, project, date, fp string, iteration int, notePath string, in enrichment.PromptInput) error {
	dir := enrichmentQueueDir(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create enrichment queue dir: %w", err)
	}

	item := enrichmentItem{
		Project:     project,
		Date:        date,
		Iteration:   iteration,
		NotePath:    notePath,
		Prompt:      in,
		Fingerprint: fp,
	}
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshal enrichment item: %w", err)
	}

	name := storage.SessionStem(date, fp, iteration) + ".json"
	path := filepath.Join(dir, name)
	if err := jobqueue.AtomicWrite(path, data); err != nil {
		return fmt.Errorf("write enrichment item: %w", err)
	}
	return nil
}

// DrainEnrichmentQueue processes queued enrichment jobs, rewriting each target
// note in place with LLM-synthesized summary/decisions/threads/tag. It is safe
// to call concurrently and idempotently:
//
//   - Each job is claimed via an atomic rename to "<item>.processing"; a
//     drainer that loses the race simply skips that item, so a job is never
//     double-processed [M4].
//   - A transient enrichment failure (API error or nil result) renames the
//     job back to its ".json" name so the next drain retries it; such jobs are
//     NOT counted as drained.
//   - The rewritten body is produced via buildSessionBody from the same
//     SessionParams the inline path uses, so an inline-enriched note and a
//     drained note converge to byte-identical bodies [M-B].
//   - meta.EnrichedAt is preserved when already set, so re-draining an
//     already-enriched note yields byte-identical file bytes.
//
// A nil enricher or a missing queue directory is a no-op returning (0, nil).
// max > 0 bounds how many jobs are processed this call; max <= 0 processes all.
func DrainEnrichmentQueue(ctx context.Context, vault *storage.Vault, cwd string, enricher *enrichment.Enricher, max int) (drained int, err error) {
	if enricher == nil {
		return 0, nil
	}

	dir := enrichmentQueueDir(cwd)

	// pendingRequeue collects one closure per item that fails this call,
	// deferred until this function returns (via the defer below) instead of
	// run immediately. A failed item's claim is deliberately left in its
	// ".processing" state for the REST of this call: jobqueue.Claim only
	// matches "*.json" files, so a still-".processing" claim is invisible to
	// it and cannot be immediately re-claimed ahead of other, not-yet-tried
	// distinct items.
	//
	// This matters because jobqueue.Claim always claims the
	// lexicographically-first claimable "*.json" file. An earlier version of
	// this migration rewrote a failed item back to its claimable name
	// IMMEDIATELY (inside requeue(), below) — which put it right back at the
	// front of the sort order, so the very next loop iteration's Claim call
	// re-claimed that SAME item again before any other, never-yet-tried item
	// ever got a turn. A `seen` map caught the resulting infinite
	// re-processing and stopped the loop, but stopping there meant a single
	// persistently-failing item (a genuinely broken job, or an LLM outage
	// failing every item) silently degraded an entire DrainEnrichmentQueue
	// call to "at most one attempt total, for the whole queue" — starving
	// every other queued item, including ones with nothing to do with the
	// failure — instead of draining up to `max` DISTINCT items as this
	// function's own doc comment promises. See
	// TestDrainSecondItemStillProcessedWhenFirstFails.
	//
	// Deferring the requeue until this call is about to return reproduces
	// the OLD (pre-migration) glob-snapshot code's fairness guarantee
	// exactly: that code computed its match list once at the top of the
	// call, so a mid-loop failure of item "a" never prevented "b" and "c"
	// from each still getting their own attempt in the same call. Here,
	// because a failed item stays claimed (as ".processing") until the very
	// end, every jobqueue.Claim call inside this loop is guaranteed to
	// return a genuinely new, never-before-attempted-this-call item (or
	// "" once none remain) — so `processed` can simply count claims, with
	// no seen-map needed at all.
	var pendingRequeue []func()
	defer func() {
		for _, fn := range pendingRequeue {
			fn()
		}
	}()

	processed := 0
	for {
		if max > 0 && processed >= max {
			break
		}

		// Claim via internal/jobqueue: an atomic rename to "<item>.processing",
		// reclaiming any stale (crashed-drainer) claims older than
		// staleProcessingAge first. A missing/empty dir or every job already
		// claimed by someone else is reported as ("", nil, nil) — treated like
		// io.EOF, not an error. Items held in pendingRequeue above are exactly
		// such ".processing" files, so they are correctly invisible here too.
		procPath, data, claimErr := jobqueue.Claim(dir, staleProcessingAge)
		if claimErr != nil {
			return drained, fmt.Errorf("enrichment drain: claim: %w", claimErr)
		}
		if procPath == "" {
			break
		}
		jsonPath := strings.TrimSuffix(procPath, jobqueue.ProcessingSuffix)
		processed++

		var item enrichmentItem
		if umErr := json.Unmarshal(data, &item); umErr != nil {
			slog.Warn("enrichment drain: corrupt item discarded", "err", umErr, "item", procPath)
			if doneErr := jobqueue.Done(procPath); doneErr != nil {
				slog.Warn("enrichment drain: removing corrupt item's claim failed; it will linger until reclaimStale picks it up", "err", doneErr, "item", procPath)
			}
			continue
		}

		// requeue defers the claimed job for a later attempt (see
		// pendingRequeue above), incrementing its attempt counter and
		// dead-lettering it once the cap is hit so a deterministically-failing
		// job cannot loop forever. The increment and dead-letter-cap check
		// happen at defer-flush time, not here — item.Attempts is captured by
		// reference (item is this closure's own enclosing loop variable), so
		// the value used is whatever it is when the deferred closure actually
		// runs, which is fine since nothing else mutates item after this
		// point in the same iteration.
		requeue := func() {
			pendingRequeue = append(pendingRequeue, func() {
				item.Attempts++
				reencode := func(int) ([]byte, error) { return json.Marshal(item) }
				deadLettered, rqErr := jobqueue.Requeue(procPath, item.Attempts, maxEnrichAttempts, reencode)
				if rqErr != nil {
					slog.Warn("enrichment drain: requeue failed", "err", rqErr, "item", procPath)
					return
				}
				if deadLettered {
					slog.Warn("enrichment drain: job exceeded max attempts; dead-lettered",
						"item", jsonPath+jobqueue.FailedSuffix, "attempts", item.Attempts, "project", item.Project)
				}
			})
		}

		res, eerr := enricher.Enrich(ctx, item.Prompt)
		if eerr != nil {
			slog.Warn("enrichment drain: enrich failed; will retry", "err", eerr, "project", item.Project)
			requeue()
			continue
		}

		meta, _, rerr := vault.ReadSession(item.Project, item.Date, item.Fingerprint, item.Iteration)
		if rerr != nil {
			slog.Warn("enrichment drain: read note failed; will retry", "err", rerr, "project", item.Project)
			requeue()
			continue
		}

		// Apply via the SAME merge policy as the inline path; an all-empty
		// result applies nothing and is treated as a transient miss to retry.
		if res == nil || !applyEnrichment(&meta, res, enricher.Model()) {
			slog.Warn("enrichment drain: no usable result; will retry", "project", item.Project)
			requeue()
			continue
		}

		// Reconstruct the body via the SAME builder + projection the inline path
		// uses so inline and drained notes converge to byte-identical bodies [M-B].
		body := buildSessionBody(paramsFromMeta(meta))

		normalized, rwErr := vault.RewriteSession(item.Project, item.Date, item.Fingerprint, item.Iteration, meta, body)
		if rwErr != nil {
			slog.Warn("enrichment drain: rewrite note failed; will retry", "err", rwErr, "project", item.Project)
			requeue()
			continue
		}
		// The drain owns the prose it just enriched, so it normalizes rather than
		// refusing — and reports it, because a silent repair is a silent edit.
		if len(normalized) > 0 {
			slog.Warn("enrichment drain: session note needed YAML repair to stay readable",
				"project", item.Project, "note", item.Date, "fields", strings.Join(normalized, ","))
		}

		// Post-enrichment length-gate re-check. internal/capture/session.go's
		// WriteSession evaluates notesummary.LengthGateBytes exactly ONCE, at
		// the initial synchronous capture, against whatever body existed at
		// that moment (inline enrichment included, since it runs earlier in
		// that same call). But THIS rewrite can grow a note's body well after
		// that one-shot check already ran and decided not to enqueue — a note
		// captured just under the gate has nothing left to ever re-evaluate
		// it, so without this it would permanently never be queued for
		// summarization. Reuses the SAME notesummary.LengthGateBytes constant
		// the capture-time gate uses (never a second, independently-defined
		// threshold) and is additive to this function's existing
		// claim/reclaim/requeue mechanics — a post-success side effect,
		// structurally the same shape as fileDecisionDrawers below: best-effort,
		// warn-logged on failure, never requeued (the note is already
		// correctly rewritten; requeueing here would re-run a real LLM
		// enrichment call — real money — over a bookkeeping-only failure).
		if len(body) > notesummary.LengthGateBytes {
			if qerr := summarize.EnqueueSessionSummary(cwd, item.Project, item.Date, item.Fingerprint, item.Iteration, item.NotePath); qerr != nil {
				slog.Warn("enrichment drain: session summary enqueue failed after async enrichment; this note will not be queued for summarization",
					"err", qerr, "project", item.Project)
			}
		}

		// File the freshly-enriched decisions into the palace. This is the
		// path that MATTERS for the hook: internal/hook/hook.go passes no
		// Decisions to WriteSession at all, so a hook-captured note has none
		// until an enrichment produces them — and when the inline enricher
		// misses, that happens HERE, not at capture. A WriteSession-only
		// ingest would file nothing for the entire hook production path.
		//
		// The stamp is the NOTE's day, never wall-clock. This is precisely the
		// path where the two come apart: a job enqueued at capture drains on a
		// LATER run — the hook drains at SessionEnd, which can be days after the
		// note was written — so time.Now() here would give Monday's decision
		// Thursday's stamp, and a query bounded by the session's own date would
		// not return it. meta is the note we just rewrote, so meta.Date is its
		// day; fileDecisionDrawers widens it. See DecisionFiledAt.
		//
		// ON ERROR: warn and CONTINUE. Deliberately NOT requeue(). The note was
		// already rewritten successfully a few lines up; requeueing would send
		// this job back through enricher.Enrich on the next drain — real LLM
		// work, real money — and rewrite an already-correct note, all because a
		// drawer append failed. The enrichment is done. A missing drawer is a
		// retrieval loss, not an enrichment loss, and it must not be paid for
		// by redoing the enrichment.
		if _, dferr := fileDecisionDrawers(vault, item.Project, meta.ID, meta.Date, meta.Decisions); dferr != nil {
			slog.Warn("enrichment drain: filing decision drawers failed; note is enriched but its decisions will not answer a palace query",
				"err", dferr, "project", item.Project, "note_path", item.NotePath)
		}

		if doneErr := jobqueue.Done(procPath); doneErr != nil {
			slog.Warn("enrichment drain: removing completed claim failed; the note is enriched but this item will be redundantly re-processed once reclaimStale picks it up", "err", doneErr, "item", procPath)
		}
		drained++
	}

	return drained, nil
}
