// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package index holds the identities the host-local search index is keyed on
// (ADR-014): the indexer version, the chunk recipe that the chunk fingerprint
// records, and the chunk id.
//
// It is a leaf. It must never import a package that imports internal/capture
// or internal/search (capture imports search), because both of them, and the
// host-local store in internal/indexstore, depend on it.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// IndexerVersion is the version of the code that turns an archive into chunks
// and KG records. A change to the chunker, the classifier's use or the
// extractor that changes what is stored bumps it; the chunk fingerprint hashes
// it, so a bump makes every host's store stale (ADR-014 decision 3).
const IndexerVersion = 1

// chunkIDBytes is the width of a chunk id: 128 bits. ADR-014 ("Chunk ids are
// wide content hashes") requires at least 128 so that ids do not collide at the
// corpus sizes the epic measured, where the 32-bit storage.DrawerID does.
const chunkIDBytes = 16

// ChunkID returns the id of a chunk in the host-local store: the first 128
// bits of sha256(content), hex-encoded (32 characters).
//
// It hashes the content ALONE. Wing and room are classification metadata, so
// neither a reclassification nor a project rename changes an id, and equal ids
// mean equal content. Legacy tracked drawers keep their storage.DrawerID; the
// two are never compared.
func ChunkID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:chunkIDBytes])
}

// ChunkSettings are the chunk size and overlap a chunker uses, after
// chunk.ChunkConfig.Normalized. They are plain ints so this leaf need not import
// internal/chunk.
type ChunkSettings struct {
	MaxChars int
	Overlap  int
}

// TranscriptRecipe is what the transcript writer reads when it turns an archive
// into chunks and KG records: the normalised effective chunk settings, the
// digest of the room classifier it scores with, the custom room keywords it
// consults first, and the extractor version.
type TranscriptRecipe struct {
	MaxChars           int
	Overlap            int
	ClassifierDigest   string
	CustomRoomKeywords map[string][]string
	ExtractorVersion   int
}

// ChunkRecipe is what the chunk fingerprint (chunks.fingerprint) covers: one
// part per chunk kind, each holding exactly what that kind's writer reads
// (ADR-014 decision 3). A part that hashed a setting its chunker ignores would
// mark a store stale for a change that alters nothing; a part that missed one
// would let a stale store pass. A chunk kind added later adds its own part in
// the change that adds its writer.
//
// palace.ProjectIndexing assembles it; the store compares and records it and
// never resolves config itself.
type ChunkRecipe struct {
	IndexerVersion int
	Transcript     TranscriptRecipe
	// Iteration and Note are what the iteration and note chunkers read. Both
	// read the compiled default today and use fixed rooms, so no config and no
	// classifier input enters them.
	Iteration ChunkSettings
	Note      ChunkSettings
}

// Sum returns the recipe's canonical, human-readable encoding: the parts in a
// fixed order, room keywords sorted by room and by keyword, every free-form
// string quoted. It is what chunks.fingerprint holds, so two recipes are equal
// exactly when their Sums are.
func (r ChunkRecipe) Sum() string {
	var b strings.Builder
	fmt.Fprintf(&b, "vp-chunks indexer=%d\n", r.IndexerVersion)
	t := r.Transcript
	fmt.Fprintf(&b, "transcript max_chars=%d overlap=%d extractor=%d classifier=%s\n",
		t.MaxChars, t.Overlap, t.ExtractorVersion, strconv.Quote(t.ClassifierDigest))
	rooms := make([]string, 0, len(t.CustomRoomKeywords))
	for room := range t.CustomRoomKeywords {
		rooms = append(rooms, room)
	}
	sort.Strings(rooms)
	for _, room := range rooms {
		kws := append([]string(nil), t.CustomRoomKeywords[room]...)
		sort.Strings(kws)
		quoted := make([]string, len(kws))
		for i, kw := range kws {
			quoted[i] = strconv.Quote(kw)
		}
		fmt.Fprintf(&b, "transcript room_keywords %s=%s\n", strconv.Quote(room), strings.Join(quoted, ","))
	}
	fmt.Fprintf(&b, "iteration max_chars=%d overlap=%d\n", r.Iteration.MaxChars, r.Iteration.Overlap)
	fmt.Fprintf(&b, "note max_chars=%d overlap=%d\n", r.Note.MaxChars, r.Note.Overlap)
	return b.String()
}
