// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
)

// commitSeats are the functions that carry the ambient-commit guard. A `git
// commit` raised inside one of them is guarded by construction, so the rule
// exempts them BY NAME.
//
// Resolved by name only, the same conservative bias gitExecEnvGuards states: a
// same-named function elsewhere would wrongly satisfy the exemption, and that
// risk is smaller than a gate nobody trusts. Three panic (their enclosing
// vault-root lock is released by a defer) and three return
// storage.ErrAmbientVaultCommit (theirs is released by hand); the rule does not
// care which, only that the seat is there.
// 🔴 KEYED ON THE FULL BINDING SCOPE — package, receiver and name — not on the
// bare function name. A bare-name exemption lets ANY same-named method on ANY
// type in ANY package evade the ratchet, which defeats the closed-set property
// the rule exists for: a new unguarded committer would only have to be called
// GitCommitAllAt. The receiver half matters too, since bindingScopes spells a
// method "Vault.ApplyAuthoredOnlyMigration".
var commitSeats = map[string]bool{
	"storage.commitAndPushPathsCore":           true, // seat 1, panics
	"storage.commitPathspec":                   true, // seat 2, panics
	"storage.GitCommitAllAt":                   true, // seat 3, panics
	"storage.healDerivedConflicts":             true, // seat 4, error arm
	"storage.untrackDerivedAfterMerge":         true, // seat 5, error arm
	"storage.Vault.ApplyAuthoredOnlyMigration": true, // seat 6, error arm
}

// unguardedVaultCommitFloor is the anti-vacuity floor, the same shape
// gitExecEnvFunnel's and vaultWriteFunnel's. A no-result must mean "every commit
// is seated", never "the walk stopped seeing commits at all". Re-derive with:
//
//	grep -rn --include='*.go' --exclude-dir=.cache -E '"commit"' internal cmd \
//	  | grep -v '_test\.go' | wc -l
//
// (that counts every `"commit"` string literal, a superset of the git-argument
// position this rule matches, so the true floor is lower and this is kept
// deliberately conservative.)
const unguardedVaultCommitFloor = 5

// unguardedVaultCommit reports a non-test function that runs `git commit`
// without being one of the seated primitives in commitSeats.
//
// # Why this rule exists
//
// Three hand-run `vp init` invocations committed Projects/pkg, pkg2 and
// nestedpkg into the operator's live vault on 2026-11-03. A sibling guard
// refuses the CONTENT write; this ratchet is about the COMMIT. Six runtime seats
// now refuse a commit whose repository is the ambient configured vault root
// under test, and this rule keeps that set CLOSED: a new committing path is
// reported rather than discovered by the next incident.
//
// 🔴 A BASELINE ENTRY HERE IS A RECORDED PERMISSION TO COMMIT — fail-open with a
// finding id. The expected baseline for this rule is EMPTY. A finding means a
// real uncovered committer: seat it, do not budget it.
//
// # What it matches
//
// The literal "commit" in a git-argument position, in ANY non-test call — not
// only the two storage helpers. Scoping the walk to gitCmd/gitCmdStdin would
// miss internal/storage/git.go's own commit, which passes "commit" straight to
// exec.Command, and that is seat 3's commit: a rule blind to its own seat is
// worse than no rule. Three shapes are matched:
//
//   - exec.Command("git", ..., "commit", ...) and the CommandContext form;
//   - gitCmd / gitCmdEnv(dir, timeout, [env,] ..., "commit", ...);
//   - gitCmdStdin / gitCmdStdinEnv(dir, timeout, stdin, [env,] ..., "commit", ...).
//
// For the helper shapes the argument position is not fixed — a caller may pass
// leading global flags, and some build them at runtime (requiredFilterArgs emits
// a variable-length `-c filter.X.required=true` run) — so the rule looks for the
// literal anywhere in the argument list rather than at a computed index. That
// over-matches a path or message that happens to be the word "commit"; the
// by-name seat exemption and an empty baseline are what keep that tolerable, and
// over-matching fails CLOSED.
//
// # Findings are keyed by FUNCTION, not by call site
//
// Same reasoning as vaultWriteFunnel and gitExecEnvFunnel: a per-line key is
// unstable under code motion that changes no behaviour, and a function with two
// unseated commits is one entry, not two.
//
// # Honest limits — read this before trusting a green run
//
//   - Syntactic only, like every rule here. **A verb held in a variable is
//     invisible**, exactly as gitExecEnvFunnel documents for its own "git"
//     literal: `verb := "commit"; gitCmd(dir, t, verb)` is not reported. Two
//     call sites in this tree already run git from a string var
//     (internal/departure/residue.go, internal/project/removed_slug.go), so the
//     shape is not hypothetical — it is simply not what this rule can see.
//   - A commit raised through a function-valued seam the rule cannot follow is
//     likewise invisible.
//   - The exemption is by NAME, so a future function sharing a seat's name is
//     exempted without carrying a seat.
//
// The rule ratchets against the known shape. It does not certify that every
// commit in the tree is seated.
func unguardedVaultCommit(files []file) []Finding {
	byFunc := map[string]*funnelBreach{}
	var order []string
	total := 0

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		for _, s := range bindingScopes(f) {
			if commitSeats[pkg+"."+s.name] {
				// Seated: its commits are guarded by construction. Still walk it
				// so the floor counts them — otherwise seating every committer
				// would make the walk look blind.
				total += countGitCommitCalls(s.body)
				continue
			}
			key := pkg + "." + s.name
			ast.Inspect(s.body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isGitCommitCall(call) {
					return true
				}
				total++
				breachAdd(byFunc, &order, key, f, call, fmt.Sprintf(
					"runs `git commit` without the ambient-commit guard — a commit whose repository is the "+
						"operator's configured vault root would land under test. Seat it (surface.GuardTestVaultCommit "+
						"where the vault-root lock is released by a defer, storage.ErrAmbientVaultCommit where it is "+
						"released by hand) and add the function to commitSeats. Do NOT baseline this: a baseline entry "+
						"here is a recorded permission to commit."))
				return true
			})
		}
	}

	var out []Finding
	for _, key := range order {
		b := byFunc[key]
		sort.Strings(b.details)
		out = append(out, Finding{
			Kind:   KindUnguardedVaultCommit,
			Symbol: key,
			Pos:    b.pos,
			Detail: b.details[0],
		})
	}

	if total < unguardedVaultCommitFloor {
		out = append(out, Finding{
			Kind:   KindUnguardedVaultCommit,
			Symbol: "sourceaudit.unguardedVaultCommit/VACUOUS",
			Pos:    "internal/sourceaudit/unguarded_vault_commit.go",
			Detail: fmt.Sprintf(
				"saw only %d `git commit` call site(s), expected at least %d — the walk is not seeing the tree's "+
					"committers, so a passing verdict is vacuous. Do NOT baseline this entry — fix the walk.",
				total, unguardedVaultCommitFloor),
		})
	}
	return out
}

// countGitCommitCalls counts the git-commit calls in body, for the floor.
func countGitCommitCalls(body *ast.BlockStmt) int {
	n := 0
	ast.Inspect(body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && isGitCommitCall(call) {
			n++
		}
		return true
	})
	return n
}

// gitCommitRunners are the storage helpers that take (dir, timeout, ...args) and
// exec git with them. Matched by the Sel half, so a package-qualified call works
// too.
var gitCommitRunners = map[string]bool{
	"gitCmd":         true,
	"gitCmdEnv":      true,
	"gitCmdStdin":    true,
	"gitCmdStdinEnv": true,
}

// isGitCommitCall reports whether call runs `git commit`: either an
// exec.Command/CommandContext whose binary literal is "git", or one of
// gitCommitRunners — in both cases with the literal "commit" somewhere in the
// argument list.
//
// The literal is looked for anywhere rather than at a fixed index because the
// verb's position is not fixed: callers pass leading git global flags, and some
// build a variable-length run of them at runtime.
func isGitCommitCall(call *ast.CallExpr) bool {
	if isGitExecCall(call) {
		return hasStringLit(call.Args, "commit")
	}
	var name string
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		name = fun.Name
	case *ast.SelectorExpr:
		name = fun.Sel.Name
	default:
		return false
	}
	if !gitCommitRunners[name] {
		return false
	}
	return hasStringLit(call.Args, "commit")
}

// hasStringLit reports whether args contains the string literal want. It looks
// one level into a variadic `append(...)` spread, which is how the runtime-built
// flag prefixes reach these helpers.
func hasStringLit(args []ast.Expr, want string) bool {
	quoted := `"` + want + `"`
	for _, a := range args {
		if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == quoted {
			return true
		}
		if inner, ok := a.(*ast.CallExpr); ok {
			if id, ok := inner.Fun.(*ast.Ident); ok && id.Name == "append" {
				if hasStringLit(inner.Args, want) {
					return true
				}
			}
		}
		if comp, ok := a.(*ast.CompositeLit); ok {
			if hasStringLit(comp.Elts, want) {
				return true
			}
		}
	}
	return false
}
