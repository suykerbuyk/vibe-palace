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

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		for _, s := range bindingScopes(f) {
			if !plannerFuncs[s.name] {
				continue
			}
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
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}
