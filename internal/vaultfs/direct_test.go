// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckDirectPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on Windows")
	}
	put := func(t *testing.T, root, rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(t *testing.T, target, at string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name     string
		setup    func(t *testing.T, vault string) (root string)
		rel      string
		indirect string // "" = nil expected; otherwise a substring of the refusal
	}{
		{"clean path", func(t *testing.T, v string) string {
			put(t, v, "Templates/commands/wrap.md", "x\n")
			return v
		}, "Templates/commands/wrap.md", ""},
		{"absent file", func(t *testing.T, v string) string {
			put(t, v, "Templates/commands/other.md", "x\n")
			return v
		}, "Templates/commands/wrap.md", ""},
		{"absent directory", func(t *testing.T, v string) string { return v }, "Templates/skills/chair/SKILL.md", ""},
		{"the file is a link", func(t *testing.T, v string) string {
			put(t, v, "Elsewhere/wrap.md", "x\n")
			link(t, filepath.Join(v, "Elsewhere", "wrap.md"), filepath.Join(v, "Templates", "commands", "wrap.md"))
			return v
		}, "Templates/commands/wrap.md", "reached through a symlink (Templates/commands/wrap.md)"},
		{"a middle directory is a link", func(t *testing.T, v string) string {
			put(t, v, "Projects/p/skills/chair/SKILL.md", "x\n")
			link(t, filepath.Join(v, "Projects", "p", "skills", "chair"), filepath.Join(v, "Templates", "skills", "chair"))
			return v
		}, "Templates/skills/chair/SKILL.md", "reached through a symlink (Templates/skills/chair)"},
		{"Templates/commands is a link", func(t *testing.T, v string) string {
			put(t, v, "Elsewhere/wrap.md", "x\n")
			link(t, filepath.Join(v, "Elsewhere"), filepath.Join(v, "Templates", "commands"))
			return v
		}, "Templates/commands/wrap.md", "reached through a symlink (Templates/commands)"},
		{"a directory where the file belongs", func(t *testing.T, v string) string {
			if err := os.MkdirAll(filepath.Join(v, "Templates", "commands", "wrap.md"), 0o755); err != nil {
				t.Fatal(err)
			}
			return v
		}, "Templates/commands/wrap.md", "not a regular file"},
		{"a special file", func(t *testing.T, v string) string {
			if err := os.MkdirAll(filepath.Join(v, "Templates", "commands"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := mkfifo(filepath.Join(v, "Templates", "commands", "wrap.md")); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
			return v
		}, "Templates/commands/wrap.md", "not a regular file"},
		{"the vault root is itself a link", func(t *testing.T, v string) string {
			put(t, v, "Templates/commands/wrap.md", "x\n")
			at := filepath.Join(t.TempDir(), "vault-link")
			link(t, v, at)
			return at
		}, "Templates/commands/wrap.md", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.setup(t, t.TempDir())
			err := CheckDirectPath(root, tc.rel)
			if tc.indirect == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrIndirectPath) || !strings.Contains(err.Error(), tc.indirect) {
				t.Fatalf("err = %v, want an ErrIndirectPath naming %q", err, tc.indirect)
			}
			if strings.Contains(err.Error(), ErrIndirectPath.Error()) {
				t.Errorf("the refusal carries the sentinel's text, which callers wrap in their own: %v", err)
			}
		})
	}

	if err := CheckDirectPath(filepath.Join(t.TempDir(), "missing"), "Templates/x.md"); err == nil || errors.Is(err, ErrIndirectPath) {
		t.Errorf("an unresolvable vault root: err = %v, want a plain error", err)
	}
}
