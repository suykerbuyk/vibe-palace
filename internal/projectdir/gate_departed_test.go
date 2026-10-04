// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package projectdir

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
)

// A departed project is refused with the departure sentinel, not this gate's,
// whether its directory is absent or still holds a re-scaffold: the record
// wins over the directory.
func TestRefuseUninitialisedAnswersDepartedFirst(t *testing.T) {
	root := gateVault(t)
	for rel, body := range map[string]string{
		"Audits/departures/q.json":  `{"format":"1","slug":"q","kind":"deleted","date":"2026-10-01"}` + "\n",
		"Audits/departures/p.json":  `{"format":"1","slug":"p","kind":"deleted","date":"2026-10-01"}` + "\n",
		"Audits/departures/ph.json": `{"format":"1","slug":"ph","kind":"deleted","date":"2026-10-01"}` + "\n",
	} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	type refuse func(vaultRoot, abs string) error
	for name, fn := range map[string]refuse{
		"RefuseUninitialisedAbs":    RefuseUninitialisedAbs,
		"RefuseUninitialisedDirAbs": RefuseUninitialisedDirAbs,
	} {
		for _, rel := range []string{
			"Projects/q/x.md",            // absent
			"palace/q/kg/triples/x.json", // absent, palace tree
			"Projects/ph/notes/x.md",     // phantom
			"Projects/p/notes/x.md",      // initialised, but the record wins
		} {
			t.Run(name+"/"+rel, func(t *testing.T) {
				err := fn(root, filepath.Join(root, filepath.FromSlash(rel)))
				if !errors.Is(err, departedpath.ErrDeparted) {
					t.Fatalf("err = %v, want departedpath.ErrDeparted", err)
				}
				if errors.Is(err, ErrUninitialisedProject) {
					t.Errorf("err = %v also reads as ErrUninitialisedProject", err)
				}
			})
		}
	}
	if err := RefuseUninitialisedDirAbs(root, filepath.Join(root, "Projects", "q")); !errors.Is(err, departedpath.ErrDeparted) {
		t.Errorf("Projects/q itself: err = %v, want departedpath.ErrDeparted", err)
	}
}
