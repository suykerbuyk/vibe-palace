// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The F2 sink's contract, asserted directly rather than through Delete/Move.
// Each of these is depended on by a caller OUTSIDE this package, so a
// well-meaning tightening of the sink would break something far away:
//
//   - directories are accepted        -> storage.pruneEmptyDirs
//   - the error is NOT wrapped        -> storage.DeleteMemory's idempotence
//   - an existing destination is
//     REPLACED, not refused           -> Move's stat is what refuses, not this

func TestRemoveNoLock_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.md")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveNoLock(p); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("file should be gone, stat err: %v", err)
	}
}

// 🔴 The sink accepts an EMPTY DIRECTORY. storage.pruneEmptyDirs removes the
// KG migration's emptied subject/object directories through it, and relies on
// os.Remove's "empty only" behaviour for control flow. Delete's file-only rule
// is policy layered ABOVE this — pinned by TestDelete_RefusesEmptyDirectory.
// Do not "harden" the sink to refuse directories; it would silently turn the
// prune into a no-op.
func TestRemoveNoLock_RemovesEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "empty")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RemoveNoLock(sub); err != nil {
		t.Fatalf("the sink must accept an empty directory: %v", err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("directory should be gone, stat err: %v", err)
	}
}

func TestRemoveNoLock_RefusesNonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "full")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveNoLock(sub); err == nil {
		t.Fatal("os.Remove must not delete a non-empty directory")
	}
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("directory must survive: %v", err)
	}
}

// 🔴 storage.DeleteMemory is documented idempotent and tests the sink's error
// with errors.Is(err, fs.ErrNotExist). Both halves matter: the sentinel must
// match, AND os.IsNotExist — which does NOT unwrap — must still match, because
// other callers in the tree use it. Wrapping the error here breaks the second.
func TestRemoveNoLock_MissingFileErrorIsUnwrapped(t *testing.T) {
	dir := t.TempDir()
	err := RemoveNoLock(filepath.Join(dir, "nope.md"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(fs.ErrNotExist) must hold, got %v", err)
	}
	if !os.IsNotExist(err) {
		t.Errorf("os.IsNotExist must hold too — it does NOT unwrap, so this fails the moment the sink wraps: %v", err)
	}
}

func TestRenameNoLock_RenamesFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.md")
	dst := filepath.Join(dir, "b.md")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoLock(src, dst); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "x" {
		t.Errorf("content: got %q", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source should be gone, stat err: %v", err)
	}
}

// 🔴 The sink REPLACES an existing destination. Move's
// refuse-existing-destination rule is the os.Stat above its sink call, not a
// property of the sink — pinned by TestMove_DestinationExists. Recorded here so
// a reader who finds only the Move test does not conclude the sink refuses too,
// and so a caller outside vaultfs knows it must enforce that itself.
func TestRenameNoLock_ReplacesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.md")
	dst := filepath.Join(dir, "b.md")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoLock(src, dst); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("destination should have been replaced: got %q", got)
	}
}

// The sink does not stamp. Removals and renames write no content, and the
// surface stamp tracks content-write semantics — so a .vibe-palace/ stamp
// directory must not appear as a side effect of either call. This is the
// deliberate asymmetry with F1/F4; pinning it stops a later reader "fixing" it.
func TestSink_DoesNotStamp(t *testing.T) {
	vault := t.TempDir()
	src := filepath.Join(vault, "a.md")
	dst := filepath.Join(vault, "b.md")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoLock(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := RemoveNoLock(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vault, ".vibe-palace")); !os.IsNotExist(err) {
		t.Errorf("the sink must not stamp; stat err: %v", err)
	}
}
