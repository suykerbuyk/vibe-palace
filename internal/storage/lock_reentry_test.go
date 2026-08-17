// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// lockReentryHazard is the why for this rule, printed only for an offending
// function.
const lockReentryHazard = "it holds the vaultlock for this path and then calls lockedWrite on the SAME path, " +
	"which acquires that very lock again. " +
	"vaultlock.Acquire opens a FRESH fd per call and flock associates a lock with the open file " +
	"DESCRIPTION, so the second acquisition contends with the first — inside the same process, and even " +
	"on the same goroutine. flockExclusive is a bare LOCK_EX with no LOCK_NB and no timeout, so this is a " +
	"PERMANENT HANG, not an error: no deadlock detector fires, no test times out on its own, the call " +
	"simply never returns. A caller that already holds the lock must write with " +
	"atomicfile.Write(v.Root, absPath, data) directly, under the held lock — that is exactly what " +
	"lockedWrite's own doc comment says it is not for"

// TestNoLockReentryInStorage forbids a function from calling lockedWrite on a
// path whose vaultlock it already holds.
//
// SAME PATH is the whole rule, and the narrowness is load-bearing. Holding one
// lock while taking another is legitimate and this package does it deliberately:
// UpsertSessionByKey, LinkArchiveToSessions and BackfillArchiveLink each lock the
// sessions DIRECTORY and then write individual NOTE paths through lockedWrite.
// vaultlock keys on a sha256 of the canonical path, so those are different lock
// files and the nesting is safe — sessions.go documents that ordering discipline
// explicitly. A check that flagged "holds a lock and reaches lockedWrite" would
// redden all three correct sites; the first draft of this test did exactly that.
//
// Analysis is syntactic, biased to FALSE NEGATIVES like internal/sourceaudit:
// the match is on the IDENTIFIER passed to both calls within one function body,
// so a path laundered through a second variable, a struct field, or a helper in
// another function is not tracked. It is a tripwire for the shape the rule
// forbids, not a proof of absence.
//
// NOT covered, deliberately: the lock ORDER discipline itself (directory before
// file, never the reverse), which sessions.go warns is also a permanent hang when
// inverted. That is a different rule and is left as prose.
func TestNoLockReentryInStorage(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()

	const primitive = "lockedWrite"
	holders := 0
	lockedWriteCalls := 0
	acquireArgsResolved := 0
	var holderNames, callerNames []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Name.Name == primitive {
				continue
			}

			held := map[string]bool{}   // identifiers whose lock this function holds
			writes := map[string]bool{} // identifiers passed to lockedWrite
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 1 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "vaultlock" &&
					(sel.Sel.Name == "Acquire" || sel.Sel.Name == "TryAcquire") {
					if len(call.Args) >= 2 {
						if id, ok := call.Args[1].(*ast.Ident); ok {
							held[id.Name] = true
							acquireArgsResolved++
						}
					}
					return true
				}
				if sel.Sel.Name == primitive {
					lockedWriteCalls++
					site := name + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
					callerNames = append(callerNames, site+" "+fd.Name.Name)
					if id, ok := call.Args[0].(*ast.Ident); ok {
						writes[id.Name] = true
					}
				}
				return true
			})

			if len(held) > 0 {
				holders++
				holderNames = append(holderNames, fd.Name.Name)
			}
			for w := range writes {
				if held[w] {
					t.Errorf("%s: %s locks %q and then calls %s(%s) — %s",
						name+":"+strconv.Itoa(fset.Position(fd.Pos()).Line), fd.Name.Name, w, primitive, w, lockReentryHazard)
				}
			}
		}
	}

	// 296: a source-walking test that stops seeing the source passes silently, and
	// this rule has ZERO offending sites today, so a clean-tree pass is worthless
	// unless every half of the machinery is demonstrably live: the walk must find
	// lock holders, must RESOLVE the path identifier each Acquire was given, and
	// must find lockedWrite call sites to compare them against.
	if holders < 5 {
		t.Fatalf("found only %d vaultlock-holding function(s) in internal/storage (%v) — expected many. "+
			"The walk is not recognising vaultlock.Acquire, so a pass here is vacuous", holders, holderNames)
	}
	if acquireArgsResolved < 5 {
		t.Fatalf("resolved only %d vaultlock.Acquire path argument(s) — expected many. The comparison below "+
			"is against an empty set, so a pass here is vacuous", acquireArgsResolved)
	}
	if lockedWriteCalls < 1 {
		t.Fatalf("found 0 %s call sites — expected at least 1. There is nothing to compare the held "+
			"paths against, so a pass here is vacuous", primitive)
	}
	t.Logf("checked %d lock-holding function(s), %d resolved Acquire path arg(s), %d %s call site(s): %v",
		holders, acquireArgsResolved, lockedWriteCalls, primitive, callerNames)
}
