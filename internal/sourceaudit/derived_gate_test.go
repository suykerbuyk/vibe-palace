// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

// The derivation type-checks the whole module, which costs ~2 s of wall clock
// and considerably more under -race. Four tests in this package need the same
// answer over the same unchanging tree, so it is computed ONCE.
//
// Memoised rather than merged into a single mega-test on purpose: each property
// below fails on its own terms and names its own defect, which is worth more
// than the seconds a shared fixture would save by collapsing them.
var (
	liveDerivationOnce = sync.OnceValues(func() ([]GateDivergence, error) {
		dir, err := moduleRoot(repoRoots[0])
		if err != nil {
			return nil, err
		}
		return deriveGate(dir)
	})
	livePackagesOnce = sync.OnceValues(func() ([]*packages.Package, error) {
		dir, err := moduleRoot(repoRoots[0])
		if err != nil {
			return nil, err
		}
		return loadModulePackages(dir)
	})
)

func liveDerivation(t *testing.T) []GateDivergence {
	t.Helper()
	divs, err := liveDerivationOnce()
	if err != nil {
		t.Fatalf("derivation over the live tree failed: %v", err)
	}
	return divs
}

func livePackages(t *testing.T) []*packages.Package {
	t.Helper()
	pkgs, err := livePackagesOnce()
	if err != nil {
		t.Fatal(err)
	}
	return pkgs
}

// skipUnlessFullSuite gates the type-checked derivation out of `-short` runs.
//
// # The decision (operator ruling, 2026-08-22)
//
// The derivation type-checks the whole module through go/packages and builds an
// SSA program plus a VTA call graph. That costs ~10 s on its own and ~49 s
// under `make test`'s `-race -cover`, which made internal/sourceaudit the
// slowest package in the suite by an order of magnitude. As a tax on every
// local `make test` that is too high for a rule whose subject — the gate
// classification of 73 commands and 70 tools — changes a few times a year.
//
// # 🔴 WHAT MAKES THIS A GATE AND NOT A DELETION
//
// `make test` passes `-short`, and so does the `test` job in
// .github/workflows/ci.yml. VERIFIED 2026-08-22: before this skip existed,
// NOTHING in CI ran the suite without `-short` — the windows-lock job is both
// `-short` and `-run`-filtered, and `make test-full` / `make cover-full` are
// manual targets no workflow invokes. A bare testing.Short() skip would
// therefore have left this rule running in no automated context at all: green,
// pointless, and exactly the failure mode ADR-010 refuses for
// ungated-vault-writer.
//
// So the skip was landed together with the thing that runs it:
//
//   - `make source-audit` — the whole package, no -short, no -race.
//   - the `source-audit` job in .github/workflows/ci.yml, which runs that
//     target on every push and pull request.
//
// If you remove either of those, you have deleted this rule. The skip message
// names them so a reader hitting SKIP can check that claim rather than trust it.
//
// The syntactic ungated-vault-writer rule is NOT gated and still runs in
// `-short`: it costs ~1.5 s, it anchors two vault mutations the funnel cannot
// contain (surface.WriteFormat, storage.DeleteDrawer), and it is the fallback
// for a toolchain the SSA builder cannot handle.
func skipUnlessFullSuite(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipped under -short: the derived-gate rule type-checks the whole module " +
			"(~10 s alone, ~49 s under -race -cover) and `make test` runs -short. " +
			"COVERAGE IS NOT LOST: `make source-audit` and the `source-audit` CI job run this " +
			"package WITHOUT -short on every push. The syntactic ungated-vault-writer rule " +
			"still runs here. See skipUnlessFullSuite for the full ruling.")
	}
}

// moduleDirForTest is the module root this package's tests derive against.
func moduleDirForTest(t *testing.T) string {
	t.Helper()
	dir, err := moduleRoot(repoRoots[0])
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	return dir
}

// 🔴 TestDerivedGateFailsLoudlyOnAMissingSink IS THE MOST IMPORTANT TEST IN
// THIS FILE, and it is written first for that reason.
//
// A call graph missing a sink reports every command that reaches it as CLEAN,
// in exactly the same shape as a correct result. An earlier probe of this
// analysis found 4 of 5 sinks and was believed only because somebody printed
// the count. So the derivation must REFUSE — not warn, not degrade, not return
// the partial answer — and the refusal must be pinned, because the code path
// that produces it is otherwise never exercised.
//
// The doctored sink stands in for the realistic causes: a renamed primitive, a
// deleted one, one no longer built, and — the likeliest of all — one spelled
// without its receiver, since ssa.Function.String renders a method as
// "(*pkg.Type).method".
func TestDerivedGateFailsLoudlyOnAMissingSink(t *testing.T) {
	skipUnlessFullSuite(t)

	moduleDir := moduleDirForTest(t)

	cases := []struct {
		name string
		sink string
	}{
		{
			// The exact shape of the earlier 4-of-5 result: a method keyed
			// without its receiver. It EXISTS in the graph under its real name.
			name: "method_without_receiver",
			sink: modulePath + "/internal/storage.appendUnderLock",
		},
		{
			name: "renamed_or_deleted_primitive",
			sink: modulePath + "/internal/atomicfile.WriteAllTheThings",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One good sink plus one bad one: the failure must not depend on
			// ALL sinks being missing.
			doctored := []string{modulePath + "/internal/atomicfile.Write", tc.sink}

			divs, err := deriveGateWithSinks(moduleDir, doctored)
			if err == nil {
				t.Fatalf("a missing sink must be a hard error, got %d divergences and nil error", len(divs))
			}
			if divs != nil {
				t.Errorf("a missing sink must emit NO result, got %d divergences", len(divs))
			}
			for _, want := range []string{"ABSENT", tc.sink, "Refusing"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %q so the reader can act on it; got:\n%v", want, err)
				}
			}
		})
	}
}

// TestDerivedGateSinksAreAllPresent is the positive half: every sink this
// derivation declares must actually be in the graph today. Its failure means
// either a primitive moved (fix the list) or the graph lost one (do not trust
// any derived result until you know which).
func TestDerivedGateSinksAreAllPresent(t *testing.T) {
	skipUnlessFullSuite(t)

	liveDerivation(t)
}

// TestDerivedGateDivergencesAreAllRuledOn asserts the property the whole design
// rests on: the derived answer is never adopted silently. Every divergence
// carries a baseline entry whose reason says what derives, what is declared,
// and why — and no entry may be a TODO.
//
// TestSourceAuditGate already fails on an unbaselined finding. This adds the
// half that one cannot see: that the reason is SUBSTANTIVE rather than a
// placeholder somebody added to go green.
func TestDerivedGateDivergencesAreAllRuledOn(t *testing.T) {
	skipUnlessFullSuite(t)

	divs := liveDerivation(t)
	if len(divs) == 0 {
		t.Fatal("no divergences at all: the derivation is probably not seeing the tree — a vacuous pass")
	}

	base, err := LoadBaseline(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, e := range base.Entries {
		reasons[e.ID] = e.Reason
	}

	for _, d := range divs {
		id := KindDerivedGateDivergence + " " + d.ID()
		reason, ok := reasons[id]
		if !ok {
			t.Errorf("%s has no baseline entry: a divergence must be RULED ON in writing, never adopted silently", id)
			continue
		}
		if strings.Contains(reason, "TODO") || strings.Contains(reason, "UNTRIAGED") {
			t.Errorf("%s carries a placeholder reason, which is not a ruling: %q", id, reason)
		}
		// A ruling names the direction it is ruling on. Anything shorter is a
		// shrug, and a shrug in this file becomes a gate somebody removed.
		if len(reason) < 200 {
			t.Errorf("%s reason is %d bytes — too short to say what derives, what is declared, and why the declared answer wins", id, len(reason))
		}
	}
}

// TestDerivedGateWitnessesEveryPositive pins the evidence requirement: a
// divergence that claims a command REACHES a sink must be able to show the
// path. Without it, a false positive from an analysis imprecision is
// indistinguishable from a real ungated writer — which is exactly the call
// vp_collect_wrap_state and vp_refresh_index required, in opposite directions.
func TestDerivedGateWitnessesEveryPositive(t *testing.T) {
	skipUnlessFullSuite(t)

	divs := liveDerivation(t)
	for _, d := range divs {
		if !d.Derived {
			continue
		}
		if len(d.Witness) < 2 {
			t.Errorf("%s derives TRUE but carries no witness path; a verdict nobody can inspect is not evidence", d.ID())
			continue
		}
		last := d.Witness[len(d.Witness)-1]
		found := false
		for _, s := range vaultWriteFunnelSinks {
			if strings.HasSuffix(s, last) || strings.Contains(s, strings.TrimPrefix(last, "[closure] ")) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s witness does not END at a declared sink: %v", d.ID(), d.Witness)
		}
	}
}

// TestDerivedGateSeesEveryRegisteredTool guards the root set against silent
// shrinkage. The MCP roots are discovered by finding mcp.Tool composite
// literals with a literal Name, so a constructor that stopped matching would
// simply vanish from the derivation — and a tool nobody derives is a tool
// nobody can catch. The surface golden is the independent census.
func TestDerivedGateSeesEveryRegisteredTool(t *testing.T) {
	skipUnlessFullSuite(t)

	goldenPath := filepath.Join(moduleDirForTest(t), "internal", "mcp", "tool_surface.golden.json")
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}

	ctors := toolConstructors(livePackages(t))

	seen := map[string]bool{}
	for _, tool := range ctors {
		seen[tool] = true
	}
	for _, g := range golden.Tools {
		if !seen[g.Name] {
			t.Errorf("registered tool %q has no discoverable constructor: the derivation cannot see it, so it cannot report a divergence for it", g.Name)
		}
	}
	if len(ctors) != len(golden.Tools) {
		t.Errorf("constructors found = %d, registered tools = %d — the root set and the surface disagree", len(ctors), len(golden.Tools))
	}
}

// TestDerivedGateSeesEveryRegisteredCommand is the CLI half of the same guard.
// Every command the registry registers must be a root; a command the derivation
// never looks at cannot diverge, and would go quietly ungated.
func TestDerivedGateSeesEveryRegisteredCommand(t *testing.T) {
	skipUnlessFullSuite(t)

	declared := declaredCLIGate(livePackages(t))
	if len(declared) < 60 {
		t.Fatalf("only %d registered commands parsed out of cmd/vp/commands.go — the parse is probably broken, and a shrunken root set passes vacuously", len(declared))
	}
	// The mutates() unwrap must survive a constructor that takes arguments —
	// the regex that missed `mutates(cmdArchiveCreate(info))` reported a
	// security finding that did not exist.
	if gated, ok := declared["cmdArchiveCreate"]; !ok || !gated {
		t.Errorf("cmdArchiveCreate parsed as gated=%v present=%v; it is written mutates(cmdArchiveCreate(info)) and IS gated", gated, ok)
	}
}
