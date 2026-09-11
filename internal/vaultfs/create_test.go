// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestCreate_NewFile(t *testing.T) {
	vault := t.TempDir()
	res, err := Create(vault, "Templates/commands/wrap.md.0123456789ab.bak", "hello\n")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p := filepath.Join(vault, "Templates", "commands", "wrap.md.0123456789ab.bak")
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("content = %q (err=%v)", got, err)
	}
	if res.Bytes != 6 || res.Sha256 != sha256Hex([]byte("hello\n")) || res.ReplacedSha256 != "" {
		t.Errorf("result = %+v", res)
	}
	if res.VaultPath != vault {
		t.Errorf("VaultPath = %q, want %q", res.VaultPath, vault)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Errorf("perm = %o, want 0644", info.Mode().Perm())
	}
}

func TestCreate_RefusesExisting(t *testing.T) {
	vault := t.TempDir()
	p := filepath.Join(vault, "f.md")
	if err := os.WriteFile(p, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(vault, "f.md", "replacement"); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "original" {
		t.Errorf("existing file changed: %q", got)
	}
	// A directory at the name is "exists" too.
	if err := os.Mkdir(filepath.Join(vault, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(vault, "d", "x"); !errors.Is(err, ErrExists) {
		t.Errorf("directory: err = %v, want ErrExists", err)
	}
}

func TestCreate_RefusesDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	vault := t.TempDir()
	if err := os.Symlink(filepath.Join(vault, "nowhere"), filepath.Join(vault, "link.bak")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(vault, "link.bak", "x"); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists for a dangling symlink", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "nowhere")); !os.IsNotExist(err) {
		t.Errorf("Create wrote through a dangling symlink (err=%v)", err)
	}
}

func TestCreate_RefusesGitAndLockSegments(t *testing.T) {
	vault := t.TempDir()
	for _, rel := range []string{".git/x.bak", "a/.GIT/x.bak", ".vp-locks/x.bak"} {
		if _, err := Create(vault, rel, "x"); !errors.Is(err, ErrRefusedPath) {
			t.Errorf("%s: err = %v, want ErrRefusedPath", rel, err)
		}
	}
	if _, err := Create(vault, "Projects/p/tasks/t.md", "x"); err == nil {
		t.Error("a task file was created through Create")
	}
	if _, err := Create(vault, "../escape.bak", "x"); !errors.Is(err, ErrPathTraversal) {
		t.Errorf("traversal: err = %v", err)
	}
}

func TestCreate_RefusesSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	vault := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(vault, "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(vault, "out/x.bak", "x"); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("err = %v, want ErrSymlinkEscape", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "x.bak")); !os.IsNotExist(err) {
		t.Errorf("wrote outside the vault (err=%v)", err)
	}
}

// TestCreate_ConcurrentExactlyOneWins: the absence check runs inside the
// path's lock, so N concurrent creates of one name produce one winner and the
// file holds the winner's bytes.
func TestCreate_ConcurrentExactlyOneWins(t *testing.T) {
	vault := t.TempDir()
	const n = 16
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = Create(vault, "race.bak", string(rune('a'+i)))
		}(i)
	}
	wg.Wait()
	wins := 0
	winner := -1
	for i, err := range results {
		switch {
		case err == nil:
			wins++
			winner = i
		case !errors.Is(err, ErrExists):
			t.Errorf("goroutine %d: unexpected error %v", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d creates succeeded, want exactly 1", wins)
	}
	got, _ := os.ReadFile(filepath.Join(vault, "race.bak"))
	if string(got) != string(rune('a'+winner)) {
		t.Errorf("file = %q, want the winner's %q", got, string(rune('a'+winner)))
	}
}

// TestCreate_BakDoesNotStamp: a backup is not content the surface stamp
// tracks, so creating one under Templates/ writes no Templates/.surface.
func TestCreate_BakDoesNotStamp(t *testing.T) {
	vault := t.TempDir()
	if _, err := Create(vault, "Templates/commands/wrap.md.0123456789ab.bak", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vault, "Templates", ".surface")); !os.IsNotExist(err) {
		t.Errorf("Templates/.surface written by a .bak create (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "Templates", "commands", ".surface")); !os.IsNotExist(err) {
		t.Errorf("Templates/commands/.surface written by a .bak create (err=%v)", err)
	}
}
