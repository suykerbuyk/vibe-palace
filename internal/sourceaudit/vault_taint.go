// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
)

// The typed taint behind vault-write-outside-funnel's rule (a): a raw os.* content
// write whose destination is a vault path, whatever route the path took.
//
// # What a vault path is
//
// A string value is vault-derived when it is one of (all keyed on types.Object,
// never on spelling):
//
//  1. a DESIGNATED PARAMETER: a string parameter of a package-level function in a
//     vault-handling package named vaultRoot/vaultPath/vaultDir/destRoot (seedPkgs),
//     `root` only in the exact functions of designatedRootFuncs, plus the exact
//     (function, parameter) rows of designatedParams. A LOCAL or a NAMED RESULT is
//     never a seed, whatever it is called;
//  2. a TYPED SOURCE: a field or method resolved through go/types Selections to a
//     member allowed in vaultSources (storage.Vault.Root and its path accessors,
//     the options structs' VaultRoot, vaultlock.Held.Root(), ...). An unresolved
//     selector is never a trusted source;
//  3. FLOW: assigned or returned from a vault-derived expression: filepath helpers,
//     `+`, string(x), and the RESULTS of a call, per result index, from a producer
//     summary (a function with a `return` of a vault-derived value, or a named
//     result assigned one), so `ap, mp := producer(root)` taints mp.
//
// # Wrappers
//
// A function whose PARAMETER reaches the destination of a raw os.* write (directly,
// or through another wrapper) is a wrapper, and a call passing it a vault-derived
// argument is reported at the call site. The funnel family (atomicfile, vaultfs,
// storage.appendUnderLock) is blessed: never a wrapper, never traversed.
//
// # What stays undetectable, and why
//
// A path laundered through a struct field or map/slice element, a channel, an
// interface-method result, a function value stored anywhere but a variable assigned
// directly from an os verb, reflection, a value built from config/env/git output
// with no vault root in its argument chain, a local whose type came from an
// unmodelled import (storage.NewVault is modelled; most constructors are not), or a
// bare `path string` parameter with no designation. The rule is a ratchet against
// the known shapes, not a proof, and its floors say when it has stopped seeing them.
//
// # Fail-open, guarded
//
// An unresolved producer taints nothing (a spurious taint is a false-positive
// flood). That is only safe because (a) every unexpected type error is fingerprinted
// and accounted (typecheckAccounted), and (b) named per-shape floors fail when a
// shape stops being seen, e.g. archive.manifestPathFor must have a computed
// multi-value summary.

// rawHit is one raw-write finding before keying.
type rawHit struct {
	key     string // finding ID symbol, `<package name>.<scope>`
	pos     string
	detail  string
	wrapper bool
	callee  string // wrapper hits: `<callee import path>.<callee name>`
}

type taintStats struct {
	rawSinks     int
	seeds        int
	selectorHits int
	producers    int
	perPkg       map[string]int  // package name -> producer count
	multi        map[string]bool // `<pkg>.<func>` -> a result at index >= 1 is vault-derived
	prodNames    map[string]bool // `<pkg>.<func>` -> has any vault-derived result
	wrapperNames map[string]bool // `<pkg>.<func>` -> a parameter reaches a raw sink
	wrappers     int
	crossUse     map[string]bool // `<member key>@<using package name>`: a typed source used across packages
}

type fnDecl struct {
	pkg   *typedPkg
	tf    *typedFile
	fd    *ast.FuncDecl
	key   string // import path . [Type .] Name
	scope string // funcName(fd), `Type.Method` for methods
}

type taintEngine struct {
	tree      *typedTree
	tainted   map[types.Object]bool
	ret       map[string][]bool
	sinkPar   map[string]map[int]bool
	aliasVerb map[types.Object]string
	decls     []*fnDecl
	declByKey map[string]*fnDecl
	srcMember map[string]bool // `<import path>.<Type>.<member>`
	stats     taintStats
	quiet     bool // set while summarising wrappers: those walks must not inflate the floors' counters
}

var rawVerbSet = map[string]bool{}

func init() {
	for k, v := range rawContentVerbs {
		rawVerbSet[k] = v
	}
}

var filepathPathFuncs = map[string]bool{"Join": true, "Clean": true, "Dir": true, "Abs": true, "FromSlash": true, "ToSlash": true, "EvalSymlinks": true}

func isStringType(t types.Type) bool {
	if t == nil {
		return false
	}
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Kind() == types.String
}

func isStringOrUnknown(t types.Type) bool {
	if t == nil {
		return true
	}
	b, ok := t.Underlying().(*types.Basic)
	return ok && (b.Kind() == types.String || b.Kind() == types.Invalid)
}

func newTaintEngine(tree *typedTree) *taintEngine {
	e := &taintEngine{
		tree:      tree,
		tainted:   map[types.Object]bool{},
		ret:       map[string][]bool{},
		sinkPar:   map[string]map[int]bool{},
		aliasVerb: map[types.Object]string{},
		declByKey: map[string]*fnDecl{},
		srcMember: map[string]bool{},
	}
	e.stats.perPkg = map[string]int{}
	e.stats.multi = map[string]bool{}
	e.stats.prodNames = map[string]bool{}
	e.stats.wrapperNames = map[string]bool{}
	e.stats.crossUse = map[string]bool{}
	for _, s := range vaultSources {
		for _, m := range s.Allow {
			e.srcMember[s.ImportPath+"."+s.Type+"."+m] = true
		}
	}
	for _, p := range tree.pkgs {
		for i := range p.files {
			tf := &p.files[i]
			for _, d := range tf.ast.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fn := &fnDecl{pkg: p, tf: tf, fd: fd, scope: funcName(fd)}
				recv := ""
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					recv = recvTypeName(fd.Recv.List[0].Type) + "."
				}
				fn.key = p.importPath + "." + recv + fd.Name.Name
				e.decls = append(e.decls, fn)
				e.declByKey[fn.key] = fn
			}
		}
	}
	return e
}

// funcKeyOf is the summary key of a resolved function object.
func funcKeyOf(fn *types.Func) string {
	if fn.Pkg() == nil {
		return ""
	}
	recv := ""
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		t := sig.Recv().Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			recv = n.Obj().Name() + "."
		}
	}
	return fn.Pkg().Path() + "." + recv + fn.Name()
}

// selMember returns `<import path>.<Type>.<member>` for a selector resolved by
// go/types to a member of a named type, or "".
func selMember(info *types.Info, x *ast.SelectorExpr) string {
	sel := info.Selections[x]
	if sel == nil {
		return ""
	}
	t := sel.Recv()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return ""
	}
	return n.Obj().Pkg().Path() + "." + n.Obj().Name() + "." + x.Sel.Name
}

// calleeOf resolves a call's callee to its summary key, the callee package's
// import path and its name. Cross-package callees resolve by import path (the fake
// importer yields no function object for them), same-package and method callees by
// their object.
func calleeOf(info *types.Info, call *ast.CallExpr) (key, path, name string) {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		if fn, ok := info.Uses[f].(*types.Func); ok && fn.Pkg() != nil {
			return funcKeyOf(fn), fn.Pkg().Path(), fn.Name()
		}
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			if pn, ok := info.Uses[x].(*types.PkgName); ok {
				return pn.Imported().Path() + "." + f.Sel.Name, pn.Imported().Path(), f.Sel.Name
			}
		}
		if sel := info.Selections[f]; sel != nil {
			if fn, ok := sel.Obj().(*types.Func); ok && fn.Pkg() != nil {
				return funcKeyOf(fn), fn.Pkg().Path(), fn.Name()
			}
		}
	}
	return "", "", ""
}

// osVerbOf resolves a call's function expression to a raw os verb: `os.X` through
// ANY import alias (resolved by PkgName, not spelling), or a variable assigned from
// one (`w := os.WriteFile`, a package-level `var seam = os.WriteFile`).
func (e *taintEngine) osVerbOf(info *types.Info, fun ast.Expr) string {
	switch x := fun.(type) {
	case *ast.ParenExpr:
		return e.osVerbOf(info, x.X)
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if pn, ok := info.Uses[id].(*types.PkgName); ok && pn.Imported().Path() == "os" && rawVerbSet[x.Sel.Name] {
				return x.Sel.Name
			}
		}
	case *ast.Ident:
		if o := info.Uses[x]; o != nil {
			return e.aliasVerb[o]
		}
	}
	return ""
}

// isSourceExpr reports a typed source selector (field) or a method value call.
func (e *taintEngine) sourceMember(info *types.Info, x *ast.SelectorExpr, pkg *typedPkg) bool {
	m := selMember(info, x)
	if m == "" || !e.srcMember[m] {
		return false
	}
	if e.quiet {
		return true
	}
	e.stats.selectorHits++
	if i := strings.LastIndex(m, "."); i > 0 {
		// the owning package of the member's type
		owner := m
		for j := 0; j < 2; j++ {
			owner = owner[:strings.LastIndex(owner, ".")]
		}
		if owner != pkg.importPath {
			e.stats.crossUse[m+"@"+pkg.name] = true
		}
	}
	return true
}

func (e *taintEngine) objOf(info *types.Info, id *ast.Ident) types.Object {
	if o := info.Uses[id]; o != nil {
		return o
	}
	return info.Defs[id]
}

// exprTaint: is e a vault-derived string?
func (e *taintEngine) exprTaint(info *types.Info, pkg *typedPkg, ex ast.Expr) bool {
	switch x := ex.(type) {
	case *ast.Ident:
		o := e.objOf(info, x)
		return o != nil && e.tainted[o]
	case *ast.ParenExpr:
		return e.exprTaint(info, pkg, x.X)
	case *ast.BinaryExpr:
		return e.exprTaint(info, pkg, x.X) || e.exprTaint(info, pkg, x.Y)
	case *ast.SelectorExpr:
		return isStringType(info.TypeOf(x)) && e.sourceMember(info, x, pkg)
	case *ast.CallExpr:
		return e.callResultTaint(info, pkg, x, 0)
	}
	return false
}

// callResultTaint: is result idx of the call vault-derived?
func (e *taintEngine) callResultTaint(info *types.Info, pkg *typedPkg, call *ast.CallExpr, idx int) bool {
	if tv, ok := info.Types[call.Fun]; ok && tv.IsType() && len(call.Args) == 1 {
		return idx == 0 && e.exprTaint(info, pkg, call.Args[0]) // string(x)
	}
	if sx, ok := call.Fun.(*ast.SelectorExpr); ok && idx == 0 {
		if m := selMember(info, sx); m != "" && e.srcMember[m] {
			e.sourceMember(info, sx, pkg)
			return true
		}
	}
	key, path, name := calleeOf(info, call)
	if key == "" {
		return false
	}
	if path == "path/filepath" && filepathPathFuncs[name] {
		return idx == 0 && len(call.Args) > 0 && e.exprTaint(info, pkg, call.Args[0])
	}
	if rt := e.ret[key]; idx < len(rt) {
		return rt[idx]
	}
	return false
}

func (e *taintEngine) set(o types.Object) bool {
	if o == nil || e.tainted[o] {
		return false
	}
	e.tainted[o] = true
	return true
}

func (e *taintEngine) lhsObj(info *types.Info, x ast.Expr) types.Object {
	id, ok := x.(*ast.Ident)
	if !ok || id.Name == "_" {
		return nil
	}
	return e.objOf(info, id)
}

// seedLitParams seeds the vault-named string parameters of the func literals held
// by PACKAGE-LEVEL vars (`var seam = func(vaultRoot string) {...}`, the test-seam
// shape bindingScopes covers). They are API boundaries like a FuncDecl's.
func (e *taintEngine) seedLitParams() {
	for _, p := range e.tree.pkgs {
		if !(seedPkgs[p.name] || strings.HasSuffix(p.importPath, "/cmd/vp")) {
			continue
		}
		for i := range p.files {
			for _, d := range p.files[i].ast.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, sp := range gd.Specs {
					vs, ok := sp.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, v := range vs.Values {
						ast.Inspect(v, func(n ast.Node) bool {
							lit, ok := n.(*ast.FuncLit)
							if !ok || lit.Type.Params == nil {
								return true
							}
							for _, fl := range lit.Type.Params.List {
								for _, nm := range fl.Names {
									if o, ok := p.info.Defs[nm].(*types.Var); ok && isStringType(o.Type()) && seedParamNames[nm.Name] && e.set(o) {
										e.stats.seeds++
									}
								}
							}
							return false // outermost literals only, like outermostFuncLits
						})
					}
				}
			}
		}
	}
}

// seed marks the designated parameters.
func (e *taintEngine) seed() {
	e.seedLitParams()
	for _, d := range e.decls {
		fd := d.fd
		if fd.Type.Params == nil {
			continue
		}
		info := d.pkg.info
		seedPkg := seedPkgs[d.pkg.name] || strings.HasSuffix(d.pkg.importPath, "/cmd/vp")
		for _, fl := range fd.Type.Params.List {
			for _, nm := range fl.Names {
				o, ok := info.Defs[nm].(*types.Var)
				if !ok || !isStringType(o.Type()) {
					continue
				}
				pk := d.pkg.name + "." + d.scope
				if (seedPkg && seedParamNames[nm.Name]) ||
					(nm.Name == "root" && designatedRootFuncs[pk] != "") ||
					designatedParams[pk+"."+nm.Name] != "" {
					if e.set(o) {
						e.stats.seeds++
					}
				}
			}
		}
	}
}

// propagate runs the assignment/return/alias flow to a fixed point.
func (e *taintEngine) propagate() {
	for iter := 0; iter < 40; iter++ {
		changed := false
		for _, p := range e.tree.pkgs {
			info := p.info
			for i := range p.files {
				ast.Inspect(p.files[i].ast, func(n ast.Node) bool {
					switch s := n.(type) {
					case *ast.AssignStmt:
						if e.flowAssign(info, p, s.Lhs, s.Rhs) {
							changed = true
						}
					case *ast.ValueSpec:
						lhs := make([]ast.Expr, len(s.Names))
						for i, nm := range s.Names {
							lhs[i] = nm
						}
						if e.flowAssign(info, p, lhs, s.Values) {
							changed = true
						}
					}
					return true
				})
			}
		}
		for _, d := range e.decls {
			if e.summarizeReturns(d) {
				changed = true
			}
		}
		if !changed {
			break
		}
	}
}

func (e *taintEngine) flowAssign(info *types.Info, p *typedPkg, lhs, rhs []ast.Expr) bool {
	changed := false
	if len(lhs) == len(rhs) {
		for i := range rhs {
			o := e.lhsObj(info, lhs[i])
			if o == nil {
				continue
			}
			if v := e.osVerbOf(info, rhs[i]); v != "" && e.aliasVerb[o] == "" {
				e.aliasVerb[o] = v
				changed = true
			}
			if isStringOrUnknown(o.Type()) && e.exprTaint(info, p, rhs[i]) && e.set(o) {
				changed = true
			}
		}
		return changed
	}
	if len(rhs) == 1 {
		if call, ok := rhs[0].(*ast.CallExpr); ok {
			for i := range lhs {
				o := e.lhsObj(info, lhs[i])
				if o != nil && isStringOrUnknown(o.Type()) && e.callResultTaint(info, p, call, i) && e.set(o) {
					changed = true
				}
			}
		}
	}
	return changed
}

// summarizeReturns updates one function's per-result taint from its return
// statements and named results (not descending into function literals).
func (e *taintEngine) summarizeReturns(d *fnDecl) bool {
	fo, ok := d.pkg.info.Defs[d.fd.Name].(*types.Func)
	if !ok {
		return false
	}
	sig := fo.Type().(*types.Signature)
	rt := e.ret[d.key]
	if rt == nil {
		rt = make([]bool, sig.Results().Len())
	}
	changed := false
	ast.Inspect(d.fd.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		r, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		if len(r.Results) == 0 {
			for i := 0; i < sig.Results().Len() && i < len(rt); i++ {
				if e.tainted[sig.Results().At(i)] && !rt[i] {
					rt[i], changed = true, true
				}
			}
			return true
		}
		if len(r.Results) == 1 && sig.Results().Len() > 1 {
			if call, ok := r.Results[0].(*ast.CallExpr); ok { // return f() forwarding a tuple
				for i := range rt {
					if !rt[i] && e.callResultTaint(d.pkg.info, d.pkg, call, i) {
						rt[i], changed = true, true
					}
				}
			}
			return true
		}
		for i, x := range r.Results {
			if i < len(rt) && !rt[i] && e.exprTaint(d.pkg.info, d.pkg, x) {
				rt[i], changed = true, true
			}
		}
		return true
	})
	e.ret[d.key] = rt
	return changed
}

func (e *taintEngine) blessedDecl(d *fnDecl) bool {
	return blessedSinkPkgs[d.pkg.importPath] || blessedSinkFuncs[d.pkg.name+"."+d.scope]
}

func (e *taintEngine) blessedCallee(key, path, name string) bool {
	if blessedSinkPkgs[path] {
		return true
	}
	if d := e.declByKey[key]; d != nil {
		return e.blessedDecl(d)
	}
	return false
}

// wrapperSummaries computes, to a fixed point, which parameters of which functions
// reach the destination (first argument) of a raw os write, directly or through
// another wrapper.
//
// A wrapper rarely writes its parameter as-is (`q := path + ".tmp"`,
// `q := filepath.Join(dir, "x")`, a chain of locals), so each string parameter is
// treated as a SEED of the very same flow the direct taint uses (flowAssign and
// exprTaint, not a second propagation): the taint set is swapped for one holding
// only that parameter, the function body is propagated to a local fixed point, and
// the sinks are tested with exprTaint. Producer summaries (e.ret) are shared, so a
// path-preserving helper that is itself a summarised producer still counts; a
// helper that only preserves a PARAMETER's taint is not modelled (a stated limit).
func (e *taintEngine) wrapperSummaries() {
	saved, savedQuiet := e.tainted, e.quiet
	e.quiet = true
	defer func() { e.tainted, e.quiet = saved, savedQuiet }()
	for iter := 0; iter < 12; iter++ {
		changed := false
		for _, d := range e.decls {
			if e.blessedDecl(d) || d.fd.Type.Params == nil || !e.hasSinkCall(d) {
				continue
			}
			info := d.pkg.info
			idx := 0
			for _, fl := range d.fd.Type.Params.List {
				for _, nm := range fl.Names {
					pi := idx
					idx++
					o, ok := info.Defs[nm].(*types.Var)
					if !ok || !isStringType(o.Type()) {
						continue
					}
					if e.sinkPar[d.key][pi] || !e.paramReachesSink(d, o) {
						continue
					}
					if e.sinkPar[d.key] == nil {
						e.sinkPar[d.key] = map[int]bool{}
					}
					e.sinkPar[d.key][pi] = true
					changed = true
				}
				if len(fl.Names) == 0 {
					idx++
				}
			}
		}
		if !changed {
			break
		}
	}
}

// hasSinkCall is a cheap pre-filter: does the body call a raw os verb or a function
// already known to be a wrapper?
func (e *taintEngine) hasSinkCall(d *fnDecl) bool {
	found := false
	ast.Inspect(d.fd.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if e.osVerbOf(d.pkg.info, call.Fun) != "" {
			found = true
		} else if key, path, name := calleeOf(d.pkg.info, call); key != "" && key != d.key && len(e.sinkPar[key]) > 0 && !e.blessedCallee(key, path, name) {
			found = true
		}
		return !found
	})
	return found
}

// paramReachesSink seeds ONLY the parameter o, propagates the function body with
// the shared flow, and reports whether a derived value reaches the destination
// argument of a raw os write or of a known wrapper.
func (e *taintEngine) paramReachesSink(d *fnDecl, o *types.Var) bool {
	info := d.pkg.info
	e.tainted = map[types.Object]bool{o: true}
	for iter := 0; iter < 20; iter++ {
		changed := false
		ast.Inspect(d.fd.Body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				if e.flowAssign(info, d.pkg, s.Lhs, s.Rhs) {
					changed = true
				}
			case *ast.ValueSpec:
				lhs := make([]ast.Expr, len(s.Names))
				for i, nm := range s.Names {
					lhs[i] = nm
				}
				if e.flowAssign(info, d.pkg, lhs, s.Values) {
					changed = true
				}
			}
			return true
		})
		if !changed {
			break
		}
	}
	reached := false
	ast.Inspect(d.fd.Body, func(n ast.Node) bool {
		if reached {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if v := e.osVerbOf(info, call.Fun); v != "" {
			if v == "OpenFile" && !opensForWrite(call) {
				return true
			}
			if e.exprTaint(info, d.pkg, call.Args[0]) {
				reached = true
			}
			return true
		}
		key, path, name := calleeOf(info, call)
		if key == "" || key == d.key || e.blessedCallee(key, path, name) {
			return true
		}
		for pj := range e.sinkPar[key] {
			if pj < len(call.Args) && e.exprTaint(info, d.pkg, call.Args[pj]) {
				reached = true
				return true
			}
		}
		return true
	})
	return reached
}

// collectStats derives the floors' counters from the finished summaries.
func (e *taintEngine) collectStats() {
	for key, rt := range e.ret {
		d := e.declByKey[key]
		if d == nil {
			continue
		}
		name := d.pkg.name + "." + d.scope
		any, multi := false, false
		for i, b := range rt {
			if b {
				any = true
				if i >= 1 {
					multi = true
				}
			}
		}
		if any {
			e.stats.producers++
			e.stats.perPkg[d.pkg.name]++
			e.stats.prodNames[name] = true
		}
		if multi {
			e.stats.multi[name] = true
		}
	}
	for key, m := range e.sinkPar {
		if len(m) == 0 {
			continue
		}
		if d := e.declByKey[key]; d != nil {
			e.stats.wrappers++
			e.stats.wrapperNames[d.pkg.name+"."+d.scope] = true
		}
	}
}

// hits walks every top-level scope and reports raw writes (and wrapper calls) whose
// destination argument is vault-derived.
func (e *taintEngine) hits() []rawHit {
	var out []rawHit
	for _, p := range e.tree.pkgs {
		info := p.info
		for i := range p.files {
			tf := &p.files[i]
			type scope struct {
				name string
				body ast.Node
			}
			var scopes []scope
			for _, d := range tf.ast.Decls {
				switch x := d.(type) {
				case *ast.FuncDecl:
					if x.Body != nil {
						scopes = append(scopes, scope{funcName(x), x.Body})
					}
				case *ast.GenDecl:
					for _, sp := range x.Specs {
						if vs, ok := sp.(*ast.ValueSpec); ok {
							for j, v := range vs.Values {
								nm := "?"
								if j < len(vs.Names) {
									nm = vs.Names[j].Name
								}
								scopes = append(scopes, scope{nm, v})
							}
						}
					}
				}
			}
			for _, sc := range scopes {
				key := p.name + "." + sc.name
				ast.Inspect(sc.body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || len(call.Args) == 0 {
						return true
					}
					pos := fmt.Sprintf("%s:%d", filepath.ToSlash(tf.orig.path), p.fset.Position(call.Pos()).Line)
					if v := e.osVerbOf(info, call.Fun); v != "" {
						if v == "OpenFile" && !opensForWrite(call) {
							return true
						}
						e.stats.rawSinks++
						if e.exprTaint(info, p, call.Args[0]) {
							out = append(out, rawHit{key: key, pos: pos, detail: fmt.Sprintf(
								"os.%s writes a VAULT path (%s) with a raw primitive — %s", v, exprSketch(call.Args[0]), funnelBypassHazard)})
						}
						return true
					}
					ck, cpath, cname := calleeOf(info, call)
					if ck == "" || e.blessedCallee(ck, cpath, cname) {
						return true
					}
					pis := make([]int, 0, len(e.sinkPar[ck]))
					for pi := range e.sinkPar[ck] {
						pis = append(pis, pi)
					}
					sort.Ints(pis)
					for _, pi := range pis {
						if pi < len(call.Args) && e.exprTaint(info, p, call.Args[pi]) {
							out = append(out, rawHit{key: key, pos: pos, wrapper: true, callee: cpath + "." + cname, detail: fmt.Sprintf(
								"%s passes a VAULT path (%s) to a function whose parameter reaches a raw os.* write — %s",
								cname, exprSketch(call.Args[pi]), funnelBypassHazard)})
							break
						}
					}
					return true
				})
			}
		}
	}
	return out
}

// runTaint executes the whole analysis over a type-checked tree.
func runTaint(tree *typedTree) (*taintEngine, []rawHit) {
	e := newTaintEngine(tree)
	e.seed()
	e.propagate()
	e.wrapperSummaries()
	e.collectStats()
	return e, e.hits()
}
