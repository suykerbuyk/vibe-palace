// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// fingerprintFile records the chunk recipe a project's store was built with,
// index.ChunkRecipe.Sum (ADR-014 decision 3).
const fingerprintFile = "chunks.fingerprint"

// ErrNoRecipe is returned by a chunk-writing step on a project whose store has
// no chunks.fingerprint yet, and by Discard(DiscardChunks), when the Tx was given
// no recipe (Tx.UseRecipe). Writing chunks without recording how they were made
// would leave a store whose staleness nothing can ever judge.
var ErrNoRecipe = errors.New("indexstore: no chunk recipe set: call Tx.UseRecipe before writing chunks")

// FingerprintStatus is how a project's chunks.fingerprint compares with a
// recipe. There are three states, never two: a missing file means the store was
// never built (unbuilt), never that it is stale.
type FingerprintStatus int

const (
	// FingerprintMissing: no chunks.fingerprint. The store is unbuilt.
	FingerprintMissing FingerprintStatus = iota
	// FingerprintMatch: the store was built with this recipe.
	FingerprintMatch
	// FingerprintMismatch: the store was built with another recipe. Nothing is
	// deleted; only vp index rebuild discards (Discard(DiscardChunks)).
	FingerprintMismatch
)

func (s FingerprintStatus) String() string {
	switch s {
	case FingerprintMissing:
		return "missing"
	case FingerprintMatch:
		return "match"
	case FingerprintMismatch:
		return "mismatch"
	}
	return fmt.Sprintf("FingerprintStatus(%d)", int(s))
}

// ReadFingerprint compares project's chunks.fingerprint with r. It takes no
// lock, like ReadGeneration: the file is only ever replaced whole.
func ReadFingerprint(vault *storage.Vault, project string, r index.ChunkRecipe) (FingerprintStatus, error) {
	pf, err := filesFor(vault, project)
	if err != nil {
		return 0, err
	}
	return fingerprintStatus(pf.fingerprint, r)
}

func fingerprintStatus(path string, r index.ChunkRecipe) (FingerprintStatus, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return FingerprintMissing, nil
	}
	if err != nil {
		return 0, fmt.Errorf("indexstore: read %s: %w", path, err)
	}
	if bytes.Equal(data, []byte(r.Sum())) {
		return FingerprintMatch, nil
	}
	return FingerprintMismatch, nil
}

// UseRecipe sets the recipe this Tx records in chunks.fingerprint. It is a
// precondition a writer sets before any chunk-writing step, as EnsureLedger is:
// the first chunk-writing step on a project with no chunks.fingerprint writes it
// from this recipe, and Discard(DiscardChunks) writes it last. A Tx that writes
// no chunks (a reap, a graph write) needs none.
//
// The recipe must come from palace.ProjectIndexing, the one assembler, so the
// writer's classifier and chunk settings and the recorded recipe cannot differ.
func (tx *Tx) UseRecipe(r index.ChunkRecipe) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.recipe = &r
}

// ensureChunkFingerprint runs before a chunk-writing step writes anything. When
// the project has no chunks.fingerprint, it writes one from the Tx's recipe,
// atomically, before the step's chunk lines; with no recipe it returns
// ErrNoRecipe and nothing is written. A fingerprint that exists is never
// rewritten here, matching or not: only a discard replaces it.
func (tx *Tx) ensureChunkFingerprint() error {
	tx.mu.Lock()
	r, checked := tx.recipe, tx.fingerprintChecked
	tx.mu.Unlock()
	if checked {
		return nil
	}
	if _, err := os.Stat(tx.files.fingerprint); err == nil {
		tx.mu.Lock()
		tx.fingerprintChecked = true
		tx.mu.Unlock()
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("indexstore: stat %s: %w", tx.files.fingerprint, err)
	}
	if r == nil {
		return ErrNoRecipe
	}
	if err := writeFile(tx.files.fingerprint, []byte(r.Sum())); err != nil {
		return err
	}
	tx.noteWrite(false)
	tx.mu.Lock()
	tx.fingerprintChecked = true
	tx.mu.Unlock()
	return nil
}
