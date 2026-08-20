// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These tests pin the OBSERVABLE CONTRACT of Delete and Move — the two
// write-side entry points behind the MCP tools vp_vault_delete and
// vp_vault_move (internal/tools/vault_file_tools.go:329,372) and behind
// `vp vault delete` / `vp vault move`.
//
// They were written BEFORE the Option E Phase 3 extraction that turned Delete
// and Move into a policy layer over the raw sink in raw.go, and run unchanged
// after it. That is their entire purpose: the refactor moved WHERE the syscall
// is issued, and these assert that nothing a caller can see moved with it.
//
// The refusals below are the contract, not implementation detail:
//
//   - .git-segment refusal            (IsRefusedWritePath)
//   - traversal + symlink containment (ResolveSafePath)
//   - sha256 compare-and-set          (Delete only)
//   - file-only: directories refused  (Delete)
//   - destination must not exist      (Move)
//   - missing source is ErrFileNotFound, NOT a silent success
//
// Several of these already had coverage; the ones that did not — containment
// at this layer rather than at ResolveSafePath's own layer, and the
// not-found sentinels — are the reason this file exists.

// --- containment: traversal ------------------------------------------------

func TestDelete_RefusesTraversalEscape(t *testing.T) {
	vault := t.TempDir()
	outside := filepath.Join(filepath.Dir(vault), "outside.md")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Delete(vault, "../outside.md", ""); err == nil {
		t.Fatal("expected traversal refusal")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the vault must be untouched: %v", err)
	}
}

func TestMove_RefusesTraversalEscape_Source(t *testing.T) {
	vault := t.TempDir()
	outside := filepath.Join(filepath.Dir(vault), "outside-src.md")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(vault, "../outside-src.md", "landed.md"); err == nil {
		t.Fatal("expected traversal refusal")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the vault must be untouched: %v", err)
	}
}

func TestMove_RefusesTraversalEscape_Destination(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(vault, "f.md", "../escaped.md"); err == nil {
		t.Fatal("expected traversal refusal")
	}
	if _, err := os.Stat(filepath.Join(vault, "f.md")); err != nil {
		t.Errorf("source must survive a refused move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(vault), "escaped.md")); !os.IsNotExist(err) {
		t.Errorf("nothing may be created outside the vault, stat err: %v", err)
	}
}

// --- containment: symlink escape -------------------------------------------

// A symlink inside the vault pointing at a real file outside it is the
// escape ResolveSafePath's EvalSymlinks pass exists to stop. Pinned here at
// the Delete layer because this is the layer the MCP tool calls.
func TestDelete_RefusesSymlinkEscape(t *testing.T) {
	vault := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(vault, "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Delete(vault, "link.md", ""); err == nil {
		t.Fatal("expected symlink-escape refusal")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("target outside the vault must be untouched: %v", err)
	}
}

func TestMove_RefusesSymlinkEscape_Source(t *testing.T) {
	vault := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(vault, "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Move(vault, "link.md", "landed.md"); err == nil {
		t.Fatal("expected symlink-escape refusal")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("target outside the vault must be untouched: %v", err)
	}
}

// --- not-found sentinels ---------------------------------------------------

// A delete of an absent file must FAIL with ErrFileNotFound. vp_vault_delete
// returns {removed}, so a silent success here would report a removal that
// never happened — and storage.DeleteMemory's deliberately idempotent
// behaviour is the CALLER's policy, not this layer's.
func TestDelete_MissingFileIsErrFileNotFound(t *testing.T) {
	vault := t.TempDir()
	_, err := Delete(vault, "nope.md", "")
	if err == nil {
		t.Fatal("expected ErrFileNotFound")
	}
	if !errors.Is(err, ErrFileNotFound) {
		t.Errorf("want ErrFileNotFound, got %v", err)
	}
}

func TestMove_MissingSourceIsErrFileNotFound(t *testing.T) {
	vault := t.TempDir()
	_, err := Move(vault, "nope.md", "somewhere.md")
	if err == nil {
		t.Fatal("expected ErrFileNotFound")
	}
	if !errors.Is(err, ErrFileNotFound) {
		t.Errorf("want ErrFileNotFound, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "somewhere.md")); !os.IsNotExist(err) {
		t.Errorf("destination must not be created, stat err: %v", err)
	}
}

// --- compare-and-set, matching arm -----------------------------------------

// The mismatch arm is pinned by TestDelete_CompareAndSet_Mismatch. This is the
// other half: a MATCHING digest must let the delete through, so the CAS cannot
// be "fixed" into a blanket refusal.
func TestDelete_CompareAndSet_Match(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "x", ""); err != nil {
		t.Fatal(err)
	}
	res, err := Delete(vault, "f.md", sha256Hex([]byte("x")))
	if err != nil {
		t.Fatalf("matching digest must delete: %v", err)
	}
	if !res.Removed {
		t.Error("removed should be true")
	}
	if _, err := os.Stat(filepath.Join(vault, "f.md")); !os.IsNotExist(err) {
		t.Errorf("file should be gone, stat err: %v", err)
	}
}

// --- file-only is POLICY, and it lives here --------------------------------

// raw.go's sink is os.Remove, which succeeds on an EMPTY DIRECTORY. That is
// deliberate — internal/storage's KG prune removes directories through it. The
// file-only rule is Delete's own policy, and this pins that it is enforced
// ABOVE the sink: an empty directory reaches Delete and is still refused.
func TestDelete_RefusesEmptyDirectory(t *testing.T) {
	vault := t.TempDir()
	dir := filepath.Join(vault, "empty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Delete(vault, "empty", ""); err == nil {
		t.Fatal("expected refusal on an empty directory")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("directory must survive the refusal: %v", err)
	}
}
