// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"io"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// reportSkippedSessions prints the notes the session reader could not parse.
//
// 🔴 IT WRITES TO STDERR, ALWAYS, and takes the writer explicitly so a caller
// cannot accidentally send it to stdout. Several of these commands emit JSON on
// stdout, and a diagnostic line in that stream turns a readable failure into an
// unparseable one — which is how a report meant to help becomes the next bug.
//
// storage.ListSessions skips a malformed note instead of failing the whole
// listing, so one bad file no longer deletes a project's history. This is the
// other half: every figure these commands print is computed over FEWER sessions
// than happened, and a total that cannot say it is short is quietly wrong
// rather than merely partial.
func reportSkippedSessions(stderr io.Writer, cmd string, skipped []storage.RecordSkip) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(stderr, "%s: %d session note(s) unreadable and EXCLUDED from this result:\n", cmd, len(skipped))
	for _, s := range skipped {
		fmt.Fprintf(stderr, "  %s\n    %s\n", s.Path, s.Reason)
	}
}
