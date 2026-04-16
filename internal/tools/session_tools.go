// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

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
	// ArchiveSessionID, if set, causes the handler to resolve an
	// existing archive for this session under the project's
	// transcripts/ directory and cross-link it with the session note.
	ArchiveSessionID string `json:"archive_session_id,omitempty"`
	// ArchiveAdapter names the adapter to resolve against. Defaults
	// to claude-code when ArchiveSessionID is set and this is empty.
	ArchiveAdapter string `json:"archive_adapter,omitempty"`
	// CWD is the working directory for writing a claim sentinel so
	// the SessionEnd hook skips sessions already captured via MCP.
	CWD string `json:"cwd,omitempty"`
}

type captureSessionResult struct {
	Status        string `json:"status"`
	Project       string `json:"project"`
	NotePath      string `json:"note_path"`
	Iteration     int    `json:"iteration"`
	SessionID     string `json:"session_id"`
	FrictionScore int    `json:"friction_score,omitempty"`
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
			"description": "If set, look up the matching transcript archive under the project's transcripts/ directory and cross-link it with this session note."
		},
		"archive_adapter": {
			"type": "string",
			"description": "Adapter name for archive lookup (default: claude-code)."
		},
		"cwd": {
			"type": "string",
			"description": "Working directory for claim sentinel (optional). When set with archive_session_id, writes a claim so the SessionEnd hook skips this session."
		}
	},
	"required": ["project", "summary"]
}`)

// CaptureSessionTool returns the MCP tool for vp_capture_session.
func CaptureSessionTool(vault *storage.Vault, indexer *capture.Indexer) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_capture_session",
		Description: "Capture a coding session: write session markdown to the vault, optionally chunk and index transcript for semantic search.",
		Schema:      captureSessionSchema,
		Handler:     captureSessionHandler(vault, indexer),
	}
}

func captureSessionHandler(vault *storage.Vault, indexer *capture.Indexer) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p captureSessionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}

		sp := capture.SessionParams{
			Project:          p.Project,
			Summary:          p.Summary,
			Title:            p.Title,
			Tag:              p.Tag,
			Model:            p.Model,
			Decisions:        p.Decisions,
			FilesChanged:     p.FilesChanged,
			OpenThreads:      p.OpenThreads,
			Transcript:       p.Transcript,
			ArchiveSessionID: p.ArchiveSessionID,
			ArchiveAdapter:   p.ArchiveAdapter,
			CWD:              p.CWD,
		}

		result, err := capture.WriteSession(ctx, vault, indexer, sp)
		if err != nil {
			return nil, err
		}

		// Write claim sentinel so the SessionEnd hook skips this session.
		if p.CWD != "" && p.ArchiveSessionID != "" {
			claimDir := filepath.Join(p.CWD, ".vibe-palace")
			if claimErr := hook.WriteClaim(claimDir, p.ArchiveSessionID, result.SessionID); claimErr != nil {
				slog.Warn("vp_capture_session: claim sentinel write failed", "err", claimErr)
			}
		}

		return captureSessionResult{
			Status:        result.Status,
			Project:       result.Project,
			NotePath:      result.NotePath,
			Iteration:     result.Iteration,
			SessionID:     result.SessionID,
			FrictionScore: result.FrictionScore,
		}, nil
	}
}
