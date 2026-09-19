// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"sort"
)

// gitEnabledOwner keeps ONE owner of the git_enabled = false refusal.
//
// # Why this rule exists
//
// The operator's acceptance for task mcp-vault-tidy-and-sync-do-not-honour-
// git-enabled-false is "functional parity from both the CLI and the MCP": one
// behaviour reachable two ways, never two implementations that agree today.
// The parity test cannot pin that. A surface that owned its own refusal — read
// the setting itself, wrap ErrGitDisabled itself, and run raw git when enabled
// — passes every parity assertion by construction: errors.Is holds, the text
// and exit code match, and the fingerprint cannot move. That is the exact
// shape the ruling forbids, so it is caught here instead.
//
// It reports two things:
//
//  1. A call to HostGitEnabled from any function outside gitEnabledReaders.
//     Inside internal/storage the allow-list is exactly RefuseIfGitDisabled,
//     so an inner function re-reading the config is a finding too: that is
//     the read-once-per-entry-call property. The other two entries are
//     REPORTING readers that refuse nothing.
//  2. Any reference to ErrGitDisabled or ErrGitConfigUnreadable in a package
//     other than storage that is not the second argument of errors.Is.
//     Mapping the refusal is allowed everywhere; constructing, wrapping or
//     returning the sentinel outside storage is not.
//
// # What it cannot see, stated
//
// It is AST-only and name-based, like every rule in this package. A hand-rolled
// os.ReadFile plus a TOML decode of git_enabled outside storage is invisible to
// it; deleting Config.GitEnabled removed the easy path to that. A surface that
// forges its own refusal WITHOUT the sentinel is caught by the parity test's
// errors.Is assertion instead. The two together cover the forbidden shape;
// neither does alone. "storage" is matched by package NAME, so a fixture or a
// stray package of that name is treated as the owner.
var gitEnabledReaders = map[string]string{
	"storage.RefuseIfGitDisabled":          "the refusal's one reader",
	"reconcile.VaultReconciler.gitEnabled": "decides whether `vp config sync` plans a git init (reporting)",
	"tools.computeVaultDirt":               "picks the bootstrap dirt-alert text (reporting)",
}

// gitEnabledSentinels are the refusal's two errors.
var gitEnabledSentinels = map[string]bool{
	"ErrGitDisabled":         true,
	"ErrGitConfigUnreadable": true,
}

func gitEnabledOwner(files []file) []Finding {
	var out []Finding
	seen := map[string]bool{}
	found := map[string]bool{}
	add := func(f Finding) {
		if !seen[f.Symbol] {
			seen[f.Symbol] = true
			out = append(out, f)
		}
	}

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		errPkgs := errorsPkgNames(f.ast)
		for _, s := range bindingScopes(f) {
			scope := pkg + "." + s.name
			if _, ok := gitEnabledReaders[scope]; ok {
				found[scope] = true
			}

			// The one legal position for a sentinel outside storage: the
			// target of errors.Is(err, <sentinel>).
			mapped := map[ast.Node]bool{}
			ast.Inspect(s.body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Is" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && errPkgs[x.Name] {
					mapped[call.Args[1]] = true
				}
				return true
			})

			ast.Inspect(s.body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.CallExpr:
					if calleeName(v) != "HostGitEnabled" {
						return true
					}
					if _, ok := gitEnabledReaders[scope]; ok {
						return true
					}
					add(Finding{
						Kind:   KindGitEnabledOwner,
						Symbol: scope + " -> HostGitEnabled",
						Pos:    posOf(f, v.Pos()),
						Detail: fmt.Sprintf(
							"%s reads git_enabled itself. The refusal has ONE reader, storage.RefuseIfGitDisabled; "+
								"a second reader is how the CLI and MCP drift into two implementations that agree "+
								"only today (and, inside storage, how one operation reads the config twice). Call "+
								"storage.RefuseIfGitDisabled, or map its error with errors.Is. If this is a new "+
								"REPORTING reader that refuses nothing, add it to gitEnabledReaders with its reason.",
							scope),
					})
				case ast.Expr:
					if pkg == "storage" {
						return true
					}
					if mapped[v] {
						// Do not descend: the selector's own Sel ident would
						// otherwise be seen as a bare reference.
						return false
					}
					var name string
					switch e := v.(type) {
					case *ast.SelectorExpr:
						name = e.Sel.Name
					case *ast.Ident:
						name = e.Name
					default:
						return true
					}
					if !gitEnabledSentinels[name] {
						return true
					}
					add(Finding{
						Kind:   KindGitEnabledOwner,
						Symbol: scope + " -> " + name,
						Pos:    posOf(f, v.Pos()),
						Detail: fmt.Sprintf(
							"%s uses storage.%s outside errors.Is. Outside internal/storage the sentinel may only "+
								"be MAPPED (errors.Is(err, storage.%s)); constructing, wrapping or returning it forges "+
								"the refusal, which then passes every parity assertion while being a second "+
								"implementation. Return the error storage.RefuseIfGitDisabled gave you.",
							scope, name, name),
					})
					return false
				}
				return true
			})
		}
	}

	// The anchor check: every allow-listed reader must still exist, or the
	// allow-list is exempting a name nothing carries and a rename leaves the
	// rule guarding less than it says. Same idiom as plannerNoWrite.
	var absent []string
	for name := range gitEnabledReaders {
		if !found[name] {
			absent = append(absent, name)
		}
	}
	sort.Strings(absent)
	for _, name := range absent {
		add(Finding{
			Kind:   KindGitEnabledOwner,
			Symbol: "sourceaudit.gitEnabledOwner/ABSENT/" + name,
			Pos:    "internal/sourceaudit/git_enabled_owner.go",
			Detail: fmt.Sprintf(
				"gitEnabledReaders allow-lists %q as a reader of git_enabled, and NO function by that name "+
					"exists in the audited tree. Either it was renamed or moved (update gitEnabledReaders in the "+
					"same commit) or it was deleted (remove the entry). Do NOT baseline this entry — a "+
					"baselined anchor is a disabled rule.",
				name),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// calleeName is the bare name a call invokes: f(...) or x.f(...).
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}
