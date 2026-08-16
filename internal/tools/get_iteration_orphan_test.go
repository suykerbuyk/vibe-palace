// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---------------------------------------------------------------------------
// Frame orphans at the MCP seam.
//
// storage.AppendIterationOwned composes every entry as
// "\n---\n" + FormatIterationHeader(n, title) + "\n\n" + body + "\n", and the
// reader half only ever split on "## Iteration N". An H2 sitting on that frame
// that is NOT that shape is a real entry boundary the reader could not see, so
// its heading and its whole narrative were fused onto the END of the previous
// numbered entry and served as part of it. Measured live, vp_get_iteration for
// N = 108, 110, 125, 128, 145 and 154 each over-returned exactly that way AND
// reported success — a reader that over-returns while claiming to be exact is
// worse than one that fails, because nothing downstream can tell.
//
// 040e9f6 taught wrapstate.ParseEntries to clamp a body at the earlier of the
// next numbered heading and the next frame orphan, and
// internal/wrapstate/heading_contract_test.go proves it at the helper. These
// tests exist because a helper test does not exercise the path the helper is
// installed on: they drive tool.Handler and ResolveURI — the real vp_get_iteration
// and vibe-palace://iteration/{project}/{n} seams — so the proof survives the
// tool wandering off the helper.
// ---------------------------------------------------------------------------

// orphanFrame builds an entry on the writer's frame whose heading is NOT what
// FormatIterationHeader emits — the shape the reader was blind to. It is
// deliberately hand-built rather than borrowed from the writer, because the
// writer can no longer produce this shape and the archive still carries it.
func orphanFrame(heading, body string) string {
	return "\n---\n" + heading + "\n\n" + strings.TrimSpace(body) + "\n"
}

// Distinctive markers. Each is a string that appears in exactly one place in
// the fixture, so a containment assertion cannot pass by coincidence.
const (
	orphanHeading110 = "## 2026-06-17 Wrap"
	orphanMarker110  = "ORPHAN-BODY-BELONGING-TO-NOBODY"
	body110          = "BODY-OF-ONE-HUNDRED-TEN"
	body112          = "BODY-OF-ONE-HUNDRED-TWELVE"
)

// TestGetIteration_OrphanNotGluedOntoPrecedingEntry is the 110/111 case.
// Live, vp_get_iteration n=110 returned 110 PLUS the whole "## 2026-06-17 Wrap"
// narrative that followed it, with complete:true and no marker. The tool must
// stop at the frame orphan even though the orphan itself can never be an entry.
func TestGetIteration_OrphanNotGluedOntoPrecedingEntry(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(110, "the reader stops here", body110),
		orphanFrame(orphanHeading110, orphanMarker110),
		frame(112, "next numbered", body112),
	)

	got := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 110})
	if got.Returned != 1 {
		t.Fatalf("returned=%d want 1: %+v", got.Returned, got.Entries)
	}
	e := got.Entries[0]
	if !strings.Contains(e.Body, body110) {
		t.Fatalf("110 lost its own narrative: body=%q", e.Body)
	}
	// The two halves of the defect: the orphan's HEADING and the orphan's BODY.
	// Asserting only the body would pass on a reader that swallowed the heading
	// and stopped — the live symptom carried both.
	if strings.Contains(e.Body, "2026-06-17 Wrap") {
		t.Fatalf("orphan HEADING glued onto 110's body: %q", e.Body)
	}
	if strings.Contains(e.Body, orphanMarker110) {
		t.Fatalf("orphan NARRATIVE glued onto 110's body: %q", e.Body)
	}
	// Bytes is the budget unit; an over-returned body inflates it silently.
	if e.Bytes != len(e.Body) {
		t.Fatalf("bytes=%d does not describe the body it advertises (len=%d)", e.Bytes, len(e.Body))
	}
	// The orphan is excluded, not renumbered: 112 is untouched and still its own.
	next := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 112})
	if next.Entries[0].Body != body112 {
		t.Fatalf("112 body=%q want %q", next.Entries[0].Body, body112)
	}
	// Archive extent must not have grown a number the orphan does not have.
	if got.OldestN != 110 || got.NewestN != 112 {
		t.Fatalf("archive extent %d..%d want 110..112", got.OldestN, got.NewestN)
	}
}

// TestGetIteration_AddendumStaysItsOwnEntry is the 108 case.
//
// 108 is a duplicate-N iteration BY DESIGN: the migration turned the orphan that
// followed the original 108 into a second, canonical 108, and file order is the
// historical record (see TestMigrationPreservesFileOrderAndAdjacency). So the
// seam must serve two distinct matches and must not fuse them — and the LAST of
// them must still stop at the next frame orphan, which is the shape that made
// 108 one of the six measured over-returning numbers in the first place.
//
// The trailing orphan is load-bearing in this fixture, not decoration: without
// it the test only re-proves numbered-heading splitting, which predates 040e9f6
// and would stay green with the orphan clamp removed.
func TestGetIteration_AddendumStaysItsOwnEntry(t *testing.T) {
	const (
		bodyFirst    = "BODY-108-THE-FIRST-NARRATIVE"
		bodyAddendum = "BODY-108-THE-ADDENDUM-NARRATIVE"
		orphanTail   = "ORPHAN-TAIL-AFTER-THE-ADDENDUM"
	)
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(108, "first", bodyFirst),
		frame(108, "addendum", bodyAddendum),
		orphanFrame("## 2026-06-12 Wrap", orphanTail),
		frame(109, "after", "BODY-109"),
	)

	got := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 108})
	if got.Returned != 2 {
		t.Fatalf("want 2 distinct matches for 108, got returned=%d %+v", got.Returned, got.Entries)
	}
	first, addendum := got.Entries[0], got.Entries[1]

	// File order is the record — the migration declined to sort for this reason.
	if first.Title != "first" || addendum.Title != "addendum" {
		t.Fatalf("file order lost: %q then %q", first.Title, addendum.Title)
	}
	if first.Body != bodyFirst {
		t.Fatalf("match 0 body=%q want %q", first.Body, bodyFirst)
	}
	if strings.Contains(first.Body, bodyAddendum) {
		t.Fatalf("the addendum was glued back onto the first 108: %q", first.Body)
	}
	if addendum.Body != bodyAddendum {
		t.Fatalf("match 1 body=%q want %q", addendum.Body, bodyAddendum)
	}
	// The last 108 sits directly on a frame orphan. This is the clamp.
	if strings.Contains(addendum.Body, orphanTail) || strings.Contains(addendum.Body, "2026-06-12 Wrap") {
		t.Fatalf("frame orphan glued onto the 108 addendum: %q", addendum.Body)
	}
	// Both matches addressable individually — a fused pair would advertise one.
	if first.Matches != 2 || addendum.Matches != 2 {
		t.Fatalf("matches=%d/%d want 2/2", first.Matches, addendum.Matches)
	}
	if first.MatchIndex == nil || *first.MatchIndex != 0 ||
		addendum.MatchIndex == nil || *addendum.MatchIndex != 1 {
		t.Fatalf("match_index %v/%v want 0/1", first.MatchIndex, addendum.MatchIndex)
	}
}

// TestGetIteration_UnnumberedOrphanIsNeverServed is defense in depth. The live
// archive is migrated, so the orphans that caused the incident are gone; this
// pins the behaviour for the NEXT unnumbered framed wrap someone appends by
// hand. An orphan with no recoverable number can never become an Entry, so the
// only two ways it can reach a caller are as a tail on its predecessor or under
// a number it does not own. Neither is allowed.
func TestGetIteration_UnnumberedOrphanIsNeverServed(t *testing.T) {
	const (
		orphanBody = "UNRULED-NARRATIVE-WITH-NO-NUMBER-AT-ALL"
		orphanHead = "## Some unruled narrative"
	)
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(40, "before", "BODY-40"),
		frame(41, "the last numbered one", "BODY-41"),
		orphanFrame(orphanHead, orphanBody),
	)

	got := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 41})
	if got.Entries[0].Body != "BODY-41" {
		t.Fatalf("41 body=%q want BODY-41 exactly (no orphan tail)", got.Entries[0].Body)
	}
	// Sweep every number a caller could plausibly ask for. Answering for a
	// number the file does not carry is the same failure as over-returning:
	// the caller cannot tell it got someone else's narrative.
	for n := 1; n <= 60; n++ {
		raw, err := json.Marshal(map[string]any{"project": "demo", "n": n})
		if err != nil {
			t.Fatal(err)
		}
		out, err := GetIterationTool(vault).Handler(t.Context(), raw)
		if err != nil {
			continue // gap: not-found is the correct answer
		}
		res, ok := out.(getIterationResult)
		if !ok {
			t.Fatalf("n=%d handler returned %T", n, out)
		}
		if n != 40 && n != 41 {
			t.Fatalf("n=%d resolved, but only 40 and 41 exist: %+v", n, res.Entries)
		}
		for _, e := range res.Entries {
			if strings.Contains(e.Body, orphanBody) || strings.Contains(e.Header, orphanHead) {
				t.Fatalf("unnumbered orphan surfaced under n=%d: %+v", n, e)
			}
		}
	}
	// Extent must report the numbers in the file, not a phantom for the orphan.
	if got.OldestN != 40 || got.NewestN != 41 {
		t.Fatalf("archive extent %d..%d want 40..41", got.OldestN, got.NewestN)
	}
}

// TestIterationResource_OrphanNotGluedOnResourcePath drives the OTHER seam.
// vibe-palace://iteration/{project}/{n} goes through LastEntryByN /
// EntryByNMatch, not through the tool handler, and it is what a host follows
// when a row is body_deferred. A tool-only proof would leave the whole
// deferred-body delivery path unasserted — which is exactly the path a large
// narrative like a wrap takes.
func TestIterationResource_OrphanNotGluedOnResourcePath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(110, "the reader stops here", body110),
		orphanFrame(orphanHeading110, orphanMarker110),
		frame(112, "next numbered", body112),
	)

	// Bare form → LastEntryByN.
	got, _, err := ResolveURI(mcp.IterationURI("demo", 110), nil, vault)
	if err != nil {
		t.Fatal(err)
	}
	if got != body110 {
		t.Fatalf("resource body=%q want %q", got, body110)
	}
	if strings.Contains(got, orphanMarker110) || strings.Contains(got, "2026-06-17 Wrap") {
		t.Fatalf("orphan served over the resource path: %q", got)
	}

	// Indexed form → EntryByNMatch. Single match, so index 0 is the same body;
	// this is the call the tool's content_uri makes on a multi-match row.
	idx, _, err := ResolveURI(mcp.IterationMatchURI("demo", 110, 0), nil, vault)
	if err != nil {
		t.Fatal(err)
	}
	if idx != body110 {
		t.Fatalf("indexed resource body=%q want %q", idx, body110)
	}

	// Byte-identity: what the tool inlined and what the resource serves are the
	// same bytes. If only one of the two seams clamps, this splits them apart.
	tool := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 110})
	if tool.Entries[0].Body != got {
		t.Fatalf("tool body %q != resource body %q", tool.Entries[0].Body, got)
	}

	// The orphan has no number of its own, so nothing addresses it.
	if _, _, err := ResolveURI(mcp.IterationURI("demo", 111), nil, vault); err == nil {
		t.Fatal("111 does not exist; the orphan must not be addressable as it")
	}
}

// TestGetIteration_RecentModeExcludesOrphan pins the third caller of the parser.
// recent mode walks EntriesNewestFirst and is the default way an agent reads the
// archive at session start, so an orphan fused onto the newest entry would enter
// context on every bootstrap-adjacent read — the highest-blast-radius version of
// the same defect.
func TestGetIteration_RecentModeExcludesOrphan(t *testing.T) {
	const (
		orphanBody = "RECENT-MODE-ORPHAN-NARRATIVE"
		orphanHead = "## 2026-07-02 Wrap"
	)
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(124, "before", "BODY-124"),
		frame(125, "the swallower", "BODY-125"),
		orphanFrame(orphanHead, orphanBody),
		frame(126, "after", "BODY-126"),
	)

	got := callGetIteration(t, vault, map[string]any{"project": "demo", "recent": true})
	if got.Returned != 3 {
		t.Fatalf("want all 3 numbered entries, got returned=%d %+v", got.Returned, got.Entries)
	}
	for _, e := range got.Entries {
		if strings.Contains(e.Body, orphanBody) || strings.Contains(e.Body, "2026-07-02 Wrap") {
			t.Fatalf("orphan fused onto entry %d in recent mode: %q", e.N, e.Body)
		}
		if strings.Contains(e.Header, orphanHead) {
			t.Fatalf("orphan heading served as an entry header: %q", e.Header)
		}
	}
	// bytes_inlined is what the budget spends. An over-returned body would make
	// the number bigger than the bodies actually delivered.
	sum := 0
	for _, e := range got.Entries {
		sum += len(e.Body)
	}
	if got.BytesInlined != sum {
		t.Fatalf("bytes_inlined=%d but bodies total %d", got.BytesInlined, sum)
	}
}
