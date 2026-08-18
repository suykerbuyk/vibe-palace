// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/chunk"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// Synthetic palace location for iteration corpus rows. Filters may use these;
// they are not a claim about real palace taxonomy.
const (
	iterationWing      = "history"
	iterationRoom      = "iterations"
	iterationHall      = "narrative"
	iterationSourceType = "iteration"
)

// iterationCacheID returns the deterministic vector/cache ID for one chunk of
// one iteration entry. Derived from (project, n, matchIndex, chunkIndex) — not
// DrawerID = md5(wing+content).
func iterationCacheID(project string, n, matchIndex, chunkIndex int) string {
	return fmt.Sprintf("iter.%s.%d.m%d.c%d", project, n, matchIndex, chunkIndex)
}

// iterationSourceRef is per entry (not per chunk): iteration/{n}/m/{matchIndex}.
// Chunk index lives on the cache ID and metadata.ChunkIndex so Search dedup
// keeps one hit per entry (highest-scoring chunk), not one per chunk.
func iterationSourceRef(n, matchIndex int) string {
	return fmt.Sprintf("iteration/%d/m/%d", n, matchIndex)
}

// collectIterationCorpus reads Projects/<project>/iterations.md, splits on
// wrapstate.ParseEntries, sub-chunks with chunk.DefaultChunkConfig (800/100),
// and returns parallel id / text / meta slices ready to merge into Rebuild.
// Missing iterations.md is not an error — empty slices.
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

	// matchIndex is 0-based among entries sharing the same N (file order).
	seenN := make(map[int]int, len(entries))
	cfg := chunk.DefaultChunkConfig()

	for _, e := range entries {
		matchIndex := seenN[e.N]
		seenN[e.N] = matchIndex + 1

		payload := e.Header
		if e.Body != "" {
			payload = e.Header + "\n\n" + e.Body
		}
		parts := chunk.Chunk(payload, cfg)
		if len(parts) == 0 {
			// Empty body after trim — still index the header alone.
			parts = []string{e.Header}
		}

		ref := iterationSourceRef(e.N, matchIndex)
		for cIdx, part := range parts {
			id := iterationCacheID(project, e.N, matchIndex, cIdx)
			ids = append(ids, id)
			texts = append(texts, part)
			metas = append(metas, drawerMeta{
				Project:    project,
				Wing:       iterationWing,
				Room:       iterationRoom,
				Hall:       iterationHall,
				SourceType: iterationSourceType,
				SourceRef:  ref,
				Date:       "",
				Content:    part,
				ChunkIndex: cIdx,
			})
		}
	}
	return ids, texts, metas, nil
}
