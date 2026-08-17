// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// atomicfileWriteRootHazard is the consequence of getting the first argument
// wrong. It is the need-to-know text for this rule: it fires only on a bad call
// site, and it is where a reader learns why the argument matters at all.
const atomicfileWriteRootHazard = "atomicfile.Write stamps the MCP surface version for vaultRoot/path ONLY when " +
	"vaultRoot is non-empty (internal/atomicfile/atomicfile.go) — pass \"\" or any substitute for a VAULT " +
	"destination and the write silently succeeds while skipping the stamp, which is how the vault loses its " +
	"version floor and a host starts looking older than it is. Pass the real vault.Root. A genuinely non-vault " +
	"destination may pass \"\", but not from this package: internal/tools writes vault paths"

// TestVaultWritesFromToolsPassTheRealVaultRoot requires atomicfile.Write's first
// argument to be a `.Root` selector, because an empty vaultRoot skips the stamp.
func TestVaultWritesFromToolsPassTheRealVaultRoot(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0
	var sites []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Write" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "atomicfile" {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}

			checked++
			pos := fset.Position(call.Pos())
			site := filepath.Base(pos.Filename) + ":" + strconv.Itoa(pos.Line)
			sites = append(sites, site)

			// The real root is a selector ending in `.Root` — vault.Root, v.Root.
			// Anything else (a literal "", a bare identifier, a joined path) is
			// either the hazard or too clever to verify syntactically; both must
			// be looked at by a human rather than passed silently.
			root, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok || root.Sel.Name != "Root" {
				t.Errorf("%s: atomicfile.Write first argument is not a vault root — %s",
					site, atomicfileWriteRootHazard)
			}
			return true
		})
	}

	// 296: a no-result reads as green. If the walk stops seeing the source — a
	// renamed package dir, a parser change, a refactor that moves these writes —
	// every assertion above silently stops running and this test still passes.
	// Count the sites and name them, so the harness has to prove it emitted a
	// signal before its silence means anything.
	if checked < 2 {
		t.Fatalf("found only %d atomicfile.Write call site(s) in internal/tools (%v) — expected at least 2 "+
			"(the audit report and the vault commit.msg copy). The walk is not seeing this package's source, "+
			"so a passing verdict here would be vacuous", checked, sites)
	}
	t.Logf("checked %d atomicfile.Write call sites: %v", checked, sites)
}

// handWrittenVaultFileHazard is the why for the rule below. It fires only on an
// offending call site.
const handWrittenVaultFileHazard = "this writes a VAULT path with a raw os.* primitive instead of storage/vaultfs. " +
	"Those packages own the vaultlock advisory-lock discipline (ADR-003); a tool that does its own read + write " +
	"opts OUT of a lock other writers are taking, and concurrent writers then lose each other's edits — which is " +
	"exactly how the resume.md hole was born. Route it through a storage method, or through " +
	"atomicfile.Write(vault.Root, …) if a whole-file replace is genuinely correct here"

// rawWriteFuncs are the os primitives that write a file's contents. os.MkdirAll
// is deliberately absent: the deleted rule was about writing a vault FILE, and
// internal/tools legitimately creates vault DIRECTORIES (system_tools.go).
var rawWriteFuncs = map[string]bool{"WriteFile": true, "Create": true, "OpenFile": true}

// TestNoHandWrittenVaultFilesInTools forbids writing a vault-derived path from
// this package with a raw os.* primitive.
//
// A destination counts as vault-derived when it is assigned from a storage.Vault
// accessor (dest, err := vault.CommitMsgFile(slug)) or joined onto a vault root
// (abs := filepath.Join(vault.Root, …)). A path rooted anywhere else is not —
// system_tools.go writes the PROJECT-local .vibe-palace.toml under p.Path, which
// is correct and must stay green.
//
// Analysis is syntactic and biased to FALSE NEGATIVES, matching internal/sourceaudit's
// posture: a destination laundered through a helper or a struct field is not
// tracked. A noisy gate is a disabled gate; this is a tripwire for the known
// shape, not a proof of absence.
func TestNoHandWrittenVaultFilesInTools(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	rawWritesInspected := 0
	vaultDerivedResolved := 0
	var derived []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		// Pass 1 — which identifiers hold a vault-derived path?
		//
		// Iterated to a FIXED POINT rather than done in one sweep, because a
		// path can be built in stages (dir := vault.TasksDir(p); sub :=
		// filepath.Join(dir, "done")) and a single source-order sweep would
		// resolve those only when they happen to appear in dependency order.
		// Order-dependent coverage is coverage that changes when someone moves
		// a line.
		vaultPath := map[string]bool{}
		for {
			added := 0
			ast.Inspect(file, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for i, rhs := range as.Rhs {
					if i >= len(as.Lhs) {
						break
					}
					lhs, ok := as.Lhs[i].(*ast.Ident)
					if !ok || lhs.Name == "_" || vaultPath[lhs.Name] {
						continue
					}
					if isVaultDerived(rhs, vaultPath) {
						vaultPath[lhs.Name] = true
						added++
						vaultDerivedResolved++
						derived = append(derived, name+":"+lhs.Name)
					}
				}
				return true
			})
			if added == 0 {
				break
			}
		}

		// Pass 2 — raw writes whose destination is one of them.
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !rawWriteFuncs[sel.Sel.Name] {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
				return true
			}

			rawWritesInspected++
			site := name + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)

			bad := isVaultDerived(call.Args[0], vaultPath)
			if id, ok := call.Args[0].(*ast.Ident); ok && vaultPath[id.Name] {
				bad = true
			}
			if bad {
				t.Errorf("%s: os.%s to a vault path — %s", site, sel.Sel.Name, handWrittenVaultFileHazard)
			}
			return true
		})
	}

	// 296 again: this rule has ZERO offending sites today, so a green verdict is
	// only worth anything if both halves of the machinery are known to be live.
	// Assert the derivation tracker resolved the destinations we know exist, and
	// that the raw-write matcher saw at least one candidate to judge. Either one
	// silently breaking would otherwise leave this test passing forever.
	if vaultDerivedResolved < 2 {
		t.Fatalf("derivation tracker resolved only %d vault-derived destination(s) (%v) — expected at least 2 "+
			"(audit_tools abs, commit_msg_tools dest). It is no longer recognising vault paths, so a pass here is vacuous",
			vaultDerivedResolved, derived)
	}
	if rawWritesInspected < 1 {
		t.Fatalf("raw-write matcher inspected 0 os.WriteFile/Create/OpenFile call sites — expected at least 1 " +
			"(system_tools.go writes the project-local config). It is not seeing raw writes at all, so a pass here is vacuous")
	}
	t.Logf("inspected %d raw write site(s); resolved %d vault-derived destination(s): %v",
		rawWritesInspected, vaultDerivedResolved, derived)
}

// isVaultDerived reports whether an expression yields a vault path: a call on a
// storage.Vault path accessor, or a filepath.Join rooted at one — either
// directly on vault.Root, or on an identifier ALREADY known to hold a vault
// path.
//
// That last case is the one worth spelling out. `os.WriteFile(filepath.Join(
// tasksDir, "readme.md"), …)` is a hand-write of a vault file, and it is the
// most likely way one actually appears here: a one-line copy of the adjacent
// `os.MkdirAll(filepath.Join(tasksDir, "done"), …)` in the same function. A
// tripwire blind to it would miss the realistic addition while catching only
// the direct-accessor form.
func isVaultDerived(e ast.Expr, known map[string]bool) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	// vault.CommitMsgFile(slug), v.IterationsFile(p), vault.TasksDir(name).
	// Keyed on the accessor NAME, not merely the receiver: a Vault has many
	// methods returning tasks, stats and triples, and counting those as paths
	// would inflate this test's own anti-vacuity number with values that could
	// never be a write destination — an instrument lying about its coverage.
	if recv.Name == "vault" || recv.Name == "v" {
		n := sel.Sel.Name
		return strings.HasSuffix(n, "File") || strings.HasSuffix(n, "Dir") || strings.HasSuffix(n, "Path")
	}
	// filepath.Join(vault.Root, …) or filepath.Join(<known vault path>, …)
	if recv.Name == "filepath" && sel.Sel.Name == "Join" && len(call.Args) > 0 {
		if root, ok := call.Args[0].(*ast.SelectorExpr); ok && root.Sel.Name == "Root" {
			return true
		}
		if base, ok := call.Args[0].(*ast.Ident); ok && known[base.Name] {
			return true
		}
		// A nested join — filepath.Join(filepath.Join(tasksDir, …), …).
		if isVaultDerived(call.Args[0], known) {
			return true
		}
	}
	return false
}
