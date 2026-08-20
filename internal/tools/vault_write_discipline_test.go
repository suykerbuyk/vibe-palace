// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// This file now carries ONE rule: the unlocked vault read-modify-write tripwire
// (ADR-003 lock discipline). Its two former siblings — the atomicfile.Write
// root-argument rule and the no-raw-writes-to-a-vault-path rule — were absorbed
// on 2026-08-20 into internal/sourceaudit's cross-package vaultWriteFunnel,
// because their os.ReadDir(".") scope was the defect: a census found bypasses in
// 17 packages, and neither pin could see outside its own.
//
// This rule stayed. It is about LOCKING, not about the write funnel, and it has
// a positive control the funnel rule has no equivalent for.

// isVaultDerived reports whether an expression yields a vault path: a call on a
// storage.Vault path accessor, or a filepath.Join rooted at one — either
// directly on vault.Root, or on an identifier ALREADY known to hold a vault
// path.
//
// That last case is the one worth spelling out. `os.WriteFile(filepath.Join(
// tasksDir, "readme.md"), …)` is a hand-write of a vault file, and it is the
// most likely way one actually appears here: a one-line copy of the adjacent
// `os.MkdirAll(filepath.Join(tasksDir, "done"), …)` in the same function. A
// tripwire blind to it would miss the realistic addition while catching only
// the direct-accessor form.
func isVaultDerived(e ast.Expr, known map[string]bool) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	// vault.CommitMsgFile(slug), v.IterationsFile(p), vault.TasksDir(name).
	// Keyed on the accessor NAME, not merely the receiver: a Vault has many
	// methods returning tasks, stats and triples, and counting those as paths
	// would inflate this test's own anti-vacuity number with values that could
	// never be a write destination — an instrument lying about its coverage.
	if recv.Name == "vault" || recv.Name == "v" {
		n := sel.Sel.Name
		return strings.HasSuffix(n, "File") || strings.HasSuffix(n, "Dir") || strings.HasSuffix(n, "Path")
	}
	// filepath.Join(vault.Root, …) or filepath.Join(<known vault path>, …)
	if recv.Name == "filepath" && sel.Sel.Name == "Join" && len(call.Args) > 0 {
		if root, ok := call.Args[0].(*ast.SelectorExpr); ok && root.Sel.Name == "Root" {
			return true
		}
		if base, ok := call.Args[0].(*ast.Ident); ok && known[base.Name] {
			return true
		}
		// A nested join — filepath.Join(filepath.Join(tasksDir, …), …).
		if isVaultDerived(call.Args[0], known) {
			return true
		}
	}
	return false
}

// vaultDerivedIdents reports which identifiers in a file hold a vault-derived
// path, keyed by name.
//
// Iterated to a FIXED POINT rather than done in one sweep, because a path can be
// built in stages (dir := vault.TasksDir(p); sub := filepath.Join(dir, "done"))
// and a single source-order sweep would resolve those only when they happen to
// appear in dependency order. Order-dependent coverage is coverage that changes
// when someone moves a line.
//
// The returned slice names what was resolved ("file.go:ident"), so a caller can
// assert the tracker is still seeing the destinations it is supposed to see —
// the anti-vacuity number both rules below depend on.
func vaultDerivedIdents(fileName string, file *ast.File) (map[string]bool, []string) {
	vaultPath := map[string]bool{}
	var resolved []string
	for {
		added := 0
		ast.Inspect(file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range as.Rhs {
				if i >= len(as.Lhs) {
					break
				}
				lhs, ok := as.Lhs[i].(*ast.Ident)
				if !ok || lhs.Name == "_" || vaultPath[lhs.Name] {
					continue
				}
				if isVaultDerived(rhs, vaultPath) {
					vaultPath[lhs.Name] = true
					added++
					resolved = append(resolved, fileName+":"+lhs.Name)
				}
			}
			return true
		})
		if added == 0 {
			break
		}
	}
	return vaultPath, resolved
}

// unlockedVaultRMWHazard is the why for the rule below, and it is the surviving
// half of the deleted internal/tools Behavioral Note. The other half — never
// write a vault file with a raw os.* primitive — is enforced by
// TestNoHandWrittenVaultFilesInTools above. This one fires only on an offending
// call site, which is where a reader needs it.
const unlockedVaultRMWHazard = "this READS a vault path and WRITES THE SAME PATH BACK without holding the " +
	"advisory lock (ADR-003, internal/vaultlock). atomicfile.Write is atomic and it stamps the surface " +
	"version, but it takes NO lock — so it is only safe inside a critical section the caller already holds. " +
	"Between the read and the write another writer (CLI vs MCP, or a concurrent goroutine) can land an edit " +
	"that this write then silently overwrites: a lost update with no error anywhere, which is exactly how the " +
	"resume.md hole was born. Fix it one of three ways: route the whole operation through a storage method " +
	"(which locks for you), use vaultfs.Write with an expectedSha256 CAS guard, or — if the read-modify-write " +
	"genuinely belongs here — take vaultlock.Acquire(vault.Root, path) around BOTH the read and the write and " +
	"keep calling atomicfile.Write directly inside it (never storage.lockedWrite, which would re-acquire the " +
	"same key and deadlock: see internal/storage/tasks.go and sessions.go)"

// rmwFinding is one offending read-then-write pair.
type rmwFinding struct {
	site string // where the write is
	path string // the rendered path expression common to both
	read string // where the matching read is
}

// rmwStats are the anti-vacuity counters: what the walk actually inspected.
type rmwStats struct {
	readsInspected int // os.ReadFile/os.Open call sites seen at all
	vaultReads     int // ...of those, ones whose argument resolved to a vault path
	atomicWrites   int // atomicfile.Write call sites seen
	lockedScopes   int // function scopes holding a vaultlock.Acquire
}

func (s *rmwStats) add(o rmwStats) {
	s.readsInspected += o.readsInspected
	s.vaultReads += o.vaultReads
	s.atomicWrites += o.atomicWrites
	s.lockedScopes += o.lockedScopes
}

// rawReadFuncs are the os primitives that pull a file's contents in. os.ReadDir
// is absent on purpose: listing a directory is not the read half of a
// read-modify-write of a file.
var rawReadFuncs = map[string]bool{"ReadFile": true, "Open": true}

// findUnlockedVaultRMW reports read-modify-writes of a vault path that are not
// covered by an advisory lock.
//
// A pair counts when the SAME rendered path expression is both read with an os.*
// primitive and written with atomicfile.Write, and the read is visible from the
// write's scope (same function, or an enclosing one — the closure case, since
// every tool here is a handler closure returned by a top-level func). Scoping by
// the enclosing function rather than by the whole file is what keeps
// commit_msg_tools.go green: it reads the PROJECT-root commit.msg and writes the
// VAULT copy, which is two different paths and not this shape at all.
//
// A vaultlock.Acquire anywhere in an enclosing scope excuses the pair. That is
// the sanctioned idiom, not a loophole: an RMW holder takes the lock itself and
// then calls atomicfile.Write directly.
//
// Analysis is syntactic and biased to FALSE NEGATIVES, matching the sibling rule
// and internal/sourceaudit: a path laundered through a helper, a struct field, or
// a differently-spelled-but-equal expression is not tracked. This is a tripwire
// for the shape that would actually get written here, not a proof of absence.
func findUnlockedVaultRMW(fset *token.FileSet, fileName string, file *ast.File) ([]rmwFinding, rmwStats) {
	known, _ := vaultDerivedIdents(fileName, file)
	var findings []rmwFinding
	var st rmwStats

	render := func(e ast.Expr) string {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, e); err != nil {
			return ""
		}
		return buf.String()
	}
	isVaultPathExpr := func(e ast.Expr) bool {
		if id, ok := e.(*ast.Ident); ok && known[id.Name] {
			return true
		}
		return isVaultDerived(e, known)
	}
	site := func(n ast.Node) string {
		return fileName + ":" + strconv.Itoa(fset.Position(n.Pos()).Line)
	}

	// Reads seen so far, per scope: rendered path expression -> where.
	reads := map[ast.Node]map[string]string{}
	// Scopes known to hold the lock.
	locked := map[ast.Node]bool{}

	var stack []ast.Node
	scopes := func() []ast.Node {
		var out []ast.Node
		for _, n := range stack {
			switch n.(type) {
			case *ast.FuncDecl, *ast.FuncLit:
				out = append(out, n)
			}
		}
		return out
	}

	// ast.Inspect calls f(nil) once for every node whose children it walked, so
	// pushing on entry and popping on nil keeps the stack balanced.
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)

		switch node := n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			// Does this scope take the lock? Cheap to answer once, here.
			var holds bool
			ast.Inspect(n, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Acquire" {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "vaultlock" {
					holds = true
				}
				return true
			})
			if holds {
				locked[n] = true
				st.lockedScopes++
			}

		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || len(node.Args) == 0 {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}

			// The read half.
			if pkg.Name == "os" && rawReadFuncs[sel.Sel.Name] {
				st.readsInspected++
				if !isVaultPathExpr(node.Args[0]) {
					return true
				}
				st.vaultReads++
				encl := scopes()
				if len(encl) == 0 {
					return true
				}
				inner := encl[len(encl)-1]
				if reads[inner] == nil {
					reads[inner] = map[string]string{}
				}
				if key := render(node.Args[0]); key != "" {
					reads[inner][key] = site(node)
				}
				return true
			}

			// The write half. atomicfile.Write(vaultRoot, absPath, data...).
			if pkg.Name == "atomicfile" && sel.Sel.Name == "Write" && len(node.Args) >= 2 {
				st.atomicWrites++
				key := render(node.Args[1])
				if key == "" {
					return true
				}
				encl := scopes()
				for _, s := range encl {
					if locked[s] {
						return true // covered by an advisory lock
					}
				}
				// Walk innermost outward: a path derived in the enclosing func
				// and written in the handler closure is still the same path.
				for _, sc := range slices.Backward(encl) {
					if where, ok := reads[sc][key]; ok {
						findings = append(findings, rmwFinding{site: site(node), path: key, read: where})
						break
					}
				}
			}
		}
		return true
	})

	return findings, st
}

// TestNoUnlockedVaultRMWInTools forbids a read-modify-write of a vault path from
// this package that does not hold the advisory lock.
//
// This is the half of the deleted internal/tools Behavioral Note that the
// write-primitive conversion did not cover. TestNoHandWrittenVaultFilesInTools
// catches an RMW whose write is os.WriteFile/Create/OpenFile; this catches the
// one whose write is atomicfile.Write, which passes that rule (it is the correct
// primitive) and the surface-root rule (its first argument is a real vault root)
// while still taking no lock.
func TestNoUnlockedVaultRMWInTools(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	var total rmwStats

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		findings, st := findUnlockedVaultRMW(fset, name, file)
		total.add(st)
		for _, f := range findings {
			t.Errorf("%s: unlocked read-modify-write of vault path %s (read at %s) — %s",
				f.site, f.path, f.read, unlockedVaultRMWHazard)
		}
	}

	// 296: a no-result reads as green, and this rule has ZERO offending sites
	// today, so the walk has to prove it emitted a signal before its silence
	// means anything. Both halves of the pair matcher must be demonstrably live:
	// the read side must have resolved real vault reads (which also proves the
	// derivation tracker still recognises vault paths), and the write side must
	// have seen real atomicfile.Write destinations to judge.
	if total.vaultReads < 3 {
		t.Fatalf("resolved only %d vault-path read(s) of %d os read call site(s) — expected at least 3 "+
			"(the knowledge and iterations resource readers, and vp_get_iteration). The read half of the "+
			"matcher is not seeing this package's vault reads, so a pass here is vacuous",
			total.vaultReads, total.readsInspected)
	}
	if total.atomicWrites < 2 {
		t.Fatalf("inspected only %d atomicfile.Write call site(s) — expected at least 2 (the audit report and "+
			"the vault commit.msg copy). The write half of the matcher is not seeing this package's writes, "+
			"so a pass here is vacuous", total.atomicWrites)
	}
	t.Logf("inspected %d os read site(s), %d of them vault paths; %d atomicfile.Write site(s); %d locked scope(s)",
		total.readsInspected, total.vaultReads, total.atomicWrites, total.lockedScopes)
}

// TestUnlockedVaultRMWDetector_PositiveControl drives the detector over sources
// that DO contain the shape, and over the three near-misses that must stay green.
//
// The counters in TestNoUnlockedVaultRMWInTools prove the walk still sees the
// package's reads and writes; they cannot prove the PAIRING still works, because
// with no offending site in the tree that logic never runs. A clean-tree pass
// would then be green whether or not the rule functions — the trap this rule was
// written to avoid. These fixtures make the pairing itself execute on every run.
func TestUnlockedVaultRMWDetector_PositiveControl(t *testing.T) {
	cases := []struct {
		name string
		want int
		src  string
	}{
		{
			name: "unlocked RMW of a vault path is caught",
			want: 1,
			src: `package tools
func RegisterX(vault *storage.Vault) {
	h := func() error {
		path, err := vault.ResumeFile("p")
		if err != nil { return err }
		data, err := os.ReadFile(path)
		if err != nil { return err }
		return atomicfile.Write(vault.Root, path, append(data, 'x'))
	}
	_ = h
}`,
		},
		{
			name: "the same RMW under vaultlock.Acquire is fine",
			want: 0,
			src: `package tools
func RegisterX(vault *storage.Vault) {
	h := func() error {
		path, err := vault.ResumeFile("p")
		if err != nil { return err }
		release, err := vaultlock.Acquire(vault.Root, path)
		if err != nil { return err }
		defer release()
		data, err := os.ReadFile(path)
		if err != nil { return err }
		return atomicfile.Write(vault.Root, path, append(data, 'x'))
	}
	_ = h
}`,
		},
		{
			name: "reading a non-vault source and writing a vault copy is not an RMW",
			want: 0,
			src: `package tools
func RegisterX(vault *storage.Vault) {
	h := func() error {
		src := filepath.Join(args.ProjectPath, "commit.msg")
		data, err := os.ReadFile(src)
		if err != nil { return err }
		dest, err := vault.CommitMsgFile("p")
		if err != nil { return err }
		return atomicfile.Write(vault.Root, dest, data)
	}
	_ = h
}`,
		},
		{
			name: "a path derived outside the closure is still the same path",
			want: 1,
			src: `package tools
func RegisterX(vault *storage.Vault) {
	path, _ := vault.IterationsFile("p")
	h := func() error {
		data, err := os.ReadFile(path)
		if err != nil { return err }
		return atomicfile.Write(vault.Root, path, data)
	}
	_ = h
}`,
		},
		{
			name: "reading one vault file and writing a different one is out of scope",
			want: 0,
			src: `package tools
func RegisterX(vault *storage.Vault) {
	h := func() error {
		in, _ := vault.KnowledgeFile("p")
		out, _ := vault.IterationsFile("p")
		data, err := os.ReadFile(in)
		if err != nil { return err }
		return atomicfile.Write(vault.Root, out, data)
	}
	_ = h
}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fixture.go", tc.src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			findings, _ := findUnlockedVaultRMW(fset, "fixture.go", file)
			if len(findings) != tc.want {
				t.Fatalf("detector reported %d finding(s), want %d: %+v", len(findings), tc.want, findings)
			}
		})
	}
}
