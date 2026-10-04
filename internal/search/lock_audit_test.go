// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parsedFunc is one function declaration of a parsed production file.
type parsedFunc struct {
	file string
	name string // "Recv.Name" for a method, "Name" otherwise
	decl *ast.FuncDecl
}

func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if st, ok := t.(*ast.StarExpr); ok {
		t = st.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// productionFuncs parses every non-test .go file under dir (recursively when
// recurse is set) and returns its function declarations.
func productionFuncs(t *testing.T, dir string, recurse bool) []parsedFunc {
	t.Helper()
	var out []parsedFunc
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (!recurse || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
				out = append(out, parsedFunc{file: path, name: funcName(fd), decl: fd})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// calls lists "pkg.Name" for a package-qualified call and ".Name" for a
// method or field call, plus "Name" for a plain call, in fn's body.
func calls(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := c.Fun.(type) {
		case *ast.Ident:
			out = append(out, f.Name)
		case *ast.SelectorExpr:
			if x, ok := f.X.(*ast.Ident); ok && (x.Name == "indexstore" || x.Name == "vaultlock") {
				out = append(out, x.Name+"."+f.Sel.Name)
			} else {
				out = append(out, "."+f.Sel.Name)
			}
		}
		return true
	})
	return out
}

// TestLockDiscipline pins, by source, the lock rules no runtime test can see
// for every path:
//
//   - indexstore.Lock is called only by (*projectLock).Tx, and the project
//     semaphore is taken only by lockProject: one acquisition path, so one
//     lock order;
//   - reapLocked and evictLocked, which run under a held commit lock, never
//     take a project mutex or a commit lock (a second commit lock in one
//     process deadlocks);
//   - only (*CacheWriter).Put calls the cache's unexported putVector, so every
//     vector is written under a held commit lock;
//   - internal/search never touches the index run lock.
func TestLockDiscipline(t *testing.T) {
	for _, fn := range productionFuncs(t, ".", false) {
		for _, c := range calls(fn.decl) {
			switch {
			case c == "indexstore.Lock" && fn.name != "projectLock.Tx":
				t.Errorf("%s (%s) calls indexstore.Lock; only (*projectLock).Tx may", fn.name, fn.file)
			case c == "acquireSem" && fn.name != "Engine.lockProject":
				t.Errorf("%s (%s) takes a project semaphore; only lockProject may", fn.name, fn.file)
			case c == ".putVector" && fn.name != "CacheWriter.Put":
				t.Errorf("%s (%s) calls the cache's putVector; only (*CacheWriter).Put may", fn.name, fn.file)
			case strings.HasPrefix(c, "indexstore.") && strings.Contains(c, "RunLock"):
				t.Errorf("%s (%s) calls %s: internal/search never takes the run lock", fn.name, fn.file, c)
			}
			if fn.name == "Engine.reapLocked" || fn.name == "Engine.evictLocked" {
				switch c {
				case ".lockProject", ".Tx", "indexstore.Lock", "acquireSem":
					t.Errorf("%s runs under a held commit lock but calls %s", fn.name, c)
				}
			}
		}
	}
}

// TestPutVectorsWritesOnlyThroughTheTxWriter: every production call
// X.PutVectors(w, ...) anywhere in the module passes a writer that the same
// function obtained from a .Writer(...) call, so no caller can hand the store a
// VectorWriter that ignores the lock or the regime.
func TestPutVectorsWritesOnlyThroughTheTxWriter(t *testing.T) {
	root := filepath.Join("..", "..")
	seen := 0
	for _, dir := range []string{filepath.Join(root, "internal"), filepath.Join(root, "cmd")} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		for _, fn := range productionFuncs(t, dir, true) {
			fromWriter := map[string]bool{}
			ast.Inspect(fn.decl.Body, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
					if c, ok := as.Rhs[0].(*ast.CallExpr); ok {
						if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Writer" {
							if id, ok := as.Lhs[0].(*ast.Ident); ok {
								fromWriter[id.Name] = true
							}
						}
					}
				}
				return true
			})
			ast.Inspect(fn.decl.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := c.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "PutVectors" || len(c.Args) == 0 {
					return true
				}
				seen++
				id, ok := c.Args[0].(*ast.Ident)
				if !ok || !fromWriter[id.Name] {
					t.Errorf("%s (%s): PutVectors' writer does not come from a .Writer(tx) call in the same function", fn.name, fn.file)
				}
				return true
			})
		}
	}
	if seen == 0 {
		t.Fatal("no production PutVectors call found: the audit is looking in the wrong place")
	}
}
