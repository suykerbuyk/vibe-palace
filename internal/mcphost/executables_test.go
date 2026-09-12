// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"fmt"
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
// exec.CommandContext, exec.LookPath and exec.Cmd{Path: ...} literal —
// including under a dot-imported os/exec — attributes each to the host type
// whose method, or whose New<Type> constructor (nested func literals
// included), contains it, and requires its binary argument to be exactly
// <bound-ident>.Executables()[<int>], where <bound-ident> is that method's own
// receiver or that constructor's own constructed local — not merely some
// other in-scope value's Executables(). Any of these fails the test, naming
// the file and line:
//
//   - a binary argument taken from anywhere else — a string literal, a const,
//     a variable — which is a name the accessor does not govern;
//   - a binary argument taken from a different host's Executables() (a call
//     that resolves, but not to the enclosing method's own receiver or
//     constructor's own local) — this is the drift the guard exists to catch;
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
//
// Known blind spots, named rather than silently present: the receiver/local
// binding check is name-based (parsing with parser.SkipObjectResolution, no
// go/types), so a shadowed inner ident sharing the bound name would be
// accepted incorrectly; a dot-imported bare reference to a tracked function
// that is never called cannot be distinguished from an unrelated identifier
// of the same name, so (unlike the qualified form) it is not flagged; and an
// exec.Cmd's Path set via a later plain field assignment (cmd.Path = ...)
// rather than in the composite literal is not seen.
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

	findings, used := scanExecUses(fset, files)
	for _, f := range findings {
		t.Error(f)
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

// TestExecutablesGuardCatchesMisbinding proves scanExecUses actually binds a
// call to the host making it, rather than merely to the enclosing method's or
// constructor's declared owner — the gap this file's guard used to have: a
// method or constructor attributed to host A whose exec call in fact reads
// some other in-scope value B's Executables() passed the old scan, because
// attribution and the Executables()[i] shape check never compared the two
// idents. Each case below is a small, self-contained source parsed directly
// (no real package, no Registry()); the negative control (case 3) proves the
// guard still accepts the exact shape grok.go and zed.go actually use.
func TestExecutablesGuardCatchesMisbinding(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantFlag bool
	}{
		{
			name: "method calls a different in-scope value's Executables",
			src: `package p
import "os/exec"
type A struct{}
func (a *A) Run(other *A) error {
	cmd := exec.Command(other.Executables()[0])
	return cmd.Run()
}
`,
			wantFlag: true,
		},
		{
			name: "constructor closure calls a different local's Executables",
			src: `package p
import "os/exec"
type A struct {
	runner func() ([]byte, error)
}
type B struct{}
func NewA() *A {
	a := &A{}
	b := &B{}
	a.runner = func() ([]byte, error) {
		return exec.Command(b.Executables()[0]).CombinedOutput()
	}
	return a
}
`,
			wantFlag: true,
		},
		{
			name: "method and constructor bound to their own receiver/local are not flagged",
			src: `package p
import "os/exec"
type A struct {
	runner   func() ([]byte, error)
	lookPath func() (string, error)
}
func NewA() *A {
	a := &A{}
	a.runner = func() ([]byte, error) {
		return exec.Command(a.Executables()[0]).CombinedOutput()
	}
	a.lookPath = func() (string, error) { return exec.LookPath(a.Executables()[0]) }
	return a
}
func (a *A) Run() error {
	cmd := exec.Command(a.Executables()[0])
	return cmd.Run()
}
func (*A) Executables() []string { return []string{"a"} }
`,
			wantFlag: false,
		},
		{
			name: "dot-imported exec call misbound to a different local",
			src: `package p
import . "os/exec"
type A struct{}
type B struct{}
func (a *A) Run(other *B) error {
	cmd := Command(other.Executables()[0])
	return cmd.Run()
}
`,
			wantFlag: true,
		},
		{
			name: "exec.Cmd literal misbound to a different local",
			src: `package p
import "os/exec"
type A struct{}
type B struct{}
func (a *A) Run(other *B) *exec.Cmd {
	return &exec.Cmd{Path: other.Executables()[0]}
}
`,
			wantFlag: true,
		},
		{
			name: "exec.Cmd literal with a hardcoded Path bypasses Executables entirely",
			src: `package p
import "os/exec"
type A struct{}
func (a *A) Run() *exec.Cmd {
	return &exec.Cmd{Path: "grok"}
}
`,
			wantFlag: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "case.go", c.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			findings, _ := scanExecUses(fset, []*ast.File{f})
			switch {
			case c.wantFlag && len(findings) == 0:
				t.Error("want at least one finding, got none")
			case !c.wantFlag && len(findings) != 0:
				t.Errorf("want no findings, got %v", findings)
			}
		})
	}
}

// scanExecUses finds every tracked os/exec call (exec.Command,
// exec.CommandContext, exec.LookPath — qualified or, under a dot import,
// bare) and every exec.Cmd{Path: ...} composite literal in files, and reports
// every one whose binary argument is not exactly <bound-ident>.Executables()[i],
// where <bound-ident> is the enclosing method's own receiver or the enclosing
// New<Owner> constructor's own constructed local (closures included). Findings
// are human-readable strings naming the file and line, meant to be reported
// with t.Error by the caller.
//
// Alongside findings it returns, per owner type, every Executables() index a
// correctly-bound call proves reached — the Registry() coverage check in
// TestHostExecutablesMatchWhatTheyRun needs this and it is cheapest to collect
// in the same pass; a test exercising only the misbinding findings (as
// TestExecutablesGuardCatchesMisbinding does) can ignore it.
func scanExecUses(fset *token.FileSet, files []*ast.File) (findings []string, used map[string][]int) {
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

	used = map[string][]int{}
	for _, f := range files {
		execName := execImportName(f)
		if execName == "" {
			continue
		}
		for _, d := range f.Decls {
			owner, where := "", "package-level declaration"
			var bound map[string]bool
			if fd, ok := d.(*ast.FuncDecl); ok {
				owner, where = declOwner(fd, types), "func "+fd.Name.Name
				if owner != "" {
					bound = boundHostIdents(fd, owner)
				}
			}

			for _, u := range execUses(d, execName) {
				pos := fset.Position(u.pos)
				switch {
				case u.call == nil:
					findings = append(findings, fmt.Sprintf("%s: uncalled reference to exec.%s in %s — call it with the binary taken from Executables() so it can be attributed", pos, u.fn, where))
				case owner == "":
					findings = append(findings, fmt.Sprintf("%s: exec.%s in %s is not attributable to a host (neither a host method nor a New<Host> constructor), so no Executables() would report it", pos, u.fn, where))
				default:
					ident, idx, ok := executablesIndexIdent(u.call.Args[execFuncs[u.fn]])
					if !ok {
						findings = append(findings, fmt.Sprintf("%s: exec.%s must take its binary as <host>.Executables()[i], got %T — a name from anywhere else can drift from what the host reports", pos, u.fn, u.call.Args[execFuncs[u.fn]]))
						continue
					}
					if !bound[ident] {
						findings = append(findings, fmt.Sprintf("%s: exec.%s in %s calls %s.Executables(), not the enclosing host's own receiver/constructed value — a name from elsewhere can drift from what %s reports", pos, u.fn, where, ident, owner))
						continue
					}
					if !slices.Contains(used[owner], idx) {
						used[owner] = append(used[owner], idx)
					}
				}
			}

			for _, cl := range cmdLiteralPaths(d, execName) {
				pos := fset.Position(cl.pos)
				switch owner {
				case "":
					findings = append(findings, fmt.Sprintf("%s: exec.Cmd literal in %s is not attributable to a host (neither a host method nor a New<Host> constructor), so no Executables() would report it", pos, where))
				default:
					ident, idx, ok := executablesIndexIdent(cl.path)
					if !ok {
						findings = append(findings, fmt.Sprintf("%s: exec.Cmd{Path: ...} in %s must take its binary as <host>.Executables()[i], got %T — a name from anywhere else (including a hardcoded literal) can drift from what the host reports", pos, where, cl.path))
						continue
					}
					if !bound[ident] {
						findings = append(findings, fmt.Sprintf("%s: exec.Cmd{Path: ...} in %s calls %s.Executables(), not the enclosing host's own receiver/constructed value — a name from elsewhere can drift from what %s reports", pos, where, ident, owner))
						continue
					}
					if !slices.Contains(used[owner], idx) {
						used[owner] = append(used[owner], idx)
					}
				}
			}
		}
	}
	return findings, used
}

// boundHostIdents returns the set of identifier names inside fd that are
// bound to owner's own value: the receiver's name for a method, or the local
// variable(s) a New<owner> constructor assigns a &owner{...} or new(owner)
// composite construction to (including inside nested closures — ast.Inspect
// already descends into func literal bodies, and Go's lexical scoping means a
// closure's use of that name resolves to the same local).
//
// This is a name-based heuristic, not full go/types resolution — consistent
// with the rest of this file, which parses with parser.SkipObjectResolution
// and avoids type-checking. A shadowed same-named ident in an inner block
// would be accepted incorrectly; see this file's other named blind spots.
func boundHostIdents(fd *ast.FuncDecl, owner string) map[string]bool {
	bound := map[string]bool{}
	if fd.Recv != nil && len(fd.Recv.List) == 1 {
		names := fd.Recv.List[0].Names
		if len(names) == 1 && names[0].Name != "" && names[0].Name != "_" {
			bound[names[0].Name] = true
		}
		return bound
	}
	if fd.Body == nil {
		return bound
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) || !isOwnerConstruction(rhs, owner) {
				continue
			}
			if id, ok := as.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
				bound[id.Name] = true
			}
		}
		return true
	})
	return bound
}

// isOwnerConstruction reports whether e constructs a value of type owner:
// &owner{...} or new(owner).
func isOwnerConstruction(e ast.Expr, owner string) bool {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	switch x := e.(type) {
	case *ast.CompositeLit:
		id, ok := x.Type.(*ast.Ident)
		return ok && id.Name == owner
	case *ast.CallExpr:
		id, ok := x.Fun.(*ast.Ident)
		if !ok || id.Name != "new" || len(x.Args) != 1 {
			return false
		}
		aid, ok := x.Args[0].(*ast.Ident)
		return ok && aid.Name == owner
	}
	return false
}

// executablesIndexIdent reports whether arg has the form <ident>.Executables()[N]
// for an integer literal N, and returns the ident's name and N.
func executablesIndexIdent(arg ast.Expr) (ident string, idx int, ok bool) {
	ix, ok := arg.(*ast.IndexExpr)
	if !ok {
		return "", 0, false
	}
	lit, ok := ix.Index.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return "", 0, false
	}
	n, err := strconv.Atoi(lit.Value)
	if err != nil {
		return "", 0, false
	}
	call, ok := ix.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return "", 0, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Executables" {
		return "", 0, false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", 0, false
	}
	return id.Name, n, true
}

// execUse is one reference to a tracked os/exec function; call is nil when
// the reference is not the callee of a call expression.
type execUse struct {
	pos  token.Pos
	fn   string
	call *ast.CallExpr
}

// execUses returns every reference to a tracked os/exec function under n.
// execName is the local name a normal import binds os/exec to ("exec", or an
// explicit alias); execName == "." marks a dot import. Under a dot import,
// tracked functions are called unqualified, so only call sites are scanned —
// an uncalled bare reference cannot be distinguished from an unrelated
// identifier sharing the same name, unlike the qualified form's uncalled-sel
// case (a named blind spot, not a silent one).
func execUses(n ast.Node, execName string) []execUse {
	if execName == "." {
		var uses []execUse
		ast.Inspect(n, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if _, tracked := execFuncs[id.Name]; tracked {
				uses = append(uses, execUse{pos: id.Pos(), fn: id.Name, call: call})
			}
			return true
		})
		return uses
	}

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
		uses[i] = execUse{pos: s.Pos(), fn: s.Sel.Name, call: callee[s]}
	}
	return uses
}

// cmdLiteral is one exec.Cmd{...} (or bare Cmd{...} under a dot import)
// composite literal's Path field, found under a node scanned by
// cmdLiteralPaths.
type cmdLiteral struct {
	pos  token.Pos
	path ast.Expr
}

// cmdLiteralPaths returns the Path field's value expression for every
// exec.Cmd{...} (or bare Cmd{...} under a dot import) composite literal under
// n — the keyed "Path:" field if present, otherwise the first positional
// element, since Path is exec.Cmd's first field. A Path set via a later plain
// field assignment (cmd.Path = ...) is not covered — named here alongside
// this file's other blind spots, not silently missed.
func cmdLiteralPaths(n ast.Node, execName string) []cmdLiteral {
	var lits []cmdLiteral
	ast.Inspect(n, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok || !isExecCmdType(cl.Type, execName) {
			return true
		}
		var path ast.Expr
		keyed := false
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			keyed = true
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Path" {
				path = kv.Value
			}
		}
		if !keyed && len(cl.Elts) > 0 {
			path = cl.Elts[0]
		}
		if path != nil {
			lits = append(lits, cmdLiteral{pos: path.Pos(), path: path})
		}
		return true
	})
	return lits
}

// isExecCmdType reports whether t is exec.Cmd (qualified by execName) or,
// under a dot import (execName == "."), the bare identifier Cmd.
func isExecCmdType(t ast.Expr, execName string) bool {
	switch x := t.(type) {
	case *ast.SelectorExpr:
		if execName == "." {
			return false
		}
		id, ok := x.X.(*ast.Ident)
		return ok && id.Name == execName && x.Sel.Name == "Cmd"
	case *ast.Ident:
		return execName == "." && x.Name == "Cmd"
	}
	return false
}

// execImportName returns the local name f binds os/exec to, or "" when f does
// not import it. A dot import ("import . \"os/exec\"") returns ".".
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
