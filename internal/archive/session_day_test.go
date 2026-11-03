// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withLocalZone runs f with time.Local set to the named zone: SessionDay must
// not depend on the process-local zone (storage.CalendarDay does).
func withLocalZone(t *testing.T, name string, f func()) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no tzdata for %s: %v", name, err)
	}
	old := time.Local
	time.Local = loc
	defer func() { time.Local = old }()
	f()
}

// claudeShape is a real-shaped Claude transcript: untimestamped metadata
// records first, then the first timestamped record.
const claudeShape = `{"type":"custom-title","title":"x"}
{"type":"mode","mode":"default"}
{"type":"system","timestamp":"2026-05-11T23:30:00-07:00","message":{"role":"system"}}
{"type":"user","timestamp":"2026-05-20T10:00:00Z","message":{"role":"user"}}
`

// TestSessionDayReadsTheTranscriptStart (R5): the first TIMESTAMPED record
// dates the session, as its UTC day, ahead of captured_at, and in every
// process-local zone.
func TestSessionDayReadsTheTranscriptStart(t *testing.T) {
	m := &Manifest{Adapter: ClaudeCodeAdapterName, SessionID: "s", CapturedAt: "2026-05-20T09:00:00Z",
		VaultRelSessionNote: "Projects/p/sessions/2026-06-01-note.md"}
	for _, zone := range []string{"America/Los_Angeles", "Asia/Tokyo", "UTC"} {
		withLocalZone(t, zone, func() {
			got, err := m.SessionDay([]byte(claudeShape))
			if err != nil {
				t.Fatal(err)
			}
			if got != (SessionDay{Day: "2026-05-12", Source: DayFromTranscript}) {
				t.Fatalf("%s: SessionDay = %+v, want 2026-05-12 from the transcript (the UTC day of 23:30 -07:00)", zone, got)
			}
		})
	}
	m.VaultRelSessionNote = ""
	if got, _ := m.SessionDay([]byte(claudeShape)); got.Day != "2026-05-12" {
		t.Fatalf("without the note link: %+v", got)
	}
}

// TestSessionDayFallsBackToCapturedAt (R5): no timestamped record, a markdown
// source with a JSON timestamp line inside its body, and a Zed archive all
// take the UTC day of captured_at; with no captured_at either, an error.
func TestSessionDayFallsBackToCapturedAt(t *testing.T) {
	markdown := "# A session note\n\nSome prose.\n\n{\"timestamp\":\"2020-01-01T00:00:00Z\"}\n"
	cases := []struct {
		name    string
		adapter string
		source  string
	}{
		{"no timestamped record", ClaudeCodeAdapterName, `{"type":"user","message":{"role":"user"}}` + "\n"},
		{"markdown with a JSON snippet", InlineAdapterName, markdown},
		{"zed", ZedAdapterName, `{"type":"user","timestamp":"2020-01-01T00:00:00Z"}` + "\n"},
	}
	for _, tc := range cases {
		for _, zone := range []string{"America/Los_Angeles", "Asia/Tokyo"} {
			withLocalZone(t, zone, func() {
				m := &Manifest{Adapter: tc.adapter, SessionID: "s", CapturedAt: "2026-05-12T23:30:00-07:00"}
				got, err := m.SessionDay([]byte(tc.source))
				if err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
				if got != (SessionDay{Day: "2026-05-13", Source: DayFromCapturedAt}) {
					t.Fatalf("%s (%s): %+v, want the UTC day of captured_at", tc.name, zone, got)
				}
			})
		}
	}
	m := &Manifest{Adapter: ClaudeCodeAdapterName, SessionID: "s"}
	if _, err := m.SessionDay([]byte("{}\n")); err == nil {
		t.Fatal("no timestamped record and no captured_at must be an error")
	}
}

// TestClaudeSessionStart: the first timestamped record, not the first line
// and not the last timestamp; no timestamp anywhere is ok=false.
func TestClaudeSessionStart(t *testing.T) {
	got, ok, err := claudeAdapter{}.SessionStart([]byte(claudeShape))
	if err != nil || !ok || !got.Equal(time.Date(2026, 5, 12, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("SessionStart = %v, %v, %v", got, ok, err)
	}
	if _, ok, err := (claudeAdapter{}).SessionStart([]byte(`{"type":"mode"}` + "\n")); ok || err != nil {
		t.Fatalf("no timestamp: ok=%v err=%v", ok, err)
	}
}

// TestSessionDayHasNoDecisionChunkCaller (5-S1): decision chunks take the day
// the ledger recorded and never read an archive, so no production code in the
// capture or tools layers (where the decision-chunk writers live) calls
// SessionDay. Its callers are the pending-archive ingester and the rebuild
// driver, which have not landed.
func TestSessionDayHasNoDecisionChunkCaller(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var callers []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "SessionDay" {
						callers = append(callers, p)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range callers {
		if strings.Contains(c, "internal/capture") || strings.Contains(c, "internal/tools") || strings.Contains(c, "internal/palace") {
			t.Errorf("%s calls SessionDay: a decision-chunk writer must take the ledger's day, never read an archive", c)
		}
	}
}

// codexOneRecord is a single Codex session_meta record whose two timestamps land
// on DIFFERENT UTC DAYS: the record's own top-level stamp is 2026-05-13, the
// payload stamp — the instant the session actually began — is 2026-05-12.
//
// 🔴 The day split is load-bearing, not decoration. It mirrors the live
// rollout-2026-10-29T14-37-46 case (payload 2026-10-29T20:37:46.909Z, top-level
// 2026-10-30T13:24:56.686Z, a 16.8h gap). A same-day fixture would let the
// correct and the buggy readings agree, and the test would pass under the very
// bug it exists to catch.
const codexOneRecord = `{"timestamp":"2026-05-13T13:24:56Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a19e1e-fixture","timestamp":"2026-05-12T23:30:00.675Z","cwd":"/tmp/x"}}` + "\n"

// A1: the start of a Codex session is session_meta's PAYLOAD timestamp, never
// the record's own top-level stamp.
func TestCodexRawSessionStartReadsSessionMetaPayloadTimestamp(t *testing.T) {
	got, ok, err := (codexRawAdapter{}).SessionStart([]byte(codexOneRecord))
	if err != nil || !ok {
		t.Fatalf("SessionStart = %v, %v, %v", got, ok, err)
	}
	want := time.Date(2026, 5, 12, 23, 30, 0, 675000000, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("SessionStart = %s, want %s (the PAYLOAD stamp; the record's own is 2026-05-13T13:24:56Z, a different UTC day)",
			got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

// A2: the production consequence. captured_at is set two days after the
// transcript stamp so it cannot coincide — deleting codexRawAdapter.SessionStart
// moves BOTH the day (2026-05-12 -> 2026-05-14) and the source (transcript ->
// captured_at).
func TestSessionDayDatesACodexArchiveFromTheTranscript(t *testing.T) {
	m := &Manifest{Adapter: CodexRawAdapterName, SessionID: "s", CapturedAt: "2026-05-14T00:30:00Z"}
	for _, zone := range []string{"America/Los_Angeles", "Asia/Tokyo", "UTC"} {
		withLocalZone(t, zone, func() {
			got, err := m.SessionDay([]byte(codexOneRecord))
			if err != nil {
				t.Fatal(err)
			}
			if got != (SessionDay{Day: "2026-05-12", Source: DayFromTranscript}) {
				t.Fatalf("%s: SessionDay = %+v, want 2026-05-12 from the transcript (captured_at would give 2026-05-14)", zone, got)
			}
		})
	}
}

// A3: every way of failing to read a start is (zero, false, nil) — never an
// error. SessionDay propagates a StartReader error and IngestArchive turns that
// into a FAILED ARCHIVE with no captured_at fallback, so a malformed rollout
// must degrade to today's behaviour rather than become un-ingestable.
func TestCodexRawSessionStartFallsBackRatherThanErroring(t *testing.T) {
	thirdIsMeta := `{"timestamp":"2026-05-13T00:00:00Z","ordinal":0,"type":"event_msg","payload":{"type":"task_started"}}
{"timestamp":"2026-05-13T00:00:01Z","ordinal":1,"type":"turn_context","payload":{"model":"gpt-6-luna"}}
{"timestamp":"2026-05-13T13:24:56Z","ordinal":2,"type":"session_meta","payload":{"session_id":"s","timestamp":"2026-05-12T23:30:00.675Z"}}
`
	cases := []struct {
		name   string
		source string
		wantOK bool
	}{
		{"empty source", "", false},
		{"markdown prose", "# A session note\n\nSome prose.\n", false},
		{"session_meta with no payload timestamp", `{"type":"session_meta","payload":{"session_id":"s"}}` + "\n", false},
		{"session_meta with an unparseable timestamp", `{"type":"session_meta","payload":{"timestamp":"not-a-time"}}` + "\n", false},
		{"session_meta is the third record", thirdIsMeta, true},
	}
	for _, tc := range cases {
		got, ok, err := (codexRawAdapter{}).SessionStart([]byte(tc.source))
		if err != nil {
			t.Errorf("%s: err = %v, want nil — an error fails the whole archive at ingest", tc.name, err)
			continue
		}
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if tc.wantOK {
			want := time.Date(2026, 5, 12, 23, 30, 0, 675000000, time.UTC)
			if !got.Equal(want) {
				t.Errorf("%s: got %s, want %s", tc.name, got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
			}
		}
	}
}

// First session_meta wins. One rollout in the live corpus carries two: line 1 is
// the file's own session, line 2 is the PARENT session with a different id and an
// EARLIER stamp. A last-wins or earliest-wins rule would date the file by its
// parent's session rather than its own.
func TestCodexRawSessionStartTakesTheFirstSessionMeta(t *testing.T) {
	twoMeta := `{"timestamp":"2026-10-31T16:42:00Z","ordinal":0,"type":"session_meta","payload":{"session_id":"own","timestamp":"2026-10-31T16:37:36.420Z"}}
{"timestamp":"2026-10-31T16:42:01Z","ordinal":1,"type":"session_meta","payload":{"session_id":"parent","timestamp":"2026-10-31T15:45:04.545Z"}}
`
	got, ok, err := (codexRawAdapter{}).SessionStart([]byte(twoMeta))
	if err != nil || !ok {
		t.Fatalf("SessionStart = %v, %v, %v", got, ok, err)
	}
	want := time.Date(2026, 10, 31, 16, 37, 36, 420000000, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("SessionStart = %s, want the FIRST session_meta %s; the second is the parent session",
			got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}
