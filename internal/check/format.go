// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// statusTag returns the bracketed label for a check status.
func statusTag(s Status) string {
	switch s {
	case Pass:
		return "[pass]"
	case Fail:
		return "[FAIL]"
	case Skip:
		return "[skip]"
	case Info:
		return "[info]"
	default:
		return "[????]"
	}
}

// Print writes human-readable diagnostic output to w.
// Returns the number of failures.
func Print(w io.Writer, version string, results []Result) int {
	fmt.Fprintf(w, "vp check — vibe-palace installation diagnostic (%s)\n\n", version)

	failures := PrintRows(w, results)

	fmt.Fprintln(w)
	switch failures {
	case 0:
		fmt.Fprintln(w, "All checks passed.")
	case 1:
		fmt.Fprintln(w, "1 check failed.")
	default:
		fmt.Fprintf(w, "%d checks failed.\n", failures)
	}

	return failures
}

// rowWidth is the column PrintRows wraps a row's body at.
const rowWidth = 80

// hangWidth is how far a wrapped entry's continuation lines sit past its first.
const hangWidth = 2

// PrintRows writes the status rows for the given results (without the
// surrounding diagnostic header or failure summary). It is the shared
// primitive used by both `vp check` and `vp init`'s end-of-run table.
// Returns the number of Fail rows emitted.
//
// Each row is a `[tag] Name:` line, then its Summary and each of its Details
// beneath it as one ENTRY apiece, indented past the tag and wrapped at
// rowWidth. The name gets a line of its own because a Summary that followed it
// ran on into its Details, with nothing marking where one artifact, behaviour
// or command stopped and the next began. For the same reason a producer hands
// PrintRows whole sentences and paragraphs, never lines it wrapped itself: a
// pre-wrapped fragment renders as an entry of its own.
//
// Result.Err is NEVER rendered — only Name, Summary and Details are. A check
// that wants its error to reach the operator places the text in Details.
func PrintRows(w io.Writer, results []Result) int {
	failures := 0
	for _, r := range results {
		tag := statusTag(r.Status)
		if r.Summary != "" || len(r.Details) > 0 {
			fmt.Fprintf(w, "%s %s:\n", tag, r.Name)
		} else {
			fmt.Fprintf(w, "%s %s\n", tag, r.Name)
		}
		indent := strings.Repeat(" ", len(tag)+1)
		if r.Summary != "" {
			writeEntry(w, indent, r.Summary)
		}
		for _, d := range r.Details {
			writeEntry(w, indent, d)
		}
		if r.Status == Fail {
			failures++
		}
	}
	return failures
}

// writeEntry writes one Summary or Details entry under indent, one output line
// per line of text, wrapping any line that would pass rowWidth at word
// boundaries.
//
// A line that fits is written verbatim, so an entry that aligns columns with
// runs of spaces keeps them. A wrapped line keeps its own leading spaces on its
// first line and hangs every continuation hangWidth further in, so where each
// entry starts stays visible (and a "- " item's continuation lines up under
// its text). A word longer than the room left is written whole on a line of its
// own rather than broken, so a long path stays copy-pasteable even though it
// overruns the column.
func writeEntry(w io.Writer, indent, text string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			fmt.Fprintln(w)
			continue
		}
		if runeLen(indent)+runeLen(line) <= rowWidth {
			fmt.Fprintf(w, "%s%s\n", indent, line)
			continue
		}
		body := strings.TrimLeft(line, " ")
		lead := indent + line[:len(line)-len(body)]
		prefix, cur := lead, ""
		for _, word := range strings.Fields(body) {
			switch {
			case cur == "":
				cur = word
			case runeLen(prefix)+runeLen(cur)+1+runeLen(word) <= rowWidth:
				cur += " " + word
			default:
				fmt.Fprintf(w, "%s%s\n", prefix, cur)
				prefix, cur = lead+strings.Repeat(" ", hangWidth), word
			}
		}
		fmt.Fprintf(w, "%s%s\n", prefix, cur)
	}
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// ProgressLine writes a progress indicator for a long-running check.
func ProgressLine(w io.Writer, name, message string) {
	fmt.Fprintf(w, "[ .. ] %-10s %s\n", name+":", message)
}
