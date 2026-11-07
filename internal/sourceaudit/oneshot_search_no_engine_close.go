// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
)

// oneshotSearchNoEngineClose keeps the ONE-SHOT `vp search` path free of a
// deferred Close that would join the detached embedder construct.
//
// # Why this rule exists
//
// cmdSearch builds its embedder lazily, so the ~90 MB download and pipeline
// build run on WarmEmbedder's detached goroutine and the caller's wait is
// bounded by readyWait (internal/search/engine.go:380, be6eca2). Engine.Close
// joins warmWG (internal/search/engine.go:3243) BEFORE closing the embedder
// (S-D1). That join is correct for `vp mcp`, a process that keeps running, and
// wrong for a one-shot CLI: a `defer eng.Close()` there would print the bounded
// refusal and then sit through the whole cold model load anyway, which is the
// original symptom moved from before the message to after it.
//
// `defer emb.Close()` is forbidden for a different and smaller reason: it does
// not join warmWG, so it is no substitute for the wait that was removed, and it
// no-ops while the construct is still in flight (internal/embedder/lazy.go:84).
// Keeping one half of a pair removed for a stated reason invites the next
// reader to restore the other half.
//
// The hazard is the ABSENCE of two statements, which no behavioural assertion
// at cmd/vp scope can see cheaply: readyWait is an unexported package var in
// internal/search, so a behavioural witness would have to sit through the real
// 15 s. Hence an AST rule.
//
// # What it checks
//
// In main.cmdSearch only: find the `search.NewEngine(...)` call, bind the
// identifier its result is assigned to and the identifier passed as its first
// argument (the embedder), and report any `defer <either>.Close()` under the
// block that holds that assignment. Two anchors keep the rule from going
// silently green: the scope must exist, and it must still contain a direct
// search.NewEngine call.
//
// # What it cannot see, stated
//
// It is AST-only and ident-based, like every rule here. It does not see:
//
//   - `defer search.NewEngine(emb, vault, cfg).Close()` — no assignment, so no
//     engine ident to bind, and the deferred selector's X is a call, not an
//     ident;
//   - a close routed through a helper (`defer closeAll(eng, emb)`), a closure
//     (`defer func() { eng.Close() }()`), a variable (`cleanup := eng.Close`),
//     or a cli-level teardown hook;
//   - a close inside a function cmdSearch calls;
//   - an engine built through a wrapper rather than a direct search.NewEngine
//     call — which also silently disarms the rule, which is what the second
//     anchor exists for.
//
// Matching is by NAME within that block, not by type or resolved object, so a
// scope that re-binds either name in a shape the shadow set does not model is a
// known FALSE-POSITIVE shape: a type-switch guard, a range variable, a
// labelled construct, a struct field selected as x.emb, or an if/for/switch
// INIT clause (`if eng := open(); ok { defer eng.Close() }`), whose declaration
// is in the statement header rather than the body's statement list. The shadow set models the
// two common closure cases — a nested `:=`/`var` re-bind and a closure
// parameter — and anything beyond them fires LOUDLY rather than silently, which
// is the safe direction: a false positive gets fixed, a guard that cannot fire
// is this project's dominant defect.
const oneshotSearchScope = "main.cmdSearch"

// oneshotSearchEngineCtor is the constructor call whose result and first
// argument this rule binds.
const (
	oneshotSearchEnginePkg  = "search"
	oneshotSearchEngineFunc = "NewEngine"
)

func oneshotSearchNoEngineClose(files []file) []Finding {
	var out []Finding
	scopeFound, ctorFound := false, false

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		for _, s := range bindingScopes(f) {
			if pkg+"."+s.name != oneshotSearchScope {
				continue
			}
			scopeFound = true
			assign, call := oneshotEngineAssign(s.body)
			if call == nil {
				continue
			}
			ctorFound = true

			names := map[string]bool{}
			if assign != nil {
				if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
					names[id.Name] = true
				}
			}
			if len(call.Args) > 0 {
				if id, ok := call.Args[0].(*ast.Ident); ok && id.Name != "_" {
					names[id.Name] = true
				}
			}
			if len(names) == 0 {
				continue
			}

			body := s.body
			if assign != nil {
				if holder := oneshotBlockHolding(s.body, assign); holder != nil {
					body = holder
				}
			}
			oneshotScanBlock(body, names, true, func(d *ast.DeferStmt, name string) {
				out = append(out, Finding{
					Kind:   KindOneshotSearchEngineClose,
					Symbol: oneshotSearchScope + "/defer " + name + ".Close",
					Pos:    posOf(f, d.Pos()),
					Detail: fmt.Sprintf(
						"%s defers %s.Close() on the ONE-SHOT `vp search` path. Engine.Close joins warmWG before "+
							"closing the embedder (S-D1), so with the lazy embedder this makes the command print its "+
							"bounded readyWait refusal and then sit through the whole cold ~90 MB model load anyway — "+
							"the hang moved from before the message to after it. Closing only the embedder is not a "+
							"substitute either: LazyEmbedder.Close does not join the warm goroutine and no-ops while "+
							"the construct is in flight. Delete the defer. Engine.Close is NOT weakened: `vp mcp` "+
							"still needs that join. Nothing durable is lost — every durable write on this path "+
							"completes inside Search, before the defer would run.",
						oneshotSearchScope, name),
				})
			})
		}
	}

	if !scopeFound {
		out = append(out, Finding{
			Kind:   KindOneshotSearchEngineClose,
			Symbol: "sourceaudit.oneshotSearchNoEngineClose/ABSENT/" + oneshotSearchScope,
			Pos:    "internal/sourceaudit/oneshot_search_no_engine_close.go",
			Detail: fmt.Sprintf(
				"this rule guards %q and NO function by that name exists in the audited tree. Either it was "+
					"renamed or moved (update oneshotSearchScope in the same commit) or it was deleted (remove "+
					"the rule). Do NOT baseline this entry — a baselined anchor is a disabled rule.",
				oneshotSearchScope),
		})
	} else if !ctorFound {
		out = append(out, Finding{
			Kind:   KindOneshotSearchEngineClose,
			Symbol: "sourceaudit.oneshotSearchNoEngineClose/NOCTOR/" + oneshotSearchScope,
			Pos:    "internal/sourceaudit/oneshot_search_no_engine_close.go",
			Detail: fmt.Sprintf(
				"%s no longer contains a direct %s.%s call, so this rule binds no engine or embedder identifier "+
					"and guards nothing. Engine construction moved into a helper or behind a wrapper; follow it "+
					"there or the deferred-Close hang can be reintroduced invisibly. Do NOT baseline this entry.",
				oneshotSearchScope, oneshotSearchEnginePkg, oneshotSearchEngineFunc),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// oneshotEngineAssign finds the search.NewEngine call under body and, when its
// result is assigned at statement level, the AssignStmt that binds it. The call
// is returned even when there is no such assignment, so the second anchor can
// tell "no constructor" from "constructed but not bound".
func oneshotEngineAssign(body *ast.BlockStmt) (*ast.AssignStmt, *ast.CallExpr) {
	var call *ast.CallExpr
	var assign *ast.AssignStmt
	ast.Inspect(body, func(n ast.Node) bool {
		if call != nil {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if c, isCtor := as.Rhs[0].(*ast.CallExpr); isCtor && isOneshotEngineCtor(c) {
				assign, call = as, c
				return false
			}
		}
		if c, isCall := n.(*ast.CallExpr); isCall && isOneshotEngineCtor(c) {
			call = c
			return false
		}
		return true
	})
	return assign, call
}

// isOneshotEngineCtor reports whether c is a direct `search.NewEngine(...)`
// call.
func isOneshotEngineCtor(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != oneshotSearchEngineFunc {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == oneshotSearchEnginePkg
}

// oneshotBlockHolding returns the block whose own statement list contains st.
func oneshotBlockHolding(root *ast.BlockStmt, st ast.Stmt) *ast.BlockStmt {
	var found *ast.BlockStmt
	ast.Inspect(root, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		blk, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for _, s := range blk.List {
			if s == st {
				found = blk
				return false
			}
		}
		return true
	})
	return found
}

// oneshotScanBlock walks b reporting every `defer <name>.Close()` for a name
// still in the active set. top marks the binding block itself, whose own `:=`
// statements ARE the bindings; every nested block drops the names it re-binds.
//
// 🔴 THE DROP IS IN SOURCE ORDER, ONE STATEMENT AT A TIME, AND THAT IS THE
// POINT. A Go declaration shadows only from its own position to the end of its
// block, so a defer that PRECEDES the declaration still refers to the outer
// value. Collecting the whole block's declarations before scanning it — which
// is what this function used to do — dropped the name too early and missed
// exactly that shape:
//
//	defer eng.Close()    // the OUTER eng: legal Go, forbidden here
//	eng := openLedger()  // declared later in the same block
//
// oneshotDeferBeforeShadow is the fixture for it, asserted to FIRE.
func oneshotScanBlock(b *ast.BlockStmt, names map[string]bool, top bool, hit func(*ast.DeferStmt, string)) {
	cur := names
	for _, st := range b.List {
		if len(cur) == 0 {
			return
		}
		if nested, ok := st.(*ast.BlockStmt); ok {
			oneshotScanBlock(nested, cur, false, hit)
		} else {
			oneshotScanStmt(st, cur, hit)
		}
		// AFTER this statement, not before the block: a name it declares
		// shadows only what follows it. The binding block is exempt because its
		// own `:=` statements are the bindings being tracked, not shadows.
		if !top {
			cur = oneshotDropNames(cur, oneshotStmtDeclares(st))
		}
	}
}

// oneshotScanStmt walks one statement's subtree, handing nested blocks and
// func literals back to oneshotScanBlock with a reduced name set.
func oneshotScanStmt(st ast.Stmt, names map[string]bool, hit func(*ast.DeferStmt, string)) {
	ast.Inspect(st, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncLit:
			oneshotScanBlock(v.Body, oneshotDropNames(names, oneshotParamNames(v.Type.Params)), false, hit)
			return false
		case *ast.BlockStmt:
			oneshotScanBlock(v, names, false, hit)
			return false
		case *ast.DeferStmt:
			if name, ok := oneshotDeferredClose(v); ok && names[name] {
				hit(v, name)
			}
			return true
		}
		return true
	})
}

// oneshotDeferredClose reports the receiver identifier of a `defer x.Close()`.
func oneshotDeferredClose(d *ast.DeferStmt) (string, bool) {
	sel, ok := d.Call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Close" {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

// oneshotStmtDeclares returns the identifiers ONE statement declares with `:=`
// or `var`. The caller applies it after scanning that statement, so a name is
// dropped from the statement that declares it onward and never before.
func oneshotStmtDeclares(st ast.Stmt) []string {
	var declared []string
	switch v := st.(type) {
	case *ast.AssignStmt:
		if v.Tok != token.DEFINE {
			return nil
		}
		for _, lhs := range v.Lhs {
			if id, ok := lhs.(*ast.Ident); ok {
				declared = append(declared, id.Name)
			}
		}
	case *ast.DeclStmt:
		gd, ok := v.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			return nil
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, id := range vs.Names {
				declared = append(declared, id.Name)
			}
		}
	}
	return declared
}

// oneshotParamNames returns the parameter names of a func literal.
func oneshotParamNames(params *ast.FieldList) []string {
	if params == nil {
		return nil
	}
	var out []string
	for _, field := range params.List {
		for _, id := range field.Names {
			out = append(out, id.Name)
		}
	}
	return out
}

// oneshotDropNames returns names minus drop, without mutating names.
func oneshotDropNames(names map[string]bool, drop []string) map[string]bool {
	if len(drop) == 0 {
		return names
	}
	out := make(map[string]bool, len(names))
	for n := range names {
		out[n] = true
	}
	for _, d := range drop {
		delete(out, d)
	}
	return out
}
