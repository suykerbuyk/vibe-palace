// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// writeGraphViaTx writes data as hnsw.idx through a committed Tx.
func writeGraphViaTx(t *testing.T, v *storage.Vault, project string, data []byte) error {
	t.Helper()
	tx, err := Lock(context.Background(), v, project, NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if werr := tx.WriteGraph(data); werr != nil {
		_ = tx.Release()
		return werr
	}
	return tx.Commit()
}

// TestWriteGraphFailurePreservesPreviousBytes (S5): a failed whole-file write
// leaves the previous hnsw.idx intact, because WriteGraph routes through the
// writeFileFn seam (no second writer). Mutant: a WriteGraph that writes the
// file directly (bypassing writeFileFn) — the injected failure would not fire,
// and the old bytes would be clobbered.
func TestWriteGraphFailurePreservesPreviousBytes(t *testing.T) {
	v := newVault(t)
	const project = "alpha"
	dir, err := v.IndexDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, graphFile)

	if err := writeGraphViaTx(t, v, project, []byte("AAAA-first-good-graph")); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Fail the next whole-file write through the seam.
	orig := writeFileFn
	writeFileFn = func(string, []byte) error { return errors.New("injected write failure") }
	defer func() { writeFileFn = orig }()

	if err := writeGraphViaTx(t, v, project, []byte("BBBB-should-not-land")); err == nil {
		t.Fatal("expected WriteGraph to fail through the injected seam")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after failed write: %v", err)
	}
	if string(got) != "AAAA-first-good-graph" {
		t.Fatalf("previous hnsw.idx bytes were not preserved: got %q", got)
	}
}

// S-CR3: ReadGraph maps only a missing file to (nil, nil); any other error is
// surfaced (never swallowed as "no graph"). Mutant: map every error to (nil,nil).
func TestReadGraph(t *testing.T) {
	v := newVault(t)
	const project = "alpha"

	// Missing file → (nil, nil).
	data, err := ReadGraph(v, project)
	if err != nil || data != nil {
		t.Fatalf("missing file -> (%v, %v), want (nil, nil)", data, err)
	}

	// A directory where hnsw.idx should be → a real (non-ErrNotExist) error.
	dir, err := v.IndexDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, graphFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGraph(v, project); err == nil {
		t.Fatal("want a surfaced error when hnsw.idx is a directory, got nil")
	}
}
