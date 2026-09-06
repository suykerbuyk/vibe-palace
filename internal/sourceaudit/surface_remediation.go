// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

// surfaceRemediation reports a SECOND COPY of the surface-mismatch remediation
// prose, and a consumer of *surface.IncompatibleError that never reaches the
// remedy at all.
//
// # The defect, and why nothing else could see it
//
// When a vault is stamped ahead of a binary, that host is STRANDED: every
// mutating tool is refused, and it cannot pull its way out — pulling raises the
// vault floor further, and the fix is a new binary rather than new vault content.
// The MCP server it is talking to is the thing that is out of date. Its ONLY
// route back is text that names the command to run.
//
// The producer was never the problem. (*surface.IncompatibleError).Error() has
// always rendered the full remedy. The gap was entirely in CONSUMERS that
// re-formatted the error from its struct fields instead of passing its text
// through, and that failure has a property which made it invisible for as long as
// it existed: the result is ACCURATE, WELL-FORMED, AND USELESS. It reads like a
// proper error. No test asserting on error TYPES can tell the difference, and the
// tests that did assert on the remediation substrings were pointed at a
// consumer's own literals, so deleting the remediation from the producer left
// them green.
//
// Two shapes, two halves:
//
//	(a) DIVERGENCE — the prose exists in more than one place and the copies rot
//	    apart. This is not hypothetical: internal/check/surface.go carried a
//	    hand-written copy that had ALREADY drifted (`action:` → `Upgrade:`,
//	    `<original-command>` → `<command>`, the framing line dropped entirely)
//	    with nothing pinning it, because the only test near it used a synthetic
//	    fixture.
//
//	(b) OMISSION — a consumer binds the error, renders its own message, and
//	    simply never reaches the remedy. internal/wrapstate/preflight.go did
//	    exactly this on the /vpc-wrap halt path, where ok:false stops the wrap and
//	    that one string is the whole of what the agent can relay.
//
// # Why this is a source rule and not a type

// Half (a) is solvable in Go — one exported Remediation() method, and the
// divergence shape is structurally impossible. Half (a) is therefore mostly a
// RATCHET here: it catches someone re-typing the prose rather than calling.
//
// Half (b) is NOT solvable in Go. A consumer can always decline to call a method,
// and the language cannot force a return value to be used. Unexporting the type
// does not help and does not build — errors.As requires a target nameable at the
// call site, and every offender bound the error successfully and then read its
// FIELDS. So enforcement has to be a source rule, and this package is the machine
// this repository already has for that: an AST audit run as a test gate with a
// shrink-only baseline.
//
// This is DETECTION, not impossibility. It is the strongest guarantee Go permits
// for the omission shape, and — running from Run() over repoRoots — it fires on
// consumers that do not exist yet, in packages that do not exist yet. A rule
// scoped to a list of consumers someone remembered is the defect, not the fix;
// that is the same lesson vaultWriteFunnel records from replacing three
// package-local os.ReadDir(".") pins.
//
// # What counts as "reaching the remedy"
//
// A function satisfies (b) by calling Remediation() or Error() on the bound
// identifier, OR Error() on the identifier it passed to errors.As — the latter
// because internal/surface/gate.go legitimately prints `err.Error()` after
// binding, and err's chain is what it just proved contains the error.
//
// Reading the numeric fields is deliberately NOT enough. That is precisely what
// the preflight did.
func surfaceRemediation(files []file) []Finding {
	byKey := map[string]*funnelBreach{}
	var order []string

	stats := surfaceStats{probesInProducer: map[string]int{}}

	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		// 🔴 THE MATCHER CANNOT EXPRESS ITS OWN PATTERN WITHOUT CONTAINING IT.
		// remediationProbes below are, necessarily, string literals holding the
		// exact prose this rule hunts for, so half (a) flags itself on its first
		// run. The exemption is scoped to THIS ONE FILE rather than to the
		// package, following vaultWriteFunnel's line: it skips internal/atomicfile
		// because the primitive cannot route through itself, and pointedly does
		// NOT skip internal/vaultfs, because a package-wide skip blinds the rule
		// to a new offender added anywhere in it. A copy of the prose pasted into
		// any OTHER sourceaudit file is still a finding.
		if pkg == "sourceaudit" && filepath.Base(f.path) == "surface_remediation.go" {
			continue
		}
		producer := isRemediationProducer(pkg, f.path)

		// ---- (a) remediation prose in a string literal outside the producer.
		ast.Inspect(f.ast, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind.String() != "STRING" {
				return true
			}
			for _, probe := range remediationProbes {
				if !strings.Contains(lit.Value, probe) {
					continue
				}
				if producer {
					// The producer is where this prose belongs. Counting it here
					// is the anti-vacuity floor: see below.
					stats.probesInProducer[probe]++
					continue
				}
				stats.strayLiterals++
				surfaceAdd(byKey, &order, pkg+"."+enclosingFuncName(f, lit.Pos()), f, lit.Pos(), fmt.Sprintf(
					"a string literal repeats the surface remediation prose %q — %s", probe, remediationDriftHazard))
			}
			return true
		})

		// ---- (b) a consumer that binds the error and never reaches the remedy.
		for _, s := range bindingScopes(f) {
			bindings := s.bindings()
			stats.bindings += len(bindings)
			for _, b := range bindings {
				if s.reachesRemedy(b) {
					continue
				}
				surfaceAdd(byKey, &order, pkg+"."+s.name, f, b.pos, fmt.Sprintf(
					"binds *surface.IncompatibleError as %q via errors.As but never reaches "+
						".Remediation() or .Error() on the path where the binding SUCCEEDED — %s",
					b.name, remediationOmissionHazard))
			}
		}
	}

	var out []Finding
	for _, key := range order {
		b := byKey[key]
		sort.Strings(b.details)
		out = append(out, Finding{
			Kind:   KindSurfaceRemediationLost,
			Symbol: key,
			Pos:    b.pos,
			Detail: strings.Join(b.details, " | "),
		})
	}

	// 🔴 ANTI-VACUITY. A no-result reads as green, and BOTH halves of this rule
	// can go silently inert without a single test noticing:
	//
	//   - Half (a) is keyed on PROSE. Rename the env var or reword the upgrade
	//     line and the probes stop matching ANYWHERE, including in the producer.
	//     The rule then reports nothing forever while a fresh copy of the new
	//     prose can be pasted anywhere it likes.
	//   - Half (b) is keyed on the TYPE NAME. Rename IncompatibleError and every
	//     binding stops being recognised. This is the exact scenario the rulings
	//     on this task called out: a rename must not turn the gate permanently
	//     green.
	//
	// So vacuity is reported as a FINDING, exactly as vaultWriteFunnel does — it
	// is not in the baseline, so the gate fails on it like new debt. This rule has
	// no *testing.T, and the protection travels with Run() to every caller rather
	// than living in one test.
	//
	// Floors are DERIVED, not remembered. Re-derive the binding floor with:
	//
	//	grep -rnE 'var [a-zA-Z_]+ \*(surface\.)?IncompatibleError' --include='*.go' \
	//	  internal/ cmd/ | grep -v '_test.go' | grep -vE ':[0-9]+:\s*//'
	//
	// Both spellings, because both count: consumer packages write the qualified
	// form and internal/surface writes the bare one. The grep is a FLOOR CHECK, not
	// the rule's definition — the rule also recognises `:=`, type switches and type
	// assertions, so it may legitimately see MORE bindings than this grep prints.
	// The comparison below is `<`, so a gain never trips it.
	//
	// The trailing comment prune is load-bearing: THIS FILE's own doc comment above
	// spells the declaration out, and without it the grep counts the documentation
	// as a sixth binding — a re-derivation misled by the very comment telling it how
	// to re-derive.
	//
	// The producer floor needs no grep: EVERY probe must appear in the producer,
	// or the probe itself is stale.
	//
	// If you legitimately delete a consumer below the binding floor this goes red,
	// and that is intended: change the floor deliberately, in the same commit.
	// Never edit a floor to make a red gate pass — that is the one move that
	// converts the guard back into the vacuous pass it exists to prevent.
	const bindingFloor = 5

	for _, probe := range remediationProbes {
		if stats.probesInProducer[probe] == 0 {
			out = append(out, surfaceVacuous("probe:"+probe, fmt.Sprintf(
				"the remediation probe %q does not appear in a string literal in the PRODUCER "+
					"(internal/surface/version.go). Either the prose was reworded and this probe is "+
					"stale, or the walk is not seeing the producer. Half (a) of this rule is matching "+
					"nothing, so a passing verdict is vacuous", probe)))
		}
	}
	if stats.bindings < bindingFloor {
		out = append(out, surfaceVacuous("bindings", fmt.Sprintf(
			"resolved only %d binding(s) of *surface.IncompatibleError, expected at least "+
				"%d — half (b) of this rule is matching less than it did and a passing verdict may be "+
				"vacuous. TWO CAUSES, and they need different fixes. Either the type was RENAMED, in "+
				"which case no binding is recognised anywhere; or a consumer was REFACTORED INTO A "+
				"BINDING SPELLING THIS RULE DOES NOT RECOGNISE, in which case the other consumers are "+
				"still policed and one is silently exempt. The recognised spellings and the ones "+
				"deliberately left out are recorded under the task slug "+
				"%q — read that before investigating anything else, because the message you are "+
				"reading names VACUITY while the actual defect may be an unrecognised spelling",
			stats.bindings, bindingFloor, surfaceSpellingTask)))
	}

	return out
}

// surfaceStats are the anti-vacuity counters. probesInProducer proves the prose
// half is still matching real text; bindings proves the consumer half still
// recognises the type. strayLiterals is reported, not floored — zero is the
// CORRECT value for it, which is why it cannot serve as a liveness signal and
// probesInProducer must.
type surfaceStats struct {
	probesInProducer map[string]int
	strayLiterals    int
	bindings         int
}

// surfaceAdd accumulates every breach in one function so the finding is keyed by
// function rather than by line — the same choice vaultWriteFunnel makes, and for
// the same reason: Finding.ID() excludes Pos, so a per-line key would be unstable
// under edits that only move code.
func surfaceAdd(m map[string]*funnelBreach, order *[]string, key string, f file, pos token.Pos, detail string) {
	b, ok := m[key]
	if !ok {
		b = &funnelBreach{pos: posOf(f, pos)}
		m[key] = b
		*order = append(*order, key)
	}
	b.details = append(b.details, detail)
}

// surfaceVacuous reports that some half of this rule has gone inert.
//
// 🔴 EACH ONE GETS A DISTINCT SYMBOL, AND THAT IS NOT COSMETIC. Finding.ID() is
// Kind + Symbol, so a shared symbol makes every vacuity finding ONE id — and a
// single baseline entry carrying it would suppress ALL of them at once, across
// both halves of the rule, on the one class this rule says must never be
// baselined. The failure mode is silent and total: the gate goes green, stays
// green, and reports nothing forever.
//
// `which` names the specific instrument that stopped working, so the entries stay
// distinguishable and a reader can see WHICH half died.
func surfaceVacuous(which, detail string) Finding {
	return Finding{
		Kind:   KindSurfaceRemediationLost,
		Symbol: "sourceaudit.surfaceRemediation/VACUOUS/" + which,
		Pos:    "internal/sourceaudit/surface_remediation.go",
		Detail: detail + ". Do NOT baseline this entry — fix the rule.",
	}
}

// remediationProbes are the load-bearing fragments of the remediation. Both,
// because they are the two halves of the way out and a consumer that carried one
// and dropped the other would still strand a host that cannot upgrade right now.
//
// Deliberately NOT the whole rendered message: matching on a fragment survives
// re-wording around it, and matching on the whole would make the rule green the
// moment anyone reflowed a line.
var remediationProbes = []string{
	"VP_SURFACE_GATE=warn",
	"git pull && make install",
}

// surfaceSpellingTask is the record of which binding spellings this rule
// recognises, which were closed, and which were deliberately left. The vacuity
// message names it because a count below the floor reports VACUITY while the real
// cause may be a consumer refactored into a spelling the rule cannot see — and a
// reader who takes the message at face value investigates a rename that never
// happened.
const surfaceSpellingTask = "seven-binding-spellings-the-remediation-rule-cannot-see"

// isRemediationProducer identifies the ONE file allowed to hold this prose.
//
// Keyed on PACKAGE NAME + BASE NAME rather than on the walk-relative path,
// because Run's roots are relative ("..", "../../cmd") and a path-prefix match
// would silently stop working if a caller passed absolute roots — a rule that
// goes green when its caller changes shape is the failure mode this package is
// about.
func isRemediationProducer(pkg, path string) bool {
	return pkg == "surface" && filepath.Base(path) == "version.go"
}

// asBinding is one binding of *surface.IncompatibleError, together with the scope
// in which reaching the remedy actually counts.
type asBinding struct {
	name   string     // the bound identifier: `ie`
	source string     // the identifier the error came from, when it is one: `err`
	scope  []ast.Stmt // the SUCCESS path — nil means "the whole function body"
	pos    token.Pos  // where to anchor the finding
}

// bindingScope is ONE body the consumer half runs over, plus the two pieces of
// context that a body cannot supply for itself: the parameters that may already
// hold the binding target, and the local name(s) this FILE's `errors` import goes
// by.
//
// 🔴 IT CARRIES A PARAMS LIST AND A BODY RATHER THAN AN *ast.FuncDecl, AND THAT IS
// THE POINT. A FuncLit has no *ast.FuncDecl and never will, so keying the walk on
// one made a package-level `var X = func(...) {…}` structurally unreachable. The
// alternative — a second walk for literals — is the two-copies defect THIS RULE
// EXISTS TO DETECT, one layer out: two places deciding "is this errors.As" rot
// apart exactly the way two copies of the remediation prose did. One
// implementation, taking what it actually needs.
type bindingScope struct {
	name    string          // how a finding names this body: funcName, or the var's name
	params  *ast.FieldList  // may be nil
	body    *ast.BlockStmt  // never nil
	errPkgs map[string]bool // local names of the "errors" import in this file
}

// bindingScopes enumerates every body in a file that the consumer half must walk.
//
// FuncDecls, and the func literals held by PACKAGE-LEVEL declarations. Literals
// NESTED INSIDE a FuncDecl are deliberately NOT enumerated here: the statement
// walk already descends into them through their *ast.BlockStmt, with the enclosing
// function's success scoping intact, so enumerating them again would report the
// same binding twice under the same key. The package-level ones had no enclosing
// walk at all, which is the entire gap.
func bindingScopes(f file) []bindingScope {
	errPkgs := errorsPkgNames(f.ast)
	var out []bindingScope
	for _, decl := range f.ast.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			out = append(out, bindingScope{
				name:    funcName(d),
				params:  d.Type.Params,
				body:    d.Body,
				errPkgs: errPkgs,
			})

		case *ast.GenDecl:
			// `var X = func(err error) string { … }` — an *ast.ValueSpec whose value
			// is a literal. The finding is keyed by the VAR's name, because that is
			// what a reader greps for and what keeps the baseline diffable.
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, val := range vs.Values {
					name := filepath.Base(f.path)
					if i < len(vs.Names) {
						name = vs.Names[i].Name
					}
					for _, lit := range outermostFuncLits(val) {
						out = append(out, bindingScope{
							name:    name,
							params:  lit.Type.Params,
							body:    lit.Body,
							errPkgs: errPkgs,
						})
					}
				}
			}
		}
	}
	return out
}

// outermostFuncLits returns the func literals in an expression that are not nested
// inside another one — `var X = wrap(func(){…})` counts, and a closure declared
// inside that literal does not, because walking the outer body reaches it.
func outermostFuncLits(e ast.Expr) []*ast.FuncLit {
	var out []*ast.FuncLit
	ast.Inspect(e, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		if lit.Body != nil {
			out = append(out, lit)
		}
		return false
	})
	return out
}

// errorsPkgNames resolves the local name(s) the "errors" import goes by IN THIS
// FILE.
//
// Hard-coding the identifier `errors` meant `import stderrors "errors"` evaded
// half (b) COMPLETELY — not a missed edge, a one-word rename that turns the whole
// consumer arm off for a file while every test stays green. Resolution is per-file
// because that is the scope of an import name.
//
// A blank import cannot be called, and a dot import leaves no receiver to match on;
// neither yields a name. When a file imports errors under no name at all the set
// falls back to the default so behaviour is unchanged for the ordinary case.
func errorsPkgNames(f *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, imp := range f.Imports {
		if imp.Path == nil || imp.Path.Value != `"errors"` {
			continue
		}
		if imp.Name == nil {
			names["errors"] = true
			continue
		}
		if imp.Name.Name == "_" || imp.Name.Name == "." {
			continue
		}
		names[imp.Name.Name] = true
	}
	if len(names) == 0 {
		names["errors"] = true
	}
	return names
}

// bindings finds every place this body binds *surface.IncompatibleError.
//
// Both the qualified form (*surface.IncompatibleError, every consumer package) and
// the bare form (*IncompatibleError, internal/surface itself) are recognised.
// Missing the bare form would exempt the gate — the package with the most direct
// stake in this prose.
//
// # Four spellings, because one defect with four spellings is one defect
//
// Keying on `var ie *T` + errors.As alone let the SAME omission walk past the rule
// in three other spellings, and one of them is the motivating specimen verbatim:
//
//	switch {                            // E1 — scopeOf saw only *ast.IfStmt, so the
//	case errors.As(err, &ie):           //      scope widened to the whole function
//	    fmt.Sprintf(…ie.BinarySurface…) //      and the default branch below
//	default:                            //      satisfied the rule
//	    return err.Error()
//	}
//
//	switch e := err.(type) { case *surface.IncompatibleError: … }   // E2
//	if e, ok := err.(*surface.IncompatibleError); ok { … }          // E3
//	ie := &surface.IncompatibleError{}; errors.As(err, &ie)         // E4
//
// E2 and E3 do not call errors.As at all; E4's `:=` is an *ast.AssignStmt, never an
// *ast.ValueSpec. A rule that goes green on the code it was written to catch is
// worse than no rule — this file says so about the if/else form, and it was true of
// the switch form at the same time.
//
// A later pass closed four more, all recorded as MISSES under
// seven-binding-spellings-the-remediation-rule-cannot-see — the defect present in
// full, the rule green:
//
//	func f(err error, ie *surface.IncompatibleError)  // S1 — the target is a PARAMETER,
//	                                                  //      so it was never `declared`
//	e := err.(*surface.IncompatibleError)             // S2 — a PLAIN assertion statement;
//	                                                  //      the success scope is the REST
//	                                                  //      of the list, because the wrong
//	                                                  //      type panics rather than falling
//	                                                  //      through
//	var X = func(err error) { … }                     // S3 — no *ast.FuncDecl exists
//	stderrors.As(err, &ie)                            // S4 — an aliased errors import
//
// S1, S3 and S4 are all closed by bindingScope carrying params, a body and the
// file's errors names instead of an *ast.FuncDecl. The fifth miss, a SHADOWED
// source identifier, belongs to reachesRemedy and is handled there.
//
// # 🔴 EACH BINDING CARRIES ITS SUCCESS SCOPE, AND THE NEGATED FORM INVERTS IT
//
// Only the branch where the type test SUCCEEDED is the path on which the remedy is
// owed; an else branch, or the code after a plain `if`, runs precisely when the
// error is something else. Getting that backwards costs in both directions:
//
//   - Positive form, `if errors.As(err, &ie) { … }` — the success path is the body.
//   - NEGATED form, `if !errors.As(err, &ie) { return … }` — the body is the FAILURE
//     branch. The success path is what comes AFTER the statement. This is the most
//     idiomatic error handling in Go, and scoping it to the body made the rule
//     demand the remedy in the one block that must not carry it — a false positive
//     on correct code, whose only documented escape was a baseline entry that would
//     exempt the function from half (b) permanently.
//
// The negated case deliberately does NOT require the guard body to terminate. When
// it does not, control reaches the following statements on BOTH paths, so those
// statements are a superset of the success path — accepting them can only cause a
// missed finding, never a false one, which is the bias this whole package keeps.
func (s bindingScope) bindings() []asBinding {
	declared := s.declaredIdents()

	var out []asBinding
	seen := map[string]bool{}
	// Deduped by name AND POSITION. Keying on the name alone kept only the FIRST
	// binding of each identifier, so a function that bound `ie` twice — the
	// compliant one first, a field-rendering one second — had the second dropped
	// and rode in behind the first. Every binding site owes the remedy on its own
	// success path.
	add := func(b asBinding) {
		key := fmt.Sprintf("%s@%d", b.name, b.pos)
		if b.name == "" || b.name == "_" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, b)
	}

	// Statement lists are walked explicitly rather than through ast.Inspect,
	// because a guard clause's success scope is "the REST OF THIS LIST" and only a
	// walk that knows where it is in a list can express that.
	var walkList func(list []ast.Stmt)
	walkStmt := func(st ast.Stmt, rest []ast.Stmt) {
		switch v := st.(type) {
		case *ast.IfStmt:
			// E3: if e, ok := err.(*T); ok { … } — and its negation, which
			// inverts the success path exactly as `!errors.As` does. Handling one
			// polarity and not the other cost a miss AND a false positive on the
			// same shape.
			if tb, ok := typeAssertBinding(v.Init); ok {
				scope := v.Body.List
				if identIsNegated(v.Cond, tb.okName) {
					scope = elseThenRest(v, rest)
				}
				add(asBinding{name: tb.name, source: tb.source, scope: scope, pos: v.Pos()})
			}
			// errors.As anywhere in the condition, negated or not.
			if call, negated, ok := s.errorsAsInCond(v.Cond, declared); ok {
				scope := v.Body.List
				if negated {
					scope = elseThenRest(v, rest)
				}
				add(asBinding{
					name:   asTargetName(call),
					source: identName(call.Args[0]),
					scope:  scope,
					pos:    call.Pos(),
				})
			}

		case *ast.AssignStmt:
			// S2: `e := err.(*T)` as a PLAIN STATEMENT — no comma-ok, no if-init, so
			// typeAssertBinding was never consulted and the whole form was invisible.
			//
			// 🔴 THE SUCCESS SCOPE IS THE REST OF THIS LIST, and that is not a
			// widening of the scope semantics — it is those semantics applied to a
			// statement that has no failure branch to confuse them with. A
			// single-value assertion PANICS on the wrong type, so every statement
			// after it runs only where the assertion succeeded. That is the same
			// reason elseThenRest is the success path of a negated guard.
			//
			// The comma-ok form reached here has an `ok` the walk cannot see tested,
			// so it gets a NIL scope — whole-body search, source renders rejected —
			// which is exactly how the bool-temp `ok := errors.As(…)` shape is
			// already treated. Guessing at a scope we cannot prove is how the
			// original defect got in.
			if tb, ok := typeAssertBinding(v); ok {
				b := asBinding{name: tb.name, source: tb.source, pos: v.Pos()}
				if tb.okName == "" {
					b.scope = rest
				}
				add(b)
			}

		case *ast.TypeSwitchStmt:
			// E2: switch e := err.(type) { case *T: … }
			name, src := typeSwitchSubject(v)
			if name != "" {
				for _, cs := range v.Body.List {
					cc, ok := cs.(*ast.CaseClause)
					if !ok {
						continue
					}
					for _, t := range cc.List {
						star, isStar := t.(*ast.StarExpr)
						if isStar && isIncompatibleErrorType(star.X) {
							add(asBinding{name: name, source: src, scope: cc.Body, pos: cc.Pos()})
						}
					}
				}
			}

		case *ast.SwitchStmt:
			// E1: switch { case errors.As(err, &ie): … }. A CaseClause is not an
			// *ast.IfStmt, which is the whole reason this arm exists.
			for _, cs := range v.Body.List {
				cc, ok := cs.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, cond := range cc.List {
					if call, negated, ok := s.errorsAsInCond(cond, declared); ok && !negated {
						add(asBinding{
							name:   asTargetName(call),
							source: identName(call.Args[0]),
							scope:  cc.Body,
							pos:    call.Pos(),
						})
					}
				}
			}
		}

		// Recurse into every nested statement list.
		ast.Inspect(st, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BlockStmt:
				if v != nil {
					walkList(v.List)
				}
			case *ast.CaseClause:
				walkList(v.Body)
			case *ast.CommClause:
				walkList(v.Body)
			}
			return true
		})
	}
	walkList = func(list []ast.Stmt) {
		for i, st := range list {
			walkStmt(st, list[i+1:])
		}
	}
	walkList(s.body.List)

	// A bare errors.As with no enclosing conditional at all — the value is bound
	// and the whole remainder of the function is the success path as far as syntax
	// can tell. Scope nil means "the whole body".
	ast.Inspect(s.body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !s.isErrorsAs(call) {
			return true
		}
		name := asTargetName(call)
		if name == "" || !declared[name] {
			return true
		}
		// add() dedupes on (name, pos), so a call already recorded by walkStmt with
		// a known scope is skipped here rather than re-added with a nil one.
		add(asBinding{name: name, source: identName(call.Args[0]), pos: call.Pos()})
		return true
	})

	return out
}

// declaredIdents collects identifiers this body has available as
// *surface.IncompatibleError, in every spelling that reaches errors.As:
//
//	func f(ie *surface.IncompatibleError)  (a PARAMETER — S1)
//	var ie *surface.IncompatibleError      (an *ast.ValueSpec with an explicit Type)
//	ie := &surface.IncompatibleError{}     (an *ast.AssignStmt — E4)
//
// 🔴 PARAMETERS COUNT, AND SEEDING THEM HERE IS WHY THIS IS ONE WALK AND NOT TWO.
// Walking the body alone meant a consumer handed the target as an argument bound
// it with errors.As and the rule saw nothing at all — not a weaker finding, NO
// finding, because errorsAsInCond refuses a target it cannot see declared. The
// declaration set is the only thing that had to widen; every spelling of the
// binding itself already funnels through this map.
//
// Naming a parameter of the type does NOT by itself make a binding — a renderer
// that simply takes `ie *surface.IncompatibleError` and calls Remediation() has
// bound nothing. It only becomes one when errors.As, a type switch or an assertion
// puts the error into it.
func (s bindingScope) declaredIdents() map[string]bool {
	declared := map[string]bool{}
	if s.params != nil {
		for _, field := range s.params.List {
			star, ok := field.Type.(*ast.StarExpr)
			if !ok || !isIncompatibleErrorType(star.X) {
				continue
			}
			for _, name := range field.Names {
				declared[name.Name] = true
			}
		}
	}
	ast.Inspect(s.body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ValueSpec:
			// `var ie *surface.IncompatibleError` — an explicit type.
			if star, ok := v.Type.(*ast.StarExpr); ok && isIncompatibleErrorType(star.X) {
				for _, name := range v.Names {
					declared[name.Name] = true
				}
				return true
			}
			// `var ie = &surface.IncompatibleError{}` — no explicit type, and it is
			// neither an *ast.AssignStmt (so the `:=` arm below misses it) nor a
			// typed ValueSpec. Ordinary Go, and it sat in the gap between the two.
			for i, val := range v.Values {
				if i < len(v.Names) && isIncompatibleErrorValue(val) {
					declared[v.Names[i].Name] = true
				}
			}
		case *ast.AssignStmt:
			for i, rhs := range v.Rhs {
				if i >= len(v.Lhs) {
					break
				}
				lhs, ok := v.Lhs[i].(*ast.Ident)
				if !ok {
					continue
				}
				if isIncompatibleErrorValue(rhs) {
					declared[lhs.Name] = true
				}
			}
		}
		return true
	})
	return declared
}

// isIncompatibleErrorValue recognises an expression whose value is a
// *surface.IncompatibleError: &T{…}, new(T), or a bare T{…} composite literal.
func isIncompatibleErrorValue(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.UnaryExpr:
		return v.Op == token.AND && isIncompatibleErrorValue(v.X)
	case *ast.CompositeLit:
		return isIncompatibleErrorType(v.Type)
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "new" && len(v.Args) == 1 {
			return isIncompatibleErrorType(v.Args[0])
		}
	}
	return false
}

// isErrorsAs reports whether a call is errors.As(src, &target), under whatever
// local name this file's "errors" import goes by.
//
// 🔴 THIS IS THE ONE DEFINITION, and it stays the one definition. Everything that
// needs to know "is this errors.As" — the condition walk, the bare-call sweep, and
// reachesRemedy's own skip of the binding call — asks here. A second copy that
// happened to still say `errors` would leave half the rule blind to an aliased
// import while the other half saw it, which is worse than either answer alone.
func (s bindingScope) isErrorsAs(call *ast.CallExpr) bool {
	if len(call.Args) != 2 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "As" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && s.errPkgs[recv.Name]
}

// asTargetName returns the identifier an errors.As call binds into, from its
// &target second argument.
func asTargetName(call *ast.CallExpr) string {
	unary, ok := call.Args[1].(*ast.UnaryExpr)
	if !ok {
		return ""
	}
	return identName(unary.X)
}

// errorsAsInCond finds a qualifying errors.As inside a condition and reports
// whether it sits under a logical NOT — the guard-clause form, whose success path
// is the opposite of its body.
func (s bindingScope) errorsAsInCond(cond ast.Expr, declared map[string]bool) (call *ast.CallExpr, negated, ok bool) {
	var walk func(e ast.Expr, neg bool)
	walk = func(e ast.Expr, neg bool) {
		if ok {
			return
		}
		switch v := e.(type) {
		case *ast.UnaryExpr:
			if v.Op == token.NOT {
				walk(v.X, !neg)
				return
			}
			walk(v.X, neg)
		case *ast.ParenExpr:
			walk(v.X, neg)
		case *ast.BinaryExpr:
			walk(v.X, neg)
			walk(v.Y, neg)
		case *ast.CallExpr:
			if !s.isErrorsAs(v) {
				return
			}
			if name := asTargetName(v); name != "" && declared[name] {
				call, negated, ok = v, neg, true
			}
		}
	}
	walk(cond, false)
	return call, negated, ok
}

// assertBinding is a comma-ok type assertion on *surface.IncompatibleError.
type assertBinding struct {
	name   string // the bound value: `e`
	okName string // the comma-ok result: `ok` — what the condition tests
	source string // the asserted expression, when it is an identifier: `err`
}

// typeAssertBinding recognises `e, ok := err.(*surface.IncompatibleError)` — in an
// if-statement's Init, or as a PLAIN STATEMENT in a list. okName is returned so the
// caller can read the CONDITION's polarity: `if …; ok {` and `if …; !ok {` have
// opposite success paths, and an EMPTY okName means the single-value form, which
// has no failure path at all because it panics.
//
// It takes an ast.Stmt rather than an *ast.IfStmt precisely so that both callers
// share it: the assertion is the same assertion wherever it is written, and a
// second recogniser for the statement form is the two-copies defect again.
func typeAssertBinding(stmt ast.Stmt) (assertBinding, bool) {
	as, isAssign := stmt.(*ast.AssignStmt)
	if !isAssign || len(as.Lhs) == 0 || len(as.Rhs) != 1 {
		return assertBinding{}, false
	}
	ta, isAssert := as.Rhs[0].(*ast.TypeAssertExpr)
	if !isAssert || ta.Type == nil {
		return assertBinding{}, false
	}
	star, isStar := ta.Type.(*ast.StarExpr)
	if !isStar || !isIncompatibleErrorType(star.X) {
		return assertBinding{}, false
	}
	b := assertBinding{name: identName(as.Lhs[0]), source: identName(ta.X)}
	if len(as.Lhs) > 1 {
		b.okName = identName(as.Lhs[1])
	}
	return b, true
}

// identIsNegated reports whether a condition tests `!name` rather than `name`.
func identIsNegated(cond ast.Expr, name string) bool {
	if name == "" {
		return false
	}
	u, ok := cond.(*ast.UnaryExpr)
	if !ok || u.Op != token.NOT {
		return false
	}
	inner := u.X
	if p, isParen := inner.(*ast.ParenExpr); isParen {
		inner = p.X
	}
	return identName(inner) == name
}

// elseThenRest is the success path of a NEGATED conditional: the else arm, if any,
// followed by whatever comes after the statement.
func elseThenRest(ifs *ast.IfStmt, rest []ast.Stmt) []ast.Stmt {
	out := []ast.Stmt{}
	if eb, isBlock := ifs.Else.(*ast.BlockStmt); isBlock {
		out = append(out, eb.List...)
	}
	return append(out, rest...)
}

// typeSwitchSubject returns the bound name and subject of `switch e := err.(type)`.
func typeSwitchSubject(ts *ast.TypeSwitchStmt) (name, source string) {
	as, ok := ts.Assign.(*ast.AssignStmt)
	if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return "", ""
	}
	ta, ok := as.Rhs[0].(*ast.TypeAssertExpr)
	if !ok {
		return "", ""
	}
	return identName(as.Lhs[0]), identName(ta.X)
}

func identName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func isIncompatibleErrorType(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "IncompatibleError"
	case *ast.SelectorExpr:
		recv, ok := v.X.(*ast.Ident)
		return ok && recv.Name == "surface" && v.Sel.Name == "IncompatibleError"
	}
	return false
}

// reachesRemedy reports whether a binding's SUCCESS PATH either RENDERS the way out
// or HANDS THE ERROR ON to something that can.
//
// # Rendering
//
// Remediation() or Error() on the bound identifier is the direct form. Error() on
// the identifier the error came from is accepted too, but only inside the success
// scope: internal/surface/gate.go prints `err.Error()` right there, which delivers
// the same bytes — the binding exists to decide WHETHER to print, not to build the
// text. The same call in an else branch means the opposite thing.
//
// # Handing it on — and why this half had to exist
//
// A consumer that RETURNS the error, passes it to a renderer, stores it in a
// struct, or joins it into another error has not lost the remedy: the value still
// carries Error(), and rendering is correctly deferred to its caller. The rule
// flagged all four, and every one is ordinary Go:
//
//	if !errors.As(err, &ie) { return "" }; return ie.Error()   // guard clause
//	return renderRemedy(ie)                                   // delegates
//	return ie                                                 // returns it
//	return &Result{Err: ie}                                   // stores it
//	return errors.Join(ctxErr, ie)                            // wraps it
//
// 🔴 THAT MATTERS MORE THAN A MISSED FINDING WOULD. The documented escape from a
// false positive here is "baseline it with that reason" — which exempts that
// function from half (b) FOREVER. A rule whose false-positive set is idiomatic Go
// therefore accumulates permanent exemptions on CORRECT code, and each one is a
// hole the ratchet no longer covers. The gate would erode from the inside while
// staying green, which is a worse failure than the one it was built to catch.
//
// So the test is: does the identifier ESCAPE, or is it only ever FIELD-READ? A
// message assembled from BinarySurface / VaultSurface / StampDir reads the fields
// and lets the value die in the function — accurate, well-formed, and useless. That
// remains the one shape this reports.
func (s bindingScope) reachesRemedy(b asBinding) bool {
	scope := b.scope
	if scope == nil {
		scope = s.body.List
	}
	found := false
	inspect := func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		// Rendering.
		case *ast.CallExpr:
			if s.isErrorsAs(v) {
				// The binding's own call: `&ie` here is the bind, not an escape.
				return true
			}
			for _, arg := range v.Args {
				if escapesAs(arg, b.name) {
					found = true
					return false
				}
			}
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Remediation":
				found = recv.Name == b.name
			case "Error":
				// 🔴 THE SOURCE ARM REQUIRES A KNOWN SUCCESS SCOPE. `err.Error()`
				// is evidence only when we know which branch we are standing in.
				// With a nil scope the search covers the WHOLE function, so a
				// source render on the FAILURE path satisfied the binding — which
				// is the motivating preflight defect, spelled with a temporary
				// bool (`ok := errors.As(…)`) or with a negated case clause. The
				// bound identifier's own methods stay acceptable at any scope,
				// because a call on `ie` can only happen where `ie` is real.
				//
				// 🔴 AND THE SOURCE MUST STILL BE THE SOURCE AT THAT LINE. `err`
				// is the most re-declared name in Go; a `err := …` in the success
				// scope makes the following `err.Error()` render a DIFFERENT
				// error, and the rule accepted it as the remedy — the same
				// accurate, well-formed, useless message this whole rule is about,
				// certified compliant by its own name check. The shadow test is
				// positional rather than "declared anywhere in the scope",
				// because a redeclaration inside a nested block does not govern a
				// render outside it, and rejecting that would redden correct code.
				found = recv.Name == b.name ||
					(b.scope != nil && b.source != "" && recv.Name == b.source &&
						!shadowedAt(scope, b.source, v.Pos()))
			}

		// Handing it on.
		case *ast.ReturnStmt:
			for _, r := range v.Results {
				if escapesAs(r, b.name) {
					found = true
					return false
				}
			}
		case *ast.KeyValueExpr:
			if escapesAs(v.Value, b.name) {
				found = true
				return false
			}
		case *ast.SendStmt:
			if escapesAs(v.Value, b.name) {
				found = true
				return false
			}
		case *ast.AssignStmt:
			// `_ = ie` discards it and is not an escape.
			allBlank := true
			for _, l := range v.Lhs {
				if identName(l) != "_" {
					allBlank = false
				}
			}
			if allBlank {
				return true
			}
			for _, r := range v.Rhs {
				if escapesAs(r, b.name) {
					found = true
					return false
				}
			}
		}
		return true
	}
	for _, st := range scope {
		ast.Inspect(st, inspect)
		if found {
			break
		}
	}
	return found
}

// shadowedAt reports whether `name` has been RE-DECLARED, at a point that governs
// pos, between the start of the success scope and pos itself.
//
// This exists for one shape: `err` is the most re-declared identifier in Go, and
// the source arm accepts `err.Error()` as the remedy. An inner `err := …` in the
// success scope makes the following `err.Error()` render an unrelated error, and
// the rule certified that as compliant — the accurate, well-formed, useless
// message it exists to catch, wearing the right name.
//
// 🔴 IT IS POSITIONAL, NOT "DECLARED ANYWHERE IN THE SCOPE", and the difference is
// the usual asymmetry: a shadow it fails to see costs a MISSED finding, while a
// shadow it imagines costs a FALSE POSITIVE on correct code — and the documented
// escape from a false positive is a baseline entry exempting that function from
// half (b) forever. So only declarations that demonstrably govern pos count: a `:=`
// or `var` earlier in the SAME list, or a declaring header (an if/for/switch init,
// a range clause, a type-switch assign, a select comm clause) of a statement pos
// sits inside. A closure parameter is deliberately not tracked — that is the safe
// direction, and inventing scope resolution here would be a second type checker.
func shadowedAt(list []ast.Stmt, name string, pos token.Pos) bool {
	for _, st := range list {
		if st == nil {
			continue
		}
		// A declaration earlier in this list governs everything after it.
		if st.End() <= pos && declaresIdent(st, name) {
			return true
		}
		if pos < st.Pos() || pos > st.End() {
			continue
		}
		// pos sits INSIDE this statement, so the statement's own header counts.
		if headerDeclaresIdent(st, name) {
			return true
		}
		for _, inner := range nestedStmtLists(st) {
			if shadowedAt(inner, name, pos) {
				return true
			}
		}
	}
	return false
}

// declaresIdent reports whether a statement DECLARES name — `:=` or `var`.
// Assignment to an existing variable is not a declaration and does not shadow:
// `err = other` leaves err the same variable, and the source arm's claim is about
// which variable the render reads, not what it holds.
func declaresIdent(st ast.Stmt, name string) bool {
	switch v := st.(type) {
	case *ast.AssignStmt:
		if v.Tok != token.DEFINE {
			return false
		}
		for _, l := range v.Lhs {
			if identName(l) == name {
				return true
			}
		}
	case *ast.DeclStmt:
		gd, ok := v.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			return false
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				if n.Name == name {
					return true
				}
			}
		}
	case *ast.LabeledStmt:
		return declaresIdent(v.Stmt, name)
	}
	return false
}

// headerDeclaresIdent reports whether a compound statement's own header declares
// name — `if err := f(); …`, `for err := range …`, `switch err := x.(type)`,
// `select { case err := <-ch: }`. Those govern the whole statement, so a render
// anywhere inside it reads the shadow rather than the bound source.
func headerDeclaresIdent(st ast.Stmt, name string) bool {
	switch v := st.(type) {
	case *ast.IfStmt:
		return v.Init != nil && declaresIdent(v.Init, name)
	case *ast.ForStmt:
		return v.Init != nil && declaresIdent(v.Init, name)
	case *ast.SwitchStmt:
		return v.Init != nil && declaresIdent(v.Init, name)
	case *ast.TypeSwitchStmt:
		return (v.Init != nil && declaresIdent(v.Init, name)) ||
			(v.Assign != nil && declaresIdent(v.Assign, name))
	case *ast.RangeStmt:
		return v.Tok == token.DEFINE &&
			(identName(v.Key) == name || identName(v.Value) == name)
	case *ast.CommClause:
		return v.Comm != nil && declaresIdent(v.Comm, name)
	case *ast.LabeledStmt:
		return headerDeclaresIdent(v.Stmt, name)
	}
	return false
}

// nestedStmtLists returns the statement lists a compound statement governs, so the
// shadow search can descend along the path that actually contains the render.
// Case and comm clauses are returned as statements in their switch's list and then
// unpacked on the next level down, which keeps their own headers in play.
func nestedStmtLists(st ast.Stmt) [][]ast.Stmt {
	switch v := st.(type) {
	case *ast.BlockStmt:
		return [][]ast.Stmt{v.List}
	case *ast.IfStmt:
		var out [][]ast.Stmt
		if v.Body != nil {
			out = append(out, v.Body.List)
		}
		if v.Else != nil {
			out = append(out, []ast.Stmt{v.Else})
		}
		return out
	case *ast.ForStmt:
		if v.Body != nil {
			return [][]ast.Stmt{v.Body.List}
		}
	case *ast.RangeStmt:
		if v.Body != nil {
			return [][]ast.Stmt{v.Body.List}
		}
	case *ast.SwitchStmt:
		if v.Body != nil {
			return [][]ast.Stmt{v.Body.List}
		}
	case *ast.TypeSwitchStmt:
		if v.Body != nil {
			return [][]ast.Stmt{v.Body.List}
		}
	case *ast.SelectStmt:
		if v.Body != nil {
			return [][]ast.Stmt{v.Body.List}
		}
	case *ast.CaseClause:
		return [][]ast.Stmt{v.Body}
	case *ast.CommClause:
		return [][]ast.Stmt{v.Body}
	case *ast.LabeledStmt:
		return [][]ast.Stmt{{v.Stmt}}
	}
	return nil
}

// escapesAs reports whether an expression hands the bound value itself onward, as
// opposed to reading a field off it. `ie` and `&ie` escape; `ie.BinarySurface` does
// not, and that distinction is the whole rule.
func escapesAs(e ast.Expr, name string) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == name
	case *ast.UnaryExpr:
		return v.Op == token.AND && escapesAs(v.X, name)
	case *ast.ParenExpr:
		return escapesAs(v.X, name)
	case *ast.CompositeLit:
		// []error{ie}, &Result{"", nil, ie}, map[string]error{"k": ie}. The KEYED
		// struct form was already caught by the *ast.KeyValueExpr arm in
		// reachesRemedy; the POSITIONAL and slice/map forms have no KeyValueExpr
		// at all and were reported as omissions on correct code.
		for _, el := range v.Elts {
			if kv, isKV := el.(*ast.KeyValueExpr); isKV {
				if escapesAs(kv.Value, name) {
					return true
				}
				continue
			}
			if escapesAs(el, name) {
				return true
			}
		}
	case *ast.SliceExpr:
		return escapesAs(v.X, name)
	}
	return false
}

// enclosingFuncName names the function a position falls inside, or the file's
// base name for a package-level declaration. Findings stay keyed by function so
// the baseline is diffable under edits that only move code.
func enclosingFuncName(f file, pos token.Pos) string {
	for _, decl := range f.ast.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if pos >= fn.Pos() && pos <= fn.End() {
			return funcName(fn)
		}
	}
	return filepath.Base(f.path)
}

const remediationDriftHazard = "this prose has exactly ONE home, " +
	"surface.(*IncompatibleError).Remediation(), and a second copy is not a style issue: the last one " +
	"drifted from the producer (`action:` became `Upgrade:`, `<original-command>` became `<command>`, " +
	"and the framing line was dropped) and NOTHING NOTICED, because the only test near it used a " +
	"synthetic fixture. Call Remediation() and render its lines. If this literal is genuinely not the " +
	"remediation — a test fixture in non-test code, a doc example — baseline it WITH THAT REASON"

const remediationOmissionHazard = "a stranded host cannot pull its way out (pulling raises the vault " +
	"floor further; the fix is a NEW BINARY) and the MCP server it is talking to is the thing that is " +
	"out of date, so its only route back is TEXT. A message built from BinarySurface / VaultSurface / " +
	"StampDir alone is accurate, well-formed, and useless — it reads like a proper error, which is why " +
	"this shape survived undetected on the /vpc-wrap halt path. Render ie.Error(), or ie.Remediation() " +
	"into whatever list this consumer emits. If this function genuinely has no output channel for it — " +
	"it binds the error only to decide control flow, or to fill typed numeric fields another site " +
	"renders — baseline it WITH THAT REASON"
