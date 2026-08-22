// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"go/ast"
	"path/filepath"
	"strings"
)

// vaultWriteSink is the single seed of the reachability walk: every stamped
// vault write funnels through surface.StampForPath. It has exactly four
// non-test callers — internal/atomicfile (the whole-file replace primitive),
// internal/storage, internal/reconcile and internal/archive — which is what
// makes "reaches a vault write" a reachability question with ONE root rather
// than a list of write helpers somebody has to keep current.
//
// Anchoring here rather than on a textual marker (`VaultPath`, `storage.Write*`)
// is deliberate and was measured: nine cmd/vp files reference VaultPath and at
// least five are read-only or mixed — cmd_plans.go and cmd_status.go are
// documented in-tree as non-writing — so a marker rule reports majority noise on
// its first run and gets baselined into uselessness.
var vaultMutationSinks = []string{
	// Content writes. Every stamped write funnels through here, so one seed
	// covers atomicfile, storage, reconcile and archive at once.
	"surface.StampForPath",

	// Content REMOVAL and RENAME. These mutate the vault and deliberately do
	// NOT stamp — internal/vaultfs/write.go:210-213 ("Delete uses os.Remove
	// directly: it removes content rather than writing new content, so it does
	// not route through atomicfile") and :268-271 (the same for Move via
	// os.Rename). They are therefore invisible to the stamp anchor BY
	// CONSTRUCTION, which is how `vp vault delete` and `vp vault move` went
	// undetected in the first sweep.
	//
	// Anchored on the NAMED vaultfs functions, never on bare os.Remove /
	// os.Rename: those appear throughout the tree on temp files, locks and
	// project-local paths, and would over-report into instant baselining.
	"vaultfs.Delete",
	"vaultfs.Move",

	// The vault's FORMAT stamp. internal/surface/format.go is explicit that
	// WriteFormat "does NOT go through WriteStamp / StampForPath: a half-
	// migrated vault must still read as format 0" — so the one write that
	// records a data-format migration is invisible to the stamp anchor.
	"surface.WriteFormat",

	// The palace drawer store mutates in place rather than through atomicfile:
	// DeleteDrawer rewrites the JSONL with os.OpenFile(O_RDWR) and AppendDrawer
	// appends. Neither stamps. `vp audit rooms --apply` reaches both through
	// Vault.MoveDrawer, which is the reason its registration is wrapped.
	"storage.AppendDrawer",
	"storage.DeleteDrawer",
}

// registerFunc is the registry method whose argument is a command constructor,
// and mutatesWrapper is the marker that puts that command behind the surface
// compatibility gate (cmd/vp/commands.go).
const (
	registerFunc   = "Register"
	mutatesWrapper = "mutates"
)

// ungatedVaultWriters reports a command registered WITHOUT mutates() whose call
// graph reaches a stamped vault write.
//
// # RULING (2026-08-21): KEPT, with a narrowed job — it is NOT superseded
//
// ADR-010 said this rule would be "superseded rather than deleted" by the
// derived predicate, and warned against leaving it green and pointless beside
// its replacement. The derivation landed (derived_gate.go), and the honest
// verdict is that it does NOT supersede this rule, for one measurable reason:
// THE TWO ANCHOR SETS DO NOT COVER THE SAME MUTATIONS.
//
// The derivation anchors on the five funnel primitives. Two vault mutations
// reach none of them and are visible ONLY here:
//
//   - surface.WriteFormat — performs its own temp-write-and-rename because
//     internal/surface cannot import internal/atomicfile (atomicfile imports
//     surface; the cycle is real and no placement list closes it). It is
//     EXEMPT-A from the funnel by construction, so a command whose only vault
//     write is a format stamp derives non-mutating.
//   - storage.DeleteDrawer — rewrites its JSONL in place through
//     os.OpenFile(O_RDWR), not through any funnel primitive.
//
// Delete this rule and those two stop being checked by anything. Its remaining
// job is therefore precisely: BE THE ANCHOR FOR THE VAULT MUTATIONS THE FUNNEL
// STRUCTURALLY CANNOT CONTAIN. Two of its anchors — storage.AppendDrawer and
// the vaultfs.Delete/Move policy layer — are now genuinely redundant with F1
// and F2 and are kept only because narrowing the set would change what this
// rule reports, which is a behaviour change this unit was not permitted to
// make.
//
// A second, weaker reason to keep it: it is syntactic, so it survives a
// toolchain the SSA builder cannot handle. That is not hypothetical — x/tools
// v0.43.0 panics on Go 1.27's stdlib unless the build is scoped (see
// buildScoped). When the derivation cannot run, this rule still can.
//
// # Why this is not a table
//
// cmd/vp/main_test.go's TestMutatingCommandsAreGated pins a hand-maintained map
// of command names. It fails in both directions for commands somebody already
// thought about, which is worth keeping — but a NEW writing command registered
// unwrapped and simply absent from the map yields want==false and
// MutatesVault==false, compares equal, and PASSES. It is a regression pin, not a
// ratchet. This rule derives the answer from the source, so it cannot go green by
// omission.
//
// # Resolution model, and what it cannot see
//
// Analysis is syntactic and biased to FALSE NEGATIVES, matching
// internal/sourceaudit's posture and the sibling checks in internal/tools
// (TestNoHandWrittenVaultFilesInTools) and internal/storage
// (TestVaultWritesFromStoragePassTheRealVaultRoot, TestLockReentry). A noisy gate
// is a disabled gate; this is a tripwire for the known shape, not a proof of
// absence. Three call forms resolve, and everything else is dropped rather than
// guessed:
//
//   - a bare identifier — same-package call;
//   - pkg.F(...) where pkg is an import of THIS file — package-qualified, and
//     unambiguous without type information because the AST distinguishes an
//     imported package name from a local variable;
//   - recv.M(...) where recv's package is recoverable syntactically: either it
//     was assigned from a constructor in the SAME body
//     (exec := templates.NewExecutor(); exec.Write(...)), or its type is DECLARED
//     — a method receiver, a parameter, a result, or a `var x pkg.T` local.
//     Parameters matter most: `func runTasksEdit(vault *storage.Vault, ...)` is
//     the dominant idiom in this tree, and without the declared-type arm
//     `vp tasks edit` was invisible.
//
// That third form is on the CRITICAL PATH, not a peripheral nicety: the chain
// this rule exists to trace is
//
//	main.cmdCommandsUpgrade -> main.runCommandsUpgrade -> main.applyAndReport
//	  -> commands.Apply -> commands.applyWithPolicy
//	  -> templates.Executor.Write   <-- METHOD CALL, receiver type not recoverable
//	  -> atomicfile.Write -> surface.StampForPath
//
// and without local constructor inference it breaks at exactly that hop and the
// rule never fires on the defect it was written for. It resolves structurally
// here, so no edge is hand-modelled — but the reach is one function body wide,
// and a future writer reached through a struct field will be MISSED.
//
// Methods are keyed by package and name with the receiver type discarded, so two
// same-named methods on different types within one package merge. That widens
// within a package only, never across one.
//
// # What it does NOT see — measured by sweep, not by sampling
//
// The honest scope was established by unwrapping mutates() on EVERY command in
// cmd/vp/commands.go one at a time and recording whether the rule fired, not by
// sampling a few. Sampling three positives is what let the first version ship
// while 15 of 27 gated commands were invisible. Two classes remain:
//
//   - INTERFACE DISPATCH. cmd/vp/cmd_config.go calls `r.Apply(ctx, filtered)`
//     where r ranges over []reconcile.Reconciler. The receiver has no
//     syntactically recoverable package, so the walk stops. `vp config sync` and
//     `vp config upgrade` are therefore NOT detected, which is worth stating
//     plainly: the command at the centre of the template-mirror investigation is
//     one this rule cannot see. Only a type-checked call graph fixes it.
//   - GIT-LEVEL VAULT MUTATION. `vault commit`, `vault tidy` and `vault sync`
//     reach storage.CommitAndPushPaths / TidyVault / SyncVault, which stage and
//     commit bytes already on disk rather than authoring content. They are
//     deliberately NOT in the anchor set, on the same transport-not-authorship
//     reasoning that keeps `vault pull` / `vault push` unwrapped (see the
//     registration comment in cmd/vp/commands.go). The codebase does wrap them,
//     so this is a boundary disagreement to settle, not an oversight to patch.
//
// A pure container command (`vp migrate`, Subcommands and no Run) writes nothing
// and is correctly NOT flagged even though its registration is wrapped.
//
// The anchor set itself is HAND-MAINTAINED, and that is the rule's structural
// weakness rather than an implementation gap: a new non-stamping vault mutation
// primitive is invisible until someone adds it here. It is a floor, not a census.
//
// # Rejected alternative, recorded so it is not rediscovered
//
// A type-checked call graph (golang.org/x/tools/go/packages, optionally SSA)
// would eliminate both the method-receiver and the interface-dispatch holes
// outright. It is NOT in go.mod as a direct dependency today. The fact that makes
// it viable if this rule ever proves too lossy — and which is not obvious —
// is that `go list -deps ./cmd/vp` does not include internal/sourceaudit: this
// package is a TEST-TIME gate and is not linked into the shipped binary, so
// x/tools would touch neither the single-binary nor the zero-CGO property. It was
// declined here for minimal impact and machinery reuse, not because it would cost
// the binary anything.
func ungatedVaultWriters(files []file) []Finding {
	callees := buildCallGraph(files)

	// Reverse reachability to the sink, to a fixpoint.
	writers := map[string]bool{}
	for _, sink := range vaultMutationSinks {
		writers[sink] = true
	}
	for changed := true; changed; {
		changed = false
		for key, cs := range callees {
			if writers[key] {
				continue
			}
			for c := range cs {
				if writers[c] {
					writers[key] = true
					changed = true
					break
				}
			}
		}
	}

	var out []Finding
	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		ast.Inspect(f.ast, func(n ast.Node) bool {
			ctor, wrapped, ok := registeredCommand(n)
			if !ok {
				return true
			}
			key := pkg + "." + ctor
			if wrapped || !writers[key] {
				return true
			}
			out = append(out, Finding{
				Kind:   KindUngatedVaultWriter,
				Symbol: key,
				Pos:    posOf(f, n.Pos()),
				Detail: "registered without mutates() but its call graph reaches a vault mutation primitive" +
					", so surfaceGate takes the warn-only path and this command can write a vault " +
					"written by a newer binary. Wrap the registration in mutates(), or — if the write " +
					"is transport rather than authorship, as vault pull/push are — say so in a comment " +
					"at the registration site and add it to the exemption list.",
			})
			return true
		})
	}
	return out
}

// registeredCommand recognises `reg.Register(cmdX())` and
// `reg.Register(mutates(cmdX()))`, returning the constructor name and whether
// the mutates() wrapper is present.
func registeredCommand(n ast.Node) (ctor string, wrapped bool, ok bool) {
	call, isCall := n.(*ast.CallExpr)
	if !isCall {
		return "", false, false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel || sel.Sel.Name != registerFunc || len(call.Args) != 1 {
		return "", false, false
	}
	inner, isCall := call.Args[0].(*ast.CallExpr)
	if !isCall {
		return "", false, false
	}
	if id, isIdent := inner.Fun.(*ast.Ident); isIdent && id.Name == mutatesWrapper {
		if len(inner.Args) != 1 {
			return "", false, false
		}
		inner, isCall = inner.Args[0].(*ast.CallExpr)
		if !isCall {
			return "", false, false
		}
		wrapped = true
	}
	id, isIdent := inner.Fun.(*ast.Ident)
	if !isIdent {
		return "", false, false
	}
	return id.Name, wrapped, true
}

// fileImports maps each import's LOCAL name in this file (its alias, or the
// final path segment) to the in-tree package name it refers to. An out-of-tree
// import resolves to its own final segment, which never matches an in-tree
// declaration — the right answer, and the same translation importGraph performs.
func fileImports(f file, pkgByDirBase map[string]string) map[string]string {
	out := map[string]string{}
	for _, spec := range f.ast.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		base := filepath.Base(path)
		pkgName := base
		if p, ok := pkgByDirBase[base]; ok {
			pkgName = p
		}
		local := base
		if spec.Name != nil {
			local = spec.Name.Name
		}
		if local == "_" || local == "." {
			continue
		}
		out[local] = pkgName
	}
	return out
}

// fieldLists returns the receiver, parameter and result field lists of fd,
// skipping any that are absent.
func fieldLists(fd *ast.FuncDecl) []*ast.FieldList {
	var out []*ast.FieldList
	if fd.Recv != nil {
		out = append(out, fd.Recv)
	}
	if fd.Type != nil {
		if fd.Type.Params != nil {
			out = append(out, fd.Type.Params)
		}
		if fd.Type.Results != nil {
			out = append(out, fd.Type.Results)
		}
	}
	return out
}

// pkgOfTypeExpr recovers the PACKAGE of a declared type without type checking:
// pkg.T, *pkg.T, []pkg.T, []*pkg.T, map[K]pkg.T and one level of nesting past
// those. A type in the current package (a bare identifier) yields no package —
// deliberately, since a bare name cannot be attributed without type information
// and guessing is the false-positive direction.
func pkgOfTypeExpr(e ast.Expr, imported map[string]string, selfPkg string) (string, bool) {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			if p, ok := imported[x.Name]; ok {
				return p, true
			}
		}
	case *ast.StarExpr:
		return pkgOfTypeExpr(t.X, imported, selfPkg)
	case *ast.ArrayType:
		return pkgOfTypeExpr(t.Elt, imported, selfPkg)
	case *ast.MapType:
		return pkgOfTypeExpr(t.Value, imported, selfPkg)
	case *ast.Ellipsis:
		return pkgOfTypeExpr(t.Elt, imported, selfPkg)
	case *ast.Ident:
		// A BARE type name is local to this package — which is exactly the shape
		// of a method receiver written inside its own package: `func (v *Vault)
		// MoveDrawer(...)` is `*Vault`, never `*storage.Vault`. Without this arm
		// every same-package method call off a receiver was dropped, and
		// `vp audit rooms` (whose whole gating rationale is Vault.MoveDrawer)
		// stayed invisible. A builtin like `string` also lands here and yields a
		// key no declaration matches, which is harmless.
		return selfPkg, true
	}
	return "", false
}

func buildCallGraph(files []file) map[string]map[string]bool {
	// NON-TEST files only. An external test package (package commands_test in
	// internal/commands/) would otherwise overwrite the directory's real package
	// name, and every import of that directory would then resolve to a package
	// nothing declares — silently severing the call graph at that hop. This cost
	// the rule its own target chain once; it was caught by tracing a path that
	// should have existed and did not.
	pkgByDirBase := map[string]string{}
	for _, f := range files {
		if f.isTest {
			continue
		}
		pkgByDirBase[filepath.Base(filepath.Dir(f.path))] = f.ast.Name.Name
	}

	// callees[pkg.Func] = set of pkg.Func it calls.
	callees := map[string]map[string]bool{}

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		imported := fileImports(f, pkgByDirBase)

		for _, d := range f.ast.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Body == nil {
				continue
			}
			key := pkg + "." + fd.Name.Name
			if callees[key] == nil {
				callees[key] = map[string]bool{}
			}
			// Pass 1: learn receiver packages from same-body constructor
			// assignments. Separate from pass 2 so a use that textually precedes
			// its assignment still resolves.
			ctorPkg := map[string]string{}
			// Receivers whose package is recoverable from a DECLARED type:
			// the method receiver, every parameter, and every result. This is
			// what reaches `func runTasksEdit(vault *storage.Vault, …)` →
			// `vault.OverwriteTaskFile(…)`, the dominant idiom in this tree and
			// the mechanism that made `vp tasks edit` invisible to the first
			// version of this rule.
			for _, fl := range fieldLists(fd) {
				for _, fld := range fl.List {
					p, ok := pkgOfTypeExpr(fld.Type, imported, pkg)
					if !ok {
						continue
					}
					for _, nm := range fld.Names {
						ctorPkg[nm.Name] = p
					}
				}
			}
			// Locals with an explicit type: `var v *storage.Vault`.
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok || vs.Type == nil {
					return true
				}
				if p, ok := pkgOfTypeExpr(vs.Type, imported, pkg); ok {
					for _, nm := range vs.Names {
						ctorPkg[nm.Name] = p
					}
				}
				return true
			})
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for i, lhs := range as.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || i >= len(as.Rhs) {
						continue
					}
					call, ok := as.Rhs[i].(*ast.CallExpr)
					if !ok {
						continue
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					x, ok := sel.X.(*ast.Ident)
					if !ok {
						continue
					}
					if p, ok := imported[x.Name]; ok {
						ctorPkg[id.Name] = p
					}
				}
				return true
			})

			// Pass 2: record resolvable calls. ast.Inspect descends into function
			// literals, so a command's `Run: func(...)` closure is attributed to
			// the constructor that builds it — which is why no separate rooting
			// mechanism for closures is needed.
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					callees[key][pkg+"."+fn.Name] = true
				case *ast.SelectorExpr:
					x, ok := fn.X.(*ast.Ident)
					if !ok {
						return true
					}
					if p, ok := imported[x.Name]; ok {
						callees[key][p+"."+fn.Sel.Name] = true
					} else if p, ok := ctorPkg[x.Name]; ok {
						callees[key][p+"."+fn.Sel.Name] = true
					}
				}
				return true
			})
		}
	}

	return callees
}
