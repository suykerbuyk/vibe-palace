// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// glideDedup is the glide-path dedup seam. It is a package var so a test can
// spy that the pre-marker fallback dedups through the shared helper rather than
// a private copy owned by this child. The default is indexstore.GlideDedup —
// the ChunkID-based helper placed in the store package by the Chair's
// 2026-10-06 tie-break (Option A); see the task's "Dedup-helper placement"
// section. It is never a private dedup in this package.
var glideDedup = indexstore.GlideDedup

// visibleDrawers is THE reader every palace-navigation surface uses — the
// graph, the list tools, the room audit, tune and discover — so none of them
// can disagree about a chunk's wing, room, hall or date (Scope 1, Design).
//
// It returns the project's drawers as the store and the pre-marker fallback
// make them visible:
//   - the host-local store's ledgered chunks always (indexstore Store.Hits over
//     the ledgered-source view, so a chunk of a half-committed or failure-only
//     archive, or of an unledgered import batch, is never shown);
//   - before the migration marker, the tracked drawers too (ADR-014 decision 7,
//     "Unmigrated vaults", lines 510-518), each skipped when its content is
//     already a store chunk — the glide-path dedup by index.ChunkID(content)
//     alone, through the shared helper, never the 32-bit drawer id and never the
//     wing, so the store's record of a reclassified copy wins.
//
// The result is sorted by wing, room, then id, so every caller reads a
// deterministic order regardless of ingest or directory-walk order.
func visibleDrawers(vault *storage.Vault, project string) ([]storage.DrawerHit, error) {
	st, err := indexstore.ReadStore(vault, project)
	if err != nil {
		return nil, fmt.Errorf("read index store: %w", err)
	}
	hits := st.Hits()

	migrated, err := storage.VaultMigrated(vault.Root)
	if err != nil {
		return nil, fmt.Errorf("read migration marker: %w", err)
	}
	if !migrated {
		fallback, err := trackedDrawerFallback(vault, project, st.ChunkIDs())
		if err != nil {
			return nil, err
		}
		hits = append(hits, fallback...)
	}

	sortDrawerHits(hits)
	return hits, nil
}

// trackedDrawerFallback reads the tracked drawers of project and returns those
// whose content is not already a ledgered store chunk. It is reached only while
// the vault carries no migration marker. storeIDs is the ledgered store's chunk
// id set; a drawer is skipped when glideDedup says its content is already there.
func trackedDrawerFallback(vault *storage.Vault, project string, storeIDs map[string]struct{}) ([]storage.DrawerHit, error) {
	wings, err := vault.ListWings(project)
	if err != nil {
		return nil, fmt.Errorf("list wings: %w", err)
	}
	var out []storage.DrawerHit
	for _, wing := range wings {
		rooms, err := vault.ListRooms(project, wing)
		if err != nil {
			return nil, fmt.Errorf("list rooms for wing %q: %w", wing, err)
		}
		for _, room := range rooms {
			drawers, err := vault.ListDrawers(project, wing, room)
			if err != nil {
				return nil, fmt.Errorf("list drawers for %s/%s: %w", wing, room, err)
			}
			for _, d := range drawers {
				if glideDedup(storeIDs, d.Content) {
					continue
				}
				out = append(out, storage.DrawerHit{Drawer: d, Wing: wing, Room: room})
			}
		}
	}
	return out, nil
}

// QueryDrawers runs vp_palace_query's filter over the host-local chunk store
// (with the pre-marker tracked-drawer fallback), newest first. It is the store
// twin of storage.ScanDrawers (Scope 2): it takes the same already-resolved
// storage.DrawerQuery and applies the SAME exact-match / substring / inclusive-
// day predicates, so the tool's filter semantics do not change when the scan
// moves from tracked drawers to the store. Like ScanDrawers it applies NO
// defaults of its own — the caller's resolver (tools.ResolvePalaceQuery) owns
// every default, the limit clamp and the default-room prune.
//
// filed_at is the chunk's session date (StoredChunk.FiledAt): the UTC day of
// the session's start for a transcript chunk, and the ledger's start day of a
// decision chunk's session (else the note's own day) — which is why the
// date_from/date_to wording moved to this tool (A1/cap-N3).
func QueryDrawers(vault *storage.Vault, q storage.DrawerQuery) ([]storage.DrawerHit, error) {
	if q.Project == "" {
		return nil, errors.New("project is required")
	}
	hits, err := visibleDrawers(vault, q.Project)
	if err != nil {
		return nil, err
	}

	needle := strings.ToLower(q.Q)
	var sourceTypes map[string]struct{}
	if len(q.SourceTypes) > 0 {
		sourceTypes = make(map[string]struct{}, len(q.SourceTypes))
		for _, st := range q.SourceTypes {
			sourceTypes[st] = struct{}{}
		}
	}

	var out []storage.DrawerHit
	for _, h := range hits {
		if q.Wing != "" && h.Wing != q.Wing {
			continue
		}
		if q.Room != "" && h.Room != q.Room {
			continue
		}
		if q.Hall != "" && h.Hall != q.Hall {
			continue
		}
		if sourceTypes != nil {
			if _, ok := sourceTypes[h.SourceType]; !ok {
				continue
			}
		}
		if needle != "" && !strings.Contains(strings.ToLower(h.Content), needle) {
			continue
		}
		if !matchesFiledAtDay(h.FiledAt, q.DateFrom, q.DateTo) {
			continue
		}
		out = append(out, h)
	}

	// Newest first; id ascending breaks a same-day tie for a stable order.
	// FiledAt is a day (YYYY-MM-DD) or an RFC3339 stamp; a lexical compare is
	// chronological for both.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FiledAt != out[j].FiledAt {
			return out[i].FiledAt > out[j].FiledAt
		}
		return out[i].ID < out[j].ID
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// matchesFiledAtDay applies the inclusive [from, to] day window, truncating a
// filed_at to its 10-byte YYYY-MM-DD prefix first (the same rule
// storage.matchesDateRange applies), so a chunk whose filed_at is a full
// RFC3339 stamp is still returned on the closing day.
func matchesFiledAtDay(filedAt, from, to string) bool {
	if from == "" && to == "" {
		return true
	}
	date := filedAt
	if len(date) >= 10 {
		date = date[:10]
	}
	if from != "" && date < from {
		return false
	}
	if to != "" && date > to {
		return false
	}
	return true
}

// sortDrawerHits orders hits by wing, then room, then id: a stable, filesystem-
// and ingest-order-independent order for the fixture-equivalence acceptance
// (9-N2) and for deterministic graph and tool output.
func sortDrawerHits(hits []storage.DrawerHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Wing != hits[j].Wing {
			return hits[i].Wing < hits[j].Wing
		}
		if hits[i].Room != hits[j].Room {
			return hits[i].Room < hits[j].Room
		}
		return hits[i].ID < hits[j].ID
	})
}
