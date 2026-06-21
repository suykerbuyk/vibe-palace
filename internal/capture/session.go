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

// SessionResult is the outcome of a successful session capture.
type SessionResult struct {
	Status        string
	Project       string
	NotePath      string
	Iteration     int
	SessionID     string
	FrictionScore int
}

// WriteSession performs the full capture pipeline: title defaulting,
// metadata population, archive resolution, friction analysis, body
// building, vault write, read-back, bidirectional archive linking,
// and optional transcript indexing. Both the MCP handler and the hook
// command call this.
func WriteSession(ctx context.Context, vault *storage.Vault, indexer *Indexer, p SessionParams) (*SessionResult, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if p.Summary == "" {
		return nil, fmt.Errorf("summary is required")
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
				slog.Warn("enrichment failed; using plain summary", "err", eerr, "project", p.Project)
			}
			// applyEnrichment overwrites only non-empty fields and reports
			// whether anything was actually applied; a nil or all-empty result
			// is a miss, so the note stays plain and the job is queued for retry.
			if res == nil || !applyEnrichment(&meta, res, p.Enricher.Model()) {
				enqueuePending = true
			}
		} else {
			slog.Warn("enrichment: extract prompt input failed", "err", err, "project", p.Project)
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
		if e, err := archive.ResolveEntry(vault.Root, p.Project, p.ArchiveSessionID); err == nil && e.Manifest.Adapter == adapter {
			archiveEntry = e
			meta.Archive = archive.VaultRelPath(vault.Root, e.ManifestPath)
		}
		// A missing archive is non-fatal -- capture still succeeds
		// without the link. Future hook runs that archive after
		// capture can call LinkSessionNote to close the loop.
	}

	// Score transcript friction before writing session.
	if p.Transcript != "" {
		if score, err := AnalyzeFriction(p.Transcript); err == nil {
			meta.FrictionScore = score
		}
	}

	body := buildSessionBody(paramsFromMeta(meta))

	sessionID, err := vault.WriteSession(p.Project, meta, body)
	if err != nil {
		return nil, fmt.Errorf("write session: %w", err)
	}

	// Parse iteration from sessionID (format: "YYYY-MM-DD-NN").
	iteration := ParseIteration(sessionID)

	// Read session back to get the note path.
	readMeta, _, readErr := vault.ReadSession(p.Project, date, iteration)
	notePath := ""
	if readErr == nil {
		notePath = readMeta.NotePath
	}

	// Enqueue-on-miss: when enrichment was attempted but produced nothing
	// usable, persist the extracted PromptInput so the Step-7 drain can retry
	// it asynchronously and rewrite this note in place. Enqueue is best-effort
	// — capture must never fail because the queue write did. Skipped when there
	// is no host dir (p.CWD == "", e.g. the pure MCP path).
	if enqueuePending && p.CWD != "" {
		if qerr := EnqueueEnrichment(p.CWD, p.Project, date, iteration, notePath, enqueueInput); qerr != nil {
			slog.Warn("enrichment: enqueue failed (non-fatal)", "err", qerr, "project", p.Project)
		}
	}

	// Close the bidirectional link: write vault_rel_session_note
	// into the archive manifest. Non-fatal on failure -- the
	// note's archive: field still provides one-way traversal.
	if archiveEntry != nil {
		sessionAbs, err := vault.SessionFile(p.Project, date, iteration)
		if err == nil {
			rel := archive.VaultRelPath(vault.Root, sessionAbs)
			_ = archive.LinkSessionNote(vault.Root, archiveEntry.ManifestPath, rel)
		}
	}

	// Index transcript if provided, unless NeedsIndexing signals deferral.
	// Per-call IndexStats are not surfaced through SessionResult today;
	// discard with `_` and revisit if dogfood-log telemetry needs them.
	if p.Transcript != "" && indexer != nil && !p.NeedsIndexing {
		if _, err := indexer.IndexTranscript(ctx, sessionID, p.Project, p.Transcript); err != nil {
			// Non-fatal: session was captured, indexing failed.
			return &SessionResult{
				Status:        "partial",
				Project:       p.Project,
				NotePath:      notePath,
				Iteration:     iteration,
				SessionID:     sessionID,
				FrictionScore: meta.FrictionScore,
			}, nil
		}
	}

	return &SessionResult{
		Status:        "ok",
		Project:       p.Project,
		NotePath:      notePath,
		Iteration:     iteration,
		SessionID:     sessionID,
		FrictionScore: meta.FrictionScore,
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
	result := ""
	for _, l := range lines {
		result += l + "\n"
	}
	return result
}

// ParseIteration extracts the iteration number from a session ID (format: "YYYY-MM-DD-NN").
func ParseIteration(sessionID string) int {
	parts := strings.Split(sessionID, "-")
	if len(parts) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}
