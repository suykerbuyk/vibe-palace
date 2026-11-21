// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
)

// The self-checks that keep the typed taint honest. Every finding here is a
// meta-finding of the funnel rule (Kind vault-write-outside-funnel, Symbol under
// `sourceaudit.`) and is NEVER baselinable: each says "fix the table or the
// helper", and a baseline entry would be a recorded permission to stop looking.

func metaFinding(symbol, detail string) Finding {
	return Finding{Kind: KindVaultWriteOutsideFunnel, Symbol: symbol,
		Pos: "internal/sourceaudit/vault_taint.go", Detail: detail + ". Do NOT baseline this entry."}
}

// rootLike is the reverse-set predicate: a member NAME that looks like a root or a
// path. A member of a listed source type with such a name and a string type must
// be allowed or explicitly rejected.
func rootLike(name string) bool {
	for _, suf := range []string{"Root", "File", "Dir", "Path"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// declaredMembers lists the string-typed root-like members (fields, and methods
// whose first result is string) of a named type in a package's scanned files, and
// reports whether the type was found.
func declaredMembers(pkg *typedPkg, typ string) (fields, methods map[string]bool, found bool) {
	fields, methods = map[string]bool{}, map[string]bool{}
	for _, tf := range pkg.files {
		for _, d := range tf.ast.Decls {
			switch x := d.(type) {
			case *ast.GenDecl:
				for _, sp := range x.Specs {
					ts, ok := sp.(*ast.TypeSpec)
					if !ok || ts.Name.Name != typ {
						continue
					}
					found = true
					if st, ok := ts.Type.(*ast.StructType); ok {
						for _, fl := range st.Fields.List {
							if id, ok := fl.Type.(*ast.Ident); ok && id.Name == "string" {
								for _, n := range fl.Names {
									fields[n.Name] = true
								}
							}
						}
					}
				}
			case *ast.FuncDecl:
				if x.Recv == nil || len(x.Recv.List) == 0 || recvTypeName(x.Recv.List[0].Type) != typ {
					continue
				}
				if x.Type.Results != nil && len(x.Type.Results.List) > 0 {
					if id, ok := x.Type.Results.List[0].Type.(*ast.Ident); ok && id.Name == "string" {
						methods[x.Name.Name] = true
					}
				}
			}
		}
	}
	return fields, methods, found
}

// checkVaultSources is the reverse-set check and the stub drift check.
func checkVaultSources(tree *typedTree) []Finding {
	byPath := map[string]*typedPkg{}
	for _, p := range tree.pkgs {
		byPath[p.importPath] = p
	}
	var out []Finding
	for _, s := range vaultSources {
		p := byPath[s.ImportPath]
		if p == nil {
			continue // package not in this tree (a fixture): nothing to judge
		}
		id := s.ImportPath + "." + s.Type
		fields, methods, found := declaredMembers(p, s.Type)
		if !found {
			out = append(out, metaFinding("sourceaudit.vaultSources/STALE "+id,
				"the listed source type no longer exists in "+s.ImportPath))
			continue
		}
		allowed := map[string]bool{}
		for _, m := range s.Allow {
			allowed[m] = true
			if !fields[m] && !methods[m] {
				out = append(out, metaFinding("sourceaudit.vaultSources/STALE "+id+"."+m,
					"an ALLOWED source member is gone or is no longer string-typed (field) / string-first-result (method): "+
						"the importer stub is built from this row"))
			}
		}
		for m := range s.Reject {
			if !fields[m] && !methods[m] {
				out = append(out, metaFinding("sourceaudit.vaultSources/STALE "+id+"."+m,
					"a REJECTED member no longer exists: remove the row"))
			}
		}
		var names []string
		for m := range fields {
			names = append(names, m)
		}
		for m := range methods {
			names = append(names, m)
		}
		sort.Strings(names)
		for _, m := range names {
			if !rootLike(m) || allowed[m] || s.Reject[m] != "" {
				continue
			}
			out = append(out, metaFinding("sourceaudit.vaultSources/UNLISTED "+id+"."+m,
				"a root-like string member (name ends in Root/File/Dir/Path) of a listed vault source type is neither "+
					"allowed nor rejected: allow it (it is a vault path) or reject it with the reason, in vault_funnel_config.go"))
		}
	}
	return out
}

// checkTypeIssues compares each package's unexpected type-error fingerprints with
// the accounted multiset, in both directions.
func checkTypeIssues(tree *typedTree) []Finding {
	var out []Finding
	seen := map[string]bool{}
	for _, p := range tree.pkgs {
		seen[p.importPath] = true
		got := map[string]int{}
		for _, fp := range p.sortedIssues() {
			got[fp]++
		}
		want := map[string]int{}
		for _, fp := range typecheckAccounted[p.importPath].fingerprints {
			want[fp]++
		}
		var keys []string
		for k := range got {
			keys = append(keys, k)
		}
		for k := range want {
			if _, ok := got[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch {
			case got[k] > want[k]:
				out = append(out, metaFinding("sourceaudit.typecheck/NEW "+p.importPath+" "+k,
					fmt.Sprintf("the fake-importer type-check of %s produced an UNEXPECTED diagnostic (%d, accounted %d): a "+
						"fail-open taint is only safe while every such diagnostic is explained. Fix the cause, or account the "+
						"fingerprint (category|file|symbol) in typecheckAccounted with its reason", p.importPath, got[k], want[k])))
			case got[k] < want[k]:
				out = append(out, metaFinding("sourceaudit.typecheck/STALE "+p.importPath+" "+k,
					fmt.Sprintf("an accounted diagnostic is no longer produced (%d seen, %d accounted): update typecheckAccounted "+
						"in the same commit", got[k], want[k])))
			}
		}
	}
	return out
}

// shapeFloors are the per-shape floors. Each says what it counts and why a drop
// means a shape disappeared; the numbers were derived from the first green run on
// the real tree and are never edited to make a red gate pass.
func shapeFloors(e *taintEngine) []Finding {
	var out []Finding
	st := e.stats
	add := func(what string, got int, floor int, why string) {
		if got < floor {
			out = append(out, metaFinding("sourceaudit.vaultWriteFunnel/VACUOUS "+what,
				fmt.Sprintf("%s: saw %d, expected at least %d — %s, so a passing verdict is vacuous", what, got, floor, why)))
		}
	}
	add("designated seeds", st.seeds, 250, "the parameter seeding collapsed (a rename of the seed names or package set)")
	add("typed-selector hits", st.selectorHits, 440, "go/types Selections stopped resolving the allowlisted source members")
	add("producers", st.producers, 45, "the producer-summary pass stopped computing result taint")
	for _, f := range []struct {
		pkg   string
		floor int
	}{{"storage", 35}, {"main", 2}, {"vaultlock", 2}, {"absorb", 1}, {"archive", 1}, {"departure", 1},
		{"migrate", 1}, {"reconcile", 1}, {"search", 1}, {"surface", 1}, {"tools", 1}, {"vaultfs", 1}} {
		add("producers in "+f.pkg, st.perPkg[f.pkg], f.floor, "that producer-bearing package lost its shape (e.g. its type-check failed)")
	}
	for _, name := range []string{"archive.manifestPathFor", "main.resolveProjectAndVault", "main.resolveProjectForWrite"} {
		if !st.multi[name] {
			out = append(out, metaFinding("sourceaudit.vaultWriteFunnel/VACUOUS multi-value "+name,
				name+" must have a computed MULTI-VALUE producer summary (a vault-derived result at index >= 1): that is the M6 "+
					"shape, and its disappearance means multi-value assignment pairing regressed even though no fixture changed"))
		}
	}
	// migrate.moveLegacyMarker was a third required wrapper until the imports ledger
	// moved into the funnel (task migrate-marker-writes-the-vault-outside-the-funnel):
	// it no longer reaches a raw write, so its disappearance is the fix, not a lost shape.
	add("wrapper summaries", st.wrappers, 25, "the wrapper (param -> raw sink) pass stopped computing")
	for _, name := range []string{"vaultlock.TryAcquireFile", "vplog.Init"} {
		if !st.wrapperNames[name] {
			out = append(out, metaFinding("sourceaudit.vaultWriteFunnel/VACUOUS wrapper "+name,
				name+" must be summarised as a wrapper (a parameter reaches a raw os.* write): it is the shape behind a live finding"))
		}
	}
	for _, m := range []string{
		pathVaultlock + ".Held.Root@storage",
	} {
		if !st.crossUse[m] {
			out = append(out, metaFinding("sourceaudit.vaultWriteFunnel/VACUOUS stub "+m,
				"the importer stub no longer resolves "+m+": a typed source used ACROSS a package boundary is invisible"))
		}
	}
	return out
}

// funnelMetaFindings is every self-check of the typed taint. The shape floors fire
// on a tree that does not hold the shapes (a fixture), exactly like the existing
// floors; tests filter on the Symbol.
func funnelMetaFindings(tree *typedTree, e *taintEngine, edgeUsed map[string]int) []Finding {
	var out []Finding
	out = append(out, checkVaultSources(tree)...)
	out = append(out, checkTypeIssues(tree)...)
	var edges []string
	for k := range funnelAllowedEdges {
		edges = append(edges, k)
	}
	sort.Strings(edges)
	for _, k := range edges {
		if edgeUsed[k] < funnelAllowedEdges[k].count {
			out = append(out, metaFinding("sourceaudit.funnelEdge/STALE "+k,
				"an enumerated call edge no longer exists (renamed, moved, or its path is no longer vault-derived): "+
					"update funnelAllowedEdges in the same commit"))
		}
	}
	out = append(out, shapeFloors(e)...)
	return out
}
