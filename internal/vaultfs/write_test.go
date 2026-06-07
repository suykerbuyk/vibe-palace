// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestWrite_HappyPath(t *testing.T) {
	vault := t.TempDir()
	res, err := Write(vault, "Notes/foo.md", "hello", "")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(vault, "Notes/foo.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("content: got %q", got)
	}
	if res.Sha256 != sha256Hex([]byte("hello")) {
		t.Errorf("sha mismatch")
	}
	if res.Bytes != 5 {
		t.Errorf("bytes: %d", res.Bytes)
	}
}

func TestWrite_FilePermissions_0o644(t *testing.T) {
	vault := t.TempDir()
	_, err := Write(vault, "f.md", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(vault, "f.md"))
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	if mode != 0o644 {
		t.Errorf("mode: got %o, want 0644", mode)
	}
}

func TestWrite_NoTempFileDebrisOnSuccess(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "dir/f.md", "x", ""); err != nil {
		t.Fatal(err)
	}
	dirents, err := os.ReadDir(filepath.Join(vault, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirents {
		if strings.HasPrefix(d.Name(), ".vp-atomic-") {
			t.Errorf("temp debris remains: %s", d.Name())
		}
	}
}

func TestWrite_NoTempFileDebrisOnRenameError(t *testing.T) {
	// Set up a destination whose target name collides with an existing
	// directory to force a rename failure, then verify no temp debris remains.
	vault := t.TempDir()
	collision := filepath.Join(vault, "f.md")
	if err := os.Mkdir(collision, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Write(vault, "f.md", "x", "")
	if err == nil {
		t.Fatal("expected rename error")
	}
	dirents, derr := os.ReadDir(vault)
	if derr != nil {
		t.Fatal(derr)
	}
	for _, d := range dirents {
		if strings.HasPrefix(d.Name(), ".vp-atomic-") {
			t.Errorf("temp debris remains: %s", d.Name())
		}
	}
}

func TestWrite_RefusesGitDir_TopLevel(t *testing.T) {
	vault := t.TempDir()
	_, err := Write(vault, ".git/HEAD", "x", "")
	if err == nil {
		t.Fatal("expected ErrRefusedPath")
	}
	if !errors.Is(err, ErrRefusedPath) {
		t.Errorf("want ErrRefusedPath, got %v", err)
	}
}

func TestWrite_RefusesGitDir_Nested(t *testing.T) {
	vault := t.TempDir()
	_, err := Write(vault, "Projects/x/.git/foo", "x", "")
	if err == nil {
		t.Fatal("expected ErrRefusedPath")
	}
	if !errors.Is(err, ErrRefusedPath) {
		t.Errorf("want ErrRefusedPath, got %v", err)
	}
}

func TestWrite_RefusesGitDir_CaseInsensitive(t *testing.T) {
	vault := t.TempDir()
	_, err := Write(vault, ".GIT/HEAD", "x", "")
	if err == nil {
		t.Fatal("expected ErrRefusedPath")
	}
	if !errors.Is(err, ErrRefusedPath) {
		t.Errorf("want ErrRefusedPath, got %v", err)
	}
}

func TestWrite_AllowsGitSubstring(t *testing.T) {
	vault := t.TempDir()
	_, err := Write(vault, "Projects/x/foo.git/bar", "ok", "")
	if err != nil {
		t.Fatalf("substring .git should be allowed: %v", err)
	}
}

func TestWrite_CompareAndSet_Match(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "v1", ""); err != nil {
		t.Fatal(err)
	}
	v1Sha := sha256Hex([]byte("v1"))
	if _, err := Write(vault, "f.md", "v2", v1Sha); err != nil {
		t.Fatalf("matching sha should succeed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(vault, "f.md"))
	if string(got) != "v2" {
		t.Errorf("content: got %q", got)
	}
}

func TestWrite_CompareAndSet_Mismatch(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "v1", ""); err != nil {
		t.Fatal(err)
	}
	_, err := Write(vault, "f.md", "v2", sha256Hex([]byte("WRONG")))
	if err == nil {
		t.Fatal("expected ErrShaConflict")
	}
	if !errors.Is(err, ErrShaConflict) {
		t.Errorf("want ErrShaConflict, got %v", err)
	}
	// File unchanged.
	got, _ := os.ReadFile(filepath.Join(vault, "f.md"))
	if string(got) != "v1" {
		t.Errorf("file should be unchanged, got %q", got)
	}
}

func TestWrite_CompareAndSet_FileMissing(t *testing.T) {
	vault := t.TempDir()
	_, err := Write(vault, "missing.md", "x", sha256Hex([]byte("anything")))
	if err == nil {
		t.Fatal("expected error when expected_sha256 supplied for missing file")
	}
}

func TestWrite_NoCompareAndSet_Overwrites(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "v1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(vault, "f.md", "v2", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(vault, "f.md"))
	if string(got) != "v2" {
		t.Errorf("content: got %q", got)
	}
}

func TestWrite_CreatesParentDirs(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "a/b/c/d.md", "x", ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(vault, "a/b/c/d.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "x" {
		t.Errorf("content: got %q", got)
	}
}

func TestEdit_HappyPath(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "alpha bravo charlie", ""); err != nil {
		t.Fatal(err)
	}
	res, err := Edit(vault, "f.md", "bravo", "DELTA", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Replacements != 1 {
		t.Errorf("replacements: %d", res.Replacements)
	}
	got, _ := os.ReadFile(filepath.Join(vault, "f.md"))
	if string(got) != "alpha DELTA charlie" {
		t.Errorf("content: %q", got)
	}
}

func TestEdit_NotFoundString(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	_, err := Edit(vault, "f.md", "missing", "x", false, "")
	if err == nil {
		t.Fatal("expected error for not-found string")
	}
}

func TestEdit_AmbiguousMatch(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "foo foo foo", ""); err != nil {
		t.Fatal(err)
	}
	_, err := Edit(vault, "f.md", "foo", "X", false, "")
	if err == nil {
		t.Fatal("expected ambiguous-match error")
	}
	if !strings.Contains(err.Error(), "replace_all") {
		t.Errorf("error should mention replace_all: %v", err)
	}
}

func TestEdit_ReplaceAll(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "foo foo foo", ""); err != nil {
		t.Fatal(err)
	}
	res, err := Edit(vault, "f.md", "foo", "X", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Replacements != 3 {
		t.Errorf("replacements: %d", res.Replacements)
	}
	got, _ := os.ReadFile(filepath.Join(vault, "f.md"))
	if string(got) != "X X X" {
		t.Errorf("content: %q", got)
	}
}

func TestEdit_RefusesGitDir(t *testing.T) {
	vault := t.TempDir()
	_, err := Edit(vault, ".git/config", "x", "y", false, "")
	if err == nil {
		t.Fatal("expected ErrRefusedPath")
	}
	if !errors.Is(err, ErrRefusedPath) {
		t.Errorf("want ErrRefusedPath, got %v", err)
	}
}

func TestEdit_CompareAndSet_Mismatch(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	_, err := Edit(vault, "f.md", "alpha", "beta", false, sha256Hex([]byte("WRONG")))
	if err == nil {
		t.Fatal("expected ErrShaConflict")
	}
	if !errors.Is(err, ErrShaConflict) {
		t.Errorf("want ErrShaConflict, got %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(vault, "f.md"))
	if string(got) != "alpha" {
		t.Errorf("file should be unchanged, got %q", got)
	}
}

func TestDelete_HappyPath(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "x", ""); err != nil {
		t.Fatal(err)
	}
	res, err := Delete(vault, "f.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Removed {
		t.Error("removed should be true")
	}
	if _, err := os.Stat(filepath.Join(vault, "f.md")); !os.IsNotExist(err) {
		t.Errorf("file should be gone, stat err: %v", err)
	}
}

func TestDelete_RefusesGitDir(t *testing.T) {
	vault := t.TempDir()
	_, err := Delete(vault, ".git/HEAD", "")
	if err == nil {
		t.Fatal("expected ErrRefusedPath")
	}
	if !errors.Is(err, ErrRefusedPath) {
		t.Errorf("want ErrRefusedPath, got %v", err)
	}
}

func TestDelete_OnDirectory(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Delete(vault, "d", "")
	if err == nil {
		t.Fatal("expected error deleting directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error should mention directory: %v", err)
	}
}

func TestDelete_CompareAndSet_Mismatch(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "x", ""); err != nil {
		t.Fatal(err)
	}
	_, err := Delete(vault, "f.md", sha256Hex([]byte("WRONG")))
	if err == nil {
		t.Fatal("expected ErrShaConflict")
	}
	if !errors.Is(err, ErrShaConflict) {
		t.Errorf("want ErrShaConflict, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "f.md")); err != nil {
		t.Errorf("file should still exist: %v", err)
	}
}

func TestMove_HappyPath(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "src.md", "data", ""); err != nil {
		t.Fatal(err)
	}
	res, err := Move(vault, "src.md", "dst/dst.md")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Moved {
		t.Error("moved should be true")
	}
	if _, statErr := os.Stat(filepath.Join(vault, "src.md")); !os.IsNotExist(statErr) {
		t.Errorf("src should be gone, stat err: %v", statErr)
	}
	got, err := os.ReadFile(filepath.Join(vault, "dst/dst.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" {
		t.Errorf("content: %q", got)
	}
}

func TestMove_DestinationExists(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "a.md", "A", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(vault, "b.md", "B", ""); err != nil {
		t.Fatal(err)
	}
	_, err := Move(vault, "a.md", "b.md")
	if err == nil {
		t.Fatal("expected error: destination exists")
	}
	// b.md unchanged.
	got, _ := os.ReadFile(filepath.Join(vault, "b.md"))
	if string(got) != "B" {
		t.Errorf("destination corrupted: %q", got)
	}
}

func TestMove_RefusesGitDir_Source(t *testing.T) {
	vault := t.TempDir()
	_, err := Move(vault, ".git/HEAD", "out.txt")
	if err == nil {
		t.Fatal("expected ErrRefusedPath on source")
	}
	if !errors.Is(err, ErrRefusedPath) {
		t.Errorf("want ErrRefusedPath, got %v", err)
	}
}

func TestMove_RefusesGitDir_Destination(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "ok.md", "x", ""); err != nil {
		t.Fatal(err)
	}
	_, err := Move(vault, "ok.md", ".git/HEAD")
	if err == nil {
		t.Fatal("expected ErrRefusedPath on destination")
	}
	if !errors.Is(err, ErrRefusedPath) {
		t.Errorf("want ErrRefusedPath, got %v", err)
	}
}

func TestMove_SamePath(t *testing.T) {
	vault := t.TempDir()
	if _, err := Write(vault, "f.md", "x", ""); err != nil {
		t.Fatal(err)
	}
	_, err := Move(vault, "f.md", "f.md")
	if err == nil {
		t.Fatal("expected error for same source/dest")
	}
}

// TestEdit_ConcurrentNoLostUpdate is the headline acceptance test for the
// vault-write-concurrency epic. It pre-seeds a file with N distinct anchor
// tokens (ANCHOR_0..ANCHOR_{N-1}) and launches N goroutines, each replacing its
// own anchor with a DONE marker via vaultfs.Edit. Because Edit does a
// whole-file read→replace→write, without the per-path lock concurrent writers
// clobber each other's DONE markers (lost update). With the lock held across the
// entire read-modify-write, every contribution survives. Run under -race.
func TestEdit_ConcurrentNoLostUpdate(t *testing.T) {
	const n = 32
	vault := t.TempDir()

	// Zero-pad token indices to a fixed width so no anchor/marker is a substring
	// of another (ANCHOR_1 would otherwise match ANCHOR_10..19, breaking the
	// single-occurrence Edit and the substring assertions below).
	anchor := func(i int) string { return fmt.Sprintf("ANCHOR_%03d", i) }
	done := func(i int) string { return fmt.Sprintf("DONE_%03d", i) }

	var seed strings.Builder
	for i := range n {
		fmt.Fprintf(&seed, "line %s\n", anchor(i))
	}
	if _, err := Write(vault, "concurrent.md", seed.String(), ""); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			_, errs[i] = Edit(vault, "concurrent.md", anchor(i), done(i), false, "")
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Edit goroutine %d: %v", i, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(vault, "concurrent.md"))
	if err != nil {
		t.Fatal(err)
	}
	final := string(got)
	for i := range n {
		if !strings.Contains(final, done(i)) {
			t.Errorf("lost update: %s missing from final content", done(i))
		}
		if strings.Contains(final, anchor(i)) {
			t.Errorf("unreplaced anchor remains: %s", anchor(i))
		}
	}
}

// TestWrite_ConcurrentSameBaseNoCorruption launches many blind Writes at the
// same target. Last-writer-wins is acceptable for a blind Write, but the final
// content must be exactly one of the written values (no interleaving / partial
// corruption) and the race detector must stay quiet.
func TestWrite_ConcurrentSameBaseNoCorruption(t *testing.T) {
	const n = 32
	vault := t.TempDir()

	if _, err := Write(vault, "blind.md", "base", ""); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	valid := make(map[string]bool, n)
	for i := range n {
		valid[fmt.Sprintf("writer-%d-payload", i)] = true
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			content := fmt.Sprintf("writer-%d-payload", i)
			_, errs[i] = Write(vault, "blind.md", content, "")
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Write goroutine %d: %v", i, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(vault, "blind.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !valid[string(got)] {
		t.Errorf("corrupted final content: %q is not any single writer's payload", got)
	}
}
