// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// writeGo drops a parseable Go file at root/rel, creating directories.
func writeGo(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// walked returns the slash-separated paths walkGoFiles visited, relative to root.
func walked(t *testing.T, root string, keep func(string) bool) []string {
	t.Helper()
	var got []string
	err := walkGoFiles(root, keep, func(p string) error {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		got = append(got, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walkGoFiles: %v", err)
	}
	sort.Strings(got)
	return got
}

func allFiles(string) bool { return true }

// 🔴 The root guard. A root whose own base name is dot-prefixed must still be
// walked: the prune applies to directories INSIDE the tree, never to the tree
// the caller named. Without `if path == root { return nil }` this walk returns
// nothing — and an audit that finds nothing passes, which is the false-green
// this package exists to eliminate.
//
// This is the hermetic form of the production hazard: repoRoots[0] is "..", a
// relative root whose DirEntry name is literally dot-prefixed. That path is
// exercised on every run of TestSourceAuditGate, but only implicitly; this test
// fails loudly and immediately instead.
func TestWalkGoFilesNeverSkipsTheRootItself(t *testing.T) {
	tmp := t.TempDir()
	// A root that the prune rule would reject if it were ever applied to it.
	root := filepath.Join(tmp, ".hidden-root")
	writeGo(t, root, "a.go")
	writeGo(t, root, "pkg/b.go")

	got := walked(t, root, allFiles)
	if want := []string{"a.go", "pkg/b.go"}; !slices.Equal(got, want) {
		t.Fatalf("walked %v, want %v: the walk skipped its own root", got, want)
	}

	// The same hazard through a relative root, which is the shape repoRoots
	// actually uses.
	sub := filepath.Join(root, "pkg")
	rel, err := filepath.Rel(sub, root)
	if err != nil {
		t.Fatal(err)
	}
	if rel != ".." {
		t.Fatalf("fixture: rel = %q, want ..", rel)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if got := walked(t, "..", allFiles); len(got) != 2 {
		t.Fatalf("relative root %q walked %v, want 2 files", "..", got)
	}
}

// Every gitignored tree is pruned by the dot-prefix rule rather than by name.
// .wt is the ADR-017 worktree root — a COMPLETE second copy of the module,
// which is what produced 30 findings from zero real breaches — and .cache is
// agent scratch, where a module copy has already appeared once. Neither is
// named anywhere in the prune: the rule is structural, so the next gitignored
// directory is covered without a third hand-list.
func TestWalkGoFilesPrunesGitignoredAndVendoredTrees(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/real.go")
	for _, pruned := range []string{
		".wt/sibling/internal/copy.go",
		".cache/chair/baseline-copy/internal/copy.go",
		".git/hooks/x.go",
		".claude/agent/x.go",
		"vendor/dep/x.go",
		"internal/testdata/fixture.go",
		"third_party/coder-hnsw/upstream.go",
	} {
		writeGo(t, root, pruned)
	}

	got := walked(t, root, allFiles)
	if want := []string{"internal/real.go"}; !slices.Equal(got, want) {
		t.Fatalf("walked %v, want only %v", got, want)
	}
}

// The two callers keep DIFFERENT file filters, and walkGoFiles must not impose
// one: loadPackages audits test files (hermeticTestMain and friends exist for
// them), while the KG walk in kg_records_test.go excludes them. Unifying the
// two silently would change which files each rule sees.
func TestWalkGoFilesLeavesTheFileFilterToTheCaller(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "a.go")
	writeGo(t, root, "a_test.go")
	writeGo(t, root, "notes.md")

	if got := walked(t, root, allFiles); !slices.Equal(got, []string{"a.go", "a_test.go"}) {
		t.Fatalf("with an accept-all filter, walked %v, want both .go files and no .md", got)
	}
	nonTest := func(p string) bool { return !strings.HasSuffix(p, "_test.go") }
	if got := walked(t, root, nonTest); !slices.Equal(got, []string{"a.go"}) {
		t.Fatalf("with the KG walk's filter, walked %v, want only a.go", got)
	}
}

// The production consequence of the two preceding tests, asserted on the real
// tree: loadPackages over this repository's own roots finds test files, and
// finds a non-trivial number of files at all. It is the loadPackages-side
// companion to moduleSources' `len(out) < 100` tripwire — stated as a shape,
// not a stored count, because a stored count rots.
func TestLoadPackagesWalksThisRepositoryAndKeepsTestFiles(t *testing.T) {
	files, err := loadPackages(repoRoots...)
	if err != nil {
		t.Fatalf("loadPackages: %v", err)
	}
	if len(files) < 100 {
		t.Fatalf("loadPackages parsed %d files; the walk is probably pointed at the wrong tree", len(files))
	}
	tests := 0
	for _, f := range files {
		if f.isTest {
			tests++
		}
		// Component-wise, not a substring: repoRoots are relative, so every
		// path legitimately begins with ".." and a naive strings.Contains("/.")
		// would flag all of them.
		for _, part := range strings.Split(filepath.ToSlash(f.path), "/") {
			if part == "." || part == ".." || !strings.HasPrefix(part, ".") {
				continue
			}
			t.Fatalf("loadPackages walked into dot-prefixed directory %q: %s", part, f.path)
		}
	}
	if tests == 0 {
		t.Fatal("loadPackages found no _test.go files; its filter was unified with the KG walk's")
	}
}
