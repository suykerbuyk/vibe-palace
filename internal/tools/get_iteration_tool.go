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
//
// Standing principle: return complete information blocks, or handles to request
// them — never truncate a narrative body to fit a budget.
const HostInlineCapBytes = 19968

// DefaultGetIterationMaxBytes is the default max_bytes when omitted. It is a
// budget for MARSHALLED entry rows (not bare body lengths), and leaves envelope
// headroom under HostInlineCapBytes on every measured project at the default.
const DefaultGetIterationMaxBytes = 12000

// MaxGetIterationMaxBytes is the hard clamp on max_bytes — the budget for the
// sum of json.Marshal costs of the entry rows that will be emitted.
//
// It is intentionally STRICTLY LESS than HostInlineCapBytes. max_bytes does
// not include the result wrapper (project, mode, newest_n, oldest_n, returned,
// bytes_inlined, more_available, max_bytes, entries key, complete). That
// envelope is reserved separately as getIterationEnvelopeReserve. Setting
// Max == HostInlineCapBytes re-created the measured breach: rezbldr reported
// bytes_inlined 18966 (under "budget") while putting 21052 B on the wire.
//
// Invariant: MaxGetIterationMaxBytes + getIterationEnvelopeReserve <= HostInlineCapBytes.
const MaxGetIterationMaxBytes = 17000

// getIterationEnvelopeReserve is the bytes reserved for the result wrapper
// around the entries array so a full row budget cannot push the wire payload
// over HostInlineCapBytes. 19968 - 17000 = 2968.
const getIterationEnvelopeReserve = HostInlineCapBytes - MaxGetIterationMaxBytes

// Compile-time guard: the two budget constants must not be equal, and the
// reserve must be positive.
func init() {
	if MaxGetIterationMaxBytes >= HostInlineCapBytes {
		panic("MaxGetIterationMaxBytes must be strictly less than HostInlineCapBytes")
	}
	if getIterationEnvelopeReserve <= 0 {
		panic("getIterationEnvelopeReserve must be positive")
	}
	if MaxGetIterationMaxBytes+getIterationEnvelopeReserve > HostInlineCapBytes {
		panic("MaxGetIterationMaxBytes + envelope reserve exceeds HostInlineCapBytes")
	}
}

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
// (Option B) and ContentURI addresses that exact narrative.
//
// Field order is load-bearing (handle-before-bulk doctrine, 00b0623): identity
// and content_uri MUST precede Header/Body so a host cut still leaves a
// recovery handle. A body is never partial: whole block or handle.
type iterationEntryRow struct {
	N          int    `json:"n"`
	Title      string `json:"title"`
	Bytes      int    `json:"bytes"`
	ContentURI string `json:"content_uri"`
	// MatchIndex is 0-based among same-N matches. Pointer so index 0 is
	// distinguishable from "field absent" (omitempty on a bare int erases 0).
	// Set together with Matches when Matches > 1; both omitted otherwise.
	MatchIndex *int   `json:"match_index,omitempty"`
	Matches    int    `json:"matches,omitempty"`
	Header     string `json:"header,omitempty"`
	Body       string `json:"body,omitempty"`
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
		"n": {"type": "integer", "description": "Fetch every narrative whose iteration number is n (all matches, file order). Inlines whole rows while the marshalled-row budget holds; remainder are manifest rows with content_uri. Mutually exclusive with recent."},
		"recent": {"type": "boolean", "description": "When true, fill backward from the newest entry until the next whole marshalled row would exceed max_bytes, then stop. Mutually exclusive with n. Never truncates a body."},
		"max_bytes": {"type": "integer", "description": "Budget for the sum of json-marshalled entry rows (default 12000, hard max 17000). Does NOT equal the host inline cap (19968): wrapper envelope is reserved separately so wire size stays under the cap. Rejected above the clamp. Never truncates a body — whole row or manifest handle."}
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
			"inlined whole or returned as a manifest row with content_uri. The budget counts " +
			"marshalled row size on the wire, not bare body length. Reports newest_n, " +
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
				"max_bytes %d exceeds hard clamp %d (marshalled-row budget; host cap is %d with envelope reserved); pass a smaller budget",
				maxBytes, MaxGetIterationMaxBytes, HostInlineCapBytes))
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

// marshalledRowBytes is the wire cost of one entry row as it will be emitted.
// The fill budget counts this, not len(Body) — bare body length under-counts
// header, title, content_uri, match fields and JSON punctuation (measured:
// rezbldr 18966 body / 21052 wire at the old equal-to-cap clamp).
func marshalledRowBytes(row iterationEntryRow) int {
	b, err := json.Marshal(row)
	if err != nil {
		// encoding/json does not fail on this struct; if it ever does, treat
		// the row as unbudgetable so we refuse to inline rather than guess.
		return MaxGetIterationMaxBytes + 1
	}
	return len(b)
}

type entryPick struct {
	e       wrapstate.Entry
	inlined bool
	idx     int // index among the candidate list (for match_index)
}

// fillRecentByMarshalledBudget walks candidates newest-first and inlines whole
// rows while the sum of marshalled row sizes fits maxBytes. When the next
// inline would exceed, STOP — older entries are more_available, not converted
// into a long manifest tail (many-small-entry projects). If even the newest
// body does not fit, emit one manifest handle for it and stop.
// Never truncates a body.
func fillRecentByMarshalledBudget(project string, candidates []wrapstate.Entry, maxBytes int) (picks []entryPick, more bool) {
	used := 0
	for i, e := range candidates {
		inlineRow := entryToRow(project, e, true, 0, 0)
		inlineCost := marshalledRowBytes(inlineRow)
		if used+inlineCost <= maxBytes {
			picks = append(picks, entryPick{e: e, inlined: true, idx: i})
			used += inlineCost
			continue
		}
		if len(picks) == 0 {
			// Newest alone exceeds budget: one whole-block handle, no body.
			picks = append(picks, entryPick{e: e, inlined: false, idx: i})
			return picks, true
		}
		return picks, true
	}
	return picks, false
}

// fillNByMarshalledBudget walks same-N matches in file order. Inline whole rows
// while the marshalled budget holds; remainder become manifest rows (handles)
// while those still fit. Matches that cannot fit even as manifests are omitted
// with more_available. Never truncates a body.
func fillNByMarshalledBudget(project string, matches []wrapstate.Entry, maxBytes int) (picks []entryPick, more bool) {
	used := 0
	total := len(matches)
	for i, e := range matches {
		inlineRow := entryToRow(project, e, true, i, total)
		inlineCost := marshalledRowBytes(inlineRow)
		if used+inlineCost <= maxBytes {
			picks = append(picks, entryPick{e: e, inlined: true, idx: i})
			used += inlineCost
			continue
		}
		// Inline does not fit — try a whole-row handle.
		manRow := entryToRow(project, e, false, i, total)
		manCost := marshalledRowBytes(manRow)
		if len(picks) == 0 {
			// First match cannot inline: always return its handle (refusal path).
			picks = append(picks, entryPick{e: e, inlined: false, idx: i})
			used += manCost
			// Continue trying further matches as manifests if budget remains.
			continue
		}
		if used+manCost <= maxBytes {
			picks = append(picks, entryPick{e: e, inlined: false, idx: i})
			used += manCost
			continue
		}
		// Cannot fit another handle — stop; remaining matches are more_available.
		return picks, true
	}
	return picks, false
}

func buildNResult(project string, n int, content string, maxBytes int) (getIterationResult, error) {
	matches := wrapstate.EntriesByN(content, n)
	if len(matches) == 0 {
		return getIterationResult{}, apperr.Caller(fmt.Errorf(
			"iteration %d not found in project %q", n, project))
	}
	picks, more := fillNByMarshalledBudget(project, matches, maxBytes)
	if len(picks) < len(matches) {
		more = true
	}

	rows := make([]iterationEntryRow, 0, len(picks))
	bytesInlined := 0
	for _, p := range picks {
		row := entryToRow(project, p.e, p.inlined, p.idx, len(matches))
		if p.inlined {
			bytesInlined += p.e.Bytes
		}
		rows = append(rows, row)
	}

	return getIterationResult{
		Project:       project,
		Mode:          "n",
		NewestN:       n,
		OldestN:       n,
		Returned:      len(rows),
		BytesInlined:  bytesInlined,
		MoreAvailable: more,
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

	picks, more := fillRecentByMarshalledBudget(project, newestFirst, maxBytes)
	if len(picks) < len(newestFirst) {
		more = true
	}

	// Emit file order (chronological ascending).
	rows := make([]iterationEntryRow, len(picks))
	bytesInlined := 0
	for i := range picks {
		p := picks[len(picks)-1-i]
		rows[i] = entryToRow(project, p.e, p.inlined, 0, 0)
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

func entryToRow(project string, e wrapstate.Entry, inline bool, matchIndex, matchTotal int) iterationEntryRow {
	// Multi-match rows MUST use the indexed URI so content_uri is byte-identical
	// to this row's body (bare form resolves last-match only — wrong for row 0).
	// Unique N uses the bare form (last == only); both resolve to the same body.
	var uri string
	if matchTotal > 1 {
		uri = mcp.IterationMatchURI(project, e.N, matchIndex)
	} else {
		uri = mcp.IterationURI(project, e.N)
	}
	row := iterationEntryRow{
		N:          e.N,
		Title:      e.Title,
		Bytes:      e.Bytes,
		ContentURI: uri,
	}
	if matchTotal > 1 {
		idx := matchIndex
		row.MatchIndex = &idx
		row.Matches = matchTotal
	}
	if inline {
		row.Header = e.Header
		row.Body = e.Body
	}
	return row
}
