// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// htmlEscapes are the three sequences json.MarshalIndent emits and an Encoder
// with SetEscapeHTML(false) does not.
var htmlEscapes = []string{`\u003c`, `\u003e`, `\u0026`}

// TestSaveDoesNotHTMLEscapeReasons is this package's half of a pair, and the
// pairing is the point.
//
// sourceaudit.Baseline.Save carried this defect and vaultaudit.Baseline.Save
// carried it identically, latently — the vault baseline is written escaped today,
// so nobody had yet hit the churn the sourceaudit file produces. Fixing one writer
// and leaving the other is the duplication shape this project keeps unwinding, so
// the two writers move together and each has a test that reddens on its own.
//
// Reason and Except are operator prose: they carry `<`, `>` and `&`. Nothing
// DECODES differently either way, so only a test reading the BYTES can see it.
func TestSaveDoesNotHTMLEscapeReasons(t *testing.T) {
	b := Baseline{Dimensions: map[string]DimensionBaseline{
		"note_path": {
			Reason:   "predates the field; linker writes it for notes > 2026-07-12 & later",
			Accepted: []string{"Projects/p/sessions/a.md"},
			Except:   map[string]string{"Projects/p/sessions/b.md": "hand-written <stub>, not linker output"},
		},
	}}

	p := filepath.Join(t.TempDir(), "baseline.json")
	// vaultRoot "" is the documented non-vault destination for a bare temp file —
	// this test must never reach a real vault.
	if err := b.Save("", p); err != nil {
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
	for _, raw := range []string{"> 2026-07-12 & later", "<stub>"} {
		if !strings.Contains(got, raw) {
			t.Errorf("Save did not emit %q raw:\n%s", raw, got)
		}
	}
}

// TestSaveOfEscapedInputEmitsRaw is the direction that matters for the LIVE
// vault: Audits/baseline.json is written in the escaped spelling today, so the
// first Save after this change must normalize it rather than preserve it. A
// writer that merely echoed the spelling it read would pass the test above and
// leave the vault file escaped forever.
func TestSaveOfEscapedInputEmitsRaw(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "escaped.json")
	escaped := `{"dimensions":{"note_path":{"reason":"linker gap \u003c2026-07-12\u003e \u0026 older","accepted":["a.md"]}}}` + "\n"
	if err := os.WriteFile(in, []byte(escaped), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := LoadBaseline(in)
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.json")
	if err := b.Save("", out); err != nil {
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
	if !strings.Contains(got, "linker gap <2026-07-12> & older") {
		t.Fatalf("Save did not emit the decoded reason raw:\n%s", got)
	}

	// Only the SPELLING may move — the jq -S equivalence the task requires,
	// asserted in Go over the decoded values.
	var before, after Baseline
	if err := json.Unmarshal([]byte(escaped), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if before.Dimensions["note_path"].Reason != after.Dimensions["note_path"].Reason {
		t.Fatalf("the DECODED reason changed — only <, > and & spelling may move.\nbefore: %q\nafter:  %q",
			before.Dimensions["note_path"].Reason, after.Dimensions["note_path"].Reason)
	}
}

// TestSaveRoundTripsItsOwnOutput pins IDEMPOTENCE, and it is worth saying plainly
// that it does NOT pin the escaping — measured, not assumed: revert Save to
// json.MarshalIndent and the two tests above go red while this one stays GREEN.
// MarshalIndent is self-consistent, so a writer that escapes on every pass
// round-trips its own output perfectly. A test credited with coverage it lacks is
// the defect this repo has already paid for once.
//
// What it does pin is real and otherwise untested here: sort/compact stability
// across a reload, and exactly one trailing newline. The Encoder change moved the
// newline from an explicit append to Encode's own, and appending on top of that
// would have added a blank line no assertion above would have caught.
//
// It is the vault-side stand-in for sourceaudit's committed-file test only in
// SHAPE: there is no committed vaultaudit baseline in this repo to diff against.
// The live one is vault-resident and a test must never reach it, which is why
// vaultRoot is "" throughout this file.
func TestSaveRoundTripsItsOwnOutput(t *testing.T) {
	dir := t.TempDir()
	b := Baseline{Dimensions: map[string]DimensionBaseline{
		"note_path": {
			Reason:   "predates the field; see doc/x.md#a>b & the linker",
			Accepted: []string{"b.md", "a.md"},
			Except:   map[string]string{"c.md": "hand-written <stub>"},
		},
		"archive": {
			Reason:   "linker has never populated this & it is a live bug",
			Accepted: []string{"d.md"},
		},
	}}

	first := filepath.Join(dir, "first.json")
	if err := b.Save("", first); err != nil {
		t.Fatal(err)
	}
	once, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadBaseline(first)
	if err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "second.json")
	if err := reloaded.Save("", second); err != nil {
		t.Fatal(err)
	}
	twice, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}

	if string(once) != string(twice) {
		t.Errorf("Save does not round-trip its own output (%d bytes then %d) — a regenerated "+
			"baseline will churn against the committed one.\nfirst:\n%s\nsecond:\n%s",
			len(once), len(twice), once, twice)
	}
}
