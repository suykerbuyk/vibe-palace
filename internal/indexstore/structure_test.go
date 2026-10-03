// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writePrimitives are the package's functions that change a file on disk.
// Everything this package writes reaches one of them.
//
// Calls are keyed by how they resolve: "f" for a call of a package-level
// function, ".m" for a method call on a value, "pkg.F" for a call into an
// imported package. A method call never matches a package function of the same
// name (rl.mu.Lock is not Lock).
var writePrimitives = map[string]bool{
	"writeFile": true, "writeFileFn": true, "removeFile": true, "removeIfExists": true,
	"appendFile": true, "truncateTornTail": true, "writeGenFile": true,
	"bumpGenFile": true, "ensureGenFile": true, "vaultfs.Delete": true,
}

// writersOutsideTx are the exported entry points that may write without being
// a *Tx method, each with the reason it is safe. Nothing on this list writes
// under index/<p>/, index/.generation/ or embed-cache/<p>/ except under a
// commit lock it has just taken.
var writersOutsideTx = map[string]string{
	"Lock":                          "creates or replaces the change counter under the commit lock it has just taken",
	"TryRunLock":                    "writes the run lock's holder record in palace/.local/locks/",
	"RunLock.SetProject":            "rewrites the holder record",
	"RunLock.SetProgress":           "rewrites the holder record",
	"RunLock.Release":               "removes the holder record",
	"RunLock.ReleaseAndRecheck":     "removes and rewrites the holder record",
	"DeleteLegacyLedgerIfUntracked": "deletes palace/<p>/ingested-archives.jsonl, outside the host-local index, through vaultfs.Delete",
}

type funcInfo struct {
	recv, name string
	exported   bool
	calls      map[string]bool
}

func (f funcInfo) key() string {
	if f.recv != "" {
		return f.recv + "." + f.name
	}
	return f.name
}

// packageFuncs parses this package's non-test files and returns every
// function with the names it calls (a method call counts by method name).
func packageFuncs(t *testing.T) []funcInfo {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []funcInfo
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		imports := map[string]bool{}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			n := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				n = imp.Name.Name
			}
			imports[n] = true
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			fi := funcInfo{name: fd.Name.Name, exported: fd.Name.IsExported(), calls: map[string]bool{}}
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				typ := fd.Recv.List[0].Type
				if s, ok := typ.(*ast.StarExpr); ok {
					typ = s.X
				}
				if id, ok := typ.(*ast.Ident); ok {
					fi.recv = id.Name
					fi.exported = fi.exported && id.IsExported()
				}
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					switch fn := x.Fun.(type) {
					case *ast.Ident:
						fi.calls[fn.Name] = true
					case *ast.SelectorExpr:
						if id, ok := fn.X.(*ast.Ident); ok && imports[id.Name] {
							fi.calls[id.Name+"."+fn.Sel.Name] = true
						} else {
							fi.calls["."+fn.Sel.Name] = true
						}
					}
				case *ast.Ident:
					// A package-level func value used without a call
					// (writeFileFn is called through writeFile's body).
					if writePrimitives[x.Name] {
						fi.calls[x.Name] = true
					}
				}
				return true
			})
			out = append(out, fi)
		}
	}
	return out
}

// Writes need a Tx: every exported function or method of this package that
// can reach a write is a method on *Tx, or is on writersOutsideTx with its
// reason. A new exported writer that bypasses the commit lock fails here.
func TestWritesNeedATx(t *testing.T) {
	funcs := packageFuncs(t)
	byName := map[string][]funcInfo{} // "f" for functions, ".m" for methods
	for _, f := range funcs {
		k := f.name
		if f.recv != "" {
			k = "." + f.name
		}
		byName[k] = append(byName[k], f)
	}
	memo := map[string]bool{}
	var reaches func(f funcInfo, seen map[string]bool) bool
	reaches = func(f funcInfo, seen map[string]bool) bool {
		if v, ok := memo[f.key()]; ok {
			return v
		}
		if seen[f.key()] {
			return false
		}
		seen[f.key()] = true
		for c := range f.calls {
			if writePrimitives[c] {
				memo[f.key()] = true
				return true
			}
			for _, g := range byName[c] {
				if reaches(g, seen) {
					memo[f.key()] = true
					return true
				}
			}
		}
		return false
	}
	var bad []string
	for _, f := range funcs {
		if !f.exported || !reaches(f, map[string]bool{}) {
			continue
		}
		if f.recv == "Tx" || f.recv == "LifecycleTx" {
			continue
		}
		if _, ok := writersOutsideTx[f.key()]; ok {
			continue
		}
		bad = append(bad, f.key())
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("exported writers outside *Tx: %v; make them *Tx methods, or add each to writersOutsideTx with the reason it is safe", bad)
	}
	// The allowlist names only functions that exist and still write, so it
	// cannot keep an entry the code no longer needs.
	for k := range writersOutsideTx {
		found := false
		for _, f := range funcs {
			if f.key() == k && reaches(f, map[string]bool{}) {
				found = true
			}
		}
		if !found {
			t.Errorf("writersOutsideTx names %s, which no longer writes", k)
		}
	}
}

// discardOnly are the identifiers only the rebuild driver
// (explicit-resumable-index-rebuild-with-disk-watchdog) may use outside this
// package: a discard, the two clears, and the proof they take.
var discardOnly = map[string]bool{
	"Discard": true, "DiscardChunks": true, "DiscardVectors": true,
	"ClearBaseline": true, "ClearFailures": true, "CompletedRebuild": true,
}

// rebuildDriverDirs is where the rebuild driver will live. Empty until that
// child lands: until then nothing outside this package may discard.
var rebuildDriverDirs = map[string]bool{}

// Only the rebuild discards: outside internal/indexstore, no non-test Go file
// calls Tx.Discard, ClearBaseline, ClearFailures or CompletedRebuild, or names
// DiscardChunks or DiscardVectors, except in the rebuild driver's package.
// Graph writes (WriteGraph, DeleteGraph) are not on the list: the ingester may
// call them.
func TestOnlyTheRebuildDiscards(t *testing.T) {
	root := filepath.Join("..", "..")
	var bad []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			rel = filepath.ToSlash(rel)
			if rel == "internal/indexstore" || rebuildDriverDirs[rel] {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if !strings.Contains(string(src), "indexstore") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, src, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !discardOnly[sel.Sel.Name] {
					return true
				}
				// io.Discard and other packages' Discard are not this one: a
				// Discard counts when it is indexstore's constant, or a call
				// passed one of its kinds (both caught by the kind names).
				if sel.Sel.Name == "Discard" {
					return true
				}
				bad = append(bad, filepath.ToSlash(p)+": "+sel.Sel.Name)
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("only the rebuild driver may discard or clear: %v", bad)
	}
}
