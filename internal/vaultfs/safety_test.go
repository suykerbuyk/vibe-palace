// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRelPath_RejectsAbsolute(t *testing.T) {
	cases := []string{"/foo", "/", "//foo"}
	for _, c := range cases {
		if err := ValidateRelPath(c); err == nil {
			t.Errorf("ValidateRelPath(%q): want error, got nil", c)
		} else if !errors.Is(err, ErrPathTraversal) {
			t.Errorf("ValidateRelPath(%q): want ErrPathTraversal, got %v", c, err)
		}
	}
}

func TestValidateRelPath_RejectsDotDotSegment(t *testing.T) {
	cases := []string{"../foo", "foo/../bar", "foo/.."}
	for _, c := range cases {
		if err := ValidateRelPath(c); err == nil {
			t.Errorf("ValidateRelPath(%q): want error, got nil", c)
		} else if !errors.Is(err, ErrPathTraversal) {
			t.Errorf("ValidateRelPath(%q): want ErrPathTraversal, got %v", c, err)
		}
	}
}

func TestValidateRelPath_RejectsNullBytes(t *testing.T) {
	if err := ValidateRelPath("foo\x00bar"); err == nil {
		t.Fatal("expected error for null byte path")
	} else if !errors.Is(err, ErrPathTraversal) {
		t.Errorf("want ErrPathTraversal, got %v", err)
	}
}

func TestValidateRelPath_RejectsControlChars(t *testing.T) {
	cases := []string{"foo\x01bar", "\x7f"}
	for _, c := range cases {
		if err := ValidateRelPath(c); err == nil {
			t.Errorf("ValidateRelPath(%q): want error, got nil", c)
		} else if !errors.Is(err, ErrPathTraversal) {
			t.Errorf("ValidateRelPath(%q): want ErrPathTraversal, got %v", c, err)
		}
	}
}

func TestValidateRelPath_RejectsEmpty(t *testing.T) {
	if err := ValidateRelPath(""); err == nil {
		t.Fatal("expected error for empty path")
	} else if !errors.Is(err, ErrPathTraversal) {
		t.Errorf("want ErrPathTraversal, got %v", err)
	}
}

func TestValidateRelPath_RejectsDot(t *testing.T) {
	cases := []string{".", "./", "./."}
	for _, c := range cases {
		if err := ValidateRelPath(c); err == nil {
			t.Errorf("ValidateRelPath(%q): want error, got nil", c)
		} else if !errors.Is(err, ErrPathTraversal) {
			t.Errorf("ValidateRelPath(%q): want ErrPathTraversal, got %v", c, err)
		}
	}
}

func TestValidateRelPath_AcceptsTypicalRelative(t *testing.T) {
	cases := []string{
		"Projects/vibe-palace/agentctx/notes/foo.md",
		"Knowledge/learnings/bar.md",
		"Templates/agentctx/commands/wrap.md",
	}
	for _, c := range cases {
		if err := ValidateRelPath(c); err != nil {
			t.Errorf("ValidateRelPath(%q): want nil, got %v", c, err)
		}
	}
}

func TestValidateRelPath_AcceptsHiddenFile(t *testing.T) {
	if err := ValidateRelPath("Projects/vibe-palace/.foo"); err != nil {
		t.Errorf("ValidateRelPath: want nil for non-.git dotfile, got %v", err)
	}
}

func TestResolveSafePath_HappyPath(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(vault, "Projects", "x", "f.md")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveSafePath(vault, "Projects/x/f.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantReal, _ := filepath.EvalSymlinks(target)
	if got != wantReal {
		t.Errorf("ResolveSafePath: got %q, want %q", got, wantReal)
	}
}

func TestResolveSafePath_RealpathStaysUnderVault(t *testing.T) {
	vault := t.TempDir()
	innerDir := filepath.Join(vault, "real")
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	innerFile := filepath.Join(innerDir, "f.md")
	if err := os.WriteFile(innerFile, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// symlink -> ./real/f.md (within vault)
	link := filepath.Join(vault, "link.md")
	if err := os.Symlink(innerFile, link); err != nil {
		t.Skip("symlinks unsupported on this platform")
	}
	got, err := ResolveSafePath(vault, "link.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantReal, _ := filepath.EvalSymlinks(innerFile)
	if got != wantReal {
		t.Errorf("ResolveSafePath: got %q, want %q", got, wantReal)
	}
}

func TestResolveSafePath_RejectsSymlinkEscape_RealpathBased(t *testing.T) {
	vault := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(vault, "leak.md")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skip("symlinks unsupported on this platform")
	}
	_, err := ResolveSafePath(vault, "leak.md")
	if err == nil {
		t.Fatal("expected ErrSymlinkEscape, got nil")
	}
	if !errors.Is(err, ErrSymlinkEscape) {
		t.Errorf("want ErrSymlinkEscape, got %v", err)
	}
}

func TestResolveSafePath_RejectsAfterClean(t *testing.T) {
	vault := t.TempDir()
	// ValidateRelPath catches ".." at the input layer; verify ResolveSafePath
	// returns ErrPathTraversal (defense-in-depth).
	_, err := ResolveSafePath(vault, "foo/../../etc/passwd")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrPathTraversal) {
		t.Errorf("want ErrPathTraversal, got %v", err)
	}
}

func TestIsRefusedWritePath_RejectsGitSegmentTopLevel(t *testing.T) {
	if !IsRefusedWritePath(".git/HEAD") {
		t.Error("IsRefusedWritePath(.git/HEAD): want true, got false")
	}
}

func TestIsRefusedWritePath_RejectsGitSegmentNested(t *testing.T) {
	if !IsRefusedWritePath("Projects/foo/.git/config") {
		t.Error("want true for nested .git segment")
	}
}

func TestIsRefusedWritePath_RejectsGitSegmentCaseInsensitive(t *testing.T) {
	cases := []string{".GIT/HEAD", "Projects/foo/.Git/foo", ".gIt/x"}
	for _, c := range cases {
		if !IsRefusedWritePath(c) {
			t.Errorf("IsRefusedWritePath(%q): want true, got false", c)
		}
	}
}

func TestIsRefusedWritePath_AllowsSubstringNotSegment(t *testing.T) {
	if IsRefusedWritePath("Projects/foo/foo.git/bar") {
		t.Error("want false for substring .git (not a full segment)")
	}
}

func TestIsRefusedWritePath_AllowsGitignore(t *testing.T) {
	cases := []string{".gitignore", "Projects/foo/.gitignore"}
	for _, c := range cases {
		if IsRefusedWritePath(c) {
			t.Errorf("IsRefusedWritePath(%q): want false, got true", c)
		}
	}
}

func TestIsRefusedWritePath_AllowsHiddenNonGit(t *testing.T) {
	cases := []string{".cache/foo", ".foo"}
	for _, c := range cases {
		if IsRefusedWritePath(c) {
			t.Errorf("IsRefusedWritePath(%q): want false, got true", c)
		}
	}
}

func TestIsRefusedWritePath_RejectsVpLocksSegment(t *testing.T) {
	cases := []string{
		".vp-locks/abc.lock",
		"Projects/x/.vp-locks/y",
		".VP-LOCKS/abc.lock",
		"Projects/x/.Vp-Locks/y",
	}
	for _, c := range cases {
		if !IsRefusedWritePath(c) {
			t.Errorf("IsRefusedWritePath(%q): want true, got false", c)
		}
	}
}

func TestIsRefusedWritePath_AllowsVpLocksSubstringNotSegment(t *testing.T) {
	if IsRefusedWritePath("Projects/foo/foo.vp-locks/bar") {
		t.Error("want false for substring .vp-locks (not a full segment)")
	}
}
