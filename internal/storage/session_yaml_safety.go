// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"path/filepath"
	"strings"
)

// The write-side guard that keeps a session note READABLE BY ITS OWN READER.
//
// # The defect this exists for
//
// gopkg.in/yaml.v3 v3.0.1 does not round-trip every string it accepts. For a
// multi-line string whose FIRST line is empty or begins with a space or a tab,
// yaml.Marshal emits a block scalar whose explicit indentation indicator
// disagrees with the indentation it then writes, and yaml.Unmarshal — the same
// library, in the same binary — refuses the result:
//
//	decisions:
//	    - |4-
//	      <parameter name="files_changed">…
//
// The indicator promises content at column 8; the content sits at column 6.
// Two real session notes were written this way, one day apart, and each one
// took its whole project's session index down with it because the reader was
// fail-closed. There is no fixed release to upgrade to: `go list -m -versions
// gopkg.in/yaml.v3` returns v3.0.0 and v3.0.1, and v3.0.1 is what has the bug.
//
// # The trigger is POSITION-DEPENDENT, which is why the verify is the guard
//
// Measured, marshalling one string and immediately unmarshalling it:
//
//	string                    as a sequence item   as a top-level scalar
//	"a\nb"                    ok                   ok
//	" a\nb"  (1st starts sp)  FAIL                 ok
//	"\ta\nb" (1st starts tab) FAIL                 FAIL
//	"\nb"    (1st line empty) FAIL                 ok
//	"a\n  b", "a\n\tb", "a\n\nb", CRLF, trailing spaces — all ok
//
// And one shape that is worse than any of the above because it does NOT fail:
// a string ending in "\n" round-trips as a SHORTER string ("a\nb\n" is read
// back as "a\nb"), because the emitter clip-chomps it. Nothing reports that.
// It was found by verifySessionRoundTrip comparing values rather than by the
// survey above, which only checked that the bytes parsed — which is precisely
// the argument for the verify comparing values.
//
// A leading space is fatal in a sequence and harmless in a scalar. So the set
// of unsafe strings is not a property of the string alone, and any predicate
// written against it is a guess at where the library's emitter will next
// disagree with its parser. normalizeYAMLScalar is a REPAIR heuristic against
// the shapes measured so far, NOT the guard. The guard is
// verifySessionRoundTrip, which asks the actual reader — and it has already
// earned that distinction once: it caught the trailing-newline shape the
// measurement above had missed.
//
// 🔴 A FIXTURE TEST COULD NOT HAVE CAUGHT THIS. A fixture pins the bytes we
// believed we would write; the defect is that we wrote bytes we could not read.
// A fixture asserting `- |4-` would have PASSED while shipping the outage. The
// round-trip property test is the check whose absence let this ship, which is
// why it is the centre of this file's tests rather than an extra.
//
// # Normalize, then verify, then fail HARD — in that order
//
// Refusing a note that merely carries a leading newline would be capture loss,
// and this project ruled at iteration 196 that capture loss fails hard with no
// `partial` tier — so refusal cannot be the ordinary path for a repairable
// string. Normalization keeps the note. A note that STILL does not round-trip
// after normalization is the genuine capture-loss case and is refused loudly.
//
// Normalization is REPORTED, never silent. An instrument that quietly repairs
// its input is the same defect as one that quietly skips it: the caller cannot
// tell a clean write from a repaired one, and the historical record was edited
// with nobody told. Every writer returns the list of fields it normalized.

// normalizeYAMLScalar returns s in a form yaml.v3 can read back, plus whether
// it changed anything.
//
// It strips leading blank lines, then leading horizontal whitespace from what
// is left of the first line. Both are the measured triggers and neither carries
// meaning in a session note's prose fields — the specimen's decisions entry was
// "\n<parameter …>", which normalizes to "<parameter …>" with nothing lost.
//
// Indentation on LATER lines is deliberately preserved: "a\n  b" and "a\n\tb"
// both round-trip, and a repair that flattened them would destroy real structure
// (code snippets and quoted output in a summary) to fix a defect they do not
// have.
func normalizeYAMLScalar(s string) (string, bool) {
	if !strings.Contains(s, "\n") {
		// Single-line strings are emitted as plain or quoted scalars, never as
		// block scalars, so the indicator bug cannot reach them.
		return s, false
	}
	out := strings.TrimLeft(s, "\n\r")
	// Only the FIRST line's leading whitespace, and only after the blank lines
	// are gone — TrimLeft over " \t\n" together would eat indentation from the
	// first line of genuinely indented content two lines down.
	if i := strings.IndexAny(out, "\n"); i >= 0 {
		first, rest := out[:i], out[i:]
		out = strings.TrimLeft(first, " \t") + rest
	} else {
		out = strings.TrimLeft(out, " \t")
	}
	// TRAILING newlines go too, and this one was found BY THE GUARD rather than
	// by the measurement that preceded it. A string ending in "\n" parses back
	// fine and comes back SHORT: yaml.v3 emits it clip-chomped, so "a\nb\n" is
	// read as "a\nb". That is silent value corruption rather than a parse
	// failure — strictly worse, because nothing anywhere reports it — and the
	// first survey of this bug missed it entirely by checking only that the
	// bytes parsed. Normalizing here makes what we write equal what we will
	// read, which is the only version of this the verify can certify.
	out = strings.TrimRight(out, "\n\r")
	return out, out != s
}

// normalizeStrings applies normalizeYAMLScalar across a slice in place-safe
// fashion, reporting the indices that changed.
func normalizeStrings(in []string) ([]string, []int) {
	if len(in) == 0 {
		return in, nil
	}
	out := make([]string, len(in))
	var changed []int
	for i, s := range in {
		n, did := normalizeYAMLScalar(s)
		out[i] = n
		if did {
			changed = append(changed, i)
		}
	}
	if changed == nil {
		return in, nil
	}
	return out, changed
}

// normalizeSessionMeta returns meta with every free-text field made safe to
// marshal, plus the yaml key names of the fields it changed.
//
// The field list is the set of SessionMeta members that carry CALLER-SUPPLIED
// prose. Decisions is where the live defect surfaced, but it is not special:
// capture/session.go:297 assigns Decisions straight from the MCP caller's
// params with no sanitization, and Summary, Title, OpenThreads and FilesChanged
// arrive by the same route. Covering only the field that happened to break
// first is how this recurs under a different key.
func normalizeSessionMeta(meta SessionMeta) (SessionMeta, []string) {
	var fields []string

	if n, did := normalizeYAMLScalar(meta.Title); did {
		meta.Title = n
		fields = append(fields, "title")
	}
	if n, did := normalizeYAMLScalar(meta.Summary); did {
		meta.Summary = n
		fields = append(fields, "summary")
	}
	if n, did := normalizeYAMLScalar(meta.SearchSummary); did {
		meta.SearchSummary = n
		fields = append(fields, "search_summary")
	}
	if n, changed := normalizeStrings(meta.Decisions); changed != nil {
		meta.Decisions = n
		fields = append(fields, fmt.Sprintf("decisions[%s]", joinInts(changed)))
	}
	if n, changed := normalizeStrings(meta.OpenThreads); changed != nil {
		meta.OpenThreads = n
		fields = append(fields, fmt.Sprintf("open_threads[%s]", joinInts(changed)))
	}
	if n, changed := normalizeStrings(meta.FilesChanged); changed != nil {
		meta.FilesChanged = n
		fields = append(fields, fmt.Sprintf("files_changed[%s]", joinInts(changed)))
	}
	return meta, fields
}

func joinInts(in []int) string {
	parts := make([]string, len(in))
	for i, n := range in {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ",")
}

// verifySessionRoundTrip parses data back with the REAL reader and reports a
// divergence from want.
//
// 🔴 IT CALLS ParseFrontmatter, NOT yaml.Unmarshal. The guard is only worth
// something if it asks the exact function the read path asks — a local
// re-implementation would drift from the reader and start certifying files the
// reader still refuses, which is this defect with extra steps.
//
// It compares the fields normalization touches rather than the whole struct:
// those are the free-text members that can trip the emitter, and a DeepEqual
// over SessionMeta would also compare pointer members (Breakdown) whose
// identity is not what this is checking.
func verifySessionRoundTrip(data []byte, want SessionMeta) error {
	got, _, err := ParseFrontmatter(data)
	if err != nil {
		return fmt.Errorf("the bytes just marshaled cannot be read back by ParseFrontmatter: %w", err)
	}
	if got.Title != want.Title {
		return fmt.Errorf("title did not survive the round trip: wrote %q, read back %q", want.Title, got.Title)
	}
	if got.Summary != want.Summary {
		return fmt.Errorf("summary did not survive the round trip: wrote %d bytes, read back %d",
			len(want.Summary), len(got.Summary))
	}
	if got.SearchSummary != want.SearchSummary {
		return fmt.Errorf("search_summary did not survive the round trip: wrote %d bytes, read back %d",
			len(want.SearchSummary), len(got.SearchSummary))
	}
	for _, f := range []struct {
		name      string
		want, got []string
	}{
		{"decisions", want.Decisions, got.Decisions},
		{"open_threads", want.OpenThreads, got.OpenThreads},
		{"files_changed", want.FilesChanged, got.FilesChanged},
	} {
		if len(f.want) != len(f.got) {
			return fmt.Errorf("%s did not survive the round trip: wrote %d entries, read back %d",
				f.name, len(f.want), len(f.got))
		}
		for i := range f.want {
			if f.want[i] != f.got[i] {
				return fmt.Errorf("%s[%d] did not survive the round trip: wrote %q, read back %q",
					f.name, i, f.want[i], f.got[i])
			}
		}
	}
	return nil
}

// errRewriteWouldNormalize is what a SINGLE-FIELD rewrite of a note already at
// rest returns when the write-side guard had to repair something else.
//
// TryLinkArchiveToSessions, ScoreUnscoredNotes and BackfillArchiveLink each
// change exactly one field of a note they just read. If normalization fires
// during one of those, the rewrite would ALSO alter prose those operations do
// not own — so they refuse instead. The precedent is
// OverwriteTaskFileRewritingHeader: which callers may move content is a
// decision made per entry point and visible to a reader, never a boolean
// threaded through a shared writer.
//
// The capture and enrichment paths do own that prose, so they normalize and
// report rather than refusing — losing a whole note to a leading newline would
// be capture loss, and capture loss fails hard with no `partial` tier.
func errRewriteWouldNormalize(fields []string) error {
	return fmt.Errorf(
		"this note needs YAML repair in %s, and a single-field rewrite must not silently "+
			"alter prose it does not own. Repair the note through the capture path, or fix it by hand",
		strings.Join(fields, ", "))
}

// SessionSkip names a session note the reader could not parse.
//
// It is a REPORT, not an error, and the distinction is the whole of the fix it
// belongs to. Two malformed notes out of 2360 once returned the entire session
// index of the two largest projects as empty, because ListSessions was
// fail-closed and every caller either bailed on the error or dropped the
// results — so `vp_bootstrap_context` reported zero sessions and the absence
// read as normal.
//
// 🔴 IT IS RETURNED, NOT LOGGED, AND THERE IS NO VARIANT THAT DISCARDS IT. A
// `ListSessions` wrapper that dropped skips would be shorter, so it is what
// every future call site would reach for — and a silently skipped file is the
// same defect as a fail-closed one, moved down a layer. Every caller takes the
// third return value and decides how to surface it; the compiler is what makes
// that decision unavoidable.
type SessionSkip struct {
	// Path is vault-relative, because that is what an operator acts on and
	// what is stable across hosts.
	Path   string
	Reason string
}

// vaultRelPath renders an absolute vault path relative to the vault root,
// falling back to the absolute path when it cannot (never worth failing a
// listing over a cosmetic path).
func vaultRelPath(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}
