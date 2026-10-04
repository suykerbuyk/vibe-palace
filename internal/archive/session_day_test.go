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
