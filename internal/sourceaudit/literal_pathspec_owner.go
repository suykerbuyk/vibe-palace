// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// literalPathspecOwner keeps vp's git on literal pathspecs, with ONE way out.
//
// # Why this rule exists
//
// gitenv.SafeGitEnv sets GIT_LITERAL_PATHSPECS=1 for every git subprocess, so
// a vault path is always a name: without it a file named a[1].md also commits
// a1.md, and one named ':!x.md' commits everything EXCEPT x.md (task
// commit-paths-read-as-pathspec-globs). git check-ignore refuses literal mode,
// so storage.GitPathIgnored turns it off with gitenv.GlobPathspecs and
// neutralises a leading ':' itself. Every other way back to pattern semantics
// is a silent reopening of the bug, which no behavioural test of today's call
// sites can catch, so it is caught here.
//
// It reports, in non-test code:
//
//  1. Any reference to gitenv.GlobPathspecs outside globPathspecsOwners — a
//     selector on the file's import of the gitenv package (under any alias),
//     or a bare identifier inside gitenv itself or under a dot import of it — in function bodies and in
//     package-level var/const initializers.
//  2. Any string literal containing a global pathspec-mode variable name or a
//     command-line flag that turns literal mode off or conflicts with it
//     (literalPathspecBypasses), outside package gitenv. That is the
//     hand-rolled bypass that never touches the constant: an extra
//     "GIT_LITERAL_PATHSPECS=0", or `git --no-literal-pathspecs`.
//  3. Any string literal that BEGINS with pathspec magic (pathspecMagicPrefixes).
//     Under literal mode a pathspec like ':(glob)palace/*/.local/**' is the name
//     of a file nobody has, so it matches nothing and git exits 0: a silent
//     zero, which is what TrackedPalaceLocalFiles would have become.
//
// Package sourceaudit itself is exempt from parts 2 and 3, because it must
// spell the strings it looks for.
//
// # What it cannot see, stated
//
// AST-only and name-based, like every rule here. A string assembled at run
// time ("GIT_LITERAL_" + "PATHSPECS=0", or a ':' prepended to a pathspec) is
// invisible; so is a git runner that builds its environment without SafeGitEnv
// at all, which is git-exec-unsafe-env's question (and that rule sees only
// exec.Command calls naming a literal "git"). The gitenv package is matched by
// import-path suffix "/gitenv", so a stray package of that name counts.
var globPathspecsOwners = map[string]string{
	"storage.GitPathIgnored": "git check-ignore refuses literal mode; passes \"./\"+rel instead",
}

// literalPathspecBypasses are the substrings part 2 reports.
var literalPathspecBypasses = []string{
	"GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS",
	"--no-literal-pathspecs", "--glob-pathspecs", "--icase-pathspecs",
}

// pathspecMagicPrefixes are the leading strings part 3 reports: long-form
// magic, and the two short forms of exclude.
var pathspecMagicPrefixes = []string{":(", ":!", ":^"}

func literalPathspecOwner(files []file) []Finding {
	var out []Finding
	seen := map[string]bool{}
	add := func(f Finding) {
		if !seen[f.Symbol] {
			seen[f.Symbol] = true
			out = append(out, f)
		}
	}
	found := map[string]bool{}
	declared := false

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		gitenvNames, dotGitenv := importNamesWithSuffix(f.ast, "gitenv")
		for _, s := range gitOwnerScopes(f) {
			scope := pkg + "." + s.name
			if _, ok := globPathspecsOwners[scope]; ok {
				found[scope] = true
			}
			if pkg == "gitenv" && s.name == "GlobPathspecs" {
				declared = true
			}
			ast.Inspect(s.body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.SelectorExpr:
					if x, ok := v.X.(*ast.Ident); ok && gitenvNames[x.Name] && v.Sel.Name == "GlobPathspecs" {
						if _, ok := globPathspecsOwners[scope]; !ok {
							add(globPathspecsFinding(f, v.Pos(), scope))
						}
						return false
					}
				case *ast.Ident:
					if (pkg == "gitenv" || dotGitenv) && v.Name == "GlobPathspecs" {
						add(globPathspecsFinding(f, v.Pos(), scope))
					}
				case *ast.BasicLit:
					if v.Kind != token.STRING || pkg == "sourceaudit" {
						return true
					}
					val, err := strconv.Unquote(v.Value)
					if err != nil {
						return true
					}
					if pkg != "gitenv" {
						for _, b := range literalPathspecBypasses {
							if strings.Contains(val, b) {
								add(Finding{
									Kind:   KindLiteralPathspecOptOut,
									Symbol: scope + " -> " + b,
									Pos:    posOf(f, v.Pos()),
									Detail: fmt.Sprintf(
										"%s spells %q in a string literal. vp's git reads pathspecs literally "+
											"(gitenv.SafeGitEnv), so every vault path is a name, never a pattern; "+
											"setting a pathspec mode by hand, or passing a flag that turns literal "+
											"mode off, reopens the glob and ':'-magic bug. If a git command truly "+
											"refuses literal mode, it is a new owner: use gitenv.GlobPathspecs, "+
											"neutralise a leading ':' yourself, and add it to globPathspecsOwners.",
										scope, b),
								})
							}
						}
					}
					for _, m := range pathspecMagicPrefixes {
						if strings.HasPrefix(val, m) {
							add(Finding{
								Kind:   KindLiteralPathspecOptOut,
								Symbol: scope + " -> magic " + m,
								Pos:    posOf(f, v.Pos()),
								Detail: fmt.Sprintf(
									"%s has a string literal beginning with pathspec magic %q. vp's git reads "+
										"pathspecs literally, so this is the name of a file nobody has: it "+
										"matches nothing and git exits 0. List the literal directory and "+
										"filter in Go instead.",
									scope, m),
							})
						}
					}
				}
				return true
			})
		}
	}

	// Anchors: the owner must exist, and so must the constant, or the rule
	// guards a name nothing carries. Same idiom as gitEnabledOwner.
	var absent []string
	for name := range globPathspecsOwners {
		if !found[name] {
			absent = append(absent, name)
		}
	}
	if !declared {
		absent = append(absent, "gitenv.GlobPathspecs")
	}
	sort.Strings(absent)
	for _, name := range absent {
		add(Finding{
			Kind:   KindLiteralPathspecOptOut,
			Symbol: "sourceaudit.literalPathspecOwner/ABSENT/" + name,
			Pos:    "internal/sourceaudit/literal_pathspec_owner.go",
			Detail: fmt.Sprintf(
				"literalPathspecOwner anchors on %q and NO declaration by that name exists in the audited "+
					"tree. It was renamed or moved (update this rule in the same commit) or deleted (remove "+
					"the entry). Do NOT baseline this entry — a baselined anchor is a disabled rule.",
				name),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

func globPathspecsFinding(f file, pos token.Pos, scope string) Finding {
	return Finding{
		Kind:   KindLiteralPathspecOptOut,
		Symbol: scope + " -> GlobPathspecs",
		Pos:    posOf(f, pos),
		Detail: fmt.Sprintf(
			"%s uses gitenv.GlobPathspecs, which turns literal pathspecs off. Its one sanctioned user is "+
				"storage.GitPathIgnored (git check-ignore refuses literal mode). Anywhere else it lets a "+
				"vault path be read as a glob or as ':' magic. If this git command truly refuses literal "+
				"mode, neutralise a leading ':' yourself and add the function to globPathspecsOwners.",
			scope),
	}
}

// importNamesWithSuffix returns the names under which f imports a package whose
// import path is last or ends in "/"+last, and whether any of those imports is
// a dot import.
func importNamesWithSuffix(f *ast.File, last string) (map[string]bool, bool) {
	names := map[string]bool{}
	dot := false
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || (path != last && !strings.HasSuffix(path, "/"+last)) {
			continue
		}
		switch {
		case imp.Name == nil:
			names[last] = true
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	return names, dot
}
