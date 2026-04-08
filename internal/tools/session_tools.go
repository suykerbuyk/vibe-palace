// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
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

		// Score transcript friction before writing session.
		if p.Transcript != "" {
			if score, err := capture.AnalyzeFriction(p.Transcript); err == nil {
				meta.FrictionScore = score
			}
		}

		body := buildSessionBody(p)

		sessionID, err := vault.WriteSession(p.Project, meta, body)
		if err != nil {
			return nil, fmt.Errorf("write session: %w", err)
		}

		// Parse iteration from sessionID (format: "YYYY-MM-DD-NN").
		iteration := parseIteration(sessionID)

		// Read session back to get the note path.
		readMeta, _, readErr := vault.ReadSession(p.Project, date, iteration)
		notePath := ""
		if readErr == nil {
			notePath = readMeta.NotePath
		}

		// Index transcript if provided.
		if p.Transcript != "" && indexer != nil {
			if err := indexer.IndexTranscript(ctx, sessionID, p.Project, p.Transcript); err != nil {
				// Non-fatal: session was captured, indexing failed.
				return captureSessionResult{
					Status:        "partial",
					Project:       p.Project,
					NotePath:      notePath,
					Iteration:     iteration,
					SessionID:     sessionID,
					FrictionScore: meta.FrictionScore,
				}, nil
			}
		}

		return captureSessionResult{
			Status:        "ok",
			Project:       p.Project,
			NotePath:      notePath,
			Iteration:     iteration,
			SessionID:     sessionID,
			FrictionScore: meta.FrictionScore,
		}, nil
	}
}

// buildSessionBody assembles the markdown body for a session file.
func buildSessionBody(p captureSessionParams) string {
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

	return joinLines(b)
}

func joinLines(lines []string) string {
	result := ""
	for _, l := range lines {
		result += l + "\n"
	}
	return result
}

// parseIteration extracts the iteration number from a session ID (format: "YYYY-MM-DD-NN").
func parseIteration(sessionID string) int {
	parts := strings.Split(sessionID, "-")
	if len(parts) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}
