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

type captureSessionParams struct {
	Project      string   `json:"project"`
	Summary      string   `json:"summary"`
	Title        string   `json:"title,omitempty"`
	Tag          string   `json:"tag,omitempty"`
	Model        string   `json:"model,omitempty"`
	Decisions    []string `json:"decisions,omitempty"`
	FilesChanged []string `json:"files_changed,omitempty"`
	OpenThreads  []string `json:"open_threads,omitempty"`
	Transcript   string   `json:"transcript,omitempty"`
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
			"type": "array",
			"items": {"type": "string"},
			"description": "Key decisions made."
		},
		"files_changed": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Files modified."
		},
		"open_threads": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Open items for next session."
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

		// Write claim sentinel so the SessionEnd hook skips this session.
		//
		// THIS RUNS BEFORE THE ERROR RETURN BELOW, AND THAT ORDER IS LOAD-BEARING.
		// The note exists at this point, and the claim asserts exactly that: "this
		// session has a note". A partial loss does not make that false. Moving this
		// after the error return — the obvious-looking tidy — would mean every
		// hard-failing capture leaves the session unclaimed, so the SessionEnd hook
		// captures it AGAIN and duplicates the note, over a loss as trivial as a
		// missing archive link.
		if p.CWD != "" && sp.ArchiveSessionID != "" {
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
