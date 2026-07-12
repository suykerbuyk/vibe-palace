// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mdfence

import (
	"strings"
	"testing"
)

// theRealLine is iterations.md:698, byte-exact. It is the counterexample that
// broke both naive fence scanners: an indented bullet continuation whose first
// non-space characters are an inline code run. Every guard in this package
// exists to classify this line correctly, so it is tested verbatim rather than
// paraphrased.
const theRealLine = "  ```bash tutorial``` extraction from `doc/TUTORIAL.md` — deferred"

func TestTheRealLineIsNotAnOpeningFence(t *testing.T) {
	ch, run, info, ok := Delim(theRealLine)
	if !ok {
		t.Fatalf("expected a delimiter-shaped line (it does start with a backtick run)")
	}
	if ch != '`' || run != 3 {
		t.Fatalf("got ch=%q run=%d, want '`' and 3", ch, run)
	}
	if !strings.Contains(info, "`") {
		t.Fatalf("info string %q should contain a backtick — that is what disqualifies it", info)
	}
	if OpensFence(ch, info) {
		t.Errorf("REGRESSION: %q was treated as an OPENING fence. Its info string carries a "+
			"backtick, which CommonMark forbids in an opening backtick fence. Treating it as "+
			"one inverts fence state and swallows the rest of the document.", theRealLine)
	}
}

func TestOutsideFences(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string // the raw lines expected OUTSIDE any fence
	}{
		{
			name:    "no fences",
			content: "alpha\nbeta\n",
			want:    []string{"alpha", "beta", ""},
		},
		{
			name:    "a real fence hides its content",
			content: "alpha\n```go\nhidden\n```\nbeta\n",
			want:    []string{"alpha", "beta", ""},
		},
		{
			name:    "tilde fence",
			content: "alpha\n~~~\nhidden\n~~~\nbeta\n",
			want:    []string{"alpha", "beta", ""},
		},
		{
			// THE BUG. An inline code run is prose, so the line itself is
			// returned AND the lines after it stay visible.
			name:    "inline code run is prose, not a fence",
			content: theRealLine + "\nafter\n",
			want:    []string{theRealLine, "after", ""},
		},
		{
			// And a genuine fence following it must still work.
			name:    "a real fence still works after an inline code run",
			content: theRealLine + "\n```go\nhidden\n```\nafter\n",
			want:    []string{theRealLine, "after", ""},
		},
		{
			// A closing delimiter carries no info string; "```go" inside a
			// fence is content, not a close.
			name:    "info string opens, only a bare run closes",
			content: "alpha\n```md\n```go\n```\nbeta\n",
			want:    []string{"alpha", "beta", ""},
		},
		{
			// Four spaces is an indented code block, not a delimiter, so it
			// must not toggle fence state.
			name:    "four-space indent is not a delimiter",
			content: "alpha\n    ```\nbeta\n",
			want:    []string{"alpha", "    ```", "beta", ""},
		},
		{
			// A longer closing run is allowed; a shorter one is not a close.
			name:    "closing run may be longer than the opener",
			content: "alpha\n```\nhidden\n`````\nbeta\n",
			want:    []string{"alpha", "beta", ""},
		},
		{
			// Unterminated: the remainder is fenced. Deliberate.
			name:    "unterminated fence swallows the remainder",
			content: "alpha\n```\nhidden\nalso hidden\n",
			want:    []string{"alpha"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, l := range OutsideFences(tc.content) {
				got = append(got, l.Text)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d lines %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("line %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestOutsideFencesReportsOriginalLineNumbers(t *testing.T) {
	// Line numbers must index the ORIGINAL content, not the filtered output —
	// callers use them in error messages that a human has to act on.
	content := "one\n```\nhidden\n```\nfive\n"
	got := OutsideFences(content)
	if len(got) < 2 {
		t.Fatalf("got %d lines, want at least 2", len(got))
	}
	if got[0].Num != 1 || got[0].Text != "one" {
		t.Errorf("first: got line %d %q, want 1 %q", got[0].Num, got[0].Text, "one")
	}
	if got[1].Num != 5 || got[1].Text != "five" {
		t.Errorf("second: got line %d %q, want 5 %q", got[1].Num, got[1].Text, "five")
	}
}
