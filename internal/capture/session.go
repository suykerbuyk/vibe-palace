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

	"github.com/google/uuid"
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
	// ArchiveSessionIDSource records where ArchiveSessionID came from. The MCP
	// handler sets ArchiveIDSourceDerived when it resolved the id server-side
	// from the host's live session map; the hook pushes the id it was handed
	// and leaves this empty. Recorded into the note's frontmatter so an audit
	// can tell a derived link from a pushed one.
	ArchiveSessionIDSource string
	ArchiveAdapter         string
	CWD                    string // for claim sentinel (Phase 3 will use this)
	NeedsIndexing          bool   // when true, skip indexing (deferred to later)

	// Host is the MCP host application this capture is attributed to, and
	// HostSource records how the caller established it (the storage.HostSource*
	// constants). The MCP handler always sets both — derived from the initialize
	// handshake's clientInfo when confirmed, a caller-declared identity as
	// fallback, storage.HostUnknown when neither exists — and WriteSession
	// records them verbatim; it never invents a host.
	//
	// The hook path sets both too, from a DIFFERENT signal: it has no handshake,
	// so it reads the WIRE DIALECT of the payload it was handed and records the
	// explicit unknown pair when that dialect is indeterminate. Both paths write
	// the same vocabulary — a host application name — and an EMPTY Host still
	// means "this writer made no claim", a third state distinct from the explicit
	// unknown. See the task hook-path-writes-no-host-attribution.
	Host       string
	HostSource string

	// Entrypoint is the hook path's report of what CLAUDE_CODE_ENTRYPOINT held
	// in its process environment, recorded verbatim like Host. It answers "how
	// was the host invoked", NOT "which host" — see storage.SessionMeta for why
	// the two must not share a field. The MCP path leaves it empty: it has no
	// such measurement, and an empty value writes no key.
	Entrypoint string

	// SessionKey is the capture-attempt idempotency key. Supply the key a previous
	// capture RETURNED to update that same note in place; leave it empty to mint a
	// new note (and a new key, which the result reports back).
	//
	// It keys the CAPTURE ATTEMPT, not the session. An agent captures once per work
	// unit and a session holds several, so a session-scoped key would make each
	// work-unit capture overwrite the last. Capture must be idempotent under RETRY,
	// which is a different thing entirely — and retry is exactly what a hard failure
	// invites, which is why this exists before the MCP path is allowed to fail hard.
	SessionKey string

	// SessionKeySource, when non-empty alongside a non-empty SessionKey, is
	// recorded on the note VERBATIM in place of the source WriteSession would
	// otherwise infer. It exists for the inline-archive path, where the MCP
	// handler pre-mints the key so the archive and the note share one id —
	// without this override a handler-minted key would masquerade as
	// caller-supplied, and vaultaudit's backfill predicate keys on
	// session_key_source == "caller", so honest provenance is what correctly
	// excludes born-linked inline pairs from backfill recovery. Left empty,
	// behavior is unchanged: a supplied key is "caller", a minted one "minted".
	SessionKeySource string

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

	// Now is the capture-time instant. Zero means time.Now(). Calendar day is
	// derived from this instant via vault.CalendarDay — callers do not pass a
	// date string. A session_key retry still computes a day here; RewriteSession
	// pins identity to the original path, so a midnight-crossing retry does not
	// rename the note.
	Now time.Time
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
	// StageArchiveResolve DELETED at 210. A missing archive is a DEFERRED link, not
	// a lost stage: the hook does not archive until SessionEnd, so at capture time
	// the archive ordinarily does not exist yet, and hook.go now closes the loop
	// from the archive side once it does. The stage was reported on EVERY session
	// ever captured, which is what a structurally-unavoidable "failure" looks like.
	// Deleted rather than left unproduced: a stage nothing can emit is a claim the
	// failure payload was making and never honoring.
	StageArchiveAdapter  = "archive_adapter_mismatch"
	StageFrictionScoring = "friction_scoring"
	// StageTranscriptArchive: the inline transcript-archive write failed; the
	// note may exist without its archive pair.
	StageTranscriptArchive = "transcript_archive"
	// StageTranscriptArchiveUnreachable: this capture will NEVER have a
	// transcript archive, and nothing later can create one.
	//
	// 🔴 THIS IS NOT StageArchiveResolve COMING BACK. That stage was deleted at
	// 210 because it fired on every session ever captured: at capture time the
	// archive ordinarily does not exist YET, the hook writes it at SessionEnd,
	// and hook.go closes the link from the archive side. A missing archive is
	// still DEFERRED and still logs at Debug — do not change that.
	//
	// This stage is the disjoint case: the host was derived from the initialize
	// handshake as HOOK-LESS, so there is no SessionEnd to defer to, and no
	// transcript was supplied for the inline path to archive. Nothing is
	// pending; the archive is simply never coming. That is a wrong-thing, not a
	// not-yet — the same footing the adapter mismatch earns its loudness on.
	StageTranscriptArchiveUnreachable = "transcript_archive_unreachable"
	StageEnrichmentEnqueue            = "enrichment_enqueue"
	StageArchiveBacklink              = "archive_backlink"
	StageTranscriptIndex              = "transcript_index"
	StageClaimSentinel                = "claim_sentinel"
	StageEnricherInit                 = "enricher_init"
)

// The key-source and archive-id-source constants (KeySourceCaller,
// KeySourceMinted, ArchiveIDSourceDerived, ArchiveIDSourceBackfilled) live in
// package storage, beside the SessionMeta fields whose values they are — moved
// there at Phase 4 of capture-note-archive-link-never-closes, because the
// backfill writer needs them and storage cannot import capture. One home per
// value; a second definition here is how two readers of one field drift.

// SessionResult is the outcome of a session capture. A non-empty Failures means
// the note landed but something around it did not — see WriteSession's contract.
type SessionResult struct {
	Status        string
	Project       string
	NotePath      string
	Iteration     int
	SessionID     string
	FrictionScore int

	// SessionKey identifies this capture attempt. Pass it back to update this same
	// note rather than mint a second one — which is what makes a retry after a
	// hard failure safe. Updated reports whether this call rewrote an existing note
	// (true) or minted a new one (false).
	SessionKey       string
	SessionKeySource string
	Updated          bool

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

	now := p.Now
	if now.IsZero() {
		now = time.Now()
	}
	date := vault.CalendarDay(now)

	title := p.Title
	if title == "" {
		title = p.Summary
		if len(title) > 80 {
			title = title[:80]
		}
	}

	// Resolve the idempotency key. A caller-supplied key means "this is a retry of
	// an attempt you already told me about"; an empty one means a fresh attempt, so
	// the server mints the key and hands it back in the result. Minting here rather
	// than deriving it from the connection is deliberate: the MCP stdio session ID
	// is the compile-time constant "stdio", one vp mcp serves a whole Zed window,
	// and every attempt to infer identity from the transport has been wrong.
	sessionKey, keySource := p.SessionKey, storage.KeySourceCaller
	if sessionKey == "" {
		sessionKey, keySource = uuid.NewString(), storage.KeySourceMinted
	} else if p.SessionKeySource != "" {
		// The caller vouched for where the key came from — record it verbatim.
		// See SessionParams.SessionKeySource for why the inline-archive path
		// must not let a handler-minted key masquerade as caller-supplied.
		keySource = p.SessionKeySource
	}

	meta := storage.SessionMeta{
		Date:             date,
		SessionKey:       sessionKey,
		SessionKeySource: keySource,
		// Record WHICH HOST SESSION this note came from, unconditionally and BEFORE
		// the resolve attempt below. The archive almost never exists yet at capture
		// time -- the hook only archives at SessionEnd/PreCompact -- so a note that
		// cannot be linked NOW is exactly the note that must stay findable LATER,
		// when hook.go closes the loop from the archive side. Capture has always been
		// handed this value and always threw it away, which is why 105 of 417
		// manifests were stranded and no agent-written note was ever linked at all.
		ArchiveSessionID:       p.ArchiveSessionID,
		ArchiveSessionIDSource: p.ArchiveSessionIDSource,
		// Host attribution is recorded VERBATIM from the params — resolution
		// belongs to the CALLER, which is the only layer that can see the signal:
		// the MCP handler reads the transport handshake, the hook reads the wire
		// dialect of its payload. WriteSession never invents a host; empty means
		// that caller made no claim at all and the note carries no key.
		Host:         p.Host,
		HostSource:   p.HostSource,
		Entrypoint:   p.Entrypoint,
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
		// A missing archive is DEFERRED, NOT LOST (210). The archive usually does
		// not exist yet at capture time -- the hook only archives at SessionEnd /
		// PreCompact, while this runs at the first Stop (~90 s in) and at the agent's
		// mid-session wrap -- so "no archive found" is the ORDINARY case, not a
		// failure. The note records its ArchiveSessionID above, so the link is
		// recoverable, and hook.go closes it from the archive side once the archive
		// exists.
		//
		// This used to lose(StageArchiveResolve) and warn "transcript will not be
		// reachable from this note". That fired once per session, STRUCTURALLY, on
		// every session ever captured -- and once the loop actually closes it is also
		// FALSE: the transcript IS reachable, thirty minutes later. A warning that is
		// both unavoidable and untrue is how vp_health goes amber for work that
		// succeeded, and how agents learn to skim it.
		//
		// The instrument for "a link that never closed" is the vault AUDIT, which
		// measures the outcome on the real vault instead of predicting it here.
		//
		// The ADAPTER MISMATCH below stays a LOUD failure: that one is not a
		// not-yet, it is a wrong-thing -- it is how a Zed transcript goes missing
		// while capture reports success (see zed-pane-capture-parity).
		e, err := archive.ResolveEntry(vault.Root, p.Project, p.ArchiveSessionID)
		switch {
		case err != nil:
			slog.Debug("capture: no archive yet; link deferred to the archiving hook run",
				"archive_session_id", p.ArchiveSessionID, "adapter", adapter, "err", err)
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
	// Auto-capture is a crash net, not an interaction record: scoring its
	// early-Stop transcript measures bootstrap/resume keywords, not the session.
	// Leave FrictionScore unset and Breakdown nil (never-scored), not a measured zero.
	if p.Transcript != "" && p.Tag != storage.TagAutoCapture {
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
	//
	// Resolve-or-mint under a single lock: a retry carrying a known key rewrites
	// that note in place, anything else mints a new one.
	ref, updated, err := vault.UpsertSessionByKey(p.Project, sessionKey, meta, body, mergeCaptureMeta)
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
	//
	// LOCKING POSTURE: BLOCKING. LinkSessionNote holds the manifest's per-path
	// vaultlock across its read-modify-write (ADR-003); capture is fail-stop and
	// carries a request context, so waiting on a contended manifest is correct
	// here. internal/hook uses archive.TryLinkSessionNote instead because it has
	// no timeout at all — do not copy this call onto such a path.
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
		Status:           "ok",
		Project:          p.Project,
		NotePath:         ref.NotePath,
		Iteration:        ref.Iteration,
		SessionID:        ref.ID,
		FrictionScore:    meta.FrictionScore,
		SessionKey:       sessionKey,
		SessionKeySource: keySource,
		Updated:          updated,
		Failures:         failures,
	}, nil
}

// mergeCaptureMeta reconciles a re-capture against the note already on disk, and
// it exists because RewriteSession marshals EXACTLY what it is handed: every
// SessionMeta field is omitempty, so a field absent from the incoming meta is not
// "left alone", it is DELETED from the note.
//
// So the rule is: the new capture's narrative wins — that is the whole point of a
// retry, and the operator's framing was that the only thing which should vary
// between two captures of one attempt is the text the agent chose. But anything
// the new capture did not recompute is INHERITED rather than dropped:
//
//   - archive: a retry that could not resolve the archive must not erase the link
//     an earlier attempt succeeded in making.
//   - friction_breakdown: its nil is load-bearing (see storage.FrictionBreakdown —
//     nil means "never scored", non-nil-all-zero means "measured, frictionless").
//     Dropping it does not merely lose data, it LIES, converting a measured session
//     into one that looks like a legacy pre-breakdown note.
//   - enrichment: the async drain may have rewritten this note with an
//     LLM-synthesized narrative AFTER the capture that created it.
//
// An enriched narrative is preserved WHOLE — text and provenance together. If the
// note carries an LLM synthesis and this capture did not itself enrich, the
// synthesized summary/decisions/threads/tag are kept and the incoming heuristic
// ones dropped. Two real cases need exactly this: the async drain enriches a note
// AFTER the capture that created it, so a later re-capture would otherwise revert
// an LLM narrative to a git-log auto-summary; and a retry whose OWN enrichment pass
// failed must not destroy the good synthesis the first attempt produced. Text and
// provenance move as a unit, so enriched_by can never end up describing text the
// LLM did not write.
func mergeCaptureMeta(old storage.SessionMeta, _ string, next storage.SessionMeta) (storage.SessionMeta, string) {
	merged := next

	if merged.Archive == "" {
		merged.Archive = old.Archive
	}
	// Preserve the host session id across a retry that did not carry one. Without
	// this, a retry from a caller that omitted archive_session_id would ERASE the
	// note's only route back to its transcript -- the same silent-deletion shape
	// the Archive line above exists to prevent, and the reason RewriteSession is
	// never handed a fresh meta blind.
	if merged.ArchiveSessionID == "" {
		merged.ArchiveSessionID = old.ArchiveSessionID
		// The provenance travels with the id: inheriting the id but dropping its
		// source would strip the very marker that says how it was obtained.
		merged.ArchiveSessionIDSource = old.ArchiveSessionIDSource
	}
	if merged.Model == "" {
		merged.Model = old.Model
	}
	if merged.Breakdown == nil {
		merged.Breakdown = old.Breakdown
		merged.FrictionScore = old.FrictionScore
	}
	if merged.EnrichedBy == "" && old.EnrichedBy != "" {
		merged.Summary = old.Summary
		merged.Decisions = old.Decisions
		merged.OpenThreads = old.OpenThreads
		merged.Tag = old.Tag
		merged.EnrichedBy = old.EnrichedBy
		merged.EnrichedAt = old.EnrichedAt
	}

	// The body is REBUILT from the merged metadata rather than spliced from the old
	// text, which is exactly what the enrichment drain does (see DrainEnrichmentQueue)
	// — so an updated note and a freshly-minted one with the same content are
	// byte-identical, no matter which path produced them. This is safe precisely
	// because capture owns the whole body: buildSessionBody is the only thing that
	// ever writes one.
	return merged, buildSessionBody(paramsFromMeta(merged))
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
