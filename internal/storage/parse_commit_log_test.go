// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"testing"
)

// TestParseCommitLogAgreesWithArchivedSHAs pins the one header rule shared by
// the writer-side archivedSHAs (SHA set) and the search-path collector
// (ParseCommitLog entries): their SHA sets must agree on every input, including
// the three edge shapes the reviewer called out and a duplicate-SHA log.
func TestParseCommitLogAgreesWithArchivedSHAs(t *testing.T) {
	const (
		shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		shaC = "cccccccccccccccccccccccccccccccccccccccc"
	)
	cases := []struct {
		name string
		body string
		shas []string // the SHA SET both functions must return
	}{
		{
			name: "a header quoted mid-line is not an entry",
			// shaB appears only inside a body line that does NOT start with the
			// header prefix, so neither function may count it.
			body: "\n## commit " + shaA + "\n\nthe body says: see `## commit " + shaB + "` for context\n",
			shas: []string{shaA},
		},
		{
			name: "an empty-body header still counts",
			body: "\n## commit " + shaA + "\n",
			shas: []string{shaA},
		},
		{
			name: "a header with a trailing suffix after the SHA",
			body: "\n## commit " + shaC + " (cherry-picked)\n\nbody\n",
			shas: []string{shaC},
		},
		{
			name: "a header with an empty SHA token is skipped",
			body: "\n## commit \n\norphan body\n",
			shas: []string{},
		},
		{
			name: "duplicate SHA headers dedup to one in the set",
			body: "\n## commit " + shaA + "\n\nfirst body\n\n## commit " + shaA + "\n\nsecond body\n",
			shas: []string{shaA},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := map[string]struct{}{}
			for _, s := range tc.shas {
				want[s] = struct{}{}
			}
			// archivedSHAs (writer path).
			got := archivedSHAs(tc.body)
			if !setsEqual(got, want) {
				t.Errorf("archivedSHAs = %v, want %v", keys(got), tc.shas)
			}
			// ParseCommitLog's SHA set (search path) must agree.
			pset := map[string]struct{}{}
			for _, e := range ParseCommitLog(tc.body) {
				pset[e.SHA] = struct{}{}
			}
			if !setsEqual(pset, want) {
				t.Errorf("ParseCommitLog SHA set = %v, want %v", keys(pset), tc.shas)
			}
		})
	}
}

// TestParseCommitLogDuplicateSHAsAreDistinctEntries: two headers with the same
// SHA must NOT be merged into one entry — each keeps its own body — so the
// collector can key distinct cache ids on (sha, content) rather than silently
// folding two different bodies into one vector.
func TestParseCommitLogDuplicateSHAsAreDistinctEntries(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	body := "\n## commit " + sha + "\n\nfirst body\n\n## commit " + sha + "\n\nsecond body\n"
	entries := ParseCommitLog(body)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (duplicate-SHA headers must not merge)", len(entries))
	}
	if entries[0].Body == entries[1].Body {
		t.Fatalf("both entries share body %q — the two bodies were merged", entries[0].Body)
	}
	if entries[0].Body != "first body" || entries[1].Body != "second body" {
		t.Fatalf("bodies = %q, %q; want \"first body\", \"second body\"", entries[0].Body, entries[1].Body)
	}
}

func setsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
