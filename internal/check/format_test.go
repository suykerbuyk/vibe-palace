// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestPrintRows_Layout pins the whole rendered block for one row that
// exercises every rule at once: the name alone on its tag line, a wrapped
// Summary, a Details entry that fits and keeps its aligned spacing, a "- " item
// whose continuation hangs under its text, and an oversized path left whole.
func TestPrintRows_Layout(t *testing.T) {
	path := "/" + strings.Repeat("very-long-directory-name/", 4) + "config.toml"
	results := []Result{
		{Name: "Vault", Status: Info, Summary: "already configured"},
		{Name: "Settings", Status: Skip},
		{
			Name:    "Upgrade policy",
			Status:  Info,
			Summary: "`vp init` is additive: it never removes a stale shim, and it never writes, prunes or reconciles vault Templates/.",
			Details: []string{
				"  host-a: 12 session(s)  <- this host",
				"- Stale shims (.claude/commands/vpc-*.md): `vp commands upgrade` removes them. It never touches vault Templates/.",
				"see " + path + " for the file",
			},
		},
	}

	var buf bytes.Buffer
	PrintRows(&buf, results)

	want := strings.Join([]string{
		"[info] Vault:",
		"       already configured",
		"[skip] Settings",
		"[info] Upgrade policy:",
		"       `vp init` is additive: it never removes a stale shim, and it never",
		"         writes, prunes or reconciles vault Templates/.",
		"         host-a: 12 session(s)  <- this host",
		"       - Stale shims (.claude/commands/vpc-*.md): `vp commands upgrade` removes",
		"         them. It never touches vault Templates/.",
		"       see",
		"         " + path,
		"         for the file",
	}, "\n") + "\n"
	if got := buf.String(); got != want {
		t.Errorf("PrintRows layout:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestPrintRows_WrapsAtRowWidthAndKeepsEveryWord checks the two properties
// every wrapped entry owes the reader, over a long entry: no line passes
// rowWidth unless it is one word too long to fit anywhere, and wrapping loses
// and reorders nothing.
func TestPrintRows_WrapsAtRowWidthAndKeepsEveryWord(t *testing.T) {
	long := strings.Repeat("alpha beta — gamma ", 30)
	oversized := strings.Repeat("x", rowWidth+10)
	results := []Result{{Name: "Long", Status: Fail, Summary: long, Details: []string{long + oversized}}}

	var buf bytes.Buffer
	if n := PrintRows(&buf, results); n != 1 {
		t.Errorf("PrintRows returned %d failures, want 1", n)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	for i, l := range lines {
		if utf8.RuneCountInString(l) > rowWidth && strings.TrimSpace(l) != oversized {
			t.Errorf("line %d is %d columns, over rowWidth %d: %q", i, utf8.RuneCountInString(l), rowWidth, l)
		}
	}

	var words []string
	for _, l := range lines[1:] {
		words = append(words, strings.Fields(l)...)
	}
	want := strings.Fields(long + long + oversized)
	if strings.Join(words, " ") != strings.Join(want, " ") {
		t.Errorf("wrapping lost or reordered words:\n got: %q\nwant: %q", strings.Join(words, " "), strings.Join(want, " "))
	}
}

// TestPrintRows_MultilineEntry covers an entry carrying its own newlines, as an
// error's text can: each line is its own output line under the indent, never
// run together with the next.
func TestPrintRows_MultilineEntry(t *testing.T) {
	var buf bytes.Buffer
	PrintRows(&buf, []Result{{Name: "Surface", Status: Info, Summary: "binary v4 < vault v5\n    action: upgrade"}})
	want := "[info] Surface:\n       binary v4 < vault v5\n           action: upgrade\n"
	if got := buf.String(); got != want {
		t.Errorf("multi-line entry:\n got: %q\nwant: %q", got, want)
	}
}
