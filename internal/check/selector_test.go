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
		if r.Name == "Host surfaces" {
			continue // inspects $HOME plugin trees, not the vault
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
	// Host surfaces may be Skip or present depending on $HOME; always one row.
	want := []string{"Core floor", "Host surfaces", "Pin coverage", "Resume caps", "Resume refs",
		"Stray scaffolds", "Surface", "Vault abs paths", "Vault filesystem"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("check row names = %v, want %v", got, want)
	}
}

// TestVaultFilesystemProducer covers the `vault-filesystem` selector end to
// end through the registry — the only way an MCP host can reach it.
//
// Its no-vault verdict is the interesting half. CheckVaultFilesystem guards
// the empty root ITSELF (vaultfs.go) rather than being wrapped in a guard like
// the vault-scoped producers, so the producer carries no `vaultRoot == ""`
// branch of its own. That is only safe while the check's own guard returns the
// SAME Skip/"no vault configured" row the wrappers synthesize — pin it, or a
// future edit to vaultfs.go silently gives one selector a different
// degradation contract from every other.
func TestVaultFilesystemProducer(t *testing.T) {
	t.Run("no_vault", func(t *testing.T) {
		rs, err := RunSelected("", "vault-filesystem")
		if err != nil {
			t.Fatalf("RunSelected: %v", err)
		}
		if len(rs) != 1 {
			t.Fatalf("want one result, got %d", len(rs))
		}
		if rs[0].Name != "Vault filesystem" || rs[0].Status != Skip {
			t.Errorf("got %+v, want a skipped Vault filesystem row", rs[0])
		}
		if rs[0].Summary != "no vault configured" {
			t.Errorf("summary = %q, want the shared %q wording", rs[0].Summary, "no vault configured")
		}
	})

	t.Run("missing_root_is_skip_not_fail", func(t *testing.T) {
		gone := filepath.Join(t.TempDir(), "not-there")
		rs, err := RunSelected(gone, "vault-filesystem")
		if err != nil {
			t.Fatalf("RunSelected: %v", err)
		}
		if rs[0].Status != Skip {
			t.Errorf("absent root = %v, want Skip — no vault is not evidence of a hostile filesystem: %+v",
				rs[0].Status, rs[0])
		}
	})

	t.Run("real_root_probes_and_cleans_up", func(t *testing.T) {
		root := t.TempDir()
		rs, err := RunSelected(root, "vault-filesystem")
		if err != nil {
			t.Fatalf("RunSelected: %v", err)
		}
		if rs[0].Status == Skip {
			t.Fatalf("a present, writable root must actually probe, got %+v", rs[0])
		}
		// This is the ONE producer that touches the filesystem it is
		// reporting on. It must leave nothing behind: vp_check runs it
		// against the live vault on every MCP caller's default invocation,
		// and a residual probe file would show up as vault dirt in the very
		// tidy report the restart preflight reads next.
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), "vp-fs-probe") {
				t.Errorf("probe file %q survived the check", e.Name())
			}
		}
	})
}

// TestStrayScaffoldsProducer covers the `stray-scaffolds` selector through the
// registry, including the no-vault verdict. Its underlying check dereferences
// v.Root unconditionally, so the producer's empty-root guard is not cosmetic:
// without it the MCP tool would panic on a host whose vault never resolved.
func TestStrayScaffoldsProducer(t *testing.T) {
	t.Run("no_vault", func(t *testing.T) {
		rs, err := RunSelected("", "stray-scaffolds")
		if err != nil {
			t.Fatalf("RunSelected: %v", err)
		}
		if len(rs) != 1 {
			t.Fatalf("want one result, got %d", len(rs))
		}
		if rs[0].Name != "Stray scaffolds" || rs[0].Status != Skip {
			t.Errorf("got %+v, want a skipped Stray scaffolds row", rs[0])
		}
		if rs[0].Summary != "no vault configured" {
			t.Errorf("summary = %q, want the shared %q wording", rs[0].Summary, "no vault configured")
		}
	})

	t.Run("finds_a_scaffold_only_project", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "Projects", "ghost")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		rs, err := RunSelected(root, "stray-scaffolds")
		if err != nil {
			t.Fatalf("RunSelected: %v", err)
		}
		if rs[0].Status != Info {
			t.Fatalf("status = %v, want Info: %+v", rs[0].Status, rs[0])
		}
		joined := strings.Join(append([]string{rs[0].Summary}, rs[0].Details...), "\n")
		if !strings.Contains(joined, "ghost") {
			t.Errorf("the stray project must be NAMED, not just counted:\n%s", joined)
		}
	})

	t.Run("clean_vault_passes", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "Projects", "real")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for _, f := range []string{"config.toml", "resume.md"} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
				t.Fatalf("write %s: %v", f, err)
			}
		}
		rs, err := RunSelected(root, "stray-scaffolds")
		if err != nil {
			t.Fatalf("RunSelected: %v", err)
		}
		if rs[0].Status != Pass {
			t.Errorf("status = %v, want Pass: %+v", rs[0].Status, rs[0])
		}
	})
}

func mustRunAll(t *testing.T) []Result {
	t.Helper()
	results, err := RunSelected("", "")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	return results
}
