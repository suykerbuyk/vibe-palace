// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// fakeHealEngine satisfies graphHealEngine without a real search engine, so the
// adapter's mapping can be tested in isolation.
type fakeHealEngine struct {
	out  search.HealOutcome
	err  error
	seen struct {
		vaultRoot, project string
	}
}

func (f *fakeHealEngine) HealGraph(_ context.Context, vaultRoot, project string) (search.HealOutcome, error) {
	f.seen.vaultRoot, f.seen.project = vaultRoot, project
	return f.out, f.err
}

// Test 9: NewEngineHealer returns a GraphHealer whose HealGraph forwards the
// (vault, project) args and maps search.HealOutcome field-for-field to
// HealResult. The compile-time assertion var _ GraphHealer = engineHealer{}
// lives in heal.go. Mutant: an adapter that drops a field or the error.
func TestEngineHealerMapsResult(t *testing.T) {
	fe := &fakeHealEngine{out: search.HealOutcome{Rebuilt: true, Deleted: false, Reason: "fingerprint mismatch"}}
	var h GraphHealer = NewEngineHealer(fe)
	r, err := h.HealGraph(context.Background(), "/vault/root", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if r != (HealResult{Rebuilt: true, Deleted: false, Reason: "fingerprint mismatch"}) {
		t.Fatalf("mapped result = %+v, want the search outcome's fields", r)
	}
	if fe.seen.vaultRoot != "/vault/root" || fe.seen.project != "proj" {
		t.Fatalf("adapter did not forward args: %+v", fe.seen)
	}

	sentinel := errors.New("boom")
	eh := NewEngineHealer(&fakeHealEngine{err: sentinel})
	if _, err := eh.HealGraph(context.Background(), "v", "p"); !errors.Is(err, sentinel) {
		t.Fatalf("adapter swallowed the error: %v", err)
	}
}

// ingestImportPath is the module path of the package that declares ingest.Deps
// and ingest.NewEngineHealer. The drift guard binds Deps literals and the
// healer constructor to THIS path (resolving each file's import specs), not to
// the bare identifier "ingest", so an aliased import cannot hide a nil healer.
const ingestImportPath = "github.com/suykerbuyk/vibe-palace/internal/ingest"

// depsHealerProductionFiles are the non-test production files that MUST each
// carry at least one correctly-wired ingest.Deps literal. Asserting them by
// name (not by a count floor) fails closed if a walker bug drops package main /
// package tools: a count floor would still be satisfied by the test factories.
// Paths are relative to the module root, slash-separated.
var depsHealerProductionFiles = []string{
	"cmd/vp/cmd_index_rebuild.go",
	"cmd/vp/cmd_drain_archives.go",
	"internal/tools/system_tools.go",
}

// TestDepsGraphHealerDriftGuard fails if any ingest.Deps composite literal
// across the production and test wiring does not set GraphHealer to a non-nil
// NewEngineHealer(...) constructor. It walks (filepath.WalkDir, never
// `git ls-files`, so new untracked files are seen) the four packages that
// construct an ingest.Deps and hands each file's complete bytes to
// scanDepsHealers — the single classifier. The walker classifies nothing
// itself; it only aggregates.
//
// SCOPE: this checks composite LITERALS only. A healer cleared AFTER the
// literal (`d.GraphHealer = nil`) is an *ast.AssignStmt the guard never visits;
// runtime nil-analysis is out of scope. All six live sites (three production,
// three test) are literal constructor calls, so the guard covers them.
func TestDepsGraphHealerDriftGuard(t *testing.T) {
	root := repoRoot(t)
	dirs := []string{
		filepath.Join("internal", "ingest"),
		filepath.Join("internal", "integration"),
		filepath.Join("cmd", "vp"),
		filepath.Join("internal", "tools"),
	}
	passing, violations := classifyDepsDirs(t, root, dirs)
	if len(violations) > 0 {
		t.Fatalf("ingest.Deps literal(s) not wired to a non-nil NewEngineHealer(...):\n%s",
			strings.Join(violations, "\n"))
	}
	for _, f := range depsHealerProductionFiles {
		if passing[f] < 1 {
			t.Fatalf("production file %s has no correctly-wired ingest.Deps literal "+
				"(found %d passing) — the walker may have dropped its package, or the "+
				"wiring moved; update depsHealerProductionFiles deliberately if so", f, passing[f])
		}
	}
}

// classifyDepsDirs walks each dir (relative to root), skipping any testdata/
// subtree, and classifies every .go file through the REAL scanDepsHealers. The
// production guard and the TestDepsGuardWalkerUsesHelper tripwire both call this
// one function, so a future edit that stops routing through scanDepsHealers (a
// stub, or a weaker re-parse) reds the tripwire. passing[rel] = literals with no
// violation; each literal yields at most one violation, so passing = literals -
// violations.
func classifyDepsDirs(t *testing.T, root string, dirs []string) (passing map[string]int, violations []string) {
	t.Helper()
	passing = map[string]int{}
	for _, d := range dirs {
		abs := filepath.Join(root, d)
		err := filepath.WalkDir(abs, func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() {
				// The main four-dir walk must not descend into testdata/, where
				// TestDepsGuardWalkerUsesHelper plants a deliberately broken
				// fixture. A walk rooted inside testdata/ (the tripwire) still
				// runs: its start dir is not named "testdata".
				if de.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			lits, viols, serr := scanDepsHealers(src)
			if serr != nil {
				return fmt.Errorf("parse %s: %w", path, serr)
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			passing[rel] = lits - len(viols)
			for _, v := range viols {
				violations = append(violations, rel+":"+v)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", abs, err)
		}
	}
	return passing, violations
}

// TestDepsGuardWalkerUsesHelper pins the drift-guard walker to the real
// scanDepsHealers. It points the SAME classifier (classifyDepsDirs) at a planted
// fixture under internal/ingest/testdata/depsguard that the helper rejects
// (an aliased-import `ing.Deps{GraphHealer: nil}`). If classifyDepsDirs is ever
// changed to a stub or a weaker re-parse, this reds. The main guard skips
// testdata/, so the fixture never reaches the real gate, and testdata/ is
// ignored by the Go build so it does not break compilation.
func TestDepsGuardWalkerUsesHelper(t *testing.T) {
	root := repoRoot(t)
	fixtureDir := filepath.Join("internal", "ingest", "testdata", "depsguard")
	_, violations := classifyDepsDirs(t, root, []string{fixtureDir})
	if len(violations) != 1 {
		t.Fatalf("planted fixture: got %d violation(s), want exactly 1: %+v", len(violations), violations)
	}
	if !strings.Contains(violations[0], "testdata/depsguard/bad.go") {
		t.Fatalf("violation is not from the planted fixture: %q", violations[0])
	}
}

// scanDepsHealers parses one COMPLETE Go source file (package clause + imports +
// body) and reports every ingest.Deps composite literal and whether each wires
// GraphHealer to the non-nil constructor ingest.NewEngineHealer. It is the
// single classifier for TestDepsGraphHealerDriftGuard: the walker passes raw
// file bytes and re-implements none of this.
//
// A composite literal is an ingest.Deps literal when its type is:
//   - bare Deps, and the file is package ingest or dot-imports the ingest path;
//   - <local>.Deps, where <local> is the name an import of ingestImportPath is
//     bound to in THIS file (default name or an alias);
//   - a per-file type ALIAS to any of the above, resolved to a fixed point so
//     an alias chain (`type D = ingest.Deps; type E = D`) and a parenthesized
//     RHS (`type D = (ingest.Deps)`) both resolve. A DEFINED type (`type D
//     ingest.Deps`, no `=`) is a distinct type and is NOT matched;
//   - elided (Type == nil) and nested one level inside a matching
//     []Deps / [N]Deps / map[K]Deps composite (the []Deps{{...}} shape).
//
// Known residual (one level deeper than the elided/alias rules, NOT caught): a
// slice-of-slice `[][]ingest.Deps{{{...}}}`, a slice-of-pointer
// `[]*ingest.Deps{{...}}`, a pointer-type alias (`type D = *ingest.Deps`) and a
// slice-type alias (`type D = []ingest.Deps`) elide their type through one more
// *ast.StarExpr / *ast.ArrayType than this guard unwraps. No such site exists;
// if one is added, extend eltTypeMatch to unwrap one *ast.StarExpr and recurse
// one *ast.ArrayType.
//
// GraphHealer is well-wired only when its value is a call to NewEngineHealer
// (bare, inside package ingest) or <local>.NewEngineHealer (same import). nil, a
// bare variable, a foreign x.NewEngineHealer, and an omitted or positional
// field are violations. Each returned violation carries a line number; the
// walker prefixes the file path. ParseFile mode is pinned here (syntax only, no
// type-check, bodies retained).
func scanDepsHealers(src []byte) (literals int, violations []string, err error) {
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, "src.go", src, parser.SkipObjectResolution)
	if perr != nil {
		return 0, nil, perr
	}

	// Local names that denote the ingest package in THIS file.
	ingestNames := map[string]bool{}
	dotImported := false
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if p != ingestImportPath {
			continue
		}
		switch {
		case imp.Name == nil:
			// default import name == the package's last path segment ("ingest").
			ingestNames[p[strings.LastIndexByte(p, '/')+1:]] = true
		case imp.Name.Name == ".":
			dotImported = true
		case imp.Name.Name == "_":
			// blank import: ingest's exported names are not in scope.
		default:
			ingestNames[imp.Name.Name] = true
		}
	}
	// Bare Deps / NewEngineHealer denote ingest only inside package ingest or
	// under a dot-import of the ingest path.
	bareIsIngest := f.Name.Name == "ingest" || dotImported

	// depsTypeMatch reports whether an expression names ingest.Deps directly
	// (bare Deps or <local>.Deps), ignoring per-file aliases.
	depsTypeMatch := func(e ast.Expr) bool {
		switch t := e.(type) {
		case *ast.Ident:
			return t.Name == "Deps" && bareIsIngest
		case *ast.SelectorExpr:
			x, ok := t.X.(*ast.Ident)
			return ok && t.Sel.Name == "Deps" && ingestNames[x.Name]
		}
		return false
	}
	// Type aliases to ingest.Deps (`type D = ingest.Deps`, or `type D = Deps`
	// inside package ingest) make D{GraphHealer: nil} a real ingest.Deps literal.
	// ts.Assign is valid only for an alias (`=`); a DEFINED type (`type D
	// ingest.Deps`) has Assign unset and is a distinct type, deliberately NOT
	// matched. Gather (name, RHS) pairs, peeling one *ast.ParenExpr so
	// `type D = (ingest.Deps)` resolves.
	depsAliases := map[string]bool{}
	type aliasSpec struct {
		name string
		rhs  ast.Expr
	}
	var aliasSpecs []aliasSpec
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || !ts.Assign.IsValid() {
				continue
			}
			rhs := ts.Type
			if p, ok := rhs.(*ast.ParenExpr); ok {
				rhs = p.X
			}
			aliasSpecs = append(aliasSpecs, aliasSpec{ts.Name.Name, rhs})
		}
	}
	// Resolve to a fixed point: an alias whose RHS is ingest.Deps directly, or an
	// *ast.Ident already known to be an ingest.Deps alias (a chain), is itself an
	// ingest.Deps alias. Go forbids alias cycles, so this terminates.
	for {
		added := false
		for _, a := range aliasSpecs {
			if depsAliases[a.name] {
				continue
			}
			match := depsTypeMatch(a.rhs)
			if !match {
				if id, ok := a.rhs.(*ast.Ident); ok && depsAliases[id.Name] {
					match = true
				}
			}
			if match {
				depsAliases[a.name] = true
				added = true
			}
		}
		if !added {
			break
		}
	}
	// isDepsType extends depsTypeMatch with this file's ingest.Deps aliases.
	isDepsType := func(e ast.Expr) bool {
		if depsTypeMatch(e) {
			return true
		}
		id, ok := e.(*ast.Ident)
		return ok && depsAliases[id.Name]
	}
	// eltTypeMatch unwraps []Deps / [N]Deps / map[K]Deps to its element type and
	// reports whether that element is ingest.Deps (for type-elided children).
	eltTypeMatch := func(e ast.Expr) bool {
		switch t := e.(type) {
		case *ast.ArrayType:
			return isDepsType(t.Elt)
		case *ast.MapType:
			return isDepsType(t.Value)
		}
		return false
	}
	// healerOK reports whether v is a call to the ingest NewEngineHealer.
	healerOK := func(v ast.Expr) bool {
		call, ok := v.(*ast.CallExpr)
		if !ok {
			return false
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			return fn.Name == "NewEngineHealer" && bareIsIngest
		case *ast.SelectorExpr:
			x, ok := fn.X.(*ast.Ident)
			return ok && fn.Sel.Name == "NewEngineHealer" && ingestNames[x.Name]
		}
		return false
	}

	lineOf := func(n ast.Node) int { return fset.Position(n.Pos()).Line }

	// classify records one ingest.Deps composite literal and its verdict.
	classify := func(cl *ast.CompositeLit) {
		literals++
		if len(cl.Elts) == 0 {
			violations = append(violations, fmt.Sprintf("line %d: ingest.Deps literal omits GraphHealer", lineOf(cl)))
			return
		}
		// Go forbids mixing keyed and unkeyed elements, so a non-keyed first
		// element means the whole literal is positional and unverifiable.
		if _, keyed := cl.Elts[0].(*ast.KeyValueExpr); !keyed {
			violations = append(violations, fmt.Sprintf("line %d: positional ingest.Deps literal; use a keyed GraphHealer so the guard can verify it", lineOf(cl)))
			return
		}
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "GraphHealer" {
				continue
			}
			if !healerOK(kv.Value) {
				violations = append(violations, fmt.Sprintf("line %d: GraphHealer is not a non-nil NewEngineHealer(...) call", lineOf(cl)))
			}
			return
		}
		violations = append(violations, fmt.Sprintf("line %d: ingest.Deps literal omits GraphHealer", lineOf(cl)))
	}

	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch {
		case cl.Type != nil && isDepsType(cl.Type):
			classify(cl)
		case cl.Type != nil && eltTypeMatch(cl.Type):
			// []Deps / [N]Deps / map[K]Deps: each type-elided element is a Deps
			// literal (one level, the []Deps{{...}} shape). A standalone visit
			// of a Type==nil composite matches neither case above, so handling
			// it from the parent here counts it exactly once.
			for _, elt := range cl.Elts {
				val := elt
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					val = kv.Value // map[K]Deps element: key: {..}
				}
				inner, ok := val.(*ast.CompositeLit)
				if !ok || inner.Type != nil {
					continue
				}
				classify(inner)
			}
		}
		return true
	})

	return literals, violations, nil
}

// TestScanDepsHealers is the permanent break-test: it drives the single
// classifier over complete-file snippets so the strengthened guard is proven to
// fail on every nil/absent spelling — including the alias, type-elided and
// foreign-callee holes the string-count guard could not see. Each snippet is a
// full file (package clause + imports + body); the helper, not the walker,
// decides the verdict, so a walker path bug cannot leave these green.
func TestScanDepsHealers(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		lits  int
		viols int
	}{
		{
			name: "in-package constructor",
			src:  "package ingest\nvar _ = Deps{GraphHealer: NewEngineHealer(e)}\n",
			lits: 1, viols: 0,
		},
		{
			name: "cross-package constructor",
			src:  "package main\nimport \"" + ingestImportPath + "\"\nvar _ = ingest.Deps{GraphHealer: ingest.NewEngineHealer(e)}\n",
			lits: 1, viols: 0,
		},
		{
			name: "multi-line literal (site #5 shape)",
			src:  "package main\nimport \"" + ingestImportPath + "\"\nvar _ = ingest.Deps{\n\tVault: v,\n\tGraphHealer: ingest.NewEngineHealer(e),\n}\n",
			lits: 1, viols: 0,
		},
		{
			name: "nil value",
			src:  "package ingest\nvar _ = Deps{GraphHealer: nil}\n",
			lits: 1, viols: 1,
		},
		{
			name: "omitted field",
			src:  "package ingest\nvar _ = Deps{Vault: v}\n",
			lits: 1, viols: 1,
		},
		{
			name: "positional literal",
			src:  "package ingest\nvar _ = Deps{v, e, emb, NewEngineHealer(e)}\n",
			lits: 1, viols: 1,
		},
		{
			name: "aliased import, nil (alias hole)",
			src:  "package main\nimport ing \"" + ingestImportPath + "\"\nvar _ = ing.Deps{GraphHealer: nil}\n",
			lits: 1, viols: 1,
		},
		{
			name: "type-elided element, nil (elided hole)",
			src:  "package main\nimport \"" + ingestImportPath + "\"\nvar _ = []ingest.Deps{{GraphHealer: nil}}\n",
			lits: 1, viols: 1,
		},
		{
			name: "map-elided element, nil (map arm)",
			src:  "package main\nimport \"" + ingestImportPath + "\"\nvar _ = map[string]ingest.Deps{\"a\": {GraphHealer: nil}}\n",
			lits: 1, viols: 1,
		},
		{
			name: "cross-package type alias, nil (alias hole)",
			src:  "package main\nimport \"" + ingestImportPath + "\"\ntype D = ingest.Deps\nvar _ = D{GraphHealer: nil}\n",
			lits: 1, viols: 1,
		},
		{
			name: "in-package type alias, nil",
			src:  "package ingest\ntype D = Deps\nvar _ = D{GraphHealer: nil}\n",
			lits: 1, viols: 1,
		},
		{
			name: "alias chain, nil (chain hole)",
			src:  "package main\nimport \"" + ingestImportPath + "\"\ntype D = ingest.Deps\ntype E = D\nvar _ = E{GraphHealer: nil}\n",
			lits: 1, viols: 1,
		},
		{
			name: "parenthesized alias, nil (paren hole)",
			src:  "package main\nimport \"" + ingestImportPath + "\"\ntype D = (ingest.Deps)\nvar _ = D{GraphHealer: nil}\n",
			lits: 1, viols: 1,
		},
		{
			name: "alias chain, constructor-wired (over-reach guard)",
			src:  "package main\nimport \"" + ingestImportPath + "\"\ntype D = ingest.Deps\ntype E = D\nvar _ = E{GraphHealer: ingest.NewEngineHealer(e)}\n",
			lits: 1, viols: 0,
		},
		{
			name: "foreign X.NewEngineHealer callee",
			src:  "package main\nimport \"" + ingestImportPath + "\"\nvar _ = ingest.Deps{GraphHealer: x.NewEngineHealer(e)}\n",
			lits: 1, viols: 1,
		},
		{
			name: "pre-built healer var (strict default, no escape hatch)",
			src:  "package main\nimport \"" + ingestImportPath + "\"\nvar _ = ingest.Deps{GraphHealer: h}\n",
			lits: 1, viols: 1,
		},
		{
			name: "stray GraphHealer token outside any literal",
			src:  "package main\nvar _ = x.GraphHealer\n",
			lits: 0, viols: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lits, viols, err := scanDepsHealers([]byte(tc.src))
			if err != nil {
				t.Fatalf("parse snippet: %v", err)
			}
			if lits != tc.lits {
				t.Fatalf("literals = %d, want %d", lits, tc.lits)
			}
			if len(viols) != tc.viols {
				t.Fatalf("violations = %d, want %d: %+v", len(viols), tc.viols, viols)
			}
		})
	}
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from the test directory")
		}
		dir = parent
	}
}
