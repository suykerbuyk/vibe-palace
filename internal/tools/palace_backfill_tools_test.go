// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// 🔴 THE ASSERTION RULE FOR THIS FILE: nothing here opens
// palace/<project>/drawers/<wing>/<room>/drawers.jsonl, and nothing reads a
// session note back off disk. Writes go in through the vp_palace_backfill_decisions
// handler and come back out through vp_palace_query. A test that reached past
// both would pass while the two tools disagreed about which wing, which room
// or which source_type a decision lives under — and that disagreement is
// exactly the failure this feature can have: a drawer correctly on disk that
// the default memory query never returns, which reads to an agent as "no
// decisions were recorded".

const (
	// pbProject is the fixture slug. It is ALSO the wing, because
	// palace.DetectWing(project, "") returns the project slug — so a backfill
	// that landed in some other wing would be invisible to the default
	// vp_palace_query, which is precisely what the round-trip assertions catch.
	pbProject = "pb-proj"

	// pbDate is deliberately far from any plausible run date, so a filed_at
	// taken from the wall clock cannot coincide with the note's own day.
	pbDate      = "2026-03-15"
	pbSessionID = "2026-03-15-01"

	pbDecision = "chose the flat sessions layout for the backfill walk"
)

// pbWriteNote drops a session note into Projects/<project>/sessions verbatim.
//
// It writes RAW BYTES rather than going through Vault.WriteSession because two
// of the cases below need frontmatter that no writer would ever produce — a
// note with no delimiters at all, and one with a malformed date: — and because
// the exact session_id and date are load-bearing in the source_ref and
// filed_at assertions. Fixture construction is not an assertion; every check
// still goes through the two tools.
func pbWriteNote(t *testing.T, vault *storage.Vault, project, filename, content string) {
	t.Helper()
	dir, err := vault.SessionDir(project)
	if err != nil {
		t.Fatalf("SessionDir(%s): %v", project, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatalf("write note %s: %v", filename, err)
	}
}

// pbNote renders a well-formed session note. summary and decisions are the two
// axes every case below varies.
func pbNote(sessionID, date, summary string, decisions []string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "session_id: %s\n", sessionID)
	fmt.Fprintf(&b, "project: %s\n", pbProject)
	fmt.Fprintf(&b, "date: %s\n", date)
	b.WriteString("iteration: 1\n")
	if summary != "" {
		fmt.Fprintf(&b, "summary: %q\n", summary)
	}
	if len(decisions) > 0 {
		b.WriteString("decisions:\n")
		for _, d := range decisions {
			fmt.Fprintf(&b, "  - %q\n", d)
		}
	}
	b.WriteString("---\n\n")
	b.WriteString("## Notes\n\nSome prose the backfill must never mine.\n")
	return b.String()
}

// callBackfill invokes the handler directly, the house pattern for handler
// tests. Cases that must exercise the compiled schema go through
// dispatchBackfill instead.
func callBackfill(t *testing.T, vault *storage.Vault, params map[string]any) palaceBackfillDecisionsResult {
	t.Helper()
	tool := PalaceBackfillDecisionsTool(vault)
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	out, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler(%v): %v", params, err)
	}
	res, ok := out.(palaceBackfillDecisionsResult)
	if !ok {
		t.Fatalf("handler returned %T, want palaceBackfillDecisionsResult", out)
	}
	return res
}

// backfillRegistry registers the tool alone so Dispatch runs the COMPILED
// schema (and the surface gate) before the handler.
func backfillRegistry(t *testing.T, vault *storage.Vault) *mcp.Registry {
	t.Helper()
	reg := mcp.NewServer(vault).Registry()
	reg.MustRegister(PalaceBackfillDecisionsTool(vault))
	return reg
}

func dispatchBackfill(t *testing.T, reg *mcp.Registry, raw json.RawMessage) (palaceBackfillDecisionsResult, error) {
	t.Helper()
	out, err := reg.Dispatch(context.Background(), palaceBackfillDecisionsName, raw)
	if err != nil {
		return palaceBackfillDecisionsResult{}, err
	}
	res, ok := out.(palaceBackfillDecisionsResult)
	if !ok {
		t.Fatalf("dispatch returned %T, want palaceBackfillDecisionsResult", out)
	}
	return res, nil
}

// pbCounts renders the tally for a failure message.
func pbCounts(c palaceBackfillCounts) string {
	return fmt.Sprintf("scanned=%d found=%d appended=%d dup=%d unparseable=%d",
		c.NotesScanned, c.DecisionsFound, c.Appended, c.SkippedDup, c.SkippedUnparseable)
}

func wantBackfillCounts(t *testing.T, got palaceBackfillCounts, want palaceBackfillCounts) {
	t.Helper()
	if got != want {
		t.Fatalf("counts = %s, want %s", pbCounts(got), pbCounts(want))
	}
}

// --- The three-call sequence ---

// TestPalaceBackfillDryRunThenApplyThenDedup is the headline behaviour, and it
// is one test rather than three because the ORDER is the property: the dry run
// must leave the palace in the state the first apply then acts on, and the
// second apply must see what the first one wrote.
//
// The closing vp_palace_query is a DEFAULT query — project only, no room, no
// source_type, no wing. That is the shape an agent actually asks a memory
// question in, and it is what proves the backfill wrote into the wing the
// query defaults to and the room it prunes to, with the source_type the
// default filter selects. A drawer that missed any one of those three would
// still be on disk and would still be unreachable.
func TestPalaceBackfillDryRunThenApplyThenDedup(t *testing.T) {
	vault := newTestVault(t)
	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md",
		pbNote(pbSessionID, pbDate, "a quiet session", []string{pbDecision}))

	// 1. Default call: no apply key at all.
	dry := callBackfill(t, vault, map[string]any{"project": pbProject})
	if dry.Apply {
		t.Error("apply echoed true on a call that never named it")
	}
	wantBackfillCounts(t, dry.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 1, DecisionsFound: 1,
	})
	// Nothing reached the palace.
	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{"project": pbProject}))

	// 2. apply: true writes exactly the one drawer the dry run counted.
	applied := callBackfill(t, vault, map[string]any{"project": pbProject, "apply": true})
	if !applied.Apply {
		t.Error("apply echoed false on an apply=true call")
	}
	wantBackfillCounts(t, applied.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 1, DecisionsFound: 1, Appended: 1,
	})

	// 3. A second apply is a no-op: the append dedups on content.
	again := callBackfill(t, vault, map[string]any{"project": pbProject, "apply": true})
	wantBackfillCounts(t, again.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 1, DecisionsFound: 1, SkippedDup: 1,
	})

	// 4. The DEFAULT memory question returns it.
	res := callPalaceQuery(t, vault, map[string]any{"project": pbProject})
	wantPQContents(t, res, pbDecision)
	if got := res.Drawers[0].SourceType; got != storage.SourceTypeDecision {
		t.Errorf("source_type = %q, want %q", got, storage.SourceTypeDecision)
	}
	if got := res.Drawers[0].Room; got != capture.DecisionRoom {
		t.Errorf("room = %q, want %q", got, capture.DecisionRoom)
	}
	if got := res.Drawers[0].Wing; got != pbProject {
		t.Errorf("wing = %q, want the project slug %q", got, pbProject)
	}
}

// --- The bare call ---

// TestPalaceBackfillBareCallIsADryRun: `{}` is the probe an agent makes when
// it first meets a tool, and it must be safe — no error, no write, and a
// report that says it walked nothing rather than one that looks like an empty
// palace. Every parameter is absent, so this is also where the VALUE-typed
// apply earns itself: a *bool would leave "absent" as a third state.
func TestPalaceBackfillBareCallIsADryRun(t *testing.T) {
	vault := newTestVault(t)
	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md",
		pbNote(pbSessionID, pbDate, "", []string{pbDecision}))

	tool := PalaceBackfillDecisionsTool(vault)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("a bare {} call was refused: %v", err)
	}
	res, ok := out.(palaceBackfillDecisionsResult)
	if !ok {
		t.Fatalf("handler returned %T, want palaceBackfillDecisionsResult", out)
	}
	if res.Apply {
		t.Error("apply defaulted to true on a bare call")
	}
	if len(res.Projects) != 0 {
		t.Errorf("projects = %+v, want none — a bare call names no target", res.Projects)
	}
	wantBackfillCounts(t, res.palaceBackfillCounts, palaceBackfillCounts{})
	if !res.Complete {
		t.Error("complete = false on a successful return")
	}

	// And the palace is untouched.
	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{"project": pbProject}))
}

// TestPalaceBackfillBareCallClearsSchemaValidation is the wire half of the
// case above, and it is a SEPARATE test because the two checks cannot be made
// by the same call. Dispatch validates first and gates second, and the gate
// needs a vault in this server's context that only the real transport injects
// (see the same note in context_query_tools_test.go), so a dispatched call can
// never reach this handler from a package test. What it CAN prove is that `{}`
// clears the compiled schema: the error that comes back must not be a
// *mcp.ValidationError. A `required` list on project would fail here.
//
// The refusal it DOES come back with is itself the assertion of the next test.
func TestPalaceBackfillBareCallClearsSchemaValidation(t *testing.T) {
	reg := backfillRegistry(t, newTestVault(t))
	_, err := dispatchBackfill(t, reg, json.RawMessage(`{}`))
	var verr *mcp.ValidationError
	if errors.As(err, &verr) {
		t.Fatalf("a bare {} call was rejected by the schema: %v", err)
	}
}

// TestPalaceBackfillGateRefusesTheDryRunToo is the observable consequence of
// declaring no ReadOnlyWhen. The tool is Mutating, so the dispatch gate
// refuses a call with no vault in context — and it refuses the DRY RUN, which
// writes nothing, exactly as it refuses an apply. A ReadOnlyWhen admitting
// apply=false would let the first of these two through.
//
// That is the intended asymmetry, not an oversight: the dry run's whole
// product is a count the operator then authorizes a write from, so a binary
// that cannot be trusted to parse the vault must not be trusted to produce it.
func TestPalaceBackfillGateRefusesTheDryRunToo(t *testing.T) {
	reg := backfillRegistry(t, newTestVault(t))
	for _, raw := range []string{
		`{"project":"` + pbProject + `"}`,
		`{"project":"` + pbProject + `","apply":false}`,
		`{"project":"` + pbProject + `","apply":true}`,
	} {
		_, err := dispatchBackfill(t, reg, json.RawMessage(raw))
		if err == nil {
			t.Errorf("%s passed the mutating gate with no vault in context", raw)
			continue
		}
		var verr *mcp.ValidationError
		if errors.As(err, &verr) {
			t.Errorf("%s failed schema validation rather than the gate: %v", raw, err)
		}
	}
}

// TestPalaceBackfillApplyWithNoTargetIsRefused is the other half of the bare
// call: silently writing nothing is an acceptable answer to a QUESTION and
// never an acceptable answer to an INSTRUCTION.
func TestPalaceBackfillApplyWithNoTargetIsRefused(t *testing.T) {
	tool := PalaceBackfillDecisionsTool(newTestVault(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"apply":true}`)); err == nil {
		t.Fatal("apply=true with neither project nor all was accepted")
	}
	// The dry-run spelling of the same call is fine.
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("the dry-run spelling of the same targetless call was refused: %v", err)
	}
}

// --- The body is never mined ---

// TestPalaceBackfillNeverMinesTheBody is the scope assertion. The note's
// summary contains the word "decided" and its body is prose about decisions;
// its YAML decisions: list is empty. A backfill that fell back to the body —
// or to a `## Decisions` heading — would file something here.
func TestPalaceBackfillNeverMinesTheBody(t *testing.T) {
	vault := newTestVault(t)
	note := "---\n" +
		"session_id: " + pbSessionID + "\n" +
		"project: " + pbProject + "\n" +
		"date: " + pbDate + "\n" +
		"iteration: 1\n" +
		"summary: \"we decided to keep the flat layout\"\n" +
		"decisions:\n" +
		"---\n\n" +
		"## Decisions\n\n- we decided to keep the flat layout\n\n" +
		"## Open Threads\n\n- whether we decided anything about wings\n"
	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md", note)

	res := callBackfill(t, vault, map[string]any{"project": pbProject, "apply": true})
	wantBackfillCounts(t, res.palaceBackfillCounts, palaceBackfillCounts{NotesScanned: 1})

	// Nothing at all, under the default query or under a substring search for
	// the words the body actually contains.
	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{"project": pbProject}))
	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{"project": pbProject, "q": "decided"}))
}

// --- One bad note does not abort the run ---

// TestPalaceBackfillSkipsBadNotesAndContinues is why this walker exists
// instead of Vault.ListSessions: that one returns (nil, err) on the FIRST note
// it cannot read or parse and discards everything already collected, so a
// single hand-edited note would abort the recovery of every note behind it.
//
// The two bad notes are the two distinct failures — frontmatter that will not
// parse, and frontmatter that parses into a date capture.DecisionFiledAt
// refuses — and both sort BETWEEN the good notes so the walk has to survive
// them and keep going rather than merely tolerate a bad note at the end.
func TestPalaceBackfillSkipsBadNotesAndContinues(t *testing.T) {
	const (
		first  = "the first decision, before the damage"
		second = "the second decision, after the damage"
	)
	vault := newTestVault(t)
	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md",
		pbNote("2026-03-15-01", "2026-03-15", "", []string{first}))
	pbWriteNote(t, vault, pbProject, "2026-03-16-01.md",
		"this note has no frontmatter delimiters at all\n")
	pbWriteNote(t, vault, pbProject, "2026-03-17-01.md",
		pbNote("2026-03-17-01", "the fifteenth of March", "", []string{"a decision the walk cannot date"}))
	pbWriteNote(t, vault, pbProject, "2026-03-18-01.md",
		pbNote("2026-03-18-01", "2026-03-18", "", []string{second}))

	res := callBackfill(t, vault, map[string]any{"project": pbProject, "apply": true})
	wantBackfillCounts(t, res.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 2, DecisionsFound: 2, Appended: 2, SkippedUnparseable: 2,
	})

	// Both good notes were filed, newest first.
	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{"project": pbProject}), second, first)

	// 🔴 The undatable note's decision was DROPPED, not stamped with the run's
	// wall clock. A fallback would file it under whatever day the backfill ran
	// on, which is the exact mis-dating capture.DecisionFiledAt exists to
	// prevent — and it would be invisible here without this check, because a
	// wrongly-dated drawer still comes back from an unbounded query.
	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{"project": pbProject, "q": "cannot date"}))
}

// --- filed_at is the note's own day ---

// TestPalaceBackfillStampsTheNotesOwnDay asserts the EXACT stamp, not its
// 10-byte date prefix. The prefix would pass for any time on the right day,
// and the widening rule is specifically midnight UTC: a backfilled decision
// and a live one of the same day must sort together, which they only do if
// both carry the identical stamp rather than merely the identical date.
func TestPalaceBackfillStampsTheNotesOwnDay(t *testing.T) {
	vault := newTestVault(t)
	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md",
		pbNote(pbSessionID, pbDate, "", []string{pbDecision}))

	callBackfill(t, vault, map[string]any{"project": pbProject, "apply": true})

	res := callPalaceQuery(t, vault, map[string]any{"project": pbProject})
	wantPQContents(t, res, pbDecision)
	if got := res.Drawers[0].FiledAt; got != "2026-03-15T00:00:00Z" {
		t.Errorf("filed_at = %q, want the note's own day 2026-03-15T00:00:00Z", got)
	}

	// A date bound on the note's day returns it — the property the stamp is
	// FOR. A run-dated drawer would fall outside this window.
	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{
		"project": pbProject, "date_from": pbDate, "date_to": pbDate,
	}), pbDecision)
}

// --- source_ref carries the per-note index ---

// TestPalaceBackfillSourceRefsCarryTheNoteIndex pins the EXACT refs, not their
// shape. The ref is what search dedups on: hand every decision of one note the
// same ref and a three-decision note returns exactly ONE vp_search hit forever,
// with the other two unreachable and nothing erroring.
func TestPalaceBackfillSourceRefsCarryTheNoteIndex(t *testing.T) {
	decisions := []string{
		"first: the walker globs a flat sessions directory",
		"second: the append is batched once per project",
		"third: a dry run reports found and writes nothing",
	}
	vault := newTestVault(t)
	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md",
		pbNote(pbSessionID, pbDate, "", decisions))

	res := callBackfill(t, vault, map[string]any{"project": pbProject, "apply": true})
	wantBackfillCounts(t, res.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 1, DecisionsFound: 3, Appended: 3,
	})

	// Newest-first with a filed_at tie falls back to ascending drawer ID, so
	// index the returned rows by content instead of by position.
	byContent := make(map[string]palaceQueryRow)
	for _, row := range callPalaceQuery(t, vault, map[string]any{"project": pbProject}).Drawers {
		byContent[row.Content] = row
	}
	if len(byContent) != len(decisions) {
		t.Fatalf("query returned %d distinct drawers, want %d", len(byContent), len(decisions))
	}

	for n, d := range decisions {
		row, ok := byContent[d]
		if !ok {
			t.Fatalf("decision %d (%q) is not in the palace", n, d)
		}
		// The ref's own last segment IS the drawer id, and the id is
		// DrawerID(wing, content) — the wing being the project slug.
		want := fmt.Sprintf("session/%s#decision/%d/%s", pbSessionID, n, storage.DrawerID(pbProject, d))
		if row.SourceRef != want {
			t.Errorf("decision %d source_ref = %q, want %q", n, row.SourceRef, want)
		}
		if !strings.HasSuffix(row.SourceRef, "/"+row.ID) {
			t.Errorf("decision %d source_ref %q does not end in its own drawer id %q", n, row.SourceRef, row.ID)
		}
	}
}

// --- all: true ---

// TestPalaceBackfillAllWalksEveryProjectWithSessions covers the operator-wide
// spelling, including the project that has session notes but has never been
// drawer-indexed. That project is the reason the enumeration is
// ListAllProjects and not ListProjects: the latter reads palace/, so it would
// skip exactly the projects with the most decisions to recover and report a
// clean zero while doing it.
func TestPalaceBackfillAllWalksEveryProjectWithSessions(t *testing.T) {
	const (
		otherProject  = "pb-other"
		otherDecision = "the other project decided to keep its own wing"
	)
	vault := newTestVault(t)
	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md",
		pbNote(pbSessionID, pbDate, "", []string{pbDecision}))
	pbWriteNote(t, vault, otherProject, "2026-03-16-01.md",
		"---\nsession_id: 2026-03-16-01\nproject: "+otherProject+
			"\ndate: 2026-03-16\niteration: 1\ndecisions:\n  - \""+otherDecision+"\"\n---\n\nbody\n")

	res := callBackfill(t, vault, map[string]any{"all": true, "apply": true})
	wantBackfillCounts(t, res.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 2, DecisionsFound: 2, Appended: 2,
	})
	if len(res.Projects) != 2 {
		t.Fatalf("projects = %+v, want both fixture projects", res.Projects)
	}
	// ListAllProjects sorts by slug.
	if res.Projects[0].Project != otherProject || res.Projects[1].Project != pbProject {
		t.Errorf("project rows = %q, %q, want %q then %q",
			res.Projects[0].Project, res.Projects[1].Project, otherProject, pbProject)
	}

	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{"project": pbProject}), pbDecision)
	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{"project": otherProject}), otherDecision)
}

// TestPalaceBackfillEmptyVaultIsNotAnError: a project with no sessions
// directory at all walks nothing and succeeds.
func TestPalaceBackfillEmptyVaultIsNotAnError(t *testing.T) {
	vault := newTestVault(t)
	res := callBackfill(t, vault, map[string]any{"project": "never-captured", "apply": true})
	wantBackfillCounts(t, res.palaceBackfillCounts, palaceBackfillCounts{})
	if len(res.Projects) != 1 || res.Projects[0].Project != "never-captured" {
		t.Errorf("projects = %+v, want one row for the named project", res.Projects)
	}
}

func TestPalaceBackfillHandlerRejectsMalformedParams(t *testing.T) {
	tool := PalaceBackfillDecisionsTool(newTestVault(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"apply":`)); err == nil {
		t.Fatal("malformed params accepted")
	}
}

// --- Wire shape ---

// TestPalaceBackfillResultEndsWithComplete pins the terminal sentinel on the
// SERIALIZED BYTES rather than on the declaration order, because the totals
// reach the document through an embedded struct and field order under
// embedding is a property of encoding/json, not something the declaration
// makes obvious. Its absence is the truncation signal; anything serialized
// after it re-opens the hole.
func TestPalaceBackfillResultEndsWithComplete(t *testing.T) {
	vault := newTestVault(t)
	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md",
		pbNote(pbSessionID, pbDate, "", []string{pbDecision}))

	raw, err := json.Marshal(callBackfill(t, vault, map[string]any{"project": pbProject}))
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.HasSuffix(string(raw), `,"complete":true}`) {
		t.Errorf("result does not END with the complete sentinel: %s", raw)
	}
	// The five counts are flattened alongside it, not nested under a key.
	for _, key := range []string{
		`"notes_scanned":1`, `"decisions_found":1`, `"appended":0`,
		`"skipped_dup":0`, `"skipped_unparseable":0`, `"apply":false`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("result is missing %s: %s", key, raw)
		}
	}
	// An empty project list must serialize as [] and never as null.
	empty, err := json.Marshal(callBackfill(t, newTestVault(t), map[string]any{}))
	if err != nil {
		t.Fatalf("marshal empty result: %v", err)
	}
	if !strings.Contains(string(empty), `"projects":[]`) {
		t.Errorf("empty project list did not serialize as []: %s", empty)
	}
}

// --- Wiring invariants ---

// TestPalaceBackfillToolIsMutating pins the classification the two declarations
// encode. The tool writes, so it is surface-gated and it is NOT servable on a
// read-only surface — and it carries no ReadOnlyWhen, so the DRY RUN is gated
// too: the dry run's product is the count an operator authorizes the write
// from, and a binary that does not understand the vault's format must not be
// allowed to produce it.
func TestPalaceBackfillToolIsMutating(t *testing.T) {
	tool := PalaceBackfillDecisionsTool(newTestVault(t))
	if tool.Name != "vp_palace_backfill_decisions" {
		t.Errorf("name = %q", tool.Name)
	}
	if !tool.Mutating {
		t.Error("the tool appends drawers but is not registered Mutating")
	}
	if tool.ReadOnlyWhen != nil {
		t.Error("the tool has a ReadOnlyWhen refinement; the dry run is gated on purpose")
	}

	var gated bool
	for _, name := range MutatingToolNames {
		if name == tool.Name {
			gated = true
		}
	}
	if !gated {
		t.Error("vp_palace_backfill_decisions is missing from MutatingToolNames")
	}
	for _, name := range ReadOnlyServeToolNames {
		if name == tool.Name {
			t.Error("vp_palace_backfill_decisions must not be in ReadOnlyServeToolNames")
		}
	}
}

// TestPalaceBackfillRegistersWithoutAnEngine: the constructor takes a
// *storage.Vault and nothing else, so the tool must be present on the
// nil-engine registration path — never behind the `if engine != nil` branch.
func TestPalaceBackfillRegistersWithoutAnEngine(t *testing.T) {
	vault := newTestVault(t)
	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md",
		pbNote(pbSessionID, pbDate, "", []string{pbDecision}))

	srv := mcp.NewServer(vault)
	RegisterAll(srv.Registry(), vpctx.NewResolver(vault.Root), vault, nil)

	var found bool
	for _, tool := range srv.Registry().List() {
		if tool.Name == palaceBackfillDecisionsName {
			found = true
			if !tool.Mutating {
				t.Error("the registered tool is not flagged Mutating")
			}
		}
	}
	if !found {
		t.Fatal("vp_palace_backfill_decisions is not registered when engine is nil")
	}

	// The registry cannot be DISPATCHED through here — the mutating gate wants
	// a vault in a context only the real transport injects — so the round trip
	// runs through the constructor the registration just used, and the tool
	// info the registry actually holds carries the Mutating flag.
	res := callBackfill(t, vault, map[string]any{"project": pbProject, "apply": true})
	wantBackfillCounts(t, res.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 1, DecisionsFound: 1, Appended: 1,
	})
	wantPQContents(t, callPalaceQuery(t, vault, map[string]any{"project": pbProject}), pbDecision)
}

// --- Per-project rows carry their own tally ---

// TestPalaceBackfillRowsCarryTheirOwnCounts pins the EMBEDDED counts on each
// project row, not just the run total.
//
// The two projects are given different decision counts on purpose. A row list
// that is present but zeroed — the shape a refactor produces when it builds the
// rows from the project names and forgets to copy the tally — passes every
// total-only assertion in this file, because the totals are accumulated
// separately. So is a row list that reports the run total on every row. Both
// die here, and only here.
func TestPalaceBackfillRowsCarryTheirOwnCounts(t *testing.T) {
	vault := newTestVault(t)

	// Slugs chosen so ListAllProjects' sort puts "pb-a" before "pb-b"; the row
	// order is asserted, because a report whose rows are not in a stable order
	// cannot be diffed across two runs.
	pbWriteNote(t, vault, "pb-a", "2026-03-15-01.md",
		pbNote("2026-03-15-01", "2026-03-15", "", []string{"alpha one"}))
	pbWriteNote(t, vault, "pb-b", "2026-03-16-01.md",
		pbNote("2026-03-16-01", "2026-03-16", "", []string{"beta one", "beta two", "beta three"}))

	res := callBackfill(t, vault, map[string]any{"all": true, "apply": true})

	if len(res.Projects) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(res.Projects), res.Projects)
	}
	if res.Projects[0].Project != "pb-a" || res.Projects[1].Project != "pb-b" {
		t.Fatalf("row order = %q, %q, want pb-a, pb-b",
			res.Projects[0].Project, res.Projects[1].Project)
	}

	wantBackfillCounts(t, res.Projects[0].palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 1, DecisionsFound: 1, Appended: 1,
	})
	wantBackfillCounts(t, res.Projects[1].palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 1, DecisionsFound: 3, Appended: 3,
	})

	// The total is the sum of the rows, which is the property that makes
	// reporting both of them non-redundant.
	wantBackfillCounts(t, res.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 2, DecisionsFound: 4, Appended: 4,
	})
}

// --- A mixed batch reports both halves ---

// TestPalaceBackfillMixedBatchCountsNewAndDuplicate covers the run that is
// neither all-new nor all-duplicate: one apply in which some drawers land and
// others are skipped because they are already on disk.
//
// It is the case that separates `appended` from `decisions_found`. A run that
// reported appended = len(built) would claim 4 here; one that reported 0
// whenever anything was skipped would claim 0. Both are wrong in the direction
// an operator cannot detect, because either number is plausible on its own —
// only the pair, summing to what was built, says what actually happened.
func TestPalaceBackfillMixedBatchCountsNewAndDuplicate(t *testing.T) {
	vault := newTestVault(t)

	pbWriteNote(t, vault, pbProject, "2026-03-15-01.md",
		pbNote(pbSessionID, pbDate, "", []string{"decision one", "decision two"}))

	first := callBackfill(t, vault, map[string]any{"project": pbProject, "apply": true})
	wantBackfillCounts(t, first.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 1, DecisionsFound: 2, Appended: 2,
	})

	// A second note repeats one decision verbatim and adds one that is new.
	// DrawerID is md5(wing+content) (internal/storage/drawers.go:44-47), so the
	// repeat collides with what the first run wrote no matter which session
	// recorded it — the source_ref differs, the id does not.
	pbWriteNote(t, vault, pbProject, "2026-03-16-01.md",
		pbNote("2026-03-16-01", "2026-03-16", "", []string{"decision two", "decision three"}))

	second := callBackfill(t, vault, map[string]any{"project": pbProject, "apply": true})

	if second.Appended == 0 {
		t.Errorf("appended = 0 for a run that wrote a new decision: %s",
			pbCounts(second.palaceBackfillCounts))
	}
	if second.SkippedDup == 0 {
		t.Errorf("skipped_dup = 0 for a run that re-walked two filed decisions: %s",
			pbCounts(second.palaceBackfillCounts))
	}
	if got := second.Appended + second.SkippedDup; got != second.DecisionsFound {
		t.Errorf("appended+skipped_dup = %d, want decisions_found = %d: %s",
			got, second.DecisionsFound, pbCounts(second.palaceBackfillCounts))
	}

	// Exact shape: both notes are walked, so four drawers are built; three
	// distinct contents exist and two of them were already filed.
	wantBackfillCounts(t, second.palaceBackfillCounts, palaceBackfillCounts{
		NotesScanned: 2, DecisionsFound: 4, Appended: 1, SkippedDup: 3,
	})
}

// --- A failing project does not erase the ones already written ---

// TestPalaceBackfillFailedProjectKeepsEarlierRows is the report-integrity
// property of an apply run over several projects.
//
// The failure is induced where a real one lives: the room's parent directory is
// occupied by a FILE, so AppendDrawers cannot create it. That happens on the
// SECOND project, after the first has already committed drawers — which is the
// only arrangement that can tell "kept walking and reported" apart from "gave
// up and returned an error". Written against a handler that returns (nil, err)
// on the first failing project, callBackfill t.Fatals on the error and this
// test cannot pass.
func TestPalaceBackfillFailedProjectKeepsEarlierRows(t *testing.T) {
	vault := newTestVault(t)

	pbWriteNote(t, vault, "pb-a", "2026-03-15-01.md",
		pbNote("2026-03-15-01", "2026-03-15", "", []string{"the one that lands"}))
	pbWriteNote(t, vault, "pb-b", "2026-03-16-01.md",
		pbNote("2026-03-16-01", "2026-03-16", "", []string{"the one that cannot"}))

	// pb-b's wing is its own slug, and DecisionRoom is the room the writer
	// would create. Put a regular file exactly where that directory belongs.
	roomDir, err := vault.DrawerDir("pb-b", "pb-b", capture.DecisionRoom)
	if err != nil {
		t.Fatalf("DrawerDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(roomDir), 0o755); err != nil {
		t.Fatalf("mkdir wing: %v", err)
	}
	if err := os.WriteFile(roomDir, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("occupy room path: %v", err)
	}

	res := callBackfill(t, vault, map[string]any{"all": true, "apply": true})

	if !res.Complete {
		t.Error("complete = false; the sentinel reports truncation, not per-project failure")
	}
	if len(res.Projects) != 2 {
		t.Fatalf("rows = %d, want 2 — the failing project must not erase the one before it: %+v",
			len(res.Projects), res.Projects)
	}

	good, bad := res.Projects[0], res.Projects[1]
	if good.Project != "pb-a" || bad.Project != "pb-b" {
		t.Fatalf("rows = %q, %q, want pb-a, pb-b", good.Project, bad.Project)
	}
	if good.Error != "" {
		t.Errorf("pb-a error = %q, want none", good.Error)
	}
	if good.Appended != 1 {
		t.Errorf("pb-a appended = %d, want 1 — its write happened before pb-b failed", good.Appended)
	}
	if bad.Error == "" {
		t.Error("pb-b error is empty; a project whose append failed must say so on its row")
	}

	// The committed work is still reachable through the query, which is the
	// operator's actual question after a partial run: what landed?
	hits := callPalaceQuery(t, vault, map[string]any{"project": "pb-a"})
	if len(hits.Drawers) != 1 {
		t.Fatalf("query returned %d drawers for pb-a, want 1", len(hits.Drawers))
	}
}
