// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

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
	"version floor and a host starts looking older than it is. Pass the real v.Root. A genuinely non-vault " +
	"destination may pass \"\" (internal/wrapstate writes project-root anchors that way, correctly), but not " +
	"from this package: internal/storage writes vault paths"

// TestVaultWritesFromStoragePassTheRealVaultRoot requires atomicfile.Write's
// first argument to be a `.Root` selector, because an empty vaultRoot skips the
// surface stamp.
//
// WHY THIS EXISTS WHEN TestEveryVaultWriterStamps ALREADY COVERS STAMPING:
// that test is a BEHAVIORAL ENUMERATION — it drives 13 named writers and asserts
// a .surface stamp lands. Enumeration cannot cover a call site nobody added a
// case for, and it cannot cover a FUTURE one at all. Measured, not assumed:
// changing internal/storage/memory.go's WriteMemory to atomicfile.Write("", …)
// compiles, leaves TestEveryVaultWriterStamps green, and leaves the ENTIRE
// storage suite green — a real vault write silently skipping its stamp, caught
// by nothing. This check is structural, so it covers every call site in the
// package including the ones no behavioral case names.
//
// The two tests are complements, not duplicates: this one proves the ARGUMENT is
// right at every site, that one proves the EFFECT lands for the writers it
// drives. Neither subsumes the other.
//
// Analysis is syntactic and biased to FALSE NEGATIVES, matching
// internal/sourceaudit's posture and the sibling check in internal/tools: a root
// laundered through a helper or a struct field is not tracked. A noisy gate is a
// disabled gate; this is a tripwire for the known shape, not a proof of absence.
func TestVaultWritesFromStoragePassTheRealVaultRoot(t *testing.T) {
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

			// The real root is a selector ending in `.Root` — v.Root. Anything
			// else (a literal "", a bare identifier, a joined path) is either the
			// hazard or too clever to verify syntactically; both must be looked at
			// by a human rather than passed silently.
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
	//
	// The floor is DERIVED, not remembered: 16 is the count this walk actually
	// found in internal/storage when the check was written, re-derived with
	//
	//   grep -rn "atomicfile\.Write(" --include='*.go' internal/storage/ \
	//     | grep -v '_test.go' | grep -vE ':[0-9]+:\s*//' | wc -l
	//
	// If you add a writer this stays green. If you legitimately DELETE one it goes
	// red, and that is intended: re-derive the floor deliberately and change it in
	// the same commit. Never edit this number to make a red test pass — that is
	// the one move that converts the guard back into the vacuous pass it exists to
	// prevent.
	const derivedFloor = 16
	if checked < derivedFloor {
		t.Fatalf("found only %d atomicfile.Write call site(s) in internal/storage (%v) — expected at least %d. "+
			"Either the walk is not seeing this package's source, so a passing verdict here would be vacuous, "+
			"or a writer was deleted and the floor needs re-deriving in this commit", checked, sites, derivedFloor)
	}
	t.Logf("checked %d atomicfile.Write call sites: %v", checked, sites)
}
