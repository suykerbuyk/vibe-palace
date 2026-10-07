// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ErrRelabelBeforeMarker is returned by Relabel when the vault carries no
// migration marker. Room relabelling writes the host-local chunk store, which
// is authoritative only after the migration to the authored-only layout
// (ADR-014 decision 9); before it, `vp audit rooms --apply` refuses rather than
// rewriting tracked drawers, which this child no longer does.
var ErrRelabelBeforeMarker = errors.New("palace: room relabelling is available only after the vault migration to the authored-only layout")

// Relabel applies room moves to the host-local chunk store (ADR-014 decision 9,
// 9-S2). It is a thin caller of the store child's write path:
//
//	indexstore.Lock → Tx.Rewrite → Tx.Commit
//
// It adds no lock, no path and no write of its own — the store child's "no
// exported write outside Tx" rule holds, and the relabel is one commit step. It
// takes the project's index commit lock (over vaultlock.AcquireFileWithTimeout)
// and NEVER the index run lock: the commit lock is a leaf and the run lock is
// only ever taken before a commit lock, so a relabel runs while an ingest or a
// `vp index rebuild` is in progress and waits only for the commit step in
// flight (ADR-014 decision 7, "Two locks", lines 646-647).
//
// On a lock timeout it writes nothing and returns the error wrapping
// vaultlock.ErrLockWaitTimeout, naming the busy lock: an explicit command fails
// rather than skipping silently. A store chunk id is index.ChunkID(content), a
// hash of the content alone, so a relabel changes no id, keeps every
// embed-cache vector and keeps each chunk's owner set (keyed by source_sha256).
// The commit bumps the store's change counter with a new epoch, so a running
// engine does a full reload and sees the move on its next search; that reload
// rule is child 2's and this function adds nothing to it. A later `vp index
// rebuild` re-runs the classifier and drops the relabel (decision 9).
func Relabel(ctx context.Context, vault *storage.Vault, project string, moves []MoveCandidate, timeout time.Duration) error {
	migrated, err := storage.VaultMigrated(vault.Root)
	if err != nil {
		return fmt.Errorf("read migration marker: %w", err)
	}
	if !migrated {
		return ErrRelabelBeforeMarker
	}
	if len(moves) == 0 {
		return nil
	}

	// Ids are content hashes, so a move changes only the room; Wing is left
	// empty, which Tx.Rewrite reads as "leave the wing as it is".
	changes := make(map[string]indexstore.Labels, len(moves))
	for _, m := range moves {
		changes[m.DrawerID] = indexstore.Labels{Room: m.ToRoom}
	}

	tx, err := indexstore.Lock(ctx, vault, project, timeout)
	if err != nil {
		return fmt.Errorf("acquire index commit lock for %s: %w", project, err)
	}
	defer tx.Release()

	if err := tx.Rewrite(changes); err != nil {
		return fmt.Errorf("relabel rooms in %s: %w", project, err)
	}
	return tx.Commit()
}
