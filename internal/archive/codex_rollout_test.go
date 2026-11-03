// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexRolloutFixture is the shared Codex rollout body for the Inspect tests and
// for TestCreateCodexRawArchiveStoresExactHashAndTheCountedTurns.
//
// Twelve records. Nine are response_item (rows 3-11); five of those are NOT
// payload.type "message" (rows 5-9); three satisfy the full counting rule —
// type "message" AND role user/assistant (rows 4, 10, 11). So turnCount is 3.
//
// 🔴 Row 9 (reasoning carrying role "assistant") is DELIBERATELY SYNTHETIC. Codex
// emits no such record: a census of all 32 rollouts on the development host found
// ZERO non-message response_item records with an accepted role (reasoning 2507,
// custom_tool_call 1680, custom_tool_call_output 1680, function_call 76,
// agent_message 5 — every one with no role). That makes the payload.type ==
// "message" predicate redundant against today's Codex, and row 9 the only way it
// can be falsified at all. Without it, TestCodexRawInspectCountsConversationalTurns'
// drop-the-type-test mutation is INERT — it passes under the very bug it exists to
// catch. Do not "clean up" this row.
//
// Rows 2 and 12 carry DIFFERENT models so last-wins is falsifiable: a first-wins
// implementation yields gpt-6-luna, not gpt-6-nova.
const codexRolloutFixture = `{"timestamp":"2026-05-13T13:24:56.000Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a19e1e-fixture","timestamp":"2026-05-12T23:30:00.675Z","cwd":"/tmp/x"}}
{"timestamp":"2026-05-12T23:30:01.000Z","ordinal":1,"type":"turn_context","payload":{"turn_id":"t1","model":"gpt-6-luna"}}
{"timestamp":"2026-05-12T23:30:02.000Z","ordinal":2,"type":"response_item","payload":{"type":"message","role":"developer","content":[]}}
{"timestamp":"2026-05-12T23:30:03.000Z","ordinal":3,"type":"response_item","payload":{"type":"message","role":"user","content":[]}}
{"timestamp":"2026-05-12T23:30:04.000Z","ordinal":4,"type":"response_item","payload":{"type":"reasoning","summary":[]}}
{"timestamp":"2026-05-12T23:30:05.000Z","ordinal":5,"type":"response_item","payload":{"type":"custom_tool_call","name":"shell"}}
{"timestamp":"2026-05-12T23:30:06.000Z","ordinal":6,"type":"response_item","payload":{"type":"custom_tool_call_output","output":"ok"}}
{"timestamp":"2026-05-12T23:30:07.000Z","ordinal":7,"type":"response_item","payload":{"type":"agent_message","author":"/root/sub","recipient":"/root"}}
{"timestamp":"2026-05-12T23:30:08.000Z","ordinal":8,"type":"response_item","payload":{"type":"reasoning","role":"assistant","summary":[]}}
{"timestamp":"2026-05-12T23:30:09.000Z","ordinal":9,"type":"response_item","payload":{"type":"message","role":"assistant","content":[]}}
{"timestamp":"2026-05-12T23:30:10.000Z","ordinal":10,"type":"response_item","payload":{"type":"message","role":"assistant","content":[]}}
{"timestamp":"2026-05-12T23:30:11.000Z","ordinal":11,"type":"turn_context","payload":{"turn_id":"t2","model":"gpt-6-nova"}}
`

// writeRollout drops body at a temp path and returns it.
func writeRollout(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A4 / A4b / A4c. One fixture, three independent mutations, each with its own
// arithmetic — recorded here so a future reader can check that each still moves
// the number rather than assuming it does.
//
//	A4  replace the whole condition with r.Type == "response_item"  3 -> 9
//	A4b add "developer" to the accepted role set                    3 -> 4
//	A4c drop ONLY r.Payload.Type == "message"                       3 -> 4 (row 9)
func TestCodexRawInspectCountsConversationalTurns(t *testing.T) {
	turns, model, err := (codexRawAdapter{}).Inspect(writeRollout(t, codexRolloutFixture))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if turns != 3 {
		t.Errorf("turnCount = %d, want 3 (rows 4, 10, 11: payload.type message with role user/assistant)", turns)
	}
	if model != "gpt-6-nova" {
		t.Errorf("model = %q, want gpt-6-nova (the LAST non-empty payload.model)", model)
	}
}

// A5. The last non-empty model wins, not the first. The fixture's two
// turn_context records carry different models precisely so a first-wins
// implementation cannot pass: it would yield gpt-6-luna.
func TestCodexRawInspectTakesTheLastModel(t *testing.T) {
	_, model, err := (codexRawAdapter{}).Inspect(writeRollout(t, codexRolloutFixture))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if model != "gpt-6-nova" {
		t.Fatalf("model = %q, want gpt-6-nova; a first-wins read yields gpt-6-luna", model)
	}
}

// A6. A rollout with no turns and no model is (0, "", nil) — NOT an error. Four
// of the 32 rollouts in the live corpus are exactly this shape, so it is the
// common case and not an edge.
func TestCodexRawInspectOnATurnlessRollout(t *testing.T) {
	only := `{"timestamp":"2026-05-13T13:24:56.000Z","ordinal":0,"type":"session_meta","payload":{"session_id":"s","timestamp":"2026-05-12T23:30:00.675Z"}}` + "\n"
	turns, model, err := (codexRawAdapter{}).Inspect(writeRollout(t, only))
	if err != nil {
		t.Fatalf("Inspect on a turnless rollout returned an error: %v", err)
	}
	if turns != 0 || model != "" {
		t.Fatalf("Inspect = (%d, %q), want (0, \"\")", turns, model)
	}
}

// A7. Counting is best-effort: a line that does not decode is skipped, not
// surfaced. The manifest's source hash already pins the exact bytes, and
// Create's only response to an Inspect error is a warning, so a strict reader
// would log on every slightly-odd transcript and lose the count with it.
func TestCodexRawInspectSkipsMalformedLinesAndStillCounts(t *testing.T) {
	lines := []byte(codexRolloutFixture)
	// Splice a non-JSON line between the two assistant messages (rows 10, 11).
	marker := `{"timestamp":"2026-05-12T23:30:10.000Z","ordinal":10,`
	idx := strings.Index(string(lines), marker)
	if idx < 0 {
		t.Fatal("fixture: row 11 marker not found")
	}
	spliced := string(lines[:idx]) + "{not json\n" + string(lines[idx:])

	turns, model, err := (codexRawAdapter{}).Inspect(writeRollout(t, spliced))
	if err != nil {
		t.Fatalf("Inspect returned an error on a malformed line: %v", err)
	}
	if turns != 3 || model != "gpt-6-nova" {
		t.Fatalf("Inspect = (%d, %q), want (3, gpt-6-nova): a malformed line must be skipped, not fatal", turns, model)
	}
}

// A6 companion: a missing source is a real error, with both results zeroed.
// This is what the retired TestCodexRawAdapterDoesNotClaimClaudeTurnCount was
// actually testing once Inspect became real — see that test's replacement note.
func TestCodexRawInspectReportsAMissingSource(t *testing.T) {
	turns, model, err := (codexRawAdapter{}).Inspect(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err == nil {
		t.Fatal("a missing source must be an error")
	}
	if turns != 0 || model != "" {
		t.Fatalf("Inspect = (%d, %q) beside an error, want both zeroed", turns, model)
	}
}
