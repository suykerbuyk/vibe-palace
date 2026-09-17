// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"sort"
)

// A PLANNER is a function that derives a migration's decisions and writes
// nothing, so that the values it derives cannot be contaminated by the writes
// its own run performs.
//
// # Why this rule exists, and why a signature was not enough
//
// `vp migrate task-board-fields` derives each task file's ModTime from
// `git log -1` — the most recent commit touching that path. The shipped version
// interleaved derivation with writes, so once any part of the run was committed,
// the derivation read the migration's OWN commit and stamped every archived file
// with the migration date. The fix is a planner that completes before the
// executor starts.
//
// 🔴 THE FIRST ATTEMPT AT ENFORCING THIS WAS WRONG, AND THE WRONGNESS IS THE
// POINT. The plan claimed that giving the planner a `root string` rather than a
// `*storage.Vault` left "no writer in scope" and made writing "structurally
// impossible" — that a break test adding a write "would not compile". Both are
// false: `storage.NewVault(root)` is exported and takes a root string, and
// `os.WriteFile` needs nothing but a path. A parameter type is a convention.
//
// Compiler-level enforcement is not reachable here: the planner must run `git
// log` against a real path, so it cannot be handed only an `fs.FS`, and Go has
// no per-symbol import restriction that would let a package boundary forbid
// `os.WriteFile`. This ratchet is therefore the STRONGEST AVAILABLE mechanism,
// not a fallback from a better one that was declined.
//
// # What it is honestly worth
//
// Like every rule in this package it is AST-only with no type resolution, and
// sourceaudit.go's own uninvokedFuncs comment states the consequence: "a name
// called anywhere counts as called everywhere". So a same-named function in
// another package satisfies a guard, and a write reached INDIRECTLY — through a
// helper this rule does not name — is invisible to it. It catches the direct,
// obvious reintroduction, which is the one a refactor actually makes. It is not
// a proof, and no comment here should be read as claiming one.
//
// The behavioural half is cmd/vp's TestBoardFieldsPlannerWritesNothing, which
// hashes the whole vault across a planning call.

// plannerFuncs names the functions that must derive without writing. Keyed by
// bare name for the same reason every other rule in this package is: there is no
// type resolution to disambiguate with.
var plannerFuncs = map[string]bool{
	"planBoardFieldsMigration": true,
	"predictFormatStamp":       true,
}

// plannerWriteCalls names the calls that write, or that hand back something that
// writes. A planner reaching any of them is the finding.
//
// `NewVault` is included deliberately even though it writes nothing itself: it
// is the documented way to obtain a writer from a bare root, so a planner that
// constructs one has already left the read-only discipline this rule exists to
// keep, and naming it catches the reintroduction one step earlier than waiting
// for the write call itself.
var plannerWriteCalls = map[string]string{
	"Write":                            "atomicfile.Write / os.WriteFile",
	"WriteStream":                      "atomicfile.WriteStream",
	"WriteFile":                        "os.WriteFile",
	"NewVault":                         "storage.NewVault (obtains a writer from a root)",
	"SetTaskMigrationFields":           "storage.Vault.SetTaskMigrationFields",
	"ApplyTaskMigrationFields":         "storage.Vault.ApplyTaskMigrationFields",
	"OverwriteTaskFile":                "storage.Vault.OverwriteTaskFile",
	"OverwriteTaskFileRewritingHeader": "storage.Vault.OverwriteTaskFileRewritingHeader",
	"WriteFormat":                      "surface.WriteFormat",
	"Rename":                           "os.Rename",
	"Remove":                           "os.Remove",
	"RemoveAll":                        "os.RemoveAll",
}

// plannerNoWrite reports a write call reachable directly from a planner.
func plannerNoWrite(files []file) []Finding {
	var out []Finding
	seen := map[string]bool{}

	// Which guarded names this walk actually FOUND. See the absent-anchor block
	// at the end of this function for what a miss means and why it is a finding.
	found := map[string]bool{}

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		for _, s := range bindingScopes(f) {
			if !plannerFuncs[s.name] {
				continue
			}
			found[s.name] = true
			ast.Inspect(s.body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				default:
					return true
				}
				what, bad := plannerWriteCalls[name]
				if !bad {
					return true
				}
				sym := pkg + "." + s.name + " -> " + name
				if seen[sym] {
					return true
				}
				seen[sym] = true
				out = append(out, Finding{
					Kind:   KindPlannerWrite,
					Symbol: sym,
					Pos:    posOf(f, call.Pos()),
					Detail: fmt.Sprintf(
						"%s is a PLANNER: it derives values a later executor writes, and it must write nothing. "+
							"It calls %s. A planner that writes can have its own derivations read its own "+
							"output — which is how this migration stamped every archived file with the migration "+
							"date instead of the file's real ModTime. Move the write into the executor.",
						s.name, what),
				})
				return true
			})
		}
	}
	// 🔴 THE ANCHOR CHECK. Every name in plannerFuncs must still BE a function in
	// the audited tree, or this rule is guarding nothing while reporting nothing.
	//
	// plannerFuncs is keyed by bare name, so renaming planBoardFieldsMigration in
	// cmd/vp leaves this rule matching zero bodies. There is no compile error —
	// the map is just a set of strings — and no finding, because a rule that
	// matches nothing emits nothing. The gate stays green, and so does every
	// fixture-based test in planner_no_write_test.go, because those fixtures
	// carry the names this map declares rather than the names cmd/vp uses. A
	// rename is an ordinary refactor; this is the failure mode that costs
	// nothing to cause and is invisible once caused.
	//
	// # Why this is a FINDING and not a test assertion
	//
	// A guard only an agent can trip is not a guard. A test-only anchor check is
	// reachable by whoever runs `go test`; a finding is reached by `make test`,
	// `make source-audit` and the source-audit CI job alike — the layer both
	// surfaces call. This is the same substitution vaultWriteFunnel made for its
	// floors (vault_write_funnel.go:207) and for the same stated reason.
	//
	// # Why this is an ABSENCE check and not a count floor
	//
	// This rule is the one syntactic rule in the package with no /VACUOUS floor,
	// and that is deliberate rather than an omission. A floor asserts a rule keeps
	// FINDING things; every sibling's healthy steady state is a non-zero
	// population it can count (writers, git subprocesses, test files). This rule's
	// healthy steady state is ZERO findings — the planner is supposed not to
	// write — so a floor on matches would be red forever and would be deleted
	// within a week. The analogue for a rule that is silent when healthy is to
	// check that its ANCHORS still exist, which is what this does. Do not "fix"
	// the missing floor.
	//
	// # What it is honestly worth
	//
	// The same bare-name weakness the doc comment above admits applies here in
	// the satisfying direction: a same-named function in any other package marks
	// the name found. So this catches a rename or a deletion, not a rename that
	// happens to collide with an unrelated symbol. Re-derive the live anchors:
	//
	//	grep -rn "func planBoardFieldsMigration\|func predictFormatStamp" --include='*.go' cmd/ internal/
	//
	// A tiny fixture tree legitimately trips this, exactly as every sibling's
	// /VACUOUS sentinel does; the tests filter it by symbol rather than by count.
	var absent []string
	for name := range plannerFuncs {
		if !found[name] {
			absent = append(absent, name)
		}
	}
	sort.Strings(absent)
	for _, name := range absent {
		out = append(out, Finding{
			Kind:   KindPlannerWrite,
			Symbol: "sourceaudit.plannerNoWrite/ABSENT/" + name,
			Pos:    "internal/sourceaudit/planner_no_write.go",
			Detail: fmt.Sprintf(
				"plannerFuncs declares %q as a planner that must not write, and NO function or "+
					"package-level func-literal var by that name exists in the audited tree. The rule is "+
					"therefore guarding nothing for that name, silently: it matches no body, so it emits no "+
					"write finding no matter what the real planner does. Either the function was renamed or "+
					"moved (update plannerFuncs in the same commit) or it was deleted (remove the entry). "+
					"Do NOT baseline this entry — a baselined anchor is a disabled rule.",
				name),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}
