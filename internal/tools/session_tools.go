// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/hostsession"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// hostSessionID resolves the live session id of the host process that spawned
// this server, or "" when it cannot be CONFIRMED. It is a var so handler tests
// can stub the derivation. The default reads Claude Code's per-process session
// map keyed by this process's parent pid — the server is a DIRECT child of the
// host (verified live: no intermediate shell) — guarded against pid reuse by
// /proc start-time. Non-Claude hosts (Zed, HTTP serve) simply have no such
// file and resolve to "": an honest unknown, never a guess.
var hostSessionID = func() string {
	home, err := archive.ClaudeHome()
	if err != nil {
		return ""
	}
	return hostsession.ClaudeSessionID(os.Getppid(), home, hostsession.ReadProcStart)
}

// clientInfoHost resolves the host name the MCP initialize handshake recorded
// on the client session flowing in ctx, or "" when it cannot be CONFIRMED (no
// session on the context, a session without clientInfo, or an unnamed client).
// It is a var so handler tests can stub the derivation, mirroring
// hostSessionID above. "" is an honest absent, never a guess.
var clientInfoHost = func(ctx context.Context) string {
	if info, ok := mcp.ClientInfoFromContext(ctx); ok {
		return info.Name
	}
	return ""
}

// declaredClientName extracts the client name from a caller-supplied
// client_info parameter. The canonical shape is the initialize clientInfo
// object ({"name": ...}); a bare JSON string is tolerated because the field
// exists for wire compatibility with hosts whose exact shape we do not
// control. Anything else — absent, null, unparseable, or nameless — is "",
// an honest absent.
func declaredClientName(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var impl struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &impl); err == nil {
		return strings.TrimSpace(impl.Name)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

// resolveCaptureHost settles the host attribution for one capture:
// DERIVED-WINS, declared as fallback, explicit unknown when neither exists.
//
// The handshake-derived value is authoritative because the transport saw the
// client name itself; a caller-declared client_info could claim anything, so
// it is honored only when there is nothing derived, and its provenance says
// "declared" so a reader can always tell. When a declared claim loses to the
// derived value, the override is logged so the discarded claim is still
// recorded somewhere. Per ADR-006 the empty case records storage.HostUnknown,
// never a default host: absence is not a value, and a fabricated host is
// indistinguishable from a measured one.
func resolveCaptureHost(ctx context.Context, clientInfo json.RawMessage) (host, source string) {
	derived := strings.TrimSpace(clientInfoHost(ctx))
	declared := declaredClientName(clientInfo)
	switch {
	case derived != "":
		if declared != "" && declared != derived {
			slog.Debug("vp_capture_session: caller-declared client_info loses to handshake-derived host",
				"derived", derived, "declared", declared)
		}
		return derived, storage.HostSourceDerived
	case declared != "":
		return declared, storage.HostSourceDeclared
	default:
		return storage.HostUnknown, storage.HostSourceUnknown
	}
}

// isHooklessClient reports whether host names a known non-Claude MCP client
// that has no SessionEnd hook and therefore needs inline transcript archive
// for durable capture.
//
// Match is case-insensitive **token** equality against a closed allow-list
// (grok, xai, zed), where tokens are letter/digit runs split on any other
// rune. Substring Contains is deliberately rejected: clientInfo.name is
// vendor-chosen, and English words like "optimized" / "authorized" embed
// "zed". Claude-family names never match — empty host session id on Claude
// is a derivation-miss, not hook-lessness, and auto-inlining there dual-notes
// against SessionEnd. The claude guard is load-bearing when an allow-list
// entry could overlap a compound name (e.g. "claude-grok-bridge").
func isHooklessClient(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	// Defensive: never auto-treat anything Claude-shaped as hook-less even if
	// a future allow-list entry accidentally overlaps a compound name.
	if strings.Contains(h, "claude") {
		return false
	}
	for _, tok := range strings.FieldsFunc(h, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		switch tok {
		case "grok", "xai", "zed":
			return true
		}
	}
	return false
}

// wantInlineTranscriptArchive decides whether this capture should create an
// inline archive pair before WriteSession. Requires empty derived host session
// id and a non-empty transcript, plus either an explicit archive_transcript
// force or a positive hook-less signal (handshake-derived host in the known
// hook-less set). Omit/false does not force; auto still applies on the
// predicate. See Decision (2026-07-28) on capture-defaults-for-hookless-hosts.
func wantInlineTranscriptArchive(force bool, archiveSessionID, transcript, host, hostSource string) bool {
	if archiveSessionID != "" || strings.TrimSpace(transcript) == "" {
		return false
	}
	if force {
		return true
	}
	return hostSource == storage.HostSourceDerived && isHooklessClient(host)
}

// flexStringList unmarshals from either a JSON array of strings or a single JSON
// string. Some MCP clients (Grok's capture shim, observed 2026-07-21) send the
// list-valued capture fields — decisions, files_changed, open_threads — as one
// string rather than an array; before this the strict "type: array" schema
// rejected the whole vp_capture_session call at validation and the session was
// LOST. A lone string is coerced to a one-element list (never split on a guessed
// delimiter); an empty string or null yields an empty list. Its underlying type
// is []string, so it assigns straight into capture.SessionParams.
type flexStringList []string

func (f *flexStringList) UnmarshalJSON(data []byte) error {
	switch trimmed := strings.TrimSpace(string(data)); {
	case trimmed == "" || trimmed == "null":
		*f = nil
	case trimmed[0] == '[':
		var arr []string
		if err := json.Unmarshal(data, &arr); err != nil {
			return err
		}
		*f = arr
	default:
		// A lone JSON string → one-element list; empty string → empty list.
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if s == "" {
			*f = nil
		} else {
			*f = flexStringList{s}
		}
	}
	return nil
}

type captureSessionParams struct {
	Project      string         `json:"project"`
	Summary      string         `json:"summary"`
	Title        string         `json:"title,omitempty"`
	Tag          string         `json:"tag,omitempty"`
	Model        string         `json:"model,omitempty"`
	Decisions    flexStringList `json:"decisions,omitempty"`
	FilesChanged flexStringList `json:"files_changed,omitempty"`
	OpenThreads  flexStringList `json:"open_threads,omitempty"`
	Transcript   string         `json:"transcript,omitempty"`
	// ArchiveSessionID is accepted for wire compatibility and IGNORED. The
	// handler derives the host session id itself (see hostSessionID) and never
	// trusts a caller-supplied one: the only caller of this tool is the agent,
	// and agents are demonstrably fed WRONG ids in their context (the commit
	// template's bridge session id, a CLAUDE_CODE_SESSION_ID frozen across
	// /clear). The archive linker matches on the stamped id alone, so a wrong
	// id would mis-link ANOTHER session's transcript — the id must be
	// correct-or-absent, never best-effort-possibly-wrong.
	ArchiveSessionID string `json:"archive_session_id,omitempty"`
	// ArchiveAdapter names the adapter to resolve against. Defaults
	// to claude-code when ArchiveSessionID is set and this is empty.
	ArchiveAdapter string `json:"archive_adapter,omitempty"`
	// ArchiveTranscript forces an INLINE transcript archive when the server
	// cannot derive a host session id and Transcript is non-empty. On a
	// derivable host it is a no-op — the SessionEnd hook owns the
	// authoritative archive. Omit/false does not force; the handler still
	// auto-enables for handshake-derived hook-less clients (grok/xai/zed)
	// under the same empty-id + transcript gate. Requires non-empty Transcript.
	ArchiveTranscript bool `json:"archive_transcript,omitempty"`
	// CWD is the working directory for writing a claim sentinel so
	// the SessionEnd hook skips sessions already captured via MCP.
	CWD string `json:"cwd,omitempty"`
	// Enrich opts this capture into an LLM synthesis pass over the
	// transcript (refining summary/decisions/threads/tag) when [enrichment]
	// is enabled in config. Default false: agent-authored notes are already
	// good, so enrichment is opt-in for the MCP path.
	Enrich bool `json:"enrich,omitempty"`
	// SessionKey is the idempotency key of a capture attempt. Pass back the
	// session_key a previous call returned to RETRY that attempt: the note is
	// updated in place instead of duplicated. Omit it for new work.
	SessionKey string `json:"session_key,omitempty"`
	// ClientInfo is a caller-DECLARED client identity, accepted for wire
	// compatibility with hosts that supply it ({"name": ...} in the initialize
	// clientInfo shape; a bare string is tolerated). It NEVER overrides the
	// server's own derivation: the handler reads the host from the initialize
	// handshake's clientInfo on the live session (mcp.ClientInfoFromContext),
	// and that derived value is authoritative — any caller could claim any
	// host, and a declared claim recorded indistinguishably from a measured
	// one is exactly the fabricated-value failure ADR-006 forbids. A declared
	// identity is honored only when no handshake identity exists, and then the
	// note's host_source says "declared" so a reader can tell them apart.
	ClientInfo json.RawMessage `json:"client_info,omitempty"`
}

type captureSessionResult struct {
	Status        string `json:"status"`
	Project       string `json:"project"`
	NotePath      string `json:"note_path"`
	Iteration     int    `json:"iteration"`
	SessionID     string `json:"session_id"`
	FrictionScore int    `json:"friction_score,omitempty"`
	// SessionKey identifies this capture attempt; pass it back to retry.
	SessionKey string `json:"session_key"`
	// Updated is true when this call rewrote an existing note rather than
	// minting a new one.
	Updated bool `json:"updated,omitempty"`
}

// captureIncompleteError renders a partially-lost capture as a machine-parseable
// FAILURE. The note has already landed — capture accumulates rather than throws —
// but something around it did not, and reporting that as `status: ok` is the
// original sin this whole task exists to delete.
//
// It is an ERROR, never a successful result carrying a warning flag, because a
// soft tier is one agents learn to skim past. And the payload is JSON EMBEDDED IN
// THE ERROR STRING rather than a structured body, because there is no structured
// channel on the error path: internal/mcp/tools.go turns a non-nil handler error
// into mcplib.NewToolResultError(err.Error()) and DISCARDS the result body. So
// "isError with a payload" is not representable, and this follows the precedent
// already set by resumeConflictError.
//
// The payload names session_key because that is the whole point: it tells the
// agent how to retry WITHOUT duplicating the note it already wrote.
func captureIncompleteError(result *capture.SessionResult) error {
	lost := make([]string, 0, len(result.Failures))
	for _, f := range result.Failures {
		lost = append(lost, f.Stage)
	}
	detail, merr := json.Marshal(map[string]any{
		"captured":    true, // the note DID land — do not blindly re-capture
		"note_path":   result.NotePath,
		"session_id":  result.SessionID,
		"session_key": result.SessionKey,
		"lost":        lost,
		"failures":    result.Failures,
		"remedy": "The session note WAS written and is safe at note_path — do not capture again as if it were lost. " +
			"To retry the parts that failed, call vp_capture_session again with the SAME session_key: " +
			"the existing note is updated in place, never duplicated. If the loss is not retryable " +
			"(e.g. no transcript archive exists for this session), the note stands and you may proceed.",
	})
	if merr != nil {
		return fmt.Errorf("capture incomplete: lost %v", lost)
	}
	return fmt.Errorf("capture incomplete: %s", detail)
}

var captureSessionSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"summary": {
			"type": "string",
			"description": "Session summary (required)."
		},
		"title": {
			"type": "string",
			"description": "Short session title."
		},
		"tag": {
			"type": "string",
			"description": "Session tag (e.g. implementation, debugging, design)."
		},
		"model": {
			"type": "string",
			"description": "LLM model used."
		},
		"decisions": {
			"type": ["array", "string"],
			"items": {"type": "string"},
			"description": "Key decisions made. An array of strings, or a single string (coerced to a one-element list)."
		},
		"files_changed": {
			"type": ["array", "string"],
			"items": {"type": "string"},
			"description": "Files modified. An array of strings, or a single string (coerced to a one-element list)."
		},
		"open_threads": {
			"type": ["array", "string"],
			"items": {"type": "string"},
			"description": "Open items for next session. An array of strings, or a single string (coerced to a one-element list)."
		},
		"transcript": {
			"type": "string",
			"description": "Full session transcript (optional, will be chunked and indexed)."
		},
		"archive_session_id": {
			"type": "string",
			"description": "IGNORED (accepted for wire compatibility). The server derives the host session id itself from the host's live session map and never trusts a caller-supplied one — a wrong id would mis-link another session's transcript. Omit it."
		},
		"archive_adapter": {
			"type": "string",
			"description": "Adapter name for archive lookup (default: claude-code)."
		},
		"archive_transcript": {
			"type": "boolean",
			"description": "Force-archive the supplied transcript as this capture's compressed archive pair, linked to the note at birth. Takes effect ONLY when the server cannot derive a host session id — on a derivable host (Claude Code) it is a no-op, because the hook archives the authoritative host transcript at SessionEnd. When omitted/false, the server still auto-archives for handshake-derived hook-less clients (grok/xai/zed) given a non-empty transcript; unknown or Claude-miss-shaped hosts never auto-on. Requires a non-empty transcript."
		},
		"cwd": {
			"type": "string",
			"description": "Working directory for claim sentinel (optional). When set and the server could derive the host session id, writes a claim so the SessionEnd hook skips re-capturing this session."
		},
		"enrich": {
			"type": "boolean",
			"description": "When true and [enrichment] is enabled in config, run an LLM synthesis pass over the transcript to refine summary/decisions/threads/tag. Default false."
		},
		"session_key": {
			"type": "string",
			"description": "Idempotency key for this capture attempt. RETRY: if a previous call failed with 'capture incomplete', pass back the session_key from its error payload — the existing note is updated in place instead of duplicated. Omit for new work: the server mints a key and returns it."
		}
	},
	"required": ["project", "summary"]
}`)

// CaptureSessionTool returns the MCP tool for vp_capture_session.
func CaptureSessionTool(vault *storage.Vault, indexer *capture.Indexer) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_capture_session",
		Mutating: true,
		Description: "Capture a coding session: write session markdown to the vault, optionally chunk and index transcript for semantic search. " +
			"Returns a session_key identifying this capture attempt. If the call fails with 'capture incomplete', the NOTE WAS STILL WRITTEN " +
			"(the error payload carries note_path) — retry by calling again with the SAME session_key, which updates that note in place rather than duplicating it.",
		Schema:  captureSessionSchema,
		Handler: captureSessionHandler(vault, indexer),
	}
}

func captureSessionHandler(vault *storage.Vault, indexer *capture.Indexer) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p captureSessionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}

		sp := capture.SessionParams{
			Project:        p.Project,
			Summary:        p.Summary,
			Title:          p.Title,
			Tag:            p.Tag,
			Model:          p.Model,
			Decisions:      p.Decisions,
			FilesChanged:   p.FilesChanged,
			OpenThreads:    p.OpenThreads,
			Transcript:     p.Transcript,
			ArchiveAdapter: p.ArchiveAdapter,
			CWD:            p.CWD,
			SessionKey:     p.SessionKey,
		}

		// HOST ATTRIBUTION — always set, never defaulted. Derived from the
		// initialize handshake's clientInfo when the transport confirmed it
		// (derived-wins over any caller-declared client_info); the declared
		// identity only as a fallback with its own provenance; an explicit
		// "unknown" when neither exists. See resolveCaptureHost.
		sp.Host, sp.HostSource = resolveCaptureHost(ctx, p.ClientInfo)

		// DERIVE ONLY — p.ArchiveSessionID is deliberately not copied above.
		// The server resolves the host session id from the host's live session
		// map and stamps it with provenance; when it cannot be confirmed the
		// note stays honestly unlinked. Deriving here (not trusting the caller)
		// is what lets the SessionEnd hook find the agent's wrap note and link
		// it to the transcript once the archive exists.
		if id := hostSessionID(); id != "" {
			sp.ArchiveSessionID = id
			sp.ArchiveSessionIDSource = storage.ArchiveIDSourceDerived
		}

		// INLINE TRANSCRIPT ARCHIVE — transcript-archive parity for hook-less
		// hosts. Fires when wantInlineTranscriptArchive says so:
		//
		//   - DERIVED-EMPTY GATE: only when the derivation above honestly
		//     failed. On a derivable host (Claude Code) the SessionEnd hook
		//     archives the AUTHORITATIVE host transcript later — never a
		//     competitor for the inline path.
		//   - POSITIVE HOOK-LESS SIGNAL or EXPLICIT FORCE: auto-on requires
		//     handshake-derived host in the known hook-less set (grok/xai/zed).
		//     Empty id alone is NOT enough — that shape is also Claude
		//     derivation-miss, and auto-inlining there dual-notes against
		//     SessionEnd. Explicit archive_transcript:true still forces under
		//     empty-id + transcript for any host (template pin path).
		//   - MINT, NEVER TRUST: the archive id is server-controlled — the
		//     caller's session_key when this is a retry (retries must converge
		//     on the SAME manifest), a fresh uuid otherwise. A minted id is
		//     collision-free by construction, so the mis-link failure the
		//     DERIVE-ONLY block above exists to prevent cannot occur here.
		//   - INLINE ADAPTER, NOT A HOST ADAPTER: the content is the agent's
		//     own rendition of the session, not host ground truth; the manifest
		//     records the mechanism (see archive.InlineAdapterName) and the
		//     host is recorded separately by the note's host provenance.
		//   - CREATE BEFORE WriteSession: the manifest must exist when
		//     WriteSession resolves the archive, so ResolveEntry finds it and
		//     the EXISTING linking machinery writes both link directions at
		//     birth — no new linking code, no deferred loop to close.
		//
		// A Create failure is stashed, never fatal: the note is capture's one
		// irreplaceable output, and a capture that loses the note over a failed
		// archive inverts the priority. The stash joins result.Failures after
		// WriteSession returns, where the accumulation lives.
		//
		// THE ELSE BRANCH IS WHERE THE SILENT LOSS LIVED. When the predicate
		// says no on a derived hook-less host with no derivable session id and
		// no transcript, this capture has no archive and no way to ever get
		// one: the inline path has nothing to write, and there is no SessionEnd
		// hook to write it later. Before this it returned status: ok and said
		// nothing, which is the same "reports success for work it did not do"
		// the adapter mismatch above was already made loud for. See
		// capture.StageTranscriptArchiveUnreachable for why this is NOT the
		// deleted StageArchiveResolve — a missing archive in general is still
		// deferred and still Debug.
		var inlineArchiveFailure *capture.CaptureFailure
		var archiveUnreachable *capture.CaptureFailure
		if wantInlineTranscriptArchive(p.ArchiveTranscript, sp.ArchiveSessionID, p.Transcript, sp.Host, sp.HostSource) {
			archiveID := p.SessionKey
			if archiveID == "" {
				// Fresh attempt: mint the id and make it the idempotency key,
				// so the note and its archive pair share one id and a retry
				// converges on both. SessionKeySource records the mint
				// honestly — a handler-minted key must never masquerade as
				// caller-supplied (see capture.SessionParams.SessionKeySource).
				archiveID = uuid.NewString()
				sp.SessionKey = archiveID
				sp.SessionKeySource = storage.KeySourceMinted
			}
			if _, aerr := archive.Create(archive.CreateOptions{
				Adapter:       archive.InlineAdapterName,
				SessionID:     archiveID,
				SourceContent: []byte(p.Transcript),
				VaultRoot:     vault.Root,
				ProjectSlug:   p.Project,
				SourceCWD:     p.CWD,
			}); aerr != nil {
				slog.Warn("vp_capture_session: inline transcript archive failed", "err", aerr)
				inlineArchiveFailure = &capture.CaptureFailure{
					Stage: capture.StageTranscriptArchive,
					Err:   aerr.Error(),
				}
				// sp.ArchiveSessionID stays empty: nothing pretends to link.
			} else {
				sp.ArchiveSessionID = archiveID
				sp.ArchiveSessionIDSource = storage.ArchiveIDSourceInline
				sp.ArchiveAdapter = archive.InlineAdapterName
			}
		} else if sp.HostSource == storage.HostSourceDerived &&
			isHooklessClient(sp.Host) &&
			sp.ArchiveSessionID == "" &&
			strings.TrimSpace(p.Transcript) == "" {
			// All three terms are load-bearing. HostSourceDerived: a DECLARED
			// host could claim anything, and refusing a capture on a caller's
			// own claim is a denial of service the transport never confirmed.
			// Empty ArchiveSessionID: a derived id means a hook DID resolve, so
			// the archive is deferred, not absent. Empty transcript: a non-empty
			// one is archived inline by the branch above and nothing is lost.
			archiveUnreachable = &capture.CaptureFailure{
				Stage: capture.StageTranscriptArchiveUnreachable,
				Err: fmt.Sprintf("host %q was derived as hook-less, so no SessionEnd hook will "+
					"archive this session later, and no transcript was supplied to archive inline: "+
					"this note will never have a transcript archive. Retry with the same session_key "+
					"and the session transcript in `transcript` to archive it alongside the note.", sp.Host),
			}
			slog.Warn("vp_capture_session: transcript archive unreachable",
				"host", sp.Host, "host_source", sp.HostSource)
		}

		// Opt-in LLM enrichment. Resolve an Enricher from config only when the
		// caller asked for it; a nil sp.Enricher leaves the plain-note behavior
		// unchanged. Every failure here is non-fatal — capture proceeds with the
		// agent-authored note and no error is surfaced to the caller.
		if p.Enrich {
			if cfg, cfgErr := vault.LoadConfig(p.Project); cfgErr != nil {
				slog.Warn("vp_capture_session: load config for enrichment failed", "err", cfgErr)
			} else if e, eerr := capture.NewEnricherFromConfig(cfg.Enrichment, vault.Root); eerr != nil {
				slog.Warn("vp_capture_session: enrichment disabled", "err", eerr)
			} else {
				sp.Enricher = e
			}
		}

		result, err := capture.WriteSession(ctx, vault, indexer, sp)
		if err != nil {
			// The note itself was not written. Nothing landed, so there is nothing to
			// tell the agent to preserve; this is a plain failure.
			return nil, err
		}

		// The inline-archive loss (if any) joins the accumulation HERE, not at
		// the Create site: result.Failures is where every peripheral loss lands,
		// and the note had to land before there was a result to append to.
		if inlineArchiveFailure != nil {
			result.Failures = append(result.Failures, *inlineArchiveFailure)
		}
		if archiveUnreachable != nil {
			result.Failures = append(result.Failures, *archiveUnreachable)
		}

		// Write claim sentinel so the SessionEnd hook skips this session.
		//
		// THIS RUNS BEFORE THE ERROR RETURN BELOW, AND THAT ORDER IS LOAD-BEARING.
		// The note exists at this point, and the claim asserts exactly that: "this
		// session has a note". A partial loss does not make that false. Moving this
		// after the error return — the obvious-looking tidy — would mean every
		// hard-failing capture leaves the session unclaimed, so the SessionEnd hook
		// captures it AGAIN and duplicates the note, over a loss as trivial as a
		// missing archive link.
		//
		// DERIVED IDS ONLY. A claim is keyed by the HOST's session id — the id the
		// SessionEnd hook queries — so only a derived id can ever match one. A
		// minted inline id is one no hook will ever ask about: writing a claim for
		// it would be a sentinel with no reader, and on a hook-less host there is
		// no hook to skip in the first place.
		if p.CWD != "" && sp.ArchiveSessionID != "" && sp.ArchiveSessionIDSource == storage.ArchiveIDSourceDerived {
			claimDir := filepath.Join(p.CWD, ".vibe-palace")
			if claimErr := hook.WriteClaim(claimDir, sp.ArchiveSessionID, result.SessionID); claimErr != nil {
				slog.Warn("vp_capture_session: claim sentinel write failed", "err", claimErr)
				result.Failures = append(result.Failures, capture.CaptureFailure{
					Stage: capture.StageClaimSentinel,
					Err:   claimErr.Error(),
				})
			}
		}

		// THE STATUS STOPS LYING HERE. Any loss is an error the agent can see and
		// act on — never an "ok" for work that was not done.
		if result.Failed() {
			return nil, captureIncompleteError(result)
		}

		return captureSessionResult{
			Status:        result.Status,
			Project:       result.Project,
			NotePath:      result.NotePath,
			Iteration:     result.Iteration,
			SessionID:     result.SessionID,
			FrictionScore: result.FrictionScore,
			SessionKey:    result.SessionKey,
			Updated:       result.Updated,
		}, nil
	}
}
