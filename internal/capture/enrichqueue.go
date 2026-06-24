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
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/enrichment"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
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
	if err := os.WriteFile(path, data, 0o644); err != nil {
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
	if _, statErr := os.Stat(dir); statErr != nil {
		if os.IsNotExist(statErr) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat enrichment queue: %w", statErr)
	}

	// Reclaim jobs orphaned by a drainer that crashed between claim and
	// completion, so they are retried rather than lost forever.
	reclaimStaleClaims(dir)

	matches, globErr := filepath.Glob(filepath.Join(dir, "*.json"))
	if globErr != nil {
		return 0, fmt.Errorf("glob enrichment queue: %w", globErr)
	}
	sort.Strings(matches)

	processed := 0
	for _, jsonPath := range matches {
		if max > 0 && processed >= max {
			break
		}

		// Claim via atomic rename. If this fails, another drainer won it or
		// the file vanished — skip without double-processing [M4]. Only a
		// successful claim consumes the budget, so lost races don't starve
		// genuinely-claimable jobs.
		procPath := jsonPath + ".processing"
		if renErr := os.Rename(jsonPath, procPath); renErr != nil {
			continue
		}
		processed++

		data, readErr := os.ReadFile(procPath)
		if readErr != nil {
			slog.Warn("enrichment drain: read claimed item failed", "err", readErr, "item", procPath)
			_ = os.Remove(procPath)
			continue
		}

		var item enrichmentItem
		if umErr := json.Unmarshal(data, &item); umErr != nil {
			slog.Warn("enrichment drain: corrupt item discarded", "err", umErr, "item", procPath)
			_ = os.Remove(procPath)
			continue
		}

		// requeue returns the claimed job for a later attempt, incrementing its
		// attempt counter and dead-lettering it once the cap is hit so a
		// deterministically-failing job cannot loop forever.
		requeue := func() {
			item.Attempts++
			if item.Attempts >= maxEnrichAttempts {
				deadPath := jsonPath + ".failed"
				if rnErr := os.Rename(procPath, deadPath); rnErr != nil {
					slog.Warn("enrichment drain: dead-letter rename failed", "err", rnErr, "item", procPath)
				} else {
					slog.Warn("enrichment drain: job exceeded max attempts; dead-lettered",
						"item", deadPath, "attempts", item.Attempts, "project", item.Project)
				}
				return
			}
			updated, mErr := json.Marshal(item)
			if mErr != nil {
				_ = os.Rename(procPath, jsonPath)
				return
			}
			if wErr := os.WriteFile(jsonPath, updated, 0o644); wErr != nil {
				_ = os.Rename(procPath, jsonPath)
				return
			}
			_ = os.Remove(procPath)
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

		if rwErr := vault.RewriteSession(item.Project, item.Date, item.Fingerprint, item.Iteration, meta, body); rwErr != nil {
			slog.Warn("enrichment drain: rewrite note failed; will retry", "err", rwErr, "project", item.Project)
			requeue()
			continue
		}

		_ = os.Remove(procPath)
		drained++
	}

	return drained, nil
}

// reclaimStaleClaims renames orphaned <id>.json.processing files — claimed by a
// drainer that crashed before finishing — back to <id>.json so they are retried,
// once they are older than staleProcessingAge. The main drain glob matches only
// *.json, so without this sweep a crashed claim would be lost forever.
func reclaimStaleClaims(dir string) {
	stale, err := filepath.Glob(filepath.Join(dir, "*.json.processing"))
	if err != nil {
		return
	}
	for _, p := range stale {
		info, statErr := os.Stat(p)
		if statErr != nil || time.Since(info.ModTime()) < staleProcessingAge {
			continue
		}
		target := strings.TrimSuffix(p, ".processing")
		if _, existsErr := os.Stat(target); existsErr == nil {
			// A fresh job already occupies the slot; drop the stale claim.
			_ = os.Remove(p)
			continue
		}
		if renErr := os.Rename(p, target); renErr != nil {
			slog.Warn("enrichment drain: reclaim stale claim failed", "err", renErr, "item", p)
			continue
		}
		slog.Info("enrichment drain: reclaimed stale claim", "item", target)
	}
}
