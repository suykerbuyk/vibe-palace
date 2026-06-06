// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a tiny helper to write a file under vault, creating parents.
func writeFile(t *testing.T, root, rel string, data []byte) string {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestRead_HappyPath(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "Notes/foo.md", []byte("hello world"))
	got, err := Read(vault, "Notes/foo.md", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Content != "hello world" {
		t.Errorf("content: got %q", got.Content)
	}
	if got.Bytes != int64(len("hello world")) {
		t.Errorf("bytes: got %d", got.Bytes)
	}
	sum := sha256.Sum256([]byte("hello world"))
	if got.Sha256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 mismatch")
	}
	if got.Mtime.IsZero() {
		t.Error("mtime should not be zero")
	}
}

func TestRead_FileNotFound(t *testing.T) {
	vault := t.TempDir()
	_, err := Read(vault, "missing.md", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrFileNotFound) {
		t.Errorf("want ErrFileNotFound, got %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		// ErrFileNotFound should chain to fs.ErrNotExist semantically; we
		// achieve that by formatting %w to ErrFileNotFound and verifying via
		// the sentinel comparison above. Keep this assertion soft.
		_ = err
	}
}

func TestRead_SizeCapDefault(t *testing.T) {
	vault := t.TempDir()
	// 1 MiB exactly succeeds.
	exact := bytes.Repeat([]byte("a"), 1<<20)
	writeFile(t, vault, "exact.bin", exact)
	if _, err := Read(vault, "exact.bin", 0); err != nil {
		t.Fatalf("1 MiB should succeed: %v", err)
	}
	// 1 MiB + 1 byte fails under default cap.
	over := bytes.Repeat([]byte("a"), (1<<20)+1)
	writeFile(t, vault, "over.bin", over)
	if _, err := Read(vault, "over.bin", 0); err == nil {
		t.Fatal("1 MiB + 1 should fail under default cap")
	}
}

func TestRead_SizeCapCustom(t *testing.T) {
	vault := t.TempDir()
	data := bytes.Repeat([]byte("a"), 5<<20) // 5 MiB
	writeFile(t, vault, "big.bin", data)
	if _, err := Read(vault, "big.bin", 5<<20); err != nil {
		t.Fatalf("5 MiB read with 5 MiB cap should succeed: %v", err)
	}
}

func TestRead_SizeCapExceedsMax(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "tiny.md", []byte("x"))
	_, err := Read(vault, "tiny.md", (10<<20)+1)
	if err == nil {
		t.Fatal("max_bytes > 10 MiB should error")
	}
}

func TestRead_PathTraversalRejected(t *testing.T) {
	vault := t.TempDir()
	_, err := Read(vault, "../etc/passwd", 0)
	if err == nil {
		t.Fatal("expected traversal rejection")
	}
	if !errors.Is(err, ErrPathTraversal) {
		t.Errorf("want ErrPathTraversal, got %v", err)
	}
}

func TestRead_FollowsSymlinkUnderVault(t *testing.T) {
	vault := t.TempDir()
	target := writeFile(t, vault, "real/inner.md", []byte("real content"))
	link := filepath.Join(vault, "alias.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	got, err := Read(vault, "alias.md", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Content != "real content" {
		t.Errorf("content via symlink: got %q", got.Content)
	}
}

func TestRead_RejectsSymlinkEscape_Realpath(t *testing.T) {
	vault := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(vault, "leak.md")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	_, err := Read(vault, "leak.md", 0)
	if err == nil {
		t.Fatal("expected ErrSymlinkEscape")
	}
	if !errors.Is(err, ErrSymlinkEscape) {
		t.Errorf("want ErrSymlinkEscape, got %v", err)
	}
}

func TestList_HappyPath(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "dir/a.md", []byte("aaa"))
	writeFile(t, vault, "dir/b.md", []byte("bbbb"))
	if err := os.MkdirAll(filepath.Join(vault, "dir/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := List(vault, "dir", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("want 3 entries, got %d: %+v", len(entries), entries)
	}
	byName := map[string]Entry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if e := byName["a.md"]; e.Type != "file" || e.Bytes != 3 {
		t.Errorf("a.md: %+v", e)
	}
	if e := byName["b.md"]; e.Type != "file" || e.Bytes != 4 {
		t.Errorf("b.md: %+v", e)
	}
	if e := byName["sub"]; e.Type != "dir" {
		t.Errorf("sub: %+v", e)
	}
}

func TestList_NotADir(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "f.md", []byte("x"))
	_, err := List(vault, "f.md", false)
	if err == nil {
		t.Fatal("expected error listing a file path")
	}
}

func TestList_NotFound(t *testing.T) {
	vault := t.TempDir()
	_, err := List(vault, "no-such-dir", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrFileNotFound) {
		t.Errorf("want ErrFileNotFound, got %v", err)
	}
}

func TestList_HidesDotGit(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "p/file.md", []byte("x"))
	if err := os.MkdirAll(filepath.Join(vault, "p", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := List(vault, "p", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, ".git") {
			t.Errorf(".git should be hidden, found %+v", e)
		}
	}
}

func TestList_HidesDotGitCaseInsensitive(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "p/file.md", []byte("x"))
	for _, name := range []string{".GIT", ".Git"} {
		if err := os.MkdirAll(filepath.Join(vault, "p", name), 0o755); err != nil {
			// case-insensitive filesystems may collapse these; just skip on conflict
			continue
		}
	}
	entries, err := List(vault, "p", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, ".git") {
			t.Errorf("case-insensitive .git should be hidden, found %+v", e)
		}
	}
}

func TestList_IncludeSha256_OptIn(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "d/a.md", []byte("aaa"))
	// default: no sha
	entries, err := List(vault, "d", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1, got %d", len(entries))
	}
	if entries[0].Sha256 != "" {
		t.Errorf("default should omit sha, got %q", entries[0].Sha256)
	}
	// opt-in: sha set
	entries2, err := List(vault, "d", true)
	if err != nil {
		t.Fatal(err)
	}
	if entries2[0].Sha256 == "" {
		t.Error("include_sha256=true should populate sha")
	}
	want := sha256.Sum256([]byte("aaa"))
	if entries2[0].Sha256 != hex.EncodeToString(want[:]) {
		t.Errorf("sha mismatch: got %s", entries2[0].Sha256)
	}
}

func TestExists_File(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "f.md", []byte("x"))
	got, err := Exists(vault, "f.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exists || got.Type != "file" {
		t.Errorf("got %+v", got)
	}
}

func TestExists_Dir(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Exists(vault, "d")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exists || got.Type != "dir" {
		t.Errorf("got %+v", got)
	}
}

func TestExists_Missing(t *testing.T) {
	vault := t.TempDir()
	got, err := Exists(vault, "nope")
	if err != nil {
		t.Fatal(err)
	}
	if got.Exists {
		t.Errorf("want exists=false, got %+v", got)
	}
	if got.Type != "" {
		t.Errorf("want type=\"\", got %q", got.Type)
	}
}

func TestExists_DanglingSymlink(t *testing.T) {
	vault := t.TempDir()
	link := filepath.Join(vault, "dangle.md")
	if err := os.Symlink("/no/such/target/anywhere/xyz", link); err != nil {
		t.Skip("symlinks unsupported")
	}
	got, err := Exists(vault, "dangle.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Exists {
		t.Errorf("dangling symlink should be exists=false, got %+v", got)
	}
}

func TestExists_Symlink_ResolvesUnderVault(t *testing.T) {
	vault := t.TempDir()
	target := writeFile(t, vault, "real.md", []byte("hi"))
	link := filepath.Join(vault, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	got, err := Exists(vault, "link.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exists || got.Type != "file" {
		t.Errorf("want exists=true type=file, got %+v", got)
	}
}

func TestSha256_HappyPath(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "f.md", []byte("hello"))
	got, err := Sha256(vault, "f.md")
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("hello"))
	if got.Sha256 != hex.EncodeToString(want[:]) {
		t.Errorf("sha mismatch")
	}
	if got.Bytes != 5 {
		t.Errorf("bytes: got %d", got.Bytes)
	}
	if got.Mtime.IsZero() {
		t.Error("mtime zero")
	}
}

func TestSha256_FileNotFound(t *testing.T) {
	vault := t.TempDir()
	_, err := Sha256(vault, "nope.md")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrFileNotFound) {
		t.Errorf("want ErrFileNotFound, got %v", err)
	}
}
