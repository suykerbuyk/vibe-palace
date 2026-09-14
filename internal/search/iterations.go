// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/chunk"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// Synthetic palace location for iteration corpus rows. Filters may use these;
// they are not a claim about real palace taxonomy.
const (
	iterationWing          = "history"
	iterationRoom          = "iterations"
	iterationHall          = "narrative"
	iterationSourceType    = "iteration"     // LLM-summary row (only the current last match of N, only when cached)
	iterationRawSourceType = "iteration_raw" // raw Header+Body row, emitted for every entry unconditionally
)

// iterationCacheID returns the deterministic vector/cache ID for one chunk of
// one iteration entry's SUMMARY row. Derived from (project, n, matchIndex,
// chunkIndex) — not DrawerID = md5(wing+content).
func iterationCacheID(project string, n, matchIndex, chunkIndex int) string {
	return fmt.Sprintf("iter.%s.%d.m%d.c%d", project, n, matchIndex, chunkIndex)
}

// iterationRawCacheID is iterationCacheID's counterpart for the always-emitted
// RAW row. It must never collide with iterationCacheID's output for the same
// (project, n, matchIndex, chunkIndex): Rebuild treats these ids as global
// vector-store/metadata keys (see Engine.detectCollision in engine.go), and a
// summary chunk sharing an id with a raw chunk would let one's cached vector
// and metadata silently answer for the other's (different) content.
func iterationRawCacheID(project string, n, matchIndex, chunkIndex int) string {
	return fmt.Sprintf("iter.%s.%d.m%d.raw.c%d", project, n, matchIndex, chunkIndex)
}

// iterationSourceRef is per entry (not per chunk): iteration/{n}/m/{matchIndex}.
// Chunk index lives on the cache ID and metadata.ChunkIndex so Search dedup
// keeps one hit per entry (highest-scoring chunk), not one per chunk.
func iterationSourceRef(n, matchIndex int) string {
	return fmt.Sprintf("iteration/%d/m/%d", n, matchIndex)
}

// iterationRawSourceRef is the raw row's own identity, distinct from the
// summary row's iterationSourceRef for the same entry. This matters beyond
// naming hygiene: Engine's dedup (internal/search/engine.go) keys solely on
// SourceRef — a bare map[string]bool with no SourceType involved — so a raw
// row sharing the summary row's SourceRef would let dedup silently pick
// whichever one scores higher for a given query, defeating the point of
// having a distinguishable summary-vs-raw identity.
func iterationRawSourceRef(n, matchIndex int) string {
	return iterationSourceRef(n, matchIndex) + "/raw"
}

// renderIterationSummary flattens a cached storage.IterationSummary into one
// coherent prose string suitable for embedding: the narrative Summary, then
// each Decisions item as its own line, then Unblocks — never the raw JSON
// blob the cache file stores on disk.
func renderIterationSummary(s storage.IterationSummary) string {
	var parts []string
	if s.Summary != "" {
		parts = append(parts, s.Summary)
	}
	for _, d := range s.Decisions {
		if d != "" {
			parts = append(parts, d)
		}
	}
	if s.Unblocks != "" {
		parts = append(parts, s.Unblocks)
	}
	return strings.Join(parts, "\n")
}

// collectIterationCorpus reads Projects/<project>/iterations.md, splits on
// wrapstate.ParseEntries, sub-chunks with chunk.DefaultChunkConfig (800/100),
// and returns parallel id / text / meta slices ready to merge into Rebuild.
// Missing iterations.md is not an error — empty slices.
//
// Per entry it emits up to two independent row families:
//
//   - A SUMMARY row (SourceType iterationSourceType), only when the entry IS
//     the current last file-order match for its N AND a fresh cached
//     storage.IterationSummary exists for that N (its MatchIndex still equals
//     the entry's own current matchIndex — otherwise the cache is stale,
//     superseded by a newer same-N entry appended since it was generated, and
//     is treated as absent).
//   - A RAW row (SourceType iterationRawSourceType), for every entry
//     unconditionally, chunking the entry's own Header+Body exactly as before
//     this summary/raw split existed.
//
// The two families never share a SourceRef or vector/cache id (see
// iterationRawSourceRef and iterationRawCacheID) — Search's dedup keys only on
// SourceRef, so a shared ref would make dedup arbitrarily pick one over the
// other, and a shared id would make Rebuild's global id-keyed metadata/vector
// cache silently answer one row's queries with the other's content.
func collectIterationCorpus(vault *storage.Vault, project string) (ids []string, texts []string, metas []drawerMeta, err error) {
	path, err := vault.IterationsFile(project)
	if err != nil {
		return nil, nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("read iterations.md: %w", err)
	}

	entries := wrapstate.ParseEntries(string(data))
	if len(entries) == 0 {
		return nil, nil, nil, nil
	}

	// totalCount[n] is how many entries in this file share iteration number
	// n, computed once so "is e the current last file-order match for its N"
	// is a map lookup against the entry's own matchIndex, not a re-parse of
	// the whole file per entry.
	totalCount := make(map[int]int, len(entries))
	for _, e := range entries {
		totalCount[e.N]++
	}

	// matchIndex is 0-based among entries sharing the same N (file order).
	seenN := make(map[int]int, len(entries))
	cfg := chunk.DefaultChunkConfig()

	for _, e := range entries {
		matchIndex := seenN[e.N]
		seenN[e.N] = matchIndex + 1
		isLastMatch := matchIndex == totalCount[e.N]-1

		// Summary row: only the entry that is the current last match for its
		// N may ever get one, and only when a non-stale cache exists.
		if isLastMatch {
			cached, ok, rerr := vault.ReadIterationSummary(project, e.N)
			if rerr != nil {
				return nil, nil, nil, fmt.Errorf("read iteration summary n=%d: %w", e.N, rerr)
			}
			if ok && cached.MatchIndex == matchIndex {
				rendered := renderIterationSummary(cached)
				sParts := chunk.Chunk(rendered, cfg)
				if len(sParts) == 0 && strings.TrimSpace(rendered) != "" {
					sParts = []string{rendered}
				}
				sRef := iterationSourceRef(e.N, matchIndex)
				for cIdx, part := range sParts {
					ids = append(ids, iterationCacheID(project, e.N, matchIndex, cIdx))
					texts = append(texts, part)
					metas = append(metas, drawerMeta{
						Project:    project,
						Wing:       iterationWing,
						Room:       iterationRoom,
						Hall:       iterationHall,
						SourceType: iterationSourceType,
						SourceRef:  sRef,
						Date:       "",
						Content:    part,
						ChunkIndex: cIdx,
					})
				}
			}
			// ok == false, or a stale cached.MatchIndex != matchIndex (a
			// newer same-N entry was appended since this cache was
			// generated): no summary row — falls through to the raw row
			// below exactly like an entry never summarized.
		}

		// Raw row: emitted for EVERY entry, unconditionally, with its own
		// distinct SourceType/SourceRef/id so it can never collide with (or
		// be dedup-shadowed by) that entry's summary row above.
		payload := e.Header
		if e.Body != "" {
			payload = e.Header + "\n\n" + e.Body
		}
		rParts := chunk.Chunk(payload, cfg)
		if len(rParts) == 0 {
			// Empty body after trim — still index the header alone.
			rParts = []string{e.Header}
		}

		rRef := iterationRawSourceRef(e.N, matchIndex)
		for cIdx, part := range rParts {
			ids = append(ids, iterationRawCacheID(project, e.N, matchIndex, cIdx))
			texts = append(texts, part)
			metas = append(metas, drawerMeta{
				Project:    project,
				Wing:       iterationWing,
				Room:       iterationRoom,
				Hall:       iterationHall,
				SourceType: iterationRawSourceType,
				SourceRef:  rRef,
				Date:       "",
				Content:    part,
				ChunkIndex: cIdx,
			})
		}
	}
	return ids, texts, metas, nil
}
