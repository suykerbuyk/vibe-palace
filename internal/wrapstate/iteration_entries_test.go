// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"strings"
	"testing"
)

// writerFrame mirrors storage.AppendIterationOwned's on-disk frame so round-trip
// tests stay coupled to the real writer without importing storage (cycle).
func writerFrame(n int, title, body string) string {
	return "\n---\n" + FormatIterationHeader(n, title) + "\n\n" + strings.TrimSpace(body) + "\n"
}

func TestParseEntries_RoundTripWriterFrame(t *testing.T) {
	body1 := "first narrative\nwith two lines"
	body2 := "second narrative"
	content := "# project — Iteration Narratives\n" +
		writerFrame(1, "one", body1) +
		writerFrame(2, "two", body2)

	got := ParseEntries(content)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2: %+v", len(got), got)
	}
	if got[0].N != 1 || got[0].Title != "one" || got[0].Body != body1 {
		t.Errorf("entry0 = %+v", got[0])
	}
	if got[0].Bytes != len(body1) {
		t.Errorf("entry0.Bytes=%d want %d", got[0].Bytes, len(body1))
	}
	if got[1].N != 2 || got[1].Title != "two" || got[1].Body != body2 {
		t.Errorf("entry1 = %+v", got[1])
	}
	// Writer/reader agree on the header FormatIterationHeader emits.
	if got[0].Header != FormatIterationHeader(1, "one") {
		t.Errorf("header0=%q", got[0].Header)
	}
}

func TestParseEntries_DuplicateN(t *testing.T) {
	content := writerFrame(128, "a", "body-a") +
		writerFrame(128, "b", "body-b") +
		writerFrame(128, "c", "body-c") +
		writerFrame(128, "d", "body-d")
	all := EntriesByN(content, 128)
	if len(all) != 4 {
		t.Fatalf("matches=%d want 4", len(all))
	}
	for i, want := range []string{"a", "b", "c", "d"} {
		if all[i].Title != want {
			t.Errorf("match[%d].Title=%q want %q", i, all[i].Title, want)
		}
	}
	last, ok := LastEntryByN(content, 128)
	if !ok || last.Title != "d" {
		t.Fatalf("LastEntryByN = %+v ok=%v", last, ok)
	}
	if gap := EntriesByN(content, 999); len(gap) != 0 {
		t.Errorf("gap should be empty, got %+v", gap)
	}
}

func TestParseEntries_InlineCodeRunDoesNotSwallow(t *testing.T) {
	// iterations.md:698 class — inline ``` run must not open a fence.
	content := FormatIterationHeader(3, "real") + "\n\n" +
		"see the ```bash tutorial``` for details\n\n" +
		FormatIterationHeader(190, "also real") + "\n\nbody-190\n"
	got := ParseEntries(content)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 (inline run must not swallow): %+v", len(got), got)
	}
	if got[0].N != 3 || got[1].N != 190 {
		t.Errorf("Ns = %d,%d", got[0].N, got[1].N)
	}
	if got[1].Body != "body-190" {
		t.Errorf("body190=%q", got[1].Body)
	}
}

func TestParseEntries_FencedFalseHeadingIgnored(t *testing.T) {
	// iterations.md:16185-16186 class — a real ## Iteration line INSIDE a fence.
	content := FormatIterationHeader(177, "real one") + "\n\n" +
		"narrative that documents the deleted duplicate rule:\n\n" +
		"```md\n" +
		"## Iteration 177 — blind-writer sample in a fence\n" +
		"## Iteration 177 — addendum sample in a fence\n" +
		"```\n\n" +
		"closing prose\n" +
		writerFrame(178, "next", "after")
	// Prepend nothing — writerFrame adds leading ---
	content = "# title\n" + writerFrame(177, "real one",
		"narrative that documents the deleted duplicate rule:\n\n"+
			"```md\n"+
			"## Iteration 177 — blind-writer sample in a fence\n"+
			"## Iteration 177 — addendum sample in a fence\n"+
			"```\n\n"+
			"closing prose") +
		writerFrame(178, "next", "after")

	got := ParseEntries(content)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 (fenced headings must not match): %+v", len(got), got)
	}
	if got[0].N != 177 || got[1].N != 178 {
		t.Errorf("Ns=%d,%d", got[0].N, got[1].N)
	}
	if strings.Contains(got[0].Body, "## Iteration 177 — blind-writer") {
		// body may contain the fenced text as content — that's fine
	}
	// Fenced samples must not appear as separate entries.
	if len(EntriesByN(content, 177)) != 1 {
		t.Errorf("177 matches=%d want 1", len(EntriesByN(content, 177)))
	}
}

func TestEntriesNewestFirst(t *testing.T) {
	content := writerFrame(1, "a", "a") + writerFrame(2, "b", "bb") + writerFrame(3, "c", "ccc")
	nf := EntriesNewestFirst(content)
	if len(nf) != 3 || nf[0].N != 3 || nf[2].N != 1 {
		t.Fatalf("newest-first = %+v", nf)
	}
}

func TestScanIterHeadings_ExportMatchesCounter(t *testing.T) {
	content := "## Iteration 5 — x\n### Iteration 7 — y\n"
	h := ScanIterHeadings(content)
	if len(h) != 2 || h[0].N != 5 || h[1].N != 7 || h[1].Hashes != "###" {
		t.Fatalf("%+v", h)
	}
}
