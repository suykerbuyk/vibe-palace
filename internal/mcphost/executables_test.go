// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// execFuncs maps each os/exec function this guard tracks to the index of its
// binary-name argument.
var execFuncs = map[string]int{"Command": 0, "CommandContext": 1, "LookPath": 0}

// TestHostExecutablesMatchWhatTheyRun proves that every registered host's
// Executables() is exactly the set of binaries its code looks up or runs, so a
// test that puts sentinels for Executables() first on PATH shadows every agent
// CLI a host can reach.
//
// Executables() is the source of those names, not a report kept beside them.
// This test parses the package's non-test files, finds every exec.Command,
// exec.CommandContext and exec.LookPath, attributes each to the host type whose
// method — or whose New<Type> constructor, nested func literals included —
// contains it, and requires its binary argument to be exactly
// <ident>.Executables()[<int>]. Any of these fails the test, naming the file
// and line:
//
//   - a binary argument taken from anywhere else — a string literal, a const, a
//     variable — which is a name the accessor does not govern;
//   - a call in a free function, or in a method of a type that is not a
//     registered host — nothing would report it;
//   - an uncalled reference (lookPath: exec.LookPath), whose binary argument
//     cannot be read here.
//
// Then, for every host in Registry(), the indexes its calls use must lie inside
// Executables() and cover all of it: a reported name no call uses would put a
// sentinel on PATH for a binary the host never reaches.
//
// The scan covers this package only. ClaudeHost delegates to internal/plugin,
// which contains no os/exec import (`grep -rn "os/exec" internal/plugin` is
// empty), so its empty set is proven, not assumed: the compiler forces it to
// answer Executables, and this scan finds nothing attributed to it. A host that
// delegates execution to another package needs that package added here.
func TestHostExecutablesMatchWhatTheyRun(t *testing.T) {
	fset := token.NewFileSet()
	var files []*ast.File
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}

	types := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
				for _, s := range g.Specs {
					types[s.(*ast.TypeSpec).Name.Name] = true
				}
			}
		}
	}

	used := map[string][]int{} // owner type → Executables() indexes its calls use
	for _, f := range files {
		execName := execImportName(f)
		if execName == "" {
			continue
		}
		for _, d := range f.Decls {
			owner, where := "", "package-level declaration"
			if fd, ok := d.(*ast.FuncDecl); ok {
				owner, where = declOwner(fd, types), "func "+fd.Name.Name
			}
			for _, u := range execUses(d, execName) {
				pos := fset.Position(u.sel.Pos())
				fn := u.sel.Sel.Name
				switch {
				case u.call == nil:
					t.Errorf("%s: uncalled reference to exec.%s in %s — call it with the binary taken from Executables() so it can be attributed", pos, fn, where)
				case owner == "":
					t.Errorf("%s: exec.%s in %s is not attributable to a host (neither a host method nor a New<Host> constructor), so no Executables() would report it", pos, fn, where)
				default:
					idx, ok := executablesIndex(u.call.Args[execFuncs[fn]])
					if !ok {
						t.Errorf("%s: exec.%s must take its binary as <host>.Executables()[i], got %T — a name from anywhere else can drift from what the host reports", pos, fn, u.call.Args[execFuncs[fn]])
						continue
					}
					if !slices.Contains(used[owner], idx) {
						used[owner] = append(used[owner], idx)
					}
				}
			}
		}
	}

	registered := map[string]bool{}
	for _, h := range Registry() {
		rt := reflect.TypeOf(h)
		if rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		registered[rt.Name()] = true
		reported := h.Executables()
		var reached []string
		for _, i := range used[rt.Name()] {
			if i >= len(reported) {
				t.Errorf("%s calls exec with Executables()[%d], but Executables() = %q", rt.Name(), i, reported)
				continue
			}
			reached = append(reached, reported[i])
		}
		want := slices.Compact(slices.Sorted(slices.Values(reached)))
		got := slices.Compact(slices.Sorted(slices.Values(reported)))
		if !slices.Equal(got, want) {
			t.Errorf("%s.Executables() = %q, but its code looks up or runs %q", rt.Name(), got, want)
		}
	}
	for owner, idx := range used {
		if !registered[owner] {
			t.Errorf("type %s looks up or runs Executables()%v but is not a host in Registry(), so nothing reports it", owner, idx)
		}
	}
}

// executablesIndex reports whether arg has the form <ident>.Executables()[N]
// for an integer literal N, and returns N.
func executablesIndex(arg ast.Expr) (int, bool) {
	ix, ok := arg.(*ast.IndexExpr)
	if !ok {
		return 0, false
	}
	lit, ok := ix.Index.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	n, err := strconv.Atoi(lit.Value)
	if err != nil {
		return 0, false
	}
	call, ok := ix.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return 0, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Executables" {
		return 0, false
	}
	if _, ok := sel.X.(*ast.Ident); !ok {
		return 0, false
	}
	return n, true
}

// execUse is one reference to a tracked os/exec function; call is nil when
// the reference is not the callee of a call expression.
type execUse struct {
	sel  *ast.SelectorExpr
	call *ast.CallExpr
}

// execUses returns every reference to a tracked os/exec function under n.
func execUses(n ast.Node, execName string) []execUse {
	callee := map[*ast.SelectorExpr]*ast.CallExpr{}
	var sels []*ast.SelectorExpr
	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if s, ok := x.Fun.(*ast.SelectorExpr); ok {
				callee[s] = x
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == execName {
				if _, tracked := execFuncs[x.Sel.Name]; tracked {
					sels = append(sels, x)
				}
			}
		}
		return true
	})
	uses := make([]execUse, len(sels))
	for i, s := range sels {
		uses[i] = execUse{sel: s, call: callee[s]}
	}
	return uses
}

// execImportName returns the local name f binds os/exec to, or "" when f does
// not import it.
func execImportName(f *ast.File) string {
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return "exec"
		}
	}
	return ""
}

// declOwner returns the type a function belongs to: its receiver's type, or T
// for a constructor named NewT where T is a type declared in this package.
// It returns "" for any other free function.
func declOwner(fd *ast.FuncDecl, types map[string]bool) string {
	if fd.Recv != nil && len(fd.Recv.List) == 1 {
		rt := fd.Recv.List[0].Type
		if star, ok := rt.(*ast.StarExpr); ok {
			rt = star.X
		}
		if id, ok := rt.(*ast.Ident); ok {
			return id.Name
		}
		return ""
	}
	if name, ok := strings.CutPrefix(fd.Name.Name, "New"); ok && types[name] {
		return name
	}
	return ""
}
