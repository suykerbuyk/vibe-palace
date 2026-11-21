// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// This file is the ONE type-check helper the source-audit rules share
// (vault-dir-creation and vault-write-outside-funnel). It was extracted from
// vault_dir_creation.go so that identifier-to-object resolution, the part earlier
// syntactic versions of these rules were beaten on twice, exists once.
//
// # How it type-checks without the module
//
// Each directory+package is checked on its own with a FAKE importer: every import
// path yields an empty, complete package, so go/types can resolve local and
// package-level identifiers and which package an import name denotes without the
// imported packages' sources. That is what lets the rules run on a synthetic
// fixture tree with no go.mod.
//
// The one thing an empty package cannot do is say what a MEMBER of an imported
// package is, so `held.Root()` (held *vaultlock.Held) used inside internal/storage
// has no resolvable receiver. The fix is a STUB, not a second type-check: for
// exactly the types named in vaultSources (keyed by FULL import path), the importer
// returns a package whose named types carry the fields and methods COPIED FROM THE
// SCANNED DECLARATIONS (names, arity, and string/error/bool/int results are exact;
// every other type is an opaque placeholder). The stub cannot drift from the code
// it stands for: sourceaudit.vaultSources/STALE and /UNLISTED findings (see
// vault_taint.go) fail when a listed member is gone, or when a root-like member
// appears on a listed type that is neither allowed nor explicitly rejected.

// typeIssue is one UNEXPECTED type error, reduced to a version-stable fingerprint
// (see fingerprintIssue).
type typeIssue struct {
	fingerprint string
}

// typedPkg is one directory+package, type-checked.
type typedPkg struct {
	name       string
	importPath string
	dir        string
	fset       *token.FileSet
	files      []typedFile
	info       *types.Info
	issues     []typeIssue
	expected   int // type errors that are the fake importer's doing (members of fake imports)
}

// typedFile pairs an audit file with its re-parsed copy in the package's shared
// FileSet (go/types needs one FileSet per package; the audit's loader gives each
// file its own).
type typedFile struct {
	orig file
	ast  *ast.File
}

// typedTree is every non-test file of the audit, type-checked.
type typedTree struct {
	pkgs []*typedPkg
	// uses maps an identifier use, keyed by original file path and byte offset, to
	// its object (the shape vault-dir-creation's pureCtx consumes).
	uses identResolver
}

// identResolver maps an identifier use, keyed by file path and byte offset, to
// the object it refers to.
type identResolver map[string]types.Object

func resolverKey(f file, id *ast.Ident) string {
	return f.path + ":" + strconv.Itoa(f.fset.Position(id.Pos()).Offset)
}

func (r identResolver) lookup(f file, id *ast.Ident) types.Object { return r[resolverKey(f, id)] }

var (
	importPathCache = map[string]string{}
	modRootCache    = map[string][2]string{} // abs dir -> {module root dir, module path}
)

// importPathOf derives a scanned directory's FULL import path. With a go.mod above
// it (the real tree), that is the module path plus the directory's path below the
// module root. Without one (a synthetic fixture tree), it is the module path plus
// the path from the last internal/ or cmd/ segment, so a fixture laid out as
// internal/storage/x.go is the real internal/storage; any other directory is its
// own base name.
func importPathOf(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if p, ok := importPathCache[abs]; ok {
		return p
	}
	p := computeImportPath(abs)
	importPathCache[abs] = p
	return p
}

func computeImportPath(abs string) string {
	for d := abs; ; {
		if mr, ok := modRootCache[d]; ok {
			if mr[0] == "" {
				break
			}
			rel, _ := filepath.Rel(mr[0], abs)
			return joinImport(mr[1], rel)
		}
		if b, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil {
			mod := ""
			for _, line := range strings.Split(string(b), "\n") {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
					mod = strings.Trim(strings.TrimSpace(rest), `"`)
					break
				}
			}
			if mod != "" {
				modRootCache[d] = [2]string{d, mod}
				rel, _ := filepath.Rel(d, abs)
				return joinImport(mod, rel)
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	segs := strings.Split(filepath.ToSlash(abs), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i] == "internal" || segs[i] == "cmd" {
			return modulePath + "/" + strings.Join(segs[i:], "/")
		}
	}
	return segs[len(segs)-1]
}

func joinImport(mod, rel string) string {
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return mod
	}
	return mod + "/" + rel
}

// fakeName is the package name a fake import gets: the last path element, or the
// one before a major-version element (`.../jsonschema/v6`), minus a `.vN` suffix
// (`gopkg.in/yaml.v3`), so `yaml.Node` and `jsonschema.X` resolve to the import.
var majorSuffix = regexp.MustCompile(`^v[0-9]+$`)
var dotMajor = regexp.MustCompile(`\.v[0-9]+$`)

func fakeName(path string) string {
	parts := strings.Split(path, "/")
	name := parts[len(parts)-1]
	if majorSuffix.MatchString(name) && len(parts) > 1 {
		name = parts[len(parts)-2]
	}
	name = dotMajor.ReplaceAllString(name, "")
	return strings.ReplaceAll(name, "-", "_")
}

// stubImporter returns an empty, complete package per import path, except for the
// paths named in vaultSources, whose listed types come from the scanned
// declarations (see the file comment).
type stubImporter struct {
	seen  map[string]*types.Package
	decls map[string][]*ast.File // import path -> original ASTs of the scanned package
	specs map[string][]vaultSourceType
}

func (si stubImporter) Import(path string) (*types.Package, error) {
	if p, ok := si.seen[path]; ok {
		return p, nil
	}
	var p *types.Package
	if specs, ok := si.specs[path]; ok && len(si.decls[path]) > 0 {
		p = buildStubPackage(path, specs, si.decls[path])
	} else {
		p = types.NewPackage(path, fakeName(path))
	}
	p.MarkComplete()
	si.seen[path] = p
	return p, nil
}

// stubKind maps a declared result/field type identifier to a go/types type; every
// type the stub does not model is an opaque placeholder (Typ[Invalid]).
func stubKind(e ast.Expr, local map[string]types.Type) types.Type {
	switch t := e.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return types.Typ[types.String]
		case "bool":
			return types.Typ[types.Bool]
		case "int":
			return types.Typ[types.Int]
		case "error":
			return types.Universe.Lookup("error").Type()
		}
		if lt, ok := local[t.Name]; ok {
			return lt
		}
	case *ast.StarExpr:
		if lt := stubKind(t.X, local); lt != types.Typ[types.Invalid] {
			if _, isNamed := lt.(*types.Named); isNamed {
				return types.NewPointer(lt)
			}
		}
	}
	return types.Typ[types.Invalid]
}

// buildStubPackage builds one stub package from the scanned declarations of path.
// Every field and every method of a listed type is present (a composite literal or
// a call elsewhere must not become a type error), with exact arity; only names
// matter to the rules. Exported top-level functions whose results mention a listed
// type or string/error are included too, so `storage.NewVault(root)` is a typed
// *storage.Vault rather than an unresolved call.
func buildStubPackage(path string, specs []vaultSourceType, files []*ast.File) *types.Package {
	name := files[0].Name.Name
	pkg := types.NewPackage(path, name)
	local := map[string]types.Type{}
	named := map[string]*types.Named{}
	for _, sp := range specs {
		tn := types.NewTypeName(token.NoPos, pkg, sp.Type, nil)
		n := types.NewNamed(tn, types.NewStruct(nil, nil), nil)
		named[sp.Type] = n
		local[sp.Type] = n
		pkg.Scope().Insert(tn)
	}
	for _, f := range files {
		for _, d := range f.Decls {
			switch x := d.(type) {
			case *ast.GenDecl:
				for _, s := range x.Specs {
					ts, ok := s.(*ast.TypeSpec)
					if !ok {
						continue
					}
					n, listed := named[ts.Name.Name]
					if !listed {
						continue
					}
					var fields []*types.Var
					if st, ok := ts.Type.(*ast.StructType); ok {
						for _, fl := range st.Fields.List {
							ft := stubKind(fl.Type, local)
							if len(fl.Names) == 0 {
								continue // an embedded field: not modelled
							}
							for _, nm := range fl.Names {
								fields = append(fields, types.NewField(token.NoPos, pkg, nm.Name, ft, false))
							}
						}
					}
					n.SetUnderlying(types.NewStruct(fields, nil))
				}
			}
		}
	}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			sig := stubSignature(fd, pkg, local, named)
			if fd.Recv != nil {
				if tn := recvTypeName(fd.Recv.List[0].Type); named[tn] != nil {
					named[tn].AddMethod(types.NewFunc(token.NoPos, pkg, fd.Name.Name, sig))
				}
			} else if fd.Name.IsExported() && pkg.Scope().Lookup(fd.Name.Name) == nil {
				pkg.Scope().Insert(types.NewFunc(token.NoPos, pkg, fd.Name.Name, sig))
			}
		}
	}
	return pkg
}

func stubSignature(fd *ast.FuncDecl, pkg *types.Package, local map[string]types.Type, named map[string]*types.Named) *types.Signature {
	var params, results []*types.Var
	if fd.Type.Params != nil {
		for _, fl := range fd.Type.Params.List {
			n := len(fl.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				params = append(params, types.NewParam(token.NoPos, pkg, "", types.Typ[types.Invalid]))
			}
		}
	}
	if fd.Type.Results != nil {
		for _, fl := range fd.Type.Results.List {
			n := len(fl.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				results = append(results, types.NewParam(token.NoPos, pkg, "", stubKind(fl.Type, local)))
			}
		}
	}
	var recv *types.Var
	if fd.Recv != nil {
		if n := named[recvTypeName(fd.Recv.List[0].Type)]; n != nil {
			var rt types.Type = n
			if _, star := fd.Recv.List[0].Type.(*ast.StarExpr); star {
				rt = types.NewPointer(n)
			}
			recv = types.NewParam(token.NoPos, pkg, "", rt)
		}
	}
	variadic := false
	if fd.Type.Params != nil && len(fd.Type.Params.List) > 0 {
		if _, ok := fd.Type.Params.List[len(fd.Type.Params.List)-1].Type.(*ast.Ellipsis); ok {
			variadic = true
		}
	}
	if variadic && len(params) > 0 {
		last := params[len(params)-1]
		params[len(params)-1] = types.NewParam(token.NoPos, pkg, "", types.NewSlice(last.Type()))
	}
	return types.NewSignatureType(recv, nil, nil, types.NewTuple(params...), types.NewTuple(results...), variadic)
}

// typeCheckTree type-checks every non-test file, one package at a time. It is
// built ONCE per Run and shared by the rules.
func typeCheckTree(files []file) *typedTree {
	tree := &typedTree{uses: identResolver{}}
	type pkgKey struct{ dir, name string }
	groups := map[pkgKey][]file{}
	var order []pkgKey
	decls := map[string][]*ast.File{}
	specs := map[string][]vaultSourceType{}
	for _, s := range vaultSources {
		specs[s.ImportPath] = append(specs[s.ImportPath], s)
	}
	for _, f := range files {
		if f.isTest {
			continue
		}
		k := pkgKey{filepath.Dir(f.path), f.ast.Name.Name}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], f)
		ip := importPathOf(k.dir)
		if _, want := specs[ip]; want {
			decls[ip] = append(decls[ip], f.ast)
		}
	}
	for _, k := range order {
		ip := importPathOf(k.dir)
		pkg := &typedPkg{name: k.name, importPath: ip, dir: k.dir, fset: token.NewFileSet()}
		var asts []*ast.File
		for _, f := range groups[k] {
			a, err := parser.ParseFile(pkg.fset, f.path, nil, parser.SkipObjectResolution)
			if err != nil {
				continue // the loader already parsed it; an unreadable file just stays unresolved
			}
			asts = append(asts, a)
			pkg.files = append(pkg.files, typedFile{orig: f, ast: a})
		}
		pkg.info = &types.Info{
			Uses:       map[*ast.Ident]types.Object{},
			Defs:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
			Types:      map[ast.Expr]types.TypeAndValue{},
		}
		var errs []types.Error
		conf := types.Config{
			Importer: stubImporter{seen: map[string]*types.Package{}, decls: decls, specs: specs},
			Error: func(err error) {
				if te, ok := err.(types.Error); ok {
					errs = append(errs, te)
				}
			},
			DisableUnusedImportCheck: true,
		}
		_, _ = conf.Check(ip, pkg.fset, asts, pkg.info)
		byPath := map[string]file{}
		for _, tf := range pkg.files {
			byPath[tf.orig.path] = tf.orig
		}
		for id, obj := range pkg.info.Uses {
			pos := pkg.fset.Position(id.Pos())
			if of, ok := byPath[pos.Filename]; ok {
				tree.uses[of.path+":"+strconv.Itoa(pos.Offset)] = obj
			}
		}
		classifyIssues(pkg, errs)
		tree.pkgs = append(tree.pkgs, pkg)
	}
	return tree
}

// classifyIssues reduces each type error to a fingerprint built from STRUCTURE
// (what the AST has at the error's position), not from go/types' wording, which is
// not stable across Go versions (go.mod sets only a minimum). Errors on a member
// of a fake import are the importer's doing and are counted, not fingerprinted.
func classifyIssues(pkg *typedPkg, errs []types.Error) {
	for _, te := range errs {
		pos := pkg.fset.Position(te.Pos)
		var tf *typedFile
		for i := range pkg.files {
			if pkg.fset.File(pkg.files[i].ast.Pos()).Name() == pos.Filename {
				tf = &pkg.files[i]
				break
			}
		}
		if tf == nil {
			pkg.issues = append(pkg.issues, typeIssue{fingerprint: fingerprintIssue("other", pos.Filename, "?")})
			continue
		}
		cat, sym := classifyAt(pkg, tf.ast, te.Pos)
		if cat == catImportMember {
			pkg.expected++
			continue
		}
		pkg.issues = append(pkg.issues, typeIssue{fingerprint: fingerprintIssue(cat, pos.Filename, sym)})
	}
}

// The categories a type error is reduced to.
const (
	catImportMember = "import-member"        // `pkg.Name` where pkg is a (fake) import: expected
	catDeclConflict = "declaration-conflict" // a declaring identifier clashes (redeclared func, duplicate method)
	catNoMember     = "no-member"            // `x.Name` on a resolved non-package value
	catUndefined    = "undefined-name"       // a bare identifier that resolves to nothing
	catOther        = "other"
)

// fingerprintIssue is the pinned fingerprint format:
//
//	<category> | <file base name> | <symbol>
//
// where the symbol is the identifier at the error position (or the AST node kind
// when the position is not an identifier). Nothing of go/types' message wording,
// the line number, or the column is in it.
func fingerprintIssue(category, filename, symbol string) string {
	return category + "|" + filepath.Base(filename) + "|" + symbol
}

// classifyAt finds the innermost node at pos and classifies it from the AST and
// the object tables.
func classifyAt(pkg *typedPkg, f *ast.File, pos token.Pos) (category, symbol string) {
	var stack []ast.Node
	var found []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		if n.Pos() <= pos && pos < n.End() {
			found = append([]ast.Node(nil), stack...)
			return true
		}
		stack = stack[:len(stack)-1] // Inspect sends no nil for a node whose children are skipped
		return false
	})
	if len(found) == 0 {
		return catOther, "?"
	}
	n := found[len(found)-1]
	id, ok := n.(*ast.Ident)
	if !ok {
		// A duplicate function is reported at its `func` keyword (the FuncType): the
		// symbol is the declared name.
		if _, isType := n.(*ast.FuncType); isType && len(found) >= 2 {
			if fd, ok := found[len(found)-2].(*ast.FuncDecl); ok {
				return catDeclConflict, fd.Name.Name
			}
		}
		return catOther, nodeKind(n)
	}
	var parent ast.Node
	if len(found) >= 2 {
		parent = found[len(found)-2]
	}
	if sel, ok := parent.(*ast.SelectorExpr); ok && sel.Sel == id {
		if x, ok := sel.X.(*ast.Ident); ok {
			if _, isPkg := pkg.info.Uses[x].(*types.PkgName); isPkg {
				return catImportMember, id.Name
			}
		}
		return catNoMember, id.Name
	}
	if pkg.info.Defs[id] != nil || isDeclName(parent, id) {
		return catDeclConflict, id.Name
	}
	return catUndefined, id.Name
}

func nodeKind(n ast.Node) string { return strings.TrimPrefix(fmt.Sprintf("%T", n), "*ast.") }

// isDeclName reports whether id is the declaring name of its parent.
func isDeclName(parent ast.Node, id *ast.Ident) bool {
	switch p := parent.(type) {
	case *ast.FuncDecl:
		return p.Name == id
	case *ast.TypeSpec:
		return p.Name == id
	case *ast.ValueSpec:
		for _, n := range p.Names {
			if n == id {
				return true
			}
		}
	case *ast.Field:
		for _, n := range p.Names {
			if n == id {
				return true
			}
		}
	}
	return false
}

// sortedIssues returns a package's fingerprints, sorted (a multiset as a slice).
func (p *typedPkg) sortedIssues() []string {
	out := make([]string, 0, len(p.issues))
	for _, i := range p.issues {
		out = append(out, i.fingerprint)
	}
	sort.Strings(out)
	return out
}
