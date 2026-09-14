// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// envIsolationBypass reports an internal/integration test that sets an
// isolation-relevant environment variable directly — os.Setenv or t.Setenv on
// HOME, XDG_CONFIG_HOME or CLAUDE_HOME — instead of routing through
// testinfra.IsolateEnv (and its WithClaudeHome option).
//
// # Why this rule exists
//
// test-infra-shared-env-isolation-fixture consolidated eight internal/integration
// files' hand-rolled HOME/XDG_CONFIG_HOME isolation recipes onto one shared
// fixture. Nothing mechanical stopped a NEW test from hand-rolling the same
// recipe again — this rule is that mechanism. It is a pure regression guard:
// unlike vaultWriteFunnel or uninvokedFuncs, its steady state is intended to be
// ZERO findings, not a long-lived, reason-carrying baseline.
//
// # Scope: package name, not path
//
// internal/integration has no importable non-test surface — every file in it is
// a _test.go file declaring `package integration`. So `f.ast.Name.Name ==
// "integration"` IS "inside internal/integration/*_test.go", with no path-prefix
// matching needed, and it excludes internal/testinfra (a different package name)
// by construction: IsolateEnv's own t.Setenv("HOME", ...) call is out of scope
// without a special-case skip, the same way vaultWriteFunnel's atomicfile skip
// is explicit only because atomicfile shares no other distinguishing feature.
//
// # Whole-file walk, not per-FuncDecl
//
// vaultWriteFunnel and friends walk each top-level *ast.FuncDecl's body. That
// shape MISSES internal/integration/tool_coverage_test.go's fixture table,
// which stores each tool's setup as a `build: func(t *testing.T, h
// *testHarness) any {...}` closure — an *ast.FuncLit sitting inside a package-
// level var's composite literal, never a FuncDecl. That file is not a
// hypothetical: it held two live, real Setenv("CLAUDE_HOME", ...) bypasses at
// the time this rule was written (fixed in the same change — see the task's
// vault record). So this rule inspects the whole file via ast.Inspect(f.ast,
// ...), catching a Setenv call regardless of how deep inside closures,
// composite literals or map values it sits.
//
// # Keyed by file + var, not by function
//
// Finding.ID() excludes Pos so that moving a line doesn't change a finding's
// identity — but a call inside an anonymous closure has no stable enclosing
// function name to key by. Keying by "integration.<file>:<VAR>" instead keeps
// the ID stable across any edit that doesn't move the violation to a different
// file or change which var it sets, which is the coarsest key that stays true
// to the same principle. Pos still points at the exact call site for a human
// triaging the finding.
func envIsolationBypass(files []file) []Finding {
	byKey := map[string]*funnelBreach{}
	var order []string
	filesScanned := 0

	for _, f := range files {
		if !f.isTest || f.ast.Name.Name != "integration" {
			continue
		}
		filesScanned++

		ast.Inspect(f.ast, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Setenv" {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || (recv.Name != "os" && recv.Name != "t") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil || !isolationEnvVars[name] {
				return true
			}

			key := fmt.Sprintf("integration.%s:%s", filepath.Base(f.path), name)
			breachAdd(byKey, &order, key, f, call, fmt.Sprintf(
				"%s.Setenv(%q, ...) sets an isolation env var directly — %s",
				recv.Name, name, envIsolationHazard))
			return true
		})
	}

	var out []Finding
	for _, key := range order {
		b := byKey[key]
		sort.Strings(b.details)
		out = append(out, Finding{
			Kind:   KindEnvIsolationBypass,
			Symbol: key,
			Pos:    b.pos,
			Detail: strings.Join(b.details, " | "),
		})
	}

	// A no-result reads as green. If this walk stops seeing
	// internal/integration's test files — a package rename, a directory move,
	// a parser change — every match above silently stops running and the gate
	// still passes. Floor is on FILES SCANNED, not on matches: the expected
	// steady state for matches is zero, so a floor on matches would be
	// meaningless, but the package should always have a healthy population of
	// _test.go files to walk.
	//
	// Floor is derived, not remembered. Re-derive with:
	//
	//	ls internal/integration/*_test.go | wc -l
	const filesScannedFloor = 50
	if filesScanned < filesScannedFloor {
		out = append(out, Finding{
			Kind:   KindEnvIsolationBypass,
			Symbol: "sourceaudit.envIsolationBypass/VACUOUS",
			Pos:    "internal/sourceaudit/env_isolation.go",
			Detail: fmt.Sprintf(
				"saw only %d internal/integration _test.go file(s) declaring package integration, "+
					"expected at least %d — the walk is not seeing the package, so a passing verdict is "+
					"vacuous. Do NOT baseline this entry — fix the walk.",
				filesScanned, filesScannedFloor),
		})
	}

	return out
}

// isolationEnvVars are exactly the variables testinfra.IsolateEnv (and its
// WithClaudeHome option) manage. Tying the set to what IsolateEnv actually
// covers — rather than a broader guess at "similar" vars — keeps the rule's
// contract legible: a hand-rolled Setenv on any of these three has a sanctioned
// replacement to route through, so flagging it is actionable, not a judgment
// call.
var isolationEnvVars = map[string]bool{
	"HOME":            true,
	"XDG_CONFIG_HOME": true,
	"CLAUDE_HOME":     true,
}

const envIsolationHazard = "internal/integration tests must isolate HOME/XDG_CONFIG_HOME/CLAUDE_HOME " +
	"through testinfra.IsolateEnv (and its WithClaudeHome option for CLAUDE_HOME), not a direct " +
	"os.Setenv/t.Setenv — test-infra-shared-env-isolation-fixture consolidated eight hand-rolled " +
	"recipes onto that one fixture, and a new hand-rolled Setenv reintroduces the exact inconsistency " +
	"it closed. If this really cannot route through IsolateEnv, baseline it WITH THAT REASON"
