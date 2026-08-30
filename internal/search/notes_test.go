// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSessionNote writes Projects/<project>/sessions/<stem>.md with the given
// frontmatter body pair, in the shape storage.ParseFrontmatter expects.
func writeSessionNote(t *testing.T, vaultRoot, project, stem, date, title, body string) string {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", project, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"---",
		"session_id: " + stem,
		"project: " + project,
		"date: " + date,
		"title: " + title,
		"---",
		body,
	}, "\n")
	path := filepath.Join(dir, stem+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCollectNoteCorpus_IndexesBodyNotFrontmatter is the core contract: the
// note BODY becomes corpus rows and the frontmatter does not. Frontmatter is
// already served verbatim by vp_search_sessions, which reads it off disk and
// never goes through the vector index.
func TestCollectNoteCorpus_IndexesBodyNotFrontmatter(t *testing.T) {
	_, v := testEngine(t)

	const bodyMarker = "OCELOT_NOTE_BODY_MARKER_alpha"
	const fmMarker = "MARMOSET_FRONTMATTER_ONLY_MARKER"
	writeSessionNote(t, v.Root, "note-only", "2026-08-30-abcd1234-01", "2026-08-30",
		fmMarker, "Wrap prose carrying "+bodyMarker+" in the narrative.")

	ids, texts, metas, err := collectNoteCorpus(v, "note-only")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatal("a project with a session note must produce corpus rows")
	}
	if len(ids) != len(texts) || len(ids) != len(metas) {
		t.Fatalf("parallel slices disagree: ids=%d texts=%d metas=%d", len(ids), len(texts), len(metas))
	}

	var sawBody bool
	for i, txt := range texts {
		if strings.Contains(txt, bodyMarker) {
			sawBody = true
		}
		// The load-bearing negative: no frontmatter-only string may appear in
		// ANY indexed text.
		if strings.Contains(txt, fmMarker) {
			t.Errorf("texts[%d] carries a frontmatter-only value %q — frontmatter must not be indexed: %q",
				i, fmMarker, txt)
		}
		if strings.Contains(txt, "session_id:") || strings.Contains(txt, "---") {
			t.Errorf("texts[%d] carries frontmatter delimiters/keys: %q", i, txt)
		}
	}
	if !sawBody {
		t.Errorf("expected the note BODY marker %q in some indexed text", bodyMarker)
	}

	m := metas[0]
	if m.Project != "note-only" {
		t.Errorf("Project = %q, want note-only", m.Project)
	}
	if m.SourceType != noteSourceType {
		t.Errorf("SourceType = %q, want %q", m.SourceType, noteSourceType)
	}
	if m.SourceType == "session" {
		t.Error("the note corpus must be distinguishable from the transcript corpus at a glance")
	}
	if m.Wing != noteWing || m.Room != noteRoom || m.Hall != noteHall {
		t.Errorf("location = %s/%s/%s, want %s/%s/%s", m.Wing, m.Room, m.Hall, noteWing, noteRoom, noteHall)
	}
	// The synthetic wing must never be a project slug — DetectWing returns the
	// project slug as the wing for every transcript-derived drawer.
	if m.Wing == "note-only" {
		t.Error("synthetic note wing must not be a project slug")
	}
	if m.Date != "2026-08-30" {
		t.Errorf("Date = %q, want 2026-08-30 (from note frontmatter)", m.Date)
	}
	if m.SourceRef != "sessions/2026-08-30-abcd1234-01.md" {
		t.Errorf("SourceRef = %q, want sessions/2026-08-30-abcd1234-01.md", m.SourceRef)
	}
}

// TestCollectNoteCorpus_IDsUniqueAndStable pins both halves: unique across
// notes AND chunks, and identical across two calls (so a rebuild reuses the
// embed cache instead of re-embedding the whole corpus).
func TestCollectNoteCorpus_IDsUniqueAndStable(t *testing.T) {
	_, v := testEngine(t)

	var long strings.Builder
	for long.Len() < 2500 {
		long.WriteString("NOTE_CHUNKING_PHRASE and more prose about the same topic. ")
	}
	writeSessionNote(t, v.Root, "multi", "2026-08-01-aaaa1111-01", "2026-08-01", "one", long.String())
	writeSessionNote(t, v.Root, "multi", "2026-08-02-aaaa1111-01", "2026-08-02", "two", "Short second note body.")

	ids1, _, metas1, err := collectNoteCorpus(v, "multi")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids1) < 3 {
		t.Fatalf("expected a chunked note plus a short one (>=3 rows), got %d", len(ids1))
	}

	seen := make(map[string]bool, len(ids1))
	for i, id := range ids1 {
		if seen[id] {
			t.Errorf("duplicate id at %d: %q", i, id)
		}
		seen[id] = true
		if len(id) == 8 {
			t.Errorf("note id %q is 8 chars — it must not look like an md5[:8] DrawerID", id)
		}
		if !strings.HasPrefix(id, "note.multi.") {
			t.Errorf("id %q must be keyed on (project, note, chunk)", id)
		}
	}

	// Chunk indices ascend within a note and the SourceRef is shared, so Search
	// dedup keeps one hit per note.
	refCounts := map[string]int{}
	for _, m := range metas1 {
		if m.ChunkIndex != refCounts[m.SourceRef] {
			t.Errorf("ChunkIndex = %d for %s, want %d", m.ChunkIndex, m.SourceRef, refCounts[m.SourceRef])
		}
		refCounts[m.SourceRef]++
	}
	if len(refCounts) != 2 {
		t.Errorf("want 2 distinct SourceRefs (one per note), got %d", len(refCounts))
	}

	ids2, _, _, err := collectNoteCorpus(v, "multi")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids1) != len(ids2) {
		t.Fatalf("id count changed between calls: %d then %d", len(ids1), len(ids2))
	}
	for i := range ids1 {
		if ids1[i] != ids2[i] {
			t.Fatalf("ids[%d] unstable: %q then %q", i, ids1[i], ids2[i])
		}
	}
}

// TestCollectNoteCorpus_EmptyBodyContributesNothing — an empty chunk in the
// index is a hit with no content.
func TestCollectNoteCorpus_EmptyBodyContributesNothing(t *testing.T) {
	_, v := testEngine(t)

	writeSessionNote(t, v.Root, "blank", "2026-08-03-bbbb2222-01", "2026-08-03", "empty", "")
	writeSessionNote(t, v.Root, "blank", "2026-08-04-bbbb2222-01", "2026-08-04", "ws", "   \n\n\t\n  ")

	ids, texts, metas, err := collectNoteCorpus(v, "blank")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 || len(texts) != 0 || len(metas) != 0 {
		t.Fatalf("empty and whitespace-only bodies must contribute nothing; got ids=%d texts=%d metas=%d",
			len(ids), len(texts), len(metas))
	}

	// Non-vacuity: the same project with one real body does produce rows.
	writeSessionNote(t, v.Root, "blank", "2026-08-05-bbbb2222-01", "2026-08-05", "real", "Actual prose here.")
	ids, _, _, err = collectNoteCorpus(v, "blank")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("want exactly the one non-empty note's chunk, got %d", len(ids))
	}
}

// TestCollectNoteCorpus_NoSessionDirIsEmpty mirrors the iteration corpus's
// missing-file convention: absence is empty-and-nil, never an error.
func TestCollectNoteCorpus_NoSessionDirIsEmpty(t *testing.T) {
	_, v := testEngine(t)
	ids, texts, metas, err := collectNoteCorpus(v, "no-such-proj")
	if err != nil {
		t.Fatalf("a missing sessions dir must not be an error: %v", err)
	}
	if len(ids) != 0 || len(texts) != 0 || len(metas) != 0 {
		t.Fatalf("want empty, got ids=%d texts=%d metas=%d", len(ids), len(texts), len(metas))
	}
}

// TestCollectNoteCorpus_UnparseableNoteIsSkipped: content that will not parse
// contributes nothing without failing the rebuild — the same convention
// collectIterationCorpus applies when ParseEntries yields no entries.
func TestCollectNoteCorpus_UnparseableNoteIsSkipped(t *testing.T) {
	_, v := testEngine(t)

	dir := filepath.Join(v.Root, "Projects", "malformed", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0000-no-frontmatter.md"),
		[]byte("no frontmatter delimiter at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSessionNote(t, v.Root, "malformed", "2026-08-06-cccc3333-01", "2026-08-06", "ok", "Good body.")

	ids, texts, _, err := collectNoteCorpus(v, "malformed")
	if err != nil {
		t.Fatalf("a malformed note must not fail the whole corpus: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("want only the well-formed note's chunk, got %d", len(ids))
	}
	if !strings.Contains(texts[0], "Good body.") {
		t.Errorf("surviving text = %q", texts[0])
	}
}

// TestNoteCacheID_KeyedOnIdentityNotContent pins the honest residual, both
// halves. The ID is keyed on (project, note, chunk) — the OPPOSITE trade from
// DrawerID = md5(wing+content):
//
//   - a note chunk can never share a key with a transcript chunk, because no
//     DrawerID is ever computed over note text; and
//   - two notes carrying IDENTICAL text still produce two separate rows, so the
//     note corpus is ADDITIVE and does not dedup against transcripts or against
//     itself.
func TestNoteCacheID_KeyedOnIdentityNotContent(t *testing.T) {
	if got := noteCacheID("vibe-palace", "2026-08-30-12a23ab8-08", 4); got != "note.vibe-palace.2026-08-30-12a23ab8-08.c4" {
		t.Fatalf("id = %q", got)
	}
	if len(noteCacheID("p", "s", 0)) == 8 {
		t.Fatal("note cache id must not be an 8-char md5 drawer id")
	}

	_, v := testEngine(t)
	const dupe = "IDENTICAL_NOTE_BODY_TEXT_IN_TWO_NOTES"
	writeSessionNote(t, v.Root, "dupes", "2026-08-07-dddd4444-01", "2026-08-07", "a", dupe)
	writeSessionNote(t, v.Root, "dupes", "2026-08-08-dddd4444-01", "2026-08-08", "b", dupe)

	ids, texts, _, err := collectNoteCorpus(v, "dupes")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("identical bodies must still produce two rows (id is keyed on identity, not content); got %d", len(ids))
	}
	if ids[0] == ids[1] {
		t.Fatalf("two notes must not share an id: %q", ids[0])
	}
	if texts[0] != texts[1] {
		t.Fatalf("the two rows should carry the same text: %q vs %q", texts[0], texts[1])
	}
}

// TestRebuild_NoteOnlyProjectIsSearchable is the whole point: a project whose
// sessions were captured as NOTES with no transcript and no archive has nothing
// the drawer index can ingest, and was therefore unreachable from `vp search`.
func TestRebuild_NoteOnlyProjectIsSearchable(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	const unique = "PANGOLIN_WRAP_NOTE_MARKER_beta"
	writeSessionNote(t, v.Root, "qa-notes-only", "2026-08-09-eeee5555-01", "2026-08-09",
		"a wrap", "The wrap note records that "+unique+" happened during the session.")

	stats, err := eng.Rebuild(ctx, "qa-notes-only")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Drawers != 0 {
		t.Errorf("Drawers = %d, want 0 — this project has no drawer store", stats.Drawers)
	}
	if stats.IterationChunks != 0 {
		t.Errorf("IterationChunks = %d, want 0", stats.IterationChunks)
	}
	if stats.NoteChunks == 0 {
		t.Fatal("NoteChunks = 0 — the note corpus did not reach Rebuild")
	}
	if stats.Indexed != stats.NoteChunks {
		t.Errorf("Indexed = %d, want %d (notes are the only source here)", stats.Indexed, stats.NoteChunks)
	}

	results, err := eng.Search(ctx, unique, SearchFilters{Project: "qa-notes-only", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findHitContaining(results, unique)
	if !ok {
		t.Fatalf("expected a hit whose content contains %q; got %d results", unique, len(results))
	}
	if got.SourceType != noteSourceType {
		t.Errorf("SourceType = %q, want %q", got.SourceType, noteSourceType)
	}
	if got.SourceRef != "sessions/2026-08-09-eeee5555-01.md" {
		t.Errorf("SourceRef = %q", got.SourceRef)
	}
	if got.Date != "2026-08-09" {
		t.Errorf("Date = %q, want 2026-08-09", got.Date)
	}
	if !strings.HasPrefix(got.DrawerID, "note.qa-notes-only.2026-08-09-eeee5555-01.c") {
		t.Errorf("DrawerID = %q", got.DrawerID)
	}
}

// 🔴 TestRebuild_NoteCorpusWritesNoKnowledgeGraphAndNoDrawers is the
// load-bearing test of this change, and it pins a RULING, not a preference.
//
// The note corpus must never invent knowledge-graph facts out of wrap prose.
// That is achieved STRUCTURALLY — the note pass does not call
// capture.IndexTranscript, so capture's extractEntities is not on the code path
// at all — rather than by a boolean that a later edit could flip. The same
// structure is what guarantees no drawer JSONL is written and no
// storage.DrawerID is ever computed over note text, so a note chunk cannot
// collide with a transcript chunk.
//
// The assertion is therefore on the FILESYSTEM after a real Rebuild over a
// note-only project: no kg/ entity file anywhere under palace/, and no
// drawers.jsonl anywhere under palace/.
func TestRebuild_NoteCorpusWritesNoKnowledgeGraphAndNoDrawers(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	// Prose deliberately dense in the entity shapes capture's extractor keys on
	// (Capitalized names, file paths, identifiers) — so a KG write, if one
	// existed on this path, would have something to find.
	writeSessionNote(t, v.Root, "kg-guard", "2026-08-10-ffff6666-01", "2026-08-10", "wrap",
		"Claude reviewed internal/search/engine.go with John and decided that "+
			"RebuildStats needs a NoteChunks counter. See doc/ARCHITECTURE.md.")

	stats, err := eng.Rebuild(ctx, "kg-guard")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.NoteChunks == 0 {
		// Non-vacuity: if nothing was indexed, "no KG was written" proves nothing.
		t.Fatal("NoteChunks = 0 — this test would be vacuous")
	}

	kgPath, err := v.KGEntitiesFile("kg-guard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kgPath); !os.IsNotExist(err) {
		t.Errorf("the note pass must write NO knowledge graph; %s exists (stat err = %v)", kgPath, err)
	}

	var kgFiles, drawerFiles []string
	palaceRoot := filepath.Join(v.Root, "palace")
	err = filepath.WalkDir(palaceRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(v.Root, path)
		switch {
		case strings.Contains(filepath.ToSlash(rel), "/kg/"):
			kgFiles = append(kgFiles, rel)
		case filepath.Base(path) == "drawers.jsonl":
			drawerFiles = append(drawerFiles, rel)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(kgFiles) != 0 {
		t.Errorf("the note pass wrote knowledge-graph files: %v", kgFiles)
	}
	if len(drawerFiles) != 0 {
		t.Errorf("the note pass wrote drawer JSONL: %v", drawerFiles)
	}

	// A second rebuild must not change that — the KG stays absent, not merely
	// "not written on a cold run".
	if _, err := eng.Rebuild(ctx, "kg-guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kgPath); !os.IsNotExist(err) {
		t.Errorf("a second Rebuild wrote %s", kgPath)
	}
}

// TestRebuild_NoteChunksSurviveAlongsideIterations proves the note source is a
// THIRD corpus, additive to the other two rather than replacing either — and
// that the widened reach is real: a project with iterations AND notes gets both.
func TestRebuild_NoteChunksSurviveAlongsideIterations(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	const iterMarker = "BOTH_SOURCES_ITERATION_MARKER"
	const noteMarker = "BOTH_SOURCES_NOTE_MARKER"
	writeIterationsMD(t, v.Root, "both", "## Iteration 3 — x\n\n"+iterMarker+"\n\n---\n")
	writeSessionNote(t, v.Root, "both", "2026-08-11-7777aaaa-01", "2026-08-11", "wrap", noteMarker)

	stats, err := eng.Rebuild(ctx, "both")
	if err != nil {
		t.Fatal(err)
	}
	if stats.IterationChunks == 0 || stats.NoteChunks == 0 {
		t.Fatalf("want both sources; IterationChunks=%d NoteChunks=%d", stats.IterationChunks, stats.NoteChunks)
	}
	if stats.Indexed != stats.IterationChunks+stats.NoteChunks {
		t.Errorf("Indexed = %d, want %d", stats.Indexed, stats.IterationChunks+stats.NoteChunks)
	}

	results, err := eng.Search(ctx, "marker", SearchFilters{Project: "both", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findHitContaining(results, iterMarker); !ok {
		t.Error("iteration hit disappeared once the note source landed")
	}
	if _, ok := findHitContaining(results, noteMarker); !ok {
		t.Error("note hit missing")
	}
}

// TestCollectNoteCorpus_UnreadableNoteFailsTheRebuild pins the OTHER half of
// the error convention, and it is the half that differs from a skip: an I/O
// failure reading a note fails the whole corpus, exactly as
// collectIterationCorpus does on a non-NotExist os.ReadFile error. A directory
// named *.md is the portable way to force EISDIR without depending on file
// permissions (which do nothing when the suite runs as root).
func TestCollectNoteCorpus_UnreadableNoteFailsTheRebuild(t *testing.T) {
	eng, v := testEngine(t)

	dir := filepath.Join(v.Root, "Projects", "unreadable", "sessions")
	if err := os.MkdirAll(filepath.Join(dir, "2026-08-13-9999zzzz-01.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := collectNoteCorpus(v, "unreadable"); err == nil {
		t.Fatal("an unreadable note must fail the corpus, not be silently skipped")
	} else if !strings.Contains(err.Error(), "read session note") {
		t.Errorf("error = %q, want it to name the note it could not read", err)
	}

	// And that failure must reach Rebuild rather than being swallowed.
	if _, err := eng.Rebuild(context.Background(), "unreadable"); err == nil {
		t.Fatal("Rebuild must surface the note-corpus error")
	} else if !strings.Contains(err.Error(), "note corpus") {
		t.Errorf("Rebuild error = %q, want it to name the note corpus", err)
	}
}
