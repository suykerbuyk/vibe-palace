// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// healthSchema no longer takes a project.
//
// It used to REQUIRE one and never use it: the log lives at a vault-global path
// (VaultLocalDir()), which takes no arguments, so the parameter was rejected-if-
// missing and then ignored. A required parameter that does nothing is theatre —
// it teaches callers a lie about what the tool is scoped to.
var healthSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"hours": {
			"type": "integer",
			"description": "Look-back window in hours (default 24, max 720). Widens the window for BOTH warn_counts and recent_warns."
		},
		"limit": {
			"type": "integer",
			"description": "Max entries in the recent_warns list (default 20, max 1000). Caps ONLY the displayed list — status and warn_counts are computed over EVERY entry in the window."
		}
	}
}`)

type healthParams struct {
	Hours int `json:"hours,omitempty"`
	Limit int `json:"limit,omitempty"`
}

// HealthTool returns the MCP tool for vp_health.
func HealthTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_health",
		Description: "Runtime health: recent warnings/errors from the vp log. " +
			"status is \"healthy\", \"warnings\", \"errors\", or \"unknown\" — where \"unknown\" means the log could not be READ " +
			"(see scan_error), which is NOT the same as healthy. Health is also pushed into vp_bootstrap_context when it is not healthy, " +
			"so you do not have to remember to ask.",
		Schema:  healthSchema,
		Handler: healthHandler(vault),
	}
}

func healthHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p healthParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}

		hours := p.Hours
		if hours <= 0 {
			hours = 24
		}
		if hours > 720 {
			hours = 720
		}

		limit := p.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > 1000 {
			limit = 1000
		}

		// vplog.Summarize is the ONE definition of what the log says about the
		// system's health, so this tool and the health pushed into
		// vp_bootstrap_context cannot drift apart into two different answers.
		return vplog.Summarize(vaultLogPath(vault), hours, limit), nil
	}
}

// vaultLogPath is where vplog writes: <vault>/palace/.local/vp.log. It takes no
// project, which is exactly why vp_health does not either.
func vaultLogPath(vault *storage.Vault) string {
	return vault.VaultLocalDir() + "/vp.log"
}

// The window vp_bootstrap_context reports health over. Kept deliberately tight:
// bootstrap health is about "did something break in the work you are resuming",
// not a full audit — that is what calling vp_health with a wider `hours` is for.
//
// ⚠ healthDisplayLimit LOOKS DEAD AND IS NOT. The bootstrap caller now clears
// Summary.RecentWarns outright, so the list this bounds is discarded — which
// invites the "cleanup" of passing 0 instead and deleting the constant. THAT IS
// A BUG: Summarize treats limit <= 0 as UNLIMITED (`if limit > 0 && len(...)`,
// internal/vplog/summary.go), so passing 0 would build the entire in-window tail
// on the hottest path in the system, to throw it away. The bound stays.
const (
	healthWindowHours  = 24
	healthDisplayLimit = 5
)

// healthMessage renders a Summary as one line an agent will actually read, for the
// post-bootstrap directive. It mirrors how friction_trend and vault_staleness
// surface themselves: a structured field for machines, plus a human-visible
// sentence, because a field in a large payload is easy to skim past.
//
// It is only ever called for a NON-healthy summary — there is no cheerful case.
func healthMessage(s vplog.Summary) string {
	switch s.Status {
	case vplog.StatusUnknown:
		return fmt.Sprintf("⚠ vp health is UNKNOWN — the log at %s could not be read (%s). "+
			"This is not the same as healthy: vp cannot currently tell you when it loses your work.", s.LogPath, s.ScanError)
	case vplog.StatusErrors:
		return fmt.Sprintf("🔴 vp logged ERRORS in the last %dh (%s). Something failed and was recorded but not surfaced. "+
			"Call vp_health for the full list before trusting recent captures.", healthWindowHours, summarizeCounts(s))
	case vplog.StatusWarnings:
		return fmt.Sprintf("⚠ vp logged warnings in the last %dh (%s). Call vp_health for detail.",
			healthWindowHours, summarizeCounts(s))
	}
	return ""
}

// callerFrictionMessage renders the caller-friction tally as ONE terse line for
// the bootstrap directive. It is a FRICTION signal, deliberately distinct from
// health.status: these are guards that WORKED (a caller passed bad input), not
// faults, so they never turn the payload amber.
//
// 🔴 IT MUST BE SILENT AT ZERO. The bootstrap payload has ~110 tokens of
// headroom and a standing operator rule that any new alert is silent in the good
// case; the caller only appends this when CallerFriction > 0. Kept to one short
// sentence (no per-category breakdown — that is what vp_health is for) so it
// costs a handful of tokens in the rare case it fires and nothing otherwise.
func callerFrictionMessage(s vplog.Summary) string {
	if s.CallerFriction <= 0 {
		return ""
	}
	return fmt.Sprintf("ℹ %d caller-side rejection(s) in %dh (guards working, not faults) — vp_health for detail.",
		s.CallerFriction, healthWindowHours)
}

// summarizeCounts renders the warn_counts map as a compact "category xN" list, in
// descending count order so the loudest thing is named first.
func summarizeCounts(s vplog.Summary) string {
	type kv struct {
		k string
		n int
	}
	pairs := make([]kv, 0, len(s.WarnCounts))
	for k, n := range s.WarnCounts {
		pairs = append(pairs, kv{k, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].k < pairs[j].k
	})
	parts := make([]string, 0, len(pairs))
	for i, p := range pairs {
		if i == 3 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", p.k, p.n))
	}
	return strings.Join(parts, ", ")
}
