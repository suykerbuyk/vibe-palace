// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/enrichment"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// SessionParams contains all inputs for a session capture. Both the MCP
// handler and the hook command populate this struct.
type SessionParams struct {
	Project          string
	Summary          string
	Title            string
	Tag              string
	Model            string
	Decisions        []string
	FilesChanged     []string
	OpenThreads      []string
	Transcript       string // raw transcript for friction analysis
	ArchiveSessionID string
	ArchiveAdapter   string
	CWD              string // for claim sentinel (Phase 3 will use this)
	NeedsIndexing    bool   // when true, skip indexing (deferred to later)

	// Enricher, when non-nil, drives a synchronous LLM enrichment pass over
	// Transcript before the note is written. This is intentionally a
	// dependency-carrying field, NOT a bool flag: a nil Enricher means
	// enrichment is disabled and capture proceeds with the plain heuristic
	// summary. Enrichment is always best-effort — a failure is warn-logged and
	// never fails the capture.
	Enricher *enrichment.Enricher

	// EnrichedBy is the enriching model name. WriteSession sets it internally
	// when an enrichment pass succeeds; it is also settable directly by the
	// Step-7 drain so the drain can reproduce the exact fenced body. A
	// non-empty EnrichedBy drives the provenance fence in buildSessionBody and
	// the enriched_by/enriched_at frontmatter fields.
	EnrichedBy string
}

// CaptureFailure names one thing the capture pipeline could not do. Stage is a
// stable machine-readable identifier (see the capture*Stage constants); Err is
// the underlying error's text.
type CaptureFailure struct {
	Stage string `json:"stage"`
	Err   string `json:"err"`
}

// The stages a capture can lose. These are stable identifiers: they are
// reported to agents in the failure payload, so renaming one is a surface
// change, not a refactor.
const (
	StageEnrichmentExtract = "enrichment_extract"
	StageEnrichment        = "enrichment"
	StageArchiveResolve    = "archive_resolve"
	StageArchiveAdapter    = "archive_adapter_mismatch"
	StageFrictionScoring   = "friction_scoring"
	StageEnrichmentEnqueue = "enrichment_enqueue"
	StageArchiveBacklink   = "archive_backlink"
	StageTranscriptIndex   = "transcript_index"
	StageClaimSentinel     = "claim_sentinel"
	StageEnricherInit      = "enricher_init"
)

// SessionResult is the outcome of a session capture. A non-empty Failures means
// the note landed but something around it did not — see WriteSession's contract.
type SessionResult struct {
	Status        string
	Project       string
	NotePath      string
	Iteration     int
	SessionID     string
	FrictionScore int

	// Failures lists every part of the pipeline that was lost. The note itself
	// is NOT among them: if WriteSession returns a non-nil result, the note is on
	// disk. Callers decide what a loss means — the MCP path raises it to the
	// agent as an error it can retry, the hook path logs it and exits zero.
	Failures []CaptureFailure
}

// Failed reports whether any part of the capture was lost.
func (r *SessionResult) Failed() bool { return len(r.Failures) > 0 }

// WriteSession performs the full capture pipeline: title defaulting,
// metadata population, archive resolution, friction analysis, body
// building, vault write, bidirectional archive linking, and optional
// transcript indexing. Both the MCP handler and the hook command call this.
//
// THE NOTE ALWAYS LANDS. Every stage but the vault write itself is
// ACCUMULATE-AND-CONTINUE: a failure is warn-logged, appended to
// result.Failures, and the pipeline proceeds. This is a hard structural rule,
// not a style preference — three of the losable stages (enrichment, archive
// resolve, friction scoring) feed the frontmatter and therefore run BEFORE the
// note exists, so an early return on any of them would write no note at all and
// lose the session entirely. Losing the session is strictly worse than losing an
// archive link, which is the whole point: capture's one irreplaceable output is
// the note. There is exactly ONE fatal error in this function, and it is
// storage.WriteSessionRef failing — at which point there is nothing to salvage.
//
// A non-nil result with a non-empty Failures therefore means: the note is safe
// on disk, and these peripheral things were lost. Callers decide what that is
// worth (the MCP path errors so the agent can retry; the hook path logs and
// exits zero — it has no reader and its only hard-failure primitive, exit 2, is
// Claude Code's blocking-error code on a hook that fires once per turn).
func WriteSession(ctx context.Context, vault *storage.Vault, indexer *Indexer, p SessionParams) (*SessionResult, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if p.Summary == "" {
		return nil, fmt.Errorf("summary is required")
	}

	var failures []CaptureFailure
	lose := func(stage string, err error, msg string, args ...any) {
		failures = append(failures, CaptureFailure{Stage: stage, Err: err.Error()})
		slog.Warn(msg, append([]any{"err", err, "stage", stage, "project", p.Project}, args...)...)
	}

	now := time.Now().UTC()
	date := now.Format("2006-01-02")

	title := p.Title
	if title == "" {
		title = p.Summary
		if len(title) > 80 {
			title = title[:80]
		}
	}

	meta := storage.SessionMeta{
		Date:         date,
		Title:        title,
		Summary:      p.Summary,
		Tag:          p.Tag,
		Model:        p.Model,
		Decisions:    p.Decisions,
		FilesChanged: p.FilesChanged,
		OpenThreads:  p.OpenThreads,
	}

	// Synchronous LLM enrichment phase. A non-nil Enricher plus a transcript
	// refines the heuristic summary/decisions/threads/tag in meta. Always
	// best-effort: any failure is warn-logged and capture proceeds with the
	// plain auto-summary (capture must never fail because enrichment did).
	// enqueuePending records that an enrichment was attempted (extraction
	// succeeded, a transcript was available) but produced nothing usable — the
	// API errored, returned nil, or returned an all-empty result. The note is
	// written plain and the extracted PromptInput is enqueued (after the write,
	// once the iteration is known) for the drain to retry asynchronously.
	var enqueuePending bool
	var enqueueInput enrichment.PromptInput
	if p.Enricher != nil && p.Transcript != "" {
		in, err := enrichment.ExtractPromptInput([]byte(p.Transcript))
		if err == nil {
			if len(p.FilesChanged) > 0 {
				in.FilesChanged = p.FilesChanged // git-derived list is authoritative
			}
			in.NarrativeSummary = p.Summary // the heuristic auto-summary as refinement context
			in.NarrativeTag = p.Tag
			enqueueInput = in
			res, eerr := p.Enricher.Enrich(ctx, in)
			if eerr != nil {
				lose(StageEnrichment, eerr, "capture: enrichment failed; note keeps the plain summary")
			}
			// applyEnrichment overwrites only non-empty fields and reports
			// whether anything was actually applied; a nil or all-empty result
			// is a miss, so the note stays plain and the job is queued for retry.
			if res == nil || !applyEnrichment(&meta, res, p.Enricher.Model()) {
				enqueuePending = true
			}
		} else {
			lose(StageEnrichmentExtract, err, "capture: enrichment extract failed; note keeps the plain summary")
		}
	}

	// Resolve the archive (if requested) before writing the session
	// so the note can carry the archive: frontmatter field. The
	// note -> manifest back-link is closed after the write below.
	var archiveEntry *archive.Entry
	if p.ArchiveSessionID != "" {
		adapter := p.ArchiveAdapter
		if adapter == "" {
			adapter = archive.ClaudeCodeAdapterName
		}
		// A missing or adapter-mismatched archive is non-fatal -- capture still
		// succeeds without the link, and a future hook run that archives after
		// capture can call LinkSessionNote to close the loop. But it is NOT
		// silent: this branch had no else, so a session that asked to be linked
		// to a transcript and was not got an identical "ok" to one that was.
		// The adapter mismatch is the one that matters -- it is how a Zed
		// transcript goes missing while capture reports success.
		e, err := archive.ResolveEntry(vault.Root, p.Project, p.ArchiveSessionID)
		switch {
		case err != nil:
			lose(StageArchiveResolve, err, "capture: archive not linked; transcript will not be reachable from this note",
				"archive_session_id", p.ArchiveSessionID, "adapter", adapter)
		case e.Manifest.Adapter != adapter:
			lose(StageArchiveAdapter,
				fmt.Errorf("archive adapter is %q, want %q", e.Manifest.Adapter, adapter),
				"capture: archive not linked; adapter mismatch",
				"archive_session_id", p.ArchiveSessionID)
		default:
			archiveEntry = e
			meta.Archive = archive.VaultRelPath(vault.Root, e.ManifestPath)
		}
	}

	// Score transcript friction before writing session.
	if p.Transcript != "" {
		b, err := AnalyzeFrictionBreakdown(p.Transcript)
		if err != nil {
			// Was swallowed by an `if err == nil` with no else: the note kept a
			// zero friction score, which is indistinguishable from a genuinely
			// frictionless session, and every friction trend silently averaged
			// the zero in.
			lose(StageFrictionScoring, err, "capture: friction scoring failed; note will carry no friction score")
		} else {
			meta.FrictionScore = b.Total()
			meta.Breakdown = b
		}
	}

	body := buildSessionBody(paramsFromMeta(meta))

	// THE ONE FATAL ERROR. Past this line the note is on disk and every
	// subsequent failure is accumulated, never returned: see the contract above.
	ref, err := vault.WriteSessionRef(p.Project, meta, body)
	if err != nil {
		return nil, fmt.Errorf("write session: %w", err)
	}

	// Enqueue-on-miss: when enrichment was attempted but produced nothing
	// usable, persist the extracted PromptInput so the Step-7 drain can retry
	// it asynchronously and rewrite this note in place. Enqueue is best-effort
	// — capture must never fail because the queue write did. Skipped when there
	// is no host dir (p.CWD == "", e.g. the pure MCP path).
	if enqueuePending && p.CWD != "" {
		if qerr := EnqueueEnrichment(p.CWD, p.Project, ref.Date, ref.Fingerprint, ref.Iteration, ref.NotePath, enqueueInput); qerr != nil {
			lose(StageEnrichmentEnqueue, qerr, "capture: enrichment enqueue failed; this note will not be retried by the drain")
		}
	}

	// Close the bidirectional link: write vault_rel_session_note into the archive
	// manifest. Non-fatal on failure -- the note's archive: field still provides
	// one-way traversal -- but no longer silent. Both halves used to discard
	// their error: the SessionFile error skipped the link with no trace, and
	// LinkSessionNote's was thrown away with `_ =`. The manifest back-link is
	// what makes a transcript discoverable FROM the archive side, so losing it
	// quietly is how a transcript becomes unreachable while capture says ok.
	if archiveEntry != nil {
		if err := archive.LinkSessionNote(vault.Root, archiveEntry.ManifestPath, ref.NotePath); err != nil {
			lose(StageArchiveBacklink, err, "capture: archive manifest back-link failed; transcript is reachable from the note but not the reverse",
				"manifest", archiveEntry.ManifestPath, "note_path", ref.NotePath)
		}
	}

	// Index transcript if provided, unless NeedsIndexing signals deferral.
	// Per-call IndexStats are not surfaced through SessionResult today;
	// discard with `_` and revisit if dogfood-log telemetry needs them.
	//
	// This used to early-return Status "partial". That tier is gone: a soft
	// status is one agents learn to skim past, and it was the only status that
	// ever reported a loss — every other loss on this path returned a flat "ok".
	// An index failure is now exactly what it is, one accumulated failure among
	// the rest, and the note it belongs to has already landed.
	if p.Transcript != "" && indexer != nil && !p.NeedsIndexing {
		if _, err := indexer.IndexTranscript(ctx, ref.ID, p.Project, p.Transcript); err != nil {
			lose(StageTranscriptIndex, err, "capture: transcript indexing failed; this session will not be semantically searchable")
		}
	}

	return &SessionResult{
		Status:        "ok",
		Project:       p.Project,
		NotePath:      ref.NotePath,
		Iteration:     ref.Iteration,
		SessionID:     ref.ID,
		FrictionScore: meta.FrictionScore,
		Failures:      failures,
	}, nil
}

// applyEnrichment overwrites meta's heuristic fields with the non-empty values
// from an enrichment result and, when at least one field was applied, stamps the
// enriching model and timestamp. It returns whether any field was applied: an
// all-empty result applies nothing and leaves the note a plain capture (so the
// caller can queue a retry rather than falsely marking the note enriched). Both
// the inline capture path and the async drain call this so they converge.
func applyEnrichment(meta *storage.SessionMeta, res *enrichment.Result, model string) bool {
	applied := false
	if res.Summary != "" {
		meta.Summary = res.Summary
		applied = true
	}
	if len(res.Decisions) > 0 {
		meta.Decisions = res.Decisions
		applied = true
	}
	if len(res.OpenThreads) > 0 {
		meta.OpenThreads = res.OpenThreads
		applied = true
	}
	if res.Tag != "" {
		meta.Tag = res.Tag
		applied = true
	}
	if applied {
		meta.EnrichedBy = model
		// Preserve an existing timestamp so a re-drain is byte-identical; only
		// stamp a fresh one on the first successful enrichment.
		if meta.EnrichedAt == "" {
			meta.EnrichedAt = time.Now().UTC().Format(time.RFC3339)
		}
	}
	return applied
}

// paramsFromMeta projects the fields buildSessionBody consumes out of a
// SessionMeta, so the inline write path and the async drain build the note body
// from the single same source — keeping enriched notes byte-identical no matter
// which path produced them.
func paramsFromMeta(meta storage.SessionMeta) SessionParams {
	return SessionParams{
		Summary:      meta.Summary,
		Decisions:    meta.Decisions,
		FilesChanged: meta.FilesChanged,
		OpenThreads:  meta.OpenThreads,
		EnrichedBy:   meta.EnrichedBy,
	}
}

// buildSessionBody assembles the markdown body for a session file.
func buildSessionBody(p SessionParams) string {
	var b []string
	b = append(b, "\n## Summary\n")
	b = append(b, p.Summary)

	if len(p.Decisions) > 0 {
		b = append(b, "\n\n## Decisions\n")
		for _, d := range p.Decisions {
			b = append(b, "- "+d)
		}
	}

	if len(p.FilesChanged) > 0 {
		b = append(b, "\n\n## Files Changed\n")
		for _, f := range p.FilesChanged {
			b = append(b, "- `"+f+"`")
		}
	}

	if len(p.OpenThreads) > 0 {
		b = append(b, "\n\n## Open Threads\n")
		for _, t := range p.OpenThreads {
			b = append(b, "- "+t)
		}
	}

	// When the session was LLM-enriched, wrap the whole assembled body in a
	// provenance fence (HTML-comment markers, matching internal/shims/shim.go).
	// Wrapping the complete body — rather than individual sections — keeps
	// section ordering identical and lets the Step-7 drain reproduce a
	// byte-identical body by reconstructing the same params with EnrichedBy set.
	// When EnrichedBy is empty the output is byte-identical to the unenriched
	// path: no fence, no footer.
	if p.EnrichedBy != "" {
		fenced := []string{"<!-- enriched -->"}
		fenced = append(fenced, b...)
		fenced = append(fenced, "<!-- /enriched -->", "", "*enriched by "+p.EnrichedBy+"*")
		b = fenced
	}

	return joinLines(b)
}

func joinLines(lines []string) string {
	var result strings.Builder
	for _, l := range lines {
		result.WriteString(l)
		result.WriteString("\n")
	}
	return result.String()
}

// ParseIteration extracts the iteration number from a session ID. It accepts
// both the host-scoped form "YYYY-MM-DD-<fp>-NN" (5 hyphen segments) and the
// legacy form "YYYY-MM-DD-NN" (4 segments): in both, NN is the final segment.
func ParseIteration(sessionID string) int {
	parts := strings.Split(sessionID, "-")
	if len(parts) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}

// ParseFingerprint extracts the writer fingerprint from a session ID. It
// returns the fp for the host-scoped form "YYYY-MM-DD-<fp>-NN" (5 segments)
// and "" for the legacy host-agnostic form "YYYY-MM-DD-NN" (4 segments). The
// empty string is the correct fp to feed back into the storage read API for a
// legacy note, so a parse miss degrades to legacy resolution.
func ParseFingerprint(sessionID string) string {
	parts := strings.Split(sessionID, "-")
	if len(parts) < 5 {
		return ""
	}
	return parts[3]
}
