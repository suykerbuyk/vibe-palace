// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Limit policy for vp_palace_query. The default keeps an unqualified memory
// question inside a single readable turn; the ceiling keeps a caller from
// asking for the whole room and blowing past the host's inline cap, where the
// answer comes back truncated and the `complete` sentinel is the only thing
// left to notice it (see palaceQueryResult below).
const (
	defaultPalaceQueryLimit = 20
	maxPalaceQueryLimit     = 50
)

// palaceQueryHalls is the hall enum, built from the palace.Hall* constants
// (internal/palace/metadata.go:17-22) rather than retyped as literals, so
// renaming a constant is a compile error here instead of a schema that
// silently advertises a hall nothing is ever filed under.
// TestPalaceQuerySchemaHallEnumMatchesConstants pins that the assembled schema
// actually carries these values.
var palaceQueryHalls = []string{
	palace.HallDecisions,
	palace.HallDiscoveries,
	palace.HallPreferences,
	palace.HallAdvice,
	palace.HallEvents,
	palace.HallFacts,
}

// palaceQuerySchema is assembled with the hall enum injected from the
// constants above; everything else is a literal.
//
// 🔴 `source_type` is "type": ["array", "string"] — NOT "array". This is a
// correctness requirement, not a nicety. Schemas are compiled and enforced
// BEFORE the handler runs on both dispatch paths (internal/mcp/tools.go:335
// and :570 call validateParams, defined at :756), so a bare "type": "array"
// rejects every scalar spelling at validation and the handler — and therefore
// flexStringList's coercion — never runs at all. flexStringList exists
// precisely because a client sent a scalar and "the strict `type: array`
// schema rejected the whole vp_capture_session call at validation and the
// session was LOST" (session_tools.go:226-233). The permissive type is the
// half of that fix that lives in the schema; dropping it re-opens the hole in
// a tool whose most load-bearing parameter is exactly this one.
var palaceQuerySchema = json.RawMessage(fmt.Sprintf(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug (required)."
		},
		"wing": {
			"type": "string",
			"description": "Wing slug. Defaults to the project slug, which is where every capture-written drawer lands. Drawers written by the mempalace migrator sit in other wings and are reachable ONLY by naming wing= explicitly."
		},
		"room": {
			"type": "string",
			"description": "Room slug. Naming a room is never overridden, and it also disables the default-room prune, so this is how you reach a decision drawer filed outside the decisions room."
		},
		"hall": {
			"type": "string",
			"enum": %s,
			"description": "Hall (TOPIC classification, assigned by keyword matching). It does NOT select a kind of memory: on live data 1,002 transcript chunks already carry hall=decisions from keyword detection. Use source_type to select the kind."
		},
		"source_type": {
			"type": ["array", "string"],
			"items": {"type": "string"},
			"description": "Source types to match, e.g. \"decision\" or \"session\". An array of strings, or a single string (coerced to a one-element list). Defaults to [\"decision\"] — an omitted source_type does NOT mean \"all drawers\"."
		},
		"q": {
			"type": "string",
			"description": "Case-insensitive substring over drawer content. Empty matches any."
		},
		"date_from": {
			"type": "string",
			"description": "Inclusive lower bound on filed_at, YYYY-MM-DD."
		},
		"date_to": {
			"type": "string",
			"description": "Inclusive upper bound on filed_at, YYYY-MM-DD. Compared against the day part only, so a drawer filed later that same day is still returned."
		},
		"limit": {
			"type": "integer",
			"description": "Max drawers to return. Default 20, clamped to 50."
		}
	},
	"required": ["project"]
}`, mustMarshalSchemaEnum(palaceQueryHalls)))

// mustMarshalSchemaEnum renders an enum list into the schema literal above. It is only
// ever handed a []string of compile-time constants, so a marshal failure is
// unreachable and a panic at init is the honest response to one — a schema
// that failed to assemble must not be served.
func mustMarshalSchemaEnum(v []string) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("palace query schema: marshal: %v", err))
	}
	return string(b)
}

// palaceQueryParams is the WIRE shape. SourceType is a flexStringList
// (session_tools.go:234-259), which yields nil for null and for "", and a
// one-element list for a bare string — which is exactly what collapses
// "omitted", "null" and "" into a single case for the resolver's default.
type palaceQueryParams struct {
	Project    string         `json:"project"`
	Wing       string         `json:"wing"`
	Room       string         `json:"room"`
	Hall       string         `json:"hall"`
	SourceType flexStringList `json:"source_type"`
	Q          string         `json:"q"`
	DateFrom   string         `json:"date_from"`
	DateTo     string         `json:"date_to"`
	Limit      int            `json:"limit"`
}

// PalaceQueryInput is the decoded, UNRESOLVED request: exactly what the caller
// asked for, with omitted fields still zero.
type PalaceQueryInput struct {
	Project     string
	Wing        string
	Room        string
	Hall        string
	SourceTypes []string
	Q           string
	DateFrom    string
	DateTo      string
	Limit       int
}

// ResolvePalaceQuery applies every default, the limit clamp, and the
// default-room prune, and returns the fully-resolved storage.DrawerQuery.
//
// It is EXPORTED on purpose. cmd/vp already imports internal/tools
// (cmd/vp/bootstrap.go:17), so a future `vp palace query` calls this same
// function and inherits this behaviour for free. If the defaults and the prune
// lived inside the MCP handler instead, every rule that DEFINES this tool —
// which wing an unqualified query lands in, that an omitted source_type means
// decisions rather than everything, the room prune and its precedence against
// an explicit room — would be MCP-only, and a CLI twin would have to
// reimplement the precedence from the prose. Two implementations of a default
// do not fail loudly when they drift: they return different subsets of the
// palace to two callers who both believe they asked the same question.
//
// storage.ScanDrawers deliberately applies no defaults of its own
// (drawer_query.go:13-24); this is the one owner.
func ResolvePalaceQuery(in PalaceQueryInput) (storage.DrawerQuery, error) {
	if in.Project == "" {
		return storage.DrawerQuery{}, errors.New("project is required")
	}

	q := storage.DrawerQuery{
		Project:     in.Project,
		Wing:        in.Wing,
		Room:        in.Room,
		Hall:        in.Hall,
		SourceTypes: in.SourceTypes,
		Q:           in.Q,
		DateFrom:    in.DateFrom,
		DateTo:      in.DateTo,
		Limit:       in.Limit,
	}

	// The wing every capture-written drawer lands in is the project slug:
	// palace.DetectWing(project, "") returns project whenever it is non-empty
	// (internal/palace/metadata.go:28-41), and the indexer calls it with the
	// project it is capturing (internal/capture/indexer.go:77). Defaulting to
	// it turns the common question into a one-wing walk. Migrator-written
	// wings (internal/migrate/mempalace.go:167) are out of scope for the
	// default and reachable only by naming wing= explicitly.
	if q.Wing == "" {
		q.Wing = in.Project
	}

	if q.Limit <= 0 {
		q.Limit = defaultPalaceQueryLimit
	}
	if q.Limit > maxPalaceQueryLimit {
		q.Limit = maxPalaceQueryLimit
	}

	// An omitted source_type is NOT "every drawer". The palace holds far more
	// transcript chunks than decisions, so an unfiltered default would bury a
	// handful of real decisions under session text that happened to contain
	// the same words.
	if len(q.SourceTypes) == 0 {
		q.SourceTypes = []string{storage.SourceTypeDecision}
	}

	// --- The default-room prune ---
	//
	// Prune to capture.DecisionRoom iff no room was named AND the RESOLVED
	// source-type set is exactly {"decision"}. The predicate reads the
	// resolver's OUTPUT, never the raw input, and both halves are load-bearing:
	//
	//   * Keying on "no room named" ALONE would send source_type=session into
	//     <wing>/decisions/drawers.jsonl, which by construction contains no
	//     session drawers at all (capture files decisions there and nothing
	//     else, internal/capture/decisions.go). The tape corpus would become
	//     unreachable through the very parameter that advertises it, and the
	//     failure is silent: an empty result is indistinguishable at the wire
	//     from an honestly-empty palace.
	//
	//   * Keying on "was source_type supplied" — i.e. pruning only a BARE
	//     query — would leave the explicit source_type=decision spelling
	//     walking the whole wing to return exactly what the pruned default
	//     returns. That is the careful spelling, the one a generated client
	//     emits, and the wing it would walk is not small: 28,194 drawers on
	//     this project, 20,077 of them in `general` alone in a single file
	//     (internal/storage/drawers.go:76-84). Because step 4 above already
	//     turned "omitted" into {"decision"}, an omitted source_type, the
	//     scalar "decision" and ["decision"] are ONE case here — which is the
	//     whole reason the predicate is computed over the resolved set. The
	//     set also makes ["decision","decision"] prune.
	//
	// ACCEPTED COST: under this prune, a source_type=decision drawer sitting
	// OUTSIDE room=decisions is reachable only by naming room= explicitly.
	// That is the trade for not walking tens of thousands of transcript
	// chunks on every unqualified memory question, and the tool description
	// states it so a caller can undo it.
	if q.Room == "" && onlyDecisionSourceType(q.SourceTypes) {
		q.Room = capture.DecisionRoom
	}

	return q, nil
}

// onlyDecisionSourceType reports whether the resolved set is exactly
// {"decision"}. It is a set membership test, not a length-1 equality, so a
// caller that repeats itself — ["decision","decision"] — still prunes.
func onlyDecisionSourceType(sourceTypes []string) bool {
	if len(sourceTypes) == 0 {
		return false
	}
	for _, st := range sourceTypes {
		if st != storage.SourceTypeDecision {
			return false
		}
	}
	return true
}

// palaceQueryRow is a drawer plus the path segments that are not fields on it.
//
// Wing and Room are added HERE rather than on storage.Drawer on purpose:
// Drawer is the record serialized into every drawers.jsonl, so adding fields
// to it changes what is written to disk — the bump trigger at
// internal/surface/version.go:30-33.
type palaceQueryRow struct {
	storage.Drawer
	Wing string `json:"wing"`
	Room string `json:"room"`
}

type palaceQueryResult struct {
	Drawers []palaceQueryRow `json:"drawers"`

	// 🔴 THE TERMINAL SENTINEL — last field, no omitempty, always true on a
	// successful return. Its ABSENCE is the signal: absent ⇒ the host cut this
	// document and `drawers` is a PREFIX. That is the dangerous failure for a
	// memory query specifically: a truncated answer to "what did we decide
	// about X" reads as a COMPLETE answer that we decided nothing about X, and
	// the agent then reasons from an absence it invented rather than asking
	// again with a narrower filter. No omitempty, because a false bool would
	// vanish from the JSON and make "cut" and "whole" serialize identically.
	// Anything declared after this field re-opens the hole.
	Complete bool `json:"complete"`
}

// PalaceQueryTool returns the MCP tool for vp_palace_query.
//
// It takes a *storage.Vault and nothing else — the KGQueryTool shape
// (kg_tools.go:139) and the shape all five palace tools use. The scan under it
// is deterministic and embedder-free, so there is no search.Engine to thread
// and no model to load.
func PalaceQueryTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_palace_query",
		Description: "Return palace memories (drawers) matching hall / room / source_type / content substring / date range, newest first. " +
			"`source_type` DEFAULTS TO \"decision\", so transcript chunks are excluded unless you ask for them (pass source_type=session for tape, or an array for both); it accepts a single string or an array of strings. " +
			"An unqualified query is also PRUNED to the `decisions` room — the cost of that is real: a source_type=decision drawer filed outside room=decisions is only reachable by naming room= explicitly. Naming a room disables the prune. " +
			"`wing` defaults to the project slug, where every capture-written drawer lands; drawers written by the mempalace migrator live in other wings and need an explicit wing=. " +
			"`hall` is a TOPIC classification assigned by keyword matching, not a memory kind — source_type is the load-bearing key. " +
			"The result ENDS with `complete`: if you do not see `complete: true`, your host truncated it and `drawers` is a PREFIX — do not read the missing drawers as decisions that were never made.",
		Schema:  palaceQuerySchema,
		Handler: palaceQueryHandler(vault),
	}
}

func palaceQueryHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p palaceQueryParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}

		q, err := ResolvePalaceQuery(PalaceQueryInput{
			Project:     p.Project,
			Wing:        p.Wing,
			Room:        p.Room,
			Hall:        p.Hall,
			SourceTypes: p.SourceType,
			Q:           p.Q,
			DateFrom:    p.DateFrom,
			DateTo:      p.DateTo,
			Limit:       p.Limit,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve palace query: %w", err)
		}

		hits, err := vault.ScanDrawers(q)
		if err != nil {
			return nil, fmt.Errorf("scan drawers: %w", err)
		}

		// make(...) not var: ScanDrawers returns a NIL slice for the empty
		// case (drawer_query.go), and that is the normal path for every
		// default query until decision drawers exist. A nil slice serializes
		// as "drawers": null, which a client reads as a missing field rather
		// than as an empty answer — so the honest-empty result would look
		// malformed exactly when it is most correct.
		rows := make([]palaceQueryRow, 0, len(hits))
		for _, h := range hits {
			rows = append(rows, palaceQueryRow{Drawer: h.Drawer, Wing: h.Wing, Room: h.Room})
		}

		return palaceQueryResult{Drawers: rows, Complete: true}, nil
	}
}
