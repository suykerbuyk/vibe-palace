// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ReadGraph reads a project's HNSW graph file (hnsw.idx) without a lock: the
// file is only ever replaced whole, by Tx.WriteGraph's atomic rename, so a
// lock-free reader never sees a torn file. A missing file is (nil, nil) — the
// "no persisted graph" signal the search engine's loader turns into a
// build-from-vectors fallback. Every other error (e.g. a permission failure) is
// surfaced, exactly as ReadGeneration does (generation.go), so a real I/O fault
// is never silently read as "no graph".
func ReadGraph(vault *storage.Vault, project string) ([]byte, error) {
	pf, err := filesFor(vault, project)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(pf.graph)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("indexstore: read %s: %w", pf.graph, err)
	}
	return data, nil
}
