// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

// loadDeclaredFixturePackages writes a synthetic module — rooted at modulePath
// so declaredMCPGate/toolConstructors' PkgPath checks match it exactly — and
// loads it AST-only (no NeedTypes/NeedDeps): toolConstructors and
// declaredMCPGate read only p.Syntax and p.PkgPath, never type information, so
// this is enough to exercise them without the cost (or the dependency-closure
// requirements) of loadModulePackages' full type-checked load.
func loadDeclaredFixturePackages(t *testing.T, toolsSrc, mutatingNamesSrc string) []*packages.Package {
	t.Helper()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module "+modulePath+"\n\ngo 1.21\n")
	toolsDir := filepath.Join(root, "internal", "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(toolsDir, "tools.go"), toolsSrc)
	mustWriteFile(t, filepath.Join(toolsDir, "registry.go"), mutatingNamesSrc)

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax,
		Dir:  root,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	return pkgs
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// declaredFixtureToolsSrc plants two tool constructors: a plain *ast.FuncDecl
// (the shape toolConstructors has always seen) and a package-level func-literal
// var (the shape it could not see before the bindingScopes-style extension).
// mcp.Tool is referenced through an unresolved import — harmless, because this
// AST-only load never requests NeedImports/NeedDeps/NeedTypes and
// toolConstructors matches the composite literal SYNTACTICALLY, by the local
// identifier "mcp", exactly as it does on the real tree.
const declaredFixtureToolsSrc = `package tools

import "example.com/fixture/mcp"

func newRealTool() mcp.Tool {
	return mcp.Tool{Name: "real_tool"}
}

// newLiteralTool is the test-seam shape: a package-level func-literal var,
// mirroring internal/wrapstate.gitCmdRunner and internal/worktree.runGit.
var newLiteralTool = func() mcp.Tool {
	return mcp.Tool{Name: "literal_tool"}
}
`

// TestToolConstructorsSeesFuncLiteralVar is the detection-half mutation proof:
// toolConstructors must find BOTH the FuncDecl-based and the func-literal-var
// tool constructor, and must report the literal one — and ONLY the literal
// one — in literalCtors.
func TestToolConstructorsSeesFuncLiteralVar(t *testing.T) {
	pkgs := loadDeclaredFixturePackages(t, declaredFixtureToolsSrc, "package tools\n\nvar MutatingToolNames = []string{}\n")

	ctors, literalCtors := toolConstructors(pkgs)

	if ctors["newRealTool"] != "real_tool" {
		t.Errorf("newRealTool not resolved to tool \"real_tool\": got %v", ctors)
	}
	if ctors["newLiteralTool"] != "literal_tool" {
		t.Errorf("newLiteralTool (a package-level func-literal var) was not discovered as a tool "+
			"constructor — the FuncDecl-only walk this fix replaces would miss it entirely: got %v", ctors)
	}
	if !literalCtors["newLiteralTool"] {
		t.Errorf("newLiteralTool must be marked literal so diffMCP can flag it for human review "+
			"instead of trusting an SSA lookup that can never resolve it: got %v", literalCtors)
	}
	if literalCtors["newRealTool"] {
		t.Errorf("newRealTool is an ordinary *ast.FuncDecl and must NOT be marked literal: got %v", literalCtors)
	}
}

// TestDiffMCPFlagsFuncLiteralCtorUnconditionally is the anti-vacuity proof
// the Chair's plan requires: a func-literal-based tool constructor must be
// reported for human review NO MATTER what tools.MutatingToolNames says about
// it — including when Derived and Declared happen to agree, which is exactly
// the case a plain `derived == declared` comparison would silently pass
// through, because `derived` for such a ctor is unconditionally false (byName
// never resolves it) and a coincidental agreement is not verification.
func TestDiffMCPFlagsFuncLiteralCtorUnconditionally(t *testing.T) {
	byName := map[string]*ssa.Function{} // go/ssa never names this ctor; empty is the honest state
	reach := map[*ssa.Function]bool{}

	for _, tc := range []struct {
		name     string
		declared bool
	}{
		{"declared mutating", true},
		{"declared non-mutating", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := "package tools\n\nvar MutatingToolNames = []string{}\n"
			if tc.declared {
				registry = `package tools

var MutatingToolNames = []string{"literal_tool"}
`
			}
			pkgs := loadDeclaredFixturePackages(t, declaredFixtureToolsSrc, registry)

			divs := diffMCP(pkgs, byName, reach)

			var got *GateDivergence
			for i := range divs {
				if divs[i].Name == "literal_tool" {
					got = &divs[i]
				}
			}
			if got == nil {
				t.Fatalf("literal_tool (func-literal-var constructor) produced no divergence at all — "+
					"a func-literal ctor must always be flagged for review, declared=%v: got %v", tc.declared, divs)
			}
			if !got.Unverifiable {
				t.Errorf("literal_tool's divergence must be marked Unverifiable: %+v", got)
			}
			if got.Declared != tc.declared {
				t.Errorf("Declared = %v, want %v", got.Declared, tc.declared)
			}
			if got.Derived {
				t.Errorf("Derived must be false for a ctor byName can never resolve: %+v", got)
			}
		})
	}
}

// TestDiffMCPDoesNotFlagAnOrdinaryFuncDeclCtorAsUnverifiable is the precision
// half: an ordinary *ast.FuncDecl tool constructor must go through the normal
// derived/declared comparison, never the forced Unverifiable path. Declaring
// real_tool mutating (while byName cannot resolve it, same as any tool with no
// SSA build in this unit test) forces a genuine derived=false/declared=true
// divergence, so this proves the normal comparison still fires correctly and
// without Unverifiable — not merely that Unverifiable is absent by accident.
func TestDiffMCPDoesNotFlagAnOrdinaryFuncDeclCtorAsUnverifiable(t *testing.T) {
	pkgs := loadDeclaredFixturePackages(t, declaredFixtureToolsSrc, `package tools

var MutatingToolNames = []string{"real_tool"}
`)
	byName := map[string]*ssa.Function{}
	reach := map[*ssa.Function]bool{}

	divs := diffMCP(pkgs, byName, reach)

	var got *GateDivergence
	for i := range divs {
		if divs[i].Name == "real_tool" {
			got = &divs[i]
		}
	}
	if got == nil {
		t.Fatalf("real_tool declared mutating but unresolvable in byName should diverge (derived=false, "+
			"declared=true) through the ORDINARY comparison: got %v", divs)
	}
	if got.Unverifiable {
		t.Errorf("real_tool is an ordinary *ast.FuncDecl constructor and must not be Unverifiable: %+v", got)
	}
	if got.Derived {
		t.Errorf("Derived must be false: byName has no entry for real_tool: %+v", got)
	}
	if !got.Declared {
		t.Errorf("Declared must be true: %+v", got)
	}
}
