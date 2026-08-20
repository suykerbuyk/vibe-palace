// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"testing"
)

// TestExternalTestPackageDoesNotSeverTheImportGraph pins importGraph's
// non-test filter.
//
// It CANNOT be written against the live tree, and that is the finding rather
// than an inconvenience: the bug needs a directory whose declared package name
// differs from its basename AND an external _test package beside it, and no
// directory satisfies both today. cmd/vp already satisfies the first (package
// main in a directory named vp); one ordinary test-file edit supplies the
// second, and nothing would report it.
//
// The masking mechanism is the unconditional segment fallback: when a package's
// name EQUALS its directory basename, the raw segment records the correct edge
// and the corrupted map entry is harmless noise. Break that equality and the
// mask comes off — which is why this fixture puts `package helperpkg` in a
// directory named `helperdir`.
func TestExternalTestPackageDoesNotSeverTheImportGraph(t *testing.T) {
	root := writeMultiPkgFixture(t, map[string]string{
		// Declared name (helperpkg) deliberately differs from the directory
		// basename (helperdir), so the segment fallback cannot cover for a
		// corrupted map entry.
		"helperdir": `package helperpkg

func Diff() string { return "live — gamma calls this" }
`,
		// beta declares its own Diff, which makes the name AMBIGUOUS tree-wide.
		// Ambiguity is what switches isCalled from permissive to import-scoped,
		// and import-scoped is the only mode that consults the graph at all.
		"beta": `package beta

func Diff() string { return "also live" }

func Drive() string { return Diff() }
`,
		"gamma": `package gamma

import "example.com/fixture/helperdir"

func Drive() string { return helperpkg.Diff() }
`,
	})
	// The external test package. Its filename sorts after helperdir.go, so under
	// the unfiltered loop it wins the last-write and stamps "helperpkg_test"
	// over the directory's real package name.
	writeExtraFile(t, root, "helperdir", "zz_external_test.go", `package helperpkg_test

func helper() {}
`)

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ids(findings); slices.Contains(got, "uninvoked helperpkg.Diff") {
		t.Fatalf("helperpkg.Diff IS called from gamma, which imports it — flagging it means the "+
			"import edge was severed by the external test package overwriting the directory's "+
			"package name. Build pkgByDirBase from NON-TEST files only.\n  findings: %v", got)
	}
}

// TestSegmentFallbackResolvesAnUnparsedImport is the counterweight, and it
// exists because the fallback LOOKS redundant once pkgByDirBase is correct.
//
// It is not. It is the only thing that resolves an import whose directory was
// never parsed — here, a package named `alpha` living in a directory named
// `alphadir` and imported by a path whose final segment is `alpha`. No entry
// keyed `alpha` exists in the map, so the resolved lookup misses entirely and
// the raw segment is the sole source of the edge.
//
// Deleting `out[pkg][seg] = true` turns this red. That is the mutation the task
// demanded in place of an argument about whether the line is dead.
func TestSegmentFallbackResolvesAnUnparsedImport(t *testing.T) {
	root := writeMultiPkgFixture(t, map[string]string{
		"alphadir": `package alpha

func Diff() string { return "live — gamma calls this" }
`,
		"beta": `package beta

func Diff() string { return "also live" }

func Drive() string { return Diff() }
`,
		"gamma": `package gamma

import "example.com/fixture/alpha"

func Drive() string { return alpha.Diff() }
`,
	})

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ids(findings); slices.Contains(got, "uninvoked alpha.Diff") {
		t.Fatalf("alpha.Diff IS called from gamma. The import path's final segment is 'alpha' "+
			"but the package lives in 'alphadir', so pkgByDirBase has no 'alpha' key and the raw "+
			"segment fallback is the ONLY thing that records this edge. It is load-bearing, not "+
			"redundant.\n  findings: %v", got)
	}
}
