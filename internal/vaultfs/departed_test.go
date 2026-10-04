// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

const departedLabel = "git@gitlab.example.com:q/vibe-palace-vault.git"

// departedVault is a vault root holding a departure record for p (written
// directly, as a pull would deliver it) and no Projects/p tree.
func departedVault(t *testing.T, recordJSON string) string {
	t.Helper()
	root := t.TempDir()
	rec := filepath.Join(root, "Audits", "departures", "p.json")
	if err := os.MkdirAll(filepath.Dir(rec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte(recordJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func movedRecord() string {
	return `{"format":"1","slug":"p","kind":"moved-to-vault","to":"` + departedLabel + `","date":"2026-09-27"}` + "\n"
}

func seedRaw(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func absent(t *testing.T, root, rel string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return errors.Is(err, os.ErrNotExist)
}

func wantDepartedRefusal(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrDepartedProject) || !errors.Is(err, ErrRefusedPath) {
		t.Fatalf("err = %v, want ErrDepartedProject (an ErrRefusedPath)", err)
	}
	msg := err.Error()
	for _, want := range []string{departedLabel, "vp vault clone " + departedLabel, "--bind p", "vp config bind p"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal does not name %q: %s", want, msg)
		}
	}
}

// Every write entry point refuses a path under a departed project's trees, and
// creates nothing.
func TestDepartedProject_EveryEntryPointRefuses(t *testing.T) {
	t.Run("Write Projects", func(t *testing.T) {
		root := departedVault(t, movedRecord())
		_, err := Write(root, "Projects/p/resume.md", "stale\n", "")
		wantDepartedRefusal(t, err)
		if !absent(t, root, "Projects/p") {
			t.Fatal("Write re-created Projects/p")
		}
	})
	t.Run("Write palace", func(t *testing.T) {
		root := departedVault(t, movedRecord())
		_, err := Write(root, "palace/p/drawers.jsonl", "{}\n", "")
		wantDepartedRefusal(t, err)
		if !absent(t, root, "palace/p") {
			t.Fatal("Write re-created palace/p")
		}
	})
	t.Run("Create", func(t *testing.T) {
		root := departedVault(t, movedRecord())
		_, err := Create(root, "Projects/p/new.md", "x\n")
		wantDepartedRefusal(t, err)
		if !absent(t, root, "Projects/p") {
			t.Fatal("Create re-created Projects/p")
		}
	})
	t.Run("Edit", func(t *testing.T) {
		root := departedVault(t, movedRecord())
		seedRaw(t, root, "Projects/p/resume.md.bak", "old\n") // residue a pull left
		_, err := Edit(root, "Projects/p/resume.md.bak", "old", "new", false, "")
		wantDepartedRefusal(t, err)
		if got, _ := os.ReadFile(filepath.Join(root, "Projects/p/resume.md.bak")); string(got) != "old\n" {
			t.Fatalf("Edit changed the file: %q", got)
		}
	})
	t.Run("Move into", func(t *testing.T) {
		root := departedVault(t, movedRecord())
		seedRaw(t, root, "Projects/other/notes.md", "n\n")
		_, err := Move(root, "Projects/other/notes.md", "Projects/p/notes.md")
		wantDepartedRefusal(t, err)
		if !absent(t, root, "Projects/p") || absent(t, root, "Projects/other/notes.md") {
			t.Fatal("Move into the departed tree ran")
		}
	})
	t.Run("Move out", func(t *testing.T) {
		root := departedVault(t, movedRecord())
		seedRaw(t, root, "Projects/p/leftover.md", "l\n")
		_, err := Move(root, "Projects/p/leftover.md", "Projects/other/leftover.md")
		wantDepartedRefusal(t, err)
		if absent(t, root, "Projects/p/leftover.md") || !absent(t, root, "Projects/other/leftover.md") {
			t.Fatal("Move out of the departed tree ran")
		}
	})
}

// The scope is the departed project's two trees only: another project, the
// vault root, and palace/.local are untouched by the rule; the tree root and a
// case-variant top directory are in it.
func TestDepartedProject_Scope(t *testing.T) {
	root := departedVault(t, movedRecord())
	// q and pq are live projects beside the departed p; p stays uninitialised.
	testutil.InitProject(t, root, "q", "pq")
	for _, ok := range []string{"Projects/q/x.md", "palace/q/x.md", "notes.md", "Projects/pq/x.md", "palace/.local/p/x"} {
		if _, err := Write(root, ok, "x\n", ""); err != nil {
			t.Fatalf("%s refused: %v", ok, err)
		}
	}
	for _, refused := range []string{"Projects/p", "projects/p/x.md", "PALACE/p/x.md", "Projects/./p/x.md"} {
		if _, err := Write(root, refused, "x\n", ""); !errors.Is(err, ErrDepartedProject) {
			t.Fatalf("%s not refused: %v", refused, err)
		}
	}
}

// The refusal names where the project went for every kind, and a record that
// cannot be read still refuses.
func TestDepartedProject_MessagesAndMalformed(t *testing.T) {
	cases := map[string]string{
		`{"format":"1","slug":"p","kind":"renamed","to":"p2","date":"2026-09-27"}`: `renamed to "p2"`,
		`{"format":"1","slug":"p","kind":"deleted","to":"","date":"2026-09-27"}`:   "revert the delete commit",
		`{not json`: "cannot be read",
		`{"format":"1","slug":"p","kind":"moved-to-vault","to":"","date":"2026-09-27"}`: "an unrecorded vault",
	}
	for rec, want := range cases {
		root := departedVault(t, rec)
		_, err := Write(root, "Projects/p/x.md", "x\n", "")
		if !errors.Is(err, ErrDepartedProject) || !strings.Contains(err.Error(), want) {
			t.Fatalf("record %s: err = %v, want it to say %q", rec, err, want)
		}
	}
}

// Delete stays ungated: removing a leftover mutates no project.
func TestDepartedProject_DeleteStaysAllowed(t *testing.T) {
	root := departedVault(t, movedRecord())
	seedRaw(t, root, "Projects/p/resume.md.bak", "old\n")
	if _, err := Delete(root, "Projects/p/resume.md.bak", ""); err != nil {
		t.Fatalf("Delete of a leftover refused: %v", err)
	}
}
