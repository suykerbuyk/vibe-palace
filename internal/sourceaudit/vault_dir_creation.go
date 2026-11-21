// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// vaultDirCreation reports a raw directory creator (os.Mkdir, os.MkdirAll,
// os.MkdirTemp, through ANY import alias of os) in a package that can write into
// a vault root, unless the call is BOUND to the under-test tripwire or sits in a
// closed, reasoned, per-call-site allowlist.
//
// # Why this rule exists
//
// task `archive-and-absorb-mkdirs-run-before-the-tripwire`: a test aimed at the
// operator's configured vault was refused at the lock and the write funnels, but
// directories were still created in it first by raw MkdirAll calls scattered
// across the tree. The seats close the known ones; this rule keeps the set
// CLOSED, so a new raw creation into a vault is reported instead of discovered.
//
// # What "bound" means
//
// A creator is accepted only if a `surface.GuardTestPathWrite(E)` statement
//
//   - precedes it in the same function, as a direct statement of a block that
//     encloses the creator (a guard in a sibling branch does not count), where
//   - E is the SAME expression as the creator's path argument (identical printed
//     AST, built only from identifiers, selectors, index expressions and the pure
//     path helpers, which are RESOLVED, not recognised by spelling: see pureCtx), and
//   - no identifier appearing in E is assigned, incremented, redeclared or has its
//     address taken between the guard and the creator.
//
// A first-statement guard on a DIFFERENT path does NOT qualify: that is the
// applyScaffold shape (a guard on the reconciler's root, a sink on a plan
// target), which the review showed can pass the rule without guarding the path
// written.
//
// # The allowlist is per CALL SITE
//
// Each entry is `package.Scope|callee(arg)` with an occurrence count and a
// reason, so a second raw call inside an allowlisted function is reported, and
// an entry whose call has gone is reported as stale. Its baseline is EMPTY: a
// baseline entry here would be a recorded permission to create directories in a
// vault.
//
// # Honest limits
//
// Syntactic and name-based, like every rule here. A directory made through a
// function value, through `exec` (git init, git clone, git worktree add), or by a
// file write that creates its parent (atomicfile does, behind its own guard) is
// not seen. The rule scans the packages in vaultDirScanPkgs and cmd/vp; a vault
// writer added in a package outside that list is invisible until it is added.
const vaultDirCreationFloor = 28

// vaultDirScanPkgs are the internal/<name> packages the rule scans.
var vaultDirScanPkgs = map[string]bool{
	"archive": true, "absorb": true, "reconcile": true, "migrate": true, "vaultaudit": true,
	"surface": true, "storage": true, "vaultfs": true, "atomicfile": true, "vaultlock": true,
	"indexstore": true, "search": true, "templates": true, "onboard": true, "tools": true,
	// the rest of the old creating-project-owner rawSinkPackages (consolidated into
	// this rule): hook holds checkout and host-state writes, the others hold no
	// directory creator today but must stay covered.
	"memory": true, "hook": true, "commands": true, "palace": true,
}

// pureCtx resolves the pure path helpers a guard or sink expression may call,
// with go/types, not with a hand-written list of binding forms.
//
// A helper is pure only if the identifier's OBJECT is the right declaration:
//
//   - filepath.Join, Dir and Clean: the selector's package identifier must resolve
//     to a *types.PkgName importing "path/filepath";
//   - lockDirPath: the identifier must resolve to a *types.Func named lockDirPath
//     that is a PACKAGE-LEVEL function of package vaultlock. A named result, a
//     parameter, a receiver, a var, const or := (in any nested block), a range
//     variable, a type-switch binding, a function literal's parameter or result,
//     or a label of the same name resolves to a different object and is not pure.
//
// This is the class fix: the earlier version enumerated the binding forms it knew
// (parameters, then local declarations) and was beaten by the next one (a named
// result), because identifier-to-declaration resolution is exactly what a type
// checker already does. The check tolerates unresolved imports (a fake importer
// hands back an empty package per import path), because the rule must run on a
// synthetic fixture tree with no go.mod, and it needs only what each identifier
// refers to, not the types of other packages' members. If the check cannot
// resolve an identifier, the helper is NOT pure and the site must be allowlisted:
// it fails closed.
type pureCtx struct {
	res identResolver
	f   file
}

var pureFilepath = map[string]bool{"Join": true, "Dir": true, "Clean": true}

func (c *pureCtx) pureCall(fset *token.FileSet, call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		id, ok := fn.X.(*ast.Ident)
		if !ok || !pureFilepath[fn.Sel.Name] {
			return false
		}
		pn, ok := c.res.lookup(c.f, id).(*types.PkgName)
		return ok && pn.Imported().Path() == "path/filepath"
	case *ast.Ident:
		fo, ok := c.res.lookup(c.f, fn).(*types.Func)
		return ok && fo.Name() == "lockDirPath" && fo.Pkg() != nil && fo.Pkg().Name() == "vaultlock" &&
			fo.Parent() == fo.Pkg().Scope()
	}
	return false
}

var dirCreators = map[string]bool{"Mkdir": true, "MkdirAll": true, "MkdirTemp": true}

// inVaultDirScan decides by the file's directory: paths reach the rule relative
// to wherever the audit runs ("../storage/x.go" from this package's tests,
// "internal/storage/x.go" in fixture trees), so the directory's own name, plus
// "cmd" in the path for cmd/vp, is what is stable.
func inVaultDirScan(f file) bool {
	dir := filepath.ToSlash(filepath.Dir(f.path))
	base := filepath.Base(dir)
	if base == "vp" && strings.Contains(dir, "cmd") {
		return true
	}
	return vaultDirScanPkgs[base] && !strings.Contains(dir, "cmd")
}

func printExpr(fset *token.FileSet, e ast.Expr) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, fset, e)
	return strings.Join(strings.Fields(b.String()), " ")
}

// pureExpr reports whether e is built only from identifiers, selectors, index
// expressions, literals and the pure path helpers.
func pureExpr(fset *token.FileSet, e ast.Expr, c *pureCtx) bool {
	switch x := e.(type) {
	case *ast.Ident, *ast.BasicLit:
		return true
	case *ast.ParenExpr:
		return pureExpr(fset, x.X, c)
	case *ast.SelectorExpr:
		return pureExpr(fset, x.X, c)
	case *ast.IndexExpr:
		return pureExpr(fset, x.X, c) && pureExpr(fset, x.Index, c)
	case *ast.CallExpr:
		if !c.pureCall(fset, x) {
			return false
		}
		for _, a := range x.Args {
			if !pureExpr(fset, a, c) {
				return false
			}
		}
		return true
	}
	return false
}

// exprIdents collects every identifier name appearing in e.
func exprIdents(e ast.Expr) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			out[id.Name] = true
		}
		return true
	})
	return out
}

// rootIdent returns the leftmost identifier of an assignable expression.
func rootIdent(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return ""
		}
	}
}

// reassignedBetween reports whether any identifier in names is assigned,
// incremented, redeclared or has its address taken in body at a position strictly
// between from and to.
func reassignedBetween(body ast.Node, names map[string]bool, from, to token.Pos) bool {
	hit := false
	ast.Inspect(body, func(n ast.Node) bool {
		if hit || n == nil || n.Pos() <= from || n.Pos() >= to {
			return !hit
		}
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				if names[rootIdent(l)] {
					hit = true
				}
			}
		case *ast.IncDecStmt:
			if names[rootIdent(x.X)] {
				hit = true
			}
		case *ast.UnaryExpr:
			if x.Op == token.AND && names[rootIdent(x.X)] {
				hit = true
			}
		case *ast.RangeStmt:
			for _, kv := range []ast.Expr{x.Key, x.Value} {
				if kv != nil && names[rootIdent(kv)] {
					hit = true
				}
			}
		}
		return !hit
	})
	return hit
}

// isGuardStmt reports whether s is a call statement to GuardTestPathWrite
// (qualified by any alias of surface, or unqualified), and returns its argument.
func isGuardStmt(s ast.Stmt, surfaceNames map[string]bool, inSurface bool) (ast.Expr, bool) {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return nil, false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil, false
	}
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		if id, ok := fn.X.(*ast.Ident); ok && surfaceNames[id.Name] && fn.Sel.Name == "GuardTestPathWrite" {
			return call.Args[0], true
		}
	case *ast.Ident:
		if inSurface && fn.Name == "GuardTestPathWrite" {
			return call.Args[0], true
		}
	}
	return nil, false
}

// stmtLists returns the statement lists directly held by n.
func stmtList(n ast.Node) []ast.Stmt {
	switch x := n.(type) {
	case *ast.BlockStmt:
		return x.List
	case *ast.CaseClause:
		return x.Body
	case *ast.CommClause:
		return x.Body
	}
	return nil
}

type dirAllow struct {
	count  int
	reason string
}

type dirSite struct {
	key, pos string
}

func vaultDirCreation(files []file, tree *typedTree) []Finding {
	unbound := map[string][]dirSite{}
	var order []string
	total, bound := 0, 0
	seenScopes := map[string]bool{}

	res := tree.uses

	for _, f := range files {
		if f.isTest || !inVaultDirScan(f) {
			continue
		}
		pkg := f.ast.Name.Name
		osNames, surfaceNames := map[string]bool{}, map[string]bool{}
		for _, im := range f.ast.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			name := ""
			if im.Name != nil {
				name = im.Name.Name
			}
			switch {
			case p == "os":
				if name == "" {
					name = "os"
				}
				osNames[name] = true
			case p == "surface" || strings.HasSuffix(p, "/surface"):
				if name == "" {
					name = "surface"
				}
				surfaceNames[name] = true
			}
		}
		for _, sc := range gitOwnerScopes(f) {
			scopeKey := pkg + "." + sc.name
			pc := &pureCtx{res: res, f: f}
			// stack-tracking walk: for every creator, the enclosing statement lists.
			var stack []ast.Node
			ast.Inspect(sc.body, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok || !osNames[id.Name] || !dirCreators[sel.Sel.Name] || len(call.Args) == 0 {
					return true
				}
				total++
				arg := call.Args[0]
				argText := printExpr(f.fset, arg)
				if boundBy(f, stack, call, arg, argText, surfaceNames, pkg == "surface", sc.body, pc) {
					bound++
					return true
				}
				key := scopeKey + "|os." + sel.Sel.Name + "(" + argText + ")"
				if _, seen := unbound[key]; !seen {
					order = append(order, key)
				}
				unbound[key] = append(unbound[key], dirSite{key: key, pos: f.fset.Position(call.Pos()).String()})
				seenScopes[scopeKey] = true
				return true
			})
		}
	}

	var out []Finding
	for _, key := range order {
		sites := unbound[key]
		allowed := vaultDirAllow[key]
		switch {
		case allowed.count == 0:
			out = append(out, Finding{Kind: KindVaultDirCreation, Symbol: "unbound-dir-creator " + key, Pos: sites[0].pos,
				Detail: "a raw directory creator in a package that can write into a vault root, not bound to " +
					"surface.GuardTestPathWrite on the same expression (with no reassignment in between). A test aimed " +
					"at the operator's configured vault would create the directory before any seat fires. Route it " +
					"through (*Vault).EnsureVaultDir / storage.EnsureDir (both seated), or put " +
					"surface.GuardTestPathWrite(<the same expression>) immediately before it; allowlist it ONLY if it " +
					"cannot reach a vault, with the reason. Do NOT baseline this: a baseline entry here is a recorded " +
					"permission to create directories in a vault."})
		case len(sites) > allowed.count:
			out = append(out, Finding{Kind: KindVaultDirCreation, Symbol: "extra-dir-creator " + key, Pos: sites[allowed.count].pos,
				Detail: fmt.Sprintf("%d raw calls with this exact text in one function; the allowlist (%q) permits %d. "+
					"A second raw directory creation inside an allowlisted function is not covered by its reason.",
					len(sites), allowed.reason, allowed.count)})
		}
	}
	var stale []string
	for key, a := range vaultDirAllow {
		if len(unbound[key]) < a.count {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		out = append(out, Finding{Kind: KindVaultDirCreation, Symbol: "sourceaudit.vaultDirCreation/STALE/" + key,
			Pos:    "internal/sourceaudit/vault_dir_creation.go",
			Detail: "the allowlist names a call site that no longer exists (renamed, moved, bound, or deleted): update the allowlist in the same commit. Do NOT baseline this entry."})
	}
	if total < vaultDirCreationFloor || bound < 3 {
		out = append(out, Finding{Kind: KindVaultDirCreation, Symbol: "sourceaudit.vaultDirCreation/VACUOUS",
			Pos: "internal/sourceaudit/vault_dir_creation.go",
			Detail: fmt.Sprintf("saw %d directory-creation call site(s) and %d bound one(s), expected at least %d and 3: the walk is not "+
				"seeing the tree, so a passing verdict is vacuous. Do NOT baseline this entry: fix the walk.", total, bound, vaultDirCreationFloor)})
	}
	return out
}

// boundBy reports whether the creator call is bound to a same-expression guard
// that precedes it in a block enclosing it, with no reassignment in between.
func boundBy(f file, stack []ast.Node, call *ast.CallExpr, arg ast.Expr, argText string, surfaceNames map[string]bool, inSurface bool, body ast.Node, pc *pureCtx) bool {
	if !pureExpr(f.fset, arg, pc) {
		return false
	}
	names := exprIdents(arg)
	for i := len(stack) - 2; i >= 0; i-- {
		list := stmtList(stack[i])
		if list == nil || i+1 >= len(stack) {
			continue
		}
		child := stack[i+1]
		for _, s := range list {
			if s.Pos() >= child.Pos() {
				break // only statements before the one holding the creator
			}
			e, ok := isGuardStmt(s, surfaceNames, inSurface)
			if !ok || !pureExpr(f.fset, e, pc) || printExpr(f.fset, e) != argText {
				continue
			}
			if !reassignedBetween(body, names, s.End(), call.Pos()) {
				return true
			}
		}
	}
	return false
}

// vaultDirAllow is the CLOSED, per-call-site allowlist. Key:
// `package.Scope|callee(arg)`. count is the exact number of such calls in that
// function; reason says why the call cannot create a directory in a vault.
var vaultDirAllow = map[string]dirAllow{
	// --- project checkout or host state: never a vault root ---
	"main.maybeWriteLocalOverride|os.MkdirAll(filepath.Dir(overridePath))": {1, "<project dir>/.vibe-palace override file in the project checkout (cmd/vp init)"},
	"main.runDrainSummaries|os.MkdirAll(queueDir)":                         {1, "the project-local summarization queue under the project checkout"},
	"hook.WriteClaim|os.MkdirAll(filepath.Dir(path))":                      {1, "the claim sentinel lives in the checkout's .vibe-palace/ (or opts.ClaimDir), never in the vault (reason carried over from the creating-project-owner baseline this rule consolidated)"},
	"hook.writeSettings|os.MkdirAll(filepath.Dir(path))":                   {1, "the host's Claude settings.json directory, not a vault (reason carried over from the creating-project-owner baseline)"},
	"storage.InstallPostCommitHook|os.MkdirAll(filepath.Dir(cur.Path))":    {1, "the project repo's .git/hooks directory, not a vault"},
	"storage.rebindApplyAnchors|os.MkdirAll(filepath.Dir(a.To))":           {1, "anchor files in the project checkout being rebound"},
	"storage.WriteCwdProjectConfig|os.MkdirAll(dir)":                       {1, "the cwd project's .vibe-palace config directory in the checkout"},
	"storage.WriteGlobalConfig|os.MkdirAll(filepath.Dir(configPath))":      {1, "the host config directory (XDG), not a vault"},
	"storage.writeScoringConfigAt|os.MkdirAll(filepath.Dir(cfgPath))":      {1, "the host scoring config directory (XDG), not a vault"},
	"storage.newRemoteSnapshot|os.MkdirAll(parent)":                        {1, "os.UserCacheDir()/vibe-palace/snap: a host cache scratch directory"},
	"storage.newRemoteSnapshot|os.MkdirTemp(parent)":                       {1, "a fresh scratch directory under the host snapshot cache"},
	"storage.remoteSnapshot.checkoutFootprint|os.MkdirTemp(s.root)":        {1, "a scratch directory inside the host snapshot cache"},
	"storage.snapshotFormat|os.MkdirTemp(snap.root)":                       {1, "a scratch directory inside the host snapshot cache"},
	"storage.snapshotFormat|os.MkdirAll(filepath.Dir(p))":                  {1, "a directory inside the scratch directory made on the line above, in the host snapshot cache"},

	// --- reached only behind a seat that refuses an ambient vault first ---
	"storage.slugMove|os.MkdirAll(filepath.Dir(dst))":                               {1, "inside the vault, but reachable only under ApplyRename's vaultlock.AcquireHeld on the vault root, whose first-statement seat refuses an ambient root before this runs"},
	"vaultfs.Move|os.MkdirAll(filepath.Dir(dstAbs))":                                {1, "after vaultlock.Acquire(vaultPath, dstAbs) at the top of the same function: that boundary's seat refuses an ambient vault first (task append-entry-paths-touch-the-vault-before-the-tripwire-fires)"},
	"surface.WriteStamp|os.MkdirAll(stampDir)":                                      {1, "every caller is behind a seat: vault_init takes AcquireHeld first, the authored-only migration is commit Seat 6 (first statement), StampForPath runs after writeAtomic's guard"},
	"surface.WriteVaultManifest|os.MkdirAll(filepath.Join(root, vaultManifestDir))": {1, "every caller is behind a seat (vault_init after AcquireHeld; the authored-only migration's first-statement seat)"},
	"vaultlock.openLockFileNoGC|os.MkdirAll(lockDir)":                               {1, "containment proof: lockDir is filepath.Join(vaultRoot, \".vp-locks\"), the very value lockDirPath(vaultRoot) that acquireGCShared's first-statement seat already judged; vaultlock's TestFilesystemEffectsAreSeatedFirst proves on every run that this helper is reached only through openLockFile after that seat. Seating it would strand the .gc flock"},

	// --- derived host-local data inside the vault, deliberately unseated (decision D5) ---
	"storage.migrateLegacyEmbedCache|os.MkdirAll(filepath.Dir(target))": {1, "palace/.local embed-cache directories: derived, host-local data that iteration 539 deliberately left unseated (SweepEmbedCaches); a first-statement seat on SweepEmbedCaches is the open alternative (operator decision D5)"},
	"storage.migrateLegacyEmbedCache|os.Mkdir(target)":                  {1, "as above (D5): the embed-cache target directory"},
	"storage.mergeLegacyEmbedCache|os.Mkdir(target)":                    {1, "as above (D5): the embed-cache merge target directory"},
}
