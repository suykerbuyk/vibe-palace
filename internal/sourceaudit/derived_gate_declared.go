// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"go/ast"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

// The DECLARED half of the derivation: what the tree says today, read by
// PARSING it.
//
// 🔴 PARSED, NEVER GREPPED, and this is a corrected mistake rather than a
// preference. An earlier pass matched `mutates(cmdX())` with a regex and
// concluded `vp archive create` was an ungated writer. It is written
// `mutates(cmdArchiveCreate(info))` — the regex missed the argument, and the
// analysis reported a security finding that did not exist. Both declared sets
// below come off the AST of the same packages the call graph was built from,
// so they cannot describe a different tree than the derivation does.

// diffCLI reports every registered command whose derived predicate disagrees
// with its mutates() wrapping in cmd/vp/commands.go.
func diffCLI(pkgs []*packages.Package, byName map[string]*ssa.Function, reach map[*ssa.Function]bool) []GateDivergence {
	declared := declaredCLIGate(pkgs)

	names := make([]string, 0, len(declared))
	for n := range declared {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []GateDivergence
	for _, ctor := range names {
		fn := byName[modulePath+"/cmd/vp."+ctor]
		derived := fn != nil && reach[fn]
		if derived == declared[ctor] {
			continue
		}
		out = append(out, GateDivergence{
			Surface:  "cli",
			Name:     ctor,
			ctor:     ctor,
			Derived:  derived,
			Declared: declared[ctor],
		})
	}
	return out
}

// diffMCP reports every registered tool whose derived predicate disagrees with
// its membership in tools.MutatingToolNames.
//
// 🔴 IT DIFFS AGAINST THE SURFACE GATE'S LIST ONLY. `Mutating` was split in
// 8a45673: tools.ReadOnlyServeToolNames is a separate, fail-closed,
// hand-declared ALLOW-list answering "may a read-only `vp mcp serve` expose
// this?". That one must stay hand-declared — deriving it would defeat the
// reason it exists, since a derived predicate computes FALSE for the
// git-channel tools that must never be served. Nothing in this file reads it,
// and nothing in this file should.
func diffMCP(pkgs []*packages.Package, byName map[string]*ssa.Function, reach map[*ssa.Function]bool) []GateDivergence {
	declared := declaredMCPGate(pkgs)
	ctors := toolConstructors(pkgs)

	tools := make([]string, 0, len(ctors))
	for _, name := range ctors {
		tools = append(tools, name)
	}
	sort.Strings(tools)

	byTool := map[string]string{}
	for ctor, tool := range ctors {
		byTool[tool] = ctor
	}

	var out []GateDivergence
	for _, tool := range tools {
		ctor := byTool[tool]
		fn := byName[modulePath+"/internal/tools."+ctor]
		derived := fn != nil && reach[fn]
		if derived == declared[tool] {
			continue
		}
		out = append(out, GateDivergence{
			Surface:  "mcp",
			Name:     tool,
			ctor:     ctor,
			Derived:  derived,
			Declared: declared[tool],
		})
	}
	return out
}

// declaredCLIGate maps each registered command constructor to whether its
// Register(...) argument is wrapped in mutates().
func declaredCLIGate(pkgs []*packages.Package) map[string]bool {
	out := map[string]bool{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.PkgPath != modulePath+"/cmd/vp" {
			return
		}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != registerFunc || len(call.Args) != 1 {
					return true
				}
				arg := call.Args[0]
				gated := false
				// mutates(cmdX(...)) — unwrap ONE level, keeping whatever
				// arguments the constructor takes.
				if inner, ok := arg.(*ast.CallExpr); ok {
					if id, ok := inner.Fun.(*ast.Ident); ok && id.Name == mutatesWrapper && len(inner.Args) == 1 {
						gated = true
						arg = inner.Args[0]
					}
				}
				if ctor, ok := arg.(*ast.CallExpr); ok {
					if id, ok := ctor.Fun.(*ast.Ident); ok {
						out[id.Name] = gated
					}
				}
				return true
			})
		}
	})
	return out
}

// declaredMCPGate maps each tool name to whether it appears in
// tools.MutatingToolNames — the surface gate's canonical declaration.
//
// The registry itself is pinned to that list by
// TestMutatingToolNamesMatchRegistry, so reading the list reads the registry's
// truth without importing internal/tools into an analysis package.
func declaredMCPGate(pkgs []*packages.Package) map[string]bool {
	out := map[string]bool{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.PkgPath != modulePath+"/internal/tools" {
			return
		}
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "MutatingToolNames" {
						continue
					}
					for _, v := range vs.Values {
						cl, ok := v.(*ast.CompositeLit)
						if !ok {
							continue
						}
						for _, el := range cl.Elts {
							if lit, ok := el.(*ast.BasicLit); ok {
								out[strings.Trim(lit.Value, `"`)] = true
							}
						}
					}
				}
			}
		}
	})
	return out
}

// toolConstructors maps each function in internal/tools that builds an mcp.Tool
// composite literal to the tool NAME in that literal.
//
// Keyed off the literal rather than off RegisterAll because the name is the
// identity everything else uses — MutatingToolNames, the surface golden, the
// wire. A constructor that builds a Tool without a literal Name would be
// invisible here; there are none today, and the 1:1 count against the golden
// (70 constructors, 70 registered tools) is asserted by the test.
func toolConstructors(pkgs []*packages.Package) map[string]string {
	out := map[string]string{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.PkgPath != modulePath+"/internal/tools" {
			return
		}
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Recv != nil {
					continue
				}
				ast.Inspect(fd, func(n ast.Node) bool {
					cl, ok := n.(*ast.CompositeLit)
					if !ok {
						return true
					}
					sel, ok := cl.Type.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Tool" {
						return true
					}
					if pkgIdent, ok := sel.X.(*ast.Ident); !ok || pkgIdent.Name != "mcp" {
						return true
					}
					for _, el := range cl.Elts {
						kv, ok := el.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						k, ok := kv.Key.(*ast.Ident)
						if !ok || k.Name != "Name" {
							continue
						}
						if lit, ok := kv.Value.(*ast.BasicLit); ok {
							out[fd.Name.Name] = strings.Trim(lit.Value, `"`)
						}
					}
					return true
				})
			}
		}
	})
	return out
}
