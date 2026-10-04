// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package atomicfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// gateVault is a vault with an initialised project p, a phantom ph (memory/
// only) and a departure record for q. fresh is absent.
func gateVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testutil.InitProject(t, root, "p")
	for rel, body := range map[string]string{
		"Projects/ph/memory/x.md":  "m\n",
		"Audits/departures/q.json": `{"format":"1","slug":"q","kind":"deleted","date":"2026-10-01"}` + "\n",
	} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// treeSnapshot lists every entry under root, vault-relative.
func treeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// gateWriters are the two exported entries that reach writeAtomic.
var gateWriters = map[string]func(root, abs string, opts ...Option) error{
	"Write": func(root, abs string, opts ...Option) error {
		return Write(root, abs, []byte("x\n"), opts...)
	},
	"WriteStream": func(root, abs string, opts ...Option) error {
		return WriteStream(root, abs, func(w io.Writer) error {
			_, err := io.WriteString(w, "x\n")
			return err
		}, opts...)
	},
}

// A write into an absent or phantom project is refused with
// ErrUninitialisedProject and leaves the vault exactly as it was: no
// Projects/<slug>/, no palace/<slug>/, no stamp, no new directory under the
// phantom.
func TestWriteRefusesUninitialisedProject(t *testing.T) {
	for name, write := range gateWriters {
		for _, rel := range []string{
			"Projects/fresh/x.md",
			"palace/fresh/kg/triples/x.json",
			"Projects/ph/notes/y.md",
			"palace/ph/kg/triples/x.json",
		} {
			t.Run(name+"/"+rel, func(t *testing.T) {
				root := gateVault(t)
				before := treeSnapshot(t, root)
				err := write(root, filepath.Join(root, filepath.FromSlash(rel)))
				if !errors.Is(err, projectdir.ErrUninitialisedProject) {
					t.Fatalf("err = %v, want ErrUninitialisedProject", err)
				}
				if after := treeSnapshot(t, root); !reflect.DeepEqual(before, after) {
					t.Errorf("refused write changed the vault:\nbefore %v\nafter  %v", before, after)
				}
				for _, dir := range []string{"Projects/fresh", "palace/fresh", "palace/ph"} {
					if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(dir))); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("%s exists after a refused write (err %v)", dir, err)
					}
				}
			})
		}
	}
}

// The counterpart: the same writes into an initialised project land.
func TestWriteAllowsInitialisedProject(t *testing.T) {
	for name, write := range gateWriters {
		for _, rel := range []string{"Projects/p/x.md", "palace/p/kg/triples/x.json"} {
			t.Run(name+"/"+rel, func(t *testing.T) {
				root := gateVault(t)
				abs := filepath.Join(root, filepath.FromSlash(rel))
				if err := write(root, abs); err != nil {
					t.Fatalf("write into initialised project: %v", err)
				}
				if got, err := os.ReadFile(abs); err != nil || string(got) != "x\n" {
					t.Errorf("content = %q, %v", got, err)
				}
			})
		}
	}
}

// A departed project answers with the departure refusal, not the
// uninitialised one, even though q is also absent.
func TestWriteDepartedRefusalWinsOverUninitialised(t *testing.T) {
	for name, write := range gateWriters {
		for _, rel := range []string{"Projects/q/x.md", "palace/q/kg/triples/x.json"} {
			t.Run(name+"/"+rel, func(t *testing.T) {
				root := gateVault(t)
				err := write(root, filepath.Join(root, filepath.FromSlash(rel)))
				if !errors.Is(err, departedpath.ErrDeparted) {
					t.Fatalf("err = %v, want ErrDeparted", err)
				}
				if errors.Is(err, projectdir.ErrUninitialisedProject) {
					t.Errorf("err = %v also reads as ErrUninitialisedProject", err)
				}
				for _, dir := range []string{"Projects/q", "palace/q"} {
					if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(dir))); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("%s exists after a refused write (err %v)", dir, err)
					}
				}
			})
		}
	}
}

// CreatingProject admits a write into an absent project: it is the write that
// creates it.
func TestWriteCreatingProjectAdmitsAbsentProject(t *testing.T) {
	for name, write := range gateWriters {
		t.Run(name, func(t *testing.T) {
			root := gateVault(t)
			abs := filepath.Join(root, "Projects", "fresh", "commands", "README.md")
			if err := write(root, abs, CreatingProject()); err != nil {
				t.Fatalf("creating-project write refused: %v", err)
			}
			if got, err := os.ReadFile(abs); err != nil || string(got) != "x\n" {
				t.Errorf("content = %q, %v", got, err)
			}
		})
	}
}

// CreatingProject lifts only the uninitialised gate: a departed project still
// refuses.
func TestWriteCreatingProjectStillRefusesDeparted(t *testing.T) {
	for name, write := range gateWriters {
		t.Run(name, func(t *testing.T) {
			root := gateVault(t)
			err := write(root, filepath.Join(root, "Projects", "q", "x.md"), CreatingProject())
			if !errors.Is(err, departedpath.ErrDeparted) {
				t.Fatalf("err = %v, want ErrDeparted", err)
			}
			if !strings.Contains(err.Error(), "q") {
				t.Errorf("refusal %v does not name the project", err)
			}
		})
	}
}
