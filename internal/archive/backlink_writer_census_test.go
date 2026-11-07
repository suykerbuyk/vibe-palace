// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 🔴 THE STRUCTURAL HALF OF THE BACK-LINK INVARIANT — AND IT IS KEYED ON THE
// CALL, NOT ON ANY IDENTIFIER.
//
// The rule is: no writer of a manifest back-link may withhold it because the
// canonical note is an auto-capture stub. That rule is enforced BEHAVIOURALLY,
// one arm per session-capture writer, in
// TestIntegrationBackLinkCallSitesAgreeOnAStubOnlySession. A guard added at any
// of those sites — spelled any way at all — reddens that site's arm.
//
// WHY A BEHAVIOURAL RATCHET CANNOT BE COMPLETE ON ITS OWN, which is the question
// the send-back asked. Behaviour is only observable through a path that something
// drives. A test suite can prove the rule holds at every writer it KNOWS about;
// it is blind by construction to a writer nobody wrote an arm for. That is not a
// gap in the arms, it is what "behavioural" means. The first version of this unit
// learned it twice: an arm that drove SessionEnd alone could not see a guard at
// internal/hook, and the arms as shipped could not see one at
// internal/capture/session.go — which both adversarial reviewers then exploited,
// using `meta.Tag != storage.TagAutoCapture`, a spelling that re-adds no previous
// identifier and so defeated the identifier-keyed check in internal/storage
// entirely.
//
// So the two halves divide the work: the ARMS prove the rule at every known
// writer, and THIS TEST guarantees that "every known writer" stays the complete
// set. It fails when the census below stops matching the tree, so a new
// back-link writer cannot be added silently — whoever adds one must come here,
// and the entry they must write names the arm they owe.
//
// It is keyed on REFERENCES to the two archive functions, with the import alias
// resolved per file, so renaming a guard, a field, a tag constant or the import
// itself cannot defeat it; only adding or removing a reference moves it.
//
// 🔴 WHAT THIS CENSUS DOES NOT GUARANTEE. Writing that it makes the writer set
// complete would be the same overclaim this unit has now found four times, so
// here is the boundary, stated rather than implied. It is an AST walk over this
// repository's production .go files. It therefore catches: a direct qualified
// call; a call through an aliased import; a method VALUE taken and called later;
// a second reference inside a function already listed; a reference in a new
// function, file or package; and build-tagged files, because it parses every
// file regardless of tags. It fails CLOSED on a parse error.
//
// It CANNOT catch a writer reached through interface dispatch, through
// reflection, or through a wrapper in a third package that this walk sees only
// as a call to that package. Nothing short of a whole-program type and
// call-graph analysis would, and that is not what this is. What the census
// guarantees is narrower and still worth having: **no one adds a back-link
// writer by naming these functions directly without this test failing**, which
// is how all four real writers are written today and how the reviewers' bypasses
// were written too.
//
// If a writer ever does appear behind an interface, this test will not say so.
// The arms are what would notice, by the back-link not being written.
// archivePkgPath is the import path whose alias the walk resolves per file.
const archivePkgPath = "github.com/suykerbuyk/vibe-palace/internal/archive"

func TestBackLinkWriterCensus(t *testing.T) {
	// Every production (non-test) call of LinkSessionNote / TryLinkSessionNote,
	// with WHO covers it. Session-capture writers must each have an arm in
	// TestIntegrationBackLinkCallSitesAgreeOnAStubOnlySession.
	// 🔴 THE COUNT IS PART OF THE KEY, and that is not fussiness. The first
	// version of this census recorded only WHICH functions call, so a SECOND
	// call added inside a function already listed here was invisible — a new
	// writer, in a known function, with no arm. Caught by mutating this test
	// (M16) after it was green, which is the third time in this unit that a
	// check which read as complete could not fire.
	want := map[string]struct {
		calls int
		why   string
	}{
		"internal/capture/codex_receipt.go:WriteCodexArchiveReceipt": {1, "arm `codex`   — Codex SessionEnd receipt"},
		"internal/capture/session.go:WriteSession":                   {1, "arm `capture` — capture with the archive already on disk"},
		"internal/hook/hook.go:Run":                                  {1, "arm `claude`  — Claude SessionEnd after a prior Stop"},

		// NOT a session-capture writer and deliberately not covered by an arm:
		// the operator-invoked applier, which writes the back-link for a pair a
		// human has already reviewed. It has no stub decision to make — the
		// canonical note is chosen by storage.preferCanonical before it runs —
		// and it is covered by internal/vaultaudit's applier tests. TWO calls:
		// one per class (half-linked, and the class-1 path through
		// BackfillArchiveLink).
		"internal/vaultaudit/backfill.go:ApplyBackfill": {2, "vaultaudit applier tests (operator-invoked, not a capture path)"},
	}

	root := repoRootFromArchive(t)
	got := map[string]int{}
	var dotImports []string

	// find, never git ls-files: a writer added in an untracked file is exactly
	// the one a census must not miss, and ls-files reports clean by omission.
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".wt", ".cache", "third_party", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		// 🔴 RESOLVE THE IMPORT ALIAS PER FILE. Matching the literal identifier
		// `archive` was the census's own bypass: `import a ".../internal/archive"`
		// then `a.TryLinkSessionNote(...)` compiles, writes a back-link, and was
		// counted as nothing. Found by a reviewer, 2026-11-07.
		// 🔴 A SET, NOT ONE NAME. Go permits the same path to be imported more
		// than once under different names, and the first version of this fix
		// stopped at the first match — so a file holding both the plain import
		// and an alias bound `archive`, ignored the alias, and a writer behind
		// the alias was counted as nothing. The alias fix had the same bypass
		// the alias fix was written to close, found by mutating it.
		qualifiers := map[string]bool{}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) != archivePkgPath {
				continue
			}
			if imp.Name == nil {
				qualifiers["archive"] = true // the package's own name
				continue
			}
			switch imp.Name.Name {
			case ".", "_":
				// A dot-import erases the qualifier entirely, so a bare
				// TryLinkSessionNote(...) would be indistinguishable from any
				// other local call; a blank import cannot reach the symbol but
				// signals someone fighting this walk. Refused outright rather
				// than handled, per the send-back.
				dotImports = append(dotImports, rel+": imports "+archivePkgPath+" as "+imp.Name.Name)
			default:
				qualifiers[imp.Name.Name] = true
			}
		}
		if len(qualifiers) == 0 {
			return nil // this file cannot name the archive package at all
		}

		// 🔴 COUNT REFERENCES, NOT CALLS. `var w = archive.TryLinkSessionNote`
		// followed by `w(...)` is a back-link writer whose call expression names
		// no package at all — the second bypass the reviewer compiled. Matching
		// the SelectorExpr itself catches the method value where it is TAKEN,
		// which is also where a human reviewer would want to see it.
		// 🔴 ATTRIBUTE BY DECLARATION, NOT BY A RUNNING VARIABLE. The first
		// version tracked the last FuncDecl seen during a whole-file Inspect, so
		// a package-level `var w = archive.TryLinkSessionNote` was blamed on
		// whichever function happened to precede it — M20 reddened the census and
		// named the wrong site. A census whose entire job is to name the writer
		// must not lie about where it is. Walking Decls gives the enclosing
		// declaration exactly.
		count := func(where string, n ast.Node) {
			ast.Inspect(n, func(x ast.Node) bool {
				sel, ok := x.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name != "LinkSessionNote" && sel.Sel.Name != "TryLinkSessionNote" {
					return true
				}
				if ident, ok := sel.X.(*ast.Ident); !ok || !qualifiers[ident.Name] {
					return true
				}
				got[rel+":"+where]++
				return true
			})
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				count(d.Name.Name, d)
			case *ast.GenDecl:
				count("<package-level>", d)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(dotImports) > 0 {
		t.Errorf("production code dot- or blank-imports the archive package:\n  %s\n\n"+
			"A dot-import erases the qualifier this census matches on, so a back-link writer behind\n"+
			"one is invisible to it. Import the package normally, with or without an alias.",
			strings.Join(dotImports, "\n  "))
	}

	var added, removed []string
	for k, n := range got {
		w, ok := want[k]
		if !ok {
			added = append(added, fmt.Sprintf("%s  (%d call(s), none expected)", k, n))
			continue
		}
		if n > w.calls {
			added = append(added, fmt.Sprintf("%s  (%d calls, census records %d)", k, n, w.calls))
		}
	}
	for k, w := range want {
		if n := got[k]; n < w.calls {
			removed = append(removed, fmt.Sprintf("%s  (%d of %d call(s) left; covered by: %s)", k, n, w.calls, w.why))
		}
	}
	sort.Strings(added)
	sort.Strings(removed)

	if len(added) > 0 {
		t.Errorf("a NEW manifest back-link writer appeared and nothing drives it:\n  %s\n\n"+
			"Every session-capture writer of a back-link must have its own arm in\n"+
			"TestIntegrationBackLinkCallSitesAgreeOnAStubOnlySession (internal/integration), because a\n"+
			"writer with no arm is a writer where the stub rule can be re-added invisibly — that is\n"+
			"exactly how internal/capture/session.go was left uncovered, and two reviewers exploited\n"+
			"it with `meta.Tag != storage.TagAutoCapture`. Add the arm, prove it reddens by mutating\n"+
			"the new site, then add the entry here. If the new call is NOT a capture path, record\n"+
			"which tests cover it instead.",
			strings.Join(added, "\n  "))
	}
	if len(removed) > 0 {
		t.Errorf("a back-link writer in this census is gone from the tree:\n  %s\n\n"+
			"If the call was deliberately removed, delete its entry here AND the arm that covered it,\n"+
			"so the suite stops claiming to measure something that no longer exists.",
			strings.Join(removed, "\n  "))
	}

	// 🔴 EVERY ARM NAMED ABOVE MUST ACTUALLY EXIST. Without this, the census is
	// satisfiable by adding a map entry instead of an arm — a reviewer raised
	// exactly that, and it is the cheaper half of the same failure the census is
	// for. A shared constant is not available across two test packages, so the
	// arm names are read out of the integration test's own source. That is a
	// source check with its own limit: it proves the subtest NAME exists, not
	// that the arm drives the writer the census claims. The mutation table in
	// that file is what carries the latter.
	armFile := filepath.Join(root, "internal", "integration", "backlink_call_sites_test.go")
	src, rerr := os.ReadFile(armFile)
	if rerr != nil {
		t.Fatalf("read the arm file this census points at: %v", rerr)
	}
	for key, w := range want {
		arm, ok := armNameFrom(w.why)
		if !ok {
			continue // a writer deliberately covered by something other than an arm
		}
		if !strings.Contains(string(src), `t.Run("`+arm+`"`) {
			t.Errorf("census entry %s names arm %q, and no such subtest exists in %s.\n"+
				"Add the arm and prove it reddens by mutating that writer — an entry in this map is\n"+
				"not coverage.",
				key, arm, filepath.ToSlash("internal/integration/backlink_call_sites_test.go"))
		}
	}
}

// armNameFrom pulls the subtest name out of a census entry's "arm `x` — …" note.
// An entry that does not name an arm (the operator-invoked applier) returns false.
func armNameFrom(why string) (string, bool) {
	const prefix = "arm `"
	i := strings.Index(why, prefix)
	if i < 0 {
		return "", false
	}
	rest := why[i+len(prefix):]
	j := strings.Index(rest, "`")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// repoRootFromArchive walks up to the module root so the census does not depend
// on the working directory a runner happens to choose.
func repoRootFromArchive(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, serr := os.Stat(filepath.Join(dir, "go.mod")); serr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find the module root above the archive package")
	return ""
}
