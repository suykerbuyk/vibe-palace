// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// HostInlineCapBytes is the measured flat host inline cap (Grok pane, iter 287):
// "first 19.5 KB of …" = 19,968 bytes. Named here so the iteration reader's
// budget clamp sits next to the figure it is defending, not in a schema string.
// resource_read_tool.go cites the same measurement for the pager's headroom.
const HostInlineCapBytes = 19968

// DefaultGetIterationMaxBytes is the default inlined-body budget for
// vp_get_iteration when max_bytes is omitted. It sits under HostInlineCapBytes
// with envelope headroom for the wrapper fields around the bodies.
const DefaultGetIterationMaxBytes = 12000

// MaxGetIterationMaxBytes is the hard clamp on max_bytes. Callers asking for
// more are rejected rather than silently honoured (the anti-pattern of
// vp_read_resource's unclamped limit).
const MaxGetIterationMaxBytes = HostInlineCapBytes

// ---------------------------------------------------------------------------
// vp_get_iteration
// ---------------------------------------------------------------------------

type getIterationParams struct {
	Project  string `json:"project"`
	N        *int   `json:"n,omitempty"`
	Recent   bool   `json:"recent,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

// iterationEntryRow is one narrative in the result. When Body is non-empty the
// entry was inlined whole; when Body is empty the row is a manifest handle
// (Option B) and ContentURI addresses the per-iteration resource.
type iterationEntryRow struct {
	N          int    `json:"n"`
	Title      string `json:"title"`
	Bytes      int    `json:"bytes"`
	Header     string `json:"header,omitempty"`
	Body       string `json:"body,omitempty"`
	ContentURI string `json:"content_uri"`
	MatchIndex int    `json:"match_index,omitempty"` // 0-based among same-N matches; set when Matches > 1
	Matches    int    `json:"matches,omitempty"`     // total same-N matches; set on every row when > 1
}

// getIterationResult is a wrapper struct (never a bare array) so complete can
// be the last field with no omitempty — the inline-delivery contract.
type getIterationResult struct {
	Project       string              `json:"project"`
	Mode          string              `json:"mode"` // "n" or "recent"
	NewestN       int                 `json:"newest_n"`
	OldestN       int                 `json:"oldest_n"`
	Returned      int                 `json:"returned"`
	BytesInlined  int                 `json:"bytes_inlined"`
	MoreAvailable bool                `json:"more_available"`
	MaxBytes      int                 `json:"max_bytes"`
	Entries       []iterationEntryRow `json:"entries"`
	Complete      bool                `json:"complete"`
}

var getIterationSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"n": {"type": "integer", "description": "Fetch every narrative whose iteration number is n (all matches, file order). Mutually exclusive with recent."},
		"recent": {"type": "boolean", "description": "When true, fill backward from the newest entry until the next whole entry would exceed max_bytes, then stop. Mutually exclusive with n. Never truncates a body."},
		"max_bytes": {"type": "integer", "description": "Budget for inlined bodies (default 12000, hard max 19968 = HostInlineCapBytes). Rejected above the clamp. Applies to recent fill; for n-mode bodies are always inlined (each entry is under the host cap)."}
	},
	"required": ["project"]
}`)

// GetIterationTool returns the structure-aware reader for iterations.md.
func GetIterationTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_get_iteration",
		Description: "Read iteration narratives from a project's iterations.md without " +
			"loading the whole archive. Pass n to fetch by number (all matches), or recent=true " +
			"to fill newest-first up to max_bytes. Bodies are never truncated: an entry is " +
			"inlined whole or returned as a manifest row with content_uri. Reports newest_n, " +
			"oldest_n, returned, bytes_inlined, more_available. complete is always last.",
		Schema:  getIterationSchema,
		Handler: getIterationHandler(vault),
	}
}

func getIterationHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p getIterationParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if strings.TrimSpace(p.Project) == "" {
			return nil, apperr.Caller(fmt.Errorf("project is required"))
		}
		hasN := p.N != nil
		if hasN && p.Recent {
			return nil, apperr.Caller(fmt.Errorf("pass exactly one of n or recent=true, not both"))
		}
		if !hasN && !p.Recent {
			return nil, apperr.Caller(fmt.Errorf("pass exactly one of n (int) or recent=true"))
		}
		if hasN && *p.N < 1 {
			return nil, apperr.Caller(fmt.Errorf("n must be a positive iteration number"))
		}

		maxBytes := p.MaxBytes
		if maxBytes == 0 {
			maxBytes = DefaultGetIterationMaxBytes
		}
		if maxBytes < 0 {
			return nil, apperr.Caller(fmt.Errorf("max_bytes must be non-negative"))
		}
		if maxBytes > MaxGetIterationMaxBytes {
			return nil, apperr.Caller(fmt.Errorf(
				"max_bytes %d exceeds hard clamp %d (HostInlineCapBytes); pass a smaller budget",
				maxBytes, MaxGetIterationMaxBytes))
		}

		path, err := vault.IterationsFile(p.Project)
		if err != nil {
			return nil, apperr.Caller(fmt.Errorf("iterations path: %w", err))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, apperr.Caller(fmt.Errorf("project %q has no iterations.md", p.Project))
			}
			return nil, fmt.Errorf("read iterations.md: %w", err)
		}
		content := string(data)

		if hasN {
			return buildNResult(p.Project, *p.N, content, maxBytes)
		}
		return buildRecentResult(p.Project, content, maxBytes)
	}
}

func buildNResult(project string, n int, content string, maxBytes int) (getIterationResult, error) {
	matches := wrapstate.EntriesByN(content, n)
	if len(matches) == 0 {
		return getIterationResult{}, apperr.Caller(fmt.Errorf(
			"iteration %d not found in project %q", n, project))
	}
	rows := make([]iterationEntryRow, 0, len(matches))
	bytesInlined := 0
	for i, e := range matches {
		row := entryToRow(project, e, true)
		if len(matches) > 1 {
			row.MatchIndex = i
			row.Matches = len(matches)
		}
		bytesInlined += e.Bytes
		rows = append(rows, row)
	}
	// newest/oldest among returned (same N; file order)
	return getIterationResult{
		Project:       project,
		Mode:          "n",
		NewestN:       n,
		OldestN:       n,
		Returned:      len(rows),
		BytesInlined:  bytesInlined,
		MoreAvailable: false,
		MaxBytes:      maxBytes,
		Entries:       rows,
		Complete:      true,
	}, nil
}

func buildRecentResult(project, content string, maxBytes int) (getIterationResult, error) {
	newestFirst := wrapstate.EntriesNewestFirst(content)
	if len(newestFirst) == 0 {
		return getIterationResult{
			Project:  project,
			Mode:     "recent",
			MaxBytes: maxBytes,
			Entries:  []iterationEntryRow{},
			Complete: true,
		}, nil
	}

	// selected holds entries in newest-first order. inlined[i] is true when
	// selected[i]'s body fits in the budget (never a partial body).
	type pick struct {
		e       wrapstate.Entry
		inlined bool
	}
	var selected []pick
	used := 0
	for _, e := range newestFirst {
		if used+e.Bytes <= maxBytes {
			selected = append(selected, pick{e: e, inlined: true})
			used += e.Bytes
			continue
		}
		if len(selected) == 0 {
			// Newest alone exceeds budget: one manifest row, no body.
			selected = append(selected, pick{e: e, inlined: false})
		}
		// Budget exhausted — stop. Older entries are signalled via more_available.
		break
	}

	more := len(selected) < len(newestFirst)
	if len(selected) == 1 && !selected[0].inlined {
		// Oversize newest: body available only via content_uri; archive continues.
		more = true
	}

	// Emit file order (chronological ascending).
	rows := make([]iterationEntryRow, len(selected))
	bytesInlined := 0
	for i := range selected {
		p := selected[len(selected)-1-i]
		rows[i] = entryToRow(project, p.e, p.inlined)
		if p.inlined {
			bytesInlined += p.e.Bytes
		}
	}

	oldestN := rows[0].N
	newestN := rows[len(rows)-1].N

	return getIterationResult{
		Project:       project,
		Mode:          "recent",
		NewestN:       newestN,
		OldestN:       oldestN,
		Returned:      len(rows),
		BytesInlined:  bytesInlined,
		MoreAvailable: more,
		MaxBytes:      maxBytes,
		Entries:       rows,
		Complete:      true,
	}, nil
}

func entryToRow(project string, e wrapstate.Entry, inline bool) iterationEntryRow {
	row := iterationEntryRow{
		N:          e.N,
		Title:      e.Title,
		Bytes:      e.Bytes,
		ContentURI: mcp.IterationURI(project, e.N),
	}
	if inline {
		row.Header = e.Header
		row.Body = e.Body
	}
	return row
}
