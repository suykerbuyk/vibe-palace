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

// creatingProjectOwner pins who may create a project in a vault.
//
// # Why this rule exists
//
// Every vault write primitive refuses a write under Projects/<slug>/ or
// palace/<slug>/ unless Projects/<slug> is an initialised project
// (projectdir.RefuseUninitialisedAbs, task
// untracked-project-stamps-from-writers-that-never-commit): a write there
// would create a project, and its .surface stamp, that nobody commits. The
// writers that create a project on purpose pass a creating-project option to
// their primitive. A new caller of that option, or a new write through a raw
// sink the gate does not sit on, would reopen the hole without any behavioural
// test noticing, so this rule pins both.
//
// It reports, in non-test code:
//
//  1. Any reference to atomicfile.CreatingProject or vaultfs.CreatingProject
//     (a selector on the file's import of that package, under any alias, or
//     unqualified inside it or under a dot import of it) outside
//     creatingProjectOwners. This part has no
//     baseline: a new owner is added here, in review.
//  2. Every function that calls vaultfs.RenameNoLock, or os.MkdirAll or
//     os.Mkdir inside a package that writes vault project trees
//     (rawSinkPackages). Each allowed
//     caller is a baseline entry naming why it may write past the gate (a
//     creator, a rename inside a tree that already exists, or a path that is
//     not a project tree at all). A new caller therefore fails until someone
//     rules on it in writing.
//  3. Any call to EnsureDir inside internal/storage: storage makes a directory
//     under a project tree with (*Vault).EnsureVaultDir, which is gated.
//
// # What it cannot see, stated
//
// It is AST-only and name-based. It cannot tell whether an os.MkdirAll path
// lies in a vault, which is why part 2 is a reviewed baseline rather than a
// verdict, and why it is limited to the packages that write vault project
// trees: internal/indexstore (host-local palace/.local/ only) and the
// checkout-side writers are not pinned. Other raw os.* writes stay covered by
// the vault-write funnel audit (vaultWriteFunnel).
var creatingProjectOwners = map[string]bool{
	"reconcile.TemplateTreeReconciler.applyScaffold": true, // the init scaffold's first README
	"storage.CopyProjectTreeEntry":                   true, // a copy, split or merge destination
	"vaultfs.Create":                                 true, // forwards vaultfs.CreatingProject to atomicfile
}

// rawSinkPackages are the packages whose os.MkdirAll and os.Mkdir calls part 2
// pins.
var rawSinkPackages = map[string]bool{
	"storage": true, "vaultfs": true, "archive": true, "reconcile": true, "migrate": true,
	"absorb": true, "tools": true, "memory": true, "hook": true, "commands": true, "palace": true,
}

func creatingProjectOwner(files []file) []Finding {
	type hit struct{ pos, detail string }
	optHits := map[string]hit{}
	sinkHits := map[string]hit{}
	ensureHits := map[string]hit{}
	found := map[string]bool{}
	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		imports := map[string]string{} // local name -> owner package
		dot := map[string]bool{}       // owner packages dot-imported by this file
		for _, im := range f.ast.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			for _, owner := range []string{"vaultfs", "atomicfile"} {
				if p == owner || strings.HasSuffix(p, "/"+owner) {
					name := owner
					if im.Name != nil {
						name = im.Name.Name
					}
					if name == "." {
						dot[owner] = true
						continue
					}
					imports[name] = owner
				}
			}
			if p == "os" {
				name := "os"
				if im.Name != nil {
					name = im.Name.Name
				}
				imports[name] = "os"
			}
		}
		for _, s := range gitOwnerScopes(f) {
			scope := pkg + "." + s.name
			if creatingProjectOwners[scope] {
				found[scope] = true
			}
			ast.Inspect(s.body, func(n ast.Node) bool {
				pos := func() string { return f.fset.Position(n.Pos()).String() }
				switch x := n.(type) {
				case *ast.SelectorExpr:
					id, ok := x.X.(*ast.Ident)
					if !ok {
						return true
					}
					owner := imports[id.Name]
					switch {
					case (owner == "atomicfile" || owner == "vaultfs") && x.Sel.Name == "CreatingProject":
						if !creatingProjectOwners[scope] {
							optHits[scope] = hit{pos(), owner + ".CreatingProject"}
						}
					case owner == "vaultfs" && x.Sel.Name == "RenameNoLock":
						sinkHits[scope] = hit{pos(), "vaultfs.RenameNoLock"}
					case owner == "os" && (x.Sel.Name == "MkdirAll" || x.Sel.Name == "Mkdir") && rawSinkPackages[pkg]:
						sinkHits[scope] = hit{pos(), "os." + x.Sel.Name}
					}
				case *ast.CallExpr:
					id, ok := x.Fun.(*ast.Ident)
					if !ok {
						return true
					}
					switch {
					case (pkg == "atomicfile" || pkg == "vaultfs" || dot["atomicfile"] || dot["vaultfs"]) && id.Name == "CreatingProject":
						if !creatingProjectOwners[scope] && s.name != "CreatingProject" {
							optHits[scope] = hit{pos(), "CreatingProject"}
						}
					case (pkg == "vaultfs" || dot["vaultfs"]) && id.Name == "RenameNoLock" && s.name != "RenameNoLock":
						sinkHits[scope] = hit{pos(), "vaultfs.RenameNoLock"}
					case pkg == "storage" && id.Name == "EnsureDir":
						ensureHits[scope] = hit{pos(), "EnsureDir"}
					}
				}
				return true
			})
		}
	}
	var out []Finding
	emit := func(m map[string]hit, symbol func(string, hit) string, detail func(string, hit) string) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, Finding{Kind: KindCreatingProjectOwner, Symbol: symbol(k, m[k]), Pos: m[k].pos, Detail: detail(k, m[k])})
		}
	}
	emit(optHits, func(k string, h hit) string { return k + " -> " + h.detail }, func(k string, h hit) string {
		return fmt.Sprintf("%s passes %s, which admits a write into a project the vault has not initialised. "+
			"Only the init scaffold and a lifecycle copy's destination create projects; a writer that is "+
			"not one must not pass it (initialise the project first, or ask to add a new owner to "+
			"creatingProjectOwners in review).", k, h.detail)
	})
	emit(sinkHits, func(k string, h hit) string { return "raw-sink " + k }, func(k string, h hit) string {
		return fmt.Sprintf("%s calls %s, a raw sink the uninitialised-project gate does not sit on. Name this "+
			"caller in baseline.json WITH A REASON (a project creator, a rename inside a tree that already "+
			"exists, or a path outside the project trees), or write through a gated primitive "+
			"(atomicfile, vaultfs, (*Vault).EnsureVaultDir).", k, h.detail)
	})
	emit(ensureHits, func(k string, h hit) string { return "storage-ensuredir " + k }, func(k string, h hit) string {
		return fmt.Sprintf("%s calls EnsureDir inside internal/storage. A storage directory under a project "+
			"tree is made with (*Vault).EnsureVaultDir, which refuses a project the vault has not initialised; "+
			"EnsureDir would leave an empty Projects/<slug>/ behind a refused write.", k)
	})
	var absent []string
	for name := range creatingProjectOwners {
		if !found[name] {
			absent = append(absent, name)
		}
	}
	sort.Strings(absent)
	for _, name := range absent {
		out = append(out, Finding{
			Kind:   KindCreatingProjectOwner,
			Symbol: "sourceaudit.creatingProjectOwner/ABSENT/" + name,
			Pos:    "internal/sourceaudit/creating_project_owner.go",
			Detail: fmt.Sprintf("creatingProjectOwners allows %q and no function by that name exists in the audited "+
				"tree: it was renamed or moved (update this rule in the same commit) or deleted (remove the entry). "+
				"Do NOT baseline this entry.", name),
		})
	}
	return out
}
