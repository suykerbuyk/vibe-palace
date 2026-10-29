// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/chunk"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// commitlog.go is the FIFTH corpus source (ADR-014 tier table): the authored,
// tracked Projects/<p>/commit-log.md append-log, split per `## commit <sha>`
// entry. Like iterations.md and session-note bodies it is a search-path corpus
// read from the vault at build/walk time — NOT a host-local store chunk, NOT an
// archive, NOT in the index ledger. It rides the same lazy embed-cache path the
// notes/iterations tiers use (walkCorpus → cachedVector → embed on miss), so it
// adds NO chunks.fingerprint part, NO ChunkRecipe part, and forces no rebuild;
// appending one commit embeds only that commit's chunks (the cache id carries
// the content tag).
const (
	// commitWing/commitHall reuse the iteration tier's classification
	// (iterations.go); commitRoom distinguishes it. These are classification
	// metadata on local chunks (ADR-014: rooms/halls are metadata, not git
	// directories), not a taxonomy directory anything creates.
	commitWing       = "history"
	commitRoom       = "commits"
	commitHall       = "narrative"
	commitSourceType = "commit"
)

// commitSourceRef is per COMMIT (one SHA), not per chunk, so Search's dedup
// (engine.go dedup: highest-scored result per SourceRef) collapses a
// multi-chunk commit message to one hit, and the caller can `git show <sha>`.
// A commit message has no LLM-summary tier, so there is exactly ONE row family
// per commit — no raw/summary SourceRef split, and therefore no intra-commit
// ref-collision concern.
func commitSourceRef(sha string) string {
	return "commit/" + sha
}

// commitCacheID is the deterministic vector/cache id for one chunk of one
// commit entry. Derived from (project, sha, chunkIndex) and the chunk's text
// (contentTag, notes.go) — not DrawerID = md5(wing+content). The text is part
// of the id so a rewritten commit body (e.g. a hand-edited log) re-embeds
// instead of serving the old text's vector. The "commit." prefix is disjoint
// from "iter." (iterations.go) and "note." (notes.go), so a commit chunk id can
// never collide with an iteration or note chunk id for any project/sha/text.
func commitCacheID(project, sha string, chunkIndex int, text string) string {
	return fmt.Sprintf("commit.%s.%s.c%d.%s", project, sha, chunkIndex, contentTag(text))
}

// collectCommitLogCorpus reads Projects/<p>/commit-log.md, splits it per
// `## commit <sha>` entry (storage.ParseCommitLog — the ONE source of truth for
// the line-start header rule, shared with the writer's archivedSHAs), sub-chunks
// each message body with chunk.DefaultChunkConfig (800/100), and returns
// parallel id / text / meta slices — the same contract collectIterationCorpus
// and collectNoteCorpus satisfy, so walkCorpus, gatherCachedCorpus and liveSet
// consume it with no special-casing.
//
// A missing commit-log.md is EMPTY, not an error (a project may have notes but
// no archived commits). A header with an empty body still indexes one chunk (the
// header line), so the commit is findable by its SHA.
//
// 🔴 Date is left EMPTY, matching the iteration corpus (iterations.go): the
// commit-log format carries no structured per-entry date, and guessing one from
// the message would mis-file commits. CONSEQUENCE: a search that sets
// date_from/date_to HIDES every commit hit, because matchesFilters (engine.go)
// drops a hit whose Date is empty under any date bound ("" < from and "" > to
// are both true). This is the same behaviour iteration hits have; revisit only
// if the writer gains a structured date line.
func collectCommitLogCorpus(vault *storage.Vault, project string) (ids []string, texts []string, metas []drawerMeta, err error) {
	path, err := vault.CommitLogFile(project)
	if err != nil {
		return nil, nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("read commit-log.md: %w", err)
	}

	entries := storage.ParseCommitLog(string(data))
	if len(entries) == 0 {
		return nil, nil, nil, nil
	}

	cfg := chunk.DefaultChunkConfig()
	for _, e := range entries {
		parts := chunk.Chunk(e.Body, cfg)
		if len(parts) == 0 {
			// Empty or whitespace-only body: still index the entry by its SHA
			// header so `vp search` can find the commit by its hash.
			parts = []string{"commit " + e.SHA}
		}
		ref := commitSourceRef(e.SHA)
		for cIdx, part := range parts {
			ids = append(ids, commitCacheID(project, e.SHA, cIdx, part))
			texts = append(texts, part)
			metas = append(metas, drawerMeta{
				Project:    project,
				Wing:       commitWing,
				Room:       commitRoom,
				Hall:       commitHall,
				SourceType: commitSourceType,
				SourceRef:  ref,
				Date:       "",
				Content:    part,
				ChunkIndex: cIdx,
			})
		}
	}
	return ids, texts, metas, nil
}
