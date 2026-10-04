// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// goSource is one parsed non-test Go file of the module.
type goSource struct {
	rel  string // slash-separated, relative to the module root
	fset *token.FileSet
	file *ast.File
}

// moduleSources parses every non-test .go file of the module, skipping VCS,
// agent, vendored and testdata trees.
func moduleSources(t *testing.T) []goSource {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := moduleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	var out []goSource
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "third_party", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		out = append(out, goSource{rel: filepath.ToSlash(rel), fset: fset, file: f})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 100 {
		t.Fatalf("only %d source files parsed; the walk is probably pointed at the wrong tree", len(out))
	}
	return out
}

// importNames is the set of names a file's imports bind.
func importNames(f *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = true
	}
	return out
}

// kgUnionCallers lists every method call of one of the tracked-only KG readers
// (or the v8 InvalidateTriple) made outside internal/storage and
// internal/kgread. A package-qualified call (kgread.KGStats) is not a method
// call and is not counted.
func kgUnionCallers(srcs []goSource) []string {
	tracked := map[string]bool{
		"QueryEntity": true, "Timeline": true, "KGStats": true,
		"ListTriples": true, "ListEntities": true, "InvalidateTriple": true,
	}
	var out []string
	for _, s := range srcs {
		if strings.HasPrefix(s.rel, "internal/storage/") || strings.HasPrefix(s.rel, "internal/kgread/") {
			continue
		}
		pkgs := importNames(s.file)
		ast.Inspect(s.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !tracked[sel.Sel.Name] {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && pkgs[id.Name] {
				return true
			}
			out = append(out, s.rel+":"+strconv.Itoa(s.fset.Position(call.Pos()).Line)+" ."+sel.Sel.Name)
			return true
		})
	}
	return out
}

// Every KG read outside storage goes through internal/kgread, which answers
// from the union of the tracked files and the host-local store. A caller left
// on a storage reader would see the tracked half only.
// (authored-and-extracted-knowledge-graph-records, Scope 4.)
func TestKGReadersGoThroughTheUnion(t *testing.T) {
	for _, c := range kgUnionCallers(moduleSources(t)) {
		t.Errorf("%s: a tracked-only KG reader called outside internal/storage; call internal/kgread instead", c)
	}
}

// The detector itself can fail: a method call on a vault is reported, a
// package-qualified call is not.
func TestKGUnionCallersDetectsAMethodCall(t *testing.T) {
	src := `package x
import "example.com/internal/kgread"
func f(vault interface{ KGStats(string) (int, error) }) {
	vault.KGStats("p")
	kgread.KGStats(nil, "p")
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := kgUnionCallers([]goSource{{rel: "internal/tools/x.go", fset: fset, file: f}})
	if len(got) != 1 || !strings.Contains(got[0], ":4 .KGStats") {
		t.Errorf("detector found %v, want exactly the method call on line 4", got)
	}
}

// Local KG records are written only by internal/indexstore's commit steps,
// which hold the project's index commit lock: no other package names the
// store's KG file. Together with indexstore's own TestWritesNeedATx (every
// exported writer there is a *Tx method) that keeps every local KG write under
// the lock. (R1.)
func TestOnlyIndexstoreNamesTheLocalKGFile(t *testing.T) {
	for _, s := range moduleSources(t) {
		if strings.HasPrefix(s.rel, "internal/indexstore/") {
			continue
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "records.jsonl") {
				t.Errorf("%s:%d names the local KG file; only internal/indexstore writes it, under the commit lock",
					s.rel, s.fset.Position(lit.Pos()).Line)
			}
			return true
		})
	}
}
