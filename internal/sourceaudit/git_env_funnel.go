// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"
)

// gitExecEnvGuards are the names — bare identifier, or the Sel half of a
// package-qualified selector — that strip repo-local git variables from a
// subprocess's environment (internal/storage/git.go's SafeGitEnv, and the
// withoutRepoLocalGitEnv it wraps). Resolved by NAME ONLY, matching this
// package's stated bias toward false negatives over false positives: a
// same-named function elsewhere would wrongly satisfy the guard, but that
// risk is smaller than a noisy gate nobody trusts.
var gitExecEnvGuards = map[string]bool{
	"SafeGitEnv":             true,
	"withoutRepoLocalGitEnv": true,
}

// gitExecEnvFunnel reports a `git` subprocess whose environment is not built
// through SafeGitEnv. Without that, an inherited GIT_DIR / GIT_WORK_TREE /
// GIT_INDEX_FILE overrides cmd.Dir and the subprocess silently operates on
// whichever repository the environment names instead of the directory the
// caller intended — measured directly with the plain git binary: `git -C
// <vault> add -- README.md` with GIT_WORK_TREE=<decoy> stages the DECOY's
// working copy of README.md, not the vault's, and a subsequent commit lands
// there with exit 0 — no error at all. See
// vault-git-runners-inherit-git-dir-from-the-environment for the finding this
// ratchets against regressing.
//
// # What it reports
//
// Every call shaped exec.Command("git", ...) or exec.CommandContext(ctx,
// "git", ...) in non-test code, across the WHOLE tree — not scoped to any one
// package, because "is this a vault git runner" cannot be decided
// syntactically, and a rule that tried would need constant re-scoping as new
// packages spawn git. A call assigned to a variable (`cmd := exec.Command(
// "git", ...)`) is guarded if the SAME function later assigns `cmd.Env = ...`
// from an expression that calls a name in gitExecEnvGuards, anywhere in that
// expression's subtree (so `append(SafeGitEnv(...), "X=1")` counts, not just
// a bare `SafeGitEnv(...)`). A call chained directly onto a method
// (`exec.Command("git", ...).Output()`, never bound to a variable) can never
// have Env attached to it at all and is reported unconditionally.
//
// # Findings are keyed by FUNCTION, not by call site
//
// Same reasoning as vaultWriteFunnel: a per-line key is unstable under code
// motion that does not change behavior, and a function with two unguarded git
// calls is one entry, not two.
//
// # Honest limits — read this before trusting a green run
//
//   - Syntactic and standard-library only, like every rule in this package. A
//     git *exec.Cmd built in one function and handed to another for its Env to
//     be set is invisible here — the tracked shape is "build it and guard it
//     in the same function body," which is every call site this task's own
//     fix touches.
//   - Resolution is a FuncDecl walk. A git subprocess built inside a func
//     literal assigned to a package-level var (a test seam, e.g.
//     internal/worktree.runGit or internal/wrapstate.gitCmdRunner) is NOT
//     visible to this rule — extending into that shape is the same
//     bindingScopes/outermostFuncLits mechanism surface_remediation.go already
//     has, and is a reasonable follow-up rather than something this rule was
//     asked to do.
//   - A git subprocess that targets a repository OTHER than a vault — the
//     project's own repo, an archive's source cwd — carries the identical
//     mechanical risk (an inherited GIT_DIR would misdirect it too) but is a
//     SEPARATE, not-yet-filed bug class from the one this rule's originating
//     task fixes. Those sites are recorded in baseline.json with a reason
//     naming that, rather than exempted here by path — so a genuinely NEW
//     ungated git call anywhere, vault or not, still fails the gate and gets a
//     human decision. That is this rule's actual job: ratchet against the
//     known shape, not certify that every git call in the tree is safe.
func gitExecEnvFunnel(files []file) []Finding {
	byFunc := map[string]*funnelBreach{}
	var order []string
	total := 0

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
			key := pkg + "." + funcName(fn)

			// varsFromGitExec: variable name -> the exec.Command/CommandContext
			// call it was assigned from, scoped to this function only.
			varsFromGitExec := map[string]*ast.CallExpr{}

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
					return true
				}
				id, ok := assign.Lhs[0].(*ast.Ident)
				if !ok {
					return true
				}
				call, ok := assign.Rhs[0].(*ast.CallExpr)
				if !ok || !isGitExecCall(call) {
					return true
				}
				total++
				varsFromGitExec[id.Name] = call
				return true
			})

			// Direct chains: exec.Command("git", ...).Something(...), never
			// bound to a variable, so its Env can never be set at all.
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				inner, ok := sel.X.(*ast.CallExpr)
				if !ok || !isGitExecCall(inner) {
					return true
				}
				total++
				breachAdd(byFunc, &order, key, f, inner,
					"git subprocess chained directly onto a method call, so its Env can never be set — bind it to a variable and set Env from SafeGitEnv")
				return true
			})

			for varName, call := range varsFromGitExec {
				if gitExecEnvGuarded(fn.Body, varName) {
					continue
				}
				breachAdd(byFunc, &order, key, f, call, fmt.Sprintf(
					"git subprocess assigned to %q never sets .Env from SafeGitEnv — an inherited GIT_DIR/GIT_WORK_TREE would override cmd.Dir",
					varName))
			}
		}
	}

	var out []Finding
	for _, key := range order {
		b := byFunc[key]
		sort.Strings(b.details)
		out = append(out, Finding{
			Kind:   KindGitExecUnsafeEnv,
			Symbol: key,
			Pos:    b.pos,
			Detail: strings.Join(b.details, " | "),
		})
	}

	// Anti-vacuity floor, same shape as vaultWriteFunnel's: a no-result must
	// mean "every git call is guarded," never "the walk stopped seeing git
	// calls at all." Re-derive with:
	//
	//	rg -c 'exec\.Command(Context)?\(' --glob '!*_test.go' internal cmd \
	//	  | rg ':' | awk -F: '{s+=$2} END {print s}'
	//
	// (that counts every exec.Command(Context) call, a superset of the "git"
	// literal this rule matches; the true floor is lower and was 20 when this
	// rule was written — kept deliberately conservative here.)
	const gitExecFloor = 15
	if total < gitExecFloor {
		out = append(out, Finding{
			Kind:   KindGitExecUnsafeEnv,
			Symbol: "sourceaudit.gitExecEnvFunnel/VACUOUS",
			Pos:    "internal/sourceaudit/git_env_funnel.go",
			Detail: fmt.Sprintf(
				"saw only %d exec.Command(\"git\", ...)/exec.CommandContext(ctx, \"git\", ...) call site(s), expected at least %d — the walk is not seeing the tree's git runners, so a passing verdict is vacuous. Do NOT baseline this entry — fix the walk.",
				total, gitExecFloor),
		})
	}

	return out
}

// isGitExecCall reports whether call is exec.Command("git", ...) or
// exec.CommandContext(ctx, "git", ...) — the "git" argument a string literal,
// resolved syntactically (no type information, so a variable holding "git"
// is invisible here, the same conservative bias documented on the rule).
func isGitExecCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "exec" {
		return false
	}
	var gitArgIdx int
	switch sel.Sel.Name {
	case "Command":
		gitArgIdx = 0
	case "CommandContext":
		gitArgIdx = 1
	default:
		return false
	}
	if len(call.Args) <= gitArgIdx {
		return false
	}
	lit, ok := call.Args[gitArgIdx].(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && lit.Value == `"git"`
}

// gitExecEnvGuarded reports whether body assigns `<varName>.Env = ...`
// anywhere, from an expression whose subtree calls a name in
// gitExecEnvGuards.
func gitExecEnvGuarded(body *ast.BlockStmt, varName string) bool {
	guarded := false
	ast.Inspect(body, func(n ast.Node) bool {
		if guarded {
			return false
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Env" {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != varName {
			return true
		}
		ast.Inspect(assign.Rhs[0], func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if gitExecEnvGuards[fn.Name] {
					guarded = true
				}
			case *ast.SelectorExpr:
				if gitExecEnvGuards[fn.Sel.Name] {
					guarded = true
				}
			}
			return true
		})
		return true
	})
	return guarded
}
