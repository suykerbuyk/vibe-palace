// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// parseIterationSourceRef extracts n and matchIndex from a source_ref for
// assertions. Tolerates the raw row's trailing "/raw" suffix (see
// iterationRawSourceRef) so it works on both summary and raw refs.
func parseIterationSourceRef(ref string) (n, matchIndex int, ok bool) {
	const prefix = "iteration/"
	if !strings.HasPrefix(ref, prefix) {
		return 0, 0, false
	}
	rest := strings.TrimPrefix(ref, prefix)
	rest = strings.TrimSuffix(rest, "/raw")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "m" {
		return 0, 0, false
	}
	n, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return n, m, true
}

func writeIterationsMD(t *testing.T, vaultRoot, project, body string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "iterations.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findHitContaining(results []SearchResult, needle string) (SearchResult, bool) {
	for _, r := range results {
		if strings.Contains(r.Content, needle) {
			return r, true
		}
	}
	return SearchResult{}, false
}

// TestRebuild_IndexesIterationsWithoutDrawers is the empty-drawer path:
// mlnx-sw-os / rusty-can shaped projects still become searchable for history.
func TestRebuild_IndexesIterationsWithoutDrawers(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	const unique = "ZEBRA_QUOKKA_ITERATION_MARKER_alpha"
	writeIterationsMD(t, v.Root, "hist-only", strings.Join([]string{
		"# History",
		"",
		"## Iteration 1 — first",
		"",
		"Body one mentions nothing special.",
		"",
		"---",
		"",
		"## Iteration 2 — second",
		"",
		"Body two carries " + unique + " in the narrative.",
		"",
		"---",
		"",
	}, "\n"))

	if _, err := eng.Rebuild(ctx, "hist-only"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// Mock embedder is non-semantic — pull a wide candidate set and match on content.
	results, err := eng.Search(ctx, unique, SearchFilters{Project: "hist-only", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findHitContaining(results, unique)
	if !ok {
		t.Fatalf("expected a hit whose content contains %q; got %d results", unique, len(results))
	}
	// No cache was seeded for this project, so this entry gets only a RAW row
	// (SourceType iteration_raw), never a summary row (SourceType iteration).
	if got.SourceType != iterationRawSourceType {
		t.Errorf("SourceType = %q, want %q", got.SourceType, iterationRawSourceType)
	}
	wantRef := iterationRawSourceRef(2, 0)
	if got.SourceRef != wantRef {
		t.Errorf("SourceRef = %q, want %q", got.SourceRef, wantRef)
	}
	if got.Project != "hist-only" {
		t.Errorf("Project = %q, want hist-only", got.Project)
	}
	n, match, ok := parseIterationSourceRef(got.SourceRef)
	if !ok || n != 2 || match != 0 {
		t.Errorf("parse SourceRef: n=%d match=%d ok=%v", n, match, ok)
	}
	// SourceRef must NOT carry a chunk index (pin 1).
	if strings.Contains(got.SourceRef, "/c/") {
		t.Errorf("SourceRef must be per-entry, not per-chunk: %q", got.SourceRef)
	}
	// SearchResult does not expose ChunkIndex; cache ID carries it (pin 2).
	if !strings.HasPrefix(got.DrawerID, "iter.hist-only.2.m0.raw.c") {
		t.Errorf("DrawerID = %q, want iter.hist-only.2.m0.raw.c*", got.DrawerID)
	}
}

// TestRebuild_DuplicateNDistinctMatchRefs pins match-index citation when N repeats.
func TestRebuild_DuplicateNDistinctMatchRefs(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	const u0 = "DUP_N_MATCH_ZERO_MARKER"
	const u1 = "DUP_N_MATCH_ONE_MARKER"
	writeIterationsMD(t, v.Root, "dup-n", strings.Join([]string{
		"## Iteration 7 — first seven",
		"",
		u0,
		"",
		"---",
		"",
		"## Iteration 7 — second seven",
		"",
		u1,
		"",
		"---",
		"",
	}, "\n"))

	if _, err := eng.Rebuild(ctx, "dup-n"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	all, err := eng.Search(ctx, "seven", SearchFilters{Project: "dup-n", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	h0, ok0 := findHitContaining(all, u0)
	h1, ok1 := findHitContaining(all, u1)
	if !ok0 || !ok1 {
		t.Fatalf("missing hits: ok0=%v ok1=%v (results=%d)", ok0, ok1, len(all))
	}
	// No cache seeded for either match, so both surface only as raw rows.
	if h0.SourceRef != iterationRawSourceRef(7, 0) {
		t.Errorf("match0 SourceRef = %q, want %q", h0.SourceRef, iterationRawSourceRef(7, 0))
	}
	if h1.SourceRef != iterationRawSourceRef(7, 1) {
		t.Errorf("match1 SourceRef = %q, want %q", h1.SourceRef, iterationRawSourceRef(7, 1))
	}
	if h0.SourceRef == h1.SourceRef {
		t.Error("duplicate-N matches must not share SourceRef")
	}
}

// TestRebuild_IterationChunksShareSourceRefSoDedupKeepsOne: a long entry that
// sub-chunks must use one SourceRef so Search dedup returns a single hit.
func TestRebuild_IterationChunksShareSourceRefSoDedupKeepsOne(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	// Build a body large enough to force multiple 800-char chunks.
	var b strings.Builder
	b.WriteString("## Iteration 9 — long\n\n")
	phrase := "LONG_ITERATION_CHUNK_PHRASE "
	for b.Len() < 2500 {
		b.WriteString(phrase)
		b.WriteString("more text about the same topic. ")
	}
	b.WriteString("\n\n---\n")
	writeIterationsMD(t, v.Root, "chunky", b.String())

	ids, texts, metas, err := collectIterationCorpus(v, "chunky")
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(texts))
	}
	// No cache seeded for this project, so this entry surfaces only as raw
	// chunks (SourceType iteration_raw, SourceRef with the "/raw" suffix).
	ref := metas[0].SourceRef
	for i, m := range metas {
		if m.SourceRef != ref {
			t.Fatalf("chunk %d SourceRef = %q, want shared %q", i, m.SourceRef, ref)
		}
		if m.SourceType != iterationRawSourceType {
			t.Errorf("chunk %d SourceType = %q, want %q", i, m.SourceType, iterationRawSourceType)
		}
		if m.ChunkIndex != i {
			t.Errorf("chunk %d ChunkIndex = %d, want %d", i, m.ChunkIndex, i)
		}
		wantID := iterationRawCacheID("chunky", 9, 0, i)
		if ids[i] != wantID {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], wantID)
		}
	}

	if _, err := eng.Rebuild(ctx, "chunky"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	results, err := eng.Search(ctx, "LONG_ITERATION_CHUNK_PHRASE", SearchFilters{Project: "chunky", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("dedup should keep one hit per entry SourceRef, got %d", len(results))
	}
	if results[0].SourceRef != iterationRawSourceRef(9, 0) {
		t.Errorf("SourceRef = %q, want %q", results[0].SourceRef, iterationRawSourceRef(9, 0))
	}
}

func TestIterationCacheID_IncludesAllFourFields(t *testing.T) {
	id := iterationCacheID("vibe-palace", 315, 2, 4)
	if id != "iter.vibe-palace.315.m2.c4" {
		t.Fatalf("id = %q", id)
	}
	// Must not look like md5[:8] drawer ids.
	if len(id) == 8 {
		t.Fatal("iteration cache id must not be an 8-char md5 drawer id")
	}
}

func TestCollectIterationCorpus_MissingFileIsEmpty(t *testing.T) {
	_, v := testEngine(t)
	ids, texts, metas, err := collectIterationCorpus(v, "no-such-proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 || len(texts) != 0 || len(metas) != 0 {
		t.Fatalf("want empty, got ids=%d texts=%d metas=%d", len(ids), len(texts), len(metas))
	}
}

// metasByType partitions metas into (summary rows, raw rows) by SourceType,
// for the tests below that must reason about the two row families separately.
func metasByType(metas []drawerMeta) (summary, raw []drawerMeta) {
	for _, m := range metas {
		switch m.SourceType {
		case iterationSourceType:
			summary = append(summary, m)
		case iterationRawSourceType:
			raw = append(raw, m)
		}
	}
	return summary, raw
}

// TestCollectIterationCorpus_SummaryRowWhenCacheMatches covers the happy
// path: a fresh cache (MatchIndex equal to the entry's own current
// matchIndex) produces BOTH a summary row (prose-rendered, not raw JSON) and
// a raw row, with DISTINCT SourceRefs — the dedup-safety property engine.go's
// SourceRef-only dedup depends on.
func TestCollectIterationCorpus_SummaryRowWhenCacheMatches(t *testing.T) {
	_, v := testEngine(t)
	const project = "sum-ok"

	writeIterationsMD(t, v.Root, project, strings.Join([]string{
		"## Iteration 1 — first",
		"",
		"The raw narrative body for iteration one, never mentioned by the cached summary.",
		"",
		"---",
		"",
	}, "\n"))

	cached := storage.IterationSummary{
		N:           1,
		MatchIndex:  0,
		Summary:     "CACHED_SUMMARY_PROSE_MARKER: shipped the widget.",
		Decisions:   []string{"Decided to use approach A.", "Decided to defer approach B."},
		Unblocks:    "Unblocks the follow-on rollout task.",
		Model:       "test-model",
		GeneratedAt: "2026-01-01T00:00:00Z",
	}
	if err := v.WriteIterationSummary(project, cached); err != nil {
		t.Fatalf("WriteIterationSummary: %v", err)
	}

	_, _, metas, err := collectIterationCorpus(v, project)
	if err != nil {
		t.Fatal(err)
	}
	summaryRows, rawRows := metasByType(metas)

	if len(summaryRows) == 0 {
		t.Fatal("expected at least one summary row, got none")
	}
	if len(rawRows) == 0 {
		t.Fatal("expected at least one raw row, got none")
	}

	sm, ok := findHitContainingMeta(summaryRows, "CACHED_SUMMARY_PROSE_MARKER")
	if !ok {
		t.Fatalf("no summary row contains the cached summary text; rows=%+v", summaryRows)
	}
	if sm.Content == "The raw narrative body for iteration one, never mentioned by the cached summary." {
		t.Error("summary row Content must be the rendered summary, not the raw entry body")
	}
	if !strings.Contains(sm.Content, "Decided to use approach A.") {
		t.Errorf("summary row Content = %q, want it to include rendered Decisions", sm.Content)
	}
	if !strings.Contains(sm.Content, "Unblocks the follow-on rollout task.") {
		t.Errorf("summary row Content = %q, want it to include rendered Unblocks", sm.Content)
	}
	if sm.SourceRef != iterationSourceRef(1, 0) {
		t.Errorf("summary SourceRef = %q, want %q", sm.SourceRef, iterationSourceRef(1, 0))
	}

	rm, ok := findHitContainingMeta(rawRows, "The raw narrative body for iteration one")
	if !ok {
		t.Fatalf("no raw row contains the raw entry text; rows=%+v", rawRows)
	}
	if rm.SourceRef != iterationRawSourceRef(1, 0) {
		t.Errorf("raw SourceRef = %q, want %q", rm.SourceRef, iterationRawSourceRef(1, 0))
	}

	// Dedup-safety assertion: engine.go's dedup keys solely on SourceRef (no
	// SourceType involved) — if the raw row shared the summary row's ref,
	// dedup could silently keep whichever one scores higher and drop the
	// other. They must be distinct.
	t.Run("summary_and_raw_have_distinct_SourceRef", func(t *testing.T) {
		if sm.SourceRef == rm.SourceRef {
			t.Fatalf("summary and raw rows for the same entry must not share SourceRef, both got %q", sm.SourceRef)
		}
	})
}

// findHitContainingMeta is findHitContaining's drawerMeta counterpart.
func findHitContainingMeta(metas []drawerMeta, needle string) (drawerMeta, bool) {
	for _, m := range metas {
		if strings.Contains(m.Content, needle) {
			return m, true
		}
	}
	return drawerMeta{}, false
}

// TestCollectIterationCorpus_NoSummaryRowWhenUncached: an entry with no
// cache file at all must produce a raw row only — no summary row, and no
// error (ReadIterationSummary's not-found case is the expected common case).
func TestCollectIterationCorpus_NoSummaryRowWhenUncached(t *testing.T) {
	_, v := testEngine(t)
	const project = "sum-uncached"

	writeIterationsMD(t, v.Root, project, strings.Join([]string{
		"## Iteration 2 — second",
		"",
		"UNCACHED_RAW_BODY_MARKER narrative text.",
		"",
		"---",
		"",
	}, "\n"))

	_, _, metas, err := collectIterationCorpus(v, project)
	if err != nil {
		t.Fatal(err)
	}
	summaryRows, rawRows := metasByType(metas)

	if len(summaryRows) != 0 {
		t.Fatalf("expected no summary rows for an uncached entry, got %d", len(summaryRows))
	}
	if _, ok := findHitContainingMeta(rawRows, "UNCACHED_RAW_BODY_MARKER"); !ok {
		t.Fatalf("expected a raw row findable via raw text even before summarization; rows=%+v", rawRows)
	}
}

// TestCollectIterationCorpus_StaleCacheFallsBackToRawOnly simulates a newer
// same-N entry having been appended to iterations.md since a cache was
// generated: the stored MatchIndex (1) no longer equals the current
// computed matchIndex (0, since there's only one entry sharing N=5). A naive
// implementation that attaches any cache sharing N to the entry — without
// checking the stored MatchIndex — would wrongly emit a summary row here.
func TestCollectIterationCorpus_StaleCacheFallsBackToRawOnly(t *testing.T) {
	_, v := testEngine(t)
	const project = "sum-stale"

	writeIterationsMD(t, v.Root, project, strings.Join([]string{
		"## Iteration 5 — fifth",
		"",
		"STALE_CACHE_RAW_BODY_MARKER narrative text.",
		"",
		"---",
		"",
	}, "\n"))

	// Deliberate mismatch: only one entry shares N=5 (current computed
	// matchIndex = 0), but the cache claims it was generated against
	// matchIndex 1.
	stale := storage.IterationSummary{
		N:          5,
		MatchIndex: 1,
		Summary:    "STALE_SUMMARY_SHOULD_NOT_APPEAR",
	}
	if err := v.WriteIterationSummary(project, stale); err != nil {
		t.Fatalf("WriteIterationSummary: %v", err)
	}

	_, _, metas, err := collectIterationCorpus(v, project)
	if err != nil {
		t.Fatal(err)
	}
	summaryRows, rawRows := metasByType(metas)

	if len(summaryRows) != 0 {
		t.Fatalf("stale cache (MatchIndex mismatch) must not produce a summary row, got %d: %+v", len(summaryRows), summaryRows)
	}
	if _, ok := findHitContainingMeta(rawRows, "STALE_CACHE_RAW_BODY_MARKER"); !ok {
		t.Fatalf("expected raw-only fallback for the stale-cache entry; rows=%+v", rawRows)
	}
}

// TestCollectIterationCorpus_OnlyLastMatchGetsSummaryRow: two entries share
// N=9 (a legitimate duplicate). A cache exists with MatchIndex 1 (the last
// match's index). Only the LAST match (matchIndex 1) may get a summary row;
// the first, superseded match (matchIndex 0) must never get one even though
// it shares the same N as the cached entry — both still get their own raw
// rows regardless.
func TestCollectIterationCorpus_OnlyLastMatchGetsSummaryRow(t *testing.T) {
	_, v := testEngine(t)
	const project = "sum-multi"

	writeIterationsMD(t, v.Root, project, strings.Join([]string{
		"## Iteration 9 — first nine",
		"",
		"FIRST_NINE_RAW_MARKER narrative text.",
		"",
		"---",
		"",
		"## Iteration 9 — second nine",
		"",
		"SECOND_NINE_RAW_MARKER narrative text.",
		"",
		"---",
		"",
	}, "\n"))

	cached := storage.IterationSummary{
		N:          9,
		MatchIndex: 1,
		Summary:    "LAST_MATCH_SUMMARY_MARKER",
	}
	if err := v.WriteIterationSummary(project, cached); err != nil {
		t.Fatalf("WriteIterationSummary: %v", err)
	}

	_, _, metas, err := collectIterationCorpus(v, project)
	if err != nil {
		t.Fatal(err)
	}
	summaryRows, rawRows := metasByType(metas)

	if len(summaryRows) != 1 {
		t.Fatalf("expected exactly one summary row (only the last match), got %d: %+v", len(summaryRows), summaryRows)
	}
	if summaryRows[0].SourceRef != iterationSourceRef(9, 1) {
		t.Errorf("summary SourceRef = %q, want %q (matchIndex 1, the last match)", summaryRows[0].SourceRef, iterationSourceRef(9, 1))
	}
	if !strings.Contains(summaryRows[0].Content, "LAST_MATCH_SUMMARY_MARKER") {
		t.Errorf("summary Content = %q, want cached summary text", summaryRows[0].Content)
	}

	if _, ok := findHitContainingMeta(rawRows, "FIRST_NINE_RAW_MARKER"); !ok {
		t.Fatalf("expected a raw row for the first (superseded) match; rows=%+v", rawRows)
	}
	if _, ok := findHitContainingMeta(rawRows, "SECOND_NINE_RAW_MARKER"); !ok {
		t.Fatalf("expected a raw row for the second (last) match; rows=%+v", rawRows)
	}
	// The superseded first match must not have a summary row referencing it.
	for _, m := range summaryRows {
		if m.SourceRef == iterationSourceRef(9, 0) {
			t.Errorf("superseded match (matchIndex 0) must not get a summary row, got one at %q", m.SourceRef)
		}
	}
}

// TestCollectIterationCorpus_DegenerateCachedSummaryProducesNoSummaryRow is
// the collector-level regression test for the round's actual bug: a cached
// storage.IterationSummary can pass the "ok && MatchIndex == matchIndex"
// check yet carry Summary, Decisions and Unblocks ALL empty/nil.
// renderIterationSummary then returns "", chunk.Chunk on trimmed-empty input
// returns nil (0 parts), and the OLD fallback guard
// (strings.TrimSpace(rendered) != "") is ALSO false in that case — so sParts
// stays empty and the summary-row append loop never runs. The old code had no
// signal that this happened: cached.MatchIndex == matchIndex was true, but
// zero rows were actually appended. A raw row's SummaryAvailable must reflect
// ACTUAL emission, not that stale pre-check — so this test would still fail
// if an implementation set summaryEmitted from
// "ok && cached.MatchIndex == matchIndex" alone, dropping the
// len(sParts) > 0 gate.
func TestCollectIterationCorpus_DegenerateCachedSummaryProducesNoSummaryRow(t *testing.T) {
	_, v := testEngine(t)
	const project = "sum-degenerate"

	writeIterationsMD(t, v.Root, project, strings.Join([]string{
		"## Iteration 4 — fourth",
		"",
		"DEGENERATE_CACHE_RAW_BODY_MARKER narrative text.",
		"",
		"---",
		"",
	}, "\n"))

	// Passes ok && MatchIndex == matchIndex, but renders to an empty string.
	degenerate := storage.IterationSummary{
		N:          4,
		MatchIndex: 0,
		Summary:    "",
		Decisions:  nil,
		Unblocks:   "",
	}
	if err := v.WriteIterationSummary(project, degenerate); err != nil {
		t.Fatalf("WriteIterationSummary: %v", err)
	}

	_, _, metas, err := collectIterationCorpus(v, project)
	if err != nil {
		t.Fatal(err)
	}
	summaryRows, rawRows := metasByType(metas)

	if len(summaryRows) != 0 {
		t.Fatalf("a degenerate cached summary (all fields empty) must produce ZERO summary rows, got %d: %+v", len(summaryRows), summaryRows)
	}

	rm, ok := findHitContainingMeta(rawRows, "DEGENERATE_CACHE_RAW_BODY_MARKER")
	if !ok {
		t.Fatalf("expected the raw row to still be present and findable by its own content; rows=%+v", rawRows)
	}
	if rm.SummaryAvailable {
		t.Error("raw row SummaryAvailable = true, want false — no summary row was actually emitted")
	}
}
