// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"strings"
	"testing"
)

// The rule's healthy steady state is zero findings, so a silent run over the
// real tree proves nothing on its own. Every fixture below is a named break
// asserted to FIRE, beside the shapes that must stay silent.
//
// 🔴 EVERY FIXTURE NESTS THE DEFER INSIDE THE `Run:` LITERAL, exactly as
// cmd/vp/cmd_search.go does. A flat `func cmdSearch() { defer eng.Close() }`
// fixture would not be acceptance: it would go red on the rename anchor while
// the real defers, nested one func literal deep, stayed invisible. The nesting
// is the thing being tested, because bindingScopes does not enumerate a literal
// nested inside a FuncDecl as its own scope.

// oneshotClean is the production shape with neither defer.
const oneshotClean = `package main

func cmdSearch() *cli.Command {
	return &cli.Command{
		Run: func(args []string) int {
			emb := embedder.NewLazy(f)
			eng := search.NewEngine(emb, vault, cfg)
			_ = eng
			return 0
		},
	}
}
`

// oneshotEngArm restores `defer eng.Close()` — the statement whose removal is
// the whole point of the change.
const oneshotEngArm = `package main

func cmdSearch() *cli.Command {
	return &cli.Command{
		Run: func(args []string) int {
			emb := embedder.NewLazy(f)
			eng := search.NewEngine(emb, vault, cfg)
			defer eng.Close()
			return 0
		},
	}
}
`

// oneshotEmbArm restores `defer emb.Close()` — the other half of the pair,
// which must fire on its own. One mutation covering both arms is exactly the
// single-arm gap search-build-budget-suite-has-two-arms-no-mutation-can-redden
// records.
const oneshotEmbArm = `package main

func cmdSearch() *cli.Command {
	return &cli.Command{
		Run: func(args []string) int {
			emb := embedder.NewLazy(f)
			eng := search.NewEngine(emb, vault, cfg)
			defer emb.Close()
			return 0
		},
	}
}
`

// oneshotOtherFunc: the same two defers in a function this rule does not guard.
// `vp drain archives` legitimately defers eng.Close() on an engine of its own.
const oneshotOtherFunc = `package main

func cmdSearch() *cli.Command {
	return &cli.Command{
		Run: func(args []string) int {
			emb := embedder.NewLazy(f)
			eng := search.NewEngine(emb, vault, cfg)
			_ = eng
			return 0
		},
	}
}

func cmdDrainArchives() int {
	emb := embedder.NewLazy(f)
	eng := search.NewEngine(emb, v, cfg)
	defer eng.Close()
	defer emb.Close()
	return 0
}
`

// oneshotEngShadow: a nested closure re-binds the NAME `eng` to something that
// is not the engine. Asserted SILENT — this and oneshotEmbShadow are the only
// things that prove the shadow set exists.
const oneshotEngShadow = `package main

func cmdSearch() *cli.Command {
	return &cli.Command{
		Run: func(args []string) int {
			emb := embedder.NewLazy(f)
			eng := search.NewEngine(emb, vault, cfg)
			_ = eng
			walk(func() {
				eng := openLedger()
				defer eng.Close()
			})
			return 0
		},
	}
}
`

// oneshotEmbShadow: the same, shadowing `emb` through a closure PARAMETER.
const oneshotEmbShadow = `package main

func cmdSearch() *cli.Command {
	return &cli.Command{
		Run: func(args []string) int {
			emb := embedder.NewLazy(f)
			eng := search.NewEngine(emb, vault, cfg)
			_ = eng
			forEach(func(emb io.Closer) {
				defer emb.Close()
			})
			return 0
		},
	}
}
`

// oneshotDeferBeforeShadow: the defer comes BEFORE the declaration that
// shadows the name, so it still refers to the OUTER `eng` — the engine. Legal
// Go, forbidden here, and asserted to FIRE.
//
// 🔴 This is the false NEGATIVE the first implementation had: it collected a
// block's declarations before scanning the block, so the name was dropped
// before the scan ever reached this defer. The two CLEAN shadow fixtures above
// stayed green through that bug, which is why this one has to exist beside
// them rather than instead of them.
const oneshotDeferBeforeShadow = `package main

func cmdSearch() *cli.Command {
	return &cli.Command{
		Run: func(args []string) int {
			emb := embedder.NewLazy(f)
			eng := search.NewEngine(emb, vault, cfg)
			_ = eng
			walk(func() {
				defer eng.Close()
				eng := openLedger()
				_ = eng
			})
			return 0
		},
	}
}
`

// oneshotEmbDeferBeforeShadow: the same, on the embedder arm, so neither arm's
// source-order handling can be satisfied by the other's.
const oneshotEmbDeferBeforeShadow = `package main

func cmdSearch() *cli.Command {
	return &cli.Command{
		Run: func(args []string) int {
			emb := embedder.NewLazy(f)
			eng := search.NewEngine(emb, vault, cfg)
			_ = eng
			walk(func() {
				defer emb.Close()
				emb := openCache()
				_ = emb
			})
			return 0
		},
	}
}
`

func oneshotFindings(t *testing.T, src string) []string {
	t.Helper()
	findings, err := Run(writeFixtureTree(t, map[string]string{"main": src}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindOneshotSearchEngineClose {
			out = append(out, f.Symbol)
		}
	}
	return out
}

// The negative control.
func TestOneshotSearchCloseIsSilentOnTheSanctionedShape(t *testing.T) {
	if got := oneshotFindings(t, oneshotClean); len(got) != 0 {
		t.Errorf("the sanctioned shape produced findings: %v", got)
	}
}

// MUTATION 1: `defer eng.Close()` restored.
func TestOneshotSearchCloseFlagsTheDeferredEngineClose(t *testing.T) {
	got := oneshotFindings(t, oneshotEngArm)
	if !slices.Contains(got, "main.cmdSearch/defer eng.Close") {
		t.Errorf("a deferred engine Close nested in the Run literal was not flagged: %v", got)
	}
	if slices.Contains(got, "main.cmdSearch/defer emb.Close") {
		t.Errorf("the absent embedder Close was flagged too: %v", got)
	}
}

// MUTATION 2: `defer emb.Close()` restored, separately.
func TestOneshotSearchCloseFlagsTheDeferredEmbedderClose(t *testing.T) {
	got := oneshotFindings(t, oneshotEmbArm)
	if !slices.Contains(got, "main.cmdSearch/defer emb.Close") {
		t.Errorf("a deferred embedder Close nested in the Run literal was not flagged: %v", got)
	}
	if slices.Contains(got, "main.cmdSearch/defer eng.Close") {
		t.Errorf("the absent engine Close was flagged too: %v", got)
	}
}

// The rule guards cmdSearch, not every engine in the binary.
func TestOneshotSearchCloseIgnoresOtherFunctions(t *testing.T) {
	if got := oneshotFindings(t, oneshotOtherFunc); len(got) != 0 {
		t.Errorf("defers in a function this rule does not guard were flagged: %v", got)
	}
}

// MUTATION 5's fixtures: the shadow set must keep these two silent.
func TestOneshotSearchCloseIsSilentOnAShadowedName(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"nested := re-binds eng", oneshotEngShadow},
		{"closure parameter re-binds emb", oneshotEmbShadow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := oneshotFindings(t, tc.src); len(got) != 0 {
				t.Errorf("a shadowed name was mistaken for the engine/embedder: %v", got)
			}
		})
	}
}

// A defer that PRECEDES the declaration shadowing its name still refers to the
// outer value, so it must FIRE. Shadowing is positional in Go; a rule that
// drops a name for the whole block gets this wrong in the silent direction.
func TestOneshotSearchCloseFlagsADeferBeforeItsShadow(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"eng arm", oneshotDeferBeforeShadow, "main.cmdSearch/defer eng.Close"},
		{"emb arm", oneshotEmbDeferBeforeShadow, "main.cmdSearch/defer emb.Close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := oneshotFindings(t, tc.src)
			if !slices.Contains(got, tc.want) {
				t.Errorf("a defer preceding its shadowing declaration was not flagged: %v", got)
			}
		})
	}
}

// MUTATION 3, the rename ANCHOR: a guarded scope that no longer exists means
// the rule guards a name nothing carries. It is NOT a substitute for mutations
// 1 and 2 — it fires whether or not the rule ever looked inside Run.
func TestOneshotSearchCloseAnchorFiresWhenTheScopeIsMissing(t *testing.T) {
	renamed := strings.Replace(oneshotClean, "func cmdSearch(", "func cmdFind(", 1)
	got := oneshotFindings(t, renamed)
	if !slices.Contains(got, "sourceaudit.oneshotSearchNoEngineClose/ABSENT/main.cmdSearch") {
		t.Errorf("renaming the guarded scope did not fire the ABSENT anchor: %v", got)
	}
}

// MUTATION 4, the constructor ANCHOR: moving search.NewEngine into a helper
// binds no identifiers, so the rule would guard nothing while the rename anchor
// stayed green.
func TestOneshotSearchCloseAnchorFiresWhenTheConstructorMoves(t *testing.T) {
	moved := `package main

func buildSearchEngine() (*search.Engine, embedder.Embedder) {
	emb := embedder.NewLazy(f)
	return search.NewEngine(emb, vault, cfg), emb
}

func cmdSearch() *cli.Command {
	return &cli.Command{
		Run: func(args []string) int {
			eng, emb := buildSearchEngine()
			defer eng.Close()
			defer emb.Close()
			return 0
		},
	}
}
`
	got := oneshotFindings(t, moved)
	if !slices.Contains(got, "sourceaudit.oneshotSearchNoEngineClose/NOCTOR/main.cmdSearch") {
		t.Errorf("moving search.NewEngine out of the guarded scope did not fire the NOCTOR anchor: %v", got)
	}
}
