// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"sort"
	"strconv"
	"strings"
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
//  1. Any reference to HostGitEnabled — a call, or the function taken as a
//     value (`var f = storage.HostGitEnabled`, `f := storage.HostGitEnabled`)
//     — from anywhere outside gitEnabledReaders. Inside internal/storage the
//     allow-list is exactly RefuseIfGitDisabled, so an inner function
//     re-reading the config is a finding too: that is the read-once-per-entry-
//     call property. The other two entries are REPORTING readers that refuse
//     nothing.
//  2. Any reference to ErrGitDisabled or ErrGitConfigUnreadable in a package
//     other than storage that is not the second argument of errors.Is.
//     Mapping the refusal is allowed everywhere; constructing, wrapping,
//     aliasing or returning the sentinel outside storage is not.
//
// Both are checked in every function body AND in every package-level var or
// const initializer: `var errOff = fmt.Errorf("…%w", storage.ErrGitDisabled)`
// forges the refusal without any function body at all. Names are resolved by
// package identity, not spelling: outside storage a reference counts only as a
// selector on the file's import of the storage package (under any alias, or a
// dot import), so a package's own unrelated ErrGitDisabled is not mistaken for
// storage's.
//
// # What it cannot see, stated
//
// It is AST-only and name-based, like every rule in this package. A hand-rolled
// os.ReadFile plus a TOML decode of git_enabled outside storage is invisible to
// it; deleting Config.GitEnabled removed the easy path to that. A surface that
// forges its own refusal WITHOUT the sentinel is caught by the parity test's
// errors.Is assertion instead. The two together cover the forbidden shape;
// neither does alone. The owner package is matched by NAME ("storage", and an
// import path ending in "/storage"), so a fixture or a stray package of that
// name is treated as the owner. Reading the config twice INSIDE an allow-listed
// reader, or from a storage core that calls a gated entry point, is not pinned
// here (recorded as a known unpinned property in the task's Code review).
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
		storageNames, dotStorage := storagePkgNames(f.ast)
		// storageRef names the storage identifier an expression refers to:
		// a selector on a storage import, or a bare identifier inside package
		// storage itself (or under a dot import of it). "" for anything else.
		storageRef := func(e ast.Expr) string {
			switch v := e.(type) {
			case *ast.SelectorExpr:
				if x, ok := v.X.(*ast.Ident); ok && storageNames[x.Name] {
					return v.Sel.Name
				}
			case *ast.Ident:
				if pkg == "storage" || dotStorage {
					return v.Name
				}
			}
			return ""
		}
		for _, s := range gitOwnerScopes(f) {
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
				v, ok := n.(ast.Expr)
				if !ok {
					return true
				}
				if mapped[v] {
					// Do not descend: the selector's own Sel ident would
					// otherwise be seen as a bare reference.
					return false
				}
				name := storageRef(v)
				switch {
				case name == "HostGitEnabled":
					if _, ok := gitEnabledReaders[scope]; ok {
						return false
					}
					add(Finding{
						Kind:   KindGitEnabledOwner,
						Symbol: scope + " -> HostGitEnabled",
						Pos:    posOf(f, v.Pos()),
						Detail: fmt.Sprintf(
							"%s reads git_enabled itself (a call, or HostGitEnabled taken as a value). The refusal "+
								"has ONE reader, storage.RefuseIfGitDisabled; a second reader is how the CLI and MCP "+
								"drift into two implementations that agree only today (and, inside storage, how one "+
								"operation reads the config twice). Call storage.RefuseIfGitDisabled, or map its error "+
								"with errors.Is. If this is a new REPORTING reader that refuses nothing, add it to "+
								"gitEnabledReaders with its reason.",
							scope),
					})
					return false
				case gitEnabledSentinels[name] && pkg != "storage":
					add(Finding{
						Kind:   KindGitEnabledOwner,
						Symbol: scope + " -> " + name,
						Pos:    posOf(f, v.Pos()),
						Detail: fmt.Sprintf(
							"%s uses storage.%s outside errors.Is. Outside internal/storage the sentinel may only "+
								"be MAPPED (errors.Is(err, storage.%s)); constructing, wrapping, aliasing or returning "+
								"it forges the refusal, which then passes every parity assertion while being a second "+
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

// gitOwnerScope is one region of a file the rule inspects, keyed by the name a
// reader greps for.
type gitOwnerScope struct {
	name string
	body ast.Node
}

// gitOwnerScopes is bindingScopes (function bodies and package-level func
// literals) PLUS every package-level var or const initializer, keyed by the
// var's name. A func literal inside an initializer is seen twice under the
// same name; add() dedupes by Symbol, so it is reported once.
func gitOwnerScopes(f file) []gitOwnerScope {
	var out []gitOwnerScope
	for _, s := range bindingScopes(f) {
		out = append(out, gitOwnerScope{name: s.name, body: s.body})
	}
	for _, decl := range f.ast.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) == 0 {
				continue
			}
			for i, val := range vs.Values {
				name := vs.Names[0].Name
				if i < len(vs.Names) {
					name = vs.Names[i].Name
				}
				out = append(out, gitOwnerScope{name: name, body: val})
			}
		}
	}
	return out
}

// storagePkgNames returns the names under which f imports the storage package
// (an import path whose last element is "storage"), and whether any of those
// imports is a dot import.
func storagePkgNames(f *ast.File) (map[string]bool, bool) {
	names := map[string]bool{}
	dot := false
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || (path != "storage" && !strings.HasSuffix(path, "/storage")) {
			continue
		}
		switch {
		case imp.Name == nil:
			names["storage"] = true
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	return names, dot
}
