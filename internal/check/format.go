// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"io"
	"strings"
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

// PrintRows writes the status rows for the given results (without the
// surrounding diagnostic header or failure summary). It is the shared
// primitive used by both `vp check` and `vp init`'s end-of-run table.
// Returns the number of Fail rows emitted.
func PrintRows(w io.Writer, results []Result) int {
	failures := 0
	for _, r := range results {
		tag := statusTag(r.Status)
		if r.Summary != "" {
			fmt.Fprintf(w, "%s %-10s %s\n", tag, r.Name+":", r.Summary)
		} else {
			fmt.Fprintf(w, "%s %s\n", tag, r.Name)
		}
		for _, d := range r.Details {
			fmt.Fprintf(w, "%s %s\n", strings.Repeat(" ", len(tag)), strings.Repeat(" ", 11)+d)
		}
		if r.Status == Fail {
			failures++
		}
	}
	return failures
}

// ProgressLine writes a progress indicator for a long-running check.
func ProgressLine(w io.Writer, name, message string) {
	fmt.Fprintf(w, "[ .. ] %-10s %s\n", name+":", message)
}
