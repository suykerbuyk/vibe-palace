// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/itersummary"
	"github.com/suykerbuyk/vibe-palace/internal/jobqueue"
	"github.com/suykerbuyk/vibe-palace/internal/notesummary"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
)

// summarizationQueuePendingEscalationThreshold and
// summarizationQueueAgeEscalationThreshold are the two independent triggers
// for the "investigate why drain isn't keeping up" verdict below: either a
// large pending count OR an old oldest-pending file is, on its own, enough to
// suggest the drain loop itself (not the [summarization] config) is the
// problem. Picked as generous, not alarmist, defaults — a project that has
// [summarization] enabled and resolving has no legitimate reason to sit on
// either this many jobs or a job this old, but neither number is small
// enough to false-positive on an ordinary short-lived backlog (e.g. right
// after a `vp summarize backfill`-style burst enqueue).
const (
	summarizationQueuePendingEscalationThreshold = 50
	summarizationQueueAgeEscalationThreshold     = 24 * time.Hour
)

// CheckSummarizationQueue reports on the health of one project's host-local
// summarization queue (internal/summarize's
// <projectPath>/.vibe-palace/summarization-queue/) without ever touching the
// queue itself — this is a read-only diagnostic, not a drain.
//
// The queue growing without bound in a project that has never configured
// [summarization] is CORRECT, deliberate behavior (see
// summarize.ErrUnsupportedKind's own doc comment): DrainSummarizationQueue
// defers every KindIteration/KindSessionNote job back to the queue with
// Attempts genuinely unchanged, precisely so a later config fix can still
// drain it losslessly. That design means an operator staring at a queue
// directory with hundreds of files on disk cannot tell, from the file count
// alone, "this project just hasn't configured summarization yet" apart from
// "this project HAS configured it and the drain loop, the LLM provider, or
// the API key is genuinely broken." Those two situations call for opposite
// responses — do nothing vs. investigate now — and conflating them either
// trains operators to ignore a real backlog or sends them chasing a
// nonexistent bug in every project that simply hasn't opted in.
//
// This function resolves that ambiguity by combining three independent
// signals: the three-way file-count breakdown (pending / claimed / permanently
// dead-lettered), the age of the oldest pending job, and a live PROBE of
// whether the project's own [summarization] config would actually construct a
// working summarizer right now (via itersummary/notesummary's own
// NewSummarizerFromConfig constructors — the exact same construction
// `vp drain summaries` performs, just discarded here instead of used). It
// never drains, claims, or mutates a single file in the queue.
//
// This is always Info or Pass, NEVER Fail — mirroring this package's other
// report-only, operator-advisory checks (e.g. CheckStrayScaffolds,
// CheckResumeCaps). A large or old backlog is not, by itself, structural
// breakage the way a corrupt vault file or a broken merge driver is: it is
// EITHER expected (summarization not configured) OR something the operator
// must independently investigate against a real LLM provider (summarization
// configured but timing out, rate-limited, or misbehaving) — in neither case
// is failing this one cheap, local check the right way to surface it. A
// missing queue directory is treated as "empty", exactly like
// internal/tools/summarize_tools.go's TriggerSummarizationDrainTool treats a
// missing queue dir as "nothing to do" rather than an error.
func CheckSummarizationQueue(vault *storage.Vault, projectPath, slug string) Result {
	r := Result{Name: "Summarization queue"}

	dir := summarize.QueueDir(projectPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			r.Status = Info
			r.Summary = fmt.Sprintf("scan queue: %v", err)
			return r
		}
		entries = nil
	}

	// Bucket by suffix, LONGEST suffix first — an entry named
	// "iteration-00001.json.failed" or "iteration-00001.json.processing" ends
	// in ".json" too, so checking the plain ".json" suffix first would
	// double-count it as pending. jobqueue's own ProcessingSuffix/FailedSuffix
	// constants are used directly rather than re-declaring the literal
	// strings, so this stays in lockstep with jobqueue's actual naming.
	var pending, claimed, failed []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".json"+jobqueue.ProcessingSuffix):
			claimed = append(claimed, name)
		case strings.HasSuffix(name, ".json"+jobqueue.FailedSuffix):
			failed = append(failed, name)
		case strings.HasSuffix(name, ".json"):
			pending = append(pending, name)
		}
	}

	if len(pending) == 0 && len(claimed) == 0 && len(failed) == 0 {
		r.Status = Pass
		r.Summary = "empty"
		return r
	}

	// The backlog-age signal: the OLDEST pending file's mtime, not an
	// average and not the newest — a single ancient straggler is exactly the
	// kind of "drain isn't keeping up" evidence a mean or a newest-file mtime
	// would dilute or hide entirely.
	var oldest time.Time
	for _, name := range pending {
		info, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil {
			continue
		}
		if oldest.IsZero() || info.ModTime().Before(oldest) {
			oldest = info.ModTime()
		}
	}
	var oldestAge time.Duration
	if !oldest.IsZero() {
		oldestAge = time.Since(oldest)
	}

	r.Status = Info
	r.Details = append(r.Details,
		fmt.Sprintf("pending: %d", len(pending)),
		fmt.Sprintf("claimed (.processing): %d", len(claimed)),
		fmt.Sprintf("dead-lettered (.failed): %d", len(failed)),
	)
	if !oldest.IsZero() {
		r.Details = append(r.Details, fmt.Sprintf("oldest pending age: %s", oldestAge.Round(time.Second)))
	}

	cfg, cfgErr := vault.LoadConfig(slug)

	var clause string
	switch {
	case len(pending) == 0:
		// Nothing pending — the config/resolvability probe below has nothing
		// to diagnose FOR (claimed/dead-lettered entries are reported via
		// Details regardless of this clause).
		clause = "no pending jobs"
	case cfgErr != nil:
		// Advisory only, per this function's own never-Fail contract: a
		// config load error here means the probe below cannot run, not that
		// the queue itself is broken.
		clause = fmt.Sprintf("%d pending — could not resolve project config to diagnose further: %v", len(pending), cfgErr)
	case !cfg.Summarization.Enabled:
		// Mutually exclusive wording clause (a): distinguishable from (b) and
		// (c) by the substring "not configured".
		clause = fmt.Sprintf("%d pending — [summarization] is not configured for this project; this is an expected backlog, not a stuck queue", len(pending))
	default:
		// [summarization] is enabled — probe-construct both summarizers
		// exactly as `vp drain summaries` (cmd/vp/cmd_drain.go) does,
		// discarding the constructed value and keeping only whether
		// construction errored.
		_, iterErr := itersummary.NewIterationSummarizerFromConfig(cfg.Summarization, vault)
		_, noteErr := notesummary.NewSessionNoteSummarizerFromConfig(cfg.Summarization, vault)
		resolveErr := iterErr
		if resolveErr == nil {
			resolveErr = noteErr
		}
		switch {
		case resolveErr != nil:
			// Mutually exclusive wording clause (b): distinguishable from (a)
			// and (c) by the substring "enabled but unresolvable", and always
			// includes the real underlying error text.
			clause = fmt.Sprintf("%d pending — summarization enabled but unresolvable: %v", len(pending), resolveErr)
		case len(pending) > summarizationQueuePendingEscalationThreshold || oldestAge > summarizationQueueAgeEscalationThreshold:
			// Mutually exclusive wording clause (c): distinguishable from (a)
			// and (b) by the substring "investigate why drain isn't keeping
			// up".
			clause = fmt.Sprintf("%d pending — summarization is configured and resolving; investigate why drain isn't keeping up", len(pending))
		default:
			// Below both escalation thresholds and config resolves fine: a
			// mild, non-alarming Info, not the escalated wording above.
			clause = fmt.Sprintf("%d pending, summarization configured and resolving", len(pending))
		}
	}

	r.Summary = fmt.Sprintf("%s (%d claimed, %d dead-lettered)", clause, len(claimed), len(failed))

	// Additive, never a replacement branch: a nonzero dead-letter count is
	// surfaced here EVERY time it is nonzero, regardless of which clause
	// above fired for the pending count.
	if len(failed) > 0 {
		r.Details = append(r.Details, fmt.Sprintf(
			"%d job(s) permanently dead-lettered (.failed) — exhausted the retry budget; investigate independently of the pending-backlog verdict above",
			len(failed)))
	}

	return r
}
