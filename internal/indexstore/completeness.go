// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"slices"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Completeness is a project's completeness record, index/<p>/completeness.json
// (ADR-014 decision 2): what was built, recorded and never inferred from an
// in-memory index or a file being present, so a fresh process and the
// coverage instrument read the same answer without loading the embedder.
//
// Its meaning (the tier table, when stale is set, what clears it) belongs to
// search-index-completeness-and-build-serialization; this package owns the
// file, its lock and its two proofs' checks. It records no graph fingerprint:
// that has one copy, inside hnsw.idx (ADR-014 decision 3).
type Completeness struct {
	// Tiers maps each built tier to the count of sources it was built from.
	Tiers map[string]TierRecord `json:"tiers,omitempty"`
	// LocalMisses is the number of loaded local-tier chunks (chunks of
	// ledgered sources) with no vector in the embed cache.
	LocalMisses int `json:"local_misses"`
	// ChunksFingerprint and EmbedFingerprint are the chunks.fingerprint recipe
	// and the embed-cache regime the local tier was built under, kept here so a
	// lost sidecar cannot hide a recipe or regime change.
	ChunksFingerprint string `json:"chunks_fingerprint,omitempty"`
	EmbedFingerprint  string `json:"embed_fingerprint,omitempty"`
	// Stale is the persistent stale flag: set when it holds a reason.
	Stale []StaleReason `json:"stale,omitempty"`
}

// TierRecord is one built tier.
type TierRecord struct {
	Sources int `json:"sources"`
}

// StaleReason is one reason a project is stale. Kind is StaleFingerprint
// (Fingerprint names which: FingerprintChunks or FingerprintEmbed) or
// StaleMissingVectors.
type StaleReason struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// The stale reasons and the fingerprints a StaleFingerprint reason names.
const (
	StaleFingerprint    = "fingerprint"
	StaleMissingVectors = "missing_vectors"

	FingerprintChunks = "chunks"
	FingerprintEmbed  = "embed"
)

// canonical returns c with its reasons sorted and deduplicated, so equal
// records encode to equal bytes.
func (c Completeness) canonical() Completeness {
	out := c
	out.Stale = slices.Clone(c.Stale)
	slices.SortFunc(out.Stale, func(a, b StaleReason) int {
		if a.Kind != b.Kind {
			if a.Kind < b.Kind {
				return -1
			}
			return 1
		}
		switch {
		case a.Fingerprint < b.Fingerprint:
			return -1
		case a.Fingerprint > b.Fingerprint:
			return 1
		}
		return 0
	})
	out.Stale = slices.Compact(out.Stale)
	if len(out.Stale) == 0 {
		out.Stale = nil
	}
	if len(out.Tiers) == 0 {
		out.Tiers = nil
	}
	return out
}

func encodeCompleteness(c Completeness) ([]byte, error) {
	data, err := json.MarshalIndent(c.canonical(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ReadCompleteness reads a project's completeness record without a lock: the
// file is only ever replaced whole. A missing file is the zero record.
func ReadCompleteness(vault *storage.Vault, project string) (Completeness, error) {
	pf, err := filesFor(vault, project)
	if err != nil {
		return Completeness{}, err
	}
	return readCompletenessFile(pf.completeness)
}

func readCompletenessFile(path string) (Completeness, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Completeness{}, nil
	}
	if err != nil {
		return Completeness{}, fmt.Errorf("indexstore: read %s: %w", path, err)
	}
	var c Completeness
	if err := json.Unmarshal(data, &c); err != nil {
		return Completeness{}, fmt.Errorf("indexstore: decode %s: %w", path, err)
	}
	return c, nil
}

// Completeness reads the record under the held lock.
func (tx *Tx) Completeness() (Completeness, error) {
	return readCompletenessFile(tx.files.completeness)
}

// WriteCompleteness replaces the record, atomically, under the held lock, and
// reports whether it wrote. A record whose canonical bytes equal the file's is
// NOT written and does not move the counter, and neither is a zero record
// where there is no file: every search that re-finds a
// persistent condition (a missing vector, a stale fingerprint) writes the same
// record, and a counter bump there would make every running engine reload on
// every search. A write is an append (gen moves, the epoch does not).
func (tx *Tx) WriteCompleteness(c Completeness) (bool, error) {
	data, err := encodeCompleteness(c)
	if err != nil {
		return false, err
	}
	cur, err := os.ReadFile(tx.files.completeness)
	if err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	if errors.Is(err, fs.ErrNotExist) && reflect.DeepEqual(c.canonical(), Completeness{}) {
		// Nothing built and nothing recorded: an absent file already reads as
		// the zero record, so a search of an empty project writes nothing.
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("indexstore: read %s: %w", tx.files.completeness, err)
	}
	if err := os.MkdirAll(tx.files.dir, 0o755); err != nil {
		return false, fmt.Errorf("indexstore: create %s: %w", tx.files.dir, err)
	}
	if err := writeFile(tx.files.completeness, data); err != nil {
		return false, err
	}
	tx.noteWrite(false)
	return true, nil
}

// ClearStale clears every stale reason, and only a completed `vp index
// rebuild` may do it: proof must be the RebuildProof of this project's
// completed rebuild (RunLock.CompletedRebuild). The conditions of "completed"
// (every live archive attempted, no local-tier miss left, embedding ran) are
// the rebuild driver's to establish before it asks for the proof.
func (tx *Tx) ClearStale(proof RebuildProof) error {
	if err := proof.check(tx); err != nil {
		return err
	}
	c, err := tx.Completeness()
	if err != nil {
		return err
	}
	c.Stale = nil
	_, err = tx.WriteCompleteness(c)
	return err
}
