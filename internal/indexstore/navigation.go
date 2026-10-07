// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Palace navigation reads the host-local store the same way search does
// (ADR-014 decision 7, "Visibility"): through the ledgered-source view,
// Store.Chunks(true). A wing or room shows only while it holds a chunk with a
// live owner — a ledgered archive, a ledgered import batch, or a note — so
// palace navigation never shows a chunk of a half-committed archive, a
// failure-only archive, or an unledgered import batch. These helpers are the
// one place that derives wings, rooms and navigable drawers from the store, so
// the graph, the list tools, the audit, tune and discover cannot disagree about
// a chunk's wing, room, hall or date (palace-navigation-over-the-host-local-
// chunk-store, Scope 1 and Design).

// Hits returns st's ledgered chunks as storage.DrawerHit values: the one
// mapping from a stored chunk to the drawer shape every palace-navigation
// surface renders. FiledAt is the selected owner's day (the session date),
// which is what the date filter and newest-first sort read.
func (s *Store) Hits() []storage.DrawerHit {
	chunks := s.Chunks(true)
	out := make([]storage.DrawerHit, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, storage.DrawerHit{
			Drawer: storage.Drawer{
				ID:         c.ID,
				Hall:       c.Hall,
				Content:    c.Content,
				SourceType: c.SourceType,
				SourceRef:  c.SourceRef,
				ChunkIndex: c.ChunkIndex,
				FiledAt:    c.FiledAt,
				AddedBy:    c.AddedBy,
			},
			Wing: c.Wing,
			Room: c.Room,
		})
	}
	return out
}

// ChunkIDs returns the set of st's ledgered chunk ids. It is the set the
// glide-path dedup matches a tracked drawer against: a store chunk id is
// index.ChunkID(content), so an id in this set means that exact content is
// already in the ledgered store.
func (s *Store) ChunkIDs() map[string]struct{} {
	ids := map[string]struct{}{}
	for _, c := range s.Chunks(true) {
		ids[c.ID] = struct{}{}
	}
	return ids
}

// GlideDedup reports whether content is already represented by a store chunk id
// in ids — the glide-path dedup rule (ADR-014 decision 7, "Chunk ids are wide
// content hashes", lines 718-724). The match is on the content hash ALONE
// (index.ChunkID): never the legacy 32-bit storage.DrawerID, which collides at
// corpus scale, and never the wing, so a reclassified copy is the same chunk.
//
// It is the shared helper the palace pre-marker fallback calls so that child
// does not own a private dedup. The search engine keeps its own inline copy of
// the same rule (internal/search/engine.go); the drift between the two is a
// flagged code-review item (see the task's "Dedup-helper placement — Chair
// decision 2026-10-06" section).
func GlideDedup(ids map[string]struct{}, content string) bool {
	_, ok := ids[index.ChunkID(content)]
	return ok
}
