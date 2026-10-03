// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DiscardKind names what a discard removes.
type DiscardKind int

const (
	// DiscardChunks removes the project's chunks, ledger, local KG and HNSW
	// graph, and recreates the ledger with a fresh baseline set. The vectors
	// and the lock files are untouched.
	DiscardChunks DiscardKind = iota + 1
	// DiscardVectors removes the project's vectors. Chunks, ledger and KG are
	// untouched; the rebuild re-embeds. The embed cache's fingerprint sidecar
	// is left alone: an embed cache in a running process remembers that it
	// checked the sidecar and would not write it again, so a fresh process
	// would treat every new vector as stale. A changed embedding regime is
	// already handled by the cache's own sidecar mismatch, which wipes the
	// vectors.
	DiscardVectors
)

// Discard is the mechanism of a discard (ADR-014 decision 3). Which
// fingerprint mismatch calls which kind is
// index-fingerprints-project-lifecycle-and-migration-marker's; the only
// caller is the rebuild driver of
// explicit-resumable-index-rebuild-with-disk-watchdog. Either kind changes the
// epoch.
func (tx *Tx) Discard(kind DiscardKind) error {
	switch kind {
	case DiscardChunks:
		return tx.fail(tx.discardChunks())
	case DiscardVectors:
		return tx.fail(tx.discardVectors())
	}
	return fmt.Errorf("indexstore: unknown discard kind %d", kind)
}

// discardChunks removes the ledger FIRST. A discard killed part-way then
// leaves no ledger at all: the next commit step finds ErrNoLedger and the
// caller recreates it, and the chunks and KG records still on disk have no
// live owner, so nothing loads. Removed in any other order, a kill could leave
// a ledger that says an archive is ingested over a store that no longer holds
// its chunks, and search would answer from nothing without a word.
func (tx *Tx) discardChunks() error {
	if err := tx.beginDestructive(); err != nil {
		return err
	}
	for _, step := range []struct{ name, path string }{
		{"discard-ledger", tx.files.ledger},
		{"discard-chunks", tx.files.chunks},
		{"discard-kg", tx.files.kg},
		{"discard-graph", tx.files.graph},
	} {
		if err := removeIfExists(step.path); err != nil {
			return err
		}
		if err := commitStep(step.name); err != nil {
			return err
		}
	}
	s := &state{gen: tx.lockGen}
	s.indexChunks(newChunkFold())
	s.indexKG(&kgFold{})
	s.ledger = foldLedger(nil)
	tx.st = s
	rec, err := tx.freshBaseline(nil)
	if err != nil {
		return err
	}
	return tx.appendLedger(s, rec)
}

func (tx *Tx) discardVectors() error {
	dir, err := tx.vault.EmbedCacheDir(tx.project)
	if err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("indexstore: read %s: %w", dir, err)
	}
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".vec") {
			continue
		}
		if err := tx.beginDestructive(); err != nil {
			return err
		}
		if err := removeIfExists(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// WriteGraph replaces the project's HNSW graph, hnsw.idx, atomically (temp
// file, fsync, rename, directory fsync). The envelope and when to write are
// hnsw-graph-file-envelope-and-warm-start's. A graph write changes no chunk,
// ledger or KG record, so it bumps the change counter's gen and leaves its
// epoch alone: an engine's in-memory graph stays valid.
func (tx *Tx) WriteGraph(data []byte) error {
	if err := tx.fail(writeFile(tx.files.graph, data)); err != nil {
		return err
	}
	tx.noteWrite(false)
	return nil
}

// DeleteGraph removes hnsw.idx (a corrupt graph, or one whose fingerprint no
// longer matches). Like WriteGraph it bumps gen only. Deleting a graph that is
// not there writes nothing.
func (tx *Tx) DeleteGraph() error {
	err := removeFile(tx.files.graph)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := tx.fail(err); err != nil {
		return err
	}
	tx.noteWrite(false)
	return nil
}
