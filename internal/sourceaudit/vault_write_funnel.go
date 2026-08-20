// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
)

// vaultWriteFunnel reports a vault mutation that does not go through the shared
// write primitive, and an atomicfile.Write whose vaultRoot argument defeats the
// stamp.
//
// # Why this replaces two package-local tests
//
// It supersedes three test functions that each walked ONE package with
// os.ReadDir("."):
//
//   - internal/storage TestVaultWritesFromStoragePassTheRealVaultRoot
//   - internal/tools   TestVaultWritesFromToolsPassTheRealVaultRoot   (the same rule, duplicated)
//   - internal/tools   TestNoHandWrittenVaultFilesInTools
//
// Their scope was the defect. A census on 2026-08-20 swept every non-test .go
// file in the tree and found 33 logical in-vault mutation paths across 17
// PACKAGES that bypass the funnel — including the template prune
// (internal/reconcile), the audit baseline (internal/vaultaudit) and the
// templates lock (internal/templates). None of those three packages was inside
// any pin's os.ReadDir("."), so none had ever been looked at. Two perfect rules
// over two packages lose to one floor-quality rule over twenty-nine.
//
// This rule runs from Run(), over repoRoots, so every package is in scope by
// construction and a NEW package is in scope the day it is created.
//
// # What it reports
//
//	(a) a raw os.* CONTENT mutation whose destination resolves to a vault path,
//	    bypassing atomicfile.Write / vaultfs.Delete / vaultfs.Move; and
//	(b) an atomicfile.Write whose first argument is not a recognisable vault root
//	    — passing "" or a substitute makes the write succeed while SKIPPING the
//	    surface stamp, which is how the vault loses its version floor.
//
// os.MkdirAll is deliberately NOT a content verb here, inheriting the deleted
// tools rule's reasoning: creating a vault DIRECTORY is legitimate and
// widespread (15 storage.EnsureDir callers), and flagging it would bury the file
// writes in noise. Directory creation is a Phase-2 concern for the F3 family;
// the census carries those rows.
//
// # Findings are keyed by FUNCTION, not by call site
//
// Finding.ID() excludes Pos deliberately, so a per-line key would be unstable
// under edits that only move code. One finding per enclosing function keeps the
// baseline diffable and maps one-to-one onto the census rows. A function with
// three bypasses is one entry; fixing one of the three correctly does NOT shrink
// the baseline, and that is the intended conservative direction.
//
// # Honest limits — read the CENSUS, not this rule, for the authoritative list
//
// Syntactic, standard library only, biased to FALSE NEGATIVES like every rule in
// this package. A destination laundered through a struct field is NOT tracked,
// and that is not a hypothetical gap: internal/reconcile writes to `a.Target`
// off a Plan action, so the template prune — one of the two coverage regressions
// the census exists to name — is invisible HERE while being fully recorded
// THERE. Likewise a bare `path string` parameter with no vault signal in its
// function (vaultaudit.Baseline.Save, vplog.Init) cannot be resolved without
// type information.
//
// So: this rule is a RATCHET AGAINST NEW BYPASSES OF THE KNOWN SHAPE. The
// census in the task
// `move-the-surface-gate-to-the-write-chokepoint` is the census. Do not treat a
// green verdict here as "the funnel is complete" — it is not, and Phase 2 is
// what completes it.
func vaultWriteFunnel(files []file) []Finding {
	byFunc := map[string]*funnelBreach{}
	var order []string

	stats := funnelStats{}

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Skip the funnel's own implementation: atomicfile IS the primitive,
			// and vaultfs.Delete/Move ARE the removal family. A primitive cannot
			// route through itself.
			if pkg == "atomicfile" {
				continue
			}

			known := vaultPathIdents(fn, &stats)
			key := pkg + "." + funcName(fn)

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				recv, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}

				switch {
				// (b) atomicfile.Write with a defeated vaultRoot.
				case recv.Name == "atomicfile" && sel.Sel.Name == "Write":
					stats.atomicfileWrites++
					if !isVaultRootExpr(call.Args[0]) {
						breachAdd(byFunc, &order, key, f, call, fmt.Sprintf(
							"atomicfile.Write's first argument is not a recognisable vault root (%s) — %s",
							exprSketch(call.Args[0]), funnelRootHazard))
					}

				// (a) raw os.* content mutation to a vault-derived destination.
				case recv.Name == "os" && rawContentVerbs[sel.Sel.Name]:
					if sel.Sel.Name == "OpenFile" && !opensForWrite(call) {
						return true
					}
					stats.rawVerbs++
					if isVaultPathExpr(call.Args[0], known) {
						breachAdd(byFunc, &order, key, f, call, fmt.Sprintf(
							"os.%s writes a VAULT path (%s) with a raw primitive — %s",
							sel.Sel.Name, exprSketch(call.Args[0]), funnelBypassHazard))
					}
				}
				return true
			})
		}
	}

	var out []Finding
	for _, key := range order {
		b := byFunc[key]
		sort.Strings(b.details)
		out = append(out, Finding{
			Kind:   KindVaultWriteOutsideFunnel,
			Symbol: key,
			Pos:    b.pos,
			Detail: strings.Join(b.details, " | "),
		})
	}

	// 296: a no-result reads as green. If this walk stops seeing the tree — a
	// moved root, a parser change, a refactor that renames the primitive — every
	// assertion above silently stops running and the gate still passes.
	//
	// The two deleted pins each carried a t.Fatalf vacuity guard. This rule has
	// no *testing.T, so vacuity is reported as a FINDING instead: it is not in
	// the baseline, so the gate fails on it exactly like new debt. That is the
	// same protection by a different door, and it travels with Run() to every
	// caller rather than living in one test.
	//
	// Floors are DERIVED, not remembered. Re-derive with:
	//
	//	grep -rn 'atomicfile\.Write(' --include='*.go' internal/ cmd/ \
	//	  | grep -v '_test.go' | grep -vE ':[0-9]+:\s*//' | wc -l
	//
	// If you legitimately delete writers below the floor this goes red, and that
	// is intended: change the floor deliberately, in the same commit. Never edit
	// a floor to make a red gate pass — that is the one move that converts the
	// guard back into the vacuous pass it exists to prevent.
	const (
		atomicfileWriteFloor = 20
		rawVerbFloor         = 40
		vaultIdentFloor      = 10
	)
	switch {
	case stats.atomicfileWrites < atomicfileWriteFloor:
		out = append(out, vacuous(fmt.Sprintf(
			"saw only %d atomicfile.Write call site(s), expected at least %d — the walk is not seeing "+
				"the tree's vault writers, so a passing verdict is vacuous",
			stats.atomicfileWrites, atomicfileWriteFloor)))
	case stats.rawVerbs < rawVerbFloor:
		out = append(out, vacuous(fmt.Sprintf(
			"inspected only %d raw os.* content-verb call site(s), expected at least %d — the raw-write "+
				"matcher is not firing, so a passing verdict is vacuous",
			stats.rawVerbs, rawVerbFloor)))
	case stats.vaultIdents < vaultIdentFloor:
		out = append(out, vacuous(fmt.Sprintf(
			"resolved only %d vault-derived identifier(s), expected at least %d — the derivation tracker "+
				"is no longer recognising vault paths, so a passing verdict is vacuous",
			stats.vaultIdents, vaultIdentFloor)))
	}

	return out
}

// funnelStats are the anti-vacuity counters. Each one proves a distinct half of
// the machinery is live: the walk sees writers, the matcher sees raw verbs, and
// the tracker resolves vault paths. Any one of them silently breaking would
// otherwise leave this rule green forever.
type funnelStats struct {
	atomicfileWrites int
	rawVerbs         int
	vaultIdents      int
}

// funnelBreach accumulates every breach in one function so the finding is keyed
// by function rather than by line.
type funnelBreach struct {
	pos     string
	details []string
}

func breachAdd(m map[string]*funnelBreach, order *[]string, key string, f file, call *ast.CallExpr, detail string) {
	b, ok := m[key]
	if !ok {
		b = &funnelBreach{pos: posOf(f, call.Pos())}
		m[key] = b
		*order = append(*order, key)
	}
	b.details = append(b.details, detail)
}

func vacuous(detail string) Finding {
	return Finding{
		Kind:   KindVaultWriteOutsideFunnel,
		Symbol: "sourceaudit.vaultWriteFunnel/VACUOUS",
		Pos:    "internal/sourceaudit/vault_write_funnel.go",
		Detail: detail + ". Do NOT baseline this entry — fix the walk.",
	}
}

// rawContentVerbs are the os primitives that change a file's CONTENT or its
// existence. os.MkdirAll and os.Mkdir are deliberately absent — see the doc
// comment on vaultWriteFunnel.
var rawContentVerbs = map[string]bool{
	"WriteFile": true,
	"Create":    true,
	"OpenFile":  true,
	"Remove":    true,
	"RemoveAll": true,
	"Rename":    true,
	"Truncate":  true,
}

const funnelRootHazard = "atomicfile.Write stamps the MCP surface version for vaultRoot/path ONLY when " +
	"vaultRoot is non-empty (internal/atomicfile/atomicfile.go:57-110) — pass \"\" or any substitute for a " +
	"VAULT destination and the write silently succeeds while skipping the stamp, which is how the vault " +
	"loses its version floor and a host starts looking older than it is. Pass the real .Root. A genuinely " +
	"non-vault destination may pass \"\" — internal/wrapstate writes project-root anchors that way, " +
	"correctly — and if that is the case here, baseline it WITH THAT REASON"

const funnelBypassHazard = "vault mutations must route through the shared primitive family: " +
	"atomicfile.Write for whole-file content, vaultfs.Delete/vaultfs.Move for removal and rename. " +
	"A raw primitive opts OUT of the vaultlock advisory-lock discipline (ADR-003) AND out of the surface " +
	"stamp, so concurrent writers lose each other's edits and the vault's version floor stops rising. " +
	"If this mutation genuinely cannot route through the family — a lock file, a probe, a stamp write that " +
	"would import-cycle — baseline it WITH THAT REASON. An unexplained entry is indistinguishable from an " +
	"oversight, and in six months nobody will know which it was"

// opensForWrite reports whether an os.OpenFile call actually opens for writing,
// by reading its FLAGS argument.
//
// This discriminator is load-bearing, and the reason is a live specimen. The
// drawer store opens `os.OpenFile(path, os.O_RDWR, 0644)`
// (internal/storage/drawers.go:164) and then only ever SCANS it —
// bufio.NewScanner(f) at :171; the actual write goes through atomicfile.Write at
// :197. Without this check the rule flags that read handle as a bypass, which is
// precisely the mistake the anchor set's own comment made and carried for
// months. A rule that reproduces the stale comment it was written to replace is
// worse than no rule.
//
// O_RDWR alone is therefore NOT a write: it permits one, but a handle that never
// writes is not a mutation, and "permits" is not something syntax can settle.
// O_CREATE, O_WRONLY, O_APPEND and O_TRUNC each mean the call intends to change
// the file, and every genuine append-writer in this tree carries at least one.
// A non-literal flags expression is treated as a write — unknown means look.
func opensForWrite(call *ast.CallExpr) bool {
	if len(call.Args) < 2 {
		return true
	}
	var literal bool
	var write bool
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch v := e.(type) {
		case *ast.BinaryExpr:
			walk(v.X)
			walk(v.Y)
		case *ast.SelectorExpr:
			if id, ok := v.X.(*ast.Ident); ok && id.Name == "os" && strings.HasPrefix(v.Sel.Name, "O_") {
				literal = true
				if writeOpenFlags[v.Sel.Name] {
					write = true
				}
			}
		}
	}
	walk(call.Args[1])
	if !literal {
		return true
	}
	return write
}

// writeOpenFlags are the os.OpenFile flags that mean the call intends to change
// the file. os.O_RDWR is deliberately absent — see opensForWrite.
var writeOpenFlags = map[string]bool{
	"O_CREATE": true,
	"O_WRONLY": true,
	"O_APPEND": true,
	"O_TRUNC":  true,
}

// isVaultRootExpr reports whether an expression is a recognisable vault root for
// atomicfile.Write's first argument.
//
// A `.Root` / `.VaultRoot` selector, or an identifier the cross-package idiom
// names as one. Anything else — a literal "", a joined path, a bare name with no
// vault signal — is either the hazard or too clever to verify syntactically, and
// both want a human rather than a silent pass.
func isVaultRootExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		return v.Sel.Name == "Root" || v.Sel.Name == "VaultRoot"
	case *ast.Ident:
		return vaultRootIdentNames[v.Name]
	}
	return false
}

// vaultRootIdentNames are the parameter names this tree uses for a vault root.
// Cross-package coverage lives or dies on this set: internal/archive,
// internal/reconcile, internal/surface and internal/check all receive the root
// as a plain parameter rather than through a *storage.Vault, so a rule that only
// understood `v.Root` would see almost nothing outside internal/storage.
var vaultRootIdentNames = map[string]bool{
	"vaultRoot": true,
	"vaultPath": true,
	"vaultDir":  true,
	"root":      true,
	"destRoot":  true,
}

// isVaultPathExpr reports whether an expression yields a path inside the vault.
//
// Recognised forms, in the order they actually appear in this tree:
//
//   - an identifier already resolved as vault-derived;
//   - a Vault path accessor — v.MemoryFile(p, rel), vault.TasksDir(name). Keyed
//     on the accessor NAME suffix, not merely the receiver: a Vault has many
//     methods returning tasks, stats and triples, and counting those as paths
//     would inflate the anti-vacuity counter with values that could never be a
//     write destination — an instrument lying about its own coverage;
//   - filepath.Join(<vault-rooted>, …), including nested joins;
//   - a suffix concatenation of a vault path — `dst+".bak"`, `logPath+".1"`,
//     `configPath+".tmp"`. This one matters more than it looks: the backup and
//     temp siblings are how three of the census's bypasses actually write
//     (internal/templates/executor.go:106, internal/reconcile/upgrade.go:80,84,
//     internal/vplog/vplog.go:52), and a rule blind to it would call them clean.
func isVaultPathExpr(e ast.Expr, known map[string]bool) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return known[v.Name]

	case *ast.BinaryExpr:
		// path + ".bak" / ".tmp" / ".1"
		return isVaultPathExpr(v.X, known) || isVaultPathExpr(v.Y, known)

	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			return false
		}
		if recv.Name == "vault" || recv.Name == "v" {
			n := sel.Sel.Name
			return strings.HasSuffix(n, "File") || strings.HasSuffix(n, "Dir") || strings.HasSuffix(n, "Path")
		}
		if recv.Name == "filepath" && (sel.Sel.Name == "Join" || sel.Sel.Name == "Clean") && len(v.Args) > 0 {
			return isVaultPathExpr(v.Args[0], known)
		}
		return false

	case *ast.SelectorExpr:
		return v.Sel.Name == "Root" || v.Sel.Name == "VaultRoot"
	}
	return false
}

// vaultPathIdents resolves, within ONE function body, which identifiers hold a
// vault-derived path.
//
// Scoped to a function rather than to a file because this rule is cross-package:
// a file-wide identifier set merges names across unrelated functions, and in a
// 1,200-line cmd/vp file that manufactures false positives — the one thing this
// package refuses to do.
//
// Iterated to a FIXED POINT rather than in one sweep, because a path is built in
// stages (dir := vault.TasksDir(p); sub := filepath.Join(dir, "done")) and a
// single source-order sweep would resolve those only when they happen to appear
// in dependency order. Order-dependent coverage is coverage that changes when
// someone moves a line.
//
// Seeded from the function's own parameters, which is what makes the
// cross-package idiom reachable: `func Create(opts CreateOptions)` gives nothing,
// but `func stampVaultWrite(vaultRoot, path string)` names its root in the
// signature, and most of this tree does the latter.
func vaultPathIdents(fn *ast.FuncDecl, stats *funnelStats) map[string]bool {
	known := map[string]bool{}

	if fn.Type.Params != nil {
		for _, p := range fn.Type.Params.List {
			for _, n := range p.Names {
				if vaultRootIdentNames[n.Name] {
					known[n.Name] = true
					stats.vaultIdents++
				}
			}
		}
	}

	for {
		added := 0
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range as.Rhs {
				if i >= len(as.Lhs) {
					break
				}
				lhs, ok := as.Lhs[i].(*ast.Ident)
				if !ok || lhs.Name == "_" || known[lhs.Name] {
					continue
				}
				if isVaultPathExpr(rhs, known) {
					known[lhs.Name] = true
					added++
					stats.vaultIdents++
				}
			}
			return true
		})
		if added == 0 {
			break
		}
	}
	return known
}

// funcName renders a FuncDecl's name, qualified by receiver type for methods so
// two same-named methods in one package stay distinct in the baseline.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return recvTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func recvTypeName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.StarExpr:
		return recvTypeName(v.X)
	case *ast.Ident:
		return v.Name
	case *ast.IndexExpr:
		return recvTypeName(v.X)
	}
	return "?"
}

// exprSketch renders an expression compactly for a finding's Detail, so a reader
// sees WHICH argument was rejected without opening the file.
func exprSketch(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.BasicLit:
		return v.Value
	case *ast.SelectorExpr:
		return exprSketch(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return exprSketch(v.Fun) + "(…)"
	case *ast.BinaryExpr:
		return exprSketch(v.X) + "+" + exprSketch(v.Y)
	}
	return "<expr>"
}
