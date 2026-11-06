// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"sort"
	"strconv"
)

// automaticRebuildBounded keeps every AUTOMATIC `vp index rebuild` spawn site
// transcript-free BY CONSTRUCTION.
//
// # Why this rule exists
//
// Two sites spawn `vp index rebuild` with no operator in the loop: the
// cold-search launcher (tools.NewRebuildLauncher) and the drain's
// rebuild-want consumer (main.spawnRebuildFor). Between them they are the hair
// trigger that re-spawned a days-long backlog build three minutes after it was
// killed — killing the build does not stop it, because any cold search starts
// another.
//
// Excluding the transcript tier by DEFAULT does not bound them: a host that
// opts in with `[index] transcripts = true` would have the whole backlog on
// that same trigger again. So each automatic site appends --no-transcripts
// unconditionally, which beats the config in ingest.resolveTranscripts. That
// is a property of a literal in two argv slices, which no test of the ingest
// decision can see, so it is pinned here.
//
// # What it checks
//
// In each allow-listed function, every []string composite literal whose first
// element is "index" and second is "rebuild" must also contain
// "--no-transcripts". The site is matched by function name, and each name must
// still EXIST in the tree: a renamed or deleted site is reported rather than
// silently unguarded.
//
// # What it cannot see, stated
//
// It is AST-only and literal-based, like every rule here. An argv assembled
// through a helper, from a slice variable, or with the flag in a constant, is
// invisible to it — as is a THIRD automatic spawn site added under a name this
// list does not carry. The anchor check below catches a rename of a known
// site; it cannot catch a new one. The end-to-end guard that the flag actually
// turns ingest off is internal/tools' launcher-argv test, not this rule.
var automaticRebuildSpawners = map[string]string{
	"tools.NewRebuildLauncher": "the cold-search detached-rebuild launcher (ADR-016 decision 4)",
	"main.spawnRebuildFor":     "the drain's rebuild-corpus-want consumer (ADR-016 decision 6, U4 B2)",
}

// transcriptFreeFlag is the literal each automatic argv must carry.
const transcriptFreeFlag = "--no-transcripts"

func automaticRebuildBounded(files []file) []Finding {
	var out []Finding
	found := map[string]bool{}

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		for _, s := range bindingScopes(f) {
			scope := pkg + "." + s.name
			if _, ok := automaticRebuildSpawners[scope]; !ok {
				continue
			}
			found[scope] = true
			ast.Inspect(s.body, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				elems, isRebuildArgv := rebuildArgvLiteral(cl)
				if !isRebuildArgv {
					return true
				}
				for _, e := range elems {
					if e == transcriptFreeFlag {
						return true
					}
				}
				out = append(out, Finding{
					Kind:   KindAutomaticRebuildUnbounded,
					Symbol: scope,
					Pos:    posOf(f, cl.Pos()),
					Detail: fmt.Sprintf(
						"%s spawns `vp index rebuild` with no %s in its argv. This is an AUTOMATIC trigger "+
							"with no operator in the loop, and an automatic trigger must be bounded BY "+
							"CONSTRUCTION, not by configuration: without the flag the spawned run resolves the "+
							"host's config, so a host with `[index] transcripts = true` gets the whole historical "+
							"backlog on a hair trigger that any cold search re-arms. Append %q to the argv. The "+
							"EXPLICIT sites (`vp index rebuild` itself, vp_refresh_index) are not in this list and "+
							"must not carry it, or opting in becomes unreachable.",
						scope, transcriptFreeFlag, transcriptFreeFlag),
				})
				return true
			})
		}
	}

	// The anchor check: an allow-listed site that no longer exists means the
	// rule is guarding a name nothing carries. Same idiom as gitEnabledOwner.
	var absent []string
	for name := range automaticRebuildSpawners {
		if !found[name] {
			absent = append(absent, name)
		}
	}
	sort.Strings(absent)
	for _, name := range absent {
		out = append(out, Finding{
			Kind:   KindAutomaticRebuildUnbounded,
			Symbol: "sourceaudit.automaticRebuildBounded/ABSENT/" + name,
			Pos:    "internal/sourceaudit/automatic_rebuild_bounded.go",
			Detail: fmt.Sprintf(
				"automaticRebuildSpawners names %q as an automatic `vp index rebuild` spawn site, and NO "+
					"function by that name exists in the audited tree. Either it was renamed or moved (update "+
					"automaticRebuildSpawners in the same commit) or it was deleted (remove the entry). Do NOT "+
					"baseline this entry — a baselined anchor is a disabled rule.",
				name),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// rebuildArgvLiteral reports the string elements of cl when it is a
// `[]string{"index", "rebuild", ...}` composite literal, and whether it is
// one. Non-string elements are returned as "" so their positions still count.
func rebuildArgvLiteral(cl *ast.CompositeLit) ([]string, bool) {
	at, ok := cl.Type.(*ast.ArrayType)
	if !ok || at.Len != nil {
		return nil, false
	}
	if id, ok := at.Elt.(*ast.Ident); !ok || id.Name != "string" {
		return nil, false
	}
	elems := make([]string, 0, len(cl.Elts))
	for _, e := range cl.Elts {
		bl, ok := e.(*ast.BasicLit)
		if !ok {
			elems = append(elems, "")
			continue
		}
		v, err := strconv.Unquote(bl.Value)
		if err != nil {
			elems = append(elems, "")
			continue
		}
		elems = append(elems, v)
	}
	if len(elems) < 2 || elems[0] != "index" || elems[1] != "rebuild" {
		return nil, false
	}
	return elems, true
}
