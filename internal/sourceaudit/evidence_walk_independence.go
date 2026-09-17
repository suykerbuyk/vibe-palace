// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"sort"
)

// An EVIDENCE REPORTER is the `vp` command a vaultaudit dimension publishes as its
// Evidence string, for a reader to run instead of trusting the report. It must
// enumerate the corpus INDEPENDENTLY of the dimension it corroborates.
//
// # Why a rule, and why no test could replace it
//
// `vp audit task-files` and DimTaskFileValidity deliberately share one predicate,
// storage.ValidateWholeTaskFile — one definition, two callers, so the report and the
// write-time refusal cannot drift apart. Everything the differential test in cmd/vp
// can actually prove therefore reduces to one thing: that the two ENUMERATIONS
// agree. The command walks Projects/*/tasks/{done,cancelled} with os.ReadDir; the
// dimension goes through vault.ListAllProjects and TaskDoneDir/TaskCancelledDir.
//
// 🔴 AND THAT IS PRECISELY WHY THE TEST CANNOT GUARD ITSELF. If someone
// "simplifies" the command to call ListAllProjects, the two sides become one
// implementation quoting itself — and the differential does not go red. It goes
// GREEN, more reliably than before, because the only thing it compared is now
// shared. A differential that passes because both sides share an enumeration is
// worse than no differential: it reports confidence it has not earned, and nothing
// downstream can tell the difference.
//
// Output comparison is structurally incapable of catching this. A structural check
// is the only thing that can, and it belongs here rather than in a test for the
// reason this package already records for the planner rule: a guard only an agent
// can trip is not a guard. A finding is reached by `make test`, `make source-audit`
// and the CI job alike.
//
// # What it is honestly worth
//
// Like every rule in this package it is AST-only with no type resolution, and
// sourceaudit.go's uninvokedFuncs comment states the consequence: "a name called
// anywhere counts as called everywhere". A same-named method on an unrelated type
// satisfies it, and a call reached INDIRECTLY through a helper this rule does not
// name is invisible to it. It catches the direct substitution — which is the one a
// simplifying refactor actually makes. It is not a proof.

// evidenceReporterFuncs names the functions that must walk the corpus themselves.
// Keyed by bare name, like every other rule here: there is no type resolution to
// disambiguate with.
var evidenceReporterFuncs = map[string]bool{
	"runTaskFileValidityReport": true,
}

// sharedEnumerationCalls names the calls that would make a reporter inherit the
// dimension's enumeration instead of performing its own.
//
// ListAllProjects is the whole point: it is the enumerator every vaultaudit
// dimension resolves projects through, so a reporter reaching it has stopped being
// a second opinion. Run is included because calling the audit outright is the same
// substitution taken one step further.
var sharedEnumerationCalls = map[string]string{
	"ListAllProjects": "storage.Vault.ListAllProjects (the enumerator the audit dimensions use)",
	"Run":             "vaultaudit.Run (the audit itself)",
}

// evidenceWalkIndependence reports a shared-enumeration call reachable directly
// from an evidence reporter.
func evidenceWalkIndependence(files []file) []Finding {
	var out []Finding
	seen := map[string]bool{}
	found := map[string]bool{}

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		for _, s := range bindingScopes(f) {
			if !evidenceReporterFuncs[s.name] {
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
				what, bad := sharedEnumerationCalls[name]
				if !bad {
					return true
				}
				sym := pkg + "." + s.name + " -> " + name
				if seen[sym] {
					return true
				}
				seen[sym] = true
				out = append(out, Finding{
					Kind:   KindSharedEnumeration,
					Symbol: sym,
					Pos:    posOf(f, call.Pos()),
					Detail: fmt.Sprintf(
						"%s is an EVIDENCE REPORTER: it is published as a vaultaudit dimension's Evidence "+
							"command so a reader can corroborate the audit, and it must enumerate the corpus "+
							"ITSELF. It calls %s. Both sides already share the predicate deliberately, so the "+
							"enumeration is the only thing the differential test compares — and sharing it does "+
							"not make that test fail, it makes it PASS while proving nothing. Walk the "+
							"directories directly instead.",
						s.name, what),
				})
				return true
			})
		}
	}

	// 🔴 THE ANCHOR CHECK, on the same reasoning planner_no_write.go records for its
	// own. evidenceReporterFuncs is keyed by bare name, so renaming the reporter
	// leaves this rule matching zero bodies: no compile error, no finding, and a
	// silently disabled guard. This rule's healthy steady state is ZERO findings, so a
	// count floor would be red forever; checking that the anchors still EXIST is the
	// analogue for a rule that is silent when healthy. Re-derive the live anchors:
	//
	//	grep -rn "func runTaskFileValidityReport" --include='*.go' cmd/ internal/
	var absent []string
	for name := range evidenceReporterFuncs {
		if !found[name] {
			absent = append(absent, name)
		}
	}
	sort.Strings(absent)
	for _, name := range absent {
		out = append(out, Finding{
			Kind:   KindSharedEnumeration,
			Symbol: "sourceaudit.evidenceWalkIndependence/ABSENT/" + name,
			Pos:    "internal/sourceaudit/evidence_walk_independence.go",
			Detail: fmt.Sprintf(
				"evidenceReporterFuncs declares %q as an evidence reporter that must walk the corpus "+
					"itself, and NO function by that name exists in the audited tree. The rule is guarding "+
					"nothing for that name, silently. Either it was renamed or moved (update "+
					"evidenceReporterFuncs in the same commit) or it was deleted (remove the entry). Do NOT "+
					"baseline this entry — a baselined anchor is a disabled rule.", name),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}
