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
)

// parseIterationSourceRef extracts n and matchIndex from a source_ref for assertions.
func parseIterationSourceRef(ref string) (n, matchIndex int, ok bool) {
	const prefix = "iteration/"
	if !strings.HasPrefix(ref, prefix) {
		return 0, 0, false
	}
	rest := strings.TrimPrefix(ref, prefix)
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

	if err := eng.Rebuild(ctx, "hist-only"); err != nil {
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
	if got.SourceType != iterationSourceType {
		t.Errorf("SourceType = %q, want %q", got.SourceType, iterationSourceType)
	}
	wantRef := iterationSourceRef(2, 0)
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
	if !strings.HasPrefix(got.DrawerID, "iter.hist-only.2.m0.c") {
		t.Errorf("DrawerID = %q, want iter.hist-only.2.m0.c*", got.DrawerID)
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

	if err := eng.Rebuild(ctx, "dup-n"); err != nil {
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
	if h0.SourceRef != iterationSourceRef(7, 0) {
		t.Errorf("match0 SourceRef = %q, want %q", h0.SourceRef, iterationSourceRef(7, 0))
	}
	if h1.SourceRef != iterationSourceRef(7, 1) {
		t.Errorf("match1 SourceRef = %q, want %q", h1.SourceRef, iterationSourceRef(7, 1))
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
	ref := metas[0].SourceRef
	for i, m := range metas {
		if m.SourceRef != ref {
			t.Fatalf("chunk %d SourceRef = %q, want shared %q", i, m.SourceRef, ref)
		}
		if m.ChunkIndex != i {
			t.Errorf("chunk %d ChunkIndex = %d, want %d", i, m.ChunkIndex, i)
		}
		wantID := iterationCacheID("chunky", 9, 0, i)
		if ids[i] != wantID {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], wantID)
		}
	}

	if err := eng.Rebuild(ctx, "chunky"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	results, err := eng.Search(ctx, "LONG_ITERATION_CHUNK_PHRASE", SearchFilters{Project: "chunky", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("dedup should keep one hit per entry SourceRef, got %d", len(results))
	}
	if results[0].SourceRef != iterationSourceRef(9, 0) {
		t.Errorf("SourceRef = %q, want %q", results[0].SourceRef, iterationSourceRef(9, 0))
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
