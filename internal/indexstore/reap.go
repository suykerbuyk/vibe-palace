// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Reap collects the project's orphans under the index commit lock (task
// host-local-index-store-ledger-and-fingerprint, "Orphan collection"; ADR-014
// decision 7). It returns the ids of the vectors it unlinked, sorted, so the
// caller can drop them from any in-memory index.
//
//  1. Owners the ledger records as replaced (a supersede's superseding_from,
//     or an archive recorded superseded) are dropped from every chunk and KG
//     record: those archives will never be ingested again. A pending archive's
//     owner is never dropped: an archive absent from the ledger, or the one a
//     session is superseding to, is an ingest in progress, which a rerun
//     completes without re-embedding. An unledgered batch is kept for the same
//     reason, and note owners are always live.
//  2. Chunk and KG records left with no owner of any kind are deleted.
//  3. Every vector in the project's embed cache whose id is in no live source
//     is unlinked. The live set is read here, under the lock: every chunk id
//     still in the store (ledgered or not) together with otherLive(), the ids
//     the caller's other sources hold (tracked drawers, notes, iterations).
//     search-index-completeness-and-build-serialization replaces otherLive
//     with the tier table's union.
//
// Every step that removes something is more than an append, so it changes
// the epoch; a reap that finds nothing to remove writes nothing, so a rebuild
// with nothing to collect does not make every running engine reload.
//
// Because the lock is held, no commit step is between writing a vector and
// writing the chunk that makes its id durable (the .vec rule), so no vector of
// a STORE chunk that is durable, or in flight, can be reaped. That guarantee
// covers store chunks only. Today the search engine's embed-cache Puts for
// drawers, notes and iterations happen outside the commit lock, so such a
// vector written after otherLive was read, for an id that is not yet in any
// source otherLive saw, can still be reaped in that narrow window; the cost is
// a re-embed. search-index-completeness-and-build-serialization closes it by
// moving those Puts under the commit lock.
//
// Lock order: the caller holds the index commit lock and may take its own
// in-process locks inside otherLive or afterwards, never the reverse. In the
// search engine that order is commit lock, then e.mu. The per-project
// in-process mutex of search-index-completeness-and-build-serialization is
// taken BEFORE the commit lock, so it must not be e.mu.
func (tx *Tx) Reap(otherLive func() (map[string]bool, error)) ([]string, error) {
	s, err := tx.state()
	if err != nil {
		return nil, err
	}
	var replaced []Owner
	for sha := range s.ledger.superseded {
		if _, live := s.ledger.liveDays[sha]; live {
			continue
		}
		replaced = append(replaced, ArchiveOwner(sha))
	}
	f, err := tx.readChunkFold()
	if err != nil {
		return nil, err
	}
	dropped := false
	for _, o := range replaced {
		if f.dropOwner(o, nil) {
			dropped = true
		}
	}
	if f.prune() {
		dropped = true
	}
	if dropped {
		if err := tx.fail(tx.writeChunkFold(s, f)); err != nil {
			return nil, err
		}
	}
	if len(replaced) > 0 {
		k, err := tx.readKGFold()
		if err != nil {
			return nil, err
		}
		kgDropped := false
		for _, o := range replaced {
			if k.dropOwner(o) {
				kgDropped = true
			}
		}
		if kgDropped {
			if err := tx.fail(tx.writeKGFold(s, k)); err != nil {
				return nil, err
			}
		}
	}

	live, err := otherLive()
	if err != nil {
		return nil, fmt.Errorf("indexstore: live set: %w", err)
	}
	dir, err := tx.vault.EmbedCacheDir(tx.project)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("indexstore: read %s: %w", dir, err)
	}
	var reaped []string
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".vec") {
			continue
		}
		id := strings.TrimSuffix(name, ".vec")
		if _, inStore := f.chunks[id]; inStore || live[id] {
			continue
		}
		if err := tx.fail(tx.beginDestructive()); err != nil {
			return nil, err
		}
		if err := tx.fail(removeIfExists(filepath.Join(dir, name))); err != nil {
			return nil, err
		}
		reaped = append(reaped, id)
	}
	slices.Sort(reaped)
	return reaped, nil
}
