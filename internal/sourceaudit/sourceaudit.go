// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package sourceaudit is a static analysis of THIS repository's own source, and
// it exists because of a specific, repeated, expensive bug class.
//
// Every serious defect found in iterations 191-201 was caught by looking at a real
// artifact, and NOT ONE was caught by a test, a check, or a code review. Two of
// those defects were MECHANICALLY DETECTABLE and hid for months anyway:
//
//   - note_path was yaml-tagged on SessionMeta and NOTHING EVER ASSIGNED IT. The
//     key was never written to a note, every read-back unmarshalled an absent
//     field, and vp_capture_session reported note_path: "" for the entire life of
//     the project while returning status "ok". Six months. Every test green.
//
//   - "Capability built, nothing invokes it" is this project's single most
//     repeated root cause: the Zed archive adapter (implemented, registered,
//     tested — and nothing ever passes archive_adapter: "zed"), `vp check` (a whole
//     check suite no template invokes), vp_health (a tool nothing called), and the
//     claim sentinel on the MCP path (gated on parameters no template supplies).
//
// Both are the same shape: A SYMBOL NOTHING EVER PRODUCES. A field that is only
// ever read. A function that is only ever defined. The compiler is happy, the
// tests are green, and the feature is inert.
//
// This package finds them, and it lives in the TEST GATE rather than in `vp check`
// deliberately. `vp check` runs from an installed binary against a vault, on a host
// that may have no source tree at all — it structurally cannot do this. A test can,
// it runs on every change instead of once a week, and it cannot be forgotten.
//
// # The baseline, and why it can only shrink
//
// The repository does not start clean. A check that fails loudly on day one gets
// disabled on day one, so findings that already exist are recorded in a BASELINE
// and the gate gives two guarantees:
//
//   - a NEW finding fails the build; and
//   - a baseline entry that is no longer a finding ALSO fails the build.
//
// The second half is the important one. It means the baseline can only ever
// SHRINK: you cannot quietly fix something and leave the debt recorded, and you
// cannot let the list rot into a lie. This is the ratchet, made mechanical.
//
// # Honest limits
//
// This is a SYNTACTIC analysis (go/ast, standard library only — no type
// information). That biases it toward FALSE NEGATIVES, never false positives:
//
//   - Field names are not unique across structs, so `Foo.Name = x` counts as an
//     assignment to every `Name` field in the repo. A write-only field that shares
//     its name with an assigned field elsewhere will be missed.
//   - A function called only via reflection looks uninvoked. Those are recorded in
//     the baseline with a reason rather than special-cased.
//
// Missing a real bug is bad. Crying wolf is worse: a noisy gate is a disabled
// gate, and a disabled gate is how note_path survived six months.
//
// # Interface dispatch: the ONE exemption, and why the line is drawn where it is
//
// A method reached only through an interface has no direct call site and so looks
// uninvoked. The tempting fix — exempt every method that satisfies some interface —
// is WRONG, and triaging the first baseline proved it: six reconcile.*.Requires()
// methods satisfy reconcile.Reconciler, an interface the tree genuinely uses, and
// they declare a dependency graph that the driver loop re-derives by hand in a
// switch and never once reads. The blanket rule would have hidden all six. An
// interface method nobody dispatches on is not noise; it is this project's signature
// bug wearing a contract.
//
// So the exemption keys on WHERE THE DISPATCHER LIVES (see stdlibContracts):
//
//   - OUT of tree (log/slog calls Handle, errors.Is calls Unwrap): no in-tree call
//     site can ever exist, the finding can never be actioned, and reporting it is
//     pure noise. Exempt.
//   - IN tree: keep flagging. This needs no special case at all — calls are tracked
//     by bare name, so as soon as any code calls x.Requires(), every implementation
//     of it goes quiet on its own.
package sourceaudit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one auditable defect in the source tree.
type Finding struct {
	Kind   string `json:"kind"`   // KindWriteOnlyField | KindUninvoked
	Symbol string `json:"symbol"` // "storage.SessionMeta.Branch" | "capture.AnalyzeFriction"
	Pos    string `json:"pos"`    // file:line, for humans; NOT part of identity
	Detail string `json:"detail"`
}

// The kinds of finding this package reports.
const (
	// KindWriteOnlyField: a struct field carrying a yaml/json tag that NOTHING in
	// non-test code ever assigns. It serializes as absent (omitempty) or as a zero
	// value, and every reader of it is reading a fact nobody ever wrote. This is
	// the note_path bug, exactly.
	KindWriteOnlyField = "write-only-field"

	// KindUninvoked: a function or method declared in non-test code and called from
	// nowhere in non-test code. Capability built, nothing invokes it.
	KindUninvoked = "uninvoked"
)

// ID is the finding's stable identity for baseline comparison. It deliberately
// EXCLUDES Pos: a finding must not silently "disappear" from the baseline because
// someone added a line above it.
func (f Finding) ID() string { return f.Kind + " " + f.Symbol }

// file is one parsed source file plus the metadata the analyses need.
type file struct {
	path   string
	isTest bool
	fset   *token.FileSet
	ast    *ast.File
}

// loadPackages parses every .go file under the given roots.
func loadPackages(roots ...string) ([]file, error) {
	var out []file
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// NEVER skip the root itself. The root's DirEntry name is whatever the
				// caller passed — ".." for a relative walk — and a naive
				// strings.HasPrefix(name, ".") check on it SKIPS THE ENTIRE TREE and
				// reports zero findings. An audit that silently finds nothing is the
				// exact false-green this package exists to eliminate, so it is worth a
				// comment: this bug was caught by MEASURING the first run instead of
				// trusting it.
				if path == root {
					return nil
				}
				// Vendored/generated trees are not ours to audit.
				if name := d.Name(); name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return fmt.Errorf("parse %s: %w", path, perr)
			}
			out = append(out, file{
				path:   path,
				isTest: strings.HasSuffix(path, "_test.go"),
				fset:   fset,
				ast:    f,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Run performs every source audit over the given roots and returns the findings,
// sorted by ID so the output is stable and diffable.
func Run(roots ...string) ([]Finding, error) {
	files, err := loadPackages(roots...)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	findings = append(findings, writeOnlyFields(files)...)
	findings = append(findings, uninvokedFuncs(files)...)

	sort.Slice(findings, func(i, j int) bool { return findings[i].ID() < findings[j].ID() })
	return findings, nil
}

// serializedField is a struct field that goes to disk or over the wire.
type serializedField struct {
	structName string
	fieldName  string
	pkg        string
	pos        string
	tag        string
}

// writeOnlyFields finds struct fields with a yaml/json tag that non-test code never
// assigns — the note_path class.
//
// A field is "assigned" if it appears anywhere in non-test code as:
//   - the key of a composite literal:  T{Field: v}
//   - the target of an assignment:     x.Field = v
//   - the target of an increment:      x.Field++
//   - the operand of a unary &:        &x.Field  (it may be written through)
//
// Assignment is tracked by BARE FIELD NAME, not by (struct, field). That is a
// deliberate conservative bias: it can only make us miss a finding, never invent
// one. See the package doc.
//
// # Only structs the code CONSTRUCTS are audited, and that is the whole trick
//
// A naive "tagged field nobody assigns" flags every DESERIALIZED struct in the
// tree — the MCP `*Params` types, hook.Payload, the Zed DB rows, LLM responses.
// Those are populated by json/yaml.Unmarshal through reflection, so NO field is
// ever assigned by hand, and reporting them is crying wolf. A noisy gate is a
// disabled gate, and a disabled gate is how note_path survived six months.
//
// So a struct is only audited when the code CONSTRUCTS it — when at least one of
// its own fields is assigned somewhere. That single rule cleanly separates the two
// populations:
//
//   - INPUT structs (unmarshaled): zero fields assigned ⇒ skipped entirely.
//   - OUTPUT/record structs (built by hand): siblings assigned ⇒ audited.
//
// And what it isolates is precisely the note_path shape: A RECORD THE CODE BUILDS,
// WITH ONE FIELD IT FORGOT. SessionMeta's Date/Title/Summary were all assigned;
// NotePath sat among them, tagged, serialized, and never set — for six months.
func writeOnlyFields(files []file) []Finding {
	var declared []serializedField
	assigned := map[string]bool{}
	// litKeys["SessionMeta"]["Date"] = true — keyed composite literals, per TYPE.
	litKeys := map[string]map[string]bool{}

	for _, f := range files {
		pkg := f.ast.Name.Name

		// Collect serialized field DECLARATIONS from non-test files only. A struct
		// that exists only in tests is not a production surface.
		if !f.isTest {
			ast.Inspect(f.ast, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					return true
				}
				for _, fld := range st.Fields.List {
					if fld.Tag == nil {
						continue
					}
					tag := fld.Tag.Value
					if !strings.Contains(tag, "yaml:") && !strings.Contains(tag, "json:") {
						continue
					}
					for _, name := range fld.Names {
						if !name.IsExported() {
							continue
						}
						declared = append(declared, serializedField{
							structName: ts.Name.Name,
							fieldName:  name.Name,
							pkg:        pkg,
							pos:        posOf(f, name.Pos()),
							tag:        tag,
						})
					}
				}
				return true
			})
		}

		// Collect ASSIGNMENTS from non-test code. Test code does not count: a field
		// that only tests ever set is exactly the note_path bug (its one test SEEDED
		// the value itself before asserting it round-tripped).
		if f.isTest {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				// The literal names its own type, so record the keys AGAINST THAT TYPE.
				// This is the precise signal; the bare-name `assigned` map below is the
				// conservative fallback for `x.Field = v`, whose receiver type we cannot
				// resolve without full type information.
				typeName := typeNameOf(node.Type)
				for _, elt := range node.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					id, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					assigned[id.Name] = true
					if typeName != "" {
						if litKeys[typeName] == nil {
							litKeys[typeName] = map[string]bool{}
						}
						litKeys[typeName][id.Name] = true
					}
				}
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok {
						assigned[sel.Sel.Name] = true
					}
				}
			case *ast.IncDecStmt:
				if sel, ok := node.X.(*ast.SelectorExpr); ok {
					assigned[sel.Sel.Name] = true
				}
			case *ast.UnaryExpr:
				// &x.Field — the address escapes, so it may be written through.
				if node.Op == token.AND {
					if sel, ok := node.X.(*ast.SelectorExpr); ok {
						assigned[sel.Sel.Name] = true
					}
				}
			}
			return true
		})
	}

	var out []Finding
	for _, d := range declared {
		if assigned[d.fieldName] {
			continue
		}
		// Skip structs the code never CONSTRUCTS. Zero keyed composite literals of this
		// type means it is a deserialized input (an MCP *Params, hook.Payload, a Zed DB
		// row, an LLM response) — populated by Unmarshal through reflection, so no field
		// is ever hand-assigned and flagging one is crying wolf. See the doc above.
		if len(litKeys[d.structName]) == 0 {
			continue
		}
		out = append(out, Finding{
			Kind:   KindWriteOnlyField,
			Symbol: d.pkg + "." + d.structName + "." + d.fieldName,
			Pos:    d.pos,
			Detail: "serialized field on a struct the code CONSTRUCTS, and nothing ever assigns it — its siblings " +
				"are set and this one was forgotten. It serializes as absent/zero, and every reader of it reads a " +
				"value nobody produced. This is the note_path bug. Write it, or delete it.",
		})
	}
	return out
}

// uninvokedFuncs finds functions and methods declared in non-test code that no
// non-test code ever calls — "capability built, nothing invokes it".
//
// Calls are tracked by BARE NAME (both `Foo()` and `x.Foo()`) because this is an
// AST-only analyzer with no type resolution: at `base.Diff(x)` there is nothing to
// say what type `base` is. Bare-name matching is the conservative direction — a name
// called anywhere counts as called everywhere — which trades false negatives for
// safety from false positives, and 203 is why (a gate that calls LIVE code dead is
// how you get a disabled gate without anyone deciding to disable it).
//
// # But the conservative direction had teeth of its own (206)
//
// A bare name is only unambiguous if ONE package declares it. When two do, a live
// call to one silently marks the OTHER as invoked — and this is not theoretical: the
// new internal/vaultaudit package declares Baseline.Diff/Save/Regenerate and
// LoadBaseline, mirroring sourceaudit's own, and its live calls made the gate report
// sourceaudit's genuinely-dead versions as STALE baseline entries. **The gate then
// demanded the removal of two TRUE records.** A false negative that merely hides a
// finding is bad; one that actively rewrites the baseline corrupts the ratchet.
// Names like Save, Load, Run and Diff are not exotic — they are Go.
//
// So: names declared in EXACTLY ONE package keep the permissive rule (there is no
// ambiguity to resolve, and permissive cannot be wrong). Names declared in TWO OR
// MORE are IMPORT-SCOPED: a call in package P satisfies a declaration in package Q
// only if P == Q or P imports Q. That is sound without type information — a package
// that does not import Q cannot name Q's function — and it is applied ONLY where the
// ambiguity exists, so the blast radius is exactly the collision class and nothing
// else.
func uninvokedFuncs(files []file) []Finding {
	type decl struct {
		name string
		recv string
		pkg  string
		pos  string
	}
	var declared []decl

	// called[callerPkg][name] — WHO called it, not merely that someone did.
	called := map[string]map[string]bool{}
	markCalled := func(pkg, name string) {
		if called[pkg] == nil {
			called[pkg] = map[string]bool{}
		}
		called[pkg][name] = true
	}

	imports := importGraph(files)

	for _, f := range files {
		pkg := f.ast.Name.Name
		if !f.isTest {
			for _, d := range f.ast.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Name == nil {
					continue
				}
				name := fd.Name.Name
				// main/init are invoked by the runtime, not by a call site.
				if name == "main" || name == "init" || name == "_" {
					continue
				}
				var recv string
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					recv = typeNameOf(fd.Recv.List[0].Type)
				}
				declared = append(declared, decl{
					name: name,
					recv: recv,
					pkg:  f.ast.Name.Name,
					pos:  posOf(f, fd.Name.Pos()),
				})
			}
		}

		// Call sites, from NON-TEST code only. A function only its own tests call is
		// precisely the thing this check is looking for.
		if f.isTest {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				markCalled(pkg, fn.Name)
			case *ast.SelectorExpr:
				markCalled(pkg, fn.Sel.Name)
			}
			return true
		})

		// A function VALUE passed around (mycmd.Run, not mycmd.Run()) is also a use:
		// it is invoked later, through the value. Without this, every handler and
		// callback in the tree looks dead.
		mark := func(e ast.Expr) { markFuncValue(e, pkg, markCalled) }
		ast.Inspect(f.ast, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.KeyValueExpr:
				mark(node.Value)
			case *ast.AssignStmt:
				for _, rhs := range node.Rhs {
					mark(rhs)
				}
			case *ast.CallExpr:
				for _, arg := range node.Args {
					mark(arg)
				}
			case *ast.ReturnStmt:
				for _, res := range node.Results {
					mark(res)
				}
			case *ast.ValueSpec:
				// `var EmbeddedSHA = realEmbeddedSHA` — a package-level test seam, and
				// this repo's standard idiom for one. Without this case the seam's real
				// implementation looks DEAD while running in production on every command,
				// which is a FALSE POSITIVE — and a gate that calls live code dead is how
				// you teach everyone to wave off its findings. Caught by triaging the
				// first baseline: templates.realEmbeddedSHA is reachable from five sites
				// via internal/templates/embedded.go:103 and the gate reported it dead.
				for _, v := range node.Values {
					mark(v)
				}
			}
			return true
		})
	}

	// Methods that exist ONLY to satisfy an interface the STANDARD LIBRARY dispatches
	// can never have an in-tree call site. Exempt them; see stdlibContracts.
	exempt := stdlibDispatchedMethods(files)

	// declaringPkgs[name] — every package declaring this name. A name declared in
	// exactly ONE package is unambiguous, and bare-name matching cannot be wrong
	// about it. Two or more, and a call must be attributed by the import graph.
	declaringPkgs := map[string]map[string]bool{}
	for _, d := range declared {
		if declaringPkgs[d.name] == nil {
			declaringPkgs[d.name] = map[string]bool{}
		}
		declaringPkgs[d.name][d.pkg] = true
	}

	var out []Finding
	for _, d := range declared {
		if isCalled(d.name, d.pkg, called, imports, declaringPkgs) {
			continue
		}
		if d.recv != "" && exempt[d.recv][d.name] {
			continue
		}
		sym := d.pkg + "." + d.name
		if d.recv != "" {
			sym = d.pkg + "." + d.recv + "." + d.name
		}
		out = append(out, Finding{
			Kind:   KindUninvoked,
			Symbol: sym,
			Pos:    d.pos,
			Detail: "declared in non-test code and called from nowhere in non-test code. Either something " +
				"should be invoking it and does not (the Zed-adapter / vp_health / vp-check class), or it is " +
				"dead and should be deleted.",
		})
	}
	return out
}

// isCalled decides whether a declaration in declPkg named name has a non-test call
// site, resolving the ambiguity that bare-name matching cannot.
//
//   - Declared in exactly ONE package ⇒ permissive: a call from anywhere counts.
//     There is nothing to confuse it with, so this cannot produce a false positive.
//   - Declared in TWO OR MORE ⇒ import-scoped: only a call from the declaring package
//     itself, or from a package that IMPORTS it, can be a call to THIS one. A package
//     that does not import Q cannot name Q's function — sound without type info.
//
// Restricting the strict rule to ambiguous names is what keeps this safe. Applying it
// everywhere would flag a method dispatched through an interface from a package that
// never imports the implementation — a false positive, and 203 established that a gate
// which calls live code dead is worse than one that misses something.
func isCalled(name, declPkg string, called map[string]map[string]bool, imports map[string]map[string]bool, declaringPkgs map[string]map[string]bool) bool {
	ambiguous := len(declaringPkgs[name]) > 1

	for callerPkg, names := range called {
		if !names[name] {
			continue
		}
		if !ambiguous {
			return true
		}
		if callerPkg == declPkg || imports[callerPkg][declPkg] {
			return true
		}
	}
	return false
}

// importGraph maps each package name to the set of IN-TREE package names it imports.
//
// Import paths are resolved to package names by their final segment, then translated
// through the packages actually parsed — so a package whose name differs from its
// directory is still resolved correctly. Out-of-tree imports (stdlib, third party)
// simply never match an in-tree declaring package, which is the right answer.
func importGraph(files []file) map[string]map[string]bool {
	// dirBase → package name, learned from the files we parsed.
	pkgByDirBase := map[string]string{}
	for _, f := range files {
		pkgByDirBase[filepath.Base(filepath.Dir(f.path))] = f.ast.Name.Name
	}

	out := map[string]map[string]bool{}
	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		if out[pkg] == nil {
			out[pkg] = map[string]bool{}
		}
		for _, imp := range f.ast.Imports {
			if imp.Path == nil {
				continue
			}
			path := strings.Trim(imp.Path.Value, `"`)
			seg := path
			if i := strings.LastIndex(seg, "/"); i >= 0 {
				seg = seg[i+1:]
			}
			// An explicit alias names the package as this file sees it, but the
			// DECLARING package's real name is what we must match, so resolve the
			// path — not the alias.
			if real, ok := pkgByDirBase[seg]; ok {
				out[pkg][real] = true
			}
			out[pkg][seg] = true
		}
	}
	return out
}

// stdlibContract is an interface whose implementations are dispatched by code
// OUTSIDE this repository.
type stdlibContract struct {
	// requires is the method set a type must have for us to believe it implements
	// the contract. Syntactic, and deliberately so: no type information is available.
	requires []string
	// dispatched are the methods the standard library will then call on it.
	dispatched []string
}

// stdlibContracts is the ONLY exemption the uninvoked rule grants, and the line it
// draws is the whole design.
//
// It is tempting to exempt any method that satisfies any interface. Do not. That
// rule would have hidden the single most valuable finding in the first triage: six
// reconcile.*.Requires() methods that satisfy reconcile.Reconciler — an interface
// the tree really uses — declaring a dependency graph that the driver loop
// (cmd/vp/cmd_config.go) re-derives by hand in a switch and NEVER READS. Satisfying
// an interface nobody dispatches on is not an exemption; it is the bug.
//
// The line that actually separates noise from bugs is WHERE THE DISPATCHER LIVES:
//
//   - Dispatcher OUT of tree (log/slog calls Handle; errors.Is calls Unwrap): no
//     in-tree call site can EVER exist, so the finding can never be actioned.
//     Reporting it is pure noise, and a noisy gate is a disabled gate.
//   - Dispatcher IN tree: "the interface is satisfied and nobody calls the method"
//     is exactly the capability-built-nothing-invokes-it class. Keep flagging it.
//     (Note this needs no special case: calls are tracked by bare name, so the
//     moment any in-tree code calls x.Requires(), every implementation goes quiet
//     on its own.)
var stdlibContracts = []stdlibContract{
	// error — errors.Is/As/Unwrap walk the chain by calling these.
	{requires: []string{"Error"}, dispatched: []string{"Unwrap", "Is", "As"}},
	// log/slog.Handler — slog.New(h) hands the value to the stdlib logger.
	{
		requires:   []string{"Enabled", "Handle", "WithAttrs", "WithGroup"},
		dispatched: []string{"Enabled", "Handle", "WithAttrs", "WithGroup"},
	},
	// fmt.Stringer — every %v/%s in the stdlib dispatches it.
	{requires: []string{"String"}, dispatched: []string{"String"}},
	// sort.Interface.
	{requires: []string{"Len", "Less", "Swap"}, dispatched: []string{"Len", "Less", "Swap"}},
	// encoding/json and gopkg.in/yaml marshalers.
	{requires: []string{"MarshalJSON"}, dispatched: []string{"MarshalJSON"}},
	{requires: []string{"UnmarshalJSON"}, dispatched: []string{"UnmarshalJSON"}},
	{requires: []string{"MarshalYAML"}, dispatched: []string{"MarshalYAML"}},
	{requires: []string{"UnmarshalYAML"}, dispatched: []string{"UnmarshalYAML"}},
	// net/http.Handler.
	{requires: []string{"ServeHTTP"}, dispatched: []string{"ServeHTTP"}},
}

// stdlibDispatchedMethods returns, per receiver type, the methods exempt from the
// uninvoked rule because the standard library — not this repo — invokes them.
func stdlibDispatchedMethods(files []file) map[string]map[string]bool {
	// methods[recvType] = set of method names declared on it, in non-test code.
	methods := map[string]map[string]bool{}
	for _, f := range files {
		if f.isTest {
			continue
		}
		for _, d := range f.ast.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			recv := typeNameOf(fd.Recv.List[0].Type)
			if recv == "" {
				continue
			}
			if methods[recv] == nil {
				methods[recv] = map[string]bool{}
			}
			methods[recv][fd.Name.Name] = true
		}
	}

	exempt := map[string]map[string]bool{}
	for recv, have := range methods {
		for _, c := range stdlibContracts {
			satisfied := true
			for _, need := range c.requires {
				if !have[need] {
					satisfied = false
					break
				}
			}
			if !satisfied {
				continue
			}
			for _, m := range c.dispatched {
				if !have[m] {
					continue
				}
				if exempt[recv] == nil {
					exempt[recv] = map[string]bool{}
				}
				exempt[recv][m] = true
			}
		}
	}
	return exempt
}

// markFuncValue records a bare function reference used as a VALUE (a handler, a
// callback, a struct field) as "called" — because it will be, through that value.
func markFuncValue(e ast.Expr, pkg string, markCalled func(pkg, name string)) {
	switch v := e.(type) {
	case *ast.Ident:
		markCalled(pkg, v.Name)
	case *ast.SelectorExpr:
		markCalled(pkg, v.Sel.Name)
	}
}

// typeNameOf renders a type expression as a bare type name: "Vault" for *Vault, and
// "SessionMeta" for storage.SessionMeta.
//
// The SelectorExpr case is load-bearing and was missed once. Without it, a
// cross-package composite literal — `storage.SessionMeta{Date: ...}`, which is how
// almost every record in this tree is actually built — resolves to "", the type is
// never recorded as constructed, and the whole field audit silently reports ZERO
// findings while looking perfectly healthy. That is the exact false-green this
// package exists to eliminate, and it is why TestFindsAPlantedWriteOnlyField exists.
func typeNameOf(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return typeNameOf(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr: // pkg.Type
		return t.Sel.Name
	case *ast.IndexExpr: // generic instantiation
		return typeNameOf(t.X)
	}
	return ""
}

func posOf(f file, p token.Pos) string {
	pos := f.fset.Position(p)
	return fmt.Sprintf("%s:%d", filepath.ToSlash(f.path), pos.Line)
}
