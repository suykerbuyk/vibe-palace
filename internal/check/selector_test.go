// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// names flattens results to their check names, which is what ordering is
// asserted on.
func names(results []Result) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Name)
	}
	return out
}

// TestProducerOrderCoversProducers pins the invariant the default-all path
// rests on: every producer is named exactly once in the declared order. A
// producer missing from ProducerOrder would silently never run when the filter
// is omitted — a check that reaches nobody, which is the failure this whole
// change exists to end.
func TestProducerOrderCoversProducers(t *testing.T) {
	if len(ProducerOrder) != len(Producers) {
		t.Fatalf("ProducerOrder has %d names, Producers has %d", len(ProducerOrder), len(Producers))
	}
	seen := map[string]bool{}
	for _, name := range ProducerOrder {
		if _, ok := Producers[name]; !ok {
			t.Errorf("ProducerOrder names %q, which is not a producer", name)
		}
		if seen[name] {
			t.Errorf("ProducerOrder names %q twice", name)
		}
		seen[name] = true
	}
	for name := range Producers {
		if !seen[name] {
			t.Errorf("producer %q is missing from ProducerOrder and would never run by default", name)
		}
	}
}

// TestRunSelectedDefaultsToEveryProducer covers the H2 default: an omitted or
// blank filter runs the whole cheap suite instead of erroring. `vp check
// --check ""` never reaches here (the CLI routes an empty --check to the full
// suite), but an MCP caller omitting an optional argument does, constantly.
func TestRunSelectedDefaultsToEveryProducer(t *testing.T) {
	for _, filter := range []string{"", "   "} {
		results, err := RunSelected("", filter)
		if err != nil {
			t.Fatalf("RunSelected(%q): %v", filter, err)
		}
		if len(results) != len(ProducerOrder) {
			t.Fatalf("RunSelected(%q) ran %d checks, want %d", filter, len(results), len(ProducerOrder))
		}
	}
}

// TestRunSelectedDeclaredOrder covers N1: the default-all path is the first
// iteration of Producers anywhere in the tree, and Go randomizes map order.
func TestRunSelectedDeclaredOrder(t *testing.T) {
	root := t.TempDir()

	var want []string
	for _, sel := range ProducerOrder {
		want = append(want, names(Producers[sel](root))...)
	}

	for i := range 16 {
		results, err := RunSelected(root, "")
		if err != nil {
			t.Fatalf("RunSelected: %v", err)
		}
		if got := names(results); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("run %d order = %v, want declared %v", i+1, got, want)
		}
	}
}

// TestRunSelectedExplicitList keeps the CLI's semantics: the caller's order
// wins, names are trimmed, and an unknown name is a hard error.
func TestRunSelectedExplicitList(t *testing.T) {
	results, err := RunSelected("", " resume-refs , surface ")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if got := names(results); strings.Join(got, ",") != "Resume refs,Surface" {
		t.Errorf("caller order not preserved: %v", got)
	}

	if _, err := RunSelected("", "surface,no-such-check"); err == nil {
		t.Errorf("unknown check name must error")
	} else if !strings.Contains(err.Error(), "no-such-check") {
		t.Errorf("error must name the offender: %v", err)
	}

	// An explicitly supplied list that names nothing runnable keeps the
	// long-standing CLI error rather than quietly meaning "everything".
	if _, err := RunSelected("", ",,"); err == nil {
		t.Errorf(`RunSelected("", ",,") must report no checks selected`)
	}
}

// TestRunSelectedSkipsWithoutVault pins the degradation contract every
// vault-scoped producer shares: no vault root → Skip, never a panic and never a
// bogus Pass.
func TestRunSelectedSkipsWithoutVault(t *testing.T) {
	results, err := RunSelected("", "")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	for _, r := range results {
		if r.Name == "Surface" {
			continue // CheckSurface tolerates an empty root on its own terms
		}
		if r.Status != Skip {
			t.Errorf("%s with no vault = %v, want Skip", r.Name, r.Status)
		}
	}
}

// TestRunSelectedIsPure is the H1 regression guard, asserted at the package
// boundary: internal/check must never resolve a vault from the process working
// directory. `vp mcp` is long-lived and its cwd is the host's launch directory,
// so a cwd lookup in here would silently scan the wrong vault for every agent.
//
// The cwd below resolves — via .vibe-palace.toml — to a real vault holding a
// resume with a host-local plan reference. RunSelected is handed an EMPTY root,
// so it must report Skip. Any cwd resolution would instead report that vault's
// breach.
func TestRunSelectedIsPure(t *testing.T) {
	decoy := t.TempDir()
	dir := filepath.Join(decoy, "Projects", "decoyproj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume.md"),
		[]byte("plan: ~/.claude/plans/decoy.md\n"), 0o644); err != nil {
		t.Fatalf("write resume: %v", err)
	}

	cwd := t.TempDir()
	toml := "vault_path = " + strconv.Quote(decoy) + "\n"
	if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write .vibe-palace.toml: %v", err)
	}
	t.Chdir(cwd)

	results, err := RunSelected("", "resume-refs")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want one result, got %d", len(results))
	}
	if results[0].Status != Skip {
		t.Fatalf("RunSelected resolved a vault from the process cwd: %+v", results[0])
	}

	// And the same call against the decoy root DOES see it — proving the test
	// would have caught a cwd lookup rather than passing vacuously.
	results, err = RunSelected(decoy, "resume-refs")
	if err != nil {
		t.Fatalf("RunSelected(decoy): %v", err)
	}
	if results[0].Status != Info {
		t.Fatalf("decoy vault should report a host-local ref: %+v", results[0])
	}
}

// TestProducersSkipContractNamesAreStable guards the row names the MCP envelope
// and the CLI table both key on.
func TestProducersSkipContractNamesAreStable(t *testing.T) {
	got := names(mustRunAll(t))
	sort.Strings(got)
	want := []string{"Core floor", "Pin coverage", "Resume caps", "Resume refs", "Surface"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("check row names = %v, want %v", got, want)
	}
}

func mustRunAll(t *testing.T) []Result {
	t.Helper()
	results, err := RunSelected("", "")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	return results
}
