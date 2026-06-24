// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---------------------------------------------------------------------------
// vp_search_sessions
// ---------------------------------------------------------------------------

type searchSessionsParams struct {
	Project     string `json:"project"`
	Query       string `json:"query,omitempty"`
	DateFrom    string `json:"date_from,omitempty"`
	DateTo      string `json:"date_to,omitempty"`
	MinFriction int    `json:"min_friction,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type sessionSearchResult struct {
	SessionID     string `json:"session_id"`
	Date          string `json:"date"`
	Title         string `json:"title,omitempty"`
	Summary       string `json:"summary,omitempty"`
	Tag           string `json:"tag,omitempty"`
	FrictionScore int    `json:"friction_score,omitempty"`
}

var searchSessionsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"query": {
			"type": "string",
			"description": "Text search across title, summary, and decisions."
		},
		"date_from": {
			"type": "string",
			"description": "Start date filter (YYYY-MM-DD, inclusive)."
		},
		"date_to": {
			"type": "string",
			"description": "End date filter (YYYY-MM-DD, inclusive)."
		},
		"min_friction": {
			"type": "integer",
			"description": "Minimum friction score (0-100)."
		},
		"tag": {
			"type": "string",
			"description": "Filter by session tag (e.g. implementation, debugging)."
		},
		"limit": {
			"type": "integer",
			"description": "Maximum results to return (default 20, max 100)."
		}
	},
	"required": ["project"]
}`)

// SearchSessionsTool returns the MCP tool for vp_search_sessions.
func SearchSessionsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_search_sessions",
		Description: "Search session metadata with date range, friction score, tag, and text query filters.",
		Schema:      searchSessionsSchema,
		Handler:     searchSessionsHandler(vault),
	}
}

func searchSessionsHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p searchSessionsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		limit := p.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}

		// Fetch sessions with date-range filtering.
		sessions, err := vault.ListSessions(p.Project, p.DateFrom, p.DateTo, 0)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}

		lowerQuery := strings.ToLower(p.Query)
		lowerTag := strings.ToLower(p.Tag)

		var results []sessionSearchResult
		for _, s := range sessions {
			if p.MinFriction > 0 && s.FrictionScore < p.MinFriction {
				continue
			}
			if lowerTag != "" && strings.ToLower(s.Tag) != lowerTag {
				continue
			}
			if lowerQuery != "" && !metaMatchesQuery(s, lowerQuery) {
				continue
			}
			results = append(results, sessionSearchResult{
				SessionID:     s.ID,
				Date:          s.Date,
				Title:         s.Title,
				Summary:       s.Summary,
				Tag:           s.Tag,
				FrictionScore: s.FrictionScore,
			})
			if len(results) >= limit {
				break
			}
		}
		return results, nil
	}
}

// ---------------------------------------------------------------------------
// vp_get_session_detail
// ---------------------------------------------------------------------------

type getSessionDetailParams struct {
	Project   string `json:"project"`
	SessionID string `json:"session_id"`
}

type sessionDetailResult struct {
	SessionID     string   `json:"session_id"`
	Project       string   `json:"project"`
	Date          string   `json:"date"`
	Iteration     int      `json:"iteration"`
	Title         string   `json:"title,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	Tag           string   `json:"tag,omitempty"`
	Model         string   `json:"model,omitempty"`
	FrictionScore int      `json:"friction_score,omitempty"`
	Decisions     []string `json:"decisions,omitempty"`
	FilesChanged  []string `json:"files_changed,omitempty"`
	OpenThreads   []string `json:"open_threads,omitempty"`
	Body          string   `json:"body"`
}

var getSessionDetailSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"session_id": {
			"type": "string",
			"description": "Session ID in YYYY-MM-DD-NN format."
		}
	},
	"required": ["project", "session_id"]
}`)

// GetSessionDetailTool returns the MCP tool for vp_get_session_detail.
func GetSessionDetailTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_session_detail",
		Description: "Get full session detail including metadata and markdown body.",
		Schema:      getSessionDetailSchema,
		Handler:     getSessionDetailHandler(vault),
	}
}

func getSessionDetailHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p getSessionDetailParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.SessionID == "" {
			return nil, fmt.Errorf("session_id is required")
		}

		date, fp, iteration, err := parseSessionID(p.SessionID)
		if err != nil {
			return nil, err
		}

		meta, body, err := vault.ReadSession(p.Project, date, fp, iteration)
		if err != nil {
			return nil, fmt.Errorf("read session: %w", err)
		}

		return sessionDetailResult{
			SessionID:     meta.ID,
			Project:       meta.Project,
			Date:          meta.Date,
			Iteration:     meta.Iteration,
			Title:         meta.Title,
			Summary:       meta.Summary,
			Tag:           meta.Tag,
			Model:         meta.Model,
			FrictionScore: meta.FrictionScore,
			Decisions:     meta.Decisions,
			FilesChanged:  meta.FilesChanged,
			OpenThreads:   meta.OpenThreads,
			Body:          body,
		}, nil
	}
}

// parseSessionID extracts date, writer fingerprint, and iteration from a
// session id. It accepts two layouts:
//
//	YYYY-MM-DD-NN            (legacy, host-agnostic) → fp == ""
//	YYYY-MM-DD-<fp>-NN       (host-scoped)           → fp is 8 lowercase-hex
//
// The layout is validated positionally (not just by length) so a malformed but
// slug-valid id like "2026-06-201-5" is REJECTED rather than silently sliced
// into a different real session. This matters because the id now arrives off the
// wire via the session resource URI, so a wrong-but-valid id must not resolve to
// an unrelated session. The returned fp is fed back into the storage read API,
// where "" selects the legacy filename.
func parseSessionID(id string) (date string, fp string, iteration int, err error) {
	formatErr := func() (string, string, int, error) {
		return "", "", 0, fmt.Errorf("invalid session_id %q: expected YYYY-MM-DD-NN or YYYY-MM-DD-<fp>-NN format", id)
	}
	// "YYYY-MM-DD-" prefix is exactly 11 chars; at least one trailing char.
	if len(id) < 13 {
		return formatErr()
	}
	// Hyphens must sit at the date separators and after the day.
	if id[4] != '-' || id[7] != '-' || id[10] != '-' {
		return formatErr()
	}
	// Date digits at the fixed positions.
	for i := 0; i < 10; i++ {
		if i == 4 || i == 7 {
			continue
		}
		if id[i] < '0' || id[i] > '9' {
			return formatErr()
		}
	}
	// The suffix after "YYYY-MM-DD-" is either "<NN>" (legacy: all digits) or
	// "<fp>-<NN>" (host-scoped: 8 lowercase-hex chars, a hyphen, then digits).
	// Validate it positionally to REJECT a malformed-but-slug-valid id; the
	// returned values themselves are then derived from the canonical extractors
	// (capture.ParseFingerprint/ParseIteration) so this parser cannot drift from
	// how the rest of the codebase splits a session id.
	rest := id[11:]
	if h := strings.IndexByte(rest, '-'); h >= 0 {
		if !isHex8(rest[:h]) || !allDigits(rest[h+1:]) {
			return formatErr()
		}
	} else if !allDigits(rest) {
		return formatErr()
	}

	date = id[:10]
	fp = capture.ParseFingerprint(id)
	iteration = capture.ParseIteration(id)
	if iteration < 1 {
		return "", "", 0, fmt.Errorf("invalid iteration in session_id %q", id)
	}
	return date, fp, iteration, nil
}

// isHex8 reports whether s is exactly 8 lowercase-hex characters, the shape of
// a surface.WriterFingerprint. It is intentionally strict so a legacy all-digit
// iteration (which is never 8 hex chars with a following hyphen) cannot be
// mistaken for a fingerprint.
func isHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// allDigits reports whether s is non-empty and entirely ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// vp_get_project_context
// ---------------------------------------------------------------------------

type getProjectContextParams struct {
	Project     string   `json:"project"`
	Sections    []string `json:"sections,omitempty"`
	MaxSessions int      `json:"max_sessions,omitempty"`
}

// ProjectContext is the response for vp_get_project_context.
type ProjectContext struct {
	Project   string                 `json:"project"`
	Summary   string                 `json:"summary,omitempty"`
	Sessions  []sessionSearchResult  `json:"sessions,omitempty"`
	Threads   []string               `json:"threads,omitempty"`
	Decisions []string               `json:"decisions,omitempty"`
	Friction  []capture.WeeklyMetric `json:"friction,omitempty"`
}

var getProjectContextSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"sections": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Sections to include: summary, sessions, threads, decisions, friction. Default: all."
		},
		"max_sessions": {
			"type": "integer",
			"description": "Maximum recent sessions to include (default 10)."
		}
	},
	"required": ["project"]
}`)

// GetProjectContextTool returns the MCP tool for vp_get_project_context.
func GetProjectContextTool(vault *storage.Vault, resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_project_context",
		Description: "Get condensed project context with configurable sections: summary, sessions, threads, decisions, friction.",
		Schema:      getProjectContextSchema,
		Handler:     getProjectContextHandler(vault, resolver),
	}
}

var allSections = map[string]bool{
	"summary": true, "sessions": true, "threads": true,
	"decisions": true, "friction": true,
}

func getProjectContextHandler(vault *storage.Vault, resolver *vpctx.Resolver) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p getProjectContextParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		maxSessions := p.MaxSessions
		if maxSessions <= 0 {
			maxSessions = 10
		}

		// Determine which sections to include.
		want := allSections
		if len(p.Sections) > 0 {
			want = make(map[string]bool, len(p.Sections))
			for _, s := range p.Sections {
				want[strings.ToLower(s)] = true
			}
		}

		result := ProjectContext{Project: p.Project}

		// Summary: extract from resume.
		if want["summary"] {
			if resume, _, err := resolver.Resolve("resume", p.Project); err == nil {
				result.Summary = extractBrief(resume, 500)
			}
		}

		// Sessions, threads, decisions all need session list.
		if want["sessions"] || want["threads"] || want["decisions"] {
			sessions, err := vault.ListSessions(p.Project, "", "", 0)
			if err == nil {
				// Take most recent maxSessions.
				start := 0
				if len(sessions) > maxSessions {
					start = len(sessions) - maxSessions
				}
				recent := sessions[start:]

				if want["sessions"] {
					for _, s := range recent {
						result.Sessions = append(result.Sessions, sessionSearchResult{
							SessionID:     s.ID,
							Date:          s.Date,
							Title:         s.Title,
							Summary:       s.Summary,
							Tag:           s.Tag,
							FrictionScore: s.FrictionScore,
						})
					}
				}

				if want["threads"] {
					seen := make(map[string]bool)
					// Walk recent sessions in reverse (most recent first).
					for i := len(recent) - 1; i >= 0; i-- {
						for _, t := range recent[i].OpenThreads {
							if !seen[t] {
								seen[t] = true
								result.Threads = append(result.Threads, t)
							}
						}
					}
				}

				if want["decisions"] {
					seen := make(map[string]bool)
					for i := len(recent) - 1; i >= 0; i-- {
						for _, d := range recent[i].Decisions {
							if !seen[d] {
								seen[d] = true
								result.Decisions = append(result.Decisions, d)
							}
						}
					}
				}
			}
		}

		// Friction trends.
		if want["friction"] {
			if trends, err := capture.GetFrictionTrends(vault, p.Project, 8); err == nil {
				result.Friction = trends
			}
		}

		return result, nil
	}
}

// ---------------------------------------------------------------------------
// vp_get_effectiveness
// ---------------------------------------------------------------------------

type getEffectivenessParams struct {
	Project string `json:"project"`
	Weeks   int    `json:"weeks,omitempty"`
}

// The effectiveness result types now live in internal/capture (review findings
// H-2 / LOW-2). These aliases keep existing references in this package and its
// tests resolving unchanged.
type (
	WeeklyEffectiveness  = capture.WeeklyEffectiveness
	EffectivenessResult  = capture.EffectivenessResult
	OverallEffectiveness = capture.OverallEffectiveness
)

var getEffectivenessSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"weeks": {
			"type": "integer",
			"description": "Number of weeks to analyze (default 8, max 52)."
		}
	},
	"required": ["project"]
}`)

// GetEffectivenessTool returns the MCP tool for vp_get_effectiveness.
func GetEffectivenessTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_effectiveness",
		Description: "Analyze context availability vs session outcomes. Compares friction scores for sessions with and without rich context (decisions, files changed).",
		Schema:      getEffectivenessSchema,
		Handler:     getEffectivenessHandler(vault),
	}
}

func getEffectivenessHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p getEffectivenessParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		weeks := p.Weeks
		if weeks <= 0 {
			weeks = 8
		}
		if weeks > 52 {
			weeks = 52
		}

		// Compute date range.
		now := time.Now().UTC()
		dateFrom := now.AddDate(0, 0, -weeks*7).Format("2006-01-02")

		sessions, err := vault.ListSessions(p.Project, dateFrom, "", 0)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}

		return capture.ComputeEffectiveness(p.Project, sessions), nil
	}
}

// metaMatchesQuery checks if any searchable field in a session contains the query.
func metaMatchesQuery(meta storage.SessionMeta, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(meta.Title), lowerQuery) {
		return true
	}
	if strings.Contains(strings.ToLower(meta.Summary), lowerQuery) {
		return true
	}
	for _, d := range meta.Decisions {
		if strings.Contains(strings.ToLower(d), lowerQuery) {
			return true
		}
	}
	return false
}

// sortStrings sorts a string slice in ascending order.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// roundTo rounds f to n decimal places.
func roundTo(f float64, n int) float64 {
	pow := math.Pow(10, float64(n))
	return math.Round(f*pow) / pow
}
