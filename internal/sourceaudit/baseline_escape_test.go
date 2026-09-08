// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// htmlEscapes are the three sequences json.MarshalIndent emits and an Encoder
// with SetEscapeHTML(false) does not. Named once, used by both writers' tests.
var htmlEscapes = []string{`\u003c`, `\u003e`, `\u0026`}

// TestSaveDoesNotHTMLEscapeReasons reddens the moment someone puts
// json.MarshalIndent back.
//
// MarshalIndent has no SetEscapeHTML knob: it ALWAYS rewrites `<`, `>` and `&`.
// Reason strings are prose written by a human triaging accepted debt, and they
// contain all three. Nothing decodes differently — encoding/json reads both
// spellings back to the same runes — so no test that unmarshals the file can see
// this. Only a test that reads the BYTES can.
func TestSaveDoesNotHTMLEscapeReasons(t *testing.T) {
	b := Baseline{Entries: []BaselineEntry{
		{ID: "z:one", Reason: "guarded by a <-chan send & a >0 length check"},
		{ID: "a:two", Reason: "dispatched via io.Writer <- stdlib; see doc/x.md#a>b"},
	}}

	p := filepath.Join(t.TempDir(), "baseline.json")
	if err := b.Save(p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	for _, esc := range htmlEscapes {
		if strings.Contains(got, esc) {
			t.Errorf("Save emitted the HTML escape %s — the writer is HTML-escaping again "+
				"(json.MarshalIndent always does; use an Encoder with SetEscapeHTML(false)).\n%s",
				esc, got)
		}
	}
	for _, raw := range []string{"<-chan", "&", ">0", "#a>b"} {
		if !strings.Contains(got, raw) {
			t.Errorf("Save did not emit %q raw:\n%s", raw, got)
		}
	}
}

// TestSaveOfEscapedInputEmitsRaw is the direction the escape bug actually
// travels: a file already written in the escaped spelling is loaded, saved, and
// must come back out RAW. Without it, a writer that merely PRESERVES whatever
// spelling it read would pass the test above (which starts from raw Go strings)
// while leaving an escaped file escaped forever.
//
// It is also the shape of the live vault's Audits/baseline.json, which carries
// the escaped spelling today.
func TestSaveOfEscapedInputEmitsRaw(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "escaped.json")
	escaped := `{"entries":[{"id":"a:one","reason":"holds while \u003cT\u003e is stdlib \u0026 dispatched"}]}` + "\n"
	if err := os.WriteFile(in, []byte(escaped), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := LoadBaseline(in)
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.json")
	if err := b.Save(out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	for _, esc := range htmlEscapes {
		if strings.Contains(got, esc) {
			t.Fatalf("Save carried the escaped spelling %s through instead of normalizing it to raw:\n%s", esc, got)
		}
	}
	if !strings.Contains(got, "<T> is stdlib & dispatched") {
		t.Fatalf("Save did not emit the decoded reason raw:\n%s", got)
	}

	// Only the SPELLING may move. The decoded reason must be identical — this is
	// the jq -S equivalence the task requires, asserted in Go.
	var before, after Baseline
	if err := json.Unmarshal([]byte(escaped), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if len(before.Entries) != len(after.Entries) ||
		before.Entries[0].ID != after.Entries[0].ID ||
		before.Entries[0].Reason != after.Entries[0].Reason {
		t.Fatalf("the DECODED content changed — only <, > and & spelling may move.\nbefore: %+v\nafter:  %+v",
			before.Entries, after.Entries)
	}
}

// TestCommittedBaselineRoundTripsThroughSave is the invariant the whole task is
// about: load the committed baseline.json, Save it, get the SAME BYTES.
//
// Before the Encoder change this failed. The committed file carries raw `<` and
// `>` (re-derive: `grep -c '<' internal/sourceaudit/baseline.json`) and the
// writer re-spelled every one of them, so anyone running -update-baseline for a
// real reason got that churn mixed into an unrelated diff, had to notice it,
// diagnose it and revert it — and the next person might simply commit it, at
// which point the churn ping-pongs on whoever last ran what. A file that does
// not round-trip through its own writer is a weak invariant to hang a
// shrink-only ratchet on.
func TestCommittedBaselineRoundTripsThroughSave(t *testing.T) {
	committed, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatal(err)
	}

	b, err := LoadBaseline(baselinePath)
	if err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(t.TempDir(), "baseline.json")
	if err := b.Save(p); err != nil {
		t.Fatal(err)
	}
	rewritten, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	if string(rewritten) != string(committed) {
		t.Errorf("regenerating %s does not reproduce the committed bytes (%d committed, %d rewritten).\n"+
			"Run `diff <(jq -S . %s) <(jq -S . %s)` — if that is EMPTY the difference is pure "+
			"spelling, which is exactly the churn this test exists to stop.",
			baselinePath, len(committed), len(rewritten), baselinePath, p)
	}
}
