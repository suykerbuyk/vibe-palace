// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// This file DERIVES the surface-gate predicate — *does this command or tool
// reach a vault-write sink?* — and reports every disagreement with the
// hand-declared answer. It implements ADR-010's decision
// (doc/adr/010-surface-gate-at-the-dispatch-seam.md) and the plan task
// `derive-the-mutating-predicate-from-funnel-reachability`, step 3 of the
// ordering ruled there.
//
// 🔴 IT DERIVES; IT DOES NOT DECIDE. The derived answer never becomes the flag.
// ADR-010's wording — "the boolean ... becomes a fact a check derives" — is
// ambiguous between GENERATING the flag and PINNING it, and generating is
// wrong. Measured on this tree, the derivation disagrees with the declared
// answer in BOTH directions, and neither direction is a mistake in the
// declaration:
//
//   - It UNDER-derives, because two accepted rulings interact. The git channel
//     is exempt from the write funnel BY VERB (a merge is N files in one
//     operation, which no per-path primitive models), so a tool whose only
//     vault writes are git-mediated reaches no sink and derives non-mutating —
//     correctly, by the funnel's own definition. `vp_vault_sync`,
//     `vp_vault_tidy` and `vault commit` are exactly that shape. Adopting the
//     derived answer would SILENTLY UNGATE them. Separately, `vp config sync`
//     and `vp_init` write with bare os.WriteFile/os.Remove/os.Rename outside
//     the funnel entirely, and would be ungated for a different reason.
//   - It OVER-derives, through two known imprecisions of this analysis — a
//     shared generic instantiation and closure attribution, both documented
//     below — so adopting the derived answer would also gate reads.
//
// So the derived answer is EVIDENCE, and a divergence from it is a question
// that a human must answer in writing. Every divergence this file reports is
// accepted only through baseline.json, with a reason naming what derives, what
// is declared, and why the declared answer wins. That makes each one a durable,
// reviewable artifact instead of a silent behaviour change — and it makes a NEW
// divergence, which is the thing worth catching, fail the build.

// KindDerivedGateDivergence: the derived surface-gate predicate disagrees with
// the hand-declared one for a CLI command or an MCP tool. Not a defect on its
// own — see the file comment. It is a question that must be answered in
// writing, or a real gating mistake.
const KindDerivedGateDivergence = "derived-gate-divergence"

// modulePath is this module's import prefix. The SSA build is scoped to it (see
// buildScoped) and reachability is only ever computed over its packages.
const modulePath = "github.com/suykerbuyk/vibe-palace"

// vaultWriteFunnelSinks is the funnel's five primitives — the F1/F2/F4 families
// every vault CONTENT mutation is meant to route through. Reaching one of these
// is what "mutates the vault" MEANS for this derivation.
//
// 🔴 THIS IS NOT THE SAME SET AS vaultMutationSinks (ungated_writer.go), AND
// THE DIFFERENCE IS LOAD-BEARING — it is why that rule is kept rather than
// deleted. Two vault mutations are structurally invisible here:
// surface.WriteFormat, which performs its own temp-write-and-rename because
// internal/surface cannot import internal/atomicfile (atomicfile imports
// surface — a real cycle, closable by no placement list), and
// storage.DeleteDrawer, which rewrites its JSONL in place through
// os.OpenFile(O_RDWR). Both are anchored by that rule and by nothing here.
//
// The git channel is deliberately absent and is NOT an oversight: it is exempt
// from the funnel by verb. Never "fix" that by adding a git function here —
// read the ruling on internal/storage's vault_write_funnel doc comment first.
var vaultWriteFunnelSinks = []string{
	// F1 — the whole-file replace primitive, and its streaming half.
	modulePath + "/internal/atomicfile.Write",
	modulePath + "/internal/atomicfile.WriteStream",
	// F2 — the raw removal/rename sink beneath vaultfs.Delete / vaultfs.Move.
	// Anchored on the SINK, never on bare os.Remove / os.Rename, which appear
	// throughout the tree on temp files, locks and project-local paths.
	modulePath + "/internal/vaultfs.RemoveNoLock",
	modulePath + "/internal/vaultfs.RenameNoLock",
	// F4 — the append primitive that owns its stamp. Written with its receiver
	// because that is how ssa.Function.String renders a method, and a lookup
	// keyed on the bare name silently finds nothing; see assertSinksPresent.
	"(*" + modulePath + "/internal/storage.Vault).appendUnderLock",
}

// GateDivergence is one command or tool whose derived predicate disagrees with
// its declared one, carrying the evidence a human needs to rule on it.
type GateDivergence struct {
	Surface  string   // "cli" or "mcp"
	Name     string   // command constructor (cmdVaultTidy) or tool name (vp_vault_sync)
	Derived  bool     // does it reach a funnel sink?
	Declared bool     // is it gated today?
	Witness  []string // root -> sink path when Derived; nil otherwise

	// ctor is the SSA root this verdict was computed from: the command
	// constructor for cli, the tool constructor for mcp. Unexported because it
	// is an implementation handle, not part of the finding's identity — the
	// identity is Surface.Name, which is what a human rules on.
	ctor string
}

// ID is the baseline key: stable across a witness path changing, because the
// path is evidence and the identity is the disagreement.
func (d GateDivergence) ID() string { return d.Surface + "." + d.Name }

// RunModule reports the TYPE-CHECKED findings over a Go module: today, the
// derived-gate divergences. It is the module-scoped sibling of Run.
//
// Kept separate from Run because the input contract differs — Run analyses a
// directory of files and is happy with a synthetic fixture tree; this needs a
// real module with a resolvable dependency graph, because reachability through
// interface dispatch is not recoverable syntactically. That is the whole reason
// this rule exists: `vp config sync` reaches its writer through
// []reconcile.Reconciler, and no AST walk recovers the receiver.
//
// dir may be any directory inside the module; the module root is derived from
// it. It ERRORS rather than returning partial findings when its sinks are
// absent or the tree does not type-check, because a reachability result
// computed from a broken graph is clean in exactly the same shape as a correct
// one.
func RunModule(dir string) ([]Finding, error) {
	moduleDir, err := moduleRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("derived gate: %w", err)
	}

	divs, err := deriveGate(moduleDir)
	if err != nil {
		return nil, err
	}

	findings := make([]Finding, 0, len(divs))
	for _, d := range divs {
		findings = append(findings, Finding{
			Kind:   KindDerivedGateDivergence,
			Symbol: d.ID(),
			Pos:    moduleDir,
			Detail: d.detail(),
		})
	}
	return findings, nil
}

func (d GateDivergence) detail() string {
	var b strings.Builder
	verb := map[bool]string{true: "reaches a funnel sink", false: "reaches NO funnel sink"}
	gate := map[bool]string{true: "gated", false: "ungated"}
	fmt.Fprintf(&b, "derived %v (%s) but declared %v (%s)",
		d.Derived, verb[d.Derived], d.Declared, gate[d.Declared])
	if len(d.Witness) > 0 {
		b.WriteString("\n    witness: ")
		b.WriteString(strings.Join(d.Witness, " -> "))
	}
	return b.String()
}

// moduleRoot walks up from dir until it finds the go.mod that owns it.
//
// Derived rather than passed, so this rule cannot be handed a root that does
// not match the tree the other rules read — and it FAILS rather than guessing,
// because a derivation pointed at the wrong tree reports a clean result in
// exactly the same shape as a correct one.
func moduleRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		abs = parent
	}
}

// loadModulePackages type-checks this module's packages.
//
// It REFUSES on any package error rather than analysing what loaded. A package
// that failed to type-check contributes no call edges, so its commands derive
// "reaches nothing" — a clean result produced by blindness, in the same shape
// as a correct one. That is the same failure mode assertSinksPresent exists to
// prevent, one layer earlier.
func loadModulePackages(moduleDir string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		// NeedDeps + NeedTypes is what makes this a TYPE-CHECKED graph rather
		// than an AST guess. go/packages shells out to the go toolchain and so
		// needs a writable GOCACHE; under `go test` that is already true.
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		Dir: moduleDir,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("derived gate: load packages: %w", err)
	}
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, p.PkgPath+": "+e.Error())
		}
	})
	if len(loadErrs) > 0 {
		sort.Strings(loadErrs)
		return nil, fmt.Errorf("derived gate: %d package load error(s), refusing to derive from a partial tree:\n  %s",
			len(loadErrs), strings.Join(loadErrs, "\n  "))
	}
	return pkgs, nil
}

// deriveGate is the analysis proper. Split from the rule so a test can drive it
// directly with a doctored sink list and prove the sink assertion fires.
func deriveGate(moduleDir string) ([]GateDivergence, error) {
	return deriveGateWithSinks(moduleDir, vaultWriteFunnelSinks)
}

func deriveGateWithSinks(moduleDir string, sinks []string) ([]GateDivergence, error) {
	pkgs, err := loadModulePackages(moduleDir)
	if err != nil {
		return nil, err
	}
	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	buildScoped(ssaPkgs)

	allFns := ssautil.AllFunctions(prog)
	graph := vta.CallGraph(allFns, cha.CallGraph(prog))

	if err := assertSinksPresent(graph, sinks); err != nil {
		return nil, err
	}

	sinkSet := make(map[string]bool, len(sinks))
	for _, s := range sinks {
		sinkSet[s] = true
	}
	reach := reachesSink(graph, allFns, sinkSet)

	byName := make(map[string]*ssa.Function, len(allFns))
	for fn := range allFns {
		byName[fn.String()] = fn
	}

	var out []GateDivergence
	for _, d := range diffCLI(pkgs, byName, reach) {
		out = append(out, d)
	}
	for _, d := range diffMCP(pkgs, byName, reach) {
		out = append(out, d)
	}
	for i := range out {
		if out[i].Derived {
			root := byName[out[i].rootSymbol()]
			out[i].Witness = witnessPath(graph, allFns, root, sinkSet)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}

func (d GateDivergence) rootSymbol() string {
	if d.Surface == "cli" {
		return modulePath + "/cmd/vp." + d.Name
	}
	return modulePath + "/internal/tools." + d.ctor
}

// buildScoped builds ONLY this module's packages.
//
// 🔴 MANDATORY, NOT AN OPTIMISATION. x/tools v0.43.0's SSA builder PANICS on
// Go 1.27's standard library — `unexpected expr: *ast.KeyValueExpr` at
// ssa/builder.go:967, reproduced on this tree with go1.27.0 — if you call
// prog.Build(), which builds every dependency. Building only module-prefixed
// packages completes cleanly (52 packages, 0 errors, ~100 ms) and is also
// exactly the scope this derivation cares about: a sink is ours, and a root is
// ours. Do not "simplify" this to prog.Build().
func buildScoped(ssaPkgs []*ssa.Package) {
	for _, p := range ssaPkgs {
		if p == nil || p.Pkg == nil {
			continue
		}
		if strings.HasPrefix(p.Pkg.Path(), modulePath) {
			p.Build()
		}
	}
}

// assertSinksPresent is the non-negotiable guard.
//
// 🔴 A CALL GRAPH MISSING A SINK REPORTS EVERY COMMAND THAT REACHES IT AS
// CLEAN, IN EXACTLY THE SAME SHAPE AS A CORRECT RESULT. An earlier probe of
// this analysis found 4 of 5 sinks and was believed only because the count was
// printed. So: if any declared sink is absent from the graph, this returns an
// error and NO result is emitted. Never downgrade it to a warning, and never
// let a caller proceed on a partial sink set — a shrunken sink set silently
// shrinks the derived mutating set, which is the vacuity failure the funnel
// rule's own floors already defend against.
//
// This is also the guard that catches the likeliest cause of that earlier
// result: a sink keyed by the wrong NAME. ssa.Function.String renders a method
// as "(*pkg.Type).method", so a sink written "storage.appendUnderLock" is
// absent from a graph that contains it — indistinguishable, without this
// assertion, from a graph that dropped it.
func assertSinksPresent(g *callgraph.Graph, sinks []string) error {
	present := make(map[string]bool, len(g.Nodes))
	for fn := range g.Nodes {
		if fn != nil {
			present[fn.String()] = true
		}
	}
	var missing []string
	for _, s := range sinks {
		if !present[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf(
		"derived gate: %d of %d declared vault-write sinks are ABSENT from the call graph:\n  %s\n"+
			"Refusing to emit a result. A graph missing a sink reports every command that reaches it as\n"+
			"clean, in the same shape as a correct answer. Check the sink's spelling first — a method is\n"+
			"rendered \"(*pkg.Type).method\" — then whether it was renamed, deleted, or is no longer built.",
		len(missing), len(sinks), strings.Join(missing, "\n  "))
}

// reachesSink computes, for every function, whether it reaches any sink over
// call edges UNION parent -> anonymous-child edges.
//
// 🔴 THE SECOND EDGE KIND IS NOT A CALL, AND IT IS REQUIRED. Every MCP handler
// and every CLI Run is a CLOSURE stored in a struct literal and invoked later
// through the registry, so pure call-edge reachability sees nothing at all
// past the constructor. Attributing a closure's calls to the function that
// BUILDS it is what makes a tool analysable.
//
// It is also this analysis's largest imprecision, and the two are the same
// mechanism: a command that REGISTERS tools (vp check counting them, vp mcp and
// vp mcp serve building the server) is credited with everything every handler
// does, because registration and invocation are indistinguishable once the
// closure is attributed to its builder. All three divergences that produces are
// recorded in baseline.json rather than papered over.
func reachesSink(g *callgraph.Graph, allFns map[*ssa.Function]bool, sinkSet map[string]bool) map[*ssa.Function]bool {
	rev := map[*ssa.Function][]*ssa.Function{}
	for fn, node := range g.Nodes {
		if fn == nil {
			continue
		}
		for _, e := range node.Out {
			if e.Callee != nil && e.Callee.Func != nil {
				rev[e.Callee.Func] = append(rev[e.Callee.Func], fn)
			}
		}
	}
	for fn := range allFns {
		if p := fn.Parent(); p != nil {
			rev[fn] = append(rev[fn], p)
		}
	}

	out := map[*ssa.Function]bool{}
	var stack []*ssa.Function
	for fn := range allFns {
		if sinkSet[fn.String()] {
			out[fn] = true
			stack = append(stack, fn)
		}
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, pred := range rev[cur] {
			if !out[pred] {
				out[pred] = true
				stack = append(stack, pred)
			}
		}
	}
	return out
}

// witnessPath returns a shortest root -> sink path, labelling each hop as a
// call or a closure attribution.
//
// A derived verdict nobody can inspect is not evidence — it is an assertion
// with a call graph behind it. The path is what lets a reviewer tell a real
// reachability (vp_refresh_index -> backfillFromArchives -> IndexTranscript ->
// AppendDrawers -> appendUnderLock) from an artifact of the analysis
// (vp_collect_wrap_state, which reaches a remover only through a generic
// instantiation shared with an unrelated caller).
func witnessPath(g *callgraph.Graph, allFns map[*ssa.Function]bool, root *ssa.Function, sinkSet map[string]bool) []string {
	if root == nil {
		return nil
	}
	type edge struct {
		to   *ssa.Function
		kind string
	}
	fwd := map[*ssa.Function][]edge{}
	for fn, node := range g.Nodes {
		if fn == nil {
			continue
		}
		for _, e := range node.Out {
			if e.Callee != nil && e.Callee.Func != nil {
				fwd[fn] = append(fwd[fn], edge{e.Callee.Func, "calls"})
			}
		}
	}
	for fn := range allFns {
		if p := fn.Parent(); p != nil {
			fwd[p] = append(fwd[p], edge{fn, "builds-closure"})
		}
	}

	prev := map[*ssa.Function]*ssa.Function{}
	kind := map[*ssa.Function]string{}
	seen := map[*ssa.Function]bool{root: true}
	queue := []*ssa.Function{root}
	var hit *ssa.Function
	for len(queue) > 0 && hit == nil {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range fwd[cur] {
			if seen[e.to] {
				continue
			}
			seen[e.to] = true
			prev[e.to] = cur
			kind[e.to] = e.kind
			if sinkSet[e.to.String()] {
				hit = e.to
				break
			}
			queue = append(queue, e.to)
		}
	}
	if hit == nil {
		return nil
	}
	var path []string
	for n := hit; n != nil; n = prev[n] {
		label := strings.ReplaceAll(n.String(), modulePath+"/", "")
		if k, ok := kind[n]; ok && k == "builds-closure" {
			label = "[closure] " + label
		}
		path = append([]string{label}, path...)
		if n == root {
			break
		}
	}
	return path
}
