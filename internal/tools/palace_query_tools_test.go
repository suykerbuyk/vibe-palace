// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// 🔴 vp_palace_query does NOT bump internal/surface.MCPSurfaceVersion, and the
// tool-surface golden (internal/mcp/tool_surface.golden.json) is regenerated
// on its own. The bump rule at internal/surface/version.go:30-33 is scoped to
// "changes in a way that affects what gets WRITTEN INTO THE VAULT", and this
// tool writes nothing: it is a read-only scan, it is absent from
// MutatingToolNames, and it adds no field to storage.Drawer (palaceQueryRow
// carries wing/room instead, precisely so drawers.jsonl is untouched). The
// precedent is exact — 807bae7 added the read-only vp_manual and moved only
// the golden. A v3 host and a v4-less host read and write the same bytes.

const (
	// The fixture's project slug. It is ALSO the default wing, because
	// palace.DetectWing(project, "") returns the project slug — so seeding
	// into wing == project is what makes the wing default observable.
	pqProject = "pq-proj"

	pqOtherRoom = "general"
)

// Fixture contents, used as the identity of a hit throughout.
const (
	pqDecisionMatch    = "chose SQLite for the drawer index"
	pqDecisionNoMatch  = "adopted an append-only drawer writer"
	pqDecisionExtra    = "pinned the tool surface with a golden"
	pqSessionInRoom    = "the transcript says we chose SQLite for the drawer index, at length"
	pqDecisionOtherRm  = "a decision drawer filed outside the decisions room"
	pqSessionOtherRoom = "a session chunk filed outside the decisions room"
)

// palaceQueryVault seeds the one fixture every case below reads.
//
// 🔴 WHY THE SESSION CHUNK SITS IN room=decisions. pqSessionInRoom repeats the
// same words as pqDecisionMatch and is filed into the SAME room the default
// query prunes to. That placement is the point of the fixture, not an
// accident: if the session chunk lived in another room, the default query
// would never open that room, and "it was excluded because source_type
// defaults to decision" would be indistinguishable from "it was excluded
// because the prune never looked there". The assertion would still pass with
// the source-type default deleted, and mutation 1 below (widen the default to
// every source type) would go INERT. Inside room=decisions the scan actually
// reaches the drawer and must reject it on source_type alone.
//
// The two out-of-room drawers are the other half: they are what the prune
// itself is observable through.
func palaceQueryVault(t *testing.T) *storage.Vault {
	t.Helper()
	vault := newTestVault(t)

	seed := func(room string, ds ...storage.Drawer) {
		t.Helper()
		n, err := vault.AppendDrawers(pqProject, pqProject, room, ds)
		if err != nil {
			t.Fatalf("AppendDrawers(%s): %v", room, err)
		}
		if n != len(ds) {
			t.Fatalf("AppendDrawers(%s) filed %d of %d drawers", room, n, len(ds))
		}
	}

	seed("decisions",
		storage.Drawer{Hall: palace.HallDecisions, Content: pqDecisionMatch, SourceType: "decision", FiledAt: "2026-09-04T09:00:00Z"},
		storage.Drawer{Hall: palace.HallDecisions, Content: pqDecisionNoMatch, SourceType: "decision", FiledAt: "2026-09-03T09:00:00Z"},
		storage.Drawer{Hall: palace.HallDecisions, Content: pqSessionInRoom, SourceType: "session", FiledAt: "2026-09-02T09:00:00Z"},
		storage.Drawer{Hall: palace.HallFacts, Content: pqDecisionExtra, SourceType: "decision", FiledAt: "2026-09-01T09:00:00Z"},
	)
	seed(pqOtherRoom,
		storage.Drawer{Hall: palace.HallFacts, Content: pqDecisionOtherRm, SourceType: "decision", FiledAt: "2026-09-06T09:00:00Z"},
		storage.Drawer{Hall: palace.HallFacts, Content: pqSessionOtherRoom, SourceType: "session", FiledAt: "2026-09-05T09:00:00Z"},
	)
	return vault
}

// callPalaceQuery invokes the handler directly (the house pattern for handler
// tests, task_tools_test.go:19-33). It SKIPS schema validation — anything that
// must exercise the schema goes through dispatchPalaceQuery instead.
func callPalaceQuery(t *testing.T, vault *storage.Vault, params map[string]any) palaceQueryResult {
	t.Helper()
	tool := PalaceQueryTool(vault)
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	out, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler(%v): %v", params, err)
	}
	res, ok := out.(palaceQueryResult)
	if !ok {
		t.Fatalf("handler returned %T, want palaceQueryResult", out)
	}
	return res
}

// palaceQueryRegistry registers the tool alone so Dispatch runs the COMPILED
// schema before the handler.
func palaceQueryRegistry(t *testing.T, vault *storage.Vault) *mcp.Registry {
	t.Helper()
	reg := mcp.NewServer(vault).Registry()
	reg.MustRegister(PalaceQueryTool(vault))
	return reg
}

func dispatchPalaceQuery(t *testing.T, reg *mcp.Registry, raw json.RawMessage) (palaceQueryResult, error) {
	t.Helper()
	out, err := reg.Dispatch(context.Background(), "vp_palace_query", raw)
	if err != nil {
		return palaceQueryResult{}, err
	}
	res, ok := out.(palaceQueryResult)
	if !ok {
		t.Fatalf("dispatch returned %T, want palaceQueryResult", out)
	}
	return res, nil
}

func pqContents(res palaceQueryResult) []string {
	out := make([]string, 0, len(res.Drawers))
	for _, d := range res.Drawers {
		out = append(out, d.Content)
	}
	return out
}

func wantPQContents(t *testing.T, res palaceQueryResult, want ...string) {
	t.Helper()
	got := pqContents(res)
	// Length first: a variadic call with no arguments yields a nil `want`,
	// which DeepEqual would call unequal to the empty-but-non-nil slice
	// pqContents always returns.
	if len(got) != len(want) {
		t.Fatalf("drawers = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drawer %d = %q, want %q (full: %q, want %q)", i, got[i], want[i], got, want)
		}
	}
}

// --- Defaults ---

// TestPalaceQueryDefaultExcludesTranscript is the headline assertion: an
// unqualified query with only a substring returns the DECISION and not the
// transcript chunk that repeats its words — even though both sit in the room
// the prune lands in.
func TestPalaceQueryDefaultExcludesTranscript(t *testing.T) {
	res := callPalaceQuery(t, palaceQueryVault(t), map[string]any{
		"project": pqProject,
		"q":       "SQLite",
	})
	wantPQContents(t, res, pqDecisionMatch)
	if !res.Complete {
		t.Error("complete = false on a successful return")
	}
}

// TestPalaceQuerySourceTypeSessionReachesTape asserts the transcript corpus is
// reachable through the parameter that advertises it.
func TestPalaceQuerySourceTypeSessionReachesTape(t *testing.T) {
	res := callPalaceQuery(t, palaceQueryVault(t), map[string]any{
		"project":     pqProject,
		"q":           "SQLite",
		"source_type": "session",
	})
	wantPQContents(t, res, pqSessionInRoom)
}

// TestPalaceQueryEmptySerializesAsEmptyArray pins the wire shape of an honest
// zero. ScanDrawers returns a NIL slice for the empty case and that is the
// NORMAL path for a default query against a palace with no decisions yet, so
// the check is on the SERIALIZED bytes: "drawers": null would read to a client
// as a missing field rather than as an empty answer.
func TestPalaceQueryEmptySerializesAsEmptyArray(t *testing.T) {
	res := callPalaceQuery(t, palaceQueryVault(t), map[string]any{
		"project": pqProject,
		"q":       "no-drawer-contains-this-needle",
	})
	if len(res.Drawers) != 0 {
		t.Fatalf("drawers = %q, want none", pqContents(res))
	}

	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if got := string(raw); got != `{"drawers":[],"complete":true}` {
		t.Errorf("serialized empty result = %s, want {\"drawers\":[],\"complete\":true}", got)
	}
	if strings.Contains(string(raw), `"drawers":null`) {
		t.Errorf("empty result serialized as null: %s", raw)
	}
}

// TestPalaceQueryEmptyPalaceIsNotAnError covers the born-empty project: no
// palace store at all is an empty result, not a failure.
func TestPalaceQueryEmptyPalaceIsNotAnError(t *testing.T) {
	res := callPalaceQuery(t, newTestVault(t), map[string]any{"project": "never-captured"})
	if len(res.Drawers) != 0 || !res.Complete {
		t.Fatalf("result = %+v, want an empty complete result", res)
	}
}

func TestPalaceQueryLimitApplied(t *testing.T) {
	res := callPalaceQuery(t, palaceQueryVault(t), map[string]any{
		"project": pqProject,
		"limit":   2,
	})
	// filed_at descending, cut AFTER the sort.
	wantPQContents(t, res, pqDecisionMatch, pqDecisionNoMatch)
}

// TestPalaceQuerySortsNewestFirstThenID pins both keys: filed_at descending,
// and drawer ID ascending as the tie-break so two drawers stamped in the same
// second come back in a stable order.
func TestPalaceQuerySortsNewestFirstThenID(t *testing.T) {
	vault := newTestVault(t)
	tied := []storage.Drawer{
		{Content: "tie one", SourceType: "decision", FiledAt: "2026-09-02T00:00:00Z"},
		{Content: "tie two", SourceType: "decision", FiledAt: "2026-09-02T00:00:00Z"},
		{Content: "newest", SourceType: "decision", FiledAt: "2026-09-03T00:00:00Z"},
	}
	if _, err := vault.AppendDrawers(pqProject, pqProject, "decisions", tied); err != nil {
		t.Fatalf("AppendDrawers: %v", err)
	}

	res := callPalaceQuery(t, vault, map[string]any{"project": pqProject})
	if len(res.Drawers) != 3 {
		t.Fatalf("drawers = %q, want 3", pqContents(res))
	}
	if res.Drawers[0].Content != "newest" {
		t.Errorf("first drawer = %q, want the newest", res.Drawers[0].Content)
	}
	if a, b := res.Drawers[1].ID, res.Drawers[2].ID; a >= b {
		t.Errorf("tied drawers not ordered by ascending id: %q then %q", a, b)
	}
}

// TestPalaceQueryDateToIncludesSameDay is the date-prefix regression: a
// drawer filed at 14:23 on the closing day must survive date_to for that day.
// Comparing a full RFC3339 stamp against a date-only bound drops it, because
// "2026-09-08T14:23:00Z" > "2026-09-08" lexically.
func TestPalaceQueryDateToIncludesSameDay(t *testing.T) {
	vault := newTestVault(t)
	if _, err := vault.AppendDrawers(pqProject, pqProject, "decisions", []storage.Drawer{
		{Content: "filed in the afternoon", SourceType: "decision", FiledAt: "2026-09-08T14:23:00Z"},
	}); err != nil {
		t.Fatalf("AppendDrawers: %v", err)
	}

	res := callPalaceQuery(t, vault, map[string]any{
		"project": pqProject,
		"date_to": "2026-09-08",
	})
	wantPQContents(t, res, "filed in the afternoon")

	// The bound still bites a day earlier.
	res = callPalaceQuery(t, vault, map[string]any{
		"project": pqProject,
		"date_to": "2026-09-07",
	})
	wantPQContents(t, res)
}

func TestPalaceQueryDateFrom(t *testing.T) {
	res := callPalaceQuery(t, palaceQueryVault(t), map[string]any{
		"project":   pqProject,
		"date_from": "2026-09-03",
	})
	wantPQContents(t, res, pqDecisionMatch, pqDecisionNoMatch)
}

func TestPalaceQueryHallFilter(t *testing.T) {
	res := callPalaceQuery(t, palaceQueryVault(t), map[string]any{
		"project": pqProject,
		"hall":    palace.HallFacts,
	})
	wantPQContents(t, res, pqDecisionExtra)
}

// --- The prune ---

// TestPalaceQueryPruneOmittedSourceType is case (a): with no room named and no
// source_type, the walk is pruned to capture.DecisionRoom, so a decision
// drawer filed in another room is ABSENT — and present the moment the caller
// names its room.
func TestPalaceQueryPruneOmittedSourceType(t *testing.T) {
	vault := palaceQueryVault(t)

	pruned := callPalaceQuery(t, vault, map[string]any{"project": pqProject})
	wantPQContents(t, pruned, pqDecisionMatch, pqDecisionNoMatch, pqDecisionExtra)

	named := callPalaceQuery(t, vault, map[string]any{"project": pqProject, "room": pqOtherRoom})
	wantPQContents(t, named, pqDecisionOtherRm)
}

// TestPalaceQueryPruneExplicitDecisionIsIdentical is case (b) and the reason
// the prune predicate reads the RESOLVED source-type set rather than "was the
// parameter supplied": the bare query, the scalar "decision" and the
// one-element ["decision"] are ONE case, and the proof is byte-identical
// results. A prune keyed on supply would let the two explicit spellings walk
// the whole wing and pick up the out-of-room decision drawer.
func TestPalaceQueryPruneExplicitDecisionIsIdentical(t *testing.T) {
	vault := palaceQueryVault(t)

	marshal := func(params map[string]any) string {
		t.Helper()
		raw, err := json.Marshal(callPalaceQuery(t, vault, params))
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		return string(raw)
	}

	omitted := marshal(map[string]any{"project": pqProject})
	scalar := marshal(map[string]any{"project": pqProject, "source_type": "decision"})
	array := marshal(map[string]any{"project": pqProject, "source_type": []string{"decision"}})
	dupes := marshal(map[string]any{"project": pqProject, "source_type": []string{"decision", "decision"}})

	if scalar != omitted {
		t.Errorf("scalar source_type=\"decision\" differs from the omitted default:\n scalar: %s\nomitted: %s", scalar, omitted)
	}
	if array != omitted {
		t.Errorf("array source_type=[\"decision\"] differs from the omitted default:\n  array: %s\nomitted: %s", array, omitted)
	}
	if dupes != omitted {
		t.Errorf("repeated source_type=[\"decision\",\"decision\"] differs from the omitted default:\n dupes: %s\nomitted: %s", dupes, omitted)
	}
}

// TestPalaceQuerySessionDoesNotPrune is case (c): a source-type set that is
// not exactly {"decision"} walks the wing, so a session drawer OUTSIDE
// room=decisions comes back. Pruning on "no room named" alone would send this
// query into a room that by construction holds no session drawers, and the
// tape corpus would be unreachable through the parameter that advertises it.
func TestPalaceQuerySessionDoesNotPrune(t *testing.T) {
	res := callPalaceQuery(t, palaceQueryVault(t), map[string]any{
		"project":     pqProject,
		"source_type": "session",
	})
	wantPQContents(t, res, pqSessionOtherRoom, pqSessionInRoom)
}

// TestPalaceQueryMixedSourceTypesDoNotPrune is case (c2).
func TestPalaceQueryMixedSourceTypesDoNotPrune(t *testing.T) {
	res := callPalaceQuery(t, palaceQueryVault(t), map[string]any{
		"project":     pqProject,
		"source_type": []string{"decision", "session"},
	})
	wantPQContents(t, res,
		pqDecisionOtherRm,  // 09-06, general
		pqSessionOtherRoom, // 09-05, general
		pqDecisionMatch,    // 09-04, decisions
		pqDecisionNoMatch,  // 09-03, decisions
		pqSessionInRoom,    // 09-02, decisions
		pqDecisionExtra,    // 09-01, decisions
	)
}

// TestPalaceQueryExplicitRoomIsNeverOverridden is case (d). Writing out
// room=decisions must behave exactly like the prune that would have chosen it,
// and naming another room with source_type omitted must stay in that room
// rather than being redirected.
func TestPalaceQueryExplicitRoomIsNeverOverridden(t *testing.T) {
	vault := palaceQueryVault(t)

	spelled := callPalaceQuery(t, vault, map[string]any{"project": pqProject, "room": "decisions"})
	wantPQContents(t, spelled, pqDecisionMatch, pqDecisionNoMatch, pqDecisionExtra)

	other := callPalaceQuery(t, vault, map[string]any{"project": pqProject, "room": pqOtherRoom})
	wantPQContents(t, other, pqDecisionOtherRm)

	otherSession := callPalaceQuery(t, vault, map[string]any{
		"project":     pqProject,
		"room":        pqOtherRoom,
		"source_type": "session",
	})
	wantPQContents(t, otherSession, pqSessionOtherRoom)
}

// --- Wire shape ---

// TestPalaceQueryScalarSourceTypeSurvivesValidation goes through reg.Dispatch
// ON PURPOSE. A test that calls tool.Handler directly with hand-built params
// SKIPS validateParams (internal/mcp/tools.go:335) and would pass even under
// an array-only schema — that is exactly the failure mode being guarded here,
// and it is the one that cost a lost vp_capture_session call
// (session_tools.go:226-233). Only the dispatch path proves the scalar
// spelling is accepted at the wire.
func TestPalaceQueryScalarSourceTypeSurvivesValidation(t *testing.T) {
	reg := palaceQueryRegistry(t, palaceQueryVault(t))

	scalar, err := dispatchPalaceQuery(t, reg, json.RawMessage(`{"project":"`+pqProject+`","source_type":"session"}`))
	if err != nil {
		t.Fatalf("scalar source_type rejected at the wire: %v", err)
	}
	array, err := dispatchPalaceQuery(t, reg, json.RawMessage(`{"project":"`+pqProject+`","source_type":["session"]}`))
	if err != nil {
		t.Fatalf("array source_type rejected at the wire: %v", err)
	}
	if !reflect.DeepEqual(pqContents(scalar), pqContents(array)) {
		t.Errorf("scalar and array spellings disagree: %q vs %q", pqContents(scalar), pqContents(array))
	}
	wantPQContents(t, scalar, pqSessionOtherRoom, pqSessionInRoom)
}

// TestPalaceQueryEmptySourceTypeBehavesAsOmitted covers the two "supplied but
// carries nothing" spellings flexStringList collapses to nil.
//
// The empty string goes through Dispatch, where the schema accepts it as a
// string. JSON null is asserted at the HANDLER: the schema's
// "type": ["array","string"] deliberately does not admit null, so null is
// refused at validation and never reaches the decoder on the wire — the
// handler-level assertion pins flexStringList's null branch for any caller
// (a future `vp palace query`) that decodes without the schema in front of it.
func TestPalaceQueryEmptySourceTypeBehavesAsOmitted(t *testing.T) {
	vault := palaceQueryVault(t)
	reg := palaceQueryRegistry(t, vault)

	baseline := callPalaceQuery(t, vault, map[string]any{"project": pqProject})

	empty, err := dispatchPalaceQuery(t, reg, json.RawMessage(`{"project":"`+pqProject+`","source_type":""}`))
	if err != nil {
		t.Fatalf(`source_type:"" rejected: %v`, err)
	}
	if !reflect.DeepEqual(pqContents(empty), pqContents(baseline)) {
		t.Errorf(`source_type:"" = %q, want the omitted default %q`, pqContents(empty), pqContents(baseline))
	}

	tool := PalaceQueryTool(vault)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"`+pqProject+`","source_type":null}`))
	if err != nil {
		t.Fatalf("source_type:null rejected by the handler: %v", err)
	}
	null := out.(palaceQueryResult)
	if !reflect.DeepEqual(pqContents(null), pqContents(baseline)) {
		t.Errorf("source_type:null = %q, want the omitted default %q", pqContents(null), pqContents(baseline))
	}
}

// TestPalaceQueryHallEnumRejectsUnknown proves the enum is ENFORCED, not
// decorative: a hall outside the palace.Hall* set fails validation before the
// handler runs.
func TestPalaceQueryHallEnumRejectsUnknown(t *testing.T) {
	reg := palaceQueryRegistry(t, palaceQueryVault(t))

	_, err := reg.Dispatch(context.Background(), "vp_palace_query",
		json.RawMessage(`{"project":"`+pqProject+`","hall":"not-a-hall"}`))
	if err == nil {
		t.Fatal("hall=not-a-hall was accepted; the enum is not enforced")
	}
	var verr *mcp.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v (%T), want a *mcp.ValidationError from the enum check", err, err)
	}

	for _, hall := range palaceQueryHalls {
		if _, err := reg.Dispatch(context.Background(), "vp_palace_query",
			json.RawMessage(`{"project":"`+pqProject+`","hall":"`+hall+`"}`)); err != nil {
			t.Errorf("hall=%q rejected by its own enum: %v", hall, err)
		}
	}
}

// TestPalaceQueryRequiresProject: the schema requires it, and the resolver
// refuses it independently for the non-MCP caller.
func TestPalaceQueryRequiresProject(t *testing.T) {
	reg := palaceQueryRegistry(t, palaceQueryVault(t))
	_, err := reg.Dispatch(context.Background(), "vp_palace_query", json.RawMessage(`{}`))
	var verr *mcp.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v (%T), want a *mcp.ValidationError for the missing project", err, err)
	}

	if _, rerr := ResolvePalaceQuery(PalaceQueryInput{}); rerr == nil {
		t.Fatal("ResolvePalaceQuery with no project returned nil error")
	}
}

func TestPalaceQueryHandlerRejectsMalformedParams(t *testing.T) {
	tool := PalaceQueryTool(palaceQueryVault(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"limit":`)); err == nil {
		t.Fatal("malformed params accepted")
	}
	// A well-formed document whose project is empty fails in the resolver.
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"project":""}`)); err == nil {
		t.Fatal("empty project accepted by the handler")
	}
}

// --- The resolver, directly ---

func TestResolvePalaceQueryDefaults(t *testing.T) {
	q, err := ResolvePalaceQuery(PalaceQueryInput{Project: pqProject})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if q.Wing != pqProject {
		t.Errorf("wing = %q, want the project slug", q.Wing)
	}
	if q.Room != "decisions" {
		t.Errorf("room = %q, want the pruned decisions room", q.Room)
	}
	if !reflect.DeepEqual(q.SourceTypes, []string{"decision"}) {
		t.Errorf("source types = %q, want [decision]", q.SourceTypes)
	}
	if q.Limit != defaultPalaceQueryLimit {
		t.Errorf("limit = %d, want %d", q.Limit, defaultPalaceQueryLimit)
	}
}

func TestResolvePalaceQueryLimitClamp(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-5, defaultPalaceQueryLimit},
		{0, defaultPalaceQueryLimit},
		{1, 1},
		{50, 50},
		{51, maxPalaceQueryLimit},
		{100000, maxPalaceQueryLimit},
	} {
		q, err := ResolvePalaceQuery(PalaceQueryInput{Project: pqProject, Limit: tc.in})
		if err != nil {
			t.Fatalf("resolve(limit=%d): %v", tc.in, err)
		}
		if q.Limit != tc.want {
			t.Errorf("limit %d resolved to %d, want %d", tc.in, q.Limit, tc.want)
		}
	}
}

// TestResolvePalaceQueryPruneMatrix pins the prune predicate itself, one row
// per branch, so a change to it fails here in words rather than only through
// the fixture.
func TestResolvePalaceQueryPruneMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		room        string
		sourceTypes []string
		wantRoom    string
	}{
		{"omitted prunes", "", nil, "decisions"},
		{"explicit decision prunes", "", []string{"decision"}, "decisions"},
		{"repeated decision prunes", "", []string{"decision", "decision"}, "decisions"},
		{"session does not prune", "", []string{"session"}, ""},
		{"mixed does not prune", "", []string{"decision", "session"}, ""},
		{"migrator type does not prune", "", []string{"iteration"}, ""},
		{"named room wins over the prune", pqOtherRoom, nil, pqOtherRoom},
		{"named decisions room is kept", "decisions", nil, "decisions"},
		{"named room wins with session too", pqOtherRoom, []string{"session"}, pqOtherRoom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ResolvePalaceQuery(PalaceQueryInput{
				Project:     pqProject,
				Room:        tc.room,
				SourceTypes: tc.sourceTypes,
			})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if q.Room != tc.wantRoom {
				t.Errorf("room = %q, want %q", q.Room, tc.wantRoom)
			}
		})
	}
}

// TestResolvePalaceQueryPassesThroughExplicitFields guards against a field
// being dropped on the way into the scan filter.
func TestResolvePalaceQueryPassesThroughExplicitFields(t *testing.T) {
	q, err := ResolvePalaceQuery(PalaceQueryInput{
		Project:     pqProject,
		Wing:        "migrated-wing",
		Room:        pqOtherRoom,
		Hall:        palace.HallFacts,
		SourceTypes: []string{"session"},
		Q:           "needle",
		DateFrom:    "2026-01-01",
		DateTo:      "2026-12-31",
		Limit:       7,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := storage.DrawerQuery{
		Project:     pqProject,
		Wing:        "migrated-wing",
		Room:        pqOtherRoom,
		Hall:        palace.HallFacts,
		SourceTypes: []string{"session"},
		Q:           "needle",
		DateFrom:    "2026-01-01",
		DateTo:      "2026-12-31",
		Limit:       7,
	}
	if !reflect.DeepEqual(q, want) {
		t.Errorf("resolved = %+v, want %+v", q, want)
	}
}

// TestPalaceQueryMigratorWingNeedsExplicitWing records the accepted scope: a
// wing the mempalace migrator wrote is invisible to the default and reachable
// by naming it.
func TestPalaceQueryMigratorWingNeedsExplicitWing(t *testing.T) {
	vault := palaceQueryVault(t)
	if _, err := vault.AppendDrawers(pqProject, "migrated-wing", "decisions", []storage.Drawer{
		{Content: "a decision the migrator carried over", SourceType: "decision", FiledAt: "2026-09-07T09:00:00Z"},
	}); err != nil {
		t.Fatalf("AppendDrawers: %v", err)
	}

	defaulted := callPalaceQuery(t, vault, map[string]any{"project": pqProject})
	wantPQContents(t, defaulted, pqDecisionMatch, pqDecisionNoMatch, pqDecisionExtra)

	named := callPalaceQuery(t, vault, map[string]any{"project": pqProject, "wing": "migrated-wing"})
	wantPQContents(t, named, "a decision the migrator carried over")
	if named.Drawers[0].Wing != "migrated-wing" || named.Drawers[0].Room != "decisions" {
		t.Errorf("row segments = %q/%q, want migrated-wing/decisions", named.Drawers[0].Wing, named.Drawers[0].Room)
	}
}

// --- Schema / wiring invariants ---

// TestPalaceQuerySchemaHallEnumMatchesConstants is the LINK between the
// palace.Hall* constants and the raw-JSON schema literal. The schema is
// assembled by injecting palaceQueryHalls, so this asserts the injection
// survived and that the list is the full constant set — a hall constant added
// without touching palaceQueryHalls shows up here rather than as a value the
// tool silently refuses.
func TestPalaceQuerySchemaHallEnumMatchesConstants(t *testing.T) {
	var schema struct {
		Properties struct {
			Hall struct {
				Enum []string `json:"enum"`
			} `json:"hall"`
			SourceType struct {
				Type []string `json:"type"`
			} `json:"source_type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(palaceQuerySchema, &schema); err != nil {
		t.Fatalf("decode palaceQuerySchema: %v\n%s", err, palaceQuerySchema)
	}

	want := []string{
		palace.HallDecisions,
		palace.HallDiscoveries,
		palace.HallPreferences,
		palace.HallAdvice,
		palace.HallEvents,
		palace.HallFacts,
	}
	if !reflect.DeepEqual(schema.Properties.Hall.Enum, want) {
		t.Errorf("hall enum = %q, want the palace.Hall* constants %q", schema.Properties.Hall.Enum, want)
	}

	// The permissive source_type type is a correctness requirement, not a
	// style choice: "array" alone rejects every scalar spelling at validation,
	// before flexStringList ever runs.
	if !reflect.DeepEqual(schema.Properties.SourceType.Type, []string{"array", "string"}) {
		t.Errorf(`source_type type = %q, want ["array","string"]`, schema.Properties.SourceType.Type)
	}
	if !reflect.DeepEqual(schema.Required, []string{"project"}) {
		t.Errorf("required = %q, want [project]", schema.Required)
	}
}

// TestPalaceQueryToolIsReadOnly pins the classification the two lists encode:
// the tool is not Mutating, has no per-invocation refinement, and is
// allow-listed for a read-only serve.
func TestPalaceQueryToolIsReadOnly(t *testing.T) {
	tool := PalaceQueryTool(newTestVault(t))
	if tool.Name != "vp_palace_query" {
		t.Errorf("name = %q", tool.Name)
	}
	if tool.Mutating {
		t.Error("vp_palace_query is registered Mutating; it writes nothing")
	}
	if tool.ReadOnlyWhen != nil {
		t.Error("vp_palace_query has a ReadOnlyWhen refinement; it is unconditionally read-only")
	}

	var served bool
	for _, name := range ReadOnlyServeToolNames {
		if name == "vp_palace_query" {
			served = true
		}
	}
	if !served {
		t.Error("vp_palace_query is missing from ReadOnlyServeToolNames")
	}
	for _, name := range MutatingToolNames {
		if name == "vp_palace_query" {
			t.Error("vp_palace_query must not be in MutatingToolNames")
		}
	}
}

// TestPalaceQueryRegistersWithoutAnEngine: the constructor takes a
// *storage.Vault and nothing else, so the tool must be present on the
// nil-engine registration path — never behind the `if engine != nil` branch.
// No embedder is constructed anywhere on this path, which is what keeps the
// tool off the tens-of-seconds cold-start road check_tool_test.go:392-427
// guards for vp_check.
func TestPalaceQueryRegistersWithoutAnEngine(t *testing.T) {
	vault := palaceQueryVault(t)
	srv := mcp.NewServer(vault)
	RegisterAll(srv.Registry(), vpctx.NewResolver(vault.Root), vault, nil)

	var found bool
	for _, tool := range srv.Registry().List() {
		if tool.Name == "vp_palace_query" {
			found = true
		}
	}
	if !found {
		t.Fatal("vp_palace_query is not registered when engine is nil")
	}

	res, err := dispatchPalaceQuery(t, srv.Registry(), json.RawMessage(`{"project":"`+pqProject+`","q":"SQLite"}`))
	if err != nil {
		t.Fatalf("dispatch on the nil-engine registry: %v", err)
	}
	wantPQContents(t, res, pqDecisionMatch)
}
