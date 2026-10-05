// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// htmFindings runs the audit over a tree of test files (dir/name -> source)
// and returns the hermetic-test-main symbols it reports.
func htmFindings(t *testing.T, tree map[string]string) []string {
	t.Helper()
	root := t.TempDir()
	for rel, src := range tree {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindHermeticTestMain {
			out = append(out, filepath.Base(f.Symbol))
		}
	}
	slices.Sort(out)
	return out
}

const htmHermetic = `package good

import (
	"os"
	"testing"

	tu "example.com/m/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(tu.RunHermetic(m)) }
`

func TestHermeticTestMainReportsEachUnhermeticPackage(t *testing.T) {
	got := htmFindings(t, map[string]string{
		// A TestMain calling RunHermetic through an aliased import: clean.
		"good/main_test.go": htmHermetic,
		"good/x_test.go":    "package good\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
		// Test files and no TestMain at all.
		"bare/x_test.go": "package bare\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
		// A TestMain that runs the tests without RunHermetic.
		"plain/main_test.go": "package plain\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) { os.Exit(m.Run()) }\n",
		// RunHermetic named, but from a package that is not testutil.
		"other/main_test.go": "package other\n\nimport (\n\t\"os\"\n\t\"testing\"\n\n\t\"example.com/m/internal/elsewhere\"\n)\n\nfunc TestMain(m *testing.M) { os.Exit(elsewhere.RunHermetic(m)) }\n",
		// No test files: nothing to run hermetically.
		"lib/lib.go": "package lib\n",
		// testutil itself defines RunHermetic and is exempt.
		"internal/testutil/x_test.go": "package testutil\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
	})
	want := []string{"bare", "other", "plain"}
	if !slices.Equal(got, want) {
		t.Errorf("hermetic-test-main findings = %q, want %q", got, want)
	}
}
