// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package projectdir

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// gateVault is a vault with an initialised project p (scaffold markers), a
// phantom ph (memory/ only) and a non-directory Projects/file.
func gateVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"Projects/p/commands/README.md": "stub\n",
		"Projects/ph/memory/x.md":       "m\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Projects", "file"), []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A write into a project tree whose project is not initialised is refused;
// the refusal names both remedies.
func TestRefuseUninitialisedAbsRefuses(t *testing.T) {
	root := gateVault(t)
	for _, rel := range []string{
		"Projects/fresh/x.md",
		"Projects/fresh/sessions/a/b.md",
		"Projects/ph/notes/x.md",
		"palace/fresh/kg/triples/x.json",
		"palace/ph/drawers/w/r/drawers.jsonl",
		"projects/fresh/x.md", // the top directory is matched case-insensitively
		"Projects/file/x.md",  // Projects/file is not a directory: the classifier errs
	} {
		t.Run(rel, func(t *testing.T) {
			err := RefuseUninitialisedAbs(root, filepath.Join(root, filepath.FromSlash(rel)))
			if !errors.Is(err, ErrUninitialisedProject) {
				t.Fatalf("err = %v, want ErrUninitialisedProject", err)
			}
		})
	}
	err := RefuseUninitialisedAbs(root, filepath.Join(root, "Projects", "fresh", "x.md"))
	for _, want := range []string{"vp init <checkout>", "vp_init"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %v does not name %q", err, want)
		}
	}
}

// Writes outside the project trees, into an initialised project, or with no
// vault root, are not judged.
func TestRefuseUninitialisedAbsAllows(t *testing.T) {
	root := gateVault(t)
	for _, rel := range []string{
		"Projects/p/notes/x.md",
		"palace/p/kg/triples/x.json",
		"palace/.local/index/fresh/chunks.jsonl",
		"Audits/departures/fresh.json",
		"Projects/config.toml", // a FILE directly under Projects/ creates no project
		"Projects/README.md",
		"Projects/fresh", // as a file write: only RefuseUninitialisedDirAbs judges the directory itself
		"Templates/commands/wrap.md",
		".gitignore",
	} {
		t.Run(rel, func(t *testing.T) {
			if err := RefuseUninitialisedAbs(root, filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
	if err := RefuseUninitialisedAbs("", filepath.Join(root, "Projects", "fresh", "x.md")); err != nil {
		t.Errorf("host-local (vaultRoot \"\") write refused: %v", err)
	}
	if err := RefuseUninitialisedAbs(root, filepath.Join(t.TempDir(), "Projects", "fresh", "x.md")); err != nil {
		t.Errorf("a path outside the vault was judged: %v", err)
	}
}

// Creating a directory is judged one level higher: Projects/<slug> itself is
// the phantom project folder a refused write must not leave behind.
func TestRefuseUninitialisedDirAbs(t *testing.T) {
	root := gateVault(t)
	for _, rel := range []string{"Projects/fresh", "Projects/ph", "palace/fresh", "Projects/fresh/sessions"} {
		if err := RefuseUninitialisedDirAbs(root, filepath.Join(root, filepath.FromSlash(rel))); !errors.Is(err, ErrUninitialisedProject) {
			t.Errorf("%s: err = %v, want ErrUninitialisedProject", rel, err)
		}
	}
	for _, rel := range []string{"Projects", "palace", "Projects/p", "Projects/p/sessions", "palace/.local", "Audits"} {
		if err := RefuseUninitialisedDirAbs(root, filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s: err = %v, want nil", rel, err)
		}
	}
}

// A vault reached through a symlinked root is judged by where the write lands:
// the lexical path is outside the root as given, and the resolved one is
// inside.
func TestRefuseUninitialisedAbsSymlinkedVaultRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	real := gateVault(t)
	link := filepath.Join(t.TempDir(), "vault-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := RefuseUninitialisedAbs(link, filepath.Join(real, "Projects", "fresh", "x.md")); !errors.Is(err, ErrUninitialisedProject) {
		t.Errorf("real path under a symlinked root: err = %v, want ErrUninitialisedProject", err)
	}
	if err := RefuseUninitialisedAbs(real, filepath.Join(link, "Projects", "fresh", "x.md")); !errors.Is(err, ErrUninitialisedProject) {
		t.Errorf("link path under the real root: err = %v, want ErrUninitialisedProject", err)
	}
	if err := RefuseUninitialisedAbs(link, filepath.Join(real, "Projects", "p", "x.md")); err != nil {
		t.Errorf("initialised project under a symlinked root refused: %v", err)
	}
}
