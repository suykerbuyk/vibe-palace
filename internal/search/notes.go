// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/chunk"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Synthetic palace location for session-note corpus rows. Filters may use
// these; they are not a claim about real palace taxonomy. The wing/room pair
// is deliberately NOT a project slug: internal/palace.DetectWing returns the
// PROJECT SLUG as the wing for every transcript-derived drawer, so a synthetic
// wing named after a project would land note rows on top of that project's real
// wing. "history" is the same synthetic wing the iteration corpus already uses;
// the room separates the two.
//
// noteSourceType is distinct from the transcript corpus's "session"
// (internal/capture/indexer.go) ON PURPOSE — a reader looking at a search hit
// must be able to tell "this came from the wrap NOTE" from "this came from the
// TRANSCRIPT" at a glance, because those are different evidence with different
// provenance.
const (
	noteWing       = "history"
	noteRoom       = "session-notes"
	noteHall       = "narrative"
	noteSourceType = "session-note"
)

// noteCacheID returns the deterministic vector/cache ID for one chunk of one
// session note. Derived from (project, note file stem, chunkIndex) — not
// DrawerID = md5(wing+content).
//
// That choice is the whole reason a note chunk cannot collide with a transcript
// chunk: DrawerID hashes CONTENT, so identical text anywhere in the vault lands
// on one key, while this ID keys on IDENTITY, so it can only ever collide with
// itself. The honest cost is the mirror image: two notes carrying the same text
// produce two separate index rows, and a note that restates a transcript is
// indexed twice. The note corpus is ADDITIVE and does not dedup against
// transcripts. That is deliberate — a false merge is a wrong answer, a
// duplicate is a redundant one.
//
// The stem comes from a filename under Projects/<p>/sessions, so it can carry
// no path separator; it is embedded between a fixed prefix and suffix, so it
// cannot escape the embed-cache directory either.
func noteCacheID(project, stem string, chunkIndex int) string {
	return fmt.Sprintf("note.%s.%s.c%d", project, stem, chunkIndex)
}

// noteSourceRef is per note (not per chunk): the note's path relative to the
// project directory, so a reader can navigate straight back to the file.
// Chunk index lives on the cache ID and metadata.ChunkIndex so Search dedup
// keeps one hit per note (highest-scoring chunk), not one per chunk.
func noteSourceRef(stem string) string {
	return fmt.Sprintf("sessions/%s.md", stem)
}

// collectNoteCorpus globs Projects/<project>/sessions/*.md, splits each file
// with storage.ParseFrontmatter, sub-chunks the BODY with
// chunk.DefaultChunkConfig (800/100), and returns parallel id / text / meta
// slices ready to merge into Rebuild. A missing sessions directory is not an
// error — empty slices.
//
// Only the body is indexed. The frontmatter is already served verbatim by
// vp_search_sessions, which reads it off disk and never goes through the vector
// index, so duplicating it here would add noise to every query without making
// anything newly reachable.
//
// This is the wrap-note corpus, and it writes NOTHING: no drawers.jsonl, no
// DrawerID, and — because it never touches internal/capture.IndexTranscript —
// no knowledge-graph extraction over wrap prose. The absence of a KG write is
// STRUCTURAL, not a flag: extractEntities is simply not on this path.
//
// Error convention, matching collectIterationCorpus: a missing directory is
// empty-and-nil, an I/O failure reading a file fails the whole rebuild (that is
// what iterations does with os.ReadFile), and content that will not parse
// contributes nothing without failing (that is what iterations does when
// ParseEntries yields no entries).
func collectNoteCorpus(vault *storage.Vault, project string) (ids []string, texts []string, metas []drawerMeta, err error) {
	dir, err := vault.SessionDir(project)
	if err != nil {
		return nil, nil, nil, err
	}
	// Glob returns matches in sorted order, so the corpus is deterministic.
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("glob session notes: %w", err)
	}
	if len(paths) == 0 {
		return nil, nil, nil, nil
	}

	cfg := chunk.DefaultChunkConfig()

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// Raced with a delete between Glob and read.
				continue
			}
			return nil, nil, nil, fmt.Errorf("read session note %s: %w", filepath.Base(path), err)
		}

		meta, body, err := storage.ParseFrontmatter(data)
		if err != nil {
			slog.Warn("note corpus: skipping unparseable session note",
				"project", project, "note", filepath.Base(path), "err", err)
			continue
		}
		if strings.TrimSpace(body) == "" {
			// An empty chunk in the index is a hit with no content.
			continue
		}

		parts := chunk.Chunk(body, cfg)
		if len(parts) == 0 {
			continue
		}

		stem := strings.TrimSuffix(filepath.Base(path), ".md")
		ref := noteSourceRef(stem)
		date := meta.Date
		if len(date) >= 10 {
			date = date[:10]
		}

		for cIdx, part := range parts {
			ids = append(ids, noteCacheID(project, stem, cIdx))
			texts = append(texts, part)
			metas = append(metas, drawerMeta{
				Project:    project,
				Wing:       noteWing,
				Room:       noteRoom,
				Hall:       noteHall,
				SourceType: noteSourceType,
				SourceRef:  ref,
				Date:       date,
				Content:    part,
				ChunkIndex: cIdx,
			})
		}
	}
	return ids, texts, metas, nil
}
