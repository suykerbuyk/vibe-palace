// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// projGateVault is a vault with an initialised project p, a phantom ph
// (memory/ only) and a departure record for q. fresh is absent.
func projGateVault(t *testing.T) *Vault {
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
	return NewVault(root)
}

// projGateSnapshot lists every entry under root, vault-relative.
func projGateSnapshot(t *testing.T, root string) []string {
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

func projGateAbsent(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a refused write (err %v)", rel, err)
		}
	}
}

// The append primitive refuses a file in an absent or phantom project. It is
// called directly: every exported append makes its directory first through
// EnsureVaultDir, which refuses on its own, so only a direct call pins the
// primitive's gate. ph/memory/ already exists, so without the gate the append
// to it would create the file.
func TestAppendUnderLockRefusesUninitialisedProject(t *testing.T) {
	for _, rel := range []string{
		"Projects/fresh/x.md",
		"palace/fresh/kg/entities.jsonl",
		"Projects/ph/memory/y.md",
	} {
		t.Run(rel, func(t *testing.T) {
			v := projGateVault(t)
			before := projGateSnapshot(t, v.Root)
			err := v.appendUnderLock(filepath.Join(v.Root, filepath.FromSlash(rel)), []byte("line\n"))
			if !errors.Is(err, projectdir.ErrUninitialisedProject) {
				t.Fatalf("err = %v, want ErrUninitialisedProject", err)
			}
			if after := projGateSnapshot(t, v.Root); !reflect.DeepEqual(before, after) {
				t.Errorf("refused append changed the vault:\nbefore %v\nafter  %v", before, after)
			}
			projGateAbsent(t, v.Root, "Projects/fresh", "palace/fresh", "palace/ph")
		})
	}
}

// The exported appends (a drawer, a KG entity) into an absent or phantom
// project are refused and leave nothing behind; into an initialised one they
// land.
func TestExportedAppendsHonourTheProjectGate(t *testing.T) {
	appends := map[string]func(v *Vault, project string) error{
		"AppendDrawer": func(v *Vault, project string) error {
			return v.AppendDrawer(project, "wing-a", "room-1", Drawer{
				Hall: "facts", Content: "c", SourceType: "session", FiledAt: "2026-10-01T00:00:00Z",
			})
		},
		"AddEntity": func(v *Vault, project string) error {
			return v.AddEntity(project, Entity{ID: "e1", Name: "Kai", Type: "person", CreatedAt: "2026-10-01T00:00:00Z"})
		},
	}
	for name, add := range appends {
		for _, project := range []string{"fresh", "ph"} {
			t.Run(name+"/"+project, func(t *testing.T) {
				v := projGateVault(t)
				before := projGateSnapshot(t, v.Root)
				if err := add(v, project); !errors.Is(err, projectdir.ErrUninitialisedProject) {
					t.Fatalf("err = %v, want ErrUninitialisedProject", err)
				}
				if after := projGateSnapshot(t, v.Root); !reflect.DeepEqual(before, after) {
					t.Errorf("refused append changed the vault:\nbefore %v\nafter  %v", before, after)
				}
				projGateAbsent(t, v.Root, "Projects/fresh", "palace/fresh", "palace/ph")
			})
		}
		t.Run(name+"/p", func(t *testing.T) {
			v := projGateVault(t)
			if err := add(v, "p"); err != nil {
				t.Fatalf("append into initialised project: %v", err)
			}
		})
	}
}

// EnsureVaultDir refuses to make a directory in an absent or phantom project,
// Projects/<slug> itself included, and makes nothing.
func TestEnsureVaultDirRefusesUninitialisedProject(t *testing.T) {
	for _, rel := range []string{
		"Projects/fresh/sessions",
		"palace/fresh/kg",
		"Projects/fresh",
		"Projects/ph/sessions",
	} {
		t.Run(rel, func(t *testing.T) {
			v := projGateVault(t)
			before := projGateSnapshot(t, v.Root)
			err := v.EnsureVaultDir(filepath.Join(v.Root, filepath.FromSlash(rel)))
			if !errors.Is(err, projectdir.ErrUninitialisedProject) {
				t.Fatalf("err = %v, want ErrUninitialisedProject", err)
			}
			if after := projGateSnapshot(t, v.Root); !reflect.DeepEqual(before, after) {
				t.Errorf("refused mkdir changed the vault:\nbefore %v\nafter  %v", before, after)
			}
			projGateAbsent(t, v.Root, "Projects/fresh", "palace/fresh", "palace/ph")
		})
	}
}

func TestEnsureVaultDirAllowsInitialisedProject(t *testing.T) {
	v := projGateVault(t)
	for _, rel := range []string{"Projects/p/sessions", "palace/p/kg"} {
		abs := filepath.Join(v.Root, filepath.FromSlash(rel))
		if err := v.EnsureVaultDir(abs); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			t.Errorf("%s not made: %v", rel, err)
		}
	}
}

// A departed project answers every storage primitive with the departure
// refusal, not the uninitialised one, though q is also absent.
func TestStorageDepartedRefusalWinsOverUninitialised(t *testing.T) {
	v := projGateVault(t)
	check := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, departedpath.ErrDeparted) {
			t.Errorf("%s: err = %v, want ErrDeparted", what, err)
		}
		if errors.Is(err, projectdir.ErrUninitialisedProject) {
			t.Errorf("%s: err = %v also reads as ErrUninitialisedProject", what, err)
		}
	}
	check("appendUnderLock", v.appendUnderLock(filepath.Join(v.Root, "Projects", "q", "x.md"), []byte("l\n")))
	check("appendUnderLock palace", v.appendUnderLock(filepath.Join(v.Root, "palace", "q", "kg", "entities.jsonl"), []byte("l\n")))
	check("EnsureVaultDir sessions", v.EnsureVaultDir(filepath.Join(v.Root, "Projects", "q", "sessions")))
	check("EnsureVaultDir kg", v.EnsureVaultDir(filepath.Join(v.Root, "palace", "q", "kg")))
	check("EnsureVaultDir root", v.EnsureVaultDir(filepath.Join(v.Root, "Projects", "q")))
	check("AppendDrawer", v.AppendDrawer("q", "wing-a", "room-1", Drawer{Hall: "facts", Content: "c", SourceType: "session", FiledAt: "2026-10-01T00:00:00Z"}))
	projGateAbsent(t, v.Root, "Projects/q", "palace/q")
}

// CopyProjectTreeEntry creates the destination project: a copy into a vault
// where the project is absent lands, in both trees.
func TestCopyProjectTreeEntryCreatesAbsentDestination(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	for _, rel := range []string{"Projects/fresh/notes/a.md", "palace/fresh/kg/entities.jsonl"} {
		body := []byte("copied " + rel + "\n")
		abs := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, body, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		e := ProjectTreeEntry{Path: rel, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
		if err := CopyProjectTreeEntry(src, dest, e); err != nil {
			t.Fatalf("copy %s into an absent destination project: %v", rel, err)
		}
		if got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel))); err != nil || string(got) != string(body) {
			t.Errorf("%s: dest content = %q, %v", rel, got, err)
		}
	}
}
