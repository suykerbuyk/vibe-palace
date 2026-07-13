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
//   - A function called only through an interface, or only via reflection, looks
//     uninvoked. Those are recorded in the baseline with a reason rather than
//     special-cased, because a special case is a place for a real bug to hide.
//
// Missing a real bug is bad. Crying wolf is worse: a noisy gate is a disabled
// gate, and a disabled gate is how note_path survived six months.
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
// Calls are tracked by BARE NAME (both `Foo()` and `x.Foo()`), which is again the
// conservative direction: a name called anywhere counts as called everywhere.
func uninvokedFuncs(files []file) []Finding {
	type decl struct {
		name string
		recv string
		pkg  string
		pos  string
	}
	var declared []decl
	called := map[string]bool{}

	for _, f := range files {
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
				called[fn.Name] = true
			case *ast.SelectorExpr:
				called[fn.Sel.Name] = true
			}
			return true
		})

		// A function VALUE passed around (mycmd.Run, not mycmd.Run()) is also a use:
		// it is invoked later, through the value. Without this, every handler and
		// callback in the tree looks dead.
		ast.Inspect(f.ast, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.KeyValueExpr:
				markFuncValue(node.Value, called)
			case *ast.AssignStmt:
				for _, rhs := range node.Rhs {
					markFuncValue(rhs, called)
				}
			case *ast.CallExpr:
				for _, arg := range node.Args {
					markFuncValue(arg, called)
				}
			case *ast.ReturnStmt:
				for _, res := range node.Results {
					markFuncValue(res, called)
				}
			}
			return true
		})
	}

	var out []Finding
	for _, d := range declared {
		if called[d.name] {
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

// markFuncValue records a bare function reference used as a VALUE (a handler, a
// callback, a struct field) as "called" — because it will be, through that value.
func markFuncValue(e ast.Expr, called map[string]bool) {
	switch v := e.(type) {
	case *ast.Ident:
		called[v.Name] = true
	case *ast.SelectorExpr:
		called[v.Sel.Name] = true
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
