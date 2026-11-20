// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultlock

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// These rows pin the under-test vault tripwire at every vaultlock function that
// creates or removes something. 🔴 NEVER THE REAL VAULT: each row registers a
// t.TempDir() as the "ambient" configured vault with surface.SetAmbientVaultRoot
// and restores it on cleanup.
//
// The assertion that discriminates is the ABSENCE of <root>/.vp-locks (and of the
// named lock file): "it panicked" alone passes when the seat sits after the
// MkdirAll. Every row has a control on a non-ambient root that DOES create the
// directory, so an absent directory cannot mean "the call never ran".

func setAmbient(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(surface.SetAmbientVaultRoot(root))
	return root
}

// setPairLegHook installs f between AcquirePair's two legs and returns the
// restore. Test-only: the setter is deliberately not part of the package.
func setPairLegHook(f func()) (restore func()) {
	pairLegHookMu.Lock()
	prev := pairLegHook
	pairLegHook = f
	pairLegHookMu.Unlock()
	return func() {
		pairLegHookMu.Lock()
		pairLegHook = prev
		pairLegHookMu.Unlock()
	}
}

// refused runs fn and returns the recovered tripwire text ("" if it did not panic).
func refused(fn func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	fn()
	return ""
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// entry is one refusal row: do runs the operation against root (named is a file
// path inside root, for the named-path openers) and releases anything it took.
type entry struct {
	name string
	do   func(root, named string)
	// lockDir is true when the operation creates <root>/.vp-locks; false for the
	// named-path openers, which create only the named file.
	lockDir bool
}

func entries() []entry {
	rel := func(release func() error, err error) {
		if err == nil && release != nil {
			_ = release()
		}
	}
	return []entry{
		{"acquireGCShared", func(r, _ string) {
			rl, err := acquireGCShared(r)
			rel(rl, err)
		}, true},
		{"acquireGCExclusiveBounded", func(r, _ string) {
			rl, _, err := acquireGCExclusiveBounded(r, 50*time.Millisecond)
			rel(rl, err)
		}, true},
		{"Acquire", func(r, _ string) { rel(Acquire(r, filepath.Join(r, "f.md"))) }, true},
		{"AcquirePair", func(r, _ string) { rel(AcquirePair(r, filepath.Join(r, "a.md"), filepath.Join(r, "b.md"))) }, true},
		{"TryAcquire", func(r, _ string) {
			rl, _, err := TryAcquire(r, filepath.Join(r, "f.md"))
			rel(rl, err)
		}, true},
		{"AcquireWithTimeout", func(r, _ string) {
			rel(AcquireWithTimeout(r, filepath.Join(r, "f.md"), 50*time.Millisecond))
		}, true},
		{"AcquireHeld", func(r, _ string) {
			h, err := AcquireHeld(r, r)
			if err == nil {
				_ = h.Release()
			}
		}, true},
		{"TryAcquireFile", func(_, n string) {
			rl, _, err := TryAcquireFile(n)
			rel(rl, err)
		}, false},
		{"AcquireFileWithTimeout", func(_, n string) {
			rel(AcquireFileWithTimeout(context.Background(), n, 50*time.Millisecond))
		}, false},
	}
}

// V1, V3, V5, V6, V7: each entry refuses an ambient root with nothing created,
// and creates what it should on a non-ambient root.
func TestAmbientGuard_EveryEntryRefusesBeforeCreatingAnything(t *testing.T) {
	for _, e := range entries() {
		t.Run(e.name, func(t *testing.T) {
			// Control first, on a root that is not ambient.
			ctl := t.TempDir()
			ctlNamed := filepath.Join(ctl, "named.lock")
			if msg := refused(func() { e.do(ctl, ctlNamed) }); msg != "" {
				t.Fatalf("control on a non-ambient root panicked: %s", msg)
			}
			if e.lockDir && !exists(filepath.Join(ctl, ".vp-locks")) {
				t.Fatal("control did not create .vp-locks: the row cannot discriminate")
			}
			if !e.lockDir && !exists(ctlNamed) {
				t.Fatal("control did not create the named lock file: the row cannot discriminate")
			}

			amb := setAmbient(t)
			named := filepath.Join(amb, "named.lock")
			msg := refused(func() { e.do(amb, named) })
			if !strings.Contains(msg, "CONFIGURED vault") {
				t.Fatalf("no tripwire refusal, got %q", msg)
			}
			if exists(filepath.Join(amb, ".vp-locks")) {
				t.Error(".vp-locks was created in the ambient vault before the refusal")
			}
			if exists(named) {
				t.Error("the named lock file was created in the ambient vault before the refusal")
			}
		})
	}
}

// V4: the sweep UNLINKS sidecars; a refusal must leave an ambient vault's sidecar.
func TestAmbientGuard_SweepRefusesAndRemovesNothing(t *testing.T) {
	mk := func(root string) string {
		dir := filepath.Join(root, ".vp-locks")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, strings.Repeat("a", 64)+".lock")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctl := t.TempDir()
	cp := mk(ctl)
	if _, err := SweepDeadLocks(ctl, false); err != nil {
		t.Fatal(err)
	}
	if exists(cp) {
		t.Fatal("control: the sweep did not remove the dead sidecar, so the row cannot discriminate")
	}

	amb := setAmbient(t)
	p := mk(amb)
	if msg := refused(func() { _, _ = SweepDeadLocks(amb, false) }); !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("no tripwire refusal, got %q", msg)
	}
	if !exists(p) {
		t.Error("the sweep removed a sidecar from the ambient vault")
	}
}

// V9 (F1): after a recovered refusal the .gc lock on the SAME ambient root is not
// held. .gc is pre-created so a stranded flock has a file to sit on.
func TestAmbientGuard_RefusalLeavesTheSameRootsGCLockFree(t *testing.T) {
	for _, e := range entries() {
		if !e.lockDir {
			continue
		}
		t.Run(e.name, func(t *testing.T) {
			amb := setAmbient(t)
			if err := os.MkdirAll(filepath.Join(amb, ".vp-locks"), 0o755); err != nil {
				t.Fatal(err)
			}
			gc := gcLockPath(amb)
			if err := os.WriteFile(gc, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if msg := refused(func() { e.do(amb, filepath.Join(amb, "n.lock")) }); !strings.Contains(msg, "CONFIGURED vault") {
				t.Fatalf("no tripwire refusal, got %q", msg)
			}
			ok, closeFn, err := flockTryExclusiveFile(gc)
			if err != nil || !ok {
				t.Fatalf("the .gc lock is still held after the refusal: ok=%v err=%v", ok, err)
			}
			_ = closeFn() // the contract: release and close right away
			ents, _ := os.ReadDir(filepath.Join(amb, ".vp-locks"))
			if len(ents) != 1 {
				t.Errorf(".vp-locks holds %d entries, want only the pre-made .gc", len(ents))
			}
		})
	}
}

// T-pair: a refusal on AcquirePair's SECOND leg (the filesystem changed between
// the legs, so the live-state verdict differs) releases the lower lock. Real
// guard, real filesystem; the hook only fixes the timing.
func TestAcquirePair_SecondLegRefusalReleasesTheLowerLock(t *testing.T) {
	setup := func(t *testing.T) (root, a, b, loKey string) {
		t.Helper()
		root = t.TempDir()
		a, b = filepath.Join(root, "a.md"), filepath.Join(root, "b.md")
		loKey = canonicalKey(a)
		if k := canonicalKey(b); k < loKey {
			loKey = k
		}
		return
	}

	t.Run("control: no hook, the pair succeeds", func(t *testing.T) {
		root, a, b, _ := setup(t)
		rl, err := AcquirePair(root, a, b)
		if err != nil {
			t.Fatal(err)
		}
		_ = rl()
	})

	t.Run("second leg refuses", func(t *testing.T) {
		root, a, b, loKey := setup(t)
		amb := setAmbient(t)
		target := filepath.Join(amb, "locks-sub")
		if err := os.MkdirAll(target, 0o755); err != nil { // the symlink target exists BEFORE the swap
			t.Fatal(err)
		}
		moved := filepath.Join(root, ".vp-locks.moved")
		t.Cleanup(setPairLegHook(func() {
			if err := os.Rename(filepath.Join(root, ".vp-locks"), moved); err != nil {
				t.Errorf("hook: %v", err)
				return
			}
			if err := os.Symlink(target, filepath.Join(root, ".vp-locks")); err != nil {
				t.Errorf("hook: %v", err)
			}
		}))
		msg := refused(func() { _, _ = AcquirePair(root, a, b) })
		if !strings.Contains(msg, "CONFIGURED vault") {
			t.Fatalf("no tripwire refusal on the second leg, got %q", msg)
		}
		lower := filepath.Join(moved, filepath.Base(lockFilePath(root, loKey)))
		// Precondition: leg 1 really ran and made its sidecar. Without it a refusal
		// on leg 1 (nothing held) would pass this test for the wrong reason.
		if !exists(lower) {
			t.Fatalf("the lower sidecar %s does not exist: leg 1 never ran", lower)
		}
		ok, closeFn, err := flockTryExclusiveFile(lower)
		if err != nil || !ok {
			t.Fatalf("the lower lock is still held after the second-leg refusal: ok=%v err=%v", ok, err)
		}
		_ = closeFn()
	})
}

// parseNonTest parses every non-test .go file of the package.
func parseNonTest(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.File{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = f
	}
	return fset, out
}

func isGuardCall(n ast.Node) bool {
	es, ok := n.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "surface" && sel.Sel.Name == "GuardTestPathWrite"
}

// osCalls lists the os.<Name> calls in fn whose name is in the shared mutator set.
func osCalls(fn *ast.FuncDecl) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" && testutil.FSMutatorCalls[sel.Sel.Name] {
			out = append(out, call)
		}
		return true
	})
	return out
}

func mentionsCreate(call *ast.CallExpr) bool {
	found := false
	for _, a := range call.Args {
		ast.Inspect(a, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "O_CREATE" {
				found = true
			}
			return true
		})
	}
	return found
}

// readOnlyOpens are the two functions that open an EXISTING lock file without
// O_CREATE and never create or remove anything. The exemption is by function
// name AND by call site: every mutator call in them must be os.OpenFile with no
// O_CREATE in its arguments.
var readOnlyOpens = map[string]bool{"flockTryExclusiveFile": true, "CommitLockFree": true}

// V8: every function that creates or removes something sits behind the tripwire
// as its FIRST statement. The one other exemption is openLockFileNoGC, a private
// post-seat helper, and only while it is reached solely through the seated
// boundary.
func TestFilesystemEffectsAreSeatedFirst(t *testing.T) {
	fset, files := parseNonTest(t)
	seated := map[string]bool{}
	funcs := map[string]*ast.FuncDecl{}
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			funcs[fn.Name.Name] = fn
			calls := osCalls(fn)
			if len(calls) == 0 {
				continue
			}
			first := len(fn.Body.List) > 0 && isGuardCall(fn.Body.List[0])
			switch {
			case first:
				seated[fn.Name.Name] = true
			case readOnlyOpens[fn.Name.Name]:
				for _, c := range calls {
					sel := c.Fun.(*ast.SelectorExpr)
					if sel.Sel.Name != "OpenFile" || mentionsCreate(c) {
						t.Errorf("%s: os.%s in %q, which is exempt only for read-only os.OpenFile without O_CREATE",
							fset.Position(c.Pos()), sel.Sel.Name, fn.Name.Name)
					}
				}
			case fn.Name.Name == "openLockFileNoGC":
				// proven below
			default:
				t.Errorf("%s: %q calls os.%s but its first statement is not surface.GuardTestPathWrite: a lock or vault mutation must be refused before its first effect",
					fset.Position(fn.Pos()), fn.Name.Name, calls[0].Fun.(*ast.SelectorExpr).Sel.Name)
			}
		}
	}

	// Anti-vacuity: the seats the design names must all have been found.
	for _, want := range []string{"acquireGCShared", "acquireGCExclusiveBounded", "sweepDeadLocks", "TryAcquireFile", "AcquireFileWithTimeout"} {
		if !seated[want] {
			t.Errorf("%s is not seated (or the walk no longer sees it)", want)
		}
	}

	// openLockFileNoGC: (a) every non-test call sits inside openLockFile, and
	// (b) openLockFile's first statement is the call to the seated acquireGCShared.
	for name, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "openLockFile" {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "openLockFileNoGC" {
						t.Errorf("%s (%s): %q calls openLockFileNoGC directly; it is a post-seat helper reachable only through openLockFile, so it would need its own seat with nothing held",
							fset.Position(call.Pos()), name, fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	olf := funcs["openLockFile"]
	if olf == nil || len(olf.Body.List) == 0 {
		t.Fatal("openLockFile not found")
	}
	firstIsGC := false
	ast.Inspect(olf.Body.List[0], func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "acquireGCShared" {
				firstIsGC = true
			}
		}
		return true
	})
	if !firstIsGC {
		t.Error("openLockFile's first statement is not the call to acquireGCShared: the post-seat helper openLockFileNoGC is no longer behind the seat")
	}
}

// V15: AcquirePair guards the lock dir and BOTH sidecar paths before its first
// leg acquires anything. A behavioural row cannot see this (leg 1's own seat
// refuses a stable ambient root anyway), so the source order is pinned here.
func TestAcquirePairGuardsBothPathsBeforeTheFirstLeg(t *testing.T) {
	_, files := parseNonTest(t)
	var pair *ast.FuncDecl
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "AcquirePair" {
				pair = fn
			}
		}
	}
	if pair == nil {
		t.Fatal("AcquirePair not found")
	}
	guards, firstAcquire := 0, token.NoPos
	ast.Inspect(pair, func(n ast.Node) bool {
		if isGuardCall(asStmt(n)) {
			if firstAcquire == token.NoPos || n.Pos() < firstAcquire {
				guards++
			}
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "acquireByKey" && (firstAcquire == token.NoPos || call.Pos() < firstAcquire) {
				firstAcquire = call.Pos()
			}
		}
		return true
	})
	// Inspect visits in source order, so the guards counted above are exactly
	// those that precede the first acquireByKey.
	if guards != 3 {
		t.Errorf("%d guard calls precede AcquirePair's first acquireByKey, want 3 (lock dir, lower and higher sidecar)", guards)
	}
}

func asStmt(n ast.Node) ast.Node {
	if es, ok := n.(*ast.ExprStmt); ok {
		return es
	}
	return nil
}

// V10: an ALIAS of the ambient root (a symlink to it) is refused too: the verdict
// resolves symlinks, so a root spelled differently is not a way around it.
func TestAmbientGuard_ASymlinkAliasOfTheAmbientRootIsRefused(t *testing.T) {
	amb := setAmbient(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(amb, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	msg := refused(func() {
		if rl, err := Acquire(alias, filepath.Join(alias, "f.md")); err == nil {
			_ = rl()
		}
	})
	if !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("an alias of the ambient root was not refused, got %q", msg)
	}
	if exists(filepath.Join(amb, ".vp-locks")) {
		t.Error(".vp-locks was created in the ambient vault through its alias")
	}
}
