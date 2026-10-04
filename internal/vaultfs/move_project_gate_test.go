// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// moveGateVault is a vault with an initialised project p holding
// Projects/p/notes/src.md, a phantom ph (memory/ only) and a departure record
// for q. fresh is absent.
func moveGateVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testutil.InitProject(t, root, "p")
	seedRaw(t, root, "Projects/p/notes/src.md", "body\n")
	seedRaw(t, root, "Projects/ph/memory/x.md", "m\n")
	seedRaw(t, root, "Audits/departures/q.json", `{"format":"1","slug":"q","kind":"deleted","date":"2026-10-01"}`+"\n")
	return root
}

// A move into an absent or phantom project is refused before the
// destination's directories are made: the source stays, and no project folder
// (or new directory under the phantom) appears.
func TestMoveRefusesUninitialisedDestination(t *testing.T) {
	for _, tc := range []struct{ to, wantAbsent string }{
		{"Projects/fresh/x.md", "Projects/fresh"},
		{"palace/fresh/kg/triples/x.json", "palace/fresh"},
		{"Projects/ph/notes/x.md", "Projects/ph/notes"},
	} {
		t.Run(tc.to, func(t *testing.T) {
			root := moveGateVault(t)
			_, err := Move(root, "Projects/p/notes/src.md", tc.to)
			if !errors.Is(err, projectdir.ErrUninitialisedProject) {
				t.Fatalf("err = %v, want ErrUninitialisedProject", err)
			}
			if absent(t, root, "Projects/p/notes/src.md") {
				t.Error("the refused move removed its source")
			}
			for _, rel := range []string{tc.wantAbsent, "Projects/fresh", "palace/fresh", "palace/ph"} {
				if !absent(t, root, rel) {
					t.Errorf("%s exists after a refused move", rel)
				}
			}
		})
	}
}

// The counterpart: a move within an initialised project lands.
func TestMoveAllowsInitialisedDestination(t *testing.T) {
	root := moveGateVault(t)
	if _, err := Move(root, "Projects/p/notes/src.md", "Projects/p/archive/dst.md"); err != nil {
		t.Fatalf("move into initialised project: %v", err)
	}
	if !absent(t, root, "Projects/p/notes/src.md") {
		t.Error("source still present after the move")
	}
	if got, err := os.ReadFile(filepath.Join(root, "Projects", "p", "archive", "dst.md")); err != nil || string(got) != "body\n" {
		t.Errorf("dst content = %q, %v", got, err)
	}
}

// A move into a departed project answers with the departure refusal, not the
// uninitialised one, though q is also absent.
func TestMoveDepartedRefusalWinsOverUninitialised(t *testing.T) {
	root := moveGateVault(t)
	_, err := Move(root, "Projects/p/notes/src.md", "Projects/q/x.md")
	if !errors.Is(err, ErrDepartedProject) {
		t.Fatalf("err = %v, want ErrDepartedProject", err)
	}
	if errors.Is(err, projectdir.ErrUninitialisedProject) {
		t.Errorf("err = %v also reads as ErrUninitialisedProject", err)
	}
	if !absent(t, root, "Projects/q") || absent(t, root, "Projects/p/notes/src.md") {
		t.Error("the refused move changed the vault")
	}
}
