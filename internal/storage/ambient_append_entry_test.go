// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// These rows pin the append entry paths against the under-test tripwire: a test
// that appends to the operator's configured vault must be refused BEFORE
// anything is created in it — no .vp-locks/, no lock file, no project directory.
//
// 🔴 NEVER THE REAL VAULT: the "ambient" vault is a t.TempDir() installed with
// surface.SetAmbientVaultRoot. The assertion that discriminates is the ABSENCE of
// the directories; each row has a control against a non-ambient initialised vault
// that DOES create them, so an absent directory cannot mean "the call never ran".

func ambientPanicText(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

func ambientPathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// appendEntryPaths is every append entry path the census names that does
// EnsureVaultDir and then vaultlock.Acquire, plus the iteration summary and KG
// writers, each as a closure over a vault and project "p".
func appendEntryPaths() map[string]func(v *Vault) error {
	return map[string]func(v *Vault) error{
		"AppendIterationOwned (project_dirs.go)": func(v *Vault) error {
			_, _, err := v.AppendIterationOwned("p", "title", "body", nil)
			return err
		},
		"ArchiveCommitBodies (commit_log.go)": func(v *Vault) error {
			_, _, err := v.ArchiveCommitBodies("p", []wrapstate.CommitInfo{{SHA: "abc1234", Subject: "s", Body: "b"}}, "abc1234")
			return err
		},
		"AddEntities (knowledge_graph.go)": func(v *Vault) error {
			_, err := v.AddEntities("p", []Entity{{ID: "e1", Name: "e", Type: "t", CreatedAt: "2026-11-19T00:00:00Z"}})
			return err
		},
		"AppendDrawers (drawers.go)": func(v *Vault) error {
			_, err := v.AppendDrawers("p", "w", "r", []Drawer{{ID: "d1", Hall: "h", Content: "c", SourceType: "t", FiledAt: "2026-11-19T00:00:00Z"}})
			return err
		},
		"WriteIterationSummary (iteration_summary.go)": func(v *Vault) error {
			return v.WriteIterationSummary("p", IterationSummary{N: 1, Summary: "s", Model: "m", GeneratedAt: "2026-11-19T00:00:00Z"})
		},
		"AddAuthoredTriple (kg_origin.go)": func(v *Vault) error {
			return v.AddAuthoredTriple("p", Triple{Subject: "a", Predicate: "b", Object: "c"})
		},
	}
}

// S1 (carries M5, not M1): each append entry path refuses an ambient root with
// nothing created, and works on a non-ambient initialised vault.
func TestAmbientAppendEntryPathsRefuseBeforeCreatingAnything(t *testing.T) {
	for name, run := range appendEntryPaths() {
		t.Run(name, func(t *testing.T) {
			ctl := t.TempDir()
			testutil.InitProject(t, ctl, "p")
			if err := run(bornCurrentVault(t, ctl)); err != nil {
				t.Fatalf("control on a non-ambient initialised vault failed: %v", err)
			}
			if !ambientPathExists(filepath.Join(ctl, ".vp-locks")) {
				t.Fatal("control did not create .vp-locks: the row cannot discriminate")
			}

			amb := t.TempDir()
			v := bornCurrentVault(t, amb)
			t.Cleanup(surface.SetAmbientVaultRoot(amb))
			before := ambientDirEntries(t, amb)
			msg := ambientPanicText(func() { _ = run(v) })
			if !strings.Contains(msg, "CONFIGURED vault") {
				t.Fatalf("no tripwire refusal, got %q", msg)
			}
			if ambientPathExists(filepath.Join(amb, ".vp-locks")) {
				t.Error(".vp-locks was created in the ambient vault")
			}
			for _, d := range []string{"Projects", "palace"} {
				if ambientPathExists(filepath.Join(amb, d)) && !ambientHasName(before, d) {
					t.Errorf("%s/ was created in the ambient vault", d)
				}
			}
			if after := ambientDirEntries(t, amb); strings.Join(after, ",") != strings.Join(before, ",") {
				t.Errorf("the ambient vault changed: before %v after %v", before, after)
			}
		})
	}
}

func ambientDirEntries(t *testing.T, root string) []string {
	t.Helper()
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func ambientHasName(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// S2: EnsureVaultDir refuses an ambient directory before creating it, ahead of
// the uninitialised-project refusal; a non-ambient initialised vault creates it.
func TestEnsureVaultDirRefusesAnAmbientDirectory(t *testing.T) {
	ctl := t.TempDir()
	testutil.InitProject(t, ctl, "p")
	cd := filepath.Join(ctl, "Projects", "p", "newdir")
	if err := NewVault(ctl).EnsureVaultDir(cd); err != nil || !ambientPathExists(cd) {
		t.Fatalf("control: err=%v exists=%v", err, ambientPathExists(cd))
	}

	amb := t.TempDir()
	t.Cleanup(surface.SetAmbientVaultRoot(amb))
	d := filepath.Join(amb, "Projects", "p", "newdir")
	msg := ambientPanicText(func() { _ = NewVault(amb).EnsureVaultDir(d) })
	if !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("no tripwire refusal (an uninitialised-project error would be the wrong refusal), got %q", msg)
	}
	if ambientPathExists(filepath.Join(amb, "Projects")) {
		t.Error("Projects/ was created in the ambient vault")
	}
}

// S4: EnsureVaultDir's first statement is the tripwire, ahead of the
// uninitialised-project refusal and the MkdirAll.
func TestEnsureVaultDirIsSeatedFirst(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "paths.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "EnsureVaultDir" {
			continue
		}
		if len(fn.Body.List) == 0 {
			t.Fatal("EnsureVaultDir has an empty body")
		}
		isGuard := false
		if es, ok := fn.Body.List[0].(*ast.ExprStmt); ok {
			if call, ok := es.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok {
						isGuard = pkg.Name == "surface" && sel.Sel.Name == "GuardTestPathWrite"
					}
				}
			}
		}
		if !isGuard {
			t.Errorf("%s: EnsureVaultDir's first statement is not surface.GuardTestPathWrite", fset.Position(fn.Pos()))
		}
		return
	}
	t.Fatal("EnsureVaultDir not found")
}
